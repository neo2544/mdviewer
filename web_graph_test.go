package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
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
	s := &webServer{
		startDir:  root,
		appRoot:   root,
		graphPath: filepath.Join(root, "graphify-out", "graph.json"),
	}
	s.tryLoadGraph()
	return s
}

func TestGraphStatusNoGraph(t *testing.T) {
	s := newTestServer(t, false)
	req := httptest.NewRequest("GET", "/api/graph/status", nil)
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
	req := httptest.NewRequest("GET", "/api/graph/status", nil)
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
