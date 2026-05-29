package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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

func TestSearchDirShallowVsRecursive(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "top.md", "needle here\n")
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, sub, "deep.md", "needle deep\n")
	// hidden + node_modules must be skipped by the recursive walk
	hidden := filepath.Join(root, ".git")
	_ = os.MkdirAll(hidden, 0o755)
	writeTestFile(t, hidden, "config.md", "needle hidden\n")
	nm := filepath.Join(root, "node_modules")
	_ = os.MkdirAll(nm, 0o755)
	writeTestFile(t, nm, "pkg.md", "needle vendored\n")

	shallow := searchDirShallow(root, "needle")
	if len(shallow) != 1 || filepath.Base(shallow[0].Path) != "top.md" {
		t.Fatalf("shallow = %+v, want only top.md", shallow)
	}

	rec := searchTreeRecursive(root, "needle", 4000)
	got := map[string]bool{}
	for _, r := range rec {
		got[filepath.Base(r.Path)] = true
	}
	if !got["top.md"] || !got["deep.md"] {
		t.Errorf("recursive should find top.md + deep.md: %+v", got)
	}
	if got["config.md"] || got["pkg.md"] {
		t.Errorf("recursive should skip .git/node_modules: %+v", got)
	}
}

func TestSearchScopeGitHTTP(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init").CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v (%s)", err, out)
	}
	writeTestFile(t, root, "rootdoc.md", "marker top\n")
	sub := filepath.Join(root, "docs")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, sub, "subdoc.md", "marker sub\n")
	s := &webServer{startDir: root, appRoot: root}

	// scope=git from the subdir resolves to repo root → finds both files.
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, httptest.NewRequest("GET",
		"/api/search?dir="+sub+"&q=marker&scope=git", nil))
	var resG []searchResult
	if err := json.NewDecoder(rec.Body).Decode(&resG); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, r := range resG {
		names[filepath.Base(r.Path)] = true
	}
	if !names["rootdoc.md"] || !names["subdoc.md"] {
		t.Errorf("scope=git from subdir should find both: %+v", names)
	}

	// Shallow scope on the subdir finds only subdoc.md.
	rec = httptest.NewRecorder()
	s.routes().ServeHTTP(rec, httptest.NewRequest("GET",
		"/api/search?dir="+sub+"&q=marker", nil))
	var resF []searchResult
	if err := json.NewDecoder(rec.Body).Decode(&resF); err != nil {
		t.Fatal(err)
	}
	if len(resF) != 1 || filepath.Base(resF[0].Path) != "subdoc.md" {
		t.Errorf("shallow scope = %+v, want only subdoc.md", resF)
	}
}
