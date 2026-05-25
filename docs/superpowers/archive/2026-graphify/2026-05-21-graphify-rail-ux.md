# graphify Rail UX Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`).

**Goal:** Four right-rail improvements: (A) build progress shows the folder being built and survives navigation; (B) an "Open graph" button renders `graph.html` in the preview pane; (C) a "Build history" list at the rail bottom navigates to built folders; (D) a collapse toggle hides/shows the right rail.

**Architecture:** Backend gains a persisted build-history list (`.mdviewer_graph_history.json`) populated on build-completion and on graph-discovery, exposed via `GET /api/graph/history`. The frontend moves the build progress into a standalone rail block independent of per-folder state, adds an Open-graph button, a history list section, and a rail collapse/reveal pair mirroring the existing left-sidebar collapse.

**Tech Stack:** Go 1.22 stdlib (`encoding/json`, `os`, `sort`, `path/filepath`, `sync`); embedded HTML/CSS/JS in `web.go`.

**Builds on:** branch `feature/graphify-integration`, HEAD `eefab8d`.

---

## File Structure

| File | Change |
|---|---|
| `web.go` | history store + `/api/graph/history` route + `recordBuild` hooks; frontend: standalone progress block, Open-graph button, history list, rail collapse toggle |
| `graph_build.go` | "already running" error message names the folder |
| `web_graph_test.go` | tests for history store + route + 409 message |
| `.gitignore` | add `.mdviewer_graph_history.json` |

---

## Task 1: Backend — build-history store + route + 409 message

**Files:** `web.go`, `graph_build.go`, `web_graph_test.go`, `.gitignore`

- [ ] **Step 1: Add the failing tests** — append to `web_graph_test.go`:

```go
func TestRecordBuildAndHistory(t *testing.T) {
	s := newTestServer(t, true) // fixture graph at <startDir>/graphify-out/graph.json
	s.recordBuild(s.startDir)

	req := httptest.NewRequest("GET", "/api/graph/history", nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []graphHistoryEntry
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || filepath.Clean(got[0].Dir) != filepath.Clean(s.startDir) {
		t.Errorf("history = %+v, want one entry for %s", got, s.startDir)
	}
	if got[0].BuiltAt.IsZero() {
		t.Errorf("history entry should carry the graph.json mtime")
	}
}

func TestGraphHistoryDropsMissing(t *testing.T) {
	s := newTestServer(t, false) // NO graph file
	// Record a dir whose graph.json does not exist.
	s.recordBuild(s.startDir)

	req := httptest.NewRequest("GET", "/api/graph/history", nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	var got []graphHistoryEntry
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("history should drop entries whose graph.json is gone, got %+v", got)
	}
}

func TestRecordBuildDedupes(t *testing.T) {
	s := newTestServer(t, true)
	s.recordBuild(s.startDir)
	s.recordBuild(s.startDir)
	s.recordBuild(s.startDir)
	if dirs := s.loadGraphHistory(); len(dirs) != 1 {
		t.Errorf("recordBuild should dedupe, got %v", dirs)
	}
}
```

- [ ] **Step 2: Run to verify FAIL**

Run: `go test -run 'TestRecordBuild|TestGraphHistory' ./...`
Expected: build errors — `recordBuild`, `loadGraphHistory`, `graphHistoryEntry` undefined.

- [ ] **Step 3: Add `historyMu` to the `webServer` struct**

The current struct (per-folder version) is:
```go
type webServer struct {
	startDir string
	appRoot  string

	graphMu    sync.RWMutex
	graphCache map[string]*GraphIndex

	buildManager *BuildManager
}
```
Add a history mutex:
```go
type webServer struct {
	startDir string
	appRoot  string

	graphMu    sync.RWMutex
	graphCache map[string]*GraphIndex

	historyMu sync.Mutex

	buildManager *BuildManager
}
```

- [ ] **Step 4: Add the history store + route handler** — append to `web.go`:

```go
const graphHistoryFileName = ".mdviewer_graph_history.json"

// graphHistoryEntry is one row of build history returned to the UI.
type graphHistoryEntry struct {
	Dir     string    `json:"dir"`
	BuiltAt time.Time `json:"built_at"`
}

func (s *webServer) graphHistoryPath() string {
	return filepath.Join(s.appRoot, graphHistoryFileName)
}

// loadGraphHistory reads the persisted list of built directories
// (most-recent-first). Missing / unreadable file -> nil.
func (s *webServer) loadGraphHistory() []string {
	data, err := os.ReadFile(s.graphHistoryPath())
	if err != nil {
		return nil
	}
	var dirs []string
	if err := json.Unmarshal(data, &dirs); err != nil {
		return nil
	}
	return dirs
}

func (s *webServer) saveGraphHistory(dirs []string) {
	data, err := json.MarshalIndent(dirs, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(s.graphHistoryPath(), data, 0o644)
}

// recordBuild moves dir to the front of the build-history list
// (de-duplicated, capped at 50). A no-op when dir is already the most
// recent entry, so repeated /api/graph/status hits stay cheap.
func (s *webServer) recordBuild(dir string) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return
	}
	abs = filepath.Clean(abs)
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	existing := s.loadGraphHistory()
	if len(existing) > 0 && existing[0] == abs {
		return
	}
	out := []string{abs}
	for _, d := range existing {
		if d != abs {
			out = append(out, d)
		}
	}
	if len(out) > 50 {
		out = out[:50]
	}
	s.saveGraphHistory(out)
}

// handleGraphHistory returns the build history, freshest first. Entries
// whose graph.json no longer exists are dropped and the file is rewritten
// (self-healing).
func (s *webServer) handleGraphHistory(w http.ResponseWriter, r *http.Request) {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	dirs := s.loadGraphHistory()
	out := []graphHistoryEntry{}
	kept := []string{}
	for _, dir := range dirs {
		info, err := os.Stat(filepath.Join(dir, "graphify-out", "graph.json"))
		if err != nil {
			continue
		}
		out = append(out, graphHistoryEntry{Dir: dir, BuiltAt: info.ModTime()})
		kept = append(kept, dir)
	}
	if len(kept) != len(dirs) {
		s.saveGraphHistory(kept)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BuiltAt.After(out[j].BuiltAt) })
	s.writeJSON(w, http.StatusOK, out)
}
```

Add `"sort"` to web.go's import block (`os`, `time`, `path/filepath`, `encoding/json`, `sync` are already imported).

- [ ] **Step 5: Register the route** — in `routes()`, after `/api/graph/backends`:

```go
	mux.HandleFunc("/api/graph/history", s.handleGraphHistory)
```

- [ ] **Step 6: Hook `recordBuild` into discovery + completion**

(6a) In `handleGraphStatus`, inside the `if g := s.graphForDir(dir); g != nil { ... }` block, add `s.recordBuild(dir)` so visiting a folder that already has a graph records it:
```go
	if g := s.graphForDir(dir); g != nil {
		resp.Available = true
		resp.NodeCount = g.NodeCount()
		resp.LoadedAt = g.LoadedAt()
		s.recordBuild(dir)
	}
```

(6b) In `handleGraphBuildStatus`, the success block currently is `if sess.OK() { s.invalidateGraph(sess.Root()) }`. Add the record:
```go
	if sess.OK() {
		s.invalidateGraph(sess.Root())
		s.recordBuild(sess.Root())
	}
```

- [ ] **Step 7: Name the folder in the "already running" 409 — `graph_build.go`**

In `BuildManager.Start`, the concurrency guard returns:
```go
			return nil, fmt.Errorf("a graphify build is already running (id=%s)", m.current.id)
```
Replace with:
```go
			return nil, fmt.Errorf("a graphify build is already running for %s", m.current.root)
```
The substring `already running` is preserved, so `handleGraphBuild`'s 409 classification still works.

- [ ] **Step 8: Update `.gitignore`**

Under the `# --- mdviewer runtime data ---` section, add a line next to `.mdviewer_favorites.json`:
```
.mdviewer_graph_history.json
```

- [ ] **Step 9: Run tests**

Run: `go test -run 'TestRecordBuild|TestGraphHistory' -count=1 ./...` → 3 PASS
Run: `go test -count=1 ./...` → full suite green
Run: `go test -race -count=1 ./...` → green

- [ ] **Step 10: Commit**

```bash
git add web.go graph_build.go web_graph_test.go .gitignore
git commit -m "feat(graph): persist build history; /api/graph/history; folder-named 409"
```

---

## Task 2: Frontend A — standalone build progress block (survives navigation)

**Files:** `web.go` (embedded HTML/CSS/JS)

The progress bar currently lives inside `#graphBuildBox`, which `refreshGraphStatus` hides on navigation. Move the progress + meta + log into a standalone `#graphBuildStatus` block at the top of the rail that `refreshGraphStatus` never touches, and label it with the folder being built.

- [ ] **Step 1: Add CSS** — inside `<style>`, after the `.graph-progress-meta[hidden]` rule:

```css
    .graph-build-status {
      display: flex;
      flex-direction: column;
      gap: 6px;
      padding: 10px 12px;
      border: 1px solid var(--line);
      border-radius: 10px;
      background: color-mix(in oklab, var(--accent) 8%, var(--panel));
    }
    .graph-build-status[hidden] { display: none; }
    .graph-build-status-folder {
      font-size: 12px;
      font-weight: 600;
      color: var(--text);
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
```

- [ ] **Step 2: Add the standalone DOM block**

Inside `#graphRail`, as the FIRST child (before the `<div class="graph-section-title">Concepts in this file</div>`), add:
```html
      <div class="graph-build-status" id="graphBuildStatus" hidden>
        <div class="graph-build-status-folder" id="graphBuildStatusFolder"></div>
        <div class="graph-progress" id="graphProgress">
          <div class="graph-progress-fill" id="graphProgressFill"></div>
        </div>
        <div class="graph-progress-meta" id="graphProgressMeta">
          <span id="graphProgressPhase">starting</span>
          <span id="graphProgressTime">0:00</span>
        </div>
        <div class="graph-build-log" id="graphBuildLog" hidden></div>
      </div>
```

- [ ] **Step 3: Remove the moved nodes from `#graphBuildBox`**

`#graphBuildBox` currently contains: the `<select>`, the `<button>`, `#graphProgress`, `#graphProgressMeta`, `#graphBuildHint`, `#graphBuildLog`. DELETE the `#graphProgress`, `#graphProgressMeta`, and `#graphBuildLog` elements from inside `#graphBuildBox` (they now live in `#graphBuildStatus`). The build box keeps ONLY the `<select>`, the `<button>`, and `#graphBuildHint`. The `[hidden]` attributes on `#graphProgress`/`#graphProgressMeta` were managed via JS; in their new home they start un-hidden inside the (hidden) `#graphBuildStatus` container — when the container is shown they all show together.

- [ ] **Step 4: Update `startGraphBuild`**

The element refs `graphProgressEl`, `graphProgressFillEl`, `graphProgressMetaEl`, `graphProgressPhaseEl`, `graphProgressTimeEl`, `graphBuildLogEl` still resolve by ID — no ref changes needed. Add one ref in the ref block:
```js
    const graphBuildStatusEl = document.getElementById("graphBuildStatus");
    const graphBuildStatusFolderEl = document.getElementById("graphBuildStatusFolder");
```

In `startGraphBuild`, at the very top (right after `graphBuildBtnEl.disabled = true;`), capture the folder and show the standalone block:
```js
      const buildDir = state.cwd || "";
      const buildFolderName = buildDir.split("/").filter(Boolean).pop() || buildDir || "(root)";
      graphBuildStatusEl.hidden = false;
      graphBuildStatusFolderEl.textContent = "Building " + buildFolderName;
```

The existing progress-init block (moved earlier to before `const src = new EventSource`) still sets widths/text on the same IDs — keep it as-is; it now writes into the standalone block.

In the SSE `closed` branch, after setting the fill to done/failed, update the folder label so the finished state is self-describing:
```js
            graphBuildStatusFolderEl.textContent =
              (data.ok ? "Built " : "Build failed: ") + buildFolderName;
```

Do NOT auto-hide `#graphBuildStatus` — it stays showing the final state until the next build starts (next `startGraphBuild` resets it). `refreshGraphStatus` must NOT touch `#graphBuildStatus`.

- [ ] **Step 5: Build + smoke**

Run: `go build ./...` → success.
```bash
go build -o /tmp/mdv-a .
ROOT=$(mktemp -d); /tmp/mdv-a --web --port 18493 --root "$ROOT" >/dev/null 2>&1 &
PID=$!; sleep 1
HTML=$(curl -s http://127.0.0.1:18493/)
for id in graphBuildStatus graphBuildStatusFolder graphProgress graphProgressFill graphBuildLog; do
  echo "$HTML" | grep -q "id=\"$id\"" && echo "$id ok" || echo "$id MISSING"
done
kill $PID 2>/dev/null; wait 2>/dev/null; rm -rf "$ROOT" /tmp/mdv-a
```
All 5 IDs print `ok`.

- [ ] **Step 6: Run all tests**

Run: `go test -count=1 ./...` → green.

- [ ] **Step 7: Commit**

```bash
git add web.go
git commit -m "feat(graph): standalone build-progress block with folder label"
```

---

## Task 3: Frontend B — "Open graph" button (renders graph.html in the preview)

**Files:** `web.go` (embedded HTML/CSS/JS)

- [ ] **Step 1: Add CSS** — inside `<style>`, after the `.graph-built-at` rule:

```css
    .graph-open-btn {
      align-self: flex-start;
      padding: 4px 10px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: var(--panel-2);
      color: var(--text);
      font-size: 12px;
      cursor: pointer;
    }
    .graph-open-btn:hover { border-color: var(--accent); }
    .graph-open-btn[hidden] { display: none; }
```

- [ ] **Step 2: Add the button to the DOM**

Right after `<div class="graph-built-at" id="graphBuiltAt" hidden></div>` (inside `#graphRail`), add:
```html
        <button class="graph-open-btn" id="graphOpenBtn" type="button" hidden>Open graph ↗</button>
```

- [ ] **Step 3: Wire it up**

Add a ref in the ref block:
```js
    const graphOpenBtnEl = document.getElementById("graphOpenBtn");
```

Add to `state` (the global state object) a field to remember the current folder's graph dir:
```js
      graphDir: "",
```

In `refreshGraphStatus`, in the branch where `data.available` is true (alongside the `graphBuiltAtEl` handling), set the state and show the button; in the else branch hide it:
```js
        if (data.available && data.built_at) {
          graphBuiltAtEl.hidden = false;
          graphBuiltAtEl.textContent =
            "Last built " + graphRelativeTime(data.built_at) +
            " · " + (data.node_count || 0) + " nodes";
          state.graphDir = data.dir || "";
          graphOpenBtnEl.hidden = false;
        } else {
          graphBuiltAtEl.hidden = true;
          state.graphDir = "";
          graphOpenBtnEl.hidden = true;
        }
```

Add the click handler (next to the other graph wiring):
```js
    graphOpenBtnEl.addEventListener("click", function () {
      if (!state.graphDir) return;
      selectFile(state.graphDir + "/graphify-out/graph.html", { historyMode: "push" });
    });
```

`selectFile` already routes `.html` files to the existing sandboxed-iframe HTML preview in the middle pane — no preview changes needed.

- [ ] **Step 4: Build + smoke**

Run: `go build ./...` → success.
```bash
go build -o /tmp/mdv-b .
ROOT=$(mktemp -d); mkdir -p "$ROOT/graphify-out"
cp testdata/graph_simple.json "$ROOT/graphify-out/graph.json"
echo '<html><body>graph</body></html>' > "$ROOT/graphify-out/graph.html"
/tmp/mdv-b --web --port 18494 --root "$ROOT" >/dev/null 2>&1 &
PID=$!; sleep 1
curl -s http://127.0.0.1:18494/ | grep -q 'id="graphOpenBtn"' && echo "button ok" || echo "MISSING"
kill $PID 2>/dev/null; wait 2>/dev/null; rm -rf "$ROOT" /tmp/mdv-b
```

- [ ] **Step 5: Run all tests** → `go test -count=1 ./...` green.

- [ ] **Step 6: Commit**

```bash
git add web.go
git commit -m "feat(graph): Open-graph button renders graph.html in the preview"
```

---

## Task 4: Frontend C — Build history list (rail bottom)

**Files:** `web.go` (embedded HTML/CSS/JS)

- [ ] **Step 1: Add CSS** — inside `<style>`, after the `.graph-open-btn[hidden]` rule:

```css
    .graph-history {
      display: flex;
      flex-direction: column;
      gap: 3px;
    }
    .graph-history-row {
      padding: 6px 8px;
      border-radius: 6px;
      cursor: pointer;
      font-size: 13px;
      color: var(--text);
      overflow: hidden;
    }
    .graph-history-row:hover { background: var(--panel-2); }
    .graph-history-row .graph-history-when {
      display: block;
      font-size: 11px;
      color: var(--muted);
    }
```

- [ ] **Step 2: Add the DOM section**

Inside `#graphRail`, as the LAST section (after the "Linked files" block, before the `#graphBuildBox`/`#graphBanner` — place it just before `#graphBanner`), add:
```html
      <div>
        <div class="graph-section-title">Build history</div>
        <div class="graph-history" id="graphHistory"></div>
      </div>
```

- [ ] **Step 3: Wire it up**

Add a ref:
```js
    const graphHistoryEl = document.getElementById("graphHistory");
```

Add the loader function (near the other graph functions):
```js
    async function loadGraphHistory() {
      let entries = [];
      try {
        const r = await fetch("/api/graph/history");
        entries = await r.json();
      } catch (err) {
        entries = [];
      }
      graphHistoryEl.innerHTML = "";
      if (!entries || !entries.length) {
        const e = document.createElement("div");
        e.className = "graph-empty";
        e.textContent = "No builds yet.";
        graphHistoryEl.appendChild(e);
        return;
      }
      for (const entry of entries) {
        const row = document.createElement("div");
        row.className = "graph-history-row";
        row.title = entry.dir;
        const name = entry.dir.split("/").filter(Boolean).pop() || entry.dir;
        const nameSpan = document.createElement("span");
        nameSpan.textContent = name;
        const whenSpan = document.createElement("span");
        whenSpan.className = "graph-history-when";
        whenSpan.textContent = "built " + graphRelativeTime(entry.built_at);
        row.appendChild(nameSpan);
        row.appendChild(whenSpan);
        row.addEventListener("click", function () {
          loadDir(entry.dir, { historyMode: "push" });
        });
        graphHistoryEl.appendChild(row);
      }
    }
```

- [ ] **Step 4: Call `loadGraphHistory` at boot and after a build**

At boot — find where `refreshGraphStatus();` and `loadGraphBackends();` are called at startup, add:
```js
    loadGraphHistory();
```

After a successful build — in `startGraphBuild`'s SSE `closed` branch, inside the `if (data.ok)` block where `refreshGraphStatus()` is already called, also refresh the history:
```js
            if (data.ok) {
              refreshGraphStatus().then(function () {
                if (state.selectedPath) loadConceptsForFile(state.selectedPath);
              });
              loadGraphHistory();
            }
```
(Keep the existing closed-branch logic; just add the `loadGraphHistory();` line in the success path.)

- [ ] **Step 5: Build + smoke**

Run: `go build ./...` → success.
```bash
go build -o /tmp/mdv-c .
ROOT=$(mktemp -d); mkdir -p "$ROOT/graphify-out"
cp testdata/graph_simple.json "$ROOT/graphify-out/graph.json"
/tmp/mdv-c --web --port 18495 --root "$ROOT" >/dev/null 2>&1 &
PID=$!; sleep 1
# visiting status records the dir in history
curl -s "http://127.0.0.1:18495/api/graph/status?dir=$ROOT" >/dev/null
echo "history:"; curl -s http://127.0.0.1:18495/api/graph/history
echo
curl -s http://127.0.0.1:18495/ | grep -q 'id="graphHistory"' && echo "section ok" || echo "MISSING"
kill $PID 2>/dev/null; wait 2>/dev/null; rm -rf "$ROOT" /tmp/mdv-c
```
Expected: history returns one entry for `$ROOT`; section present.

- [ ] **Step 6: Run all tests** → `go test -count=1 ./...` green.

- [ ] **Step 7: Commit**

```bash
git add web.go
git commit -m "feat(graph): build history list in the rail; click to navigate"
```

---

## Task 5: Frontend D — right-rail collapse toggle

**Files:** `web.go` (embedded HTML/CSS/JS)

The `.app` grid already supports `.graph-rail-collapsed` (added earlier). This task adds the toggle button + a floating reveal button, mirroring the existing LEFT sidebar collapse.

- [ ] **Step 1: Study the existing left-sidebar collapse**

Read these in `web.go` and understand the pattern before writing code:
- The `#collapseSidebar` button (in the sidebar topbar) and its click handler.
- The `#revealSidebar` floating button (shown when the sidebar is collapsed) and its handler.
- `state.sidebarCollapsed`, the `.app.sidebar-collapsed` class toggle, and how the collapsed state is persisted to `localStorage` and restored at boot.
- The `.reveal-sidebar` CSS.

Mirror that pattern exactly for the right rail. Use these names: `#collapseGraphRail`, `#revealGraphRail`, `state.graphRailCollapsed`, class `.graph-rail-collapsed`, localStorage key `mdviewer.graphRailCollapsed`.

- [ ] **Step 2: Add CSS for the two buttons**

Add a `.collapse-graph-rail` style for the in-rail button and a `.reveal-graph-rail` style for the floating button. The reveal button mirrors `.reveal-sidebar` but is anchored to the RIGHT edge instead of the left. Example (adapt positioning to match how `.reveal-sidebar` is written):
```css
    .collapse-graph-rail {
      align-self: flex-end;
    }
    .reveal-graph-rail {
      position: fixed;
      top: 18px;
      right: 18px;
      z-index: 50;
    }
    .reveal-graph-rail[hidden] { display: none; }
```
If `.reveal-sidebar` uses a shared `.action` button class, reuse it on the reveal button too for visual consistency.

- [ ] **Step 3: Add the DOM**

(3a) A collapse button at the TOP of `#graphRail` — make it the very first child, before `#graphBuildStatus`:
```html
      <button class="action collapse-graph-rail" id="collapseGraphRail" type="button" title="Hide graph panel">›</button>
```

(3b) A floating reveal button — place it next to the existing `#revealSidebar` element in the DOM:
```html
  <button class="action reveal-graph-rail" id="revealGraphRail" type="button" title="Show graph panel" hidden>✦ Graph</button>
```

- [ ] **Step 4: Wire the toggle**

Add refs:
```js
    const collapseGraphRailEl = document.getElementById("collapseGraphRail");
    const revealGraphRailEl = document.getElementById("revealGraphRail");
```

Add to `state`:
```js
      graphRailCollapsed: false,
```

Add an apply function and handlers, mirroring the sidebar logic:
```js
    function applyGraphRailCollapsed() {
      appShellEl.classList.toggle("graph-rail-collapsed", state.graphRailCollapsed);
      revealGraphRailEl.hidden = !state.graphRailCollapsed;
      try {
        localStorage.setItem("mdviewer.graphRailCollapsed", state.graphRailCollapsed ? "1" : "0");
      } catch (e) {}
    }

    collapseGraphRailEl.addEventListener("click", function () {
      state.graphRailCollapsed = true;
      applyGraphRailCollapsed();
    });
    revealGraphRailEl.addEventListener("click", function () {
      state.graphRailCollapsed = false;
      applyGraphRailCollapsed();
    });
```
Use whatever the app-shell element variable is actually called (the left sidebar handler references it — match that; it is the element with `id="appShell"`).

- [ ] **Step 5: Restore the collapsed state at boot**

Where the sidebar collapsed state is restored from localStorage at startup, add the parallel restore:
```js
    try {
      state.graphRailCollapsed = localStorage.getItem("mdviewer.graphRailCollapsed") === "1";
    } catch (e) {}
    applyGraphRailCollapsed();
```

- [ ] **Step 6: Build + smoke**

Run: `go build ./...` → success.
```bash
go build -o /tmp/mdv-d .
ROOT=$(mktemp -d); /tmp/mdv-d --web --port 18496 --root "$ROOT" >/dev/null 2>&1 &
PID=$!; sleep 1
HTML=$(curl -s http://127.0.0.1:18496/)
for id in collapseGraphRail revealGraphRail; do
  echo "$HTML" | grep -q "id=\"$id\"" && echo "$id ok" || echo "$id MISSING"
done
kill $PID 2>/dev/null; wait 2>/dev/null; rm -rf "$ROOT" /tmp/mdv-d
```
Both IDs print `ok`.

- [ ] **Step 7: Run all tests** → `go test -count=1 ./...` green.

- [ ] **Step 8: Commit**

```bash
git add web.go
git commit -m "feat(graph): right-rail collapse toggle with floating reveal button"
```

---

## Task 6: Integration smoke + acceptance

**Files:** none.

- [ ] **Step 1:** `go test -count=1 ./...` and `go test -race -count=1 ./...` → green.
- [ ] **Step 2:** `go build -o mdviewer .` → success.
- [ ] **Step 3:** End-to-end check:
```bash
ROOT=$(mktemp -d); mkdir -p "$ROOT/proj/graphify-out"
cp testdata/graph_simple.json "$ROOT/proj/graphify-out/graph.json"
echo '<html><body>g</body></html>' > "$ROOT/proj/graphify-out/graph.html"
./mdviewer --web --port 18497 --root "$ROOT" >/dev/null 2>&1 &
PID=$!; sleep 1
echo "status (records history):"; curl -s "http://127.0.0.1:18497/api/graph/status?dir=$ROOT/proj"
echo; echo "history:"; curl -s http://127.0.0.1:18497/api/graph/history
echo; echo "page elements:"
HTML=$(curl -s http://127.0.0.1:18497/)
for id in graphBuildStatus graphOpenBtn graphHistory collapseGraphRail revealGraphRail; do
  echo "$HTML" | grep -q "id=\"$id\"" && echo "  $id ok" || echo "  $id MISSING"
done
kill $PID 2>/dev/null; wait 2>/dev/null; rm -rf "$ROOT"
```
Verify: status `available:true`; history has one entry for `$ROOT/proj`; all 5 element IDs present.
- [ ] **Step 4:** Commit any stray fixes; otherwise done.

---

## Self-Review Notes

**Spec coverage:** A → Task 1 (409 message) + Task 2 (standalone block + folder label); B → Task 3; C → Task 1 (store/route/hooks) + Task 4 (list UI); D → Task 5.

**Placeholder scan:** none — Task 5 Step 1 deliberately delegates "read and mirror" for the sidebar-collapse pattern rather than reproducing fragile code; every other step has complete code.

**Type consistency:** `graphHistoryEntry{Dir,BuiltAt}` (Go) ↔ `entry.dir`/`entry.built_at` (JS). `recordBuild`/`loadGraphHistory`/`saveGraphHistory`/`graphHistoryPath` consistent. New element IDs (`graphBuildStatus`, `graphBuildStatusFolder`, `graphOpenBtn`, `graphHistory`, `collapseGraphRail`, `revealGraphRail`) each declared in DOM and referenced once in the JS ref block.

**Known limitation:** the history list is not live-updated while another client builds; it refreshes at boot and after this client's own builds. Acceptable for a single-user local tool.
