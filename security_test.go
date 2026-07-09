package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestWithSecurity verifies the local-only hardening middleware: it must
// block DNS-rebinding (spoofed Host) and browser CSRF (cross-origin
// state-changing requests) while leaving every legitimate access path
// untouched (same-origin app requests, plain navigations, the OS "open"
// flow that carries no origin signal).
func TestWithSecurity(t *testing.T) {
	s := &webServer{}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := s.withSecurity(next)

	type hdr struct{ k, v string }
	cases := []struct {
		name    string
		method  string
		host    string
		headers []hdr
		want    int
	}{
		// ── Host allow-list (anti DNS-rebinding) ──────────────────
		{"get loopback ipv4", http.MethodGet, "127.0.0.1:8421", nil, http.StatusOK},
		{"get localhost", http.MethodGet, "localhost:8421", nil, http.StatusOK},
		{"get loopback ipv6", http.MethodGet, "[::1]:8421", nil, http.StatusOK},
		{"get spoofed host blocked", http.MethodGet, "evil.example.com:8421", nil, http.StatusForbidden},
		{"get spoofed host no port blocked", http.MethodGet, "evil.example.com", nil, http.StatusForbidden},

		// ── Safe methods are never origin-checked (CORS already
		//    protects response confidentiality; Host check stops rebinding).
		{"get cross-site still ok (safe method)", http.MethodGet, "127.0.0.1:8421",
			[]hdr{{"Sec-Fetch-Site", "cross-site"}}, http.StatusOK},

		// ── CSRF guard on state-changing methods ──────────────────
		{"post same-origin allowed", http.MethodPost, "127.0.0.1:8421",
			[]hdr{{"Sec-Fetch-Site", "same-origin"}}, http.StatusOK},
		{"post user-initiated (none) allowed", http.MethodPost, "127.0.0.1:8421",
			[]hdr{{"Sec-Fetch-Site", "none"}}, http.StatusOK},
		{"post cross-site blocked", http.MethodPost, "127.0.0.1:8421",
			[]hdr{{"Sec-Fetch-Site", "cross-site"}}, http.StatusForbidden},
		{"post same-site blocked", http.MethodPost, "127.0.0.1:8421",
			[]hdr{{"Sec-Fetch-Site", "same-site"}}, http.StatusForbidden},
		{"post foreign origin blocked", http.MethodPost, "127.0.0.1:8421",
			[]hdr{{"Origin", "https://evil.example.com"}}, http.StatusForbidden},
		{"post loopback origin allowed", http.MethodPost, "127.0.0.1:8421",
			[]hdr{{"Origin", "http://127.0.0.1:8421"}}, http.StatusOK},
		{"post localhost origin allowed", http.MethodPost, "127.0.0.1:8421",
			[]hdr{{"Origin", "http://localhost:8421"}}, http.StatusOK},
		{"post no origin signal allowed (native client)", http.MethodPost, "127.0.0.1:8421",
			nil, http.StatusOK},

		// ── Host check runs before the CSRF guard ─────────────────
		{"post spoofed host blocked despite same-origin", http.MethodPost, "evil.example.com:8421",
			[]hdr{{"Sec-Fetch-Site", "same-origin"}}, http.StatusForbidden},

		// Other state-changing verbs use the same guard.
		{"delete cross-site blocked", http.MethodDelete, "127.0.0.1:8421",
			[]hdr{{"Sec-Fetch-Site", "cross-site"}}, http.StatusForbidden},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "http://"+tc.host+"/api/file", nil)
			req.Host = tc.host
			for _, h := range tc.headers {
				req.Header.Set(h.k, h.v)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("%s %s host=%q: got status %d, want %d",
					tc.method, "/api/file", tc.host, rec.Code, tc.want)
			}
		})
	}
}
