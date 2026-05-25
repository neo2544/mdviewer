# graphify Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a right-side concept rail to MD Viewer's web mode that reads a `graphify-out/graph.json` produced by the standalone `graphify` CLI. Stage 1 (Consumer) reads/displays; Stage 2 (Builder) lets the user trigger graph build from the UI.

**Architecture:** Add a new `graph.go` (in-memory index of `graph.json` with mtime-based reload) and 3 HTTP routes for Stage 1. Add `graph_build.go` and 2 more routes (+SSE) for Stage 2. Frontend gets a new 240px `.graph-rail` panel in the existing CSS grid; the rail talks to the new routes when a file is selected.

**Tech Stack:** Go 1.22 (stdlib only — `net/http`, `os/exec`, `encoding/json`, `sync`), embedded HTML/CSS/JS inside `web.go`'s `webAppHTML` constant. Graph data is produced externally by `graphify` (Python, separate install).

**Spec:** `docs/superpowers/specs/2026-05-21-graphify-integration-design.md`

---

## File Structure

| File | Stage | Responsibility |
|---|---|---|
| `graph.go` (new) | 1 | `GraphIndex` type: load + index `graph.json`, lookups by file path and node id |
| `graph_test.go` (new) | 1 | Unit tests for `GraphIndex` |
| `graph_build.go` (new) | 2 | Spawn `graphify` subprocess, single-build mutex, SSE progress |
| `graph_build_test.go` (new) | 2 | Build orchestrator tests (uses stub `graphify` on PATH) |
| `web.go` (modify) | 1 + 2 | Add fields to `webServer`, register new routes, embed graph rail HTML/CSS/JS |

Tests live next to the file under test (Go convention). Each Stage 1 task ends with a green test run + commit. Stage 2 tasks gate on `graphify` being installable as a stub — the orchestrator never assumes a real graphify is present during tests.

---

## Stage 1 — Consumer

### Task 1: `GraphIndex.Load` parses minimal graph.json

**Files:**
- Create: `graph.go`
- Create: `graph_test.go`
- Create: `testdata/graph_simple.json`

- [ ] **Step 1: Create the test data fixture**

Create `testdata/graph_simple.json`:

```json
{
  "directed": false,
  "multigraph": false,
  "graph": {},
  "nodes": [
    {"id": "auth_session_token", "label": "Token", "file_type": "code", "source_file": "auth/session.go"},
    {"id": "auth_login_login",   "label": "Login", "file_type": "code", "source_file": "auth/login.go"},
    {"id": "docs_intro_token",   "label": "Token", "file_type": "document", "source_file": "docs/intro.md"}
  ],
  "links": [
    {"source": "auth_session_token", "target": "auth_login_login",  "relation": "calls"},
    {"source": "auth_session_token", "target": "docs_intro_token",  "relation": "references"}
  ]
}
```

The shape matches what `graphify.export.to_json` writes (NetworkX node-link with `links` key, not `edges`).

- [ ] **Step 2: Write the failing test**

Create `graph_test.go`:

```go
package main

import (
	"path/filepath"
	"testing"
)

func TestGraphIndexLoad(t *testing.T) {
	root, _ := filepath.Abs(".")
	g, err := LoadGraph("testdata/graph_simple.json", root)
	if err != nil {
		t.Fatalf("LoadGraph error: %v", err)
	}
	if got, want := len(g.nodes), 3; got != want {
		t.Errorf("node count = %d, want %d", got, want)
	}
	if g.nodes["auth_session_token"].Label != "Token" {
		t.Errorf("label mismatch for auth_session_token")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test -run TestGraphIndexLoad ./...`
Expected: FAIL — `LoadGraph` undefined.

- [ ] **Step 4: Implement `LoadGraph` and types**

Create `graph.go`:

```go
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Node is the subset of graphify node fields the viewer needs.
type Node struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	FileType   string `json:"file_type"`
	SourceFile string `json:"source_file"`
}

// FileRef is a node grouped by its source_file for the "Linked files" list.
type FileRef struct {
	Path     string `json:"path"`
	Label    string `json:"label"`
	FileType string `json:"file_type"`
}

// graphJSON mirrors the NetworkX node-link shape produced by
// graphify.export.to_json. We only deserialize the fields we need.
type graphJSON struct {
	Nodes []Node `json:"nodes"`
	Links []struct {
		Source string `json:"source"`
		Target string `json:"target"`
	} `json:"links"`
}

// GraphIndex is the in-memory query side of graph.json. All maps are
// populated at Load time and the struct is treated as immutable after
// — any update is a full replacement via Reload.
type GraphIndex struct {
	mu         sync.RWMutex
	nodes      map[string]Node
	byFile     map[string][]string
	neighbors  map[string][]string
	loadedAt   time.Time
	sourcePath string
	projectRoot string
}

// LoadGraph reads graph.json and returns a populated index. source_file
// values are normalised against projectRoot so that frontend queries
// using absolute paths always match.
func LoadGraph(jsonPath, projectRoot string) (*GraphIndex, error) {
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return nil, fmt.Errorf("read graph.json: %w", err)
	}
	var raw graphJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse graph.json: %w", err)
	}

	g := &GraphIndex{
		nodes:       make(map[string]Node, len(raw.Nodes)),
		byFile:      make(map[string][]string),
		neighbors:   make(map[string][]string),
		sourcePath:  jsonPath,
		projectRoot: projectRoot,
		loadedAt:    time.Now(),
	}
	for _, n := range raw.Nodes {
		n.SourceFile = g.normalisePath(n.SourceFile)
		g.nodes[n.ID] = n
		if n.SourceFile != "" {
			g.byFile[n.SourceFile] = append(g.byFile[n.SourceFile], n.ID)
		}
	}
	for _, e := range raw.Links {
		g.neighbors[e.Source] = append(g.neighbors[e.Source], e.Target)
		g.neighbors[e.Target] = append(g.neighbors[e.Target], e.Source)
	}
	return g, nil
}

// normalisePath returns an absolute path resolved against projectRoot.
// Relative paths from graphify are common (it stores paths relative to
// the scanned folder); absolute paths pass through.
func (g *GraphIndex) normalisePath(p string) string {
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(g.projectRoot, p)
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test -run TestGraphIndexLoad ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add graph.go graph_test.go testdata/graph_simple.json
git commit -m "feat(graph): add GraphIndex.Load for graphify graph.json"
```

---

### Task 2: `ConceptsInFile` returns nodes for an absolute path

**Files:**
- Modify: `graph.go`
- Modify: `graph_test.go`

- [ ] **Step 1: Add the failing test**

Append to `graph_test.go`:

```go
func TestConceptsInFile(t *testing.T) {
	root, _ := filepath.Abs(".")
	g, err := LoadGraph("testdata/graph_simple.json", root)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	abs := filepath.Join(root, "auth/session.go")
	nodes := g.ConceptsInFile(abs)
	if len(nodes) != 1 || nodes[0].ID != "auth_session_token" {
		t.Errorf("ConceptsInFile(session.go) = %+v, want [auth_session_token]", nodes)
	}
	if g.ConceptsInFile(filepath.Join(root, "nope.go")) == nil {
		t.Errorf("ConceptsInFile(missing) should return non-nil empty slice")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test -run TestConceptsInFile ./...`
Expected: FAIL — method undefined.

- [ ] **Step 3: Implement `ConceptsInFile`**

Append to `graph.go`:

```go
// ConceptsInFile returns nodes whose source_file equals absPath. Returns
// a non-nil empty slice when the file has no extracted concepts so JSON
// responses render as [] rather than null.
func (g *GraphIndex) ConceptsInFile(absPath string) []Node {
	g.mu.RLock()
	defer g.mu.RUnlock()
	key := filepath.Clean(absPath)
	ids := g.byFile[key]
	out := make([]Node, 0, len(ids))
	for _, id := range ids {
		out = append(out, g.nodes[id])
	}
	return out
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test -run TestConceptsInFile ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add graph.go graph_test.go
git commit -m "feat(graph): add ConceptsInFile lookup"
```

---

### Task 3: `FilesForConcept` returns the file list for a node

**Files:**
- Modify: `graph.go`
- Modify: `graph_test.go`

- [ ] **Step 1: Add the failing test**

Append to `graph_test.go`:

```go
func TestFilesForConcept(t *testing.T) {
	root, _ := filepath.Abs(".")
	g, err := LoadGraph("testdata/graph_simple.json", root)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	refs := g.FilesForConcept("auth_session_token")
	// auth_session_token's own file + 2 neighbours (auth_login, docs_intro).
	// Deduped by source_file; auth_session_token's own file shouldn't
	// appear in the result (we want OTHER files).
	gotPaths := map[string]bool{}
	for _, r := range refs {
		gotPaths[r.Path] = true
	}
	wantPaths := []string{
		filepath.Join(root, "auth/login.go"),
		filepath.Join(root, "docs/intro.md"),
	}
	for _, w := range wantPaths {
		if !gotPaths[w] {
			t.Errorf("FilesForConcept missing %s; got %v", w, refs)
		}
	}
	if gotPaths[filepath.Join(root, "auth/session.go")] {
		t.Errorf("FilesForConcept should not return the source file of the node itself")
	}
	if g.FilesForConcept("nonexistent") == nil {
		t.Errorf("missing node should return non-nil empty slice, not nil")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test -run TestFilesForConcept ./...`
Expected: FAIL.

- [ ] **Step 3: Implement `FilesForConcept`**

Append to `graph.go`:

```go
// FilesForConcept returns the OTHER files that contain the given node
// or any of its graph neighbours. Used by the "Linked files" panel —
// the file the node itself was extracted from is excluded so the panel
// only shows targets the user can actually jump to.
func (g *GraphIndex) FilesForConcept(nodeID string) []FileRef {
	g.mu.RLock()
	defer g.mu.RUnlock()
	base, ok := g.nodes[nodeID]
	if !ok {
		return []FileRef{}
	}
	selfFile := base.SourceFile
	seen := map[string]bool{selfFile: true}
	out := []FileRef{}
	add := func(n Node) {
		if n.SourceFile == "" || seen[n.SourceFile] {
			return
		}
		seen[n.SourceFile] = true
		out = append(out, FileRef{Path: n.SourceFile, Label: n.Label, FileType: n.FileType})
	}
	for _, nid := range g.neighbors[nodeID] {
		if n, ok := g.nodes[nid]; ok {
			add(n)
		}
	}
	return out
}

// NodeCount is read by /api/graph/status.
func (g *GraphIndex) NodeCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.nodes)
}

// LoadedAt is read by /api/graph/status.
func (g *GraphIndex) LoadedAt() time.Time {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.loadedAt
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test -run TestFilesForConcept ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add graph.go graph_test.go
git commit -m "feat(graph): add FilesForConcept lookup + NodeCount/LoadedAt"
```

---

### Task 4: `ReloadIfChanged` swaps the index when mtime advances

**Files:**
- Modify: `graph.go`
- Modify: `graph_test.go`

- [ ] **Step 1: Add the failing test**

Append to `graph_test.go`:

```go
func TestReloadIfChanged(t *testing.T) {
	root := t.TempDir()
	dst := filepath.Join(root, "graph.json")
	mustCopy := func(src string) {
		t.Helper()
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("read fixture: %v", err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			t.Fatalf("write copy: %v", err)
		}
	}
	mustCopy("testdata/graph_simple.json")

	g, err := LoadGraph(dst, root)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	if g.NodeCount() != 3 {
		t.Fatalf("initial node count = %d, want 3", g.NodeCount())
	}

	// Overwrite with a different fixture (one node) and bump mtime.
	smaller := `{"nodes":[{"id":"x","label":"X","file_type":"document","source_file":"only.md"}],"links":[]}`
	if err := os.WriteFile(dst, []byte(smaller), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(dst, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	changed, err := g.ReloadIfChanged()
	if err != nil {
		t.Fatalf("ReloadIfChanged: %v", err)
	}
	if !changed {
		t.Fatalf("expected reload to happen")
	}
	if g.NodeCount() != 1 {
		t.Errorf("after reload node count = %d, want 1", g.NodeCount())
	}

	// Second call without mtime change → no reload.
	changed2, err := g.ReloadIfChanged()
	if err != nil {
		t.Fatalf("ReloadIfChanged 2nd: %v", err)
	}
	if changed2 {
		t.Errorf("second reload should be no-op")
	}
}
```

Required import additions for the test file: `"os"`, `"time"`.

- [ ] **Step 2: Run to verify it fails**

Run: `go test -run TestReloadIfChanged ./...`
Expected: FAIL — method undefined.

- [ ] **Step 3: Implement `ReloadIfChanged`**

Append to `graph.go`:

```go
// fileMTime returns the modification time of the source path or zero if
// the file is unreadable.
func (g *GraphIndex) fileMTime() time.Time {
	fi, err := os.Stat(g.sourcePath)
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// ReloadIfChanged re-reads graph.json when its mtime is newer than the
// timestamp captured at the previous load. Returns (true, nil) if the
// in-memory index was replaced, (false, nil) otherwise.
//
// Reload is in-place — callers keep the same *GraphIndex pointer — so
// existing references in the webServer struct don't need re-wiring.
func (g *GraphIndex) ReloadIfChanged() (bool, error) {
	current := g.fileMTime()
	if current.IsZero() {
		return false, fmt.Errorf("stat %s: file unreadable", g.sourcePath)
	}
	g.mu.RLock()
	prev := g.loadedAt
	g.mu.RUnlock()
	if !current.After(prev) {
		return false, nil
	}
	fresh, err := LoadGraph(g.sourcePath, g.projectRoot)
	if err != nil {
		return false, err
	}
	g.mu.Lock()
	g.nodes = fresh.nodes
	g.byFile = fresh.byFile
	g.neighbors = fresh.neighbors
	g.loadedAt = current
	g.mu.Unlock()
	return true, nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test -run TestReloadIfChanged ./...`
Expected: PASS.

- [ ] **Step 5: Run the full graph test suite**

Run: `go test ./... -run TestGraph`
Expected: All 4 graph tests PASS.

- [ ] **Step 6: Commit**

```bash
git add graph.go graph_test.go
git commit -m "feat(graph): add ReloadIfChanged for mtime-based hot reload"
```

---

### Task 5: Wire `webServer` to the graph index

**Files:**
- Modify: `web.go:15-19` (struct fields)
- Modify: `web.go:125-135` (`runWebServer` startup load)

- [ ] **Step 1: Add fields to `webServer`**

Replace the existing struct declaration (currently `type webServer struct { startDir string; appRoot string }`):

```go
type webServer struct {
	startDir string
	appRoot  string

	graphMu   sync.RWMutex
	graph     *GraphIndex // nil = no graph available
	graphPath string      // <startDir>/graphify-out/graph.json
}
```

Add `"sync"` to the import block if not already present.

- [ ] **Step 2: Load graph at startup**

Replace `runWebServer` body (currently lines ~125-135) with:

```go
func runWebServer(startDir, appRoot, addr string) error {
	if addr == "" {
		addr = "127.0.0.1:8421"
	}
	server := &webServer{
		startDir:  startDir,
		appRoot:   appRoot,
		graphPath: filepath.Join(startDir, "graphify-out", "graph.json"),
	}
	server.tryLoadGraph()
	fmt.Printf("mdviewer web preview running at http://%s\n", addr)
	return http.ListenAndServe(addr, server.routes())
}

// tryLoadGraph is best-effort. A missing graph.json is the common case
// (user hasn't run graphify yet) and must not break startup.
func (s *webServer) tryLoadGraph() {
	g, err := LoadGraph(s.graphPath, s.startDir)
	if err != nil {
		return
	}
	s.graphMu.Lock()
	s.graph = g
	s.graphMu.Unlock()
}

// currentGraph returns the active index, refreshing it if graph.json's
// mtime has advanced since the last load. Returns nil when no graph
// exists (caller MUST nil-check).
func (s *webServer) currentGraph() *GraphIndex {
	s.graphMu.RLock()
	g := s.graph
	s.graphMu.RUnlock()
	if g == nil {
		// Re-attempt cold load — handles the "graph appeared after
		// startup" case (e.g. user ran graphify in another terminal).
		s.tryLoadGraph()
		s.graphMu.RLock()
		g = s.graph
		s.graphMu.RUnlock()
		return g
	}
	if _, err := g.ReloadIfChanged(); err != nil {
		// Treat reload error as "stale data is better than nothing".
		// Logging here would spam the console on every request when
		// the file is being rewritten by graphify; silently keep the
		// previous index.
	}
	return g
}
```

The `menubarApp` runner in `menubar.go` also constructs a `webServer` (see Step 3 below) — both call sites need `graphPath` set.

- [ ] **Step 3: Update the menubar entry point too**

In `menubar.go`, find the line that constructs `webServer` (currently `server := &webServer{ startDir: startDir, appRoot: appRoot }`) and replace with:

```go
server := &webServer{
    startDir:  startDir,
    appRoot:   appRoot,
    graphPath: filepath.Join(startDir, "graphify-out", "graph.json"),
}
server.tryLoadGraph()
```

- [ ] **Step 4: Build to verify everything still compiles**

Run: `go build ./...`
Expected: success, no warnings (the existing `ld: warning: ignoring duplicate libraries: '-lobjc'` is unrelated and acceptable).

- [ ] **Step 5: Commit**

```bash
git add web.go menubar.go
git commit -m "feat(graph): wire GraphIndex into webServer with cold-load fallback"
```

---

### Task 6: `GET /api/graph/status` endpoint

**Files:**
- Modify: `web.go:55-67` (routes)
- Modify: `web.go` append handler

- [ ] **Step 1: Register the route**

In `routes()`, insert after the `/favicon.ico` line:

```go
mux.HandleFunc("/api/graph/status", s.handleGraphStatus)
```

- [ ] **Step 2: Implement the handler**

Append to `web.go`:

```go
type graphStatusResponse struct {
	Available bool      `json:"available"`
	NodeCount int       `json:"node_count"`
	LoadedAt  time.Time `json:"loaded_at,omitempty"`
	Path      string    `json:"path"`
}

func (s *webServer) handleGraphStatus(w http.ResponseWriter, r *http.Request) {
	resp := graphStatusResponse{Path: s.graphPath}
	if g := s.currentGraph(); g != nil {
		resp.Available = true
		resp.NodeCount = g.NodeCount()
		resp.LoadedAt = g.LoadedAt()
	}
	s.writeJSON(w, http.StatusOK, resp)
}
```

`"time"` is already imported by `web.go`; verify before saving.

- [ ] **Step 3: Write the route test**

Create `web_graph_test.go`:

```go
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// newTestServer points a webServer at a temp dir, optionally copying the
// fixture graph.json into <tempdir>/graphify-out/.
func newTestServer(t *testing.T, withGraph bool) *webServer {
	t.Helper()
	root := t.TempDir()
	if withGraph {
		if err := os.MkdirAll(filepath.Join(root, "graphify-out"), 0o755); err != nil {
			t.Fatal(err)
		}
		src, err := os.ReadFile("testdata/graph_simple.json")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "graphify-out", "graph.json"), src, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := &webServer{
		startDir:  root,
		appRoot:   root,
		graphPath: filepath.Join(root, "graphify-out", "graph.json"),
	}
	s.tryLoadGraph()
	return s
}

func TestGraphStatusNoGraph(t *testing.T) {
	s := newTestServer(t, false)
	req := httptest.NewRequest("GET", "/api/graph/status", nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp graphStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Available {
		t.Errorf("Available = true, want false")
	}
}

func TestGraphStatusWithGraph(t *testing.T) {
	s := newTestServer(t, true)
	req := httptest.NewRequest("GET", "/api/graph/status", nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	var resp graphStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Available {
		t.Errorf("Available = false, want true")
	}
	if resp.NodeCount != 3 {
		t.Errorf("NodeCount = %d, want 3", resp.NodeCount)
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test -run TestGraphStatus ./...`
Expected: PASS (both cases).

- [ ] **Step 5: Commit**

```bash
git add web.go web_graph_test.go
git commit -m "feat(graph): GET /api/graph/status route"
```

---

### Task 7: `GET /api/graph/file?path=...` endpoint

**Files:**
- Modify: `web.go` routes + handler
- Modify: `web_graph_test.go`

- [ ] **Step 1: Register the route**

Insert in `routes()`:

```go
mux.HandleFunc("/api/graph/file", s.handleGraphFile)
```

- [ ] **Step 2: Implement the handler**

Append to `web.go`:

```go
func (s *webServer) handleGraphFile(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	g := s.currentGraph()
	if g == nil {
		// Match the "no concepts" empty response so the frontend has
		// a single render path; the rail decides what to show based
		// on /api/graph/status.
		s.writeJSON(w, http.StatusOK, []Node{})
		return
	}
	s.writeJSON(w, http.StatusOK, g.ConceptsInFile(abs))
}
```

- [ ] **Step 3: Add tests**

Append to `web_graph_test.go`:

```go
func TestGraphFileReturnsConcepts(t *testing.T) {
	s := newTestServer(t, true)
	abs := filepath.Join(s.startDir, "auth/session.go")
	req := httptest.NewRequest("GET", "/api/graph/file?path="+abs, nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got []Node
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "auth_session_token" {
		t.Errorf("got %+v", got)
	}
}

func TestGraphFileMissingPath(t *testing.T) {
	s := newTestServer(t, true)
	req := httptest.NewRequest("GET", "/api/graph/file", nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestGraphFileNoGraph(t *testing.T) {
	s := newTestServer(t, false)
	req := httptest.NewRequest("GET", "/api/graph/file?path=/nope", nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() == "null\n" {
		t.Errorf("response is null; should be []")
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test -run TestGraphFile ./...`
Expected: 3 PASS.

- [ ] **Step 5: Commit**

```bash
git add web.go web_graph_test.go
git commit -m "feat(graph): GET /api/graph/file?path=... route"
```

---

### Task 8: `GET /api/graph/concept?id=...` endpoint

**Files:**
- Modify: `web.go` routes + handler
- Modify: `web_graph_test.go`

- [ ] **Step 1: Register the route**

Insert in `routes()`:

```go
mux.HandleFunc("/api/graph/concept", s.handleGraphConcept)
```

- [ ] **Step 2: Implement the handler**

Append to `web.go`:

```go
func (s *webServer) handleGraphConcept(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	g := s.currentGraph()
	if g == nil {
		http.Error(w, "no graph available", http.StatusNotFound)
		return
	}
	g.mu.RLock()
	_, exists := g.nodes[id]
	g.mu.RUnlock()
	if !exists {
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}
	s.writeJSON(w, http.StatusOK, g.FilesForConcept(id))
}
```

- [ ] **Step 3: Add tests**

Append to `web_graph_test.go`:

```go
func TestGraphConceptReturnsFiles(t *testing.T) {
	s := newTestServer(t, true)
	req := httptest.NewRequest("GET", "/api/graph/concept?id=auth_session_token", nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got []FileRef
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("got %d files, want 2", len(got))
	}
}

func TestGraphConceptMissingNode(t *testing.T) {
	s := newTestServer(t, true)
	req := httptest.NewRequest("GET", "/api/graph/concept?id=nope", nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test -run TestGraphConcept ./...`
Expected: 2 PASS.

- [ ] **Step 5: Commit**

```bash
git add web.go web_graph_test.go
git commit -m "feat(graph): GET /api/graph/concept?id=... route"
```

---

### Task 9: Add the graph rail layout (CSS + DOM, no JS yet)

**Files:**
- Modify: `web.go` (HTML/CSS inside `webAppHTML`)

- [ ] **Step 1: Update the CSS grid for the app shell**

In `web.go` locate the `.app` rule (currently around line 573) and replace its grid-template-columns:

```css
:root {
  /* ...existing tokens... */
  --graph-rail-width: 240px;
}
.app {
  display: grid;
  grid-template-columns:
    var(--sidebar-width)
    var(--splitter-width)
    minmax(0, 1fr)
    var(--graph-rail-width);
  height: 100vh;
  gap: 0;
  padding: 18px;
  overflow: hidden;
}
.app.sidebar-collapsed {
  grid-template-columns:
    0px
    0px
    minmax(0, 1fr)
    var(--graph-rail-width);
}
.app.graph-rail-collapsed {
  grid-template-columns:
    var(--sidebar-width)
    var(--splitter-width)
    minmax(0, 1fr)
    0px;
}
.app.sidebar-collapsed.graph-rail-collapsed {
  grid-template-columns: 0px 0px minmax(0, 1fr) 0px;
}
```

- [ ] **Step 2: Add graph rail styles**

Add a new block (anywhere inside the `<style>`) before the `</style>` tag:

```css
.graph-rail {
  margin-left: 14px;
  padding: 14px 14px 18px;
  background: color-mix(in oklab, var(--panel) 92%, transparent);
  border: 1px solid var(--line);
  border-radius: 14px;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.app.graph-rail-collapsed .graph-rail {
  display: none;
}
.graph-section-title {
  font-size: 11px;
  text-transform: uppercase;
  letter-spacing: .18em;
  color: var(--muted);
}
.graph-chips {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.graph-chip {
  padding: 4px 10px;
  border-radius: 999px;
  border: 1px solid var(--line);
  background: var(--panel-2);
  color: var(--text);
  font-size: 12px;
  cursor: pointer;
}
.graph-chip:hover { border-color: var(--accent); }
.graph-chip.active { background: var(--accent); color: var(--bg); border-color: transparent; }
.graph-chip[data-file-type="document"] { border-left: 3px solid var(--accent); }
.graph-chip[data-file-type="code"]     { border-left: 3px solid var(--accent-2); }
.graph-chip[data-file-type="paper"]    { border-left: 3px solid color-mix(in oklab, var(--accent) 60%, white); }
.graph-chip[data-file-type="image"]    { border-left: 3px solid var(--muted); }
.graph-links {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.graph-link {
  padding: 6px 8px;
  border-radius: 6px;
  cursor: pointer;
  font-size: 13px;
  color: var(--text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.graph-link:hover { background: var(--panel-2); }
.graph-link .graph-link-path {
  display: block;
  font-size: 11px;
  color: var(--muted);
}
.graph-empty {
  color: var(--muted);
  font-size: 12px;
  font-style: italic;
}
```

- [ ] **Step 3: Insert the rail DOM after the main preview**

Find the `</main>` that closes `<main class="shell preview">` and insert immediately after it (before the closing `</div>` of `<div class="app">`):

```html
    <aside class="shell graph-rail" id="graphRail" aria-label="Graph concepts">
      <div>
        <div class="graph-section-title">Concepts in this file</div>
        <div class="graph-chips" id="graphChips">
          <div class="graph-empty" id="graphEmpty">Open a file to see extracted concepts.</div>
        </div>
      </div>
      <div>
        <div class="graph-section-title">Linked files</div>
        <div class="graph-links" id="graphLinks"></div>
      </div>
      <div class="graph-section-title" id="graphBanner" hidden></div>
    </aside>
```

- [ ] **Step 4: Build to check syntax**

Run: `go build ./...`
Expected: success.

- [ ] **Step 5: Smoke-test visually**

Run:
```bash
go build -o /tmp/mdv .
/tmp/mdv --web --port 8765 --root . &
SERVER_PID=$!
sleep 1
echo "open http://127.0.0.1:8765/"
# Open in browser manually, verify right rail shows "Concepts in this file"
# placeholder and the layout doesn't break.
kill $SERVER_PID
```

Confirm visually that the rail appears and doesn't overlap the preview. If broken, fix CSS before committing.

- [ ] **Step 6: Commit**

```bash
git add web.go
git commit -m "feat(graph): add graph rail layout (CSS + DOM skeleton)"
```

---

### Task 10: Wire rail JS — fetch status, populate chips, click → linked files, click link → open file

**Files:**
- Modify: `web.go` (JS inside `webAppHTML`)

- [ ] **Step 1: Add graph state to the global state object**

Find `const state = {` (around line 1971). Inside the object literal, add the new fields:

```js
const state = {
  cwd: "",
  // ...existing fields...
  selectedPath: "",
  // graph-rail state
  graphAvailable: false,
  graphConcepts: [],         // current file's concepts
  graphActiveNodeId: null,   // selected chip
  // ...rest unchanged...
};
```

- [ ] **Step 2: Add the graph functions**

Find a good location (just before the `selectFile` declaration is fine). Add:

```js
const graphChipsEl = document.getElementById("graphChips");
const graphLinksEl = document.getElementById("graphLinks");
const graphEmptyEl = document.getElementById("graphEmpty");
const graphBannerEl = document.getElementById("graphBanner");

async function refreshGraphStatus() {
  try {
    const r = await fetch("/api/graph/status");
    const data = await r.json();
    state.graphAvailable = !!data.available;
    if (!state.graphAvailable) {
      graphBannerEl.hidden = false;
      graphBannerEl.textContent =
        "Run `graphify .` in this folder to enable concept search.";
    } else {
      graphBannerEl.hidden = true;
    }
  } catch (err) {
    state.graphAvailable = false;
  }
}

async function loadConceptsForFile(absPath) {
  graphLinksEl.innerHTML = "";
  state.graphActiveNodeId = null;
  if (!state.graphAvailable || !absPath) {
    graphChipsEl.innerHTML = "";
    graphChipsEl.appendChild(graphEmptyEl);
    graphEmptyEl.textContent = state.graphAvailable
      ? "Open a file to see extracted concepts."
      : "";
    return;
  }
  let nodes = [];
  try {
    const r = await fetch("/api/graph/file?path=" + encodeURIComponent(absPath));
    nodes = await r.json();
  } catch (err) {
    nodes = [];
  }
  state.graphConcepts = nodes;
  graphChipsEl.innerHTML = "";
  if (!nodes.length) {
    const e = document.createElement("div");
    e.className = "graph-empty";
    e.textContent = "No concepts extracted from this file.";
    graphChipsEl.appendChild(e);
    return;
  }
  for (const n of nodes) {
    const btn = document.createElement("button");
    btn.className = "graph-chip";
    btn.type = "button";
    btn.dataset.id = n.id;
    btn.dataset.fileType = n.file_type || "";
    btn.textContent = n.label;
    btn.addEventListener("click", () => activateConcept(n.id, btn));
    graphChipsEl.appendChild(btn);
  }
}

async function activateConcept(nodeId, chipEl) {
  // Toggle visual state
  for (const c of graphChipsEl.querySelectorAll(".graph-chip")) {
    c.classList.toggle("active", c === chipEl);
  }
  state.graphActiveNodeId = nodeId;
  graphLinksEl.innerHTML = "";
  let refs = [];
  try {
    const r = await fetch("/api/graph/concept?id=" + encodeURIComponent(nodeId));
    if (!r.ok) throw new Error(String(r.status));
    refs = await r.json();
  } catch (err) {
    const e = document.createElement("div");
    e.className = "graph-empty";
    e.textContent = "Concept lookup failed.";
    graphLinksEl.appendChild(e);
    return;
  }
  if (!refs.length) {
    const e = document.createElement("div");
    e.className = "graph-empty";
    e.textContent = "No other files contain this concept.";
    graphLinksEl.appendChild(e);
    return;
  }
  for (const ref of refs) {
    const row = document.createElement("div");
    row.className = "graph-link";
    row.title = ref.path;
    const name = ref.path.split("/").pop();
    row.innerHTML =
      '<span>' + name + '</span>' +
      '<span class="graph-link-path">' + ref.path + '</span>';
    row.addEventListener("click", () => {
      selectFile(ref.path, { historyMode: "push" });
    });
    graphLinksEl.appendChild(row);
  }
}
```

- [ ] **Step 3: Hook `loadConceptsForFile` into `selectFile`**

Find `async function selectFile(path, options = {})` (around line 2797). At the END of the function, after the existing body finishes successfully, add:

```js
    // graph rail update — runs in parallel with the rest, never blocks
    // selection. Errors are swallowed by loadConceptsForFile itself.
    loadConceptsForFile(path);
```

- [ ] **Step 4: Call `refreshGraphStatus` at boot**

Find the application bootstrap (search for `window.addEventListener("load"` or the IIFE that calls `loadDir` first). Add a one-time call:

```js
    refreshGraphStatus();
```

Place it next to the existing initial `loadDir` / `loadFavorites` calls so it runs once at boot.

- [ ] **Step 5: Build and visually verify**

```bash
go build -o /tmp/mdv .
/tmp/mdv --web --port 8765 --root <some-folder-with-graphify-out> &
SERVER_PID=$!
sleep 1
# Open http://127.0.0.1:8765/, click a file, verify chips appear and
# clicking a chip lists linked files.
kill $SERVER_PID
```

If there is no `graphify-out/` directory, instead point `--root` at a folder where you've created one with the testdata fixture:

```bash
mkdir -p /tmp/mdv-test/graphify-out
cp testdata/graph_simple.json /tmp/mdv-test/graphify-out/graph.json
mkdir -p /tmp/mdv-test/auth /tmp/mdv-test/docs
echo "# Login" > /tmp/mdv-test/auth/login.go
echo "# Session" > /tmp/mdv-test/auth/session.go
echo "# Intro" > /tmp/mdv-test/docs/intro.md
/tmp/mdv --web --port 8765 --root /tmp/mdv-test
```

Confirm: chips appear on `session.go`, clicking the chip reveals `login.go` and `intro.md`, clicking those navigates.

- [ ] **Step 6: Commit**

```bash
git add web.go
git commit -m "feat(graph): wire rail JS — chips + linked files + click-to-jump"
```

---

### Task 11: Stage-1 manual acceptance + run full test suite

**Files:** none (verification only)

- [ ] **Step 1: Run all tests**

Run: `go test ./...`
Expected: ALL PASS.

- [ ] **Step 2: Build the final binary**

Run: `go build -o mdviewer .`
Expected: success, binary updated.

- [ ] **Step 3: Manual smoke test on this project**

```bash
# Use this very project as the graph corpus once a graph.json exists.
# If you don't have one, run `/graphify .` first or copy the fixture:
mkdir -p graphify-out
cp testdata/graph_simple.json graphify-out/graph.json   # only for smoke
./mdviewer --web --port 8765 --root .
```

Verify:
1. Right rail renders next to preview.
2. Selecting `auth/session.go` (fake path; substitute with any file in your fixture) shows chips.
3. Clicking a chip shows linked files.
4. Clicking a linked file navigates the preview.
5. Selecting a file with no concepts shows "No concepts extracted from this file."
6. Stop the server (`Ctrl-C`).
7. Delete `graphify-out/graph.json` and restart — banner shows the "Run graphify" message; chips area is empty.

- [ ] **Step 4: Clean up smoke-test data**

```bash
rm -f graphify-out/graph.json    # only if you copied the fixture there
```

- [ ] **Step 5: Commit any stray changes**

If steps 1-4 surfaced doc tweaks or fixes, commit them now. Otherwise no-op.

```bash
git status
# If clean, do nothing. If dirty, fix and commit.
```

Stage 1 is now mergeable on its own.

---

## Stage 2 — Builder

### Task 12: `BuildSession` orchestrates graphify CLI

**Files:**
- Create: `graph_build.go`
- Create: `graph_build_test.go`

- [ ] **Step 1: Write the failing test using a stub graphify on PATH**

Create `graph_build_test.go`:

```go
package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// installStubGraphify drops a tiny shell script named "graphify" into a
// temp dir and prepends that dir to PATH. The script prints two phases
// and writes a graph.json so the orchestrator can observe the success
// path end-to-end.
func installStubGraphify(t *testing.T, root string, exitCode int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stub graphify uses sh; skip on windows")
	}
	bindir := t.TempDir()
	script := `#!/bin/sh
echo "[graphify] detect: 3 files"
sleep 0.05
echo "[graphify] extract: working"
sleep 0.05
mkdir -p "` + root + `/graphify-out"
cat > "` + root + `/graphify-out/graph.json" <<'JSON'
{"nodes":[{"id":"x","label":"X","file_type":"document","source_file":"only.md"}],"links":[]}
JSON
exit ` + itoa(exitCode) + `
`
	path := filepath.Join(bindir, "graphify")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bindir+":"+os.Getenv("PATH"))
	t.Setenv("GEMINI_API_KEY", "stub-key")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	sign := ""
	if n < 0 {
		sign = "-"
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return sign + string(buf[i:])
}

func TestBuildSessionSuccess(t *testing.T) {
	root := t.TempDir()
	installStubGraphify(t, root, 0)

	mgr := newBuildManager()
	sess, err := mgr.Start(context.Background(), root)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	deadline := time.After(5 * time.Second)
	phases := []string{}
	for {
		select {
		case <-deadline:
			t.Fatalf("build did not finish in time; phases so far: %v", phases)
		case ev, ok := <-sess.Events():
			if !ok {
				// channel closed = done
				if sess.Err() != nil {
					t.Fatalf("session ended with err: %v", sess.Err())
				}
				if !sess.OK() {
					t.Fatalf("session not OK; phases: %v", phases)
				}
				return
			}
			phases = append(phases, ev.Phase)
		}
	}
}

func TestBuildSessionRejectsConcurrent(t *testing.T) {
	root := t.TempDir()
	installStubGraphify(t, root, 0)

	mgr := newBuildManager()
	if _, err := mgr.Start(context.Background(), root); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	_, err := mgr.Start(context.Background(), root)
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("expected 'already running' error, got %v", err)
	}
}

func TestBuildSessionRequiresAPIKey(t *testing.T) {
	root := t.TempDir()
	installStubGraphify(t, root, 0)
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")

	mgr := newBuildManager()
	_, err := mgr.Start(context.Background(), root)
	if err == nil {
		t.Fatalf("expected API key error")
	}
}

func TestBuildSessionRequiresGraphifyOnPath(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", "/dev/null")
	t.Setenv("GEMINI_API_KEY", "stub")

	mgr := newBuildManager()
	_, err := mgr.Start(context.Background(), root)
	if err == nil {
		t.Fatalf("expected 'not found' error")
	}
	// sanity: confirm exec.LookPath is what failed
	if _, e := exec.LookPath("graphify"); e == nil {
		t.Fatalf("graphify unexpectedly on PATH: %v", e)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test -run TestBuildSession ./...`
Expected: FAIL — `newBuildManager` undefined.

- [ ] **Step 3: Implement `graph_build.go`**

Create `graph_build.go`:

```go
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// BuildEvent is one progress line emitted to subscribers via SSE.
type BuildEvent struct {
	Phase   string    `json:"phase"`   // detect | extract | cluster | done | error
	Message string    `json:"message"` // human-readable line
	At      time.Time `json:"at"`
}

// BuildSession represents a single graphify invocation. Multiple HTTP
// clients can subscribe to the same session.
type BuildSession struct {
	id      string
	root    string
	startAt time.Time

	mu      sync.Mutex
	done    bool
	err     error
	events  []BuildEvent
	subs    []chan BuildEvent
}

func (s *BuildSession) ID() string         { return s.id }
func (s *BuildSession) Root() string       { return s.root }
func (s *BuildSession) Err() error         { s.mu.Lock(); defer s.mu.Unlock(); return s.err }
func (s *BuildSession) OK() bool           { s.mu.Lock(); defer s.mu.Unlock(); return s.done && s.err == nil }
func (s *BuildSession) Events() <-chan BuildEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan BuildEvent, 16)
	// Replay buffered events to late subscribers.
	for _, ev := range s.events {
		ch <- ev
	}
	if s.done {
		close(ch)
		return ch
	}
	s.subs = append(s.subs, ch)
	return ch
}

func (s *BuildSession) push(ev BuildEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
	for _, sub := range s.subs {
		select {
		case sub <- ev:
		default:
			// Drop on slow subscriber rather than block the build.
		}
	}
}

func (s *BuildSession) finish(err error) {
	s.mu.Lock()
	s.done = true
	s.err = err
	subs := s.subs
	s.subs = nil
	s.mu.Unlock()
	for _, sub := range subs {
		close(sub)
	}
}

// BuildManager owns the single-build mutex and tracks the most recent
// session.
type BuildManager struct {
	mu      sync.Mutex
	current *BuildSession
}

func newBuildManager() *BuildManager { return &BuildManager{} }

// Start launches a new build session. Returns an error if one is already
// running, graphify isn't on PATH, or no LLM API key is set.
func (m *BuildManager) Start(ctx context.Context, root string) (*BuildSession, error) {
	if os.Getenv("GEMINI_API_KEY") == "" && os.Getenv("GOOGLE_API_KEY") == "" {
		return nil, errors.New("GEMINI_API_KEY or GOOGLE_API_KEY must be set to run graphify")
	}
	bin, err := exec.LookPath("graphify")
	if err != nil {
		return nil, fmt.Errorf("graphify not found on PATH (try `pip install graphifyy`): %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current != nil {
		m.current.mu.Lock()
		running := !m.current.done
		m.current.mu.Unlock()
		if running {
			return nil, fmt.Errorf("a graphify build is already running (id=%s)", m.current.id)
		}
	}

	sess := &BuildSession{
		id:      fmt.Sprintf("%d", time.Now().UnixNano()),
		root:    root,
		startAt: time.Now(),
	}
	m.current = sess

	go runBuild(ctx, bin, sess)
	return sess, nil
}

// Current returns the most recent session (running or completed). May be nil.
func (m *BuildManager) Current() *BuildSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current
}

// runBuild is the actual subprocess driver. It streams stdout/stderr
// line-by-line and pushes a BuildEvent per non-empty line.
func runBuild(ctx context.Context, bin string, sess *BuildSession) {
	cmd := exec.CommandContext(ctx, bin, sess.root)
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		sess.push(BuildEvent{Phase: "error", Message: "spawn: " + err.Error(), At: time.Now()})
		sess.finish(err)
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go pumpLines(stdout, sess, &wg)
	go pumpLines(stderr, sess, &wg)
	wg.Wait()

	err := cmd.Wait()
	if err != nil {
		sess.push(BuildEvent{Phase: "error", Message: err.Error(), At: time.Now()})
	} else {
		sess.push(BuildEvent{Phase: "done", Message: "graphify exited 0", At: time.Now()})
	}
	sess.finish(err)
}

// phaseFromLine maps a log line to one of detect | extract | cluster |
// report. The CLI prefixes its own phase names; we look for substrings.
func phaseFromLine(line string) string {
	switch {
	case contains(line, "detect"):
		return "detect"
	case contains(line, "extract"):
		return "extract"
	case contains(line, "cluster") || contains(line, "communit"):
		return "cluster"
	case contains(line, "report") || contains(line, "viz"):
		return "report"
	default:
		return "log"
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func pumpLines(r io.Reader, sess *BuildSession, wg *sync.WaitGroup) {
	defer wg.Done()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		sess.push(BuildEvent{
			Phase:   phaseFromLine(line),
			Message: line,
			At:      time.Now(),
		})
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test -run TestBuildSession ./...`
Expected: 4 PASS.

- [ ] **Step 5: Commit**

```bash
git add graph_build.go graph_build_test.go
git commit -m "feat(graph): BuildManager + BuildSession orchestrator with single-build lock"
```

---

### Task 13: `POST /api/graph/build` + SSE `GET /api/graph/build/status`

**Files:**
- Modify: `web.go` (routes, struct field, handlers)
- Modify: `web_graph_test.go`

- [ ] **Step 1: Add the manager to `webServer`**

Replace the `webServer` struct (extending what Task 5 set up):

```go
type webServer struct {
	startDir string
	appRoot  string

	graphMu      sync.RWMutex
	graph        *GraphIndex
	graphPath    string

	buildManager *BuildManager
}
```

In `runWebServer` and the `menubar.go` server constructor, add:

```go
server.buildManager = newBuildManager()
```

right after the existing `server.tryLoadGraph()` line.

- [ ] **Step 2: Register the two new routes**

Append to `routes()`:

```go
mux.HandleFunc("/api/graph/build", s.handleGraphBuild)
mux.HandleFunc("/api/graph/build/status", s.handleGraphBuildStatus)
```

- [ ] **Step 3: Implement the build trigger**

Append to `web.go`:

```go
func (s *webServer) handleGraphBuild(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sess, err := s.buildManager.Start(r.Context(), s.startDir)
	if err != nil {
		// "already running" → 409; everything else (no PATH, no key) → 503
		if strings.Contains(err.Error(), "already running") {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	s.writeJSON(w, http.StatusAccepted, map[string]string{"job_id": sess.ID()})
}
```

Add `"strings"` to imports if not present.

- [ ] **Step 4: Implement the SSE status handler**

Append to `web.go`:

```go
func (s *webServer) handleGraphBuildStatus(w http.ResponseWriter, r *http.Request) {
	sess := s.buildManager.Current()
	if sess == nil {
		http.Error(w, "no build sessions", http.StatusNotFound)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	encoder := json.NewEncoder(w)
	for ev := range sess.Events() {
		// SSE framing: `data: <json>\n\n`
		_, _ = io.WriteString(w, "data: ")
		_ = encoder.Encode(ev) // Encode writes the trailing newline
		_, _ = io.WriteString(w, "\n")
		flusher.Flush()
	}

	// Final event when the channel closes — tells the client to stop.
	final := map[string]any{
		"phase":   "closed",
		"ok":      sess.OK(),
		"message": "",
	}
	if e := sess.Err(); e != nil {
		final["message"] = e.Error()
	}
	_, _ = io.WriteString(w, "data: ")
	_ = encoder.Encode(final)
	_, _ = io.WriteString(w, "\n")
	flusher.Flush()

	// After a successful build, force a re-load of the graph index so
	// subsequent /api/graph/* requests see the new data without waiting
	// for the mtime stat on the next file selection.
	if sess.OK() {
		s.tryLoadGraph()
	}
}
```

Add `"io"` to imports if not present (it likely already is for SSE).

- [ ] **Step 5: Add tests**

Append to `web_graph_test.go`:

```go
func TestGraphBuildMethodNotAllowed(t *testing.T) {
	s := newTestServer(t, false)
	s.buildManager = newBuildManager()
	req := httptest.NewRequest("GET", "/api/graph/build", nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestGraphBuildMissingAPIKey(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	s := newTestServer(t, false)
	s.buildManager = newBuildManager()
	req := httptest.NewRequest("POST", "/api/graph/build", nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}
```

- [ ] **Step 6: Run tests**

Run: `go test -run TestGraphBuild ./...`
Expected: 2 PASS.

- [ ] **Step 7: Commit**

```bash
git add web.go web_graph_test.go menubar.go
git commit -m "feat(graph): POST /api/graph/build + SSE status endpoint"
```

---

### Task 14: Frontend — Build button + SSE progress display

**Files:**
- Modify: `web.go` (HTML, CSS, JS inside `webAppHTML`)

- [ ] **Step 1: Add CSS for the build UI**

Inside the `<style>` block, append:

```css
.graph-build {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-top: 6px;
}
.graph-build-btn {
  padding: 6px 12px;
  border: 1px solid var(--accent);
  background: transparent;
  color: var(--accent);
  border-radius: 8px;
  cursor: pointer;
  font-size: 12px;
}
.graph-build-btn:disabled { opacity: 0.5; cursor: progress; }
.graph-build-log {
  max-height: 120px;
  overflow-y: auto;
  font-family: ui-monospace, monospace;
  font-size: 11px;
  color: var(--muted);
  background: var(--code);
  padding: 6px 8px;
  border-radius: 6px;
  white-space: pre-wrap;
}
.graph-build-log[hidden] { display: none; }
```

- [ ] **Step 2: Add the build UI DOM**

In the `#graphRail` `<aside>` body (created in Task 9), insert before the closing `</aside>`:

```html
      <div class="graph-build" id="graphBuildBox" hidden>
        <button class="graph-build-btn" id="graphBuildBtn" type="button">Build graph</button>
        <div class="graph-build-log" id="graphBuildLog" hidden></div>
      </div>
```

- [ ] **Step 3: Wire the JS**

Append to the JS block (near the other graph functions):

```js
const graphBuildBoxEl = document.getElementById("graphBuildBox");
const graphBuildBtnEl = document.getElementById("graphBuildBtn");
const graphBuildLogEl = document.getElementById("graphBuildLog");

graphBuildBtnEl.addEventListener("click", startGraphBuild);

async function startGraphBuild() {
  graphBuildBtnEl.disabled = true;
  graphBuildLogEl.hidden = false;
  graphBuildLogEl.textContent = "Starting graphify…\n";
  let resp;
  try {
    resp = await fetch("/api/graph/build", { method: "POST" });
  } catch (err) {
    graphBuildLogEl.textContent += "network error: " + err + "\n";
    graphBuildBtnEl.disabled = false;
    return;
  }
  if (!resp.ok) {
    const msg = await resp.text();
    graphBuildLogEl.textContent += "error: " + msg + "\n";
    graphBuildBtnEl.disabled = false;
    return;
  }
  // Open SSE
  const src = new EventSource("/api/graph/build/status");
  src.onmessage = (ev) => {
    try {
      const data = JSON.parse(ev.data);
      if (data.phase === "closed") {
        graphBuildLogEl.textContent += data.ok ? "done.\n" : "failed: " + data.message + "\n";
        src.close();
        graphBuildBtnEl.disabled = false;
        if (data.ok) {
          // Refresh status + re-fetch current file's concepts.
          refreshGraphStatus().then(() => {
            if (state.selectedPath) loadConceptsForFile(state.selectedPath);
          });
        }
        return;
      }
      const ts = data.at ? data.at.replace("T", " ").slice(11, 19) : "";
      graphBuildLogEl.textContent +=
        "[" + ts + "] " + (data.phase || "log") + ": " + (data.message || "") + "\n";
      graphBuildLogEl.scrollTop = graphBuildLogEl.scrollHeight;
    } catch (e) {
      // ignore parse errors
    }
  };
  src.onerror = () => {
    graphBuildLogEl.textContent += "stream closed.\n";
    src.close();
    graphBuildBtnEl.disabled = false;
  };
}

// Override the Task 10 refreshGraphStatus so the build box is shown
// when the graph is absent.
const _refreshGraphStatusBase = refreshGraphStatus;
refreshGraphStatus = async function () {
  await _refreshGraphStatusBase();
  graphBuildBoxEl.hidden = state.graphAvailable;
};
```

Wait — the above redefinition trick won't work with `const` declared by Task 10. Replace the original Task 10 declaration of `refreshGraphStatus` with a `let` instead so we can extend it, OR (cleaner) inline the build-box toggle into the original function. Use this final form for the function (replace the entire `refreshGraphStatus` from Task 10):

```js
async function refreshGraphStatus() {
  try {
    const r = await fetch("/api/graph/status");
    const data = await r.json();
    state.graphAvailable = !!data.available;
    if (!state.graphAvailable) {
      graphBannerEl.hidden = false;
      graphBannerEl.textContent =
        "Graph not built yet. Click 'Build graph' to extract concepts from this folder.";
      graphBuildBoxEl.hidden = false;
    } else {
      graphBannerEl.hidden = true;
      graphBuildBoxEl.hidden = true;
    }
  } catch (err) {
    state.graphAvailable = false;
    graphBuildBoxEl.hidden = false;
  }
}
```

- [ ] **Step 4: Build**

Run: `go build ./...`
Expected: success.

- [ ] **Step 5: Manual end-to-end test**

```bash
# Use the project's own root; ensure GEMINI_API_KEY is set if you want
# a REAL build, otherwise rely on the stub from the test suite for
# unit-level verification.
unset GEMINI_API_KEY
go build -o /tmp/mdv .
/tmp/mdv --web --port 8765 --root /tmp/mdv-test &
SERVER_PID=$!
sleep 1
# Open http://127.0.0.1:8765/, confirm "Build graph" button shows.
# Click it: expect 503 + log "GEMINI_API_KEY or GOOGLE_API_KEY must be set"
# Now export GEMINI_API_KEY=... and click again to do a real build
# (optional, requires real LLM quota).
kill $SERVER_PID
```

- [ ] **Step 6: Commit**

```bash
git add web.go
git commit -m "feat(graph): Build button + SSE progress log in graph rail"
```

---

### Task 15: Stage-2 manual acceptance

**Files:** none

- [ ] **Step 1: Full test suite green**

Run: `go test ./...`
Expected: ALL PASS.

- [ ] **Step 2: Cross-mode smoke**

Verify all three modes still build and start:

```bash
go build -o mdviewer .
# Web mode
./mdviewer --web --port 8765 --root . & sleep 1; kill %1
# Menubar mode (macOS only) — quick sanity, no menu interaction needed
./mdviewer --menubar --port 8766 --root . & sleep 1; kill %1
# TUI mode (just open and quit immediately)
echo q | ./mdviewer .
```

- [ ] **Step 3: Final commit**

If steps 1-2 are clean, no commit needed. Otherwise fix and commit.

```bash
git status
```

Stage 2 is now mergeable.

---

## Self-Review Notes (filled by author)

**Spec coverage:** Verified each section of the spec maps to at least one task:
- §6.1 graph.go → Tasks 1–4
- §6.2 webServer struct → Task 5, extended in Task 13
- §6.3 routes (status/file/concept) → Tasks 6–8
- §6.4 routes (build/build-status) → Tasks 12–13
- §6.5 frontend → Tasks 9–10, extended in Task 14
- §7 data flow → covered by Tasks 7, 8, 10
- §8 edge cases → distributed across Tasks 4, 5, 7, 8, 12, 13
- §9 test strategy → tasks have test code inline

**Placeholder scan:** No `TBD`, `TODO`, or "implement later" in any task. Every code-bearing step has full code.

**Type consistency:** `Node`, `FileRef`, `GraphIndex`, `BuildSession`, `BuildManager`, `BuildEvent` names are reused identically. Method names (`ConceptsInFile`, `FilesForConcept`, `ReloadIfChanged`, `NodeCount`, `LoadedAt`, `Start`, `Current`, `Events`, `Err`, `OK`, `ID`, `Root`) match across tasks and across the test code.

**One subtlety to watch during execution:** Task 14 step 3 originally tried to redefine `refreshGraphStatus`; the corrected form in the same step replaces Task 10's definition. Whoever implements should literally **replace** the Task 10 function body rather than redeclare.

---

Plan complete and saved to `docs/superpowers/plans/2026-05-21-graphify-integration.md`. Two execution options:

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
