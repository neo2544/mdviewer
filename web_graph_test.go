package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
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
	return &webServer{
		startDir:   root,
		appRoot:    root,
		graphCache: make(map[string]*GraphIndex),
	}
}

func TestGraphStatusNoGraph(t *testing.T) {
	s := newTestServer(t, false)
	req := httptest.NewRequest("GET", "/api/graph/status?dir="+s.startDir, nil)
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
	req := httptest.NewRequest("GET", "/api/graph/status?dir="+s.startDir, nil)
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

func TestGraphFileReturnsConcepts(t *testing.T) {
	s := newTestServer(t, true)
	abs := filepath.Join(s.startDir, "auth/session.go")
	req := httptest.NewRequest("GET",
		"/api/graph/file?dir="+s.startDir+"&path="+abs, nil)
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
	req := httptest.NewRequest("GET", "/api/graph/file?dir="+s.startDir, nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestGraphFileNoGraph(t *testing.T) {
	s := newTestServer(t, false)
	req := httptest.NewRequest("GET", "/api/graph/file?dir="+s.startDir+"&path=/nope", nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() == "null\n" {
		t.Errorf("response is null; should be []")
	}
}

func TestGraphConceptReturnsFiles(t *testing.T) {
	s := newTestServer(t, true)
	req := httptest.NewRequest("GET", "/api/graph/concept?dir="+s.startDir+"&id=auth_session_token", nil)
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
	req := httptest.NewRequest("GET", "/api/graph/concept?dir="+s.startDir+"&id=nope", nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

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
	req := httptest.NewRequest("POST", "/api/graph/build?backend=gemini-api", nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

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

func TestGraphStatusReportsBuiltAt(t *testing.T) {
	s := newTestServer(t, true)
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

func TestGraphBuildSurvivesRequestCompletion(t *testing.T) {
	root := t.TempDir()
	installStubGraphify(t, root, 0) // stub graphify on PATH; sets GEMINI_API_KEY

	s := &webServer{
		startDir:   root,
		appRoot:    root,
		graphCache: make(map[string]*GraphIndex),
	}
	s.buildManager = newBuildManager()

	srv := httptest.NewServer(s.routes())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/graph/build?backend=gemini-api", "", nil)
	if err != nil {
		t.Fatalf("POST build: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("build POST status = %d, want 202", resp.StatusCode)
	}

	// The stub graphify sleeps ~0.1s then writes graph.json. With the bug
	// (r.Context()), the subprocess is killed the moment this POST returns
	// and graph.json never appears. With the fix it completes.
	graphPath := filepath.Join(root, "graphify-out", "graph.json")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(graphPath); err == nil {
			return // build completed — fix works
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("graph.json never appeared — build was likely killed by request-context cancellation")
}
