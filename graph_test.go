package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGraphIndexLoad(t *testing.T) {
	root, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	g, err := LoadGraph("testdata/graph_simple.json", root)
	if err != nil {
		t.Fatalf("LoadGraph error: %v", err)
	}
	if got, want := len(g.nodes), 3; got != want {
		t.Errorf("node count = %d, want %d", got, want)
	}
	if g.nodes["auth_session_token"].Label != "Token" {
		t.Errorf("label mismatch for auth_session_token")
	}
}

func TestConceptsInFile(t *testing.T) {
	root, _ := filepath.Abs(".")
	g, err := LoadGraph("testdata/graph_simple.json", root)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	abs := filepath.Join(root, "auth/session.go")
	nodes := g.ConceptsInFile(abs)
	if len(nodes) != 1 || nodes[0].ID != "auth_session_token" {
		t.Errorf("ConceptsInFile(session.go) = %+v, want [auth_session_token]", nodes)
	}
	if g.ConceptsInFile(filepath.Join(root, "nope.go")) == nil {
		t.Errorf("ConceptsInFile(missing) should return non-nil empty slice")
	}
}

func TestFilesForConcept(t *testing.T) {
	root, _ := filepath.Abs(".")
	g, err := LoadGraph("testdata/graph_simple.json", root)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	refs := g.FilesForConcept("auth_session_token")
	gotPaths := map[string]bool{}
	for _, r := range refs {
		gotPaths[r.Path] = true
	}
	wantPaths := []string{
		filepath.Join(root, "auth/login.go"),
		filepath.Join(root, "docs/intro.md"),
	}
	for _, w := range wantPaths {
		if !gotPaths[w] {
			t.Errorf("FilesForConcept missing %s; got %v", w, refs)
		}
	}
	if gotPaths[filepath.Join(root, "auth/session.go")] {
		t.Errorf("FilesForConcept should not return the source file of the node itself")
	}
	if g.FilesForConcept("nonexistent") == nil {
		t.Errorf("missing node should return non-nil empty slice, not nil")
	}
}

func TestReloadIfChanged(t *testing.T) {
	root := t.TempDir()
	dst := filepath.Join(root, "graph.json")
	mustCopy := func(src string) {
		t.Helper()
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("read fixture: %v", err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			t.Fatalf("write copy: %v", err)
		}
	}
	mustCopy("testdata/graph_simple.json")

	g, err := LoadGraph(dst, root)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	if g.NodeCount() != 3 {
		t.Fatalf("initial node count = %d, want 3", g.NodeCount())
	}

	// Overwrite with a different fixture (one node) and bump mtime.
	smaller := `{"nodes":[{"id":"x","label":"X","file_type":"document","source_file":"only.md"}],"links":[]}`
	if err := os.WriteFile(dst, []byte(smaller), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(dst, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	changed, err := g.ReloadIfChanged()
	if err != nil {
		t.Fatalf("ReloadIfChanged: %v", err)
	}
	if !changed {
		t.Fatalf("expected reload to happen")
	}
	if g.NodeCount() != 1 {
		t.Errorf("after reload node count = %d, want 1", g.NodeCount())
	}

	// Second call without mtime change → no reload.
	changed2, err := g.ReloadIfChanged()
	if err != nil {
		t.Fatalf("ReloadIfChanged 2nd: %v", err)
	}
	if changed2 {
		t.Errorf("second reload should be no-op")
	}
}
