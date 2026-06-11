package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
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

	exprNeedle, termsNeedle := parseSearchExpr("needle")
	shallow := searchDirShallow(root, exprNeedle, termsNeedle)
	if len(shallow) != 1 || filepath.Base(shallow[0].Path) != "top.md" {
		t.Fatalf("shallow = %+v, want only top.md", shallow)
	}

	rec := searchTreeRecursive(root, exprNeedle, termsNeedle, 4000, false)
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

func TestReorderFavorites(t *testing.T) {
	dir := t.TempDir()
	s := &webServer{startDir: dir, appRoot: dir}
	if err := s.saveFavorites([]string{"/a", "/b", "/c"}); err != nil {
		t.Fatal(err)
	}
	// Reorder to c, a, b — and include an unknown path (ignored) + omit nothing.
	body := `{"order":["/c","/a","/zzz"]}`
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, httptest.NewRequest("POST", "/api/favorites/reorder", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	got := s.loadFavorites()
	// /c,/a from payload (unknown /zzz dropped), then omitted /b appended.
	want := []string{"/c", "/a", "/b"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("reorder = %v, want %v", got, want)
	}
}

func TestReorderFavoritesRejectsGET(t *testing.T) {
	s := &webServer{appRoot: t.TempDir()}
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, httptest.NewRequest("GET", "/api/favorites/reorder", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestSearchTreeDocsOnly(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "doc.md", "needle in markdown\n")
	writeTestFile(t, root, "code.go", "needle in code\n")
	expr, terms := parseSearchExpr("needle")

	docs := searchTreeRecursive(root, expr, terms, 4000, true)
	got := map[string]bool{}
	for _, r := range docs {
		got[filepath.Base(r.Path)] = true
	}
	if !got["doc.md"] || got["code.go"] {
		t.Errorf("docsOnly should find doc.md and skip code.go: %+v", got)
	}

	all := searchTreeRecursive(root, expr, terms, 4000, false)
	got = map[string]bool{}
	for _, r := range all {
		got[filepath.Base(r.Path)] = true
	}
	if !got["doc.md"] || !got["code.go"] {
		t.Errorf("allFiles should find both: %+v", got)
	}
}

func TestSearchExprAndAcrossLines(t *testing.T) {
	root := t.TempDir()
	// a and b on different lines -> AND must NOT match this file.
	writeTestFile(t, root, "split.md", "alpha here\nbeta there\n")
	// a and b on the SAME line -> AND matches.
	writeTestFile(t, root, "together.md", "alpha and beta same line\n")
	expr, terms := parseSearchExpr("alpha&beta")

	res := searchDirShallow(root, expr, terms)
	got := map[string]bool{}
	for _, r := range res {
		got[filepath.Base(r.Path)] = true
	}
	if got["split.md"] {
		t.Error("AND must not match keywords on different lines")
	}
	if !got["together.md"] {
		t.Error("AND must match keywords on the same line")
	}
}

// MatchedTerms feeds the folder-result color chips; it must list exactly the
// distinct terms present in the file (encounter order), regardless of operator.
func TestSearchMatchedTerms(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "both.md", "alpha and beta on one line\n")
	writeTestFile(t, root, "onlyalpha.md", "alpha alone here\n")

	// AND: only both.md qualifies, and it reports both terms.
	exprAnd, termsAnd := parseSearchExpr("alpha&beta")
	got := map[string][]string{}
	for _, r := range searchDirShallow(root, exprAnd, termsAnd) {
		got[filepath.Base(r.Path)] = r.MatchedTerms
	}
	if mt := got["both.md"]; len(mt) != 2 || mt[0] != "alpha" || mt[1] != "beta" {
		t.Errorf("both.md MatchedTerms = %v, want [alpha beta]", mt)
	}
	if _, ok := got["onlyalpha.md"]; ok {
		t.Error("onlyalpha.md should not qualify for alpha&beta")
	}

	// OR: a file matching only one branch reports only the present term.
	exprOr, termsOr := parseSearchExpr("alpha|beta")
	got2 := map[string][]string{}
	for _, r := range searchDirShallow(root, exprOr, termsOr) {
		got2[filepath.Base(r.Path)] = r.MatchedTerms
	}
	if mt := got2["onlyalpha.md"]; len(mt) != 1 || mt[0] != "alpha" {
		t.Errorf("onlyalpha.md MatchedTerms = %v, want [alpha]", mt)
	}
	if mt := got2["both.md"]; len(mt) != 2 {
		t.Errorf("both.md MatchedTerms = %v, want both terms", mt)
	}
}

// Count is the number of satisfying lines, not occurrences.
func TestSearchCountMatchingLines(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "multi.md", "needle one\nno match\nneedle two\nneedle three\n")
	expr, terms := parseSearchExpr("needle")
	res := searchDirShallow(root, expr, terms)
	if len(res) != 1 || res[0].Count != 3 {
		t.Fatalf("Count = %+v, want a single file with 3 matching lines", res)
	}
}
