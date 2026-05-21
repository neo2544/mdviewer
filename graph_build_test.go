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
