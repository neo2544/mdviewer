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
