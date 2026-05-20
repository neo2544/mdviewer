package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Node is the subset of graphify node fields the viewer needs.
type Node struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	FileType   string `json:"file_type"`
	SourceFile string `json:"source_file"`
}

// FileRef is a node grouped by its source_file for the "Linked files" list.
type FileRef struct {
	Path     string `json:"path"`
	Label    string `json:"label"`
	FileType string `json:"file_type"`
}

// graphJSON mirrors the NetworkX node-link shape produced by
// graphify.export.to_json. We only deserialize the fields we need.
type graphJSON struct {
	Nodes []Node `json:"nodes"`
	Links []struct {
		Source string `json:"source"`
		Target string `json:"target"`
	} `json:"links"`
}

// GraphIndex is the in-memory query side of graph.json. All maps are
// populated at Load time and the struct is treated as immutable after
// — any update is a full replacement via Reload.
type GraphIndex struct {
	mu          sync.RWMutex
	nodes       map[string]Node
	byFile      map[string][]string
	neighbors   map[string][]string
	loadedAt    time.Time
	sourcePath  string
	projectRoot string
}

// LoadGraph reads graph.json and returns a populated index. source_file
// values are normalised against projectRoot so that frontend queries
// using absolute paths always match.
func LoadGraph(jsonPath, projectRoot string) (*GraphIndex, error) {
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return nil, fmt.Errorf("read graph.json: %w", err)
	}
	var raw graphJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse graph.json: %w", err)
	}

	g := &GraphIndex{
		nodes:       make(map[string]Node, len(raw.Nodes)),
		byFile:      make(map[string][]string),
		neighbors:   make(map[string][]string),
		sourcePath:  jsonPath,
		projectRoot: projectRoot,
		loadedAt:    time.Now(),
	}
	for _, n := range raw.Nodes {
		n.SourceFile = g.normalisePath(n.SourceFile)
		g.nodes[n.ID] = n
		if n.SourceFile != "" {
			g.byFile[n.SourceFile] = append(g.byFile[n.SourceFile], n.ID)
		}
	}
	for _, e := range raw.Links {
		g.neighbors[e.Source] = append(g.neighbors[e.Source], e.Target)
		g.neighbors[e.Target] = append(g.neighbors[e.Target], e.Source)
	}
	return g, nil
}

// normalisePath returns an absolute path resolved against projectRoot.
// Relative paths from graphify are common (it stores paths relative to
// the scanned folder); absolute paths pass through.
func (g *GraphIndex) normalisePath(p string) string {
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(g.projectRoot, p)
}
