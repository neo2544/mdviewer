# graphify Build Backends Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the MD Viewer "Build graph" button choose among 5 build backends (Gemini API, Claude CLI, Gemini CLI, Codex CLI, Ollama) via a dropdown, so users without an API key can build using an agent CLI's subscription auth.

**Architecture:** `graph_build.go` gains a `Backend` model, a `detectBackends()` scanner, and a `buildCommand()` factory that constructs the right `*exec.Cmd` per backend. `BuildManager.Start` takes a `backendID`. `web.go` adds `GET /api/graph/backends` and a `backend` query param on `POST /api/graph/build`, plus a `<select>` in the build UI.

**Tech Stack:** Go 1.22 stdlib (`os/exec`, `os`, `context`), embedded HTML/JS in `web.go`.

**Builds on:** branch `feature/graphify-integration` (HEAD `dd227bc`). This plan continues the same branch.

**Spec context:** The 5 backends and their commands —

| ID | Label | Command | Detect |
|---|---|---|---|
| `gemini-api` | Gemini API | `graphify extract <root> --backend gemini` | `GEMINI_API_KEY` or `GOOGLE_API_KEY` set |
| `claude-cli` | Claude CLI | `claude -p "/graphify ." --dangerously-skip-permissions` (cwd=root) | `claude` on PATH |
| `gemini-cli` | Gemini CLI (experimental) | `gemini -p "<trigger>"` (cwd=root) | `gemini` on PATH |
| `codex-cli` | Codex CLI (experimental) | `codex exec "<trigger>"` (cwd=root) | `codex` on PATH |
| `ollama` | Ollama (experimental/local) | `graphify extract <root> --backend ollama` | `ollama` on PATH |

---

## File Structure

| File | Responsibility |
|---|---|
| `graph_build.go` (modify) | Add `Backend`, `detectBackends`, `pickAutoBackend`, `buildCommand`; rewire `Start`/`runBuild` |
| `graph_build_test.go` (modify) | Tests for detection + command construction; update existing `TestBuildSession*` for new `Start` signature |
| `web.go` (modify) | `GET /api/graph/backends` route; `backend` param on build route; dropdown in build UI |
| `web_graph_test.go` (modify) | Test for `/api/graph/backends` |

---

## Task 1: Backend model + detection + command factory

**Files:**
- Modify: `graph_build.go`
- Modify: `graph_build_test.go`

This task is purely additive — it adds new functions but does NOT yet change `Start`. Task 2 rewires `Start`.

- [ ] **Step 1: Write the failing tests** — append to `graph_build_test.go`:

```go
func TestDetectBackends(t *testing.T) {
	backends := detectBackends()
	if len(backends) != 5 {
		t.Fatalf("detectBackends returned %d backends, want 5", len(backends))
	}
	ids := map[string]Backend{}
	for _, b := range backends {
		ids[b.ID] = b
	}
	for _, want := range []string{"gemini-api", "claude-cli", "gemini-cli", "codex-cli", "ollama"} {
		if _, ok := ids[want]; !ok {
			t.Errorf("missing backend %q", want)
		}
	}
	if !ids["gemini-cli"].Experimental || !ids["codex-cli"].Experimental {
		t.Errorf("gemini-cli and codex-cli must be marked Experimental")
	}
	if ids["claude-cli"].Experimental {
		t.Errorf("claude-cli must not be Experimental")
	}
}

func TestDetectBackendsGeminiAPIKey(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	for _, b := range detectBackends() {
		if b.ID == "gemini-api" && b.Available {
			t.Errorf("gemini-api should be unavailable with no key")
		}
	}
	t.Setenv("GOOGLE_API_KEY", "x")
	for _, b := range detectBackends() {
		if b.ID == "gemini-api" && !b.Available {
			t.Errorf("gemini-api should be available with GOOGLE_API_KEY set")
		}
	}
}

func TestBuildCommandGeminiAPI(t *testing.T) {
	root := t.TempDir()
	installStubGraphify(t, root, 0) // also sets GEMINI_API_KEY
	cmd, err := buildCommand(context.Background(), "gemini-api", root)
	if err != nil {
		t.Fatalf("buildCommand: %v", err)
	}
	args := cmd.Args
	// expect: <graphify> extract <root> --backend gemini
	if len(args) < 4 || args[1] != "extract" || args[2] != root {
		t.Errorf("unexpected args: %v", args)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--backend gemini") {
		t.Errorf("expected --backend gemini in %v", args)
	}
}

func TestBuildCommandGeminiAPINoKey(t *testing.T) {
	root := t.TempDir()
	installStubGraphify(t, root, 0)
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	if _, err := buildCommand(context.Background(), "gemini-api", root); err == nil {
		t.Errorf("expected error for gemini-api with no key")
	}
}

func TestBuildCommandUnknownBackend(t *testing.T) {
	root := t.TempDir()
	if _, err := buildCommand(context.Background(), "nonsense", root); err == nil {
		t.Errorf("expected error for unknown backend")
	}
}
```

The test file already imports `context`, `os`, `os/exec`, `path/filepath`, `runtime`, `strings`, `testing`, `time` (from Task 12). No new imports needed.

- [ ] **Step 2: Run to verify FAIL**

Run: `go test -run 'TestDetectBackends|TestBuildCommand' ./...`
Expected: build errors — `detectBackends`, `buildCommand`, `Backend` undefined.

- [ ] **Step 3: Implement — append to `graph_build.go`**

```go
// Backend describes one selectable graph-build backend for the UI.
type Backend struct {
	ID           string `json:"id"`
	Label        string `json:"label"`
	Available    bool   `json:"available"`
	Experimental bool   `json:"experimental"`
}

// detectBackends scans the environment and PATH and reports which build
// backends are usable on this machine. The order is the auto-selection
// priority order (see pickAutoBackend).
func detectBackends() []Backend {
	hasGeminiKey := os.Getenv("GEMINI_API_KEY") != "" || os.Getenv("GOOGLE_API_KEY") != ""
	onPath := func(bin string) bool {
		_, err := exec.LookPath(bin)
		return err == nil
	}
	return []Backend{
		{ID: "gemini-api", Label: "Gemini API", Available: hasGeminiKey},
		{ID: "claude-cli", Label: "Claude CLI", Available: onPath("claude")},
		{ID: "ollama", Label: "Ollama (local)", Available: onPath("ollama")},
		{ID: "gemini-cli", Label: "Gemini CLI", Available: onPath("gemini"), Experimental: true},
		{ID: "codex-cli", Label: "Codex CLI", Available: onPath("codex"), Experimental: true},
	}
}

// pickAutoBackend returns the first available backend ID in priority
// order, or "" when nothing is usable.
func pickAutoBackend() string {
	for _, b := range detectBackends() {
		if b.Available {
			return b.ID
		}
	}
	return ""
}

// graphifyTriggerPrompt is the natural-language instruction handed to an
// agent CLI (gemini, codex) that doesn't support graphify's "/graphify"
// slash command directly. Best-effort — these backends are experimental.
const graphifyTriggerPrompt = "Use the graphify tool to build a knowledge graph for the current directory. Run it non-interactively and write the result to graphify-out/graph.json."

// buildCommand constructs the exec.Cmd for the requested backend. An
// empty or "auto" backendID resolves to pickAutoBackend(). The returned
// command, when run, must produce <root>/graphify-out/graph.json.
func buildCommand(ctx context.Context, backendID, root string) (*exec.Cmd, error) {
	resolved := backendID
	if resolved == "" || resolved == "auto" {
		resolved = pickAutoBackend()
		if resolved == "" {
			return nil, errors.New("no usable build backend found: set GEMINI_API_KEY, or install the claude/gemini/codex/ollama CLI")
		}
	}

	graphifyCmd := func(extraArgs ...string) (*exec.Cmd, error) {
		bin, err := exec.LookPath("graphify")
		if err != nil {
			return nil, fmt.Errorf("graphify not found on PATH (try `pip install graphifyy`): %w", err)
		}
		args := append([]string{"extract", root}, extraArgs...)
		return exec.CommandContext(ctx, bin, args...), nil
	}
	agentCmd := func(bin string, args ...string) (*exec.Cmd, error) {
		path, err := exec.LookPath(bin)
		if err != nil {
			return nil, fmt.Errorf("%s not found on PATH: %w", bin, err)
		}
		c := exec.CommandContext(ctx, path, args...)
		c.Dir = root // agent CLIs and the graphify skill default to cwd
		return c, nil
	}

	switch resolved {
	case "gemini-api":
		if os.Getenv("GEMINI_API_KEY") == "" && os.Getenv("GOOGLE_API_KEY") == "" {
			return nil, errors.New("gemini-api backend requires GEMINI_API_KEY or GOOGLE_API_KEY")
		}
		return graphifyCmd("--backend", "gemini")
	case "ollama":
		return graphifyCmd("--backend", "ollama")
	case "claude-cli":
		return agentCmd("claude", "-p", "/graphify .", "--dangerously-skip-permissions")
	case "gemini-cli":
		return agentCmd("gemini", "-p", graphifyTriggerPrompt)
	case "codex-cli":
		return agentCmd("codex", "exec", graphifyTriggerPrompt)
	default:
		return nil, fmt.Errorf("unknown backend: %s", resolved)
	}
}
```

- [ ] **Step 4: Run to verify PASS**

Run: `go test -run 'TestDetectBackends|TestBuildCommand' -count=1 ./...`
Expected: 5 PASS.

- [ ] **Step 5: Commit**

```bash
git add graph_build.go graph_build_test.go
git commit -m "feat(graph): add Backend model, detection, and per-backend command factory"
```

---

## Task 2: Rewire `Start` and `runBuild` to use the chosen backend

**Files:**
- Modify: `graph_build.go`
- Modify: `graph_build_test.go`

Currently `BuildManager.Start(ctx, root)` hardcodes the API-key guard and `runBuild` does `exec.CommandContext(bin, sess.root)`. After this task, `Start` takes a `backendID`, builds the command via `buildCommand`, and `runBuild` receives a ready `*exec.Cmd`.

- [ ] **Step 1: Update the existing tests for the new signature**

In `graph_build_test.go`, the Task-12 tests call `mgr.Start(context.Background(), root)`. They must become `mgr.Start(context.Background(), root, "<backend>")`.

Replace `TestBuildSessionSuccess`:

```go
func TestBuildSessionSuccess(t *testing.T) {
	root := t.TempDir()
	installStubGraphify(t, root, 0) // sets GEMINI_API_KEY=stub-key

	mgr := newBuildManager()
	sess, err := mgr.Start(context.Background(), root, "gemini-api")
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
```

Replace `TestBuildSessionRejectsConcurrent`:

```go
func TestBuildSessionRejectsConcurrent(t *testing.T) {
	root := t.TempDir()
	installStubGraphify(t, root, 0)

	mgr := newBuildManager()
	if _, err := mgr.Start(context.Background(), root, "gemini-api"); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	_, err := mgr.Start(context.Background(), root, "gemini-api")
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("expected 'already running' error, got %v", err)
	}
}
```

Replace `TestBuildSessionRequiresAPIKey` — its meaning changes. With multi-backend, "no key" no longer blocks all builds (claude-cli works keyless). The test now verifies the *gemini-api backend specifically* rejects when its key is absent:

```go
func TestBuildSessionGeminiAPIRequiresKey(t *testing.T) {
	root := t.TempDir()
	installStubGraphify(t, root, 0)
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")

	mgr := newBuildManager()
	_, err := mgr.Start(context.Background(), root, "gemini-api")
	if err == nil {
		t.Fatalf("expected gemini-api to reject with no key")
	}
}
```

Replace `TestBuildSessionRequiresGraphifyOnPath` — explicitly request `gemini-api` so the failure is graphify-not-found, not auto-resolution picking another backend:

```go
func TestBuildSessionRequiresGraphifyOnPath(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", "/dev/null")
	t.Setenv("GEMINI_API_KEY", "stub")

	mgr := newBuildManager()
	_, err := mgr.Start(context.Background(), root, "gemini-api")
	if err == nil {
		t.Fatalf("expected 'not found' error")
	}
	if _, e := exec.LookPath("graphify"); e == nil {
		t.Fatalf("graphify unexpectedly on PATH")
	}
}
```

- [ ] **Step 2: Run to verify FAIL**

Run: `go test -run TestBuildSession ./...`
Expected: build errors — `Start` still has 2-arg signature.

- [ ] **Step 3: Rewire `Start` and `runBuild` in `graph_build.go`**

Replace the existing `Start` method entirely:

```go
// Start launches a new build session using the given backend. Returns an
// error if one is already running, or the backend cannot produce a
// runnable command (missing binary, missing key). An empty backendID
// resolves to the auto-selected backend.
func (m *BuildManager) Start(ctx context.Context, root, backendID string) (*BuildSession, error) {
	cmd, err := buildCommand(ctx, backendID, root)
	if err != nil {
		return nil, err
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

	go runBuild(cmd, sess)
	return sess, nil
}
```

Replace the existing `runBuild` function — it now receives a ready `*exec.Cmd` instead of a bare binary path:

```go
// runBuild drives the prepared command, streaming stdout/stderr
// line-by-line and pushing a BuildEvent per non-empty line.
func runBuild(cmd *exec.Cmd, sess *BuildSession) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		sess.push(BuildEvent{Phase: "error", Message: "stdout pipe: " + err.Error(), At: time.Now()})
		sess.finish(err)
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		sess.push(BuildEvent{Phase: "error", Message: "stderr pipe: " + err.Error(), At: time.Now()})
		sess.finish(err)
		return
	}

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

	werr := cmd.Wait()
	if werr != nil {
		sess.push(BuildEvent{Phase: "error", Message: werr.Error(), At: time.Now()})
	} else {
		sess.push(BuildEvent{Phase: "done", Message: "build exited 0", At: time.Now()})
	}
	sess.finish(werr)
}
```

Note: the `os` import is still needed (used by `detectBackends`/`buildCommand`), and `errors` too. `exec` and `context` likewise. No import changes.

- [ ] **Step 4: Run to verify PASS**

Run: `go test -run TestBuildSession -count=1 ./...`
Expected: PASS (`TestBuildSessionSuccess`, `TestBuildSessionRejectsConcurrent`, `TestBuildSessionGeminiAPIRequiresKey`, `TestBuildSessionRequiresGraphifyOnPath`).

- [ ] **Step 5: Build to confirm `web.go` still compiles**

Run: `go build ./...`
Expected: FAIL — `web.go`'s `handleGraphBuild` calls `s.buildManager.Start(r.Context(), s.startDir)` with the old 2-arg signature. This is expected; Task 3 fixes the caller. To keep this task's commit green on its own, ALSO apply the minimal caller fix now:

In `web.go`, find `handleGraphBuild` and change the `Start` call from:
```go
	sess, err := s.buildManager.Start(r.Context(), s.startDir)
```
to:
```go
	sess, err := s.buildManager.Start(r.Context(), s.startDir, r.URL.Query().Get("backend"))
```

(Task 3 builds the proper backends route + UI on top; this one-line change keeps the build green.)

Re-run: `go build ./...` → success.

- [ ] **Step 6: Run all tests**

Run: `go test -count=1 ./...`
Expected: full suite green.

- [ ] **Step 7: Commit**

```bash
git add graph_build.go graph_build_test.go web.go
git commit -m "feat(graph): Start accepts a backend; runBuild drives a prepared command"
```

---

## Task 3: `GET /api/graph/backends` route

**Files:**
- Modify: `web.go`
- Modify: `web_graph_test.go`

- [ ] **Step 1: Register the route** — in `routes()`, after `/api/graph/build/status`:

```go
	mux.HandleFunc("/api/graph/backends", s.handleGraphBackends)
```

- [ ] **Step 2: Implement the handler** — append to `web.go`:

```go
// handleGraphBackends lists the build backends usable on this machine so
// the UI can populate the backend dropdown.
func (s *webServer) handleGraphBackends(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, detectBackends())
}
```

- [ ] **Step 3: Add a test** — append to `web_graph_test.go`:

```go
func TestGraphBackendsLists(t *testing.T) {
	s := newTestServer(t, false)
	req := httptest.NewRequest("GET", "/api/graph/backends", nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []Backend
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Errorf("got %d backends, want 5", len(got))
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test -run TestGraphBackends -count=1 ./...` → PASS
Run: `go test -count=1 ./...` → full suite green

- [ ] **Step 5: Commit**

```bash
git add web.go web_graph_test.go
git commit -m "feat(graph): GET /api/graph/backends route"
```

---

## Task 4: Backend dropdown in the build UI

**Files:**
- Modify: `web.go` (HTML/CSS/JS in `webAppHTML`)

- [ ] **Step 1: Add CSS** — inside `<style>`, after the `.graph-build-hint.warn` rule:

```css
    .graph-backend-select {
      width: 100%;
      padding: 5px 8px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: var(--panel-2);
      color: var(--text);
      font-size: 12px;
    }
    .graph-backend-select:disabled { opacity: 0.5; }
```

- [ ] **Step 2: Add the dropdown to the build box DOM**

Find `<div class="graph-build" id="graphBuildBox" hidden>` and insert the `<select>` as the FIRST child, before the `<button>`:

```html
      <div class="graph-build" id="graphBuildBox" hidden>
        <select class="graph-backend-select" id="graphBackendSelect" aria-label="Build backend"></select>
        <button class="graph-build-btn" id="graphBuildBtn" type="button">Build graph</button>
        <div class="graph-build-hint" id="graphBuildHint">
          Needs <code>GEMINI_API_KEY</code>. No key? Run <code>/graphify .</code> in Claude Code, then refresh.
        </div>
        <div class="graph-build-log" id="graphBuildLog" hidden></div>
      </div>
```

- [ ] **Step 3: Populate the dropdown + pass the choice to the build POST**

In the JS, find the `const graphBuildBoxEl = ...` declarations block. Add one more ref:

```js
    const graphBackendSelectEl = document.getElementById("graphBackendSelect");
```

Add a function to populate the dropdown (place it next to `refreshGraphStatus`):

```js
    async function loadGraphBackends() {
      let backends = [];
      try {
        const r = await fetch("/api/graph/backends");
        backends = await r.json();
      } catch (err) {
        backends = [];
      }
      graphBackendSelectEl.innerHTML = "";
      const auto = document.createElement("option");
      auto.value = "auto";
      auto.textContent = "Auto (best available)";
      graphBackendSelectEl.appendChild(auto);
      for (const b of backends) {
        const opt = document.createElement("option");
        opt.value = b.id;
        let label = b.label;
        if (b.experimental) label += " · experimental";
        if (!b.available) label += " (unavailable)";
        opt.textContent = label;
        opt.disabled = !b.available;
        graphBackendSelectEl.appendChild(opt);
      }
    }
```

In `startGraphBuild`, change the build POST to include the selected backend. Find:

```js
        resp = await fetch("/api/graph/build", { method: "POST" });
```

Replace with:

```js
        const backend = graphBackendSelectEl.value || "auto";
        resp = await fetch("/api/graph/build?backend=" + encodeURIComponent(backend), { method: "POST" });
```

- [ ] **Step 4: Call `loadGraphBackends()` at boot**

Find where `refreshGraphStatus()` is called at boot and add `loadGraphBackends();` right next to it.

- [ ] **Step 5: Build**

Run: `go build ./...` → success.

- [ ] **Step 6: Headless smoke test**

```bash
go build -o /tmp/mdv-bk .
TESTROOT=$(mktemp -d)
/tmp/mdv-bk --web --port 18470 --root $TESTROOT >/tmp/mdv-bk.log 2>&1 &
PID=$!
sleep 1
echo "--- /api/graph/backends ---"
curl -s http://127.0.0.1:18470/api/graph/backends
echo
echo "--- HTML has dropdown ---"
curl -s http://127.0.0.1:18470/ | grep -c 'id="graphBackendSelect"'
kill $PID 2>/dev/null
wait 2>/dev/null
rm -rf $TESTROOT /tmp/mdv-bk /tmp/mdv-bk.log
```

Expected: backends JSON array of 5 objects; HTML grep count `1`.

- [ ] **Step 7: Run all tests**

Run: `go test -count=1 ./...` → green.

- [ ] **Step 8: Commit**

```bash
git add web.go
git commit -m "feat(graph): backend dropdown in the build UI"
```

---

## Self-Review Notes

**Spec coverage:**
- 5 backends → Task 1 `detectBackends` + `buildCommand`
- backend dropdown (manual selection) → Task 4
- experimental labels → Task 1 `Experimental` field + Task 4 label rendering
- `Start` takes backend → Task 2
- `/api/graph/backends` → Task 3
- `backend` param on build → Task 2 (caller fix) + Task 4 (UI sends it)

**Placeholder scan:** No TBD/TODO; every code step has full code.

**Type consistency:** `Backend{ID,Label,Available,Experimental}` used identically in graph_build.go, the route handler, the test, and the JS (`b.id`, `b.label`, `b.available`, `b.experimental`). `Start` signature `(ctx, root, backendID)` consistent across Task 2 definition, the web.go caller, and all test call sites.

**Known limitation:** `gemini-cli` and `codex-cli` trigger prompts are best-effort natural language; if those CLIs don't reliably run graphify, the SSE `error` event surfaces the failure and the UI already labels them "experimental".
