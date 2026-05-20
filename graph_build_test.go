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
	// sanity: confirm exec.LookPath fails
	if _, e := exec.LookPath("graphify"); e == nil {
		t.Fatalf("graphify unexpectedly on PATH: %v", e)
	}
}
