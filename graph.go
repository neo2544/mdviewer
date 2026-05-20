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

// FilesForConcept returns the OTHER files that contain the given node
// or any of its graph neighbours. Used by the "Linked files" panel —
// the file the node itself was extracted from is excluded so the panel
// only shows targets the user can actually jump to.
func (g *GraphIndex) FilesForConcept(nodeID string) []FileRef {
	g.mu.RLock()
	defer g.mu.RUnlock()
	base, ok := g.nodes[nodeID]
	if !ok {
		return []FileRef{}
	}
	selfFile := base.SourceFile
	seen := map[string]bool{selfFile: true}
	out := []FileRef{}
	add := func(n Node) {
		if n.SourceFile == "" || seen[n.SourceFile] {
			return
		}
		seen[n.SourceFile] = true
		out = append(out, FileRef{Path: n.SourceFile, Label: n.Label, FileType: n.FileType})
	}
	for _, nid := range g.neighbors[nodeID] {
		if n, ok := g.nodes[nid]; ok {
			add(n)
		}
	}
	return out
}

// NodeCount is read by /api/graph/status.
func (g *GraphIndex) NodeCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.nodes)
}

// LoadedAt is read by /api/graph/status.
func (g *GraphIndex) LoadedAt() time.Time {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.loadedAt
}

// ConceptsInFile returns nodes whose source_file equals absPath. Returns
// a non-nil empty slice when the file has no extracted concepts so JSON
// responses render as [] rather than null.
func (g *GraphIndex) ConceptsInFile(absPath string) []Node {
	g.mu.RLock()
	defer g.mu.RUnlock()
	key := filepath.Clean(absPath)
	ids := g.byFile[key]
	out := make([]Node, 0, len(ids))
	for _, id := range ids {
		out = append(out, g.nodes[id])
	}
	return out
}

// fileMTime returns the modification time of the source path or zero if
// the file is unreadable.
func (g *GraphIndex) fileMTime() time.Time {
	fi, err := os.Stat(g.sourcePath)
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// ReloadIfChanged re-reads graph.json when its mtime is newer than the
// timestamp captured at the previous load. Returns (true, nil) if the
// in-memory index was replaced, (false, nil) otherwise.
//
// Reload is in-place — callers keep the same *GraphIndex pointer — so
// existing references in the webServer struct don't need re-wiring.
func (g *GraphIndex) ReloadIfChanged() (bool, error) {
	current := g.fileMTime()
	if current.IsZero() {
		return false, fmt.Errorf("stat %s: file unreadable", g.sourcePath)
	}
	g.mu.RLock()
	prev := g.loadedAt
	g.mu.RUnlock()
	if !current.After(prev) {
		return false, nil
	}
	fresh, err := LoadGraph(g.sourcePath, g.projectRoot)
	if err != nil {
		return false, err
	}
	g.mu.Lock()
	g.nodes = fresh.nodes
	g.byFile = fresh.byFile
	g.neighbors = fresh.neighbors
	g.loadedAt = current
	g.mu.Unlock()
	return true, nil
}
