# graphify Build Progress + Last-Built Display Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`).

**Goal:** Show (1) when the current folder's graph was last built, and (2) a phase-based progress bar with an elapsed timer while a build runs.

**Architecture:** `/api/graph/status` reports `built_at` = the graph.json file mtime. The frontend renders a "Last built …" line when a graph exists, and during a build maps each SSE `phase` event to a progress percentage, animating a bar plus a per-second elapsed timer.

**Tech Stack:** Go 1.22 stdlib; embedded HTML/CSS/JS in `web.go`.

**Builds on:** branch `feature/graphify-integration`, HEAD `660bcf5`.

**Honest scope note:** graphify does not emit a numeric percentage. Progress is derived from discrete phases (`detect`/`extract`/`cluster`/`report`/`done`) already classified by `phaseFromLine` in `graph_build.go`. For agent-CLI backends the phase signal is less reliable, so the elapsed timer is the always-correct secondary indicator.

---

## File Structure

| File | Change |
|---|---|
| `web.go` | `graphStatusResponse` gains `BuiltAt`; `handleGraphStatus` stats graph.json; frontend gets last-built line + progress bar |
| `web_graph_test.go` | Test that `built_at` is populated when a graph exists |

---

## Task 1: Backend — `built_at` in graph status

**Files:**
- Modify: `web.go`
- Modify: `web_graph_test.go`

- [ ] **Step 1: Add the failing test** — append to `web_graph_test.go`:

```go
func TestGraphStatusReportsBuiltAt(t *testing.T) {
	s := newTestServer(t, true) // copies fixture to <root>/graphify-out/graph.json
	req := httptest.NewRequest("GET", "/api/graph/status?dir="+s.startDir, nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	var resp graphStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.BuiltAt.IsZero() {
		t.Errorf("BuiltAt should be set when a graph file exists")
	}
}

func TestGraphStatusBuiltAtZeroWhenNoGraph(t *testing.T) {
	s := newTestServer(t, false)
	req := httptest.NewRequest("GET", "/api/graph/status?dir="+s.startDir, nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	var resp graphStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if !resp.BuiltAt.IsZero() {
		t.Errorf("BuiltAt should be zero when no graph file exists")
	}
}
```

- [ ] **Step 2: Run to verify FAIL**

Run: `go test -run TestGraphStatusBuiltAt -count=1 ./...`
Expected: build error — `graphStatusResponse` has no `BuiltAt` field.

- [ ] **Step 3: Implement** — in `web.go`, update `graphStatusResponse` and `handleGraphStatus`.

Replace the struct:
```go
type graphStatusResponse struct {
	Available bool      `json:"available"`
	NodeCount int       `json:"node_count"`
	LoadedAt  time.Time `json:"loaded_at,omitempty"`
	BuiltAt   time.Time `json:"built_at,omitempty"`
	Dir       string    `json:"dir"`
	Path      string    `json:"path"`
}
```

In `handleGraphStatus`, after computing `resp.Path` and before/around the `graphForDir` block, stat the file for its mtime:
```go
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
	// built_at = the graph.json file's mtime — the moment graphify last
	// wrote it. Distinct from loaded_at (when the server read it).
	if info, err := os.Stat(resp.Path); err == nil {
		resp.BuiltAt = info.ModTime()
	}
	if g := s.graphForDir(dir); g != nil {
		resp.Available = true
		resp.NodeCount = g.NodeCount()
		resp.LoadedAt = g.LoadedAt()
	}
	s.writeJSON(w, http.StatusOK, resp)
}
```

`os` and `time` and `filepath` are already imported by web.go — confirm.

- [ ] **Step 4: Run to verify PASS**

Run: `go test -run TestGraphStatusBuiltAt -count=1 ./...` → 2 PASS
Run: `go test -count=1 ./...` → full suite green

- [ ] **Step 5: Commit**

```bash
git add web.go web_graph_test.go
git commit -m "feat(graph): report graph.json mtime as built_at in status"
```

---

## Task 2: Frontend — last-built line + phase progress bar

**Files:**
- Modify: `web.go` (embedded HTML/CSS/JS)

- [ ] **Step 1: Add CSS** — inside `<style>`, after the `.graph-backend-select:disabled` rule:

```css
    .graph-built-at {
      font-size: 11px;
      color: var(--muted);
    }
    .graph-progress {
      height: 6px;
      border-radius: 999px;
      background: var(--panel-2);
      overflow: hidden;
    }
    .graph-progress[hidden] { display: none; }
    .graph-progress-fill {
      height: 100%;
      width: 0%;
      background: var(--accent);
      border-radius: 999px;
      transition: width 0.4s ease;
    }
    .graph-progress-fill.error { background: oklch(0.6 0.2 25); }
    .graph-progress-meta {
      display: flex;
      justify-content: space-between;
      font-size: 11px;
      color: var(--muted);
    }
    .graph-progress-meta[hidden] { display: none; }
```

- [ ] **Step 2: Add DOM**

(2a) Last-built line — inside `#graphRail`, find the `<div class="graph-section-title">Concepts in this file</div>` block. Add a sibling line right after that title:
```html
        <div class="graph-section-title">Concepts in this file</div>
        <div class="graph-built-at" id="graphBuiltAt" hidden></div>
```

(2b) Progress bar — inside `#graphBuildBox`, between the `<button id="graphBuildBtn">` and the `<div class="graph-build-hint">`:
```html
        <button class="graph-build-btn" id="graphBuildBtn" type="button">Build graph</button>
        <div class="graph-progress" id="graphProgress" hidden>
          <div class="graph-progress-fill" id="graphProgressFill"></div>
        </div>
        <div class="graph-progress-meta" id="graphProgressMeta" hidden>
          <span id="graphProgressPhase">starting…</span>
          <span id="graphProgressTime">0:00</span>
        </div>
        <div class="graph-build-hint" id="graphBuildHint">
```

- [ ] **Step 3: Add element refs + helpers**

In the JS element-ref block (where `graphBackendSelectEl` etc. are declared), add:
```js
    const graphBuiltAtEl = document.getElementById("graphBuiltAt");
    const graphProgressEl = document.getElementById("graphProgress");
    const graphProgressFillEl = document.getElementById("graphProgressFill");
    const graphProgressMetaEl = document.getElementById("graphProgressMeta");
    const graphProgressPhaseEl = document.getElementById("graphProgressPhase");
    const graphProgressTimeEl = document.getElementById("graphProgressTime");
```

Add these helper functions near the other graph functions:
```js
    function relativeTime(iso) {
      if (!iso) return "";
      const then = new Date(iso).getTime();
      if (isNaN(then)) return "";
      const secs = Math.max(0, Math.round((Date.now() - then) / 1000));
      if (secs < 60) return secs + "s ago";
      const mins = Math.round(secs / 60);
      if (mins < 60) return mins + "m ago";
      const hrs = Math.round(mins / 60);
      if (hrs < 24) return hrs + "h ago";
      return Math.round(hrs / 24) + "d ago";
    }

    function phaseToPercent(phase) {
      switch (phase) {
        case "detect":  return 15;
        case "extract": return 65;
        case "cluster": return 85;
        case "report":  return 95;
        case "done":    return 100;
        default:        return -1; // log / unknown — keep current width
      }
    }

    function formatElapsed(totalSecs) {
      const m = Math.floor(totalSecs / 60);
      const s = totalSecs % 60;
      return m + ":" + (s < 10 ? "0" : "") + s;
    }
```

- [ ] **Step 4: Show "last built" in `refreshGraphStatus`**

In `refreshGraphStatus`, after `state.graphAvailable = !!data.available;`, add handling for the built-at line:
```js
        if (data.available && data.built_at) {
          graphBuiltAtEl.hidden = false;
          graphBuiltAtEl.textContent =
            "Last built " + relativeTime(data.built_at) +
            " · " + (data.node_count || 0) + " nodes";
        } else {
          graphBuiltAtEl.hidden = true;
        }
```

- [ ] **Step 5: Drive the progress bar in `startGraphBuild`**

In `startGraphBuild`, at the very start of the function (right after `graphBuildBtnEl.disabled = true;`), add progress setup:
```js
      // progress bar setup
      graphProgressEl.hidden = false;
      graphProgressMetaEl.hidden = false;
      graphProgressFillEl.classList.remove("error");
      graphProgressFillEl.style.width = "0%";
      graphProgressPhaseEl.textContent = "starting…";
      graphProgressTimeEl.textContent = "0:00";
      let progressPct = 0;
      const buildStart = Date.now();
      const elapsedTimer = setInterval(function () {
        graphProgressTimeEl.textContent =
          formatElapsed(Math.floor((Date.now() - buildStart) / 1000));
      }, 1000);
```

In the SSE `src.onmessage` handler, inside the `try` block: when handling a normal (non-"closed") event, after appending the log line, update the bar:
```js
          const pct = phaseToPercent(data.phase);
          if (pct > progressPct) {
            progressPct = pct;
            graphProgressFillEl.style.width = progressPct + "%";
          }
          if (data.phase && data.phase !== "log") {
            graphProgressPhaseEl.textContent = data.phase;
          }
```

In the `data.phase === "closed"` branch, finalize:
```js
            clearInterval(elapsedTimer);
            if (data.ok) {
              graphProgressFillEl.style.width = "100%";
              graphProgressPhaseEl.textContent = "done";
            } else {
              graphProgressFillEl.classList.add("error");
              graphProgressPhaseEl.textContent = "failed";
            }
```
(place these lines alongside the existing `closed`-branch logic — do not remove the existing `graphBuildLogEl.textContent += ...`, `src.close()`, `graphBuildBtnEl.disabled = false`, and `refreshGraphStatus()` calls).

In the `src.onerror` handler, also `clearInterval(elapsedTimer);` so the timer stops if the stream drops.

- [ ] **Step 6: Build**

Run: `go build ./...` → success.

- [ ] **Step 7: Headless smoke test**

```bash
go build -o /tmp/mdv-prog .
ROOT=$(mktemp -d)
mkdir -p "$ROOT/graphify-out"
cp testdata/graph_simple.json "$ROOT/graphify-out/graph.json"
/tmp/mdv-prog --web --port 18490 --root "$ROOT" >/tmp/mdv-prog.log 2>&1 &
PID=$!
sleep 1
echo "--- status has built_at ---"
curl -s "http://127.0.0.1:18490/api/graph/status?dir=$ROOT" | grep -o '"built_at":"[^"]*"' || echo "MISSING built_at"
echo "--- HTML has progress + built-at elements ---"
HTML=$(curl -s http://127.0.0.1:18490/)
for id in graphBuiltAt graphProgress graphProgressFill graphProgressMeta graphProgressPhase graphProgressTime; do
  echo "$HTML" | grep -q "id=\"$id\"" && echo "  $id ok" || echo "  $id MISSING"
done
kill $PID 2>/dev/null; wait 2>/dev/null
rm -rf "$ROOT" /tmp/mdv-prog /tmp/mdv-prog.log
```

Expected: `built_at` present in status JSON; all 6 element IDs present.

- [ ] **Step 8: Run all tests**

Run: `go test -count=1 ./...` → green.

- [ ] **Step 9: Commit**

```bash
git add web.go
git commit -m "feat(graph): show last-built time and phase progress bar in the rail"
```

---

## Self-Review Notes

**Spec coverage:** last-built history → Task 1 `built_at` + Task 2 Step 4; progress → Task 2 Steps 3/5 (phase→% map + elapsed timer + bar).

**Placeholder scan:** none — all code complete.

**Type consistency:** `graphStatusResponse.BuiltAt` (Go) ↔ `data.built_at` (JS). `phaseToPercent` phase strings match the values `phaseFromLine` produces in `graph_build.go` (`detect`/`extract`/`cluster`/`report`/`done`). Element IDs consistent between DOM (Step 2) and refs (Step 3).

**Known limitation:** for agent-CLI backends (`claude-cli`/`codex-cli`/`gemini-cli`) the phase markers in the output stream are not guaranteed, so the bar may jump or stall at a phase; the elapsed timer is the reliable indicator. Acceptable and documented.

**No backticks in embedded JS** — all new JS uses plain string concatenation (Go raw-string host constraint).
