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
