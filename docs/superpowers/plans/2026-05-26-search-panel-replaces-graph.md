# Search Panel Replaces Graph — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`).

**Goal:** Remove all graph-related features from the right panel and replace them with a search panel. The new panel has a search input, an "In this file" section (matches in the currently viewed file with click-to-scroll + inline highlight), and a "Same folder" section listing other files containing the query (file name + match count, click to open).

**Architecture:** A new `GET /api/search?dir=&q=` route does a same-folder grep (text/markdown files, skip binaries) and returns `[{path, count, snippets}]`. The frontend right panel is renamed from "graph rail" to "search panel" — the existing aside + collapse toggle infrastructure is kept (the user values being able to hide the panel), only the contents and identifiers change. In-file search is client-side: walk the preview's text nodes, wrap matches in `<mark>`, render a clickable list.

**Tech Stack:** Go stdlib (`bufio`, `bytes`, `os`, `path/filepath`); embedded HTML/CSS/JS in `web.go`.

**Builds on:** branch `feature/graphify-integration`, HEAD `78cefcc`.

---

## File Structure

| File | Action |
|---|---|
| `graph.go`, `graph_test.go`, `graph_build.go`, `graph_build_test.go`, `web_graph_test.go`, `testdata/graph_simple.json` | **DELETE** |
| `docs/superpowers/specs/2026-05-21-graphify-integration-design.md` | **MOVE** to `docs/superpowers/archive/2026-graphify/` |
| `docs/superpowers/plans/2026-05-21-graphify-*.md`, `2026-05-26-graphify-native-graph-view.md` | **MOVE** to `docs/superpowers/archive/2026-graphify/` |
| `web.go` | **MAJOR REWRITE** of the right panel & all graph touchpoints |
| `menubar.go` | drop graph field inits |
| `.gitignore` | remove `.mdviewer_graph_history.json` line |
| `search_test.go` (new) | tests for the new search backend |

---

## Task 1: Remove graph code & archive plan/spec docs

**Files:** delete the listed `.go` and JSON files, `git mv` the doc set, rewrite `web.go` and `menubar.go` to drop all graph touchpoints. After this task the build still compiles and `go test ./...` passes (with fewer tests).

The right panel's outer `<aside>` element + the existing collapse/reveal toggle infrastructure are **kept** but renamed: `graphRail`→`searchPanel`, class `.graph-rail`→`.search-panel`, CSS var `--graph-rail-width`→`--search-panel-width`, collapse class `.graph-rail-collapsed`→`.search-panel-collapsed`, button IDs `collapseGraphRail`/`revealGraphRail`→`collapseSearchPanel`/`revealSearchPanel`, state `state.graphRailCollapsed`→`state.searchPanelCollapsed`, localStorage key `mdviewer.graphRailCollapsed`→`mdviewer.searchPanelCollapsed`. The aside's interior is gutted; Task 3 fills it.

- [ ] **Step 1: Delete graph code files**

```bash
git rm graph.go graph_test.go graph_build.go graph_build_test.go web_graph_test.go testdata/graph_simple.json
rmdir testdata 2>/dev/null || true
```

- [ ] **Step 2: Archive the plan / spec docs**

```bash
mkdir -p docs/superpowers/archive/2026-graphify
git mv docs/superpowers/specs/2026-05-21-graphify-integration-design.md docs/superpowers/archive/2026-graphify/
git mv docs/superpowers/plans/2026-05-21-graphify-integration.md docs/superpowers/archive/2026-graphify/
git mv docs/superpowers/plans/2026-05-21-graphify-build-backends.md docs/superpowers/archive/2026-graphify/
git mv docs/superpowers/plans/2026-05-21-graphify-per-folder.md docs/superpowers/archive/2026-graphify/
git mv docs/superpowers/plans/2026-05-21-graphify-build-progress.md docs/superpowers/archive/2026-graphify/
git mv docs/superpowers/plans/2026-05-21-graphify-rail-ux.md docs/superpowers/archive/2026-graphify/
git mv docs/superpowers/plans/2026-05-26-graphify-native-graph-view.md docs/superpowers/archive/2026-graphify/
```

- [ ] **Step 3: Surgical cleanup of `web.go`**

Remove EVERY graph-related identifier. Use grep to locate and delete. The deletions span CSS, HTML, JS in the embedded `webAppHTML`, plus Go routes/handlers.

**Go side** — remove from `webServer` struct: `graphMu`, `graphCache`, `historyMu`, `buildManager`. Remove all of these methods/handlers/types (delete the entire function): `tryLoadGraph` (if still exists), `currentGraph` (if still exists), `graphForDir`, `invalidateGraph`, `loadGraphHistory`, `saveGraphHistory`, `recordBuild`, `graphHistoryPath`, `handleGraphStatus`, `handleGraphFile`, `handleGraphConcept`, `handleGraphBuild`, `handleGraphBuildStatus`, `handleGraphBackends`, `handleGraphHistory`, `handleGraphData`, the types `graphStatusResponse` and `graphHistoryEntry`, the constants `graphHistoryFileName` and any `vis-network` / `GraphIndex` references. Remove the corresponding `mux.HandleFunc("/api/graph/*", ...)` lines from `routes()` (all of them).

**HTML/CSS/JS side** (inside `webAppHTML`) — remove every `.graph-*` CSS rule (`.graph-rail` rule is RENAMED to `.search-panel` in the same edit; see below), every `id="graph*"` element except keep the outer aside (renamed). Remove all `graph*El` const refs and all graph JS functions (`refreshGraphStatus`, `loadConceptsForFile`, `activateConcept`, `loadGraphBackends`, `startGraphBuild`, `loadGraphHistory`, `openGraphView`, `loadVisNetwork`, `fetchGraphData`, `nodeColorFor`, `focusSubgraph`, `graphRelativeTime`, the `VIS_NETWORK_CDN` constant, the `graphDataCache` Map, `applyGraphRailCollapsed`). Remove all state fields starting with `graph` (`graphAvailable`, `graphConcepts`, `graphActiveNodeId`, `graphDir`, `activeGraphDir`, `activeGraphFocus`, `activeGraphMode`, `graphRailCollapsed`). Remove the `?graph=` branch from `restoreRoute`, the `graph` field from `routeURL`/`routeFromLocation`/`currentRoute`, the `activeGraph*` clears from the top of `selectFile`/`loadDir`. Remove every boot call to graph functions (`refreshGraphStatus()`, `loadGraphBackends()`, `loadGraphHistory()`, `applyGraphRailCollapsed()`).

**RENAME (keep but rename)** — the right-panel shell and its collapse toggle:
- CSS: rename `--graph-rail-width` → `--search-panel-width`. Rename `.graph-rail` → `.search-panel`, `.app.graph-rail-collapsed` → `.app.search-panel-collapsed` (everywhere — grid rules, hide rule). Rename `.collapse-graph-rail` → `.collapse-search-panel`. Rename `.reveal-graph-rail` → `.reveal-search-panel`. Rename `.app.graph-rail-collapsed ~ .reveal-graph-rail` → `.app.search-panel-collapsed ~ .reveal-search-panel`.
- HTML: `<aside id="graphRail" class="shell graph-rail" ...>` → `<aside id="searchPanel" class="shell search-panel" aria-label="Search panel">`. Empty its interior except for the collapse button (which is renamed in the next bullet). Buttons: `id="collapseGraphRail"` → `id="collapseSearchPanel"` (and class `.collapse-graph-rail` → `.collapse-search-panel`), `id="revealGraphRail"` (text "✦ Graph") → `id="revealSearchPanel"` (text "🔍 Search"), and `.reveal-graph-rail` → `.reveal-search-panel`.
- JS: rename refs `collapseGraphRailEl`/`revealGraphRailEl` → `collapseSearchPanelEl`/`revealSearchPanelEl`. Rename `state.graphRailCollapsed` → `state.searchPanelCollapsed`. Rename localStorage key `mdviewer.graphRailCollapsed` → `mdviewer.searchPanelCollapsed`. Rename the apply function `applyGraphRailCollapsed` → `applySearchPanelCollapsed`. Rename the click handlers and the boot restore accordingly.

After this step the right panel is an empty (renamed) aside with just the collapse button; Task 3 fills it.

- [ ] **Step 4: Clean up `menubar.go`**

In `runMenuBarApp`, the `webServer` struct literal currently has `graphPath`/`graphCache` and a `server.tryLoadGraph()` / `server.buildManager = newBuildManager()` invocation. Remove those — leave only `startDir` and `appRoot`:

```go
		server := &webServer{
			startDir: startDir,
			appRoot:  appRoot,
		}
```

Remove `"path/filepath"` import if unused after the change (it's only used to construct graphPath now; verify and remove).

- [ ] **Step 5: Update `.gitignore`**

Remove the `.mdviewer_graph_history.json` line. Keep `graphify-out/` (still a reasonable ignore for users who run graphify externally).

- [ ] **Step 6: Build + tests**

```bash
go build ./...     # clean
go test ./...      # passes (test count drops — graph tests are gone)
```

If build fails because some graph identifier is still referenced, grep web.go for the identifier and delete. The cleanest verification: `grep -nE 'graph|Graph' web.go menubar.go` should return only the renamed panel identifiers (`searchPanel`, etc.) and any unrelated word matches — no references to deleted graph functions.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "refactor: remove graph features and archive plan docs"
```

---

## Task 2: Backend — `GET /api/search?dir=&q=`

**Files:** `web.go`, `search_test.go` (new)

A grep-like endpoint that scans non-binary text files in the given dir (non-recursive) and reports per-file match counts + short snippets.

- [ ] **Step 1: Write the failing tests** — create `search_test.go`:

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
	writeTestFile(t, root, "beta.md",  "# Beta\nNo matches here\n")
	writeTestFile(t, root, "gamma.txt", "world of text\n")
	// binary should be skipped
	if err := os.WriteFile(filepath.Join(root, "blob.bin"),
		[]byte{0, 1, 2, 3, 'w','o','r','l','d'}, 0o644); err != nil {
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
```

- [ ] **Step 2: Run to verify FAIL**

`go test -run TestSearch ./...` → build errors (`searchResult`, route undefined).

- [ ] **Step 3: Implement** — append to `web.go`:

```go
const searchMaxSnippets = 3
const searchSnippetLen = 60
const searchMaxFileBytes = 2 * 1024 * 1024 // skip files larger than 2 MB

type searchResult struct {
	Path     string   `json:"path"`
	Count    int      `json:"count"`
	Snippets []string `json:"snippets"`
}

// isProbablyText returns true if the byte slice looks like text content.
// We declare "binary" when it contains a NUL byte in the prefix — the same
// heuristic git uses.
func isProbablyText(b []byte) bool {
	limit := len(b)
	if limit > 8000 {
		limit = 8000
	}
	for i := 0; i < limit; i++ {
		if b[i] == 0 {
			return false
		}
	}
	return true
}

func (s *webServer) handleSearch(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("dir")
	if dir == "" {
		dir = s.startDir
	}
	q := r.URL.Query().Get("q")
	if q == "" {
		http.Error(w, "missing q", http.StatusBadRequest)
		return
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		http.Error(w, "invalid dir", http.StatusBadRequest)
		return
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	needle := strings.ToLower(q)
	out := []searchResult{}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.Size() > searchMaxFileBytes {
			continue
		}
		full := filepath.Join(abs, e.Name())
		data, err := os.ReadFile(full)
		if err != nil || !isProbablyText(data) {
			continue
		}
		lower := strings.ToLower(string(data))
		count := strings.Count(lower, needle)
		if count == 0 {
			continue
		}
		snippets := collectSnippets(string(data), lower, needle, searchMaxSnippets)
		out = append(out, searchResult{Path: full, Count: count, Snippets: snippets})
	}
	// Most matches first.
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	s.writeJSON(w, http.StatusOK, out)
}

// collectSnippets returns up to maxCount short context strings around the
// first matches of needle in haystackLower (which must be the lowercase
// of haystack).
func collectSnippets(haystack, haystackLower, needleLower string, maxCount int) []string {
	out := []string{}
	from := 0
	for i := 0; i < maxCount; i++ {
		idx := strings.Index(haystackLower[from:], needleLower)
		if idx < 0 {
			break
		}
		abs := from + idx
		startCtx := abs - searchSnippetLen/2
		if startCtx < 0 {
			startCtx = 0
		}
		endCtx := abs + len(needleLower) + searchSnippetLen/2
		if endCtx > len(haystack) {
			endCtx = len(haystack)
		}
		snip := haystack[startCtx:endCtx]
		// trim newlines for a clean one-line preview
		snip = strings.ReplaceAll(snip, "\n", " ")
		snip = strings.TrimSpace(snip)
		out = append(out, snip)
		from = abs + len(needleLower)
	}
	return out
}
```

Add `"sort"` and `"strings"` to `web.go` imports if missing.

- [ ] **Step 4: Register the route**

In `routes()`, add:
```go
	mux.HandleFunc("/api/search", s.handleSearch)
```

- [ ] **Step 5: Verify**

`go test -run TestSearch -count=1 ./...` → 3 PASS.
`go test -count=1 ./...` → full suite green.

- [ ] **Step 6: Commit**

```bash
git add web.go search_test.go
git commit -m "feat(search): GET /api/search — same-folder text grep with snippets"
```

---

## Task 3: Frontend — search panel shell

**Files:** `web.go` (embedded HTML/CSS/JS).

- [ ] **Step 1: Add CSS** — inside `<style>` (place near where the graph rail styles used to live):

```css
    .search-panel-body {
      display: flex;
      flex-direction: column;
      gap: 14px;
    }
    .search-input {
      width: 100%;
      padding: 7px 10px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: var(--panel-2);
      color: var(--text);
      font-size: 13px;
    }
    .search-input:focus {
      outline: none;
      border-color: var(--accent);
    }
    .search-summary {
      font-size: 11px;
      color: var(--muted);
    }
    .search-section-title {
      font-size: 11px;
      text-transform: uppercase;
      letter-spacing: .18em;
      color: var(--muted);
    }
    .search-hit-list {
      display: flex;
      flex-direction: column;
      gap: 3px;
      max-height: 35vh;
      overflow-y: auto;
    }
    .search-hit {
      padding: 6px 8px;
      border-radius: 6px;
      cursor: pointer;
      font-size: 12px;
      color: var(--text);
      line-height: 1.45;
    }
    .search-hit:hover { background: var(--panel-2); }
    .search-hit .search-hit-needle {
      color: var(--accent);
      font-weight: 600;
    }
    .search-file-row {
      padding: 6px 8px;
      border-radius: 6px;
      cursor: pointer;
      font-size: 13px;
      color: var(--text);
      display: flex;
      justify-content: space-between;
      gap: 8px;
    }
    .search-file-row:hover { background: var(--panel-2); }
    .search-file-row .search-file-count {
      color: var(--muted);
      font-size: 11px;
    }
    .search-empty {
      color: var(--muted);
      font-size: 12px;
      font-style: italic;
    }
    mark.search-mark {
      background: color-mix(in oklab, var(--accent) 35%, transparent);
      color: inherit;
      border-radius: 3px;
      padding: 0 2px;
    }
    mark.search-mark.current {
      background: var(--accent);
      color: var(--bg);
    }
```

- [ ] **Step 2: Fill the `#searchPanel` aside**

Replace the empty interior of the renamed `<aside id="searchPanel">` (which currently has only the collapse button after Task 1) with:

```html
      <button class="action collapse-search-panel" id="collapseSearchPanel" type="button" title="Hide search panel">›</button>
      <div class="search-panel-body">
        <input type="search" class="search-input" id="searchPanelInput" placeholder="Search in this folder…" spellcheck="false" autocomplete="off" />
        <div>
          <div class="search-section-title">In this file</div>
          <div class="search-summary" id="searchInFileSummary">Type to search.</div>
          <div class="search-hit-list" id="searchInFileHits"></div>
        </div>
        <div>
          <div class="search-section-title">Same folder</div>
          <div class="search-hit-list" id="searchFolderHits"></div>
        </div>
      </div>
```

The collapse button stays at the top; the `<input>` + two sections form the body.

- [ ] **Step 3: Add element refs + boot wiring**

In the ref block, add:
```js
    const searchPanelInputEl = document.getElementById("searchPanelInput");
    const searchInFileSummaryEl = document.getElementById("searchInFileSummary");
    const searchInFileHitsEl = document.getElementById("searchInFileHits");
    const searchFolderHitsEl = document.getElementById("searchFolderHits");
```

Add to global `state`:
```js
      searchQueryRight: "",   // distinct from the left-sidebar file-name search
      searchInFileHits: [],   // array of <mark> elements in preview order
      searchInFileFocus: -1,  // index of the currently emphasized hit
```

- [ ] **Step 4: Build + tests**

`go build ./...` → clean. `go test -count=1 ./...` → green. Visually the right panel now shows an input + two empty sections.

- [ ] **Step 5: Commit**

```bash
git add web.go
git commit -m "feat(search): right-panel search UI shell"
```

---

## Task 4: Frontend — in-file search + inline highlight

**Files:** `web.go`.

- [ ] **Step 1: Implement the in-file search routine** — add near the other graph→search helpers (place after the `searchFolderHitsEl` ref):

```js
    // clearInFileHighlights removes any <mark.search-mark> wrappers from
    // previewBodyEl, restoring the text nodes.
    function clearInFileHighlights() {
      const marks = previewBodyEl.querySelectorAll("mark.search-mark");
      for (const m of marks) {
        const parent = m.parentNode;
        if (!parent) continue;
        parent.replaceChild(document.createTextNode(m.textContent || ""), m);
        parent.normalize();
      }
      state.searchInFileHits = [];
      state.searchInFileFocus = -1;
    }

    // walkTextNodes yields every text node descendant of `root` that has
    // non-empty content and isn't inside a SCRIPT/STYLE/MARK element.
    function walkTextNodes(root, visit) {
      const SKIP = { SCRIPT: 1, STYLE: 1, MARK: 1 };
      const stack = [root];
      while (stack.length) {
        const node = stack.pop();
        for (const child of Array.from(node.childNodes)) {
          if (child.nodeType === 1) {
            if (!SKIP[child.tagName]) stack.push(child);
          } else if (child.nodeType === 3) {
            if (child.nodeValue && child.nodeValue.length) visit(child);
          }
        }
      }
    }

    // highlightInFile wraps each occurrence of needle in previewBodyEl with
    // <mark class="search-mark">. Case-insensitive. Returns the array of
    // mark elements in document order.
    function highlightInFile(needle) {
      clearInFileHighlights();
      if (!needle) return [];
      const lower = needle.toLowerCase();
      const len = needle.length;
      const hits = [];
      const targets = [];
      walkTextNodes(previewBodyEl, function (t) { targets.push(t); });
      for (const node of targets) {
        const text = node.nodeValue || "";
        const lo = text.toLowerCase();
        let idx = lo.indexOf(lower);
        if (idx < 0) continue;
        const parent = node.parentNode;
        if (!parent) continue;
        let cursor = 0;
        const frag = document.createDocumentFragment();
        while (idx >= 0) {
          if (idx > cursor) {
            frag.appendChild(document.createTextNode(text.substring(cursor, idx)));
          }
          const mark = document.createElement("mark");
          mark.className = "search-mark";
          mark.textContent = text.substring(idx, idx + len);
          frag.appendChild(mark);
          hits.push(mark);
          cursor = idx + len;
          idx = lo.indexOf(lower, cursor);
        }
        if (cursor < text.length) {
          frag.appendChild(document.createTextNode(text.substring(cursor)));
        }
        parent.replaceChild(frag, node);
      }
      state.searchInFileHits = hits;
      state.searchInFileFocus = -1;
      return hits;
    }

    // focusHit scrolls to the i-th in-file hit and emphasises it.
    function focusHit(i) {
      const hits = state.searchInFileHits;
      if (!hits.length) return;
      if (state.searchInFileFocus >= 0 && hits[state.searchInFileFocus]) {
        hits[state.searchInFileFocus].classList.remove("current");
      }
      const clamped = Math.max(0, Math.min(i, hits.length - 1));
      state.searchInFileFocus = clamped;
      const mark = hits[clamped];
      mark.classList.add("current");
      mark.scrollIntoView({ behavior: "smooth", block: "center" });
    }

    // renderInFileResults updates the summary + clickable hit list in the
    // right panel.
    function renderInFileResults(needle, hits) {
      searchInFileHitsEl.innerHTML = "";
      if (!needle) {
        searchInFileSummaryEl.textContent = "Type to search.";
        return;
      }
      if (!hits.length) {
        searchInFileSummaryEl.textContent = "No matches in this file.";
        return;
      }
      searchInFileSummaryEl.textContent = hits.length + " match" + (hits.length === 1 ? "" : "es");
      const maxList = 50;
      const shown = hits.slice(0, maxList);
      for (let i = 0; i < shown.length; i++) {
        const mark = shown[i];
        const ctxBefore = (mark.previousSibling && mark.previousSibling.nodeValue) || "";
        const ctxAfter  = (mark.nextSibling && mark.nextSibling.nodeValue) || "";
        const row = document.createElement("div");
        row.className = "search-hit";
        const pre = document.createElement("span");
        pre.textContent = "…" + ctxBefore.slice(-30);
        const hit = document.createElement("span");
        hit.className = "search-hit-needle";
        hit.textContent = mark.textContent;
        const post = document.createElement("span");
        post.textContent = ctxAfter.slice(0, 30) + "…";
        row.appendChild(pre);
        row.appendChild(hit);
        row.appendChild(post);
        row.addEventListener("click", function () { focusHit(i); });
        searchInFileHitsEl.appendChild(row);
      }
      if (hits.length > maxList) {
        const more = document.createElement("div");
        more.className = "search-empty";
        more.textContent = (hits.length - maxList) + " more matches not shown.";
        searchInFileHitsEl.appendChild(more);
      }
    }

    // runInFileSearch ties the two together.
    function runInFileSearch(needle) {
      const hits = highlightInFile(needle);
      renderInFileResults(needle, hits);
    }
```

- [ ] **Step 2: Wire the input + clear on file change**

Add a debounced handler on the search input (after the boot wiring):
```js
    let searchDebounce = null;
    searchPanelInputEl.addEventListener("input", function () {
      state.searchQueryRight = searchPanelInputEl.value || "";
      clearTimeout(searchDebounce);
      searchDebounce = setTimeout(function () {
        runInFileSearch(state.searchQueryRight);
        runFolderSearch(state.searchQueryRight); // Task 5 implements this
      }, 120);
    });
```

`runFolderSearch` is declared in Task 5 but referenced here — define a stub for Task 4 so the build passes:
```js
    function runFolderSearch(needle) { /* Task 5 fills this in */ }
```

When the user opens a new file, the existing highlights become stale (the previewBodyEl content changed). At the end of `selectFile` (the very end, AFTER the file content has rendered), re-apply:
```js
      // re-run in-file search on the newly rendered preview, if a query
      // is active.
      if (state.searchQueryRight) {
        runInFileSearch(state.searchQueryRight);
      }
```

- [ ] **Step 3: Build + tests**

`go build ./...` → clean. `go test -count=1 ./...` → green. Open the app, type into the right-panel search → matches highlight in the preview and a clickable list appears.

- [ ] **Step 4: Commit**

```bash
git add web.go
git commit -m "feat(search): in-file matches with inline highlight and click-to-scroll"
```

---

## Task 5: Frontend — cross-file results (same folder)

**Files:** `web.go`.

- [ ] **Step 1: Implement `runFolderSearch`** — replace the Task-4 stub:

```js
    let searchFolderAbort = null;
    async function runFolderSearch(needle) {
      searchFolderHitsEl.innerHTML = "";
      if (!needle) return;
      if (searchFolderAbort) { try { searchFolderAbort.abort(); } catch (e) {} }
      const ctrl = new AbortController();
      searchFolderAbort = ctrl;
      let results = [];
      try {
        const url = "/api/search?dir=" + encodeURIComponent(state.cwd || "") +
                    "&q=" + encodeURIComponent(needle);
        const r = await fetch(url, { signal: ctrl.signal });
        if (!r.ok) throw new Error(String(r.status));
        results = await r.json();
      } catch (err) {
        if (err && err.name === "AbortError") return;
        const e = document.createElement("div");
        e.className = "search-empty";
        e.textContent = "Search failed.";
        searchFolderHitsEl.appendChild(e);
        return;
      }
      // Hide the currently-open file from the cross-file list — its
      // matches are already in the "In this file" section.
      const filtered = results.filter(function (r) {
        return r.path !== state.selectedPath;
      });
      if (!filtered.length) {
        const e = document.createElement("div");
        e.className = "search-empty";
        e.textContent = "No matches in other files.";
        searchFolderHitsEl.appendChild(e);
        return;
      }
      for (const r of filtered) {
        const row = document.createElement("div");
        row.className = "search-file-row";
        row.title = r.path;
        const name = document.createElement("span");
        name.textContent = r.path.split("/").pop();
        const count = document.createElement("span");
        count.className = "search-file-count";
        count.textContent = r.count + (r.count === 1 ? " match" : " matches");
        row.appendChild(name);
        row.appendChild(count);
        row.addEventListener("click", function () {
          selectFile(r.path, { historyMode: "push" });
        });
        searchFolderHitsEl.appendChild(row);
      }
    }
```

The `selectFile` end-hook from Task 4 (re-running the in-file search after a file change) means: after the user clicks a row, `selectFile` loads the file then re-applies the in-file search on the newly rendered content. The cross-file list itself doesn't need to refresh — the same folder is being searched and the file the user just opened is filtered out.

- [ ] **Step 2: Build + smoke**

`go build ./...` → clean.
```bash
go build -o /tmp/mdv-s .
ROOT=$(mktemp -d)
printf '# Alpha\nHello world\nHello again\n' > "$ROOT/alpha.md"
printf '# Beta\nNo matches here\n'           > "$ROOT/beta.md"
printf 'world of text\n'                     > "$ROOT/gamma.txt"
/tmp/mdv-s --web --port 18510 --root "$ROOT" >/dev/null 2>&1 &
PID=$!; sleep 1
echo "search route:"
curl -s "http://127.0.0.1:18510/api/search?dir=$ROOT&q=world"
echo
kill $PID 2>/dev/null; wait 2>/dev/null; rm -rf "$ROOT" /tmp/mdv-s
```
Expected: JSON array with two entries (alpha.md count 1, gamma.txt count 1).

- [ ] **Step 3: Tests**

`go test -count=1 ./...` → green.

- [ ] **Step 4: Commit**

```bash
git add web.go
git commit -m "feat(search): same-folder cross-file results with click-to-open"
```

---

## Task 6: Integration acceptance

**Files:** none.

- [ ] **Step 1:** `go test -count=1 ./...` and `go test -race -count=1 ./...` → green.
- [ ] **Step 2:** `go build -o mdviewer .` → success.
- [ ] **Step 3:** End-to-end check:
```bash
ROOT=$(mktemp -d)
printf '# Alpha\nHello world\nHello again world\n' > "$ROOT/alpha.md"
printf '# Beta\nNo matches\n'                       > "$ROOT/beta.md"
printf 'world of text\nmore world here\n'           > "$ROOT/gamma.txt"
./mdviewer --web --port 18511 --root "$ROOT" >/dev/null 2>&1 &
PID=$!; sleep 1
echo "search alpha:"
curl -s "http://127.0.0.1:18511/api/search?dir=$ROOT&q=world" | head -c 200
echo; echo "elements:"
HTML=$(curl -s http://127.0.0.1:18511/)
for id in searchPanel searchPanelInput searchInFileHits searchFolderHits collapseSearchPanel revealSearchPanel; do
  echo "$HTML" | grep -q "id=\"$id\"" && echo "  $id ok" || echo "  $id MISSING"
done
kill $PID 2>/dev/null; wait 2>/dev/null; rm -rf "$ROOT"
```
Verify: search returns alpha.md (2) and gamma.txt (2). All 6 element IDs present.

- [ ] **Step 4:** Reinstall the menubar app:
```bash
bash scripts/install.sh --root /Users/1111038/Desktop/ATDT_Tech/MD_Viewer --port 8421
```

- [ ] **Step 5:** Commit any stray fixes; otherwise done.

---

## Self-Review Notes

**Spec coverage:** graph features removed → Task 1; search backend → Task 2; UI shell → Task 3; in-file matches + highlight + click-to-scroll → Task 4; same-folder cross-file results + open-on-click → Task 5.

**Placeholder scan:** none — every step has complete code.

**Known limitations:**
- Non-recursive search by design (the user picked same-folder-only).
- Highlights live in `previewBodyEl`; iframe-rendered HTML files (e.g. graphify output) cannot be highlighted because the content is in another document — the cross-frame iframe contents are out of scope.
- Files larger than 2 MB are skipped by the backend.
- Case-insensitive only. No regex / no whole-word toggle in v1.
