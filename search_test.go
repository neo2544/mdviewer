package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newSearchServer(t *testing.T) (*webServer, string) {
	t.Helper()
	root := t.TempDir()
	writeTestFile(t, root, "alpha.md", "# Alpha\nHello world\nHello again\n")
	writeTestFile(t, root, "beta.md", "# Beta\nNo matches here\n")
	writeTestFile(t, root, "gamma.txt", "world of text\n")
	// binary should be skipped
	if err := os.WriteFile(filepath.Join(root, "blob.bin"),
		[]byte{0, 1, 2, 3, 'w', 'o', 'r', 'l', 'd'}, 0o644); err != nil {
		t.Fatal(err)
	}
	return &webServer{startDir: root, appRoot: root}, root
}

func TestSearchReturnsMatches(t *testing.T) {
	s, root := newSearchServer(t)
	req := httptest.NewRequest("GET",
		"/api/search?dir="+root+"&q=world", nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var results []searchResult
	if err := json.NewDecoder(rec.Body).Decode(&results); err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, r := range results {
		got[filepath.Base(r.Path)] = r.Count
	}
	if got["alpha.md"] != 1 || got["gamma.txt"] != 1 {
		t.Errorf("matches = %v, want alpha.md:1 + gamma.txt:1", got)
	}
	if _, ok := got["beta.md"]; ok {
		t.Errorf("beta.md should not be in results")
	}
	if _, ok := got["blob.bin"]; ok {
		t.Errorf("binary file should be skipped")
	}
}

func TestSearchMissingQuery(t *testing.T) {
	s, root := newSearchServer(t)
	req := httptest.NewRequest("GET", "/api/search?dir="+root, nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", rec.Code)
	}
}

func TestSearchCaseInsensitive(t *testing.T) {
	s, root := newSearchServer(t)
	req := httptest.NewRequest("GET",
		"/api/search?dir="+root+"&q=HELLO", nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	var results []searchResult
	if err := json.NewDecoder(rec.Body).Decode(&results); err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if filepath.Base(r.Path) == "alpha.md" && r.Count != 2 {
			t.Errorf("alpha.md count = %d, want 2 (case-insensitive)", r.Count)
		}
	}
}

func TestGitToWebURL(t *testing.T) {
	cases := map[string]string{
		"git@github.com:neo/repo.git":       "https://github.com/neo/repo",
		"git@github.com:neo/repo":           "https://github.com/neo/repo",
		"ssh://git@github.com/neo/repo.git": "https://github.com/neo/repo",
		"git://github.com/neo/repo.git":     "https://github.com/neo/repo",
		"https://github.com/neo/repo.git":   "https://github.com/neo/repo",
		"https://github.com/neo/repo":       "https://github.com/neo/repo",
		"/local/path":                       "",
		"":                                  "",
	}
	for in, want := range cases {
		if got := gitToWebURL(in); got != want {
			t.Errorf("gitToWebURL(%q) = %q, want %q", in, got, want)
		}
	}
}
