# Native Graph View Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`).

**Goal:** Replace the sandboxed `graph.html` preview with a native graph view rendered in MD Viewer's preview pane. Nodes are clickable — clicking a node calls `selectFile(file, push)` to open the file with browser-back support. The graph view itself is a routable state (`?graph=<dir>`) so back-navigation returns to it.

**Architecture:** A new `GET /api/graph/data?dir=<dir>` serves the raw `graph.json`. The frontend lazy-loads `vis-network` from a CDN on first use and renders into `previewBodyEl`. A focus mode (current file + 1–2 hop neighbors) is default; a toggle switches to the full graph. The existing router (`routeURL`/`routeFromLocation`/`restoreRoute`) is extended with a `graph` field.

**Tech Stack:** Go stdlib; vis-network ~9 from CDN, lazy-loaded.

**Builds on:** branch `feature/graphify-integration`, HEAD `3d92ad9`.

---

## File Structure

| File | Change |
|---|---|
| `web.go` | `/api/graph/data` route + handler; embedded frontend graph-view module (loader, render, focus/full toggle); router extended with `graph` field |
| `web_graph_test.go` | tests for `/api/graph/data` |

---

## Task 1: Backend — `GET /api/graph/data` route

**Files:** `web.go`, `web_graph_test.go`

- [ ] **Step 1: Add failing tests** — append to `web_graph_test.go`:

```go
func TestGraphDataReturnsJSON(t *testing.T) {
	s := newTestServer(t, true) // copies fixture graph.json to <root>/graphify-out/
	req := httptest.NewRequest("GET", "/api/graph/data?dir="+s.startDir, nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var doc map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["nodes"]; !ok {
		t.Errorf("response missing nodes key")
	}
	if _, ok := doc["links"]; !ok {
		t.Errorf("response missing links key")
	}
}

func TestGraphDataMissing(t *testing.T) {
	s := newTestServer(t, false) // no graph file
	req := httptest.NewRequest("GET", "/api/graph/data?dir="+s.startDir, nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
```

`"strings"` is already imported by `web_graph_test.go` (it's used by `TestGraphBuildMissingAPIKey`). Confirm.

- [ ] **Step 2: Run to verify FAIL**

`go test -run TestGraphData -count=1 ./...` → 404 not registered.

- [ ] **Step 3: Register the route** — in `routes()`, after `/api/graph/history`:

```go
	mux.HandleFunc("/api/graph/data", s.handleGraphData)
```

- [ ] **Step 4: Implement the handler** — append to `web.go`:

```go
// handleGraphData streams the raw graphify graph.json for the given dir.
// The native graph view fetches this and renders it client-side.
func (s *webServer) handleGraphData(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("dir")
	if dir == "" {
		dir = s.startDir
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		http.Error(w, "invalid dir", http.StatusBadRequest)
		return
	}
	jsonPath := filepath.Join(abs, "graphify-out", "graph.json")
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		http.Error(w, "no graph for "+abs, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}
```

- [ ] **Step 5: Verify**

`go test -run TestGraphData -count=1 ./...` → 2 PASS.
`go test -count=1 ./...` → full suite green.

- [ ] **Step 6: Commit**

```bash
git add web.go web_graph_test.go
git commit -m "feat(graph): GET /api/graph/data streams graph.json for rendering"
```

---

## Task 2: Frontend — vis-network lazy load + render graph in preview

**Files:** `web.go` (embedded HTML/CSS/JS)

This task adds the rendering pipeline. `openGraphView(dir, focusPath)` is the entry point. History integration is Task 3.

- [ ] **Step 1: Add CSS** — inside `<style>`, before the closing `</style>`:

```css
    .native-graph-host {
      position: relative;
      width: 100%;
      height: 100%;
      background: var(--code);
      border-radius: 8px;
      overflow: hidden;
    }
    .native-graph-canvas {
      width: 100%;
      height: 100%;
    }
    .native-graph-bar {
      position: absolute;
      top: 10px;
      left: 10px;
      right: 10px;
      display: flex;
      gap: 8px;
      align-items: center;
      pointer-events: none;
    }
    .native-graph-bar > * { pointer-events: auto; }
    .native-graph-bar .label {
      font-size: 11px;
      color: var(--muted);
      background: color-mix(in oklab, var(--panel) 80%, transparent);
      padding: 4px 8px;
      border-radius: 6px;
    }
    .native-graph-bar button {
      padding: 4px 10px;
      border: 1px solid var(--line);
      border-radius: 6px;
      background: var(--panel-2);
      color: var(--text);
      font-size: 12px;
      cursor: pointer;
    }
    .native-graph-bar button.active {
      border-color: var(--accent);
      color: var(--accent);
    }
    .native-graph-loading {
      position: absolute;
      inset: 0;
      display: flex;
      align-items: center;
      justify-content: center;
      font-size: 13px;
      color: var(--muted);
    }
```

- [ ] **Step 2: Add the lazy loader + cache helpers** — place this JS near the other graph helpers (e.g. just after `graphRelativeTime`):

```js
    // ---- native graph view ------------------------------------------------
    const VIS_NETWORK_CDN = "https://cdn.jsdelivr.net/npm/vis-network@9.1.9/standalone/umd/vis-network.min.js";
    let visNetworkPromise = null;
    function loadVisNetwork() {
      if (window.vis && window.vis.Network) return Promise.resolve();
      if (visNetworkPromise) return visNetworkPromise;
      visNetworkPromise = new Promise(function (resolve, reject) {
        const s = document.createElement("script");
        s.src = VIS_NETWORK_CDN;
        s.onload = function () { resolve(); };
        s.onerror = function () { reject(new Error("failed to load vis-network from CDN")); };
        document.head.appendChild(s);
      });
      return visNetworkPromise;
    }

    const graphDataCache = new Map(); // dir -> {nodes, links}
    async function fetchGraphData(dir) {
      if (graphDataCache.has(dir)) return graphDataCache.get(dir);
      const r = await fetch("/api/graph/data?dir=" + encodeURIComponent(dir));
      if (!r.ok) throw new Error("graph.json fetch failed: " + r.status);
      const doc = await r.json();
      graphDataCache.set(dir, doc);
      return doc;
    }

    // nodeColorFor maps file_type → a stable color so the same file type
    // gets the same color across renders.
    function nodeColorFor(fileType) {
      switch (fileType) {
        case "code": return "#7aa2f7";
        case "document": return "#9ece6a";
        case "paper": return "#e0af68";
        case "image": return "#bb9af7";
        case "concept": return "#f7768e";
        case "rationale": return "#7dcfff";
        default: return "#a9b1d6";
      }
    }

    // focusSubgraph returns a {nodes, links} subset containing the nodes
    // whose source_file matches focusPath plus their N-hop neighbours.
    function focusSubgraph(doc, focusPath, hops) {
      if (!focusPath) return doc;
      const nodes = doc.nodes || [];
      const links = doc.links || [];
      const seedIds = new Set();
      for (const n of nodes) {
        if (n.source_file === focusPath) seedIds.add(n.id);
      }
      if (!seedIds.size) return doc; // file not in graph — fall back to full
      const adj = new Map();
      for (const l of links) {
        if (!adj.has(l.source)) adj.set(l.source, []);
        if (!adj.has(l.target)) adj.set(l.target, []);
        adj.get(l.source).push(l.target);
        adj.get(l.target).push(l.source);
      }
      const keep = new Set(seedIds);
      let frontier = Array.from(seedIds);
      for (let h = 0; h < hops; h++) {
        const next = [];
        for (const id of frontier) {
          const ns = adj.get(id) || [];
          for (const nb of ns) {
            if (!keep.has(nb)) {
              keep.add(nb);
              next.push(nb);
            }
          }
        }
        frontier = next;
      }
      return {
        nodes: nodes.filter(function (n) { return keep.has(n.id); }),
        links: links.filter(function (l) { return keep.has(l.source) && keep.has(l.target); }),
      };
    }
```

- [ ] **Step 3: Add the main render function** — append (still next to the graph helpers):

```js
    // openGraphView replaces the preview body with a native graph render.
    // mode: "focus" (default — current file + neighbours) | "full".
    // Task 3 wires this into the router; for now it just renders.
    async function openGraphView(dir, focusPath, mode) {
      if (!dir) return;
      const useMode = mode || "focus";
      // Tear down any previous render.
      previewBodyEl.innerHTML = "";
      const host = document.createElement("div");
      host.className = "native-graph-host";
      const canvas = document.createElement("div");
      canvas.className = "native-graph-canvas";
      const bar = document.createElement("div");
      bar.className = "native-graph-bar";
      const label = document.createElement("span");
      label.className = "label";
      label.textContent = (useMode === "focus" ? "Focus" : "Full") +
        " · " + (dir.split("/").filter(Boolean).pop() || dir);
      bar.appendChild(label);
      const focusBtn = document.createElement("button");
      focusBtn.type = "button";
      focusBtn.textContent = "Focus";
      const fullBtn = document.createElement("button");
      fullBtn.type = "button";
      fullBtn.textContent = "Full";
      (useMode === "focus" ? focusBtn : fullBtn).classList.add("active");
      bar.appendChild(focusBtn);
      bar.appendChild(fullBtn);
      const loading = document.createElement("div");
      loading.className = "native-graph-loading";
      loading.textContent = "Loading graph…";
      host.appendChild(canvas);
      host.appendChild(bar);
      host.appendChild(loading);
      previewBodyEl.appendChild(host);

      let doc;
      try {
        await loadVisNetwork();
        doc = await fetchGraphData(dir);
      } catch (err) {
        loading.textContent = "Failed to load graph: " + (err && err.message ? err.message : err);
        return;
      }
      const sub = (useMode === "focus")
        ? focusSubgraph(doc, focusPath || "", 1)
        : doc;
      const visNodes = (sub.nodes || []).map(function (n) {
        return {
          id: n.id,
          label: n.label || n.id,
          color: { background: nodeColorFor(n.file_type), border: "transparent" },
          font: { color: "#cdd6f4", size: 12 },
          shape: "dot",
          size: 10,
          title: n.source_file || n.label,
          srcFile: n.source_file || "",
        };
      });
      const visEdges = (sub.links || []).map(function (l, i) {
        return { id: "e" + i, from: l.source, to: l.target,
                 color: { color: "rgba(160,160,200,0.35)" }, width: 1 };
      });
      loading.remove();
      const network = new window.vis.Network(canvas, {
        nodes: new window.vis.DataSet(visNodes),
        edges: new window.vis.DataSet(visEdges),
      }, {
        physics: { stabilization: { iterations: 80 } },
        interaction: { hover: true },
      });
      // Task 3 attaches the click → selectFile handler. Stash the network
      // and the toggle buttons on a window-scoped object so Task 3 can wire
      // them without re-declaring the function.
      window.__graphView = { network, focusBtn, fullBtn, dir, focusPath, useMode };
    }

    // Replace the existing graphOpenBtn click handler. It used to call
    // selectFile(<graph.html>) — now it opens the native view.
    graphOpenBtnEl.addEventListener("click", function () {
      if (!state.graphDir) return;
      openGraphView(state.graphDir, state.selectedPath || "", "focus");
    });
```

IMPORTANT: the existing `graphOpenBtnEl.addEventListener("click", ...)` from a previous task — DELETE the old one (it called `selectFile(<.../graph.html>)`). The new handler above is the only one.

- [ ] **Step 4: Build + smoke**

`go build ./...` → clean.
```bash
go build -o /tmp/mdv-gv .
ROOT=$(mktemp -d); mkdir -p "$ROOT/graphify-out"
cp testdata/graph_simple.json "$ROOT/graphify-out/graph.json"
/tmp/mdv-gv --web --port 18500 --root "$ROOT" >/dev/null 2>&1 &
PID=$!; sleep 1
echo "data route:"; curl -s "http://127.0.0.1:18500/api/graph/data?dir=$ROOT" | head -c 80
echo; echo "vis-network referenced:"; curl -s http://127.0.0.1:18500/ | grep -c "vis-network@9"
kill $PID 2>/dev/null; wait 2>/dev/null; rm -rf "$ROOT" /tmp/mdv-gv
```
Expected: data route returns JSON starting with `{`; HTML contains the vis-network CDN URL ≥1.

- [ ] **Step 5: Tests + commit**

`go test -count=1 ./...` → green.

```bash
git add web.go
git commit -m "feat(graph): native graph view in preview (vis-network lazy CDN)"
```

---

## Task 3: Frontend — node click → selectFile + history integration

**Files:** `web.go`

The router has `routeURL`/`routeFromLocation`/`restoreRoute`/`currentRoute`. Extend each to recognize a `graph` field, so `?graph=<dir>` is a routable state. Wire node-click to `selectFile`.

- [ ] **Step 1: Extend router types**

In `routeURL` (around line 2845), add the `graph` param BEFORE `dir`:
```js
    function routeURL(route) {
      const params = new URLSearchParams();
      if (route.graph) params.set("graph", route.graph);
      if (route.dir) params.set("dir", route.dir);
      if (route.path) params.set("path", route.path);
      if (route.hash) params.set("hash", route.hash);
      const query = params.toString();
      return query ? "?" + query : location.pathname;
    }
```

In `routeFromLocation` (around line 2865):
```js
    function routeFromLocation() {
      const params = new URLSearchParams(location.search);
      return {
        graph: params.get("graph") || "",
        dir: params.get("dir") || "",
        path: params.get("path") || "",
        hash: params.get("hash") || "",
      };
    }
```

In `currentRoute` (the function that builds the route object for `syncHistory`), include the active graph view if one is showing. Locate the function (it returns `{dir, path, hash}` — the snippet at line 2840 shows the structure). Replace its body to also include `graph`:
```js
    function currentRoute() {
      return {
        graph: state.activeGraphDir || "",
        dir: state.cwd || "",
        path: state.selectedPath || "",
        hash: state.selectedHash || "",
      };
    }
```

Add to the global `state` object:
```js
      activeGraphDir: "",
      activeGraphFocus: "",
      activeGraphMode: "",
```

- [ ] **Step 2: Track graph activation in `openGraphView`**

In `openGraphView` (added in Task 2), after the function-level setup but BEFORE the `await loadVisNetwork()` call, set state and sync history:
```js
      state.activeGraphDir = dir;
      state.activeGraphFocus = focusPath || "";
      state.activeGraphMode = useMode;
      if (!state.restoringHistory) {
        syncHistory("push");
      }
```

- [ ] **Step 3: Clear graph state when selecting a file**

`selectFile` already pushes history. We need `state.activeGraphDir` to be cleared BEFORE `selectFile` calls `syncHistory`, so the pushed route is the file route, not the graph route.

Find `selectFile` (around line ~3270 — search for `async function selectFile`). At its very top, before anything else, add:
```js
      // navigating to a file leaves the graph view
      state.activeGraphDir = "";
      state.activeGraphFocus = "";
      state.activeGraphMode = "";
```

`loadDir` also pushes history; add the same three lines at the top of `loadDir`'s body (before the fetch).

- [ ] **Step 4: Wire node-click on the network**

In `openGraphView`, RIGHT AFTER `const network = new window.vis.Network(...)`, add:
```js
      network.on("click", function (params) {
        if (!params || !params.nodes || !params.nodes.length) return;
        const id = params.nodes[0];
        const nodeData = (sub.nodes || []).find(function (n) { return n.id === id; });
        if (!nodeData || !nodeData.source_file) return;
        selectFile(nodeData.source_file, { historyMode: "push" });
      });
      focusBtn.addEventListener("click", function () {
        openGraphView(dir, focusPath || state.selectedPath || "", "focus");
      });
      fullBtn.addEventListener("click", function () {
        openGraphView(dir, focusPath || state.selectedPath || "", "full");
      });
```

- [ ] **Step 5: Extend `restoreRoute` to re-render the graph on popstate**

In `restoreRoute` (around line 3815), at the top of the `try` block (before the `const dir = route.dir || state.cwd || "";` line), add a branch:
```js
        if (route.graph) {
          // First make sure the right folder is loaded in the file list.
          if (route.dir && route.dir !== state.cwd) {
            await loadDir(route.dir, { historyMode: "", keepSelection: true });
          }
          await openGraphView(route.graph, route.path || "", "focus");
          return;
        }
```

Wait — there's a subtlety. `openGraphView` does its own `syncHistory("push")`. But during `restoringHistory`, we don't want it to push. The Task 2 code already guards: `if (!state.restoringHistory) { syncHistory("push"); }`. So restoreRoute's flag (`state.restoringHistory = true`) prevents the duplicate push. Good.

Also — `openGraphView` sets `state.activeGraphDir` BEFORE returning, which is what `currentRoute` needs to read on subsequent pushes. After restoreRoute returns and `state.restoringHistory` flips back to false, the next selectFile will see `activeGraphDir` set and... wait, it shouldn't. The user opened the graph; next click should be either a node (→ selectFile clears it) or popstate (which re-runs restoreRoute). So at the moment we're showing the graph, `state.activeGraphDir` is set, and any further interaction clears it via selectFile/loadDir. Good.

- [ ] **Step 6: Boot-restore the graph state**

The bootstrap calls `restoreRoute(routeFromLocation(), "replace")` (around line 5071). Since `routeFromLocation` now reads `graph`, if the user reloads while showing the graph (URL has `?graph=...`), the boot restore will hit the new branch and re-render. No change needed beyond the work already in Step 5.

- [ ] **Step 7: Build + smoke**

`go build ./...` → clean.
```bash
go build -o /tmp/mdv-gv3 .
ROOT=$(mktemp -d); mkdir -p "$ROOT/graphify-out" "$ROOT/auth" "$ROOT/docs"
cp testdata/graph_simple.json "$ROOT/graphify-out/graph.json"
echo x > "$ROOT/auth/session.go"; echo x > "$ROOT/auth/login.go"; echo x > "$ROOT/docs/intro.md"
/tmp/mdv-gv3 --web --port 18501 --root "$ROOT" >/dev/null 2>&1 &
PID=$!; sleep 1
HTML=$(curl -s http://127.0.0.1:18501/)
echo "openGraphView defined: $(echo "$HTML" | grep -c 'function openGraphView')"
echo "?graph route handled: $(echo "$HTML" | grep -c 'route.graph')"
kill $PID 2>/dev/null; wait 2>/dev/null; rm -rf "$ROOT" /tmp/mdv-gv3
```
Both counts ≥ 1.

- [ ] **Step 8: Tests + commit**

`go test -count=1 ./...` → green.

```bash
git add web.go
git commit -m "feat(graph): node-click → selectFile, history-integrated graph view"
```

---

## Task 4: Frontend — focus / full toggle UX polish

**Files:** `web.go`

Task 2/3 already wired the focus/full buttons to re-open the view. This task adds:
1. The toggle visibly reflects the current mode (Task 2 already did `classList.add("active")` on the right button — verify).
2. When in focus mode and the current file isn't in the graph, render the full graph and show a small "file not in graph — showing full graph" note.

- [ ] **Step 1: Update `openGraphView` to expose a fallback note**

In `openGraphView`, after the `const sub = ...` line:
```js
      const fellBackToFull = (useMode === "focus" && focusPath && sub === doc);
      // (focusSubgraph returns the unmodified doc when the focus file
      // isn't in the graph — sub === doc identifies that case.)
      if (fellBackToFull) {
        const note = document.createElement("span");
        note.className = "label";
        note.textContent = "(file not in graph — showing full)";
        bar.appendChild(note);
      }
```

- [ ] **Step 2: Build + smoke + tests**

`go build ./...` → clean.
`go test -count=1 ./...` → green.

- [ ] **Step 3: Commit**

```bash
git add web.go
git commit -m "feat(graph): focus-mode fallback note when file is absent from graph"
```

---

## Task 5: Integration acceptance

**Files:** none.

- [ ] **Step 1:** `go test -count=1 ./...` and `go test -race -count=1 ./...` → green.
- [ ] **Step 2:** `go build -o mdviewer .` → success.
- [ ] **Step 3:** End-to-end:
```bash
ROOT=$(mktemp -d); mkdir -p "$ROOT/graphify-out" "$ROOT/auth"
cp testdata/graph_simple.json "$ROOT/graphify-out/graph.json"
echo x > "$ROOT/auth/session.go"
./mdviewer --web --port 18502 --root "$ROOT" >/dev/null 2>&1 &
PID=$!; sleep 1
echo "graph data ok:"; curl -s "http://127.0.0.1:18502/api/graph/data?dir=$ROOT" | head -c 50; echo
echo "elements:"
HTML=$(curl -s http://127.0.0.1:18502/)
echo "$HTML" | grep -q "openGraphView" && echo "  openGraphView ok"
echo "$HTML" | grep -q "VIS_NETWORK_CDN" && echo "  CDN constant ok"
echo "$HTML" | grep -q "route.graph" && echo "  router branch ok"
kill $PID 2>/dev/null; wait 2>/dev/null; rm -rf "$ROOT"
```
- [ ] **Step 4:** Commit any stray fixes; otherwise done.

---

## Self-Review Notes

**Spec coverage:** native render in preview pane → Task 2; node→selectFile + history → Task 3; focus/full toggle → Tasks 2+3+4; back-navigation → Task 3 via router extension.

**Type consistency:** `route.graph` shared across `routeURL`/`routeFromLocation`/`currentRoute`/`restoreRoute`. `state.activeGraphDir`/`Focus`/`Mode` consistent.

**Known limitations:** vis-network is fetched from jsdelivr CDN — requires internet on first use (cached by browser afterwards). The 600KB library loads only when the user opens the graph view. No interactive search / community filtering — graphify's `graph.html` retains those (link via "Open in new tab" remains an option for power users; can be added in a follow-up).
