package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type webServer struct {
	startDir string
	appRoot  string
}

type webEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	ModTime string `json:"mod_time"`
}

type listResponse struct {
	Cwd       string     `json:"cwd"`
	Entries   []webEntry `json:"entries"`
	Favorites []string          `json:"favorites"`
	Aliases   map[string]string `json:"aliases"`
}

type fileResponse struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Content string `json:"content,omitempty"`
	RawURL  string `json:"raw_url,omitempty"`
	Size    int64  `json:"size"`
	ModTime string `json:"mod_time"`
}

type saveFileRequest struct {
	Path        string `json:"path"`
	Content     string `json:"content"`
	BaseModTime string `json:"base_mod_time"`
	Force       bool   `json:"force"`
}

// routes wires up every HTTP endpoint the web viewer exposes. Both the
// standalone web server and the menu-bar app reuse this so the route set
// stays in one place.
func (s *webServer) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/icon.png", s.handleIcon)
	mux.HandleFunc("/favicon.ico", s.handleIcon)
	mux.HandleFunc("/api/list", s.handleList)
	mux.HandleFunc("/api/file", s.handleFile)
	mux.HandleFunc("/api/file/save", s.handleSaveFile)
	mux.HandleFunc("/api/raw", s.handleRaw)
	mux.HandleFunc("/api/favorites/toggle", s.handleToggleFavorite)
	mux.HandleFunc("/api/resolve", s.handleResolve)
	mux.HandleFunc("/api/usage", s.handleUsage)
	mux.HandleFunc("/api/aliases", s.handleAliases)
	mux.HandleFunc("/api/search", s.handleSearch)
	return mux
}

// handleIcon serves the same M↓ template image embedded for the systray.
// Browsers use it as the tab favicon (/favicon.ico → png ok in modern UAs)
// and the apple-touch-icon. Black-with-alpha PNG; visibility in dark tabs
// depends on the browser's tab strip colour, same as any single-tone icon.
func (s *webServer) handleIcon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(menubarIconHiresPNG)
}

// handleAliases: GET returns the full alias map. POST upserts a single
// alias ({path, alias}). An empty alias removes the entry.
func (s *webServer) handleAliases(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.writeJSON(w, http.StatusOK, s.loadAliases())
	case http.MethodPost:
		var payload struct {
			Path  string `json:"path"`
			Alias string `json:"alias"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if payload.Path == "" {
			http.Error(w, "missing path", http.StatusBadRequest)
			return
		}
		aliases := s.loadAliases()
		trimmed := strings.TrimSpace(payload.Alias)
		if trimmed == "" {
			delete(aliases, payload.Path)
		} else {
			aliases[payload.Path] = trimmed
		}
		if err := s.saveAliases(aliases); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.writeJSON(w, http.StatusOK, aliases)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleUsage returns the embedded USAGE_WEB.md content. The web client
// renders it in the preview area whenever no file is selected.
func (s *webServer) handleUsage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(usageWebMD))
}

func runWebServer(startDir, appRoot, addr string) error {
	if addr == "" {
		addr = "127.0.0.1:8421"
	}
	server := &webServer{
		startDir: startDir,
		appRoot:  appRoot,
	}
	fmt.Printf("mdviewer web preview running at http://%s\n", addr)
	return http.ListenAndServe(addr, server.routes())
}

func (s *webServer) favoritesPath() string {
	return filepath.Join(s.appRoot, favoritesFileName)
}

func (s *webServer) loadFavorites() []string {
	data, err := os.ReadFile(s.favoritesPath())
	if err != nil {
		return nil
	}

	var favorites []string
	if err := json.Unmarshal(data, &favorites); err != nil {
		return nil
	}

	seen := make(map[string]struct{}, len(favorites))
	out := make([]string, 0, len(favorites))
	for _, dir := range favorites {
		if dir == "" {
			continue
		}
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		out = append(out, dir)
	}
	return out
}

func (s *webServer) saveFavorites(favorites []string) error {
	data, err := json.MarshalIndent(favorites, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.favoritesPath(), data, 0o644)
}

// Aliases: user-friendly labels for favorited paths. Stored alongside
// favorites in their own JSON file so the mapping is shared across
// browsers / devices the same way favorites are.

func (s *webServer) aliasesPath() string {
	return filepath.Join(s.appRoot, aliasesFileName)
}

func (s *webServer) loadAliases() map[string]string {
	data, err := os.ReadFile(s.aliasesPath())
	if err != nil {
		return map[string]string{}
	}
	var out map[string]string
	if err := json.Unmarshal(data, &out); err != nil || out == nil {
		return map[string]string{}
	}
	return out
}

func (s *webServer) saveAliases(aliases map[string]string) error {
	if aliases == nil {
		aliases = map[string]string{}
	}
	data, err := json.MarshalIndent(aliases, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.aliasesPath(), data, 0o644)
}

func (s *webServer) writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (s *webServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(webAppHTML))
}

func (s *webServer) handleList(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("dir")
	if dir == "" {
		dir = s.startDir
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// show_hidden=1 includes dotfiles. Defaults to false (Finder-like).
	showHidden := r.URL.Query().Get("show_hidden") == "1"

	items, err := os.ReadDir(absDir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var entries []webEntry
	if absDir != "/" {
		entries = append(entries, webEntry{
			Name:  "..",
			Path:  filepath.Dir(absDir),
			IsDir: true,
		})
	}

	var dirs, files []webEntry
	for _, item := range items {
		if !showHidden && strings.HasPrefix(item.Name(), ".") {
			continue
		}
		fullPath := filepath.Join(absDir, item.Name())
		var size int64
		var modTime string
		if info, err := item.Info(); err == nil {
			size = info.Size()
			modTime = info.ModTime().Format(time.RFC3339)
		}
		if item.IsDir() {
			dirs = append(dirs, webEntry{
				Name:    item.Name() + "/",
				Path:    fullPath,
				IsDir:   true,
				ModTime: modTime,
			})
			continue
		}
		files = append(files, webEntry{
			Name:    item.Name(),
			Path:    fullPath,
			IsDir:   false,
			Size:    size,
			ModTime: modTime,
		})
	}

	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	entries = append(entries, dirs...)
	entries = append(entries, files...)

	s.writeJSON(w, http.StatusOK, listResponse{
		Cwd:       absDir,
		Entries:   entries,
		Favorites: s.loadFavorites(),
		Aliases:   s.loadAliases(),
	})
}

func (s *webServer) handleFile(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	info, err := os.Stat(absPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if info.IsDir() {
		http.Error(w, "path is a directory", http.StatusBadRequest)
		return
	}

	ext := strings.ToLower(filepath.Ext(absPath))
	resp := fileResponse{
		Path:    absPath,
		Name:    filepath.Base(absPath),
		Size:    info.Size(),
		ModTime: info.ModTime().Format(time.RFC3339),
	}

	switch ext {
	case ".md", ".markdown", ".mdx":
		data, err := os.ReadFile(absPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp.Kind = "markdown"
		resp.Content = string(data)
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg":
		resp.Kind = "image"
		resp.RawURL = "/api/raw?path=" + url.QueryEscape(absPath)
	case ".html", ".htm":
		resp.Kind = "html"
		resp.RawURL = "/api/raw?path=" + url.QueryEscape(absPath)
	case ".txt", ".go", ".py", ".js", ".ts", ".sh", ".yaml", ".yml", ".json", ".toml", ".sql", ".log":
		data, err := os.ReadFile(absPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp.Kind = "text"
		resp.Content = string(data)
	default:
		resp.Kind = "binary"
	}

	s.writeJSON(w, http.StatusOK, resp)
}

func (s *webServer) handleRaw(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.ServeFile(w, r, absPath)
}

func (s *webServer) handleSaveFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req saveFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Path == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}

	absPath, err := filepath.Abs(req.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	info, err := os.Stat(absPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if info.IsDir() {
		http.Error(w, "path is a directory", http.StatusBadRequest)
		return
	}

	ext := strings.ToLower(filepath.Ext(absPath))
	switch ext {
	case ".md", ".markdown", ".mdx", ".txt", ".go", ".py", ".js", ".ts", ".sh", ".yaml", ".yml", ".json", ".toml", ".sql", ".log":
	default:
		http.Error(w, "unsupported file type for editing", http.StatusBadRequest)
		return
	}

	currentModTime := info.ModTime().Format(time.RFC3339)
	if !req.Force && req.BaseModTime != "" && req.BaseModTime != currentModTime {
		s.writeJSON(w, http.StatusConflict, map[string]any{
			"error":            "file_changed",
			"message":          "file changed on disk",
			"current_mod_time": currentModTime,
		})
		return
	}

	if err := os.WriteFile(absPath, []byte(req.Content), info.Mode().Perm()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	updatedInfo, err := os.Stat(absPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	kind := "text"
	if ext == ".md" || ext == ".markdown" || ext == ".mdx" {
		kind = "markdown"
	}

	s.writeJSON(w, http.StatusOK, fileResponse{
		Path:    absPath,
		Name:    filepath.Base(absPath),
		Kind:    kind,
		Content: req.Content,
		Size:    updatedInfo.Size(),
		ModTime: updatedInfo.ModTime().Format(time.RFC3339),
	})
}

// handleResolve takes a raw user-supplied path (possibly with ~, quotes,
// or relative segments) and returns the absolute resolved path along with
// metadata describing whether it exists and whether it's a directory.
// The browser uses this to decide whether to call loadDir or selectFile.
func (s *webServer) handleResolve(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("path")
	base := r.URL.Query().Get("base")
	if raw == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}
	if base == "" {
		base = s.startDir
	}

	resolved, err := resolveUserPath(raw, base)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	resp := map[string]any{
		"path":   resolved,
		"parent": filepath.Dir(resolved),
		"exists": false,
		"is_dir": false,
	}
	if info, err := os.Stat(resolved); err == nil {
		resp["exists"] = true
		resp["is_dir"] = info.IsDir()
	}
	s.writeJSON(w, http.StatusOK, resp)
}

func (s *webServer) handleToggleFavorite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload struct {
		Dir string `json:"dir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	dir, err := filepath.Abs(payload.Dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	favorites := s.loadFavorites()
	found := -1
	for i, favorite := range favorites {
		if favorite == dir {
			found = i
			break
		}
	}
	if found >= 0 {
		favorites = append(favorites[:found], favorites[found+1:]...)
	} else {
		favorites = append(favorites, dir)
		sort.Strings(favorites)
	}

	if err := s.saveFavorites(favorites); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"favorites": favorites,
		"favorited": found < 0,
	})
}

const webAppHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>mdviewer web preview</title>
  <link rel="icon" type="image/png" href="/icon.png" />
  <link rel="apple-touch-icon" href="/icon.png" />
  <script src="https://cdn.jsdelivr.net/npm/marked/marked.min.js"></script>
  <style>
    /* Default = dark. Light tokens are applied either:
       (a) when the system prefers light AND the user hasn't forced dark, or
       (b) when the user explicitly picks Light. */
    :root {
      --bg: oklch(0.19 0.02 262);
      --panel: oklch(0.23 0.025 262);
      --panel-2: oklch(0.26 0.03 262);
      --text: oklch(0.9 0.02 85);
      --muted: oklch(0.72 0.02 260);
      --accent: oklch(0.73 0.19 330);
      --accent-2: oklch(0.72 0.16 235);
      --line: oklch(0.38 0.05 275);
      --code: oklch(0.17 0.02 260);
      --shadow: 0 24px 80px color-mix(in oklab, black 58%, transparent);
      --sidebar-width: 320px;
      --splitter-width: 12px;
      --file-meta-width: 6.25rem;
      --search-panel-width: 240px;
    }
    /* Light token set, factored so we can apply via either media query or
       an explicit data-theme attribute. */
    @media (prefers-color-scheme: light) {
      :root:not([data-theme="dark"]) {
        --bg: oklch(0.985 0.005 250);
        --panel: oklch(0.96 0.008 250);
        --panel-2: oklch(0.92 0.012 255);
        --text: oklch(0.22 0.025 262);
        --muted: oklch(0.5 0.02 260);
        --accent: oklch(0.52 0.21 330);
        --accent-2: oklch(0.55 0.17 235);
        --line: oklch(0.86 0.01 260);
        --code: oklch(0.94 0.008 250);
        --shadow: 0 12px 40px color-mix(in oklab, black 12%, transparent);
      }
    }
    :root[data-theme="light"] {
      --bg: oklch(0.985 0.005 250);
      --panel: oklch(0.96 0.008 250);
      --panel-2: oklch(0.92 0.012 255);
      --text: oklch(0.22 0.025 262);
      --muted: oklch(0.5 0.02 260);
      --accent: oklch(0.52 0.21 330);
      --accent-2: oklch(0.55 0.17 235);
      --line: oklch(0.86 0.01 260);
      --code: oklch(0.94 0.008 250);
      --shadow: 0 12px 40px color-mix(in oklab, black 12%, transparent);
    }
    * { box-sizing: border-box; }
    html, body { height: 100%; margin: 0; }
    body {
      background:
        radial-gradient(circle at top left, color-mix(in oklab, var(--accent) 18%, transparent), transparent 30%),
        radial-gradient(circle at top right, color-mix(in oklab, var(--accent-2) 14%, transparent), transparent 32%),
        var(--bg);
      color: var(--text);
      font-family: ui-sans-serif, system-ui, sans-serif;
      overflow: hidden;
    }
    .app {
      display: grid;
      grid-template-columns:
        var(--sidebar-width)
        var(--splitter-width)
        minmax(0, 1fr)
        var(--splitter-width)
        var(--search-panel-width);
      height: 100vh;
      gap: 0;
      padding: 18px;
      overflow: hidden;
    }
    .app.sidebar-collapsed {
      grid-template-columns:
        0px
        0px
        minmax(0, 1fr)
        var(--splitter-width)
        var(--search-panel-width);
    }
    .app.search-panel-collapsed {
      grid-template-columns:
        var(--sidebar-width)
        var(--splitter-width)
        minmax(0, 1fr)
        0px
        0px;
    }
    .app.sidebar-collapsed.search-panel-collapsed {
      grid-template-columns: 0px 0px minmax(0, 1fr) 0px 0px;
    }
    .shell {
      background: color-mix(in oklab, var(--panel) 92%, black);
      border: 1px solid var(--line);
      border-radius: 20px;
      box-shadow: var(--shadow);
      overflow: hidden;
      min-height: 0;
      min-width: 0;
    }
    .sidebar-shell {
      margin-right: 9px;
      min-width: 0;
    }
    .sidebar {
      display: grid;
      grid-template-rows: auto minmax(0, 1fr) auto;
    }
    .app.sidebar-collapsed .sidebar-shell {
      opacity: 0;
      pointer-events: none;
      margin-right: 0;
    }
    .splitter {
      display: grid;
      place-items: center;
      cursor: col-resize;
      user-select: none;
    }
    .splitter::before {
      content: "";
      width: 4px;
      height: calc(100% - 24px);
      border-radius: 999px;
      background: color-mix(in oklab, var(--line) 88%, transparent);
      transition: background 120ms ease, transform 120ms ease;
    }
    .splitter:hover::before,
    .splitter.dragging::before {
      background: color-mix(in oklab, var(--accent) 55%, var(--accent-2));
      transform: scaleX(1.15);
    }
    .topbar {
      padding: 18px 20px 14px;
      border-bottom: 1px solid color-mix(in oklab, var(--line) 80%, transparent);
    }
    .sidebar-topbar {
      display: flex;
      justify-content: space-between;
      gap: 12px;
      align-items: flex-start;
    }
    .sidebar-topbar > div {
      min-width: 0;
      flex: 1 1 auto;
    }
    .eyebrow { color: var(--accent); font-size: 12px; text-transform: uppercase; letter-spacing: .18em; }
    .title { margin-top: 8px; font-size: 22px; font-weight: 700; }
    .brand-row {
      display: flex;
      align-items: center;
      gap: 10px;
      margin-bottom: 14px;
    }
    .brand-mark {
      width: 30px;
      height: 30px;
      flex-shrink: 0;
      color: var(--accent);
    }
    .brand-name {
      font-size: 15px;
      font-weight: 700;
      letter-spacing: .01em;
      color: var(--text);
    }
    .subtle { color: var(--muted); font-size: 13px; }
    #cwd {
      display: block;
      max-width: 100%;
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
    }
    .searchbox {
      margin-top: 14px;
    }
    .search-input {
      width: 100%;
      border: 1px solid color-mix(in oklab, var(--line) 85%, transparent);
      background: color-mix(in oklab, var(--panel-2) 86%, transparent);
      color: var(--text);
      border-radius: 12px;
      padding: 10px 12px;
      font: inherit;
      outline: none;
    }
    .search-input::placeholder {
      color: var(--muted);
    }
    .search-input:focus {
      border-color: color-mix(in oklab, var(--accent) 50%, var(--accent-2));
      box-shadow: 0 0 0 3px color-mix(in oklab, var(--accent) 18%, transparent);
    }
    .pane {
      min-height: 0;
      overflow: auto;
      padding: 14px 12px 10px;
      min-width: 0;
    }
    .file-header {
      display: flex;
      align-items: center;
      gap: 12px;
      padding: 0 12px 10px;
    }
    .header-button {
      border: 0;
      background: transparent;
      color: var(--muted);
      font-size: 11px;
      font-weight: 700;
      letter-spacing: .08em;
      text-transform: uppercase;
      padding: 0;
      cursor: pointer;
      text-align: left;
      flex: 1 1 auto;
      min-width: 0;
    }
    .header-button.size-col {
      flex: 0 0 var(--file-meta-width);
      min-width: var(--file-meta-width);
      margin-left: auto;
      text-align: right;
    }
    .header-button.active {
      color: var(--accent);
    }
    .header-button::after {
      content: "";
      margin-left: 6px;
    }
    .header-button.active[data-direction="asc"]::after {
      content: "↑";
    }
    .header-button.active[data-direction="desc"]::after {
      content: "↓";
    }
    /* Favorite row: container around the main button + edit button */
    .favorite-row {
      position: relative;
      display: flex;
      align-items: stretch;
      width: 100%;
      border-radius: 12px;
    }
    .favorite-row:hover { background: color-mix(in oklab, var(--panel-2) 80%, transparent); }
    .favorite-row.active { background: color-mix(in oklab, var(--accent) 16%, var(--panel-2)); }
    .favorite-main {
      flex: 1 1 auto;
      min-width: 0;
      display: flex;
      flex-direction: column;
      gap: 2px;
      padding: 8px 12px;
      border: 0;
      background: transparent;
      color: inherit;
      text-align: left;
      cursor: pointer;
      border-radius: 12px;
    }
    .favorite-alias {
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
      min-width: 0;
      font-weight: 500;
    }
    .favorite-sub {
      color: var(--muted);
      font-size: 11px;
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
      min-width: 0;
    }
    .favorite-edit {
      flex: 0 0 auto;
      border: 0;
      background: transparent;
      color: var(--muted);
      cursor: pointer;
      padding: 0 10px;
      opacity: 0;
      transition: opacity 120ms ease;
      font-size: 13px;
      border-radius: 8px;
    }
    .favorite-row:hover .favorite-edit { opacity: 1; }
    .favorite-edit:hover {
      background: color-mix(in oklab, var(--panel-2) 80%, transparent);
      color: var(--text);
    }
    .file, .favorite, .recent {
      display: flex;
      align-items: center;
      gap: 12px;
      width: 100%;
      min-width: 0;
      padding: 10px 12px;
      border: 0;
      background: transparent;
      color: inherit;
      text-align: left;
      border-radius: 12px;
      cursor: pointer;
    }
    .file:hover, .favorite:hover, .recent:hover { background: color-mix(in oklab, var(--panel-2) 80%, transparent); }
    .file.active, .favorite.active, .recent.active { background: color-mix(in oklab, var(--accent) 16%, var(--panel-2)); }
    .recent {
      display: grid;
      grid-template-columns: 1fr auto;
      gap: 4px 12px;
      padding: 8px 12px;
    }
    .recent-name {
      grid-column: 1;
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
      min-width: 0;
    }
    .recent-time {
      grid-column: 2;
      grid-row: 1;
      color: var(--muted);
      font-size: 11px;
      font-variant-numeric: tabular-nums;
      white-space: nowrap;
    }
    .recent-path {
      grid-column: 1 / span 2;
      grid-row: 2;
      color: var(--muted);
      font-size: 11px;
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
      min-width: 0;
    }
    .file-name, .favorite-name {
      flex: 1 1 auto;
      min-width: 0;
      max-width: 100%;
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
    }
    .file-name {
      max-width: calc(100% - var(--file-meta-width));
    }
    .file-meta {
      display: grid;
      grid-template-columns: 7px auto;
      align-items: center;
      gap: 6px;
      justify-content: end;
      margin-left: auto;
      flex: 0 0 var(--file-meta-width);
      white-space: nowrap;
      width: var(--file-meta-width);
      min-width: var(--file-meta-width);
      overflow: hidden;
    }
    .file-size {
      color: var(--muted);
      font-size: 11px;
      flex-shrink: 0;
      white-space: nowrap;
      font-variant-numeric: tabular-nums;
      text-align: right;
      justify-self: end;
      overflow: visible;
    }
    .update-badge {
      width: 7px;
      height: 7px;
      border-radius: 999px;
      background: transparent;
      flex-shrink: 0;
      display: none;
    }
    .file.has-flag .update-badge {
      display: block;
    }
    .file.flag-new .update-badge {
      background: oklch(0.78 0.16 200);
      box-shadow: 0 0 0 1px color-mix(in oklab, oklch(0.78 0.16 200) 35%, transparent);
    }
    .file.flag-recent .update-badge {
      background: oklch(0.8 0.16 95);
      box-shadow: 0 0 0 1px color-mix(in oklab, oklch(0.8 0.16 95) 35%, transparent);
    }
    .file.flag-updated .update-badge {
      background: oklch(0.74 0.18 330);
      box-shadow: 0 0 0 1px color-mix(in oklab, oklch(0.74 0.18 330) 35%, transparent);
    }
    /* Aggregate flag on a directory row — child contains a change.
       Render as a hollow ring so it reads as "something inside" rather than
       "this thing changed". */
    .file.flag-aggregate .update-badge {
      background: transparent;
      box-shadow: inset 0 0 0 1.5px currentColor;
      color: color-mix(in oklab, var(--accent) 70%, transparent);
      opacity: 0.85;
    }
    .file-match {
      color: oklch(0.72 0.21 22);
      font-weight: 800;
    }
    .section {
      border-top: 1px solid color-mix(in oklab, var(--line) 80%, transparent);
      padding: 14px 12px 16px;
    }
    .section-head {
      display: flex;
      align-items: center;
      justify-content: space-between;
      margin-bottom: 10px;
      padding: 0 8px;
    }
    .section-title { color: var(--accent); font-weight: 700; letter-spacing: .04em; }
    .section-toggle {
      display: flex;
      align-items: center;
      gap: 6px;
      padding: 0;
      background: none;
      border: 0;
      color: inherit;
      font: inherit;
      cursor: pointer;
      text-align: left;
    }
    .section-toggle:hover .section-chevron { color: var(--accent); }
    .section-toggle .section-title { color: var(--accent); font-weight: 700; letter-spacing: .04em; }
    .section-chevron {
      display: inline-block;
      width: 12px;
      color: var(--muted);
      transition: transform 120ms ease, color 120ms ease;
    }
    .section.collapsed .section-chevron { transform: rotate(-90deg); }
    .section.collapsed .section-list { display: none; }
    .section-actions {
      display: flex;
      gap: 4px;
      align-items: center;
    }
    .preview {
      display: grid;
      grid-template-rows: auto minmax(0, 1fr) auto;
      min-width: 0;
    }
    .preview-head, .preview-foot {
      padding: 16px 18px;
      border-bottom: 1px solid color-mix(in oklab, var(--line) 80%, transparent);
      display: flex;
      justify-content: space-between;
      gap: 16px;
      align-items: center;
    }
    .preview-head > div:first-child {
      display: block;
      min-width: 0;
      flex: 1 1 auto;
    }
    .preview-foot {
      border-top: 1px solid color-mix(in oklab, var(--line) 80%, transparent);
      border-bottom: 0;
      font-size: 13px;
      color: var(--muted);
    }
    .path-copy {
      display: inline-flex;
      align-items: center;
      gap: 8px;
      max-width: min(60%, 720px);
      padding: 6px 12px;
      border-radius: 999px;
      border: 1px solid color-mix(in oklab, var(--line) 85%, transparent);
      background: color-mix(in oklab, var(--panel-2) 60%, transparent);
      color: var(--text);
      font-family: ui-monospace, SFMono-Regular, monospace;
      font-size: 12px;
      cursor: pointer;
      transition: background 120ms ease, border-color 120ms ease, transform 80ms ease;
      min-width: 0;
    }
    .path-copy:hover {
      background: color-mix(in oklab, var(--accent) 18%, var(--panel-2));
      border-color: color-mix(in oklab, var(--accent) 45%, var(--line));
    }
    .path-copy:active {
      transform: scale(0.98);
    }
    .path-copy:disabled {
      opacity: 0.45;
      cursor: default;
    }
    .path-copy.copied {
      background: color-mix(in oklab, var(--accent) 38%, var(--panel-2));
      border-color: color-mix(in oklab, var(--accent) 70%, var(--line));
    }
    .path-copy-icon {
      font-size: 13px;
      color: color-mix(in oklab, var(--accent) 60%, var(--text));
      flex-shrink: 0;
    }
    .path-copy-label {
      flex: 1 1 auto;
      min-width: 0;
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
      direction: rtl;
      text-align: left;
    }
    .actions {
      display: flex;
      align-items: center;
      gap: 6px;
      flex: 0 0 auto;
    }
    .actions .divider {
      width: 1px;
      height: 22px;
      margin: 0 4px;
      background: color-mix(in oklab, var(--line) 75%, transparent);
      flex: 0 0 auto;
    }
    .chip, .action {
      border-radius: 999px;
      border: 1px solid color-mix(in oklab, var(--line) 85%, transparent);
      background: color-mix(in oklab, var(--panel-2) 78%, transparent);
      color: var(--text);
      padding: 7px 12px;
      font-size: 12px;
      font-weight: 500;
      letter-spacing: 0.005em;
      appearance: none;
      -webkit-appearance: none;
      display: inline-flex;
      align-items: center;
      gap: 6px;
      line-height: 1;
      transition:
        background 140ms ease,
        border-color 140ms ease,
        color 140ms ease,
        transform 80ms ease,
        box-shadow 140ms ease;
    }
    .action { cursor: pointer; }
    .action:hover:not(:disabled):not(.is-primary) {
      background: color-mix(in oklab, var(--panel-2) 92%, var(--text) 8%);
      border-color: color-mix(in oklab, var(--line) 60%, var(--text) 12%);
    }
    .action:active:not(:disabled) { transform: scale(0.97); }
    .action:focus-visible {
      outline: 2px solid color-mix(in oklab, var(--accent) 55%, transparent);
      outline-offset: 2px;
    }
    .action:disabled {
      opacity: 0.4;
      cursor: default;
    }
    .action .ico {
      width: 14px;
      height: 14px;
      flex: 0 0 auto;
      stroke: currentColor;
      stroke-width: 1.7;
      fill: none;
      stroke-linecap: round;
      stroke-linejoin: round;
      opacity: 0.85;
    }
    .action kbd {
      font-family: ui-monospace, SFMono-Regular, "JetBrains Mono", monospace;
      font-size: 10.5px;
      padding: 1px 5px;
      margin-left: 2px;
      border-radius: 4px;
      border: 1px solid color-mix(in oklab, var(--line) 70%, transparent);
      background: color-mix(in oklab, var(--bg) 60%, transparent);
      color: var(--muted);
      letter-spacing: 0.02em;
    }

    /* Segmented control for Preview/Edit (mutually exclusive modes) */
    .seg {
      display: inline-flex;
      padding: 3px;
      border-radius: 999px;
      border: 1px solid color-mix(in oklab, var(--line) 85%, transparent);
      background: color-mix(in oklab, var(--panel-2) 50%, transparent);
      gap: 2px;
    }
    .seg-btn {
      appearance: none;
      -webkit-appearance: none;
      border: 1px solid transparent;
      background: transparent;
      color: var(--muted);
      padding: 5px 11px;
      font-size: 12px;
      font-weight: 500;
      border-radius: 999px;
      cursor: pointer;
      display: inline-flex;
      align-items: center;
      gap: 5px;
      line-height: 1;
      transition: background 140ms ease, color 140ms ease, box-shadow 140ms ease;
    }
    .seg-btn .ico {
      width: 13px;
      height: 13px;
      stroke: currentColor;
      stroke-width: 1.7;
      fill: none;
      stroke-linecap: round;
      stroke-linejoin: round;
      opacity: 0.8;
    }
    .seg-btn:hover:not(:disabled):not(.active) {
      color: var(--text);
      background: color-mix(in oklab, var(--panel-2) 80%, transparent);
    }
    .seg-btn.active {
      color: var(--text);
      background: var(--panel);
      box-shadow:
        0 1px 0 color-mix(in oklab, var(--line) 60%, transparent),
        0 2px 6px color-mix(in oklab, black 18%, transparent);
    }
    .seg-btn.active .ico { opacity: 1; color: var(--accent); }
    .seg-btn:focus-visible {
      outline: 2px solid color-mix(in oklab, var(--accent) 55%, transparent);
      outline-offset: 2px;
    }
    .seg-btn:disabled { opacity: 0.4; cursor: default; }

    /* Icon-only round buttons */
    .action.icon-only {
      padding: 7px;
      width: 30px;
      height: 30px;
      justify-content: center;
    }
    .action.icon-only .ico { width: 15px; height: 15px; opacity: 0.85; }

    /* Save when dirty — primary emphasis */
    .action.is-primary {
      background: var(--accent);
      border-color: color-mix(in oklab, var(--accent) 80%, black 20%);
      color: oklch(0.99 0.005 250);
      box-shadow:
        0 1px 0 color-mix(in oklab, var(--accent) 50%, white 30%) inset,
        0 4px 12px color-mix(in oklab, var(--accent) 35%, transparent);
    }
    .action.is-primary:hover:not(:disabled) {
      background: color-mix(in oklab, var(--accent) 88%, white 12%);
      border-color: color-mix(in oklab, var(--accent) 72%, black 28%);
    }
    .action.is-primary .ico { opacity: 1; stroke-width: 2; }
    .action.is-primary kbd {
      background: color-mix(in oklab, black 22%, transparent);
      border-color: color-mix(in oklab, black 30%, transparent);
      color: color-mix(in oklab, white 85%, transparent);
    }

    /* Info chip — clearly non-interactive */
    .chip {
      cursor: default;
      padding: 6px 10px 6px 9px;
      background: transparent;
      border-color: color-mix(in oklab, var(--line) 60%, transparent);
      color: var(--muted);
      font-size: 11px;
      font-weight: 500;
      letter-spacing: 0.04em;
      text-transform: uppercase;
    }
    .chip::before {
      content: "";
      width: 6px;
      height: 6px;
      border-radius: 50%;
      background: var(--muted);
      flex: 0 0 auto;
      box-shadow: 0 0 0 2px color-mix(in oklab, var(--muted) 25%, transparent);
    }
    .chip[data-kind="markdown"]::before,
    .chip[data-kind="text"]::before { background: var(--accent); box-shadow: 0 0 0 2px color-mix(in oklab, var(--accent) 25%, transparent); }
    .chip[data-kind="image"]::before { background: var(--accent-2); box-shadow: 0 0 0 2px color-mix(in oklab, var(--accent-2) 25%, transparent); }
    .chip[data-kind="mermaid"]::before { background: oklch(0.78 0.16 145); box-shadow: 0 0 0 2px oklch(0.78 0.16 145 / 0.25); }
    .chip[data-kind="html"]::before { background: oklch(0.76 0.17 50); box-shadow: 0 0 0 2px oklch(0.76 0.17 50 / 0.25); }

    /* Sandboxed HTML preview — fills the preview body bleed-to-edge */
    .html-frame-wrap {
      position: absolute;
      inset: 0;
      display: grid;
      grid-template-rows: 1fr auto;
      background: var(--bg);
    }
    .html-frame {
      width: 100%;
      height: 100%;
      border: 0;
      background: #fff;
      display: block;
    }
    .html-frame-note {
      padding: 6px 14px;
      font-size: 11px;
      color: var(--muted);
      background: color-mix(in oklab, var(--panel) 90%, transparent);
      border-top: 1px solid color-mix(in oklab, var(--line) 70%, transparent);
      display: flex;
      gap: 8px;
      align-items: center;
      letter-spacing: 0.02em;
    }
    .html-frame-note a {
      color: var(--accent);
      text-decoration: none;
      border-bottom: 1px dashed color-mix(in oklab, var(--accent) 40%, transparent);
    }
    .html-frame-note a:hover {
      color: color-mix(in oklab, var(--accent) 85%, var(--text) 15%);
    }
    .preview-body:has(.html-frame-wrap) { padding: 0; position: relative; }
    .collapse-toggle {
      min-width: 40px;
      padding-inline: 0;
      font-weight: 700;
    }
    .reveal-sidebar {
      position: fixed;
      left: 18px;
      top: 18px;
      z-index: 20;
      display: none;
    }
    .app.sidebar-collapsed + .reveal-sidebar {
      display: inline-flex;
    }
    .collapse-search-panel {
      align-self: flex-end;
    }
    .reveal-search-panel {
      position: fixed;
      top: 18px;
      right: 18px;
      z-index: 20;
      display: none;
    }
    .app.search-panel-collapsed ~ .reveal-search-panel {
      display: inline-flex;
    }
    .reveal-search-panel[hidden] { display: none; }
    .preview-body {
      overflow: auto;
      padding: 24px clamp(18px, 4vw, 42px) 32px;
      min-width: 0;
      line-height: 1.65;
    }
    .editor-wrap {
      height: 100%;
      min-height: 100%;
      display: flex;
    }
    .editor {
      width: 100%;
      min-height: 100%;
      resize: none;
      border: 1px solid color-mix(in oklab, var(--line) 85%, transparent);
      border-radius: 16px;
      background: color-mix(in oklab, var(--code) 92%, black);
      color: var(--text);
      padding: 18px;
      font: 14px/1.6 ui-monospace, SFMono-Regular, monospace;
      outline: none;
      box-shadow: inset 0 1px 0 color-mix(in oklab, white 4%, transparent);
    }
    .editor:focus {
      border-color: color-mix(in oklab, var(--accent) 50%, var(--accent-2));
      box-shadow: 0 0 0 3px color-mix(in oklab, var(--accent) 18%, transparent);
    }
    .preview-body img {
      max-width: 100%;
      border-radius: 16px;
      border: 1px solid color-mix(in oklab, var(--line) 85%, transparent);
      box-shadow: var(--shadow);
      background: white;
    }
    .preview-body pre {
      overflow: auto;
      padding: 16px;
      border-radius: 16px;
      background: var(--code);
      border: 1px solid color-mix(in oklab, var(--line) 85%, transparent);
    }
    .preview-body code { font-family: ui-monospace, SFMono-Regular, monospace; }
    #previewTitle, #previewMeta {
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
    }
    .preview-body h1, .preview-body h2, .preview-body h3 { line-height: 1.15; }
    .preview-body blockquote {
      margin: 0;
      padding-left: 16px;
      border-left: 3px solid color-mix(in oklab, var(--accent) 40%, transparent);
      color: color-mix(in oklab, var(--text) 78%, var(--muted));
    }
    .mermaid {
      overflow: auto;
      padding: 12px;
      border-radius: 16px;
      background: white;
      position: relative;
    }
    /* Alt/Option held: enter text-selection mode. Pan/drag is suspended in
       JS, and we make SVG text actually selectable here. */
    .mermaid.alt-select {
      cursor: text !important;
      outline: 2px dashed color-mix(in oklab, var(--accent) 60%, transparent);
      outline-offset: 4px;
    }
    .mermaid.alt-select,
    .mermaid.alt-select svg,
    .mermaid.alt-select svg text,
    .mermaid.alt-select foreignObject,
    .mermaid.alt-select foreignObject * {
      user-select: text;
      -webkit-user-select: text;
    }
    /* Body-level override — covers the lightbox (which sets user-select:none)
       and beats most third-party extension stylesheets via !important. */
    body.alt-select-mode .mermaid,
    body.alt-select-mode .mermaid *,
    body.alt-select-mode .lightbox-stage .mermaid,
    body.alt-select-mode .lightbox-stage .mermaid * {
      user-select: text !important;
      -webkit-user-select: text !important;
      cursor: text !important;
    }
    /* Hover toolbar on top of mermaid diagrams. Renders only on hover so it
       doesn't compete with the diagram visually. */
    .mermaid-toolbar {
      position: absolute;
      top: 6px;
      right: 6px;
      display: flex;
      gap: 4px;
      opacity: 0;
      transition: opacity 120ms ease;
      pointer-events: none;
      z-index: 4;
    }
    .mermaid-wrap { position: relative; }
    .mermaid-wrap:hover .mermaid-toolbar,
    .mermaid-wrap:focus-within .mermaid-toolbar,
    .mermaid:hover .mermaid-toolbar,
    .mermaid:focus-within .mermaid-toolbar,
    .mermaid-toolbar.show {
      opacity: 1;
      pointer-events: auto;
    }
    .mermaid-tool-btn {
      border: 1px solid color-mix(in oklab, var(--line) 85%, transparent);
      background: color-mix(in oklab, var(--panel-2) 92%, transparent);
      color: var(--text);
      font-size: 11px;
      padding: 4px 8px;
      border-radius: 8px;
      cursor: pointer;
      box-shadow: 0 2px 6px rgba(0,0,0,0.15);
      backdrop-filter: blur(4px);
    }
    .mermaid-tool-btn:hover {
      background: color-mix(in oklab, var(--accent) 18%, var(--panel-2));
    }
    .mermaid-tool-btn.copied {
      background: color-mix(in oklab, oklch(0.7 0.18 150) 30%, var(--panel-2));
      border-color: color-mix(in oklab, oklch(0.7 0.18 150) 60%, transparent);
    }
    .empty {
      color: var(--muted);
      display: grid;
      place-items: center;
      min-height: 100%;
      text-align: center;
    }
    .floating-tooltip {
      position: fixed;
      top: 0;
      left: 0;
      max-width: 280px;
      white-space: pre-line;
      padding: 10px 12px;
      border-radius: 12px;
      background: color-mix(in oklab, var(--code) 96%, black);
      border: 1px solid color-mix(in oklab, var(--line) 85%, transparent);
      box-shadow: var(--shadow);
      color: var(--text);
      font-size: 12px;
      line-height: 1.45;
      z-index: 1000;
      pointer-events: none;
      opacity: 0;
      transform: translate3d(0, 0, 0);
      transition: opacity 120ms ease;
    }
    .floating-tooltip.visible {
      opacity: 1;
    }
    .floating-tooltip.single-line {
      max-width: min(80vw, 960px);
      white-space: nowrap;
    }
    .preview-body img,
    .preview-body .mermaid {
      cursor: zoom-in;
    }
    /* ---- "Show all" popup modal ---- */
    .popup-modal {
      position: fixed;
      inset: 0;
      background: color-mix(in oklab, black 55%, transparent);
      backdrop-filter: blur(4px);
      -webkit-backdrop-filter: blur(4px);
      z-index: 2400;
      display: flex;
      justify-content: center;
      align-items: flex-start;
      padding-top: 10vh;
    }
    .popup-modal[hidden] { display: none; }
    .popup-card {
      width: min(720px, 92vw);
      max-height: 80vh;
      background: var(--panel-2);
      border: 1px solid color-mix(in oklab, var(--line) 80%, transparent);
      border-radius: 14px;
      box-shadow: 0 20px 60px rgba(0,0,0,0.45);
      display: flex;
      flex-direction: column;
      overflow: hidden;
    }
    .popup-head {
      display: flex;
      align-items: center;
      justify-content: space-between;
      padding: 14px 16px 8px;
    }
    .popup-title {
      font-weight: 700;
      color: var(--accent);
      font-size: 14px;
      letter-spacing: 0.04em;
    }
    .popup-close {
      border: 0;
      background: transparent;
      color: var(--muted);
      cursor: pointer;
      font-size: 16px;
      padding: 4px 8px;
      border-radius: 8px;
    }
    .popup-close:hover { background: color-mix(in oklab, var(--panel-2) 80%, transparent); color: var(--text); }
    #popupSearch {
      border: 0;
      outline: 0;
      background: transparent;
      color: inherit;
      font-size: 13px;
      padding: 8px 16px 12px;
      border-bottom: 1px solid color-mix(in oklab, var(--line) 70%, transparent);
    }
    .popup-results {
      flex: 1 1 auto;
      overflow-y: auto;
      padding: 4px 0;
    }
    .popup-foot {
      font-size: 11px;
      padding: 8px 16px;
      border-top: 1px solid color-mix(in oklab, var(--line) 70%, transparent);
    }
    .popup-item {
      display: grid;
      grid-template-columns: auto 1fr auto auto;
      gap: 4px 12px;
      padding: 10px 16px;
      cursor: pointer;
      align-items: center;
    }
    .popup-item:hover, .popup-item.active {
      background: color-mix(in oklab, var(--accent) 14%, var(--panel-2));
    }
    .popup-icon { grid-column: 1; font-size: 13px; color: var(--muted); width: 1.5em; text-align: center; }
    .popup-name {
      grid-column: 2;
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
      min-width: 0;
      font-weight: 500;
    }
    .popup-time {
      grid-column: 3;
      font-size: 11px;
      color: var(--muted);
      white-space: nowrap;
      font-variant-numeric: tabular-nums;
    }
    .popup-status {
      grid-column: 4;
      font-size: 10px;
      padding: 2px 8px;
      border-radius: 999px;
      white-space: nowrap;
      letter-spacing: 0.04em;
      text-transform: uppercase;
    }
    .popup-status.state-updated {
      background: color-mix(in oklab, oklch(0.74 0.18 330) 25%, transparent);
      color: oklch(0.78 0.18 330);
    }
    .popup-status.state-unchanged {
      background: color-mix(in oklab, var(--muted) 18%, transparent);
      color: var(--muted);
    }
    .popup-status.state-unknown {
      background: transparent;
      color: var(--muted);
      opacity: 0.6;
    }
    .popup-path {
      grid-column: 2 / span 3;
      grid-row: 2;
      color: var(--muted);
      font-size: 11px;
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
      min-width: 0;
    }
    .popup-empty { padding: 24px 16px; text-align: center; color: var(--muted); }
    .popup-edit {
      grid-column: 5;
      border: 0;
      background: transparent;
      color: var(--muted);
      cursor: pointer;
      padding: 2px 8px;
      font-size: 13px;
      border-radius: 6px;
    }
    .popup-edit:hover { background: color-mix(in oklab, var(--panel-2) 80%, transparent); color: var(--text); }

    /* ---- Command palette (Cmd/Ctrl+K) ---- */
    .palette {
      position: fixed;
      inset: 0;
      background: color-mix(in oklab, black 55%, transparent);
      backdrop-filter: blur(4px);
      -webkit-backdrop-filter: blur(4px);
      z-index: 2500;
      display: flex;
      justify-content: center;
      align-items: flex-start;
      padding-top: 12vh;
    }
    .palette[hidden] { display: none; }
    .palette-card {
      width: min(640px, 90vw);
      background: var(--panel-2);
      border: 1px solid color-mix(in oklab, var(--line) 80%, transparent);
      border-radius: 14px;
      box-shadow: 0 20px 60px rgba(0,0,0,0.45);
      overflow: hidden;
      display: flex;
      flex-direction: column;
    }
    #paletteInput {
      border: 0;
      outline: 0;
      background: transparent;
      color: inherit;
      font-size: 15px;
      padding: 14px 16px;
      border-bottom: 1px solid color-mix(in oklab, var(--line) 70%, transparent);
    }
    .palette-hint {
      font-size: 11px;
      color: var(--muted);
      padding: 6px 16px;
      border-bottom: 1px solid color-mix(in oklab, var(--line) 70%, transparent);
    }
    .palette-results {
      max-height: 50vh;
      overflow-y: auto;
    }
    .palette-section {
      padding: 6px 16px 2px;
      font-size: 10px;
      letter-spacing: 0.08em;
      text-transform: uppercase;
      color: var(--muted);
    }
    .palette-empty {
      padding: 20px 16px;
      color: var(--muted);
      font-size: 13px;
      text-align: center;
    }
    .palette-item {
      display: grid;
      grid-template-columns: auto 1fr auto;
      gap: 4px 12px;
      padding: 8px 16px;
      cursor: pointer;
      align-items: center;
    }
    .palette-item:hover, .palette-item.active {
      background: color-mix(in oklab, var(--accent) 16%, var(--panel-2));
    }
    .palette-kind {
      grid-column: 1;
      font-size: 11px;
      color: var(--muted);
      width: 1.5em;
      text-align: center;
    }
    .palette-name {
      grid-column: 2;
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
    }
    .palette-time {
      grid-column: 3;
      font-size: 11px;
      color: var(--muted);
      white-space: nowrap;
    }
    .palette-path {
      grid-column: 2 / span 2;
      grid-row: 2;
      color: var(--muted);
      font-size: 11px;
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
    }
    .palette-match { color: oklch(0.78 0.16 200); font-weight: 700; }

    body.lightbox-open { overflow: hidden; }
    .lightbox {
      position: fixed;
      inset: 0;
      background: color-mix(in oklab, black 78%, transparent);
      backdrop-filter: blur(8px);
      -webkit-backdrop-filter: blur(8px);
      z-index: 2000;
      overflow: hidden;
      user-select: none;
      touch-action: none;
    }
    .lightbox[hidden] { display: none; }
    .lightbox-stage {
      position: absolute;
      top: 0;
      left: 0;
      transform-origin: 0 0;
      cursor: grab;
      will-change: transform;
    }
    .lightbox.dragging .lightbox-stage { cursor: grabbing; }
    .lightbox-stage > * {
      display: block;
      box-shadow: 0 24px 80px rgba(0,0,0,0.55);
      border-radius: 8px;
      background: white;
    }
    .lightbox-stage img,
    .lightbox-stage svg {
      max-width: none !important;
      max-height: none !important;
      border: 0;
    }
    .lightbox-stage .mermaid {
      overflow: visible !important;
      padding: 24px;
      margin: 0;
    }
    .lightbox-stage .mermaid > svg {
      display: block;
    }
    .lightbox-toolbar {
      position: fixed;
      top: 18px;
      right: 18px;
      display: flex;
      gap: 8px;
      z-index: 2001;
    }
    .lightbox-toolbar button {
      width: 38px;
      height: 38px;
      border-radius: 999px;
      border: 1px solid color-mix(in oklab, var(--line) 70%, transparent);
      background: color-mix(in oklab, var(--panel) 92%, black);
      color: var(--text);
      font-size: 16px;
      font-weight: 700;
      cursor: pointer;
      display: grid;
      place-items: center;
      padding: 0;
      box-shadow: 0 8px 24px rgba(0,0,0,0.35);
    }
    .lightbox-toolbar button:hover {
      background: color-mix(in oklab, var(--accent) 35%, var(--panel));
    }
    .lightbox-scale {
      position: fixed;
      top: 22px;
      left: 50%;
      transform: translateX(-50%);
      z-index: 2001;
      font-size: 12px;
      letter-spacing: 0.06em;
      color: color-mix(in oklab, white 85%, transparent);
      background: color-mix(in oklab, black 55%, transparent);
      padding: 6px 12px;
      border-radius: 999px;
      pointer-events: none;
      font-variant-numeric: tabular-nums;
    }
    .lightbox-hint {
      position: fixed;
      bottom: 18px;
      left: 50%;
      transform: translateX(-50%);
      z-index: 2001;
      font-size: 12px;
      color: color-mix(in oklab, white 80%, transparent);
      background: color-mix(in oklab, black 60%, transparent);
      padding: 8px 14px;
      border-radius: 999px;
      pointer-events: none;
    }
    @media (max-width: 960px) {
      .app { grid-template-columns: 1fr; grid-template-rows: 42vh 1fr; gap: 18px; }
      .splitter { display: none; }
      .app.sidebar-collapsed { grid-template-columns: 1fr; grid-template-rows: 1fr; }
      .app.sidebar-collapsed .sidebar-shell { display: none; }
      .reveal-sidebar { display: inline-flex; }
    }
    .search-panel {
      margin-left: 14px;
      padding: 14px 14px 18px;
      background: color-mix(in oklab, var(--panel) 92%, transparent);
      border: 1px solid var(--line);
      border-radius: 14px;
      overflow-y: auto;
      display: flex;
      flex-direction: column;
      gap: 14px;
    }
    .app.search-panel-collapsed .search-panel {
      display: none;
    }
    .search-panel-body {
      display: flex;
      flex-direction: column;
      gap: 14px;
    }
    .search-input {
      width: 100%;
      padding: 7px 10px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: var(--panel-2);
      color: var(--text);
      font-size: 13px;
    }
    .search-input:focus {
      outline: none;
      border-color: var(--accent);
    }
    .search-summary {
      font-size: 11px;
      color: var(--muted);
    }
    .search-section-title {
      font-size: 11px;
      text-transform: uppercase;
      letter-spacing: .18em;
      color: var(--muted);
    }
    .search-hit-list {
      display: flex;
      flex-direction: column;
      gap: 3px;
      max-height: 35vh;
      overflow-y: auto;
    }
    .search-hit {
      padding: 6px 8px;
      border-radius: 6px;
      cursor: pointer;
      font-size: 12px;
      color: var(--text);
      line-height: 1.45;
    }
    .search-hit:hover { background: var(--panel-2); }
    .search-hit .search-hit-needle {
      color: var(--accent);
      font-weight: 600;
    }
    .search-file-row {
      padding: 6px 8px;
      border-radius: 6px;
      cursor: pointer;
      font-size: 13px;
      color: var(--text);
      display: flex;
      justify-content: space-between;
      gap: 8px;
    }
    .search-file-row:hover { background: var(--panel-2); }
    .search-file-row .search-file-count {
      color: var(--muted);
      font-size: 11px;
    }
    .search-empty {
      color: var(--muted);
      font-size: 12px;
      font-style: italic;
    }
    mark.search-mark {
      background: color-mix(in oklab, var(--accent) 35%, transparent);
      color: inherit;
      border-radius: 3px;
      padding: 0 2px;
    }
    mark.search-mark.current {
      background: var(--accent);
      color: var(--bg);
    }
    .usage-guide {
      background: color-mix(in oklab, var(--accent) 6%, var(--panel));
      border: 1px dashed color-mix(in oklab, var(--accent) 45%, var(--line));
      border-radius: 12px;
      padding: 4px 18px 24px;
    }
    .usage-guide-banner {
      display: flex;
      align-items: center;
      gap: 12px;
      padding: 14px 0;
      margin: 0 0 12px 0;
      border-bottom: 1px solid color-mix(in oklab, var(--accent) 30%, var(--line));
    }
    .usage-guide-icon {
      font-size: 22px;
      line-height: 1;
    }
    .usage-guide-title {
      font-size: 13px;
      font-weight: 700;
      letter-spacing: .04em;
      color: var(--accent);
      text-transform: uppercase;
    }
    .usage-guide-subtitle {
      font-size: 12px;
      color: var(--muted);
      margin-top: 2px;
    }
    .usage-guide-body > :first-child { margin-top: 0; }
  </style>
  <script>
    // Apply theme BEFORE first paint to avoid a flash of the wrong colors.
    (function() {
      try {
        var t = localStorage.getItem("mdviewer.theme") || "auto";
        if (t === "light" || t === "dark") {
          document.documentElement.setAttribute("data-theme", t);
        }
      } catch (e) {}
    })();
  </script>
</head>
<body>
  <div class="app" id="appShell">
    <aside class="shell sidebar sidebar-shell">
      <div class="topbar sidebar-topbar">
        <div>
          <div class="brand-row">
            <svg class="brand-mark" viewBox="0 0 100 100" xmlns="http://www.w3.org/2000/svg"
                 fill="none" stroke="currentColor" stroke-width="7"
                 stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
              <rect x="5" y="11" width="90" height="78" rx="10" />
              <path d="M 21.5 27.5 L 21.5 72.5 M 21.5 27.5 L 37.2 56.2 L 52.9 27.5 L 52.9 72.5" />
              <line x1="69.2" y1="33.4" x2="69.2" y2="72.5" />
              <polyline points="64.1,55.9 69.2,72.5 74.3,55.9" />
            </svg>
            <span class="brand-name">MD Viewer</span>
          </div>
          <div class="eyebrow">Local Preview</div>
          <div class="title">Markdown Browser</div>
          <div class="subtle" id="cwd"></div>
          <div class="searchbox">
            <input class="search-input" id="searchInput" type="search" placeholder="Search files" spellcheck="false" />
          </div>
          <div class="searchbox path-jump">
            <input class="search-input" id="pathInput" type="text" placeholder="Jump to path (Enter)…  e.g. ~/notes/foo.md" spellcheck="false" autocomplete="off" />
          </div>
        </div>
        <button class="action collapse-toggle" id="collapseSidebar" title="Collapse sidebar">‹</button>
      </div>
      <div class="pane">
        <div class="file-header">
          <button class="header-button active" id="sortName" data-direction="asc" type="button">Name</button>
          <button class="header-button size-col" id="sortSize" data-direction="asc" type="button">Size</button>
        </div>
        <div id="files"></div>
      </div>
      <div class="section" data-section="recentFiles">
        <div class="section-head">
          <button class="section-toggle" type="button" aria-expanded="true" title="Collapse section">
            <span class="section-chevron">▾</span>
            <span class="section-title">Recent files</span>
          </button>
          <div class="section-actions">
            <button class="action" id="showAllRecentFiles" title="Show all recent files" hidden>Show all</button>
            <button class="action" id="clearRecentFiles" title="Clear recent files">Clear</button>
          </div>
        </div>
        <div class="section-list" id="recentFiles"></div>
      </div>
      <div class="section" data-section="recentDirs">
        <div class="section-head">
          <button class="section-toggle" type="button" aria-expanded="true" title="Collapse section">
            <span class="section-chevron">▾</span>
            <span class="section-title">Recent folders</span>
          </button>
          <div class="section-actions">
            <button class="action" id="showAllRecentDirs" title="Show all recent folders" hidden>Show all</button>
            <button class="action" id="clearRecentDirs" title="Clear recent folders">Clear</button>
          </div>
        </div>
        <div class="section-list" id="recentDirs"></div>
      </div>
      <div class="section" data-section="favorites">
        <div class="section-head">
          <button class="section-toggle" type="button" aria-expanded="true" title="Collapse section">
            <span class="section-chevron">▾</span>
            <span class="section-title">Favorites</span>
          </button>
          <div class="section-actions">
            <button class="action" id="showAllFavorites" title="Show all favorites" hidden>Show all</button>
            <button class="action" id="toggleFavorite">Toggle current</button>
          </div>
        </div>
        <div class="section-list" id="favorites"></div>
      </div>
    </aside>
    <div class="splitter" id="splitter" aria-hidden="true"></div>

    <main class="shell preview">
      <div class="preview-head">
        <div>
          <div class="eyebrow">Preview</div>
          <div class="title" id="previewTitle">Select a file</div>
          <div class="subtle" id="previewMeta"></div>
        </div>
        <div class="actions">
          <div class="seg" role="tablist" aria-label="View mode">
            <button class="seg-btn" id="previewModeButton" type="button" role="tab" aria-selected="false">
              <svg class="ico" viewBox="0 0 24 24" aria-hidden="true"><path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12Z"/><circle cx="12" cy="12" r="3"/></svg>
              <span>Preview</span>
            </button>
            <button class="seg-btn" id="editModeButton" type="button" role="tab" aria-selected="false">
              <svg class="ico" viewBox="0 0 24 24" aria-hidden="true"><path d="M12 20h9"/><path d="M16.5 3.5a2.121 2.121 0 1 1 3 3L7 19l-4 1 1-4Z"/></svg>
              <span>Edit</span>
            </button>
          </div>
          <button class="action" id="saveButton" type="button" title="Save (⌘S)">
            <svg class="ico" viewBox="0 0 24 24" aria-hidden="true"><path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2Z"/><path d="M17 21v-8H7v8M7 3v5h8"/></svg>
            <span>Save</span>
            <kbd>⌘S</kbd>
          </button>
          <button class="action icon-only" id="refreshButton" type="button" title="Refresh" aria-label="Refresh">
            <svg class="ico" viewBox="0 0 24 24" aria-hidden="true"><path d="M3 12a9 9 0 0 1 15.5-6.3L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-15.5 6.3L3 16"/><path d="M3 21v-5h5"/></svg>
          </button>
          <span class="divider" aria-hidden="true"></span>
          <button class="action icon-only" id="themeToggle" type="button" title="Cycle theme: Auto → Light → Dark" aria-label="Theme">
            <svg class="ico" viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.41-1.41M17.66 6.34l1.41-1.41"/></svg>
          </button>
          <span class="chip" id="kindChip" data-kind="idle" aria-live="polite">Idle</span>
        </div>
      </div>
      <div class="preview-body" id="previewBody">
        <div class="empty">Choose a Markdown, text, Mermaid, or image file from the left.</div>
      </div>
      <div class="preview-foot">
        <span id="statusText">Ready</span>
        <button type="button" id="copyPathBtn" class="path-copy" data-path="" disabled>
          <span class="path-copy-icon" aria-hidden="true">⧉</span>
          <span class="path-copy-label">No file selected</span>
        </button>
        <span id="scrollText">Preview 0%</span>
      </div>
    </main>
    <div class="splitter" id="rightSplitter" aria-hidden="true"></div>
    <aside id="searchPanel" class="shell search-panel" aria-label="Search panel">
      <button class="action collapse-search-panel" id="collapseSearchPanel" type="button" title="Hide search panel">&#x203A;</button>
      <div class="search-panel-body">
        <input type="search" class="search-input" id="searchPanelInput" placeholder="Search in this folder&#x2026;" spellcheck="false" autocomplete="off" />
        <div>
          <div class="search-section-title">In this file</div>
          <div class="search-summary" id="searchInFileSummary">Type to search.</div>
          <div class="search-hit-list" id="searchInFileHits"></div>
        </div>
        <div>
          <div class="search-section-title">Same folder</div>
          <div class="search-hit-list" id="searchFolderHits"></div>
        </div>
      </div>
    </aside>
  </div>
  <button class="action reveal-sidebar" id="revealSidebar" title="Show sidebar">☰ Files</button>
  <button class="action reveal-search-panel" id="revealSearchPanel" type="button" title="Show search panel" hidden>&#x1F50D; Search</button>
  <div class="floating-tooltip" id="floatingTooltip"></div>
  <div class="popup-modal" id="listPopup" hidden>
    <div class="popup-card">
      <div class="popup-head">
        <div class="popup-title" id="popupTitle">Items</div>
        <button type="button" class="popup-close" id="popupClose" title="Close">✕</button>
      </div>
      <input type="text" id="popupSearch" placeholder="Filter…" autocomplete="off" spellcheck="false" />
      <div id="popupResults" class="popup-results"></div>
      <div class="popup-foot subtle">Click to open · Esc to close</div>
    </div>
  </div>
  <div class="palette" id="palette" hidden>
    <div class="palette-card">
      <input type="text" id="paletteInput" placeholder="Search recent files & folders… (Cmd/Ctrl+K)" autocomplete="off" spellcheck="false" />
      <div class="palette-hint">↑↓ navigate · Enter open · Esc close</div>
      <div id="paletteResults" class="palette-results"></div>
    </div>
  </div>
  <div class="lightbox" id="lightbox" hidden>
    <div class="lightbox-stage" id="lightboxStage"></div>
    <div class="lightbox-scale" id="lightboxScale">100%</div>
    <div class="lightbox-toolbar">
      <button type="button" data-action="zoom-out" title="Zoom out">−</button>
      <button type="button" data-action="reset" title="Reset (Double-click)">⤢</button>
      <button type="button" data-action="zoom-in" title="Zoom in">+</button>
      <button type="button" data-action="close" title="Close (Esc)">✕</button>
    </div>
    <div class="lightbox-hint">Wheel: zoom · Drag: pan · Double-click: reset · Esc: close</div>
  </div>

  <script type="module">
    // Pinned to 11.13.0 — the "polished" 11.x release with backward-compat
    // fixes and new diagram types (Venn, Ishikawa). 11.14/11.15 may bring
    // regressions, so bump explicitly after verifying.
    import mermaid from "https://cdn.jsdelivr.net/npm/mermaid@11.13.0/dist/mermaid.esm.min.mjs";
    mermaid.initialize({ startOnLoad: false, theme: "neutral", securityLevel: "loose" });

    // --- Change-tracking persistence (P2) ---
    const TRACKING_STORAGE_KEY = "mdviewer.changeTracking.v1";
    const TRACKING_DISMISS_TTL_MS = 30 * 24 * 60 * 60 * 1000; // 30 days
    const TRACKING_DISMISS_MAX = 5000;
    const RECENT_FILES_MAX = 50;
    const RECENT_DIRS_MAX = 20;

    function loadTracking() {
      try {
        const raw = JSON.parse(localStorage.getItem(TRACKING_STORAGE_KEY) || "{}");
        return {
          dirSnapshots: (raw && typeof raw.dirSnapshots === "object" && raw.dirSnapshots) ? raw.dirSnapshots : {},
          dismissedEntryMap: (raw && typeof raw.dismissedEntryMap === "object" && raw.dismissedEntryMap) ? raw.dismissedEntryMap : {},
          dismissedAt: (raw && typeof raw.dismissedAt === "object" && raw.dismissedAt) ? raw.dismissedAt : {},
          lastSeenAt: typeof raw.lastSeenAt === "number" ? raw.lastSeenAt : Date.now(),
          recentFiles: Array.isArray(raw.recentFiles) ? raw.recentFiles : [],
          recentDirs: Array.isArray(raw.recentDirs) ? raw.recentDirs : [],
          aliases: (raw && typeof raw.aliases === "object" && raw.aliases) ? raw.aliases : {},
        };
      } catch {
        return { dirSnapshots: {}, dismissedEntryMap: {}, dismissedAt: {}, lastSeenAt: Date.now(), recentFiles: [], recentDirs: [], aliases: {} };
      }
    }

    function gcDismissed() {
      const now = Date.now();
      const entries = Object.entries(state.dismissedAt);
      // Drop expired
      for (const [p, ts] of entries) {
        if (typeof ts !== "number" || now - ts > TRACKING_DISMISS_TTL_MS) {
          delete state.dismissedEntryMap[p];
          delete state.dismissedAt[p];
        }
      }
      // Cap size: keep newest by timestamp
      const keys = Object.keys(state.dismissedAt);
      if (keys.length > TRACKING_DISMISS_MAX) {
        const sorted = keys.sort((a, b) => state.dismissedAt[a] - state.dismissedAt[b]);
        const drop = sorted.slice(0, keys.length - TRACKING_DISMISS_MAX);
        for (const p of drop) {
          delete state.dismissedEntryMap[p];
          delete state.dismissedAt[p];
        }
      }
    }

    function saveTracking() {
      try {
        gcDismissed();
        localStorage.setItem(TRACKING_STORAGE_KEY, JSON.stringify({
          dirSnapshots: state.dirSnapshots,
          dismissedEntryMap: state.dismissedEntryMap,
          dismissedAt: state.dismissedAt,
          lastSeenAt: Date.now(),
          recentFiles: state.recentFiles,
          recentDirs: state.recentDirs,
          aliases: state.aliases,
        }));
      } catch {
        // Storage may be unavailable / quota exceeded — degrade silently.
      }
    }

    const __tracking = loadTracking();

    const state = {
      cwd: "",
      entries: [],
      sessionStartedAt: Date.now(),
      // Per-directory snapshots: { [dir]: { [path]: modTime } } (P1)
      dirSnapshots: __tracking.dirSnapshots,
      // Active visual flags for the current sidebar: { [path]: "new"|"updated"|"recent" }
      fileFlags: {},
      // Dismissed map: which modTime the user has already acknowledged for a path.
      dismissedEntryMap: __tracking.dismissedEntryMap,
      // When each dismissal happened (for GC).
      dismissedAt: __tracking.dismissedAt,
      // Timestamp of the previous session's end — used as the "recent" cutoff (P3).
      lastSeenAt: __tracking.lastSeenAt,
      // Recent files/dirs — MRU queues. Most-recent first.
      // Items: { path, name, kind?, openedAt, lastSeenModTime }
      recentFiles: __tracking.recentFiles,
      recentDirs: __tracking.recentDirs,
      // path -> alias (user-friendly label). Currently shown for favorites.
      aliases: __tracking.aliases,
      paletteOpen: false,
      paletteQuery: "",
      paletteIndex: 0,
      popupKind: "",
      popupQuery: "",
      searchQuery: "",
      sortKey: "name",
      sortDirection: "asc",
      selectedPath: "",
      selectedHash: "",
      favorites: [],
      selectedKind: "",
      selectedModTime: "",
      selectedContent: "",
      editorMode: "preview",
      editDraft: "",
      editBaseModTime: "",
      editDirty: false,
      restoringHistory: false,
      sidebarWidth: Number(localStorage.getItem("mdviewer.sidebarWidth") || 320),
      searchPanelWidth: Number(localStorage.getItem("mdviewer.searchPanelWidth") || 240),
      sidebarCollapsed: localStorage.getItem("mdviewer.sidebarCollapsed") === "1",
      searchPanelCollapsed: localStorage.getItem("mdviewer.searchPanelCollapsed") === "1",
      // Finder-style hidden-file toggle. Persisted; flipped by Cmd/Ctrl+Shift+.
      showHidden: localStorage.getItem("mdviewer.showHidden") === "1",
      searchQueryRight: "",   // distinct from the left-sidebar file-name search
      searchInFileHits: [],   // array of <mark> elements in preview order
      searchInFileFocus: -1,  // index of the currently emphasized hit
      sectionCollapsed: {
        recentFiles: localStorage.getItem("mdviewer.section.recentFiles.collapsed") === "1",
        recentDirs:  localStorage.getItem("mdviewer.section.recentDirs.collapsed") === "1",
        favorites:   localStorage.getItem("mdviewer.section.favorites.collapsed") === "1",
      },
    };

    // Persist lastSeenAt on unload so the next session can use it for "recent" detection.
    window.addEventListener("beforeunload", () => { try { saveTracking(); } catch {} });

    const appShellEl = document.getElementById("appShell");
    const filesEl = document.getElementById("files");
    const favoritesEl = document.getElementById("favorites");
    const recentFilesEl = document.getElementById("recentFiles");
    const recentDirsEl = document.getElementById("recentDirs");
    const searchInputEl = document.getElementById("searchInput");
    const pathInputEl = document.getElementById("pathInput");
    const sortNameEl = document.getElementById("sortName");
    const sortSizeEl = document.getElementById("sortSize");
    const cwdEl = document.getElementById("cwd");
    const previewTitleEl = document.getElementById("previewTitle");
    const previewMetaEl = document.getElementById("previewMeta");
    const previewBodyEl = document.getElementById("previewBody");
    const kindChipEl = document.getElementById("kindChip");
    const statusTextEl = document.getElementById("statusText");
    const scrollTextEl = document.getElementById("scrollText");
    const copyPathBtnEl = document.getElementById("copyPathBtn");
    const copyPathLabelEl = copyPathBtnEl.querySelector(".path-copy-label");
    const copyPathIconEl = copyPathBtnEl.querySelector(".path-copy-icon");
    const previewModeButtonEl = document.getElementById("previewModeButton");
    const editModeButtonEl = document.getElementById("editModeButton");
    const saveButtonEl = document.getElementById("saveButton");
    const floatingTooltipEl = document.getElementById("floatingTooltip");
    const splitterEl = document.getElementById("splitter");
    const rightSplitterEl = document.getElementById("rightSplitter");
    const collapseSidebarEl = document.getElementById("collapseSidebar");
    const revealSidebarEl = document.getElementById("revealSidebar");

    function applySidebarLayout() {
      const minWidth = 140;
      const maxWidth = Math.max(minWidth, window.innerWidth - 260);
      const width = Math.min(maxWidth, Math.max(minWidth, state.sidebarWidth || 320));
      state.sidebarWidth = width;
      document.documentElement.style.setProperty("--sidebar-width", width + "px");
      appShellEl.classList.toggle("sidebar-collapsed", !!state.sidebarCollapsed);
      collapseSidebarEl.textContent = state.sidebarCollapsed ? "›" : "‹";
      collapseSidebarEl.title = state.sidebarCollapsed ? "Expand sidebar" : "Collapse sidebar";
      localStorage.setItem("mdviewer.sidebarWidth", String(width));
      localStorage.setItem("mdviewer.sidebarCollapsed", state.sidebarCollapsed ? "1" : "0");
    }

    function applySectionLayout(name) {
      const sec = document.querySelector('.section[data-section="' + name + '"]');
      if (!sec) return;
      const collapsed = !!(state.sectionCollapsed && state.sectionCollapsed[name]);
      sec.classList.toggle("collapsed", collapsed);
      const btn = sec.querySelector(".section-toggle");
      if (btn) {
        btn.setAttribute("aria-expanded", collapsed ? "false" : "true");
        btn.title = collapsed ? "Expand section" : "Collapse section";
      }
      try {
        localStorage.setItem("mdviewer.section." + name + ".collapsed", collapsed ? "1" : "0");
      } catch (e) {}
    }

    function applyAllSectionLayouts() {
      applySectionLayout("recentFiles");
      applySectionLayout("recentDirs");
      applySectionLayout("favorites");
    }

    function toggleSection(name) {
      if (!state.sectionCollapsed) state.sectionCollapsed = {};
      state.sectionCollapsed[name] = !state.sectionCollapsed[name];
      applySectionLayout(name);
    }

    function applySearchPanelLayout() {
      const minWidth = 200;
      const maxWidth = Math.max(minWidth, window.innerWidth - 360);
      const width = Math.min(maxWidth, Math.max(minWidth, state.searchPanelWidth || 240));
      state.searchPanelWidth = width;
      document.documentElement.style.setProperty("--search-panel-width", width + "px");
      localStorage.setItem("mdviewer.searchPanelWidth", String(width));
    }

    function slugify(text) {
      return text
        .toLowerCase()
        .trim()
        .replace(/[^\p{L}\p{N}\s-]/gu, "")
        .replace(/\s+/g, "-")
        .replace(/-+/g, "-");
    }

    function splitTarget(target) {
      const hashIndex = target.indexOf("#");
      if (hashIndex === -1) return { path: target, hash: "" };
      return {
        path: target.slice(0, hashIndex),
        hash: target.slice(hashIndex + 1),
      };
    }

    function resolveLocalTarget(target) {
      const { path, hash } = splitTarget(target);
      if (!path) {
        return { path: state.selectedPath, hash };
      }
      if (path.startsWith("/")) {
        return { path, hash };
      }
      const baseDir = state.selectedPath ? state.selectedPath.replace(/\/[^/]*$/, "") : state.cwd;
      const resolved = new URL(path, "file://" + (baseDir.endsWith("/") ? baseDir : baseDir + "/")).pathname;
      return { path: decodeURIComponent(resolved), hash };
    }

    function scrollToHash(hash) {
      if (!hash) return;
      const target = previewBodyEl.querySelector("#" + CSS.escape(hash));
      if (target) {
        target.scrollIntoView({ block: "start", behavior: "smooth" });
      }
    }

    function decorateRenderedMarkdown() {
      const headings = previewBodyEl.querySelectorAll("h1, h2, h3, h4, h5, h6");
      for (const heading of headings) {
        if (!heading.id) {
          heading.id = slugify(heading.textContent || "");
        }
      }

      const images = previewBodyEl.querySelectorAll("img");
      for (const img of images) {
        const src = img.getAttribute("src");
        if (!src || /^(https?:|data:|blob:)/i.test(src)) continue;
        const resolved = resolveLocalTarget(src);
        img.src = "/api/raw?path=" + encodeURIComponent(resolved.path) + "&t=" + Date.now();
      }

      const links = previewBodyEl.querySelectorAll("a[href]");
      for (const link of links) {
        const href = link.getAttribute("href");
        if (!href) continue;
        if (/^(https?:|mailto:|tel:)/i.test(href)) {
          link.target = "_blank";
          link.rel = "noreferrer noopener";
          continue;
        }
        link.dataset.internalHref = href;
      }
    }

    function humanSize(size) {
      if (!size) return "-";
      const units = ["B", "KB", "MB", "GB", "TB"];
      let value = size;
      let unit = units[0];
      for (let i = 1; i < units.length && value >= 1024; i += 1) {
        value /= 1024;
        unit = units[i];
      }
      return unit === "B" ? value + unit : (value >= 10 ? value.toFixed(0) : value.toFixed(1)) + unit;
    }

    function escapeHTML(text) {
      return text
        .replaceAll("&", "&amp;")
        .replaceAll("<", "&lt;")
        .replaceAll(">", "&gt;")
        .replaceAll('"', "&quot;")
        .replaceAll("'", "&#39;");
    }

    function escapeRegExp(text) {
      return text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    }

    function highlightName(text, query) {
      if (!query) {
        return escapeHTML(text);
      }
      const pattern = new RegExp("(" + escapeRegExp(query) + ")", "ig");
      return escapeHTML(text).replace(pattern, '<span class="file-match">$1</span>');
    }

    function shortenFavoritePath(path) {
      if (!path) return "";
      const parts = path.split("/").filter(Boolean);
      if (parts.length <= 2) return path;
      return "…/" + parts.slice(-2).join("/");
    }

    function shortenDisplayPath(path) {
      if (!path) return "";
      const parts = path.split("/").filter(Boolean);
      if (parts.length <= 4) return path;
      return "/" + parts.slice(0, 2).join("/") + "/…/" + parts.slice(-2).join("/");
    }

    function compareEntries(a, b) {
      if (a.name === "..") return -1;
      if (b.name === "..") return 1;
      if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1;

      let result = 0;
      if (state.sortKey === "size") {
        result = (a.size || 0) - (b.size || 0);
        if (result === 0) {
          result = a.name.localeCompare(b.name);
        }
      } else {
        result = a.name.localeCompare(b.name);
      }

      return state.sortDirection === "asc" ? result : -result;
    }

    function updateSortButtons() {
      sortNameEl.classList.toggle("active", state.sortKey === "name");
      sortSizeEl.classList.toggle("active", state.sortKey === "size");
      sortNameEl.dataset.direction = state.sortDirection;
      sortSizeEl.dataset.direction = state.sortDirection;
    }

    function canEditKind(kind) {
      return kind === "markdown" || kind === "text";
    }

    function updateEditorButtons() {
      const editable = canEditKind(state.selectedKind);
      const previewActive = state.editorMode === "preview";
      const editActive = state.editorMode === "edit";
      previewModeButtonEl.classList.toggle("active", previewActive);
      editModeButtonEl.classList.toggle("active", editActive);
      previewModeButtonEl.setAttribute("aria-selected", previewActive ? "true" : "false");
      editModeButtonEl.setAttribute("aria-selected", editActive ? "true" : "false");
      editModeButtonEl.disabled = !editable || !state.selectedPath;
      const canSave = editable && state.selectedPath && state.editDirty;
      saveButtonEl.disabled = !canSave;
      saveButtonEl.classList.toggle("is-primary", !!canSave);
    }

    function setEditorMode(mode) {
      state.editorMode = mode === "edit" ? "edit" : "preview";
      updateEditorButtons();
      if (state.selectedPath) {
        renderCurrentView();
      }
    }

    function formatMetaTime(value) {
      if (!value) return "Unknown";
      return new Date(value).toLocaleString();
    }

    function describeEntryMeta(entry) {
      const flag = state.fileFlags[entry.path] || "";
      const lines = [];
      if (flag) {
        lines.push("Status: " + flag.charAt(0).toUpperCase() + flag.slice(1));
      }
      lines.push("Updated: " + formatMetaTime(entry.mod_time || entry.modTime));
      if (!entry.is_dir) {
        lines.push("Size: " + humanSize(entry.size));
      }
      return lines.join("\n");
    }

    function showTooltip(text, x, y, options = {}) {
      if (!text) return;
      floatingTooltipEl.textContent = text;
      floatingTooltipEl.classList.toggle("single-line", !!options.singleLine);
      floatingTooltipEl.classList.add("visible");
      const margin = 16;
      const rect = floatingTooltipEl.getBoundingClientRect();
      const left = Math.min(x + 14, window.innerWidth - rect.width - margin);
      const top = Math.min(y + 14, window.innerHeight - rect.height - margin);
      floatingTooltipEl.style.left = Math.max(margin, left) + "px";
      floatingTooltipEl.style.top = Math.max(margin, top) + "px";
    }

    function hideTooltip() {
      floatingTooltipEl.classList.remove("visible");
      floatingTooltipEl.classList.remove("single-line");
    }

    function setSidebarCollapsed(collapsed) {
      state.sidebarCollapsed = collapsed;
      applySidebarLayout();
    }

    function applySearchPanelCollapsed() {
      appShellEl.classList.toggle("search-panel-collapsed", state.searchPanelCollapsed);
      revealSearchPanelEl.hidden = !state.searchPanelCollapsed;
      try {
        localStorage.setItem("mdviewer.searchPanelCollapsed", state.searchPanelCollapsed ? "1" : "0");
      } catch (e) {}
    }

    function updateChangedPaths(dir, entries, options = {}) {
      const nextMap = {};
      // Preserve any existing flags from other directories (P1: per-dir snapshots
      // mean flags lit elsewhere shouldn't be wiped when switching directories).
      const nextFlags = { ...state.fileFlags };

      const prevForDir = state.dirSnapshots[dir];
      const firstVisitToDir = !prevForDir;

      for (const entry of entries) {
        const modTime = entry.mod_time || entry.modTime || "";
        nextMap[entry.path] = modTime;
        if (entry.is_dir) {
          continue;
        }

        const dismissed = state.dismissedEntryMap[entry.path];

        if (firstVisitToDir) {
          // P3: use the previous-session boundary (lastSeenAt) instead of a
          // 5-minute window around session start. "Recent" now means
          // "changed since you last had this app open".
          const isRecent = modTime && Date.parse(modTime) >= state.lastSeenAt;
          if (isRecent && dismissed !== modTime) {
            nextFlags[entry.path] = "recent";
          }
          continue;
        }

        const previous = prevForDir[entry.path];
        if (typeof previous === "undefined") {
          if (dismissed !== modTime) {
            nextFlags[entry.path] = "new";
          }
          continue;
        }

        if (previous !== modTime && dismissed !== modTime) {
          nextFlags[entry.path] = "updated";
        }
      }

      // Drop flags for paths that used to be in this directory but no longer are.
      // (Flags on paths belonging to OTHER directories — still tracked in
      // state.dirSnapshots — must be preserved.)
      if (prevForDir) {
        for (const p of Object.keys(nextFlags)) {
          if ((p in prevForDir) && !(p in nextMap)) {
            delete nextFlags[p];
          }
        }
      }

      // The 'silent' option only suppresses the status-bar message in loadDir;
      // change detection itself must still run, otherwise the periodic
      // background refresh (refreshCurrentDir, every 2.5s) — which is the only
      // path that ever sees external edits while the user sits on the
      // directory — will never light a dot.
      state.dirSnapshots[dir] = nextMap;
      state.fileFlags = nextFlags;
      saveTracking();
    }

    // --- Recent files / folders ---
    function pushRecent(list, item, max) {
      if (!item || !item.path) return list;
      // Remove any existing entry with the same path, then unshift.
      const next = list.filter((x) => x.path !== item.path);
      next.unshift(item);
      if (next.length > max) next.length = max;
      return next;
    }

    function addRecentFile(path, name, kind, modTime) {
      if (!path) return;
      state.recentFiles = pushRecent(state.recentFiles, {
        path,
        name: name || basename(path),
        kind: kind || "",
        openedAt: Date.now(),
        // modTime captured when the user last opened this file — used by the
        // "all recents" popup to flag items that have changed on disk since.
        lastSeenModTime: modTime || "",
      }, RECENT_FILES_MAX);
      saveTracking();
      renderRecents();
    }

    function addRecentDir(path) {
      if (!path) return;
      state.recentDirs = pushRecent(state.recentDirs, {
        path,
        name: basename(path) || path,
        openedAt: Date.now(),
      }, RECENT_DIRS_MAX);
      saveTracking();
      renderRecents();
    }

    // For a recent file item, decide whether it has been modified on disk
    // since the user last opened it. We look up the file's current modTime
    // from whichever dirSnapshot tracks it; if unknown (directory not yet
    // visited this session), report "unknown" instead of guessing.
    function recentFileStatus(item) {
      if (!item || !item.path) return { state: "unknown" };
      const parentDir = item.path.replace(/\/[^/]*$/, "");
      const snapshot = state.dirSnapshots[parentDir];
      if (!snapshot || !(item.path in snapshot)) return { state: "unknown" };
      const currentMod = snapshot[item.path];
      if (!item.lastSeenModTime) return { state: "unknown", currentMod };
      if (currentMod && currentMod !== item.lastSeenModTime) {
        return { state: "updated", currentMod };
      }
      return { state: "unchanged", currentMod };
    }

    function recentDirStatus(item) {
      // For directories: report "updated" if any flagged child currently
      // lives under this dir. Otherwise "unknown" / "unchanged".
      if (!item || !item.path) return { state: "unknown" };
      const prefix = item.path + "/";
      const flagged = Object.keys(state.fileFlags).some((p) => p.startsWith(prefix));
      if (flagged) return { state: "updated" };
      if (state.dirSnapshots[item.path]) return { state: "unchanged" };
      return { state: "unknown" };
    }

    function clearRecents(which) {
      if (which === "files" || which === "all") state.recentFiles = [];
      if (which === "dirs" || which === "all") state.recentDirs = [];
      saveTracking();
      renderRecents();
    }

    function getAlias(path) {
      return (state.aliases && state.aliases[path]) || "";
    }

    async function setAlias(path, alias) {
      if (!path) return;
      const trimmed = (alias || "").trim();
      // Optimistic local update — instant UI feedback.
      if (trimmed) {
        state.aliases[path] = trimmed;
      } else {
        delete state.aliases[path];
      }
      saveTracking();
      renderFavorites();
      if (state.popupKind === "favorites") renderPopup();
      // Persist to the server so the alias is visible from other browsers.
      try {
        const res = await fetch("/api/aliases", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ path, alias: trimmed }),
        });
        if (!res.ok) throw new Error(await res.text());
        const next = await res.json();
        if (next && typeof next === "object") {
          state.aliases = next;
          saveTracking();
          renderFavorites();
          if (state.popupKind === "favorites") renderPopup();
        }
      } catch (err) {
        console.error("setAlias server save failed:", err);
        statusTextEl.textContent = "Alias saved locally only (server write failed)";
      }
    }

    function promptAlias(path) {
      const current = getAlias(path);
      const next = window.prompt("Alias for this favorite (leave blank to remove):", current);
      if (next === null) return; // cancelled
      setAlias(path, next);
    }

    function basename(p) {
      if (!p) return "";
      const trimmed = p.replace(/\/+$/, "");
      const idx = trimmed.lastIndexOf("/");
      return idx >= 0 ? trimmed.slice(idx + 1) : trimmed;
    }

    function relativeTime(ts) {
      if (!ts) return "";
      const diff = Date.now() - ts;
      const s = Math.floor(diff / 1000);
      if (s < 10) return "just now";
      if (s < 60) return s + "s ago";
      const m = Math.floor(s / 60);
      if (m < 60) return m + "m ago";
      const h = Math.floor(m / 60);
      if (h < 24) return h + "h ago";
      const d = Math.floor(h / 24);
      if (d < 7) return d + "d ago";
      const date = new Date(ts);
      return date.toLocaleDateString();
    }

    function clearFileFlag(path, modTime = "") {
      if (!path) return;
      const snapshotMod = (() => {
        // Find this path's modTime from whichever directory snapshot has it.
        for (const dir of Object.keys(state.dirSnapshots)) {
          const m = state.dirSnapshots[dir];
          if (m && (path in m)) return m[path];
        }
        return "";
      })();
      const currentMod = modTime || snapshotMod || state.selectedModTime || "";
      if (currentMod) {
        state.dismissedEntryMap[path] = currentMod;
        state.dismissedAt[path] = Date.now();
      }
      delete state.fileFlags[path];
      saveTracking();
    }

    async function fetchJSON(url, options) {
      const res = await fetch(url, options);
      if (!res.ok) throw new Error(await res.text());
      return res.json();
    }

    function currentRoute() {
      return {
        dir: state.cwd || "",
        path: state.selectedPath || "",
        hash: state.selectedHash || "",
      };
    }

    function routeURL(route) {
      const params = new URLSearchParams();
      if (route.dir) params.set("dir", route.dir);
      if (route.path) params.set("path", route.path);
      if (route.hash) params.set("hash", route.hash);
      const query = params.toString();
      return query ? "?" + query : location.pathname;
    }

    function syncHistory(mode = "push") {
      if (state.restoringHistory) return;
      const route = currentRoute();
      const url = routeURL(route);
      if (mode === "replace") {
        history.replaceState(route, "", url);
      } else {
        history.pushState(route, "", url);
      }
    }

    function routeFromLocation() {
      const params = new URLSearchParams(location.search);
      return {
        dir: params.get("dir") || "",
        path: params.get("path") || "",
        hash: params.get("hash") || "",
      };
    }

    function describeLoadError(err, dir) {
      const msg = (err && err.message) ? err.message : String(err);
      const lower = msg.toLowerCase();
      const target = dir || "this folder";
      if (lower.includes("permission denied") || lower.includes("operation not permitted") || lower.includes("eacces") || lower.includes("eperm")) {
        return "Permission denied for " + target + ". On macOS, grant access in System Settings → Privacy & Security → Files and Folders (or Full Disk Access) to the mdviewer binary, or to your Terminal if you launched it from there. Then restart mdviewer.";
      }
      if (lower.includes("no such file") || lower.includes("enoent")) {
        return "Folder not found: " + target;
      }
      return "Failed to load " + target + ": " + msg;
    }

    async function loadDir(dir = "", options = {}) {
      const params = new URLSearchParams();
      if (dir) params.set("dir", dir);
      if (state.showHidden) params.set("show_hidden", "1");
      const query = params.toString() ? "?" + params.toString() : "";
      let data;
      try {
        data = await fetchJSON("/api/list" + query);
      } catch (err) {
        const friendly = describeLoadError(err, dir);
        statusTextEl.textContent = friendly;
        console.error("loadDir failed:", err);
        return;
      }
      state.cwd = data.cwd;
      updateChangedPaths(data.cwd, data.entries, { silent: !!options.silent });
      state.entries = data.entries;
      state.favorites = Array.isArray(data.favorites) ? data.favorites : [];
      // Server is the source of truth for aliases — merge into state so
      // they are visible across browsers / devices. (We keep the localStorage
      // copy as a fallback when the server reply is missing the field, e.g.
      // very old binaries.)
      if (data && data.aliases && typeof data.aliases === "object") {
        state.aliases = data.aliases;
        try { saveTracking(); } catch (e) {}
      }
      if (options.clearSelection !== false && !options.keepSelection) {
        state.selectedPath = "";
        state.selectedHash = "";
        state.selectedKind = "";
        state.selectedContent = "";
        updateCopyPathButton("");
        // Show the welcome / usage guide whenever no file is selected.
        showUsageGuide();
      }
      cwdEl.textContent = shortenDisplayPath(state.cwd);
      cwdEl.dataset.path = state.cwd;
      renderFiles(data.entries);
      renderFavorites();
      updateToggleFavoriteLabel();
      // Record visited directory in MRU (skip silent background loads to
      // avoid polluting recents with auto-refresh activity).
      if (!options.silent && !options.keepSelection) {
        addRecentDir(state.cwd);
      } else {
        renderRecents();
      }
      if (!options.silent) {
        statusTextEl.textContent = "Loaded " + data.cwd;
      }
      if (options.historyMode) {
        syncHistory(options.historyMode);
      }
    }

    function updateToggleFavoriteLabel() {
      const el = document.getElementById("toggleFavorite");
      if (!el) return;
      const list = Array.isArray(state.favorites) ? state.favorites : [];
      const isFav = !!state.cwd && list.indexOf(state.cwd) !== -1;
      el.textContent = isFav ? "★ Remove favorite" : "Add to favorites";
      el.classList.toggle("active", isFav);
      el.title = isFav
        ? "Remove " + state.cwd + " from favorites"
        : "Add " + (state.cwd || "current folder") + " to favorites";
    }

    function renderFiles(entries) {
      filesEl.innerHTML = "";
      const query = state.searchQuery.trim().toLowerCase();
      const filteredEntries = query
        ? entries.filter((entry) => entry.name.toLowerCase().includes(query))
        : [...entries];
      filteredEntries.sort(compareEntries);

      if (!filteredEntries.length) {
        filesEl.innerHTML = '<div class="subtle" style="padding: 4px 12px;">No matching files</div>';
        return;
      }
      // Precompute which directories contain any flagged child path.
      // P4: surfaces "something inside here changed" without forcing the user
      // to descend into every subfolder. Only works for dirs we've already
      // visited in some session (since unvisited dirs have no children tracked).
      const flagDirPriority = { recent: 1, updated: 2, new: 3 };
      const dirAggregateFlag = {};
      for (const [p, f] of Object.entries(state.fileFlags)) {
        if (!f) continue;
        for (const entry of filteredEntries) {
          if (!entry.is_dir) continue;
          if (p.startsWith(entry.path + "/")) {
            const existing = dirAggregateFlag[entry.path];
            if (!existing || (flagDirPriority[f] || 0) > (flagDirPriority[existing] || 0)) {
              dirAggregateFlag[entry.path] = f;
            }
          }
        }
      }

      for (const entry of filteredEntries) {
        const button = document.createElement("button");
        const directFlag = state.fileFlags[entry.path] || "";
        const aggFlag = entry.is_dir ? (dirAggregateFlag[entry.path] || "") : "";
        const flag = directFlag || aggFlag;
        button.className = "file"
          + (entry.path === state.selectedPath ? " active" : "")
          + (flag ? " has-flag flag-" + flag : "")
          + (aggFlag && !directFlag ? " flag-aggregate" : "");
        button.dataset.meta = describeEntryMeta(entry);
        if (flag) {
          button.dataset.flag = flag;
        }
        button.innerHTML = '<span class="file-name"></span><span class="file-meta"><span class="update-badge"></span><span class="file-size"></span></span>';
        button.querySelector(".file-name").innerHTML = highlightName(entry.name, state.searchQuery.trim());
        button.querySelector(".file-size").textContent = entry.is_dir ? "" : humanSize(entry.size);
        button.onclick = () => entry.is_dir
          ? loadDir(entry.path, { historyMode: "push" })
          : selectFile(entry.path, { historyMode: "push" });
        filesEl.appendChild(button);
      }
    }

    function renderFavorites() {
      favoritesEl.innerHTML = "";
      if (!state.favorites.length) {
        favoritesEl.innerHTML = '<div class="subtle" style="padding: 0 8px;">No favorites</div>';
        toggleShowAll("showAllFavorites", 0);
        return;
      }
      const shown = state.favorites.slice(0, SIDEBAR_RECENT_LIMIT);
      for (const favorite of shown) {
        favoritesEl.appendChild(buildFavoriteRow(favorite));
      }
      toggleShowAll("showAllFavorites", state.favorites.length);
    }

    function buildFavoriteRow(favorite) {
      const alias = getAlias(favorite);
      const row = document.createElement("div");
      row.className = "favorite-row" + (favorite === state.cwd ? " active" : "");
      row.dataset.path = favorite;

      const main = document.createElement("button");
      main.type = "button";
      main.className = "favorite-main";
      main.onclick = () => loadDir(favorite, { historyMode: "push" });
      if (alias) {
        const nameEl = document.createElement("span");
        nameEl.className = "favorite-alias";
        nameEl.textContent = alias;
        const pathEl = document.createElement("span");
        pathEl.className = "favorite-sub";
        pathEl.textContent = shortenFavoritePath(favorite);
        pathEl.title = favorite;
        main.appendChild(nameEl);
        main.appendChild(pathEl);
      } else {
        const nameEl = document.createElement("span");
        nameEl.className = "favorite-alias";
        nameEl.textContent = shortenFavoritePath(favorite);
        nameEl.title = favorite;
        main.appendChild(nameEl);
      }
      row.appendChild(main);

      // Edit alias (✎) button — visible on hover.
      const editBtn = document.createElement("button");
      editBtn.type = "button";
      editBtn.className = "favorite-edit";
      editBtn.title = alias ? "Edit alias" : "Set alias";
      editBtn.textContent = "✎";
      editBtn.onclick = (event) => {
        event.stopPropagation();
        promptAlias(favorite);
      };
      row.appendChild(editBtn);

      // Double-click on the row also opens the alias prompt — easy to discover.
      main.addEventListener("dblclick", (event) => {
        event.preventDefault();
        event.stopPropagation();
        promptAlias(favorite);
      });

      return row;
    }

    function renderRecentList(containerEl, items, onClick, opts = {}) {
      containerEl.innerHTML = "";
      if (!items.length) {
        const empty = document.createElement("div");
        empty.className = "subtle";
        empty.style.padding = "0 8px";
        empty.textContent = opts.emptyText || "Nothing here yet";
        containerEl.appendChild(empty);
        return;
      }
      for (const item of items) {
        const button = document.createElement("button");
        button.className = "recent" + (opts.activeWhen && opts.activeWhen(item) ? " active" : "");
        button.dataset.path = item.path;
        const nameEl = document.createElement("span");
        nameEl.className = "recent-name";
        nameEl.textContent = item.name || basename(item.path) || item.path;
        const timeEl = document.createElement("span");
        timeEl.className = "recent-time";
        timeEl.textContent = relativeTime(item.openedAt);
        timeEl.title = item.openedAt ? new Date(item.openedAt).toLocaleString() : "";
        const pathEl = document.createElement("span");
        pathEl.className = "recent-path";
        pathEl.textContent = shortenDisplayPath(item.path);
        pathEl.title = item.path;
        button.appendChild(nameEl);
        button.appendChild(timeEl);
        button.appendChild(pathEl);
        button.onclick = () => onClick(item);
        containerEl.appendChild(button);
      }
    }

    const SIDEBAR_RECENT_LIMIT = 3;

    function renderRecents() {
      if (recentFilesEl) {
        const shown = state.recentFiles.slice(0, SIDEBAR_RECENT_LIMIT);
        renderRecentList(recentFilesEl, shown, (item) => {
          selectFile(item.path, { historyMode: "push" });
        }, {
          activeWhen: (item) => item.path === state.selectedPath,
          emptyText: "No recent files",
        });
        toggleShowAll("showAllRecentFiles", state.recentFiles.length);
      }
      if (recentDirsEl) {
        const shown = state.recentDirs.slice(0, SIDEBAR_RECENT_LIMIT);
        renderRecentList(recentDirsEl, shown, (item) => {
          loadDir(item.path, { historyMode: "push" });
        }, {
          activeWhen: (item) => item.path === state.cwd,
          emptyText: "No recent folders",
        });
        toggleShowAll("showAllRecentDirs", state.recentDirs.length);
      }
    }

    function toggleShowAll(buttonId, totalCount) {
      const btn = document.getElementById(buttonId);
      if (!btn) return;
      if (totalCount > SIDEBAR_RECENT_LIMIT) {
        btn.hidden = false;
        btn.textContent = "Show all (" + totalCount + ")";
      } else {
        btn.hidden = true;
      }
    }

    const collapseSearchPanelEl = document.getElementById("collapseSearchPanel");
    const revealSearchPanelEl = document.getElementById("revealSearchPanel");
    const searchPanelInputEl = document.getElementById("searchPanelInput");
    const searchInFileSummaryEl = document.getElementById("searchInFileSummary");
    const searchInFileHitsEl = document.getElementById("searchInFileHits");
    const searchFolderHitsEl = document.getElementById("searchFolderHits");

    // clearInFileHighlights removes any <mark.search-mark> wrappers from
    // previewBodyEl, restoring the text nodes.
    function clearInFileHighlights() {
      const marks = previewBodyEl.querySelectorAll("mark.search-mark");
      for (const m of marks) {
        const parent = m.parentNode;
        if (!parent) continue;
        parent.replaceChild(document.createTextNode(m.textContent || ""), m);
        parent.normalize();
      }
      state.searchInFileHits = [];
      state.searchInFileFocus = -1;
    }

    // walkTextNodes yields every text node descendant of root that has
    // non-empty content and isn't inside a SCRIPT/STYLE/MARK element.
    function walkTextNodes(root, visit) {
      const SKIP = { SCRIPT: 1, STYLE: 1, MARK: 1 };
      const stack = [root];
      while (stack.length) {
        const node = stack.pop();
        for (const child of Array.from(node.childNodes)) {
          if (child.nodeType === 1) {
            if (!SKIP[child.tagName]) stack.push(child);
          } else if (child.nodeType === 3) {
            if (child.nodeValue && child.nodeValue.length) visit(child);
          }
        }
      }
    }

    // highlightInFile wraps each occurrence of needle in previewBodyEl with
    // <mark class="search-mark">. Case-insensitive. Returns the array of
    // mark elements in document order.
    function highlightInFile(needle) {
      clearInFileHighlights();
      if (!needle) return [];
      const lower = needle.toLowerCase();
      const len = needle.length;
      const hits = [];
      const targets = [];
      walkTextNodes(previewBodyEl, function (t) { targets.push(t); });
      for (const node of targets) {
        const text = node.nodeValue || "";
        const lo = text.toLowerCase();
        let idx = lo.indexOf(lower);
        if (idx < 0) continue;
        const parent = node.parentNode;
        if (!parent) continue;
        let cursor = 0;
        const frag = document.createDocumentFragment();
        while (idx >= 0) {
          if (idx > cursor) {
            frag.appendChild(document.createTextNode(text.substring(cursor, idx)));
          }
          const mark = document.createElement("mark");
          mark.className = "search-mark";
          mark.textContent = text.substring(idx, idx + len);
          frag.appendChild(mark);
          hits.push(mark);
          cursor = idx + len;
          idx = lo.indexOf(lower, cursor);
        }
        if (cursor < text.length) {
          frag.appendChild(document.createTextNode(text.substring(cursor)));
        }
        parent.replaceChild(frag, node);
      }
      state.searchInFileHits = hits;
      state.searchInFileFocus = -1;
      return hits;
    }

    // focusHit scrolls to the i-th in-file hit and emphasises it.
    function focusHit(i) {
      const hits = state.searchInFileHits;
      if (!hits.length) return;
      if (state.searchInFileFocus >= 0 && hits[state.searchInFileFocus]) {
        hits[state.searchInFileFocus].classList.remove("current");
      }
      const clamped = Math.max(0, Math.min(i, hits.length - 1));
      state.searchInFileFocus = clamped;
      const mark = hits[clamped];
      mark.classList.add("current");
      mark.scrollIntoView({ behavior: "smooth", block: "center" });
    }

    // renderInFileResults updates the summary + clickable hit list in the
    // right panel.
    function renderInFileResults(needle, hits) {
      searchInFileHitsEl.innerHTML = "";
      if (!needle) {
        searchInFileSummaryEl.textContent = "Type to search.";
        return;
      }
      if (!hits.length) {
        searchInFileSummaryEl.textContent = "No matches in this file.";
        return;
      }
      searchInFileSummaryEl.textContent = hits.length + " match" + (hits.length === 1 ? "" : "es");
      const maxList = 50;
      const shown = hits.slice(0, maxList);
      for (let i = 0; i < shown.length; i++) {
        const mark = shown[i];
        const ctxBefore = (mark.previousSibling && mark.previousSibling.nodeValue) || "";
        const ctxAfter  = (mark.nextSibling && mark.nextSibling.nodeValue) || "";
        const row = document.createElement("div");
        row.className = "search-hit";
        const pre = document.createElement("span");
        pre.textContent = "..." + ctxBefore.slice(-30);
        const hit = document.createElement("span");
        hit.className = "search-hit-needle";
        hit.textContent = mark.textContent;
        const post = document.createElement("span");
        post.textContent = ctxAfter.slice(0, 30) + "...";
        row.appendChild(pre);
        row.appendChild(hit);
        row.appendChild(post);
        row.addEventListener("click", function () { focusHit(i); });
        searchInFileHitsEl.appendChild(row);
      }
      if (hits.length > maxList) {
        const more = document.createElement("div");
        more.className = "search-empty";
        more.textContent = (hits.length - maxList) + " more matches not shown.";
        searchInFileHitsEl.appendChild(more);
      }
    }

    // runInFileSearch ties the two together.
    function runInFileSearch(needle) {
      const hits = highlightInFile(needle);
      renderInFileResults(needle, hits);
    }

    let searchFolderAbort = null;
    async function runFolderSearch(needle) {
      searchFolderHitsEl.innerHTML = "";
      if (!needle) return;
      if (searchFolderAbort) { try { searchFolderAbort.abort(); } catch (e) {} }
      const ctrl = new AbortController();
      searchFolderAbort = ctrl;
      let results = [];
      try {
        const url = "/api/search?dir=" + encodeURIComponent(state.cwd || "") +
                    "&q=" + encodeURIComponent(needle);
        const r = await fetch(url, { signal: ctrl.signal });
        if (!r.ok) throw new Error(String(r.status));
        results = await r.json();
      } catch (err) {
        if (err && err.name === "AbortError") return;
        const e = document.createElement("div");
        e.className = "search-empty";
        e.textContent = "Search failed.";
        searchFolderHitsEl.appendChild(e);
        return;
      }
      // Hide the currently-open file from the cross-file list — its
      // matches are already in the "In this file" section.
      const filtered = results.filter(function (r) {
        return r.path !== state.selectedPath;
      });
      if (!filtered.length) {
        const e = document.createElement("div");
        e.className = "search-empty";
        e.textContent = "No matches in other files.";
        searchFolderHitsEl.appendChild(e);
        return;
      }
      for (const r of filtered) {
        const row = document.createElement("div");
        row.className = "search-file-row";
        row.title = r.path;
        const name = document.createElement("span");
        name.textContent = r.path.split("/").pop();
        const count = document.createElement("span");
        count.className = "search-file-count";
        count.textContent = r.count + (r.count === 1 ? " match" : " matches");
        row.appendChild(name);
        row.appendChild(count);
        row.addEventListener("click", function () {
          selectFile(r.path, { historyMode: "push" });
        });
        searchFolderHitsEl.appendChild(row);
      }
    }

    async function selectFile(path, options = {}) {
      state.selectedPath = path;
      state.selectedHash = options.hash || "";
      if (!state.cwd || !path.startsWith(state.cwd + "/")) {
        await loadDir(path.replace(/\/[^/]*$/, ""), { keepSelection: true });
      }
      renderFiles(state.entries);
      let data;
      try {
        data = await fetchJSON("/api/file?path=" + encodeURIComponent(path));
      } catch (err) {
        const friendly = describeLoadError(err, path);
        statusTextEl.textContent = friendly;
        console.error("selectFile failed:", err);
        return;
      }
      state.selectedKind = data.kind;
      state.selectedModTime = data.mod_time;
      state.selectedContent = data.content || "";
      state.editBaseModTime = data.mod_time;
      state.editDraft = data.content || "";
      state.editDirty = false;
      if (!canEditKind(data.kind)) {
        state.editorMode = "preview";
      }
      clearFileFlag(path, data.mod_time);
      addRecentFile(path, data.name, data.kind, data.mod_time);
      renderFiles(state.entries);
      previewTitleEl.textContent = data.name;
      previewMetaEl.textContent = new Date(data.mod_time).toLocaleString() + " · " + humanSize(data.size);
      kindChipEl.textContent = data.kind;
      kindChipEl.setAttribute("data-kind", data.kind || "idle");
      await renderCurrentView(data);
      if (state.selectedHash) {
        requestAnimationFrame(() => scrollToHash(state.selectedHash));
      } else {
        previewBodyEl.scrollTop = 0;
      }
      statusTextEl.textContent = "Showing " + data.name;
      updateCopyPathButton(data.path || path);
      updateEditorButtons();
      if (options.historyMode) {
        syncHistory(options.historyMode);
      }
      // re-run in-file search on the newly rendered preview, if a query
      // is active.
      if (state.searchQueryRight) {
        runInFileSearch(state.searchQueryRight);
      }
    }

    let usageGuideCache = null;
    async function showUsageGuide() {
      // Render the embedded USAGE_WEB.md as a friendly welcome / help screen
      // whenever the user has no file selected.
      try {
        if (!usageGuideCache) {
          const res = await fetch("/api/usage");
          if (!res.ok) throw new Error(await res.text());
          usageGuideCache = await res.text();
        }
        const rendered = marked.parse(usageGuideCache);
        previewBodyEl.innerHTML =
          '<div class="usage-guide">' +
          '<div class="usage-guide-banner">' +
          '<span class="usage-guide-icon">📘</span>' +
          '<div class="usage-guide-text">' +
          '<div class="usage-guide-title">Built-in usage guide</div>' +
          '<div class="usage-guide-subtitle">Select a file from the left to open your content.</div>' +
          '</div>' +
          '</div>' +
          '<div class="usage-guide-body">' + rendered + '</div>' +
          '</div>';
        // Run mermaid (in case the guide ever uses it) and decorate links.
        try { await mermaid.run({ nodes: previewBodyEl.querySelectorAll(".mermaid") }); } catch (e) {}
        decorateRenderedMarkdown();
        previewTitleEl.textContent = "Markdown Browser";
        previewMetaEl.textContent = "Usage guide";
        kindChipEl.textContent = "Help";
        kindChipEl.setAttribute("data-kind", "help");
      } catch (err) {
        console.error("usage load failed:", err);
        previewBodyEl.innerHTML = '<div class="empty">Choose a Markdown, text, Mermaid, or image file from the left.</div>';
      }
    }

    async function renderCurrentView(data = null) {
      const activeData = data || {
        kind: state.selectedKind,
        content: state.selectedContent,
        raw_url: state.selectedPath ? "/api/raw?path=" + encodeURIComponent(state.selectedPath) : "",
      };

      if (state.editorMode === "edit" && canEditKind(activeData.kind)) {
        previewBodyEl.innerHTML = '<div class="editor-wrap"><textarea class="editor" spellcheck="false"></textarea></div>';
        const editorEl = previewBodyEl.querySelector(".editor");
        editorEl.value = state.editDraft;
        editorEl.focus({ preventScroll: true });
        editorEl.setSelectionRange(editorEl.value.length, editorEl.value.length);
        editorEl.addEventListener("input", (event) => {
          state.editDraft = event.target.value;
          state.editDirty = state.editDraft !== state.selectedContent;
          updateEditorButtons();
          statusTextEl.textContent = state.editDirty ? "Unsaved changes" : "Editing";
        });
        return;
      }

      await renderPreview(activeData);
    }

    async function renderPreview(data) {
      if (data.kind === "markdown") {
        previewBodyEl.innerHTML = marked.parse(data.content);
        const blocks = previewBodyEl.querySelectorAll("pre code.language-mermaid");
        for (const code of blocks) {
          // Wrap each mermaid host in a .mermaid-wrap container. mermaid 11.x
          // rewrites the .mermaid node's children when rendering, which
          // would erase any toolbar appended inside. Putting the toolbar on
          // the wrapper keeps it safe.
          const wrap = document.createElement("div");
          wrap.className = "mermaid-wrap";
          const host = document.createElement("div");
          host.className = "mermaid";
          host.textContent = code.textContent;
          wrap.appendChild(host);
          code.parentElement.replaceWith(wrap);
        }
        await mermaid.run({ nodes: previewBodyEl.querySelectorAll(".mermaid") });
        decorateRenderedMarkdown();
        // Explicitly attach zoom/toolbar after mermaid finishes — the
        // MutationObserver path is unreliable for the toolbar because it
        // can fire before the <svg> is in place.
        attachZoomToPreview();
        // A second pass on the next frame catches any diagrams that mermaid
        // wired up asynchronously (some diagram types do this).
        requestAnimationFrame(() => attachZoomToPreview());
        return;
      }
      if (data.kind === "image") {
        previewBodyEl.innerHTML = '<img alt="" src="' + data.raw_url + '&t=' + Date.now() + '" />';
        return;
      }
      if (data.kind === "html") {
        // Render HTML files in a sandboxed iframe. allow-scripts only —
        // no same-origin, so inline JS (vis-network, d3, etc.) works but
        // cannot reach the host page's storage or DOM.
        previewBodyEl.innerHTML = "";
        const wrap = document.createElement("div");
        wrap.className = "html-frame-wrap";
        const frame = document.createElement("iframe");
        frame.className = "html-frame";
        frame.setAttribute("sandbox", "allow-scripts allow-popups allow-popups-to-escape-sandbox allow-forms");
        frame.setAttribute("loading", "lazy");
        frame.setAttribute("referrerpolicy", "no-referrer");
        frame.title = data.name || "HTML preview";
        frame.src = data.raw_url + "&t=" + Date.now();
        wrap.appendChild(frame);
        // Small footer chip indicating sandboxed mode + "open in new tab" escape hatch.
        const note = document.createElement("div");
        note.className = "html-frame-note";
        note.innerHTML = '<span>Sandboxed preview</span> · <a href="' + data.raw_url + '" target="_blank" rel="noopener">Open in new tab ↗</a>';
        wrap.appendChild(note);
        previewBodyEl.appendChild(wrap);
        return;
      }
      if (data.kind === "text") {
        previewBodyEl.innerHTML = "";
        const pre = document.createElement("pre");
        pre.textContent = data.content;
        previewBodyEl.appendChild(pre);
        return;
      }
      previewBodyEl.innerHTML = '<div class="empty">Binary or unsupported file type.</div>';
    }

    async function saveCurrentFile(force = false) {
      if (!state.selectedPath || !canEditKind(state.selectedKind)) return;
      if (!state.editDirty && !force) {
        statusTextEl.textContent = "No changes to save";
        return;
      }

      const res = await fetch("/api/file/save", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          path: state.selectedPath,
          content: state.editDraft,
          base_mod_time: state.editBaseModTime,
          force,
        }),
      });

      if (res.status === 409) {
        const conflict = await res.json();
        const overwrite = window.confirm("File changed on disk. Overwrite with your edits?");
        if (overwrite) {
          return saveCurrentFile(true);
        }
        statusTextEl.textContent = "Save cancelled due to external changes";
        return;
      }

      if (!res.ok) {
        statusTextEl.textContent = "Save failed";
        return;
      }

      const data = await res.json();
      state.selectedModTime = data.mod_time;
      state.editBaseModTime = data.mod_time;
      state.selectedContent = data.content || state.editDraft;
      state.editDraft = state.selectedContent;
      state.editDirty = false;
      previewMetaEl.textContent = new Date(data.mod_time).toLocaleString() + " · " + humanSize(data.size);
      clearFileFlag(state.selectedPath, data.mod_time);
      await loadDir(state.cwd, { keepSelection: true, silent: true });
      renderFiles(state.entries);
      updateEditorButtons();
      statusTextEl.textContent = "Saved " + data.name;
      if (state.editorMode === "preview") {
        await renderCurrentView(data);
      }
    }

    async function refreshSelected() {
      if (!state.selectedPath) return;
      const data = await fetchJSON("/api/file?path=" + encodeURIComponent(state.selectedPath));
      if (data.mod_time !== state.selectedModTime) {
        if (state.editorMode === "edit" && state.editDirty) {
          statusTextEl.textContent = "File changed on disk while editing";
          return;
        }
        await selectFile(state.selectedPath);
      }
    }

    async function refreshCurrentDir() {
      if (!state.cwd) return;
      await loadDir(state.cwd, { keepSelection: true, silent: true });
    }

    function updateCopyPathButton(path) {
      if (path) {
        copyPathBtnEl.disabled = false;
        copyPathBtnEl.dataset.path = path;
        copyPathBtnEl.title = "Click to copy full path:\n" + path;
        copyPathLabelEl.textContent = path;
      } else {
        copyPathBtnEl.disabled = true;
        copyPathBtnEl.dataset.path = "";
        copyPathBtnEl.title = "No file selected";
        copyPathLabelEl.textContent = "No file selected";
      }
      copyPathBtnEl.classList.remove("copied");
      copyPathIconEl.textContent = "⧉";
    }

    async function copyTextToClipboard(text) {
      if (!text) return false;
      try {
        if (navigator.clipboard && window.isSecureContext) {
          await navigator.clipboard.writeText(text);
          return true;
        }
      } catch (e) {
        // fall through to legacy path
      }
      // Legacy fallback for older browsers / non-secure contexts
      const ta = document.createElement("textarea");
      ta.value = text;
      ta.style.position = "fixed";
      ta.style.opacity = "0";
      ta.style.pointerEvents = "none";
      document.body.appendChild(ta);
      ta.select();
      let ok = false;
      try { ok = document.execCommand("copy"); } catch (e) { ok = false; }
      document.body.removeChild(ta);
      return ok;
    }

    async function copyCurrentPath() {
      const path = copyPathBtnEl.dataset.path;
      if (!path) return;
      const ok = await copyTextToClipboard(path);
      if (ok) {
        copyPathBtnEl.classList.add("copied");
        copyPathIconEl.textContent = "✓";
        statusTextEl.textContent = "Copied path to clipboard";
        setTimeout(() => {
          copyPathBtnEl.classList.remove("copied");
          copyPathIconEl.textContent = "⧉";
        }, 1200);
      } else {
        statusTextEl.textContent = "Could not copy path (clipboard blocked)";
      }
    }

    async function jumpToPath(rawPath) {
      const value = (rawPath || "").trim();
      if (!value) return;
      try {
        const data = await fetchJSON(
          "/api/resolve?path=" + encodeURIComponent(value) +
          "&base=" + encodeURIComponent(state.cwd || "")
        );
        if (!data || !data.path) {
          statusTextEl.textContent = "Could not resolve path: " + value;
          return;
        }
        if (!data.exists) {
          statusTextEl.textContent = "Path not found: " + data.path;
          return;
        }
        if (data.is_dir) {
          await loadDir(data.path, { historyMode: "push" });
          statusTextEl.textContent = "Jumped to " + data.path;
        } else {
          await selectFile(data.path, { historyMode: "push" });
        }
        pathInputEl.value = "";
        pathInputEl.blur();
      } catch (err) {
        statusTextEl.textContent = "Jump failed: " + (err && err.message ? err.message : err);
      }
    }

    async function toggleFavorite() {
      if (!state.cwd) {
        statusTextEl.textContent = "No folder loaded";
        return;
      }
      try {
        const result = await fetchJSON("/api/favorites/toggle", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ dir: state.cwd }),
        });
        // Apply the toggle locally without a full directory reload —
        // keeps the current selection, scroll position, and editor state.
        state.favorites = Array.isArray(result && result.favorites) ? result.favorites : [];
        renderFavorites();
        updateToggleFavoriteLabel();
        const wasFavorited = !!(result && result.favorited);
        statusTextEl.textContent = wasFavorited
          ? "Added " + state.cwd + " to favorites"
          : "Removed " + state.cwd + " from favorites";
      } catch (err) {
        statusTextEl.textContent = "Favorite toggle failed: " + (err && err.message ? err.message : err);
      }
    }

    async function restoreRoute(route, historyMode = "") {
      state.restoringHistory = true;
      try {
        const dir = route.dir || state.cwd || "";
        if (dir) {
          await loadDir(dir, { historyMode, clearSelection: !route.path });
        } else {
          await loadDir("", { historyMode, clearSelection: !route.path });
        }
        if (route.path) {
          await selectFile(route.path, { hash: route.hash || "", historyMode });
        } else if (route.hash) {
          state.selectedHash = route.hash;
          requestAnimationFrame(() => scrollToHash(route.hash));
        }
      } finally {
        state.restoringHistory = false;
      }
    }

    previewBodyEl.addEventListener("scroll", () => {
      const maxScroll = Math.max(1, previewBodyEl.scrollHeight - previewBodyEl.clientHeight);
      const percent = Math.min(100, Math.max(0, Math.round(previewBodyEl.scrollTop / maxScroll * 100)));
      scrollTextEl.textContent = "Preview " + percent + "%";
    });

    filesEl.addEventListener("pointerover", (event) => {
      const row = event.target.closest(".file[data-meta]");
      if (!row) return;
      showTooltip(row.dataset.meta, event.clientX, event.clientY);
    });

    filesEl.addEventListener("pointermove", (event) => {
      const row = event.target.closest(".file[data-meta]");
      if (!row || !floatingTooltipEl.classList.contains("visible")) return;
      showTooltip(row.dataset.meta, event.clientX, event.clientY);
    });

    filesEl.addEventListener("pointerout", (event) => {
      const next = event.relatedTarget;
      if (next && event.currentTarget.contains(next)) return;
      hideTooltip();
    });

    cwdEl.addEventListener("pointerover", (event) => {
      if (!cwdEl.dataset.path) return;
      showTooltip(cwdEl.dataset.path, event.clientX, event.clientY, { singleLine: true });
    });

    cwdEl.addEventListener("pointermove", (event) => {
      if (!cwdEl.dataset.path || !floatingTooltipEl.classList.contains("visible")) return;
      showTooltip(cwdEl.dataset.path, event.clientX, event.clientY, { singleLine: true });
    });

    cwdEl.addEventListener("pointerout", () => {
      hideTooltip();
    });

    favoritesEl.addEventListener("pointerover", (event) => {
      const row = event.target.closest(".favorite-row[data-path]");
      if (!row) return;
      showTooltip(row.dataset.path, event.clientX, event.clientY, { singleLine: true });
    });

    favoritesEl.addEventListener("pointermove", (event) => {
      const row = event.target.closest(".favorite-row[data-path]");
      if (!row || !floatingTooltipEl.classList.contains("visible")) return;
      showTooltip(row.dataset.path, event.clientX, event.clientY, { singleLine: true });
    });

    favoritesEl.addEventListener("pointerout", (event) => {
      const next = event.relatedTarget;
      if (next && event.currentTarget.contains(next)) return;
      hideTooltip();
    });

    splitterEl.addEventListener("pointerdown", (event) => {
      if (window.innerWidth <= 960 || state.sidebarCollapsed) return;
      splitterEl.classList.add("dragging");
      splitterEl.setPointerCapture(event.pointerId);

      const onMove = (moveEvent) => {
        state.sidebarWidth = moveEvent.clientX - 18;
        applySidebarLayout();
      };

      const onUp = () => {
        splitterEl.classList.remove("dragging");
        splitterEl.removeEventListener("pointermove", onMove);
        splitterEl.removeEventListener("pointerup", onUp);
        splitterEl.removeEventListener("pointercancel", onUp);
      };

      splitterEl.addEventListener("pointermove", onMove);
      splitterEl.addEventListener("pointerup", onUp);
      splitterEl.addEventListener("pointercancel", onUp);
    });

    rightSplitterEl.addEventListener("pointerdown", (event) => {
      if (window.innerWidth <= 960 || state.searchPanelCollapsed) return;
      rightSplitterEl.classList.add("dragging");
      rightSplitterEl.setPointerCapture(event.pointerId);

      const onMove = (moveEvent) => {
        // The panel is on the right side, so dragging RIGHT shrinks it.
        // width = distance from the right edge of the viewport to the cursor,
        // minus the outer 18px padding.
        state.searchPanelWidth = window.innerWidth - moveEvent.clientX - 18;
        applySearchPanelLayout();
      };

      const onUp = () => {
        rightSplitterEl.classList.remove("dragging");
        rightSplitterEl.removeEventListener("pointermove", onMove);
        rightSplitterEl.removeEventListener("pointerup", onUp);
        rightSplitterEl.removeEventListener("pointercancel", onUp);
      };

      rightSplitterEl.addEventListener("pointermove", onMove);
      rightSplitterEl.addEventListener("pointerup", onUp);
      rightSplitterEl.addEventListener("pointercancel", onUp);
    });

    previewBodyEl.addEventListener("click", async (event) => {
      const link = event.target.closest("a[data-internal-href]");
      if (!link) return;
      event.preventDefault();
      const target = link.dataset.internalHref;
      if (!target) return;

      const resolved = resolveLocalTarget(target);
      if (resolved.path === state.selectedPath && resolved.hash) {
        state.selectedHash = resolved.hash;
        scrollToHash(resolved.hash);
        syncHistory("push");
        return;
      }

      try {
        await selectFile(resolved.path, { hash: resolved.hash, historyMode: "push" });
      } catch (error) {
        try {
          await loadDir(resolved.path, { historyMode: "push" });
          statusTextEl.textContent = "Opened directory " + resolved.path;
        } catch (dirError) {
          statusTextEl.textContent = "Could not open link target";
        }
      }
    });

    window.addEventListener("popstate", async (event) => {
      const route = event.state || routeFromLocation();
      await restoreRoute(route);
    });

    collapseSidebarEl.onclick = () => setSidebarCollapsed(!state.sidebarCollapsed);
    revealSidebarEl.onclick = () => setSidebarCollapsed(false);
    collapseSearchPanelEl.onclick = function () {
      state.searchPanelCollapsed = true;
      applySearchPanelCollapsed();
    };
    revealSearchPanelEl.onclick = function () {
      state.searchPanelCollapsed = false;
      applySearchPanelCollapsed();
    };
    previewModeButtonEl.onclick = () => setEditorMode("preview");
    editModeButtonEl.onclick = () => {
      if (!canEditKind(state.selectedKind)) return;
      setEditorMode("edit");
    };
    saveButtonEl.onclick = () => saveCurrentFile();
    let searchDebounce = null;
    searchPanelInputEl.addEventListener("input", function () {
      state.searchQueryRight = searchPanelInputEl.value || "";
      clearTimeout(searchDebounce);
      searchDebounce = setTimeout(function () {
        runInFileSearch(state.searchQueryRight);
        runFolderSearch(state.searchQueryRight);
      }, 120);
    });
    searchInputEl.addEventListener("input", (event) => {
      state.searchQuery = event.target.value || "";
      renderFiles(state.entries);
    });
    sortNameEl.addEventListener("click", () => {
      if (state.sortKey === "name") {
        state.sortDirection = state.sortDirection === "asc" ? "desc" : "asc";
      } else {
        state.sortKey = "name";
        state.sortDirection = "asc";
      }
      updateSortButtons();
      renderFiles(state.entries);
    });
    sortSizeEl.addEventListener("click", () => {
      if (state.sortKey === "size") {
        state.sortDirection = state.sortDirection === "asc" ? "desc" : "asc";
      } else {
        state.sortKey = "size";
        state.sortDirection = "desc";
      }
      updateSortButtons();
      renderFiles(state.entries);
    });

    // ---------- Theme toggle (Auto → Light → Dark) ----------
    const themeToggleEl = document.getElementById("themeToggle");
    const THEME_ORDER = ["auto", "light", "dark"];
    const THEME_LABEL = { auto: "Auto", light: "Light", dark: "Dark" };
    const THEME_ICON = {
      auto:  '<svg class="ico" viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M12 3v18"/><path d="M12 3a9 9 0 0 1 0 18Z" fill="currentColor" stroke="none"/></svg>',
      light: '<svg class="ico" viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.41-1.41M17.66 6.34l1.41-1.41"/></svg>',
      dark:  '<svg class="ico" viewBox="0 0 24 24" aria-hidden="true"><path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8Z"/></svg>',
    };
    function currentTheme() {
      try { return localStorage.getItem("mdviewer.theme") || "auto"; }
      catch (e) { return "auto"; }
    }
    function applyTheme(theme) {
      try { localStorage.setItem("mdviewer.theme", theme); } catch (e) {}
      if (theme === "auto") {
        document.documentElement.removeAttribute("data-theme");
      } else {
        document.documentElement.setAttribute("data-theme", theme);
      }
      if (themeToggleEl) {
        themeToggleEl.innerHTML = THEME_ICON[theme] || THEME_ICON.auto;
        const label = THEME_LABEL[theme] || "Auto";
        themeToggleEl.title = "Theme: " + label + " (click to cycle)";
        themeToggleEl.setAttribute("aria-label", "Theme: " + label);
      }
    }
    applyTheme(currentTheme());
    if (themeToggleEl) {
      themeToggleEl.onclick = () => {
        const cur = currentTheme();
        const next = THEME_ORDER[(THEME_ORDER.indexOf(cur) + 1) % THEME_ORDER.length];
        applyTheme(next);
      };
    }

    document.getElementById("refreshButton").onclick = () => state.selectedPath
      ? selectFile(state.selectedPath, { hash: state.selectedHash, historyMode: "replace" })
      : loadDir(state.cwd, { historyMode: "replace" });
    document.getElementById("toggleFavorite").onclick = toggleFavorite;
    document.getElementById("clearRecentFiles").onclick = () => {
      if (state.recentFiles.length && confirm("Clear recent files list?")) clearRecents("files");
    };
    document.getElementById("clearRecentDirs").onclick = () => {
      if (state.recentDirs.length && confirm("Clear recent folders list?")) clearRecents("dirs");
    };
    copyPathBtnEl.addEventListener("click", () => { copyCurrentPath(); });
    pathInputEl.addEventListener("keydown", (event) => {
      if (event.key === "Enter") {
        event.preventDefault();
        jumpToPath(pathInputEl.value);
      } else if (event.key === "Escape") {
        pathInputEl.value = "";
        pathInputEl.blur();
      }
    });

    // ---------- Command palette (Cmd/Ctrl+P) ----------
    // ---------- "Show all" list popup ----------
    const popupEl = document.getElementById("listPopup");
    const popupTitleEl = document.getElementById("popupTitle");
    const popupSearchEl = document.getElementById("popupSearch");
    const popupResultsEl = document.getElementById("popupResults");

    function openListPopup(kind) {
      state.popupKind = kind;
      state.popupQuery = "";
      popupSearchEl.value = "";
      const titles = {
        recentFiles: "All recent files",
        recentDirs: "All recent folders",
        favorites: "All favorites",
      };
      popupTitleEl.textContent = titles[kind] || "Items";
      popupEl.hidden = false;
      renderPopup();
      setTimeout(() => popupSearchEl.focus(), 0);
    }

    function closeListPopup() {
      state.popupKind = "";
      popupEl.hidden = true;
    }

    function popupItems() {
      if (state.popupKind === "recentFiles") {
        return state.recentFiles.map((it) => ({
          ...it,
          _kind: "file",
          _status: recentFileStatus(it),
        }));
      }
      if (state.popupKind === "recentDirs") {
        return state.recentDirs.map((it) => ({
          ...it,
          _kind: "dir",
          _status: recentDirStatus(it),
        }));
      }
      if (state.popupKind === "favorites") {
        return state.favorites.map((p) => ({
          path: p,
          name: getAlias(p) || basename(p) || p,
          alias: getAlias(p),
          openedAt: 0,
          _kind: "favorite",
          _status: recentDirStatus({ path: p }),
        }));
      }
      return [];
    }

    function renderPopup() {
      if (popupEl.hidden) return;
      const q = (state.popupQuery || "").trim().toLowerCase();
      const all = popupItems();
      const items = q
        ? all.filter((it) => {
            const name = (it.name || "").toLowerCase();
            const path = (it.path || "").toLowerCase();
            return name.includes(q) || path.includes(q);
          })
        : all;

      popupResultsEl.innerHTML = "";
      if (!items.length) {
        const empty = document.createElement("div");
        empty.className = "popup-empty";
        empty.textContent = q ? "No matches" : "Nothing here yet";
        popupResultsEl.appendChild(empty);
        return;
      }
      for (const it of items) {
        const row = document.createElement("div");
        row.className = "popup-item";
        row.dataset.path = it.path;

        const icon = document.createElement("span");
        icon.className = "popup-icon";
        icon.textContent = it._kind === "file" ? "📄" : (it._kind === "dir" ? "📁" : "★");
        row.appendChild(icon);

        const nameEl = document.createElement("span");
        nameEl.className = "popup-name";
        nameEl.textContent = it.name || basename(it.path);
        row.appendChild(nameEl);

        const timeEl = document.createElement("span");
        timeEl.className = "popup-time";
        if (it.openedAt) {
          timeEl.textContent = relativeTime(it.openedAt);
          timeEl.title = "Last opened: " + new Date(it.openedAt).toLocaleString();
        } else {
          timeEl.textContent = "";
        }
        row.appendChild(timeEl);

        const statusEl = document.createElement("span");
        const s = it._status || { state: "unknown" };
        statusEl.className = "popup-status state-" + s.state;
        statusEl.textContent = s.state === "updated" ? "Updated" : s.state === "unchanged" ? "Up to date" : "—";
        if (s.state === "unknown") statusEl.title = "Folder not visited this session — can't tell";
        row.appendChild(statusEl);

        // For favorites, expose an inline edit-alias button.
        if (it._kind === "favorite") {
          const editBtn = document.createElement("button");
          editBtn.type = "button";
          editBtn.className = "popup-edit";
          editBtn.textContent = "✎";
          editBtn.title = it.alias ? "Edit alias" : "Set alias";
          editBtn.onclick = (event) => {
            event.stopPropagation();
            promptAlias(it.path);
          };
          row.appendChild(editBtn);
        }

        const pathEl = document.createElement("span");
        pathEl.className = "popup-path";
        pathEl.textContent = shortenDisplayPath(it.path);
        pathEl.title = it.path;
        row.appendChild(pathEl);

        row.onclick = () => {
          closeListPopup();
          if (it._kind === "file") {
            selectFile(it.path, { historyMode: "push" });
          } else {
            loadDir(it.path, { historyMode: "push" });
          }
        };
        popupResultsEl.appendChild(row);
      }
    }

    popupSearchEl.addEventListener("input", () => {
      state.popupQuery = popupSearchEl.value;
      renderPopup();
    });
    popupSearchEl.addEventListener("keydown", (event) => {
      if (event.key === "Escape") { event.preventDefault(); closeListPopup(); }
    });
    document.getElementById("popupClose").onclick = closeListPopup;
    popupEl.addEventListener("click", (event) => {
      if (event.target === popupEl) closeListPopup();
    });

    document.getElementById("showAllRecentFiles").onclick = () => openListPopup("recentFiles");
    document.getElementById("showAllRecentDirs").onclick = () => openListPopup("recentDirs");
    document.getElementById("showAllFavorites").onclick = () => openListPopup("favorites");

    const paletteEl = document.getElementById("palette");
    const paletteInputEl = document.getElementById("paletteInput");
    const paletteResultsEl = document.getElementById("paletteResults");

    function paletteCandidates() {
      // Combine files + dirs, tag with kind, dedupe by path (file wins).
      const seen = new Set();
      const out = [];
      for (const f of state.recentFiles) {
        if (seen.has(f.path)) continue;
        seen.add(f.path);
        out.push({ ...f, _kind: "file" });
      }
      for (const d of state.recentDirs) {
        if (seen.has(d.path)) continue;
        seen.add(d.path);
        out.push({ ...d, _kind: "dir" });
      }
      // Sort by openedAt desc to put most-recent at top regardless of source.
      out.sort((a, b) => (b.openedAt || 0) - (a.openedAt || 0));
      return out;
    }

    function fuzzyMatchScore(query, target) {
      // Very small subsequence-fuzzy: every query char must appear in order.
      // Score rewards contiguous matches and earlier positions. Returns
      // { score, ranges } where ranges are [start, end) pairs to highlight.
      if (!query) return { score: 0, ranges: [] };
      const q = query.toLowerCase();
      const t = target.toLowerCase();
      let ti = 0, qi = 0;
      let score = 0;
      let lastMatch = -2;
      const ranges = [];
      while (ti < t.length && qi < q.length) {
        if (t[ti] === q[qi]) {
          if (ti === lastMatch + 1) {
            score += 5; // contiguous bonus
            // extend last range
            ranges[ranges.length - 1][1] = ti + 1;
          } else {
            score += 1;
            ranges.push([ti, ti + 1]);
          }
          if (ti === 0 || /[\s/_\-.]/.test(t[ti - 1])) score += 3; // word-start bonus
          lastMatch = ti;
          qi++;
        }
        ti++;
      }
      if (qi < q.length) return null; // not all chars matched
      // Earlier match cluster boosts score.
      score -= ranges[0][0] * 0.05;
      return { score, ranges };
    }

    function highlightRanges(text, ranges) {
      if (!ranges || !ranges.length) return escapeHtml(text);
      let html = "";
      let cursor = 0;
      for (const [s, e] of ranges) {
        if (cursor < s) html += escapeHtml(text.slice(cursor, s));
        html += '<span class="palette-match">' + escapeHtml(text.slice(s, e)) + "</span>";
        cursor = e;
      }
      if (cursor < text.length) html += escapeHtml(text.slice(cursor));
      return html;
    }

    function escapeHtml(s) {
      return String(s).replace(/[&<>"']/g, (c) => ({"&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;","'":"&#39;"}[c]));
    }

    function paletteFilter(query) {
      const all = paletteCandidates();
      const q = (query || "").trim();
      if (!q) return all.map((item) => ({ item, ranges: [], score: 0 }));
      const scored = [];
      for (const item of all) {
        // Match against name + path; pick the higher-scoring field for highlight.
        const name = item.name || basename(item.path);
        const nameM = fuzzyMatchScore(q, name);
        const pathM = fuzzyMatchScore(q, item.path);
        let best = null;
        if (nameM && (!best || nameM.score + 5 > best.score)) best = { ranges: nameM.ranges, score: nameM.score + 5, field: "name" };
        if (pathM && (!best || pathM.score > best.score)) best = { ranges: pathM.ranges, score: pathM.score, field: "path" };
        if (!best) continue;
        scored.push({ item, ranges: best.ranges, score: best.score, field: best.field });
      }
      scored.sort((a, b) => b.score - a.score || (b.item.openedAt || 0) - (a.item.openedAt || 0));
      return scored.slice(0, 50);
    }

    function renderPalette() {
      const results = paletteFilter(state.paletteQuery);
      paletteResultsEl.innerHTML = "";
      if (!results.length) {
        const empty = document.createElement("div");
        empty.className = "palette-empty";
        empty.textContent = state.paletteQuery ? "No matches" : "No recent items yet";
        paletteResultsEl.appendChild(empty);
        return;
      }
      if (state.paletteIndex >= results.length) state.paletteIndex = 0;
      if (state.paletteIndex < 0) state.paletteIndex = results.length - 1;
      state.paletteResults = results;
      results.forEach((r, idx) => {
        const row = document.createElement("div");
        row.className = "palette-item" + (idx === state.paletteIndex ? " active" : "");
        row.dataset.idx = String(idx);
        const name = r.item.name || basename(r.item.path);
        const kindLabel = r.item._kind === "dir" ? "📁" : "📄";
        row.innerHTML =
          '<span class="palette-kind">' + kindLabel + '</span>' +
          '<span class="palette-name">' + (r.field === "name" ? highlightRanges(name, r.ranges) : escapeHtml(name)) + '</span>' +
          '<span class="palette-time">' + escapeHtml(relativeTime(r.item.openedAt)) + '</span>' +
          '<span class="palette-path">' + (r.field === "path" ? highlightRanges(r.item.path, r.ranges) : escapeHtml(shortenDisplayPath(r.item.path))) + '</span>';
        row.onmouseenter = () => {
          state.paletteIndex = idx;
          // Re-render active highlight without rebuilding everything:
          paletteResultsEl.querySelectorAll(".palette-item.active").forEach((n) => n.classList.remove("active"));
          row.classList.add("active");
        };
        row.onclick = () => { state.paletteIndex = idx; paletteAcceptSelected(); };
        paletteResultsEl.appendChild(row);
      });
      // Keep the active row visible when navigating by keyboard.
      const active = paletteResultsEl.querySelector(".palette-item.active");
      if (active) active.scrollIntoView({ block: "nearest" });
    }

    function openPalette() {
      state.paletteOpen = true;
      state.paletteQuery = "";
      state.paletteIndex = 0;
      paletteInputEl.value = "";
      paletteEl.hidden = false;
      renderPalette();
      // Focus on next tick so the keydown that opened us doesn't bleed in.
      setTimeout(() => paletteInputEl.focus(), 0);
    }

    function closePalette() {
      state.paletteOpen = false;
      paletteEl.hidden = true;
    }

    function paletteAcceptSelected() {
      const results = state.paletteResults || [];
      const chosen = results[state.paletteIndex];
      if (!chosen) return;
      closePalette();
      if (chosen.item._kind === "dir") {
        loadDir(chosen.item.path, { historyMode: "push" });
      } else {
        selectFile(chosen.item.path, { historyMode: "push" });
      }
    }

    paletteInputEl.addEventListener("input", () => {
      state.paletteQuery = paletteInputEl.value;
      state.paletteIndex = 0;
      renderPalette();
    });
    paletteInputEl.addEventListener("keydown", (event) => {
      if (event.key === "Escape") { event.preventDefault(); closePalette(); return; }
      if (event.key === "Enter") { event.preventDefault(); paletteAcceptSelected(); return; }
      if (event.key === "ArrowDown") { event.preventDefault(); state.paletteIndex++; renderPalette(); return; }
      if (event.key === "ArrowUp") { event.preventDefault(); state.paletteIndex--; renderPalette(); return; }
    });
    paletteEl.addEventListener("click", (event) => {
      // Click outside the card closes.
      if (event.target === paletteEl) closePalette();
    });

    document.addEventListener("keydown", (event) => {
      const lowerKey = event.key.toLowerCase();
      // Cmd/Ctrl+K → open Recent palette. (Cmd+P would collide with the
      // browser's print shortcut on macOS, so we use the Slack/Notion/Linear
      // convention instead.)
      if ((event.metaKey || event.ctrlKey) && lowerKey === "k" && !event.shiftKey) {
        event.preventDefault();
        if (state.paletteOpen) closePalette(); else openPalette();
        return;
      }
      // Esc closes the "Show all" popup when focus is anywhere outside its input.
      if (event.key === "Escape" && !popupEl.hidden) {
        closeListPopup();
        return;
      }
      // Cmd/Ctrl+L → focus the "Jump to path" input (URL-bar style).
      if ((event.metaKey || event.ctrlKey) && lowerKey === "l") {
        event.preventDefault();
        pathInputEl.focus();
        pathInputEl.select();
        return;
      }
      // Cmd/Ctrl+Shift+. → Finder-style toggle for showing hidden (dot) files.
      // Match by event.code so it works regardless of which character the
      // Shift+. combo emits ('>' on US layouts, etc.).
      if ((event.metaKey || event.ctrlKey) && event.shiftKey &&
          (event.code === "Period" || lowerKey === "." || lowerKey === ">")) {
        event.preventDefault();
        state.showHidden = !state.showHidden;
        try { localStorage.setItem("mdviewer.showHidden", state.showHidden ? "1" : "0"); } catch (e) {}
        statusTextEl.textContent = state.showHidden ? "Hidden files: shown" : "Hidden files: hidden";
        loadDir(state.cwd, { keepSelection: true, silent: true });
        return;
      }
      const isSaveKey = (event.metaKey || event.ctrlKey) && lowerKey === "s";
      if (!isSaveKey) return;
      if (!state.selectedPath || !canEditKind(state.selectedKind)) return;
      event.preventDefault();
      saveCurrentFile();
    });

    // ---------- Inline zoom + Lightbox for images and mermaid diagrams ----------
    const lightboxEl = document.getElementById("lightbox");
    const lightboxStageEl = document.getElementById("lightboxStage");
    const lightboxScaleEl = document.getElementById("lightboxScale");
    const lightboxToolbarEl = lightboxEl.querySelector(".lightbox-toolbar");
    const ZOOM_MIN = 0.2;
    const ZOOM_MAX = 12;
    const lbState = { scale: 1, x: 0, y: 0, dragging: false, sx: 0, sy: 0, dx: 0, dy: 0, didDrag: false };

    function clamp(v, lo, hi) { return Math.min(hi, Math.max(lo, v)); }

    function findScalableTarget(child) {
      if (!child) return null;
      if (child.tagName === "svg") return { el: child, kind: "svg" };
      const svg = child.querySelector("svg");
      if (svg) return { el: svg, kind: "svg" };
      if (child.tagName === "IMG") return { el: child, kind: "img" };
      return null;
    }

    // Returns the union of every painted leaf element's bbox, transformed
    // into SVG user space via getCTM(). Reliable without layout — but
    // does NOT include markers (arrowheads) or stroke width, since those
    // are excluded from getBBox().
    function leafUnionBBox(svg) {
      let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
      let found = false;
      const selector = "path, rect, circle, ellipse, line, polyline, polygon, text, foreignObject, image, use";
      const nodes = svg.querySelectorAll(selector);
      for (const el of nodes) {
        if (el.closest("defs, marker, clipPath, mask, pattern, symbol")) continue;
        let cs;
        try { cs = window.getComputedStyle(el); } catch (e) { continue; }
        if (cs.display === "none" || cs.visibility === "hidden") continue;
        if (parseFloat(cs.opacity || "1") <= 0) continue;
        if (typeof el.getBBox !== "function") continue;
        let bb;
        try { bb = el.getBBox(); } catch (e) { continue; }
        if (!bb || (bb.width <= 0 && bb.height <= 0)) continue;
        // Inflate by half the stroke width so the bbox includes the
        // stroke ring rather than just the geometric path.
        const sw = parseFloat(cs.strokeWidth || "0") || 0;
        const half = sw / 2;
        const bx = bb.x - half, by = bb.y - half;
        const bw = bb.width + sw, bh = bb.height + sw;
        let ctm = null;
        try { ctm = el.getCTM(); } catch (e) {}
        const corners = [
          [bx,       by],
          [bx + bw,  by],
          [bx,       by + bh],
          [bx + bw,  by + bh],
        ];
        for (let i = 0; i < 4; i++) {
          let px = corners[i][0];
          let py = corners[i][1];
          if (ctm) {
            const x = ctm.a * px + ctm.c * py + ctm.e;
            const y = ctm.b * px + ctm.d * py + ctm.f;
            px = x; py = y;
          }
          if (px < minX) minX = px;
          if (py < minY) minY = py;
          if (px > maxX) maxX = px;
          if (py > maxY) maxY = py;
        }
        found = true;
      }
      if (!found) return null;
      return { x: minX, y: minY, width: maxX - minX, height: maxY - minY };
    }

    // Picks the best crop rectangle for the SVG. The leaf-union bbox is
    // tight and ignores invisible scaffolding (good for Mermaid) but
    // excludes arrowhead markers, which can cause the right (or any)
    // edge of the diagram to be cut off. The root getBBox includes
    // markers but can include hidden scaffolding too. We blend:
    //   • If both exist and root is only mildly larger than leaf (≤2.5×
    //     area), prefer root — markers are included, scaffolding is
    //     negligible.
    //   • If root is dramatically larger, scaffolding is inflating it →
    //     fall back to the leaf union.
    function computeVisualSvgBBox(svg) {
      const leaf = leafUnionBBox(svg);
      let root = null;
      if (typeof svg.getBBox === "function") {
        try {
          const r = svg.getBBox();
          if (r && r.width > 0 && r.height > 0) root = r;
        } catch (e) {}
      }
      if (!leaf && !root) return null;
      if (!leaf) return root;
      if (!root) return leaf;
      const leafArea = Math.max(1, leaf.width * leaf.height);
      const rootArea = root.width * root.height;
      return rootArea > leafArea * 2.5 ? leaf : root;
    }

    function captureLightboxNatural() {
      const child = lightboxStageEl.firstElementChild;
      const target = findScalableTarget(child);
      if (!target) return;
      if (target.kind === "svg") {
        const svg = target.el;
        let bb = null;
        // Strategy 1: union of visible leaf-element pixel bboxes
        //   (most reliable — ignores invisible Mermaid scaffolding).
        bb = computeVisualSvgBBox(svg);
        // Strategy 2: fall back to root getBBox().
        if (!bb && typeof svg.getBBox === "function") {
          try {
            const b = svg.getBBox();
            if (b.width > 0 && b.height > 0) bb = b;
          } catch (e) {}
        }
        // Strategy 3: declared viewBox.
        if (!bb) {
          const vb = svg.getAttribute("viewBox");
          if (vb) {
            const parts = vb.trim().split(/[\s,]+/);
            if (parts.length === 4) {
              bb = {
                x: parseFloat(parts[0]) || 0,
                y: parseFloat(parts[1]) || 0,
                width: parseFloat(parts[2]),
                height: parseFloat(parts[3]),
              };
            }
          }
        }
        // Strategy 4: declared width/height attributes.
        if (!bb) {
          const w = parseFloat(svg.getAttribute("width"));
          const h = parseFloat(svg.getAttribute("height"));
          if (w > 0 && h > 0) bb = { x: 0, y: 0, width: w, height: h };
        }
        if (bb && bb.width > 0 && bb.height > 0) {
          // Use the larger dimension for the padding so wide diagrams get
          // enough breathing room on the long axis (where arrowheads /
          // labels are most likely to overflow).
          const pad = Math.max(16, Math.max(bb.width, bb.height) * 0.03);
          const vbX = bb.x - pad;
          const vbY = bb.y - pad;
          const vbW = bb.width + pad * 2;
          const vbH = bb.height + pad * 2;
          svg.setAttribute("viewBox", vbX + " " + vbY + " " + vbW + " " + vbH);
          svg.setAttribute("preserveAspectRatio", "xMidYMid meet");
          svg.dataset.lbNaturalW = vbW;
          svg.dataset.lbNaturalH = vbH;
        }
      } else if (target.kind === "img") {
        const img = target.el;
        if (img.naturalWidth) img.dataset.lbNaturalW = img.naturalWidth;
        if (img.naturalHeight) img.dataset.lbNaturalH = img.naturalHeight;
      }
    }

    function applyLightboxScale() {
      const child = lightboxStageEl.firstElementChild;
      const target = findScalableTarget(child);
      if (!target) return;
      const el = target.el;
      const natW = parseFloat(el.dataset.lbNaturalW) || 0;
      const natH = parseFloat(el.dataset.lbNaturalH) || 0;
      if (natW <= 0 || natH <= 0) return;
      const w = natW * lbState.scale;
      const h = natH * lbState.scale;
      if (target.kind === "svg") {
        // Resize the SVG itself so it re-rasterizes at the new size (crisp at any zoom).
        el.setAttribute("width", w);
        el.setAttribute("height", h);
        el.style.width = w + "px";
        el.style.height = h + "px";
      } else {
        el.style.width = w + "px";
        el.style.height = h + "px";
      }
    }

    function applyLightboxTransform() {
      // Pan via CSS translate, scale via element resize (so SVG re-renders crisply).
      lightboxStageEl.style.transform = "translate(" + lbState.x + "px, " + lbState.y + "px)";
      applyLightboxScale();
      lightboxScaleEl.textContent = Math.round(lbState.scale * 100) + "%";
    }

    function fitLightboxContent() {
      const child = lightboxStageEl.firstElementChild;
      if (!child) return;
      // Make sure we know the natural dimensions before sizing.
      captureLightboxNatural();
      const target = findScalableTarget(child);
      let natW = 0, natH = 0;
      if (target) {
        natW = parseFloat(target.el.dataset.lbNaturalW) || 0;
        natH = parseFloat(target.el.dataset.lbNaturalH) || 0;
      }
      const finish = () => {
        const vw = window.innerWidth, vh = window.innerHeight;
        // SVG is vector — let it scale up freely to fill the viewport.
        // Raster images cap at 2x to avoid obvious pixelation.
        const cap = target && target.kind === "svg" ? 12 : 2;
        const fit = Math.min(cap, Math.min(vw * 0.92 / natW, vh * 0.86 / natH));
        lbState.scale = fit > 0 ? fit : 1;
        lbState.x = (vw - natW * lbState.scale) / 2;
        lbState.y = (vh - natH * lbState.scale) / 2;
        applyLightboxTransform();
      };
      if (natW > 0 && natH > 0) {
        finish();
        return;
      }
      // Fall back to bounding-rect measurement when natural size isn't reported yet.
      lbState.scale = 1; lbState.x = 0; lbState.y = 0;
      applyLightboxTransform();
      requestAnimationFrame(() => {
        const w = child.offsetWidth || child.getBoundingClientRect().width;
        const h = child.offsetHeight || child.getBoundingClientRect().height;
        if (!w || !h) {
          requestAnimationFrame(fitLightboxContent);
          return;
        }
        // Treat the measured size as natural so subsequent zooms scale from here.
        if (target && target.el) {
          target.el.dataset.lbNaturalW = w;
          target.el.dataset.lbNaturalH = h;
          natW = w; natH = h;
          finish();
        } else {
          const vw = window.innerWidth, vh = window.innerHeight;
          const fitVal = Math.min(2, Math.min(vw * 0.92 / w, vh * 0.86 / h));
          lbState.scale = fitVal > 0 ? fitVal : 1;
          lbState.x = (vw - w * lbState.scale) / 2;
          lbState.y = (vh - h * lbState.scale) / 2;
          // No scalable target → fall back to CSS transform scale on the stage.
          lightboxStageEl.style.transform = "translate(" + lbState.x + "px, " + lbState.y + "px) scale(" + lbState.scale + ")";
          lightboxScaleEl.textContent = Math.round(lbState.scale * 100) + "%";
        }
      });
    }

    function openLightbox(node) {
      lightboxStageEl.innerHTML = "";
      lightboxStageEl.appendChild(node);
      lightboxEl.hidden = false;
      document.body.classList.add("lightbox-open");
      // Strip Mermaid's max-width / inline sizing so the SVG can lay out
      // at its intrinsic size; we'll set our own dimensions in
      // captureLightboxNatural / applyLightboxScale.
      const target = findScalableTarget(node);
      if (target && target.kind === "svg") {
        const svg = target.el;
        svg.style.maxWidth = "none";
        svg.style.maxHeight = "none";
        svg.style.width = "";
        svg.style.height = "";
      }
      // Defer measurement one frame so getBoundingClientRect on inner
      // shapes returns real values (the SVG was just inserted).
      requestAnimationFrame(() => {
        captureLightboxNatural();
        fitLightboxContent();
      });
    }

    function closeLightbox() {
      lightboxEl.hidden = true;
      lightboxStageEl.innerHTML = "";
      document.body.classList.remove("lightbox-open");
    }

    function lightboxZoomAt(clientX, clientY, factor) {
      const newScale = clamp(lbState.scale * factor, ZOOM_MIN, ZOOM_MAX);
      const ratio = newScale / lbState.scale;
      lbState.x = clientX - (clientX - lbState.x) * ratio;
      lbState.y = clientY - (clientY - lbState.y) * ratio;
      lbState.scale = newScale;
      applyLightboxTransform();
    }

    lightboxEl.addEventListener("wheel", (event) => {
      event.preventDefault();
      const factor = event.deltaY < 0 ? 1.18 : 1 / 1.18;
      lightboxZoomAt(event.clientX, event.clientY, factor);
    }, { passive: false });

    lightboxEl.addEventListener("pointerdown", (event) => {
      if (event.target.closest(".lightbox-toolbar")) return;
      // Alt/Option held → user wants to select text inside the diagram,
      // not pan the lightbox. Let the browser handle the selection.
      if (event.altKey || state.altKey) return;
      // Click on backdrop (outside the stage) closes; tracked via target match in pointerup.
      lbState.dragging = true;
      lbState.didDrag = false;
      lbState.sx = event.clientX;
      lbState.sy = event.clientY;
      lbState.dx = lbState.x;
      lbState.dy = lbState.y;
      lightboxEl.classList.add("dragging");
      try { lightboxEl.setPointerCapture(event.pointerId); } catch (e) {}
    });

    lightboxEl.addEventListener("pointermove", (event) => {
      if (!lbState.dragging) return;
      // Same guard as the inline pan handler — abort if Alt comes down mid-drag.
      if (event.altKey || state.altKey) {
        lbState.dragging = false;
        lightboxEl.classList.remove("dragging");
        try { lightboxEl.releasePointerCapture(event.pointerId); } catch (e) {}
        return;
      }
      const dx = event.clientX - lbState.sx;
      const dy = event.clientY - lbState.sy;
      if (!lbState.didDrag && Math.hypot(dx, dy) > 3) lbState.didDrag = true;
      lbState.x = lbState.dx + dx;
      lbState.y = lbState.dy + dy;
      applyLightboxTransform();
    });

    lightboxEl.addEventListener("pointerup", (event) => {
      lbState.dragging = false;
      lightboxEl.classList.remove("dragging");
      try { lightboxEl.releasePointerCapture(event.pointerId); } catch (e) {}
      // Only close via the ✕ button or the Esc key — clicking the
      // backdrop is reserved for pan/zoom interactions so the user
      // doesn't accidentally close the view mid-inspection.
    });

    lightboxStageEl.addEventListener("dblclick", (event) => {
      event.preventDefault();
      fitLightboxContent();
    });

    lightboxToolbarEl.addEventListener("click", (event) => {
      const btn = event.target.closest("button[data-action]");
      if (!btn) return;
      const action = btn.dataset.action;
      const cx = window.innerWidth / 2;
      const cy = window.innerHeight / 2;
      if (action === "close") closeLightbox();
      else if (action === "zoom-in") lightboxZoomAt(cx, cy, 1.25);
      else if (action === "zoom-out") lightboxZoomAt(cx, cy, 1 / 1.25);
      else if (action === "reset") fitLightboxContent();
    });

    document.addEventListener("keydown", (event) => {
      if (lightboxEl.hidden) return;
      if (event.key === "Escape") { event.preventDefault(); closeLightbox(); }
      else if (event.key === "+" || event.key === "=") { event.preventDefault(); lightboxZoomAt(window.innerWidth / 2, window.innerHeight / 2, 1.25); }
      else if (event.key === "-" || event.key === "_") { event.preventDefault(); lightboxZoomAt(window.innerWidth / 2, window.innerHeight / 2, 1 / 1.25); }
      else if (event.key === "0") { event.preventDefault(); fitLightboxContent(); }
    });

    function buildLightboxClone(source) {
      if (source.tagName === "IMG") {
        const clone = source.cloneNode(true);
        clone.removeAttribute("style");
        return clone;
      }
      if (source.classList.contains("mermaid")) {
        const wrap = document.createElement("div");
        wrap.className = "mermaid";
        wrap.innerHTML = source.innerHTML;
        return wrap;
      }
      return source.cloneNode(true);
    }

    // ----- Inline zoom on the in-page image / mermaid element -----
    const inlineState = new WeakMap();

    // Track Alt/Option key globally so .mermaid hosts can switch into
    // text-selection mode. The CSS toggles on the .alt-select class.
    // We also expose a boolean (state.altKey) because the per-pointer
    // event.altKey property is not always reliable across browsers /
    // pointer-event sources (touch, pen, some Linux WMs). The keydown/keyup
    // path here is the authoritative source.
    state.altKey = false;
    function setMermaidAltSelect(on) {
      state.altKey = !!on;
      const nodes = document.querySelectorAll(".mermaid");
      nodes.forEach((n) => n.classList.toggle("alt-select", !!on));
      // Also flip a body-level flag so we can override .lightbox's
      // user-select: none (and any third-party extension styles) with high
      // specificity + !important. Without this, alt-drag inside the
      // lightbox can't select SVG text.
      document.body.classList.toggle("alt-select-mode", !!on);
    }
    window.addEventListener("keydown", (event) => {
      if (event.key === "Alt" || event.altKey) setMermaidAltSelect(true);
    });
    window.addEventListener("keyup", (event) => {
      if (event.key === "Alt") setMermaidAltSelect(false);
    });
    // Browsers sometimes miss keyup if focus leaves — clear on blur to be safe.
    window.addEventListener("blur", () => setMermaidAltSelect(false));
    // Some platforms only deliver altKey via mouse events; sync from those too.
    window.addEventListener("mousemove", (event) => {
      if (event.altKey && !state.altKey) setMermaidAltSelect(true);
      else if (!event.altKey && state.altKey) setMermaidAltSelect(false);
    }, { passive: true });

    // ----- Copy diagram text to clipboard -----
    function extractMermaidText(svg) {
      // Collect every <text> and <foreignObject>, group by y (line),
      // sort each line by x, then join. This gives a roughly readable
      // textual dump of the diagram contents.
      const nodes = svg.querySelectorAll("text, foreignObject");
      const items = [];
      for (const node of nodes) {
        // Skip foreignObject children that are themselves inside another
        // foreignObject we already captured (we'd double-count).
        if (node.tagName === "text" && node.closest("foreignObject")) continue;
        let rect;
        try { rect = node.getBoundingClientRect(); } catch (e) { continue; }
        const text = (node.textContent || "").replace(/\s+/g, " ").trim();
        if (!text) continue;
        items.push({ y: rect.top, x: rect.left, text });
      }
      if (!items.length) return "";
      items.sort((a, b) => a.y - b.y || a.x - b.x);
      const lines = [];
      let current = [];
      let lastY = -Infinity;
      const lineTolerance = 6; // px
      for (const it of items) {
        if (Math.abs(it.y - lastY) > lineTolerance && current.length) {
          lines.push(current);
          current = [];
        }
        current.push(it);
        lastY = it.y;
      }
      if (current.length) lines.push(current);
      return lines
        .map((line) => line.sort((a, b) => a.x - b.x).map((it) => it.text).join("  "))
        .join("\n");
    }

    async function copyMermaidText(el, btn) {
      const svg = el.querySelector("svg");
      if (!svg) return;
      const text = extractMermaidText(svg);
      if (!text) {
        if (btn) flashButton(btn, "Empty", false);
        return;
      }
      try {
        await navigator.clipboard.writeText(text);
        if (btn) flashButton(btn, "Copied ✓", true);
      } catch (err) {
        console.error("copy failed:", err);
        if (btn) flashButton(btn, "Copy failed", false);
      }
    }

    function flashButton(btn, label, ok) {
      const original = btn.dataset.originalLabel || btn.textContent;
      btn.dataset.originalLabel = original;
      btn.textContent = label;
      btn.classList.toggle("copied", !!ok);
      clearTimeout(btn._flashTimer);
      btn._flashTimer = setTimeout(() => {
        btn.textContent = btn.dataset.originalLabel;
        btn.classList.remove("copied");
      }, 1400);
    }

    function attachMermaidToolbar(el) {
      if (!el.classList.contains("mermaid")) return;
      // The toolbar lives on the wrapper, not inside .mermaid itself —
      // mermaid 11.x rewrites .mermaid's children so a toolbar appended
      // inside would be erased on each render.
      const wrap = el.closest(".mermaid-wrap");
      if (!wrap) return;
      if (wrap.dataset.toolbarAttached === "1") return;
      // Only attach AFTER mermaid finishes rendering and produces an <svg>.
      if (!el.querySelector("svg")) return;
      wrap.dataset.toolbarAttached = "1";
      const bar = document.createElement("div");
      bar.className = "mermaid-toolbar";
      const copyBtn = document.createElement("button");
      copyBtn.type = "button";
      copyBtn.className = "mermaid-tool-btn";
      copyBtn.textContent = "Copy text";
      copyBtn.title = "Copy all text in this diagram (Alt-drag to select manually)";
      // Stop these events from bubbling to the diagram's zoom/pan handlers,
      // otherwise clicking the button would open the lightbox.
      const stop = (e) => { e.stopPropagation(); };
      copyBtn.addEventListener("pointerdown", stop);
      copyBtn.addEventListener("pointerup", stop);
      copyBtn.addEventListener("click", (event) => {
        event.stopPropagation();
        event.preventDefault();
        copyMermaidText(el, copyBtn);
      });
      bar.appendChild(copyBtn);
      wrap.appendChild(bar);
    }

    function getInline(el) {
      let s = inlineState.get(el);
      if (!s) {
        s = { scale: 1, x: 0, y: 0, dragging: false, sx: 0, sy: 0, dx: 0, dy: 0, didDrag: false };
        inlineState.set(el, s);
      }
      return s;
    }

    function applyInlineTransform(el) {
      const s = getInline(el);
      el.style.transformOrigin = "0 0";
      el.style.transform = "translate(" + s.x + "px, " + s.y + "px) scale(" + s.scale + ")";
      el.classList.toggle("inline-zoomed", s.scale !== 1 || s.x !== 0 || s.y !== 0);
      el.style.cursor = s.scale > 1 ? "grab" : "zoom-in";
      el.style.position = s.scale !== 1 || s.x !== 0 || s.y !== 0 ? "relative" : "";
      el.style.zIndex = s.scale > 1 ? "5" : "";
    }

    function resetInline(el) {
      const s = getInline(el);
      s.scale = 1; s.x = 0; s.y = 0;
      applyInlineTransform(el);
    }

    function inlineZoomAt(el, clientX, clientY, factor) {
      const s = getInline(el);
      const rect = el.getBoundingClientRect();
      // Use element parent's coordinate space; transform-origin is the element's top-left (pre-transform).
      // Compute mouse offset within the element's current visual bounding rect.
      const mx = clientX - rect.left;
      const my = clientY - rect.top;
      // The point in untransformed local space:
      const lx = mx / s.scale;
      const ly = my / s.scale;
      const newScale = clamp(s.scale * factor, ZOOM_MIN, ZOOM_MAX);
      // Keep the point under the cursor stationary while zooming.
      // After zoom: new_x = old_x - lx * (new_scale - old_scale)
      s.x = s.x - lx * (newScale - s.scale);
      s.y = s.y - ly * (newScale - s.scale);
      s.scale = newScale;
      applyInlineTransform(el);
    }

    function attachInlineZoom(el) {
      if (el.dataset.zoomAttached === "1") return;
      el.dataset.zoomAttached = "1";

      el.addEventListener("wheel", (event) => {
        const s = getInline(el);
        const modifier = event.ctrlKey || event.metaKey;
        // Allow page scroll when not zoomed and no modifier held.
        if (!modifier && s.scale === 1) return;
        event.preventDefault();
        const factor = event.deltaY < 0 ? 1.15 : 1 / 1.15;
        inlineZoomAt(el, event.clientX, event.clientY, factor);
      }, { passive: false });

      el.addEventListener("pointerdown", (event) => {
        if (event.button !== 0) return;
        // Don't start pan/drag if user is interacting with the diagram toolbar.
        if (event.target && event.target.closest && event.target.closest(".mermaid-toolbar")) return;
        // Alt/Option held → user wants to select text, not pan. Check both
        // the per-event flag AND our authoritative global Alt state so that
        // we don't miss it when the pointer source doesn't carry modifiers.
        if (event.altKey || state.altKey) return;
        const s = getInline(el);
        s.dragging = true;
        s.didDrag = false;
        s.sx = event.clientX; s.sy = event.clientY;
        s.dx = s.x; s.dy = s.y;
        if (s.scale > 1) {
          try { el.setPointerCapture(event.pointerId); } catch (e) {}
          el.style.cursor = "grabbing";
        }
      });

      el.addEventListener("pointermove", (event) => {
        const s = getInline(el);
        if (!s.dragging) return;
        // If the user presses Alt mid-drag, abort the pan so they can switch
        // into text-selection mode without first releasing the mouse button.
        if (event.altKey || state.altKey) {
          s.dragging = false;
          try { el.releasePointerCapture(event.pointerId); } catch (e) {}
          el.style.cursor = s.scale > 1 ? "grab" : "zoom-in";
          return;
        }
        const dx = event.clientX - s.sx;
        const dy = event.clientY - s.sy;
        if (!s.didDrag && Math.hypot(dx, dy) > 3) s.didDrag = true;
        if (s.scale > 1) {
          s.x = s.dx + dx;
          s.y = s.dy + dy;
          applyInlineTransform(el);
        }
      });

      el.addEventListener("pointerup", (event) => {
        const s = getInline(el);
        const wasDrag = s.didDrag && s.scale > 1;
        const fromToolbar = event.target && event.target.closest && event.target.closest(".mermaid-toolbar");
        s.dragging = false;
        try { el.releasePointerCapture(event.pointerId); } catch (e) {}
        applyInlineTransform(el);
        // Don't open lightbox if: user was dragging, clicked the toolbar,
        // held Alt (text-select mode), or there is an active text selection
        // they probably want to keep.
        const sel = window.getSelection && window.getSelection();
        const hasSelection = sel && !sel.isCollapsed && el.contains(sel.anchorNode);
        if (!wasDrag && !fromToolbar && !event.altKey && !hasSelection) {
          openLightbox(buildLightboxClone(el));
        }
      });

      el.addEventListener("dblclick", (event) => {
        event.preventDefault();
        event.stopPropagation();
        resetInline(el);
      });

      // Suppress default click behavior so it doesn't conflict with our pointerup logic.
      el.addEventListener("click", (event) => {
        // Toolbar buttons handle their own click; let them through.
        if (event.target && event.target.closest && event.target.closest(".mermaid-toolbar")) return;
        event.preventDefault();
        event.stopPropagation();
      });
    }

    function attachZoomToPreview() {
      const targets = previewBodyEl.querySelectorAll("img, .mermaid");
      targets.forEach((el) => {
        attachInlineZoom(el);
        if (el.classList.contains("mermaid")) attachMermaidToolbar(el);
      });
    }

    // Re-attach zoom handlers any time the preview body content changes.
    const previewObserver = new MutationObserver(() => attachZoomToPreview());
    previewObserver.observe(previewBodyEl, { childList: true, subtree: true });
    attachZoomToPreview();

    applySidebarLayout();
    applySearchPanelLayout();
    applySearchPanelCollapsed();
    updateSortButtons();
    updateEditorButtons();
    renderRecents();
    for (const btn of document.querySelectorAll('.section[data-section] .section-toggle')) {
      const sec = btn.closest('.section');
      const name = sec && sec.dataset.section;
      if (!name) continue;
      btn.addEventListener("click", function () { toggleSection(name); });
    }
    applyAllSectionLayouts();
    // Refresh relative-time labels in the Recent sections every minute so
    // "2m ago" doesn't sit stale at 0s for hours.
    setInterval(() => { renderRecents(); }, 60 * 1000);
    setInterval(refreshSelected, 2000);
    setInterval(refreshCurrentDir, 2500);
    restoreRoute(routeFromLocation(), "replace");
  </script>
</body>
</html>`

const searchMaxSnippets = 3
const searchSnippetLen = 60
const searchMaxFileBytes = 2 * 1024 * 1024 // skip files larger than 2 MB

type searchResult struct {
	Path     string   `json:"path"`
	Count    int      `json:"count"`
	Snippets []string `json:"snippets"`
}

// isProbablyText returns true if the byte slice looks like text content.
// We declare "binary" when it contains a NUL byte in the prefix — the same
// heuristic git uses.
func isProbablyText(b []byte) bool {
	limit := len(b)
	if limit > 8000 {
		limit = 8000
	}
	for i := 0; i < limit; i++ {
		if b[i] == 0 {
			return false
		}
	}
	return true
}

func (s *webServer) handleSearch(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("dir")
	if dir == "" {
		dir = s.startDir
	}
	q := r.URL.Query().Get("q")
	if q == "" {
		http.Error(w, "missing q", http.StatusBadRequest)
		return
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		http.Error(w, "invalid dir", http.StatusBadRequest)
		return
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	needle := strings.ToLower(q)
	out := []searchResult{}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.Size() > searchMaxFileBytes {
			continue
		}
		full := filepath.Join(abs, e.Name())
		data, err := os.ReadFile(full)
		if err != nil || !isProbablyText(data) {
			continue
		}
		lower := strings.ToLower(string(data))
		count := strings.Count(lower, needle)
		if count == 0 {
			continue
		}
		snippets := collectSnippets(string(data), lower, needle, searchMaxSnippets)
		out = append(out, searchResult{Path: full, Count: count, Snippets: snippets})
	}
	// Most matches first.
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	s.writeJSON(w, http.StatusOK, out)
}

// collectSnippets returns up to maxCount short context strings around the
// first matches of needle in haystackLower (which must be the lowercase
// of haystack).
func collectSnippets(haystack, haystackLower, needleLower string, maxCount int) []string {
	out := []string{}
	from := 0
	for i := 0; i < maxCount; i++ {
		idx := strings.Index(haystackLower[from:], needleLower)
		if idx < 0 {
			break
		}
		absIdx := from + idx
		startCtx := absIdx - searchSnippetLen/2
		if startCtx < 0 {
			startCtx = 0
		}
		endCtx := absIdx + len(needleLower) + searchSnippetLen/2
		if endCtx > len(haystack) {
			endCtx = len(haystack)
		}
		snip := haystack[startCtx:endCtx]
		// trim newlines for a clean one-line preview
		snip = strings.ReplaceAll(snip, "\n", " ")
		snip = strings.TrimSpace(snip)
		out = append(out, snip)
		from = absIdx + len(needleLower)
	}
	return out
}
