# graphify Per-Folder Graph Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Make the graph build + concept rail operate on the folder the user is currently viewing (`state.cwd`) instead of the fixed launch `--root`. Each folder gets its own `graphify-out/graph.json`; navigating folders switches the rail to that folder's graph.

**Architecture:** Replace the single `webServer.graph`/`graphPath` with a per-directory cache `graphCache map[string]*GraphIndex` resolved by `graphForDir(dir)`. All graph HTTP routes accept a `dir` query param. The frontend sends `state.cwd` on every graph request and refreshes the rail whenever `loadDir` changes `state.cwd`.

**Tech Stack:** Go 1.22 stdlib; embedded HTML/JS in `web.go`.

**Builds on:** branch `feature/graphify-integration`, HEAD `8009eea`.

**Background:** Currently `handleGraphBuild` calls `Start(ctx, s.startDir, ...)` and `graphPath` is `<startDir>/graphify-out/graph.json` — both pinned to the launch `--root`. The user wants "build the folder I'm looking at, output there, show it there".

---

## File Structure

| File | Change |
|---|---|
| `web.go` | Replace single-graph fields with `graphCache`; add `graphForDir`/`invalidateGraph`; all graph routes take `dir`; frontend sends `dir` + refreshes rail on navigation |
| `menubar.go` | Drop `graphPath` field init + `tryLoadGraph()` call |
| `web_graph_test.go` | Update tests for `dir` param + per-folder resolution |

`graph.go` and `graph_build.go` need NO changes — `LoadGraph` already takes a `projectRoot`, and `BuildManager.Start` already takes an arbitrary `root`.

---

## Task 1: Backend — per-folder graph cache + `dir` on all routes

**Files:**
- Modify: `web.go`
- Modify: `menubar.go`
- Modify: `web_graph_test.go`

- [ ] **Step 1: Replace the `webServer` graph fields**

Current struct:
```go
type webServer struct {
	startDir string
	appRoot  string

	graphMu   sync.RWMutex
	graph     *GraphIndex
	graphPath string

	buildManager *BuildManager
}
```

Replace with:
```go
type webServer struct {
	startDir string
	appRoot  string

	graphMu    sync.RWMutex
	graphCache map[string]*GraphIndex // abs folder path -> its graph index

	buildManager *BuildManager
}
```

- [ ] **Step 2: Replace `tryLoadGraph`/`currentGraph` with `graphForDir`/`invalidateGraph`**

Delete the existing `tryLoadGraph` and `currentGraph` methods. Replace `runWebServer` (it currently sets `graphPath` and calls `tryLoadGraph`):

```go
func runWebServer(startDir, appRoot, addr string) error {
	if addr == "" {
		addr = "127.0.0.1:8421"
	}
	server := &webServer{
		startDir:   startDir,
		appRoot:    appRoot,
		graphCache: make(map[string]*GraphIndex),
	}
	server.buildManager = newBuildManager()
	fmt.Printf("mdviewer web preview running at http://%s\n", addr)
	return http.ListenAndServe(addr, server.routes())
}

// graphForDir returns the graph index for the graphify-out/graph.json
// inside dir, loading and caching it on first use and hot-reloading it
// when the file's mtime advances. Returns nil when dir has no graph.
// An empty dir falls back to the server's launch root.
func (s *webServer) graphForDir(dir string) *GraphIndex {
	if dir == "" {
		dir = s.startDir
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil
	}
	abs = filepath.Clean(abs)

	s.graphMu.RLock()
	g := s.graphCache[abs]
	s.graphMu.RUnlock()
	if g != nil {
		// Stale data beats no data — ignore reload errors (the file may
		// be mid-rewrite by a running graphify build).
		_, _ = g.ReloadIfChanged()
		return g
	}

	jsonPath := filepath.Join(abs, "graphify-out", "graph.json")
	loaded, err := LoadGraph(jsonPath, abs)
	if err != nil {
		return nil
	}
	s.graphMu.Lock()
	s.graphCache[abs] = loaded
	s.graphMu.Unlock()
	return loaded
}

// invalidateGraph drops the cached index for dir so the next graphForDir
// call reloads it from disk. Called after a build completes.
func (s *webServer) invalidateGraph(dir string) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return
	}
	s.graphMu.Lock()
	delete(s.graphCache, filepath.Clean(abs))
	s.graphMu.Unlock()
}
```

- [ ] **Step 3: Update `menubar.go`**

In `runMenuBarApp`, the `webServer` literal currently sets `graphPath` and calls `server.tryLoadGraph()`. Replace that construction with:

```go
		server := &webServer{
			startDir:   startDir,
			appRoot:    appRoot,
			graphCache: make(map[string]*GraphIndex),
		}
		server.buildManager = newBuildManager()
```

Remove the now-deleted `server.tryLoadGraph()` line. (Keep `path/filepath` import — `runMenuBarApp` no longer uses it for graphPath, but verify nothing else in menubar.go needs it; if `go build` reports it unused, remove the import.)

- [ ] **Step 4: Update `handleGraphStatus`**

Replace it:
```go
type graphStatusResponse struct {
	Available bool      `json:"available"`
	NodeCount int       `json:"node_count"`
	LoadedAt  time.Time `json:"loaded_at,omitempty"`
	Dir       string    `json:"dir"`
	Path      string    `json:"path"`
}

func (s *webServer) handleGraphStatus(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("dir")
	if dir == "" {
		dir = s.startDir
	}
	abs, _ := filepath.Abs(dir)
	resp := graphStatusResponse{
		Dir:  abs,
		Path: filepath.Join(abs, "graphify-out", "graph.json"),
	}
	if g := s.graphForDir(dir); g != nil {
		resp.Available = true
		resp.NodeCount = g.NodeCount()
		resp.LoadedAt = g.LoadedAt()
	}
	s.writeJSON(w, http.StatusOK, resp)
}
```

- [ ] **Step 5: Update `handleGraphFile`**

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
	g := s.graphForDir(r.URL.Query().Get("dir"))
	if g == nil {
		s.writeJSON(w, http.StatusOK, []Node{})
		return
	}
	s.writeJSON(w, http.StatusOK, g.ConceptsInFile(abs))
}
```

- [ ] **Step 6: Update `handleGraphConcept`**

```go
func (s *webServer) handleGraphConcept(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	g := s.graphForDir(r.URL.Query().Get("dir"))
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

- [ ] **Step 7: Update `handleGraphBuild`**

Replace the `Start` call so it builds the requested dir:
```go
func (s *webServer) handleGraphBuild(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	dir := r.URL.Query().Get("dir")
	if dir == "" {
		dir = s.startDir
	}
	// The build outlives this HTTP request (202 returns immediately, the
	// build runs in a background goroutine). Binding it to r.Context()
	// would cancel the subprocess the instant the handler returns.
	sess, err := s.buildManager.Start(context.Background(), dir, r.URL.Query().Get("backend"))
	if err != nil {
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

- [ ] **Step 8: Update `handleGraphBuildStatus` post-build reload**

The handler currently ends with `if sess.OK() { s.tryLoadGraph() }`. Replace that with:
```go
	if sess.OK() {
		s.invalidateGraph(sess.Root())
	}
```
`BuildSession.Root()` returns the dir that was built — invalidating its cache entry makes the next `graphForDir` reload the fresh graph.

- [ ] **Step 9: Update `web_graph_test.go`**

`newTestServer` builds a webServer with a fixture graph at `<root>/graphify-out/graph.json`. Update it to the new struct and have callers pass `dir`:

Replace `newTestServer`:
```go
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
	return &webServer{
		startDir:   root,
		appRoot:    root,
		graphCache: make(map[string]*GraphIndex),
	}
}
```

Now update each graph test to pass `?dir=<startDir>` so it consults the fixture. The graph tests and their new request URLs:

- `TestGraphStatusNoGraph`: `GET /api/graph/status?dir=` + s.startDir — still expects `available:false`.
- `TestGraphStatusWithGraph`: `GET /api/graph/status?dir=` + s.startDir — expects `available:true`, `node_count:3`.
- `TestGraphFileReturnsConcepts`: `GET /api/graph/file?dir=<startDir>&path=<startDir>/auth/session.go`.
- `TestGraphFileMissingPath`: `GET /api/graph/file?dir=<startDir>` (no path) — 400.
- `TestGraphFileNoGraph`: `GET /api/graph/file?dir=<startDir>&path=/nope` — 200, body `[]`.
- `TestGraphConceptReturnsFiles`: `GET /api/graph/concept?dir=<startDir>&id=auth_session_token`.
- `TestGraphConceptMissingNode`: `GET /api/graph/concept?dir=<startDir>&id=nope` — 404.
- `TestGraphBackendsLists`: unchanged (no graph dependency).
- `TestGraphBuildMethodNotAllowed`: unchanged.
- `TestGraphBuildMissingAPIKey`: unchanged (still `?backend=gemini-api`).
- `TestGraphBuildSurvivesRequestCompletion`: unchanged — it already POSTs `?backend=gemini-api`; with no `dir`, the build falls back to `s.startDir` which is the temp root, exactly what the stub expects.

For each test, build the request URL with the startDir, e.g.:
```go
	req := httptest.NewRequest("GET", "/api/graph/status?dir="+s.startDir, nil)
```
and for the file test:
```go
	req := httptest.NewRequest("GET",
		"/api/graph/file?dir="+s.startDir+"&path="+filepath.Join(s.startDir, "auth/session.go"), nil)
```

Apply the analogous `dir=` addition to every graph-route request in the file. The `newTestServer` tests that previously called `s.tryLoadGraph()` must drop that call (the method no longer exists) — `graphForDir` lazy-loads.

Also: any test that set `s.buildManager = newBuildManager()` still works (the field is unchanged).

- [ ] **Step 10: Build + test**

Run: `go build ./...` → success (watch for unused `path/filepath` in menubar.go — remove the import if the compiler flags it).
Run: `go test -count=1 ./...` → full suite green.
Run: `go test -race -count=1 ./...` → green.

- [ ] **Step 11: Commit**

```bash
git add web.go menubar.go web_graph_test.go
git commit -m "feat(graph): per-folder graph resolution — routes take a dir param"
```

---

## Task 2: Frontend — send `dir`, refresh rail on folder navigation

**Files:**
- Modify: `web.go` (embedded JS)

- [ ] **Step 1: Send `dir` in `refreshGraphStatus`**

Replace the fetch line in `refreshGraphStatus`:
```js
        const r = await fetch("/api/graph/status");
```
with:
```js
        const r = await fetch("/api/graph/status?dir=" + encodeURIComponent(state.cwd || ""));
```

- [ ] **Step 2: Send `dir` in `loadConceptsForFile`**

Replace the fetch line:
```js
        const r = await fetch("/api/graph/file?path=" + encodeURIComponent(absPath));
```
with:
```js
        const r = await fetch("/api/graph/file?dir=" + encodeURIComponent(state.cwd || "") +
                              "&path=" + encodeURIComponent(absPath));
```

- [ ] **Step 3: Send `dir` in `activateConcept`**

Replace the fetch line:
```js
        const r = await fetch("/api/graph/concept?id=" + encodeURIComponent(nodeId));
```
with:
```js
        const r = await fetch("/api/graph/concept?dir=" + encodeURIComponent(state.cwd || "") +
                              "&id=" + encodeURIComponent(nodeId));
```

- [ ] **Step 4: Send `dir` in `startGraphBuild`**

Replace the build POST line:
```js
        const backend = graphBackendSelectEl.value || "auto";
        resp = await fetch("/api/graph/build?backend=" + encodeURIComponent(backend), { method: "POST" });
```
with:
```js
        const backend = graphBackendSelectEl.value || "auto";
        resp = await fetch("/api/graph/build?dir=" + encodeURIComponent(state.cwd || "") +
                           "&backend=" + encodeURIComponent(backend), { method: "POST" });
```

- [ ] **Step 5: Refresh the rail when the folder changes**

In `loadDir`, just after `state.cwd = data.cwd;` (line ~2773), add:
```js
      // The graph rail is per-folder — re-evaluate it whenever the
      // current directory changes.
      refreshGraphStatus().then(function () {
        loadConceptsForFile(state.selectedPath || "");
      });
```

`loadConceptsForFile("")` with an empty path hits the early-return branch and clears the chips, which is the desired "no file selected in this folder yet" state. When a file IS selected (keepSelection navigations), it reloads that file's concepts against the new folder's graph.

- [ ] **Step 6: Build + smoke test**

Run: `go build ./...` → success.

```bash
go build -o /tmp/mdv-pf .
# Folder A has a graph, folder B does not.
ROOT=$(mktemp -d)
mkdir -p "$ROOT/sub-with-graph/graphify-out" "$ROOT/sub-no-graph"
cp testdata/graph_simple.json "$ROOT/sub-with-graph/graphify-out/graph.json"
/tmp/mdv-pf --web --port 18480 --root "$ROOT" >/tmp/mdv-pf.log 2>&1 &
PID=$!
sleep 1
echo "--- status of sub-with-graph (expect available:true) ---"
curl -s "http://127.0.0.1:18480/api/graph/status?dir=$ROOT/sub-with-graph"
echo
echo "--- status of sub-no-graph (expect available:false) ---"
curl -s "http://127.0.0.1:18480/api/graph/status?dir=$ROOT/sub-no-graph"
echo
kill $PID 2>/dev/null; wait 2>/dev/null
rm -rf "$ROOT" /tmp/mdv-pf /tmp/mdv-pf.log
```

Expected: first curl → `available:true,node_count:3`; second → `available:false`.

- [ ] **Step 7: Run all tests**

Run: `go test -count=1 ./...` → green.

- [ ] **Step 8: Commit**

```bash
git add web.go
git commit -m "feat(graph): frontend sends current folder; rail refreshes on navigation"
```

---

## Task 3: Integration smoke + acceptance

**Files:** none (verification only)

- [ ] **Step 1: Full suite + race**

Run: `go test -count=1 ./...` and `go test -race -count=1 ./...` → both green.

- [ ] **Step 2: Build final binary**

Run: `go build -o mdviewer .` → success.

- [ ] **Step 3: Two-folder end-to-end**

```bash
ROOT=$(mktemp -d)
mkdir -p "$ROOT/alpha/graphify-out" "$ROOT/beta"
cp testdata/graph_simple.json "$ROOT/alpha/graphify-out/graph.json"
mkdir -p "$ROOT/alpha/auth" "$ROOT/alpha/docs"
echo x > "$ROOT/alpha/auth/session.go"
echo x > "$ROOT/alpha/auth/login.go"
echo x > "$ROOT/alpha/docs/intro.md"
./mdviewer --web --port 18481 --root "$ROOT" >/tmp/mdv-acc.log 2>&1 &
PID=$!
sleep 1
echo "alpha status:";   curl -s "http://127.0.0.1:18481/api/graph/status?dir=$ROOT/alpha"
echo; echo "alpha file:"; curl -s "http://127.0.0.1:18481/api/graph/file?dir=$ROOT/alpha&path=$ROOT/alpha/auth/session.go"
echo; echo "beta status:"; curl -s "http://127.0.0.1:18481/api/graph/status?dir=$ROOT/beta"
echo
kill $PID 2>/dev/null; wait 2>/dev/null
rm -rf "$ROOT" /tmp/mdv-acc.log
```

Verify: alpha status `available:true`; alpha file returns the `auth_session_token` concept; beta status `available:false`.

- [ ] **Step 4: Commit if any stray fixes; otherwise done.**

```bash
git status
```

---

## Self-Review Notes

**Spec coverage:** build targets `state.cwd` (Task 1 Step 7 + Task 2 Step 4); output lands in `<cwd>/graphify-out/` (graphify writes relative to the dir passed to `BuildManager.Start`); rail reads `<cwd>/graphify-out/` (Task 1 `graphForDir` + Task 2 `dir` params); rail switches on folder navigation (Task 2 Step 5).

**Placeholder scan:** none.

**Type consistency:** `graphCache map[string]*GraphIndex`, `graphForDir(dir string) *GraphIndex`, `invalidateGraph(dir string)` used consistently. `graphStatusResponse` gains a `Dir` field; the frontend doesn't depend on it (it only reads `available`), so no frontend coupling. `BuildSession.Root()` already exists (Task 12 of the prior plan).

**Known limitation:** the per-folder cache never evicts entries — a long-lived server browsing thousands of folders accumulates `GraphIndex` objects. Acceptable for a local desktop tool; note as a follow-up if it ever matters.
