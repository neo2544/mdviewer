package main

import (
	"path/filepath"
	"testing"
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
