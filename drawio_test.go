package main

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTempDrawio(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestHandleFileMarksDrawioKind(t *testing.T) {
	p := writeTempDrawio(t, "diagram.drawio", "<mxfile><diagram id=\"d1\" name=\"P1\"/></mxfile>")
	s := &webServer{appRoot: t.TempDir()}
	req := httptest.NewRequest("GET", "/api/file?path="+url.QueryEscape(p), nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	var resp fileResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Kind != "drawio" {
		t.Fatalf("kind = %q, want drawio", resp.Kind)
	}
	if !strings.HasPrefix(resp.RawURL, "/api/drawio?path=") {
		t.Fatalf("raw_url = %q, want /api/drawio?path=...", resp.RawURL)
	}
	if resp.Content != "" {
		t.Fatalf("expected no inline content for drawio, got %d bytes", len(resp.Content))
	}
}

func TestHandleDrawioServesViewerHTML(t *testing.T) {
	xml := "<mxfile><diagram id=\"d1\" name=\"페이지1\"><mxGraphModel/></diagram></mxfile>"
	p := writeTempDrawio(t, "flow.drawio", xml)
	s := &webServer{appRoot: t.TempDir()}
	req := httptest.NewRequest("GET", "/api/drawio?path="+url.QueryEscape(p), nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("content-type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "class=\"mxgraph\"") {
		t.Fatalf("missing mxgraph container: %s", body)
	}
	if !strings.Contains(body, "data-mxgraph=") {
		t.Fatalf("missing data-mxgraph attribute: %s", body)
	}
	if !strings.Contains(body, "viewer-static.min.js") {
		t.Fatalf("missing viewer script tag: %s", body)
	}
	// The diagram XML must be embedded HTML-escaped inside the attribute,
	// never as raw markup the browser would parse.
	if strings.Contains(body, "<mxfile") {
		t.Fatalf("raw unescaped XML leaked into page: %s", body)
	}
	if !strings.Contains(body, "&lt;mxfile") {
		t.Fatalf("escaped XML not found in page: %s", body)
	}
}

func TestHandleDrawioRejectsNonDrawio(t *testing.T) {
	p := writeTempDrawio(t, "notes.txt", "hello")
	s := &webServer{appRoot: t.TempDir()}
	req := httptest.NewRequest("GET", "/api/drawio?path="+url.QueryEscape(p), nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
