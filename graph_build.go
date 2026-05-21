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
//
// Design note: Events() returns a single stable channel per session.
// All events (including those buffered before Events() is first called)
// are replayed into that channel when the session completes, so late
// callers see the full history. For active sessions a background pump
// drains the internal broadcast slice into the channel.
type BuildSession struct {
	id      string
	root    string
	startAt time.Time

	mu     sync.Mutex
	done   bool
	err    error
	events []BuildEvent

	// evCh is the single canonical events channel returned by Events().
	// It is created lazily on first call to Events().
	once  sync.Once
	evCh  chan BuildEvent

	subs []chan BuildEvent
}

func (s *BuildSession) ID() string   { return s.id }
func (s *BuildSession) Root() string { return s.root }
func (s *BuildSession) Err() error   { s.mu.Lock(); defer s.mu.Unlock(); return s.err }
func (s *BuildSession) OK() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done && s.err == nil
}

// Events returns the single canonical event channel for this session.
// The channel is created once (sync.Once). If the build is already done
// when Events() is first called, all buffered events are replayed and
// the channel is closed. Otherwise events stream in real-time and the
// channel is closed when the build finishes.
//
// Known limitation: the buffered channel has capacity 64; if more than
// 64 events accumulate before the first call to Events(), the replay
// inside the lock will block. In practice graphify emits O(10) lines.
func (s *BuildSession) Events() <-chan BuildEvent {
	s.once.Do(func() {
		s.mu.Lock()
		ch := make(chan BuildEvent, 64)
		s.evCh = ch
		if s.done {
			// replay all buffered events then close
			for _, ev := range s.events {
				ch <- ev
			}
			close(ch)
		} else {
			// subscribe so future pushes land on ch too
			s.subs = append(s.subs, ch)
			// replay already-buffered events that arrived before subscription
			for _, ev := range s.events {
				select {
				case ch <- ev:
				default:
				}
			}
		}
		s.mu.Unlock()
	})
	return s.evCh
}

func (s *BuildSession) push(ev BuildEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
	for _, sub := range s.subs {
		select {
		case sub <- ev:
		default:
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

// Current returns the most recent session (running or completed). May be nil.
func (m *BuildManager) Current() *BuildSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current
}

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
