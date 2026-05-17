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
	Favorites []string   `json:"favorites"`
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

func runWebServer(startDir, appRoot string) error {
	server := &webServer{
		startDir: startDir,
		appRoot:  appRoot,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", server.handleIndex)
	mux.HandleFunc("/api/list", server.handleList)
	mux.HandleFunc("/api/file", server.handleFile)
	mux.HandleFunc("/api/file/save", server.handleSaveFile)
	mux.HandleFunc("/api/raw", server.handleRaw)
	mux.HandleFunc("/api/favorites/toggle", server.handleToggleFavorite)
	mux.HandleFunc("/api/resolve", server.handleResolve)

	addr := "127.0.0.1:8421"
	fmt.Printf("mdviewer web preview running at http://%s\n", addr)
	return http.ListenAndServe(addr, mux)
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
		if strings.HasPrefix(item.Name(), ".") {
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
  <script src="https://cdn.jsdelivr.net/npm/marked/marked.min.js"></script>
  <style>
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
      grid-template-columns: var(--sidebar-width) var(--splitter-width) minmax(0, 1fr);
      height: 100vh;
      gap: 0;
      padding: 18px;
      overflow: hidden;
    }
    .app.sidebar-collapsed {
      grid-template-columns: 0px 0px minmax(0, 1fr);
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
    .file, .favorite {
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
    .file:hover, .favorite:hover { background: color-mix(in oklab, var(--panel-2) 80%, transparent); }
    .file.active, .favorite.active { background: color-mix(in oklab, var(--accent) 16%, var(--panel-2)); }
    .file.updated {
      box-shadow: inset 0 0 0 1px color-mix(in oklab, var(--accent-2) 65%, transparent);
      background: color-mix(in oklab, var(--accent-2) 12%, var(--panel-2));
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
    .actions { display: flex; gap: 10px; }
    .actions { flex: 0 0 auto; }
    .chip, .action {
      border-radius: 999px;
      border: 1px solid color-mix(in oklab, var(--line) 85%, transparent);
      background: color-mix(in oklab, var(--panel-2) 78%, transparent);
      color: var(--text);
      padding: 9px 12px;
      font-size: 12px;
      appearance: none;
      -webkit-appearance: none;
    }
    .action { cursor: pointer; }
    .action:disabled {
      opacity: 0.45;
      cursor: default;
    }
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
    @media (max-width: 960px) {
      .app { grid-template-columns: 1fr; grid-template-rows: 42vh 1fr; gap: 18px; }
      .splitter { display: none; }
      .app.sidebar-collapsed { grid-template-columns: 1fr; grid-template-rows: 1fr; }
      .app.sidebar-collapsed .sidebar-shell { display: none; }
      .reveal-sidebar { display: inline-flex; }
    }
  </style>
</head>
<body>
  <div class="app" id="appShell">
    <aside class="shell sidebar sidebar-shell">
      <div class="topbar sidebar-topbar">
        <div>
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
      <div class="section">
        <div class="section-head">
          <div class="section-title">Favorites</div>
          <button class="action" id="toggleFavorite">Toggle current</button>
        </div>
        <div id="favorites"></div>
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
          <button class="action" id="previewModeButton">Preview</button>
          <button class="action" id="editModeButton">Edit</button>
          <button class="action" id="saveButton">Save</button>
          <button class="action" id="refreshButton">Refresh</button>
          <span class="chip" id="kindChip">Idle</span>
        </div>
      </div>
      <div class="preview-body" id="previewBody">
        <div class="empty">Choose a Markdown, text, Mermaid, or image file from the left.</div>
      </div>
      <div class="preview-foot">
        <span id="statusText">Ready</span>
        <span id="scrollText">Preview 0%</span>
      </div>
    </main>
  </div>
  <button class="action reveal-sidebar" id="revealSidebar" title="Show sidebar">☰ Files</button>
  <div class="floating-tooltip" id="floatingTooltip"></div>

  <script type="module">
    import mermaid from "https://cdn.jsdelivr.net/npm/mermaid@11/dist/mermaid.esm.min.mjs";
    mermaid.initialize({ startOnLoad: false, theme: "neutral", securityLevel: "loose" });

    const state = {
      cwd: "",
      entries: [],
      sessionStartedAt: Date.now(),
      prevEntryMap: {},
      fileFlags: {},
      dismissedEntryMap: {},
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
      seenDirectories: {},
      sidebarWidth: Number(localStorage.getItem("mdviewer.sidebarWidth") || 320),
      sidebarCollapsed: localStorage.getItem("mdviewer.sidebarCollapsed") === "1",
    };

    const appShellEl = document.getElementById("appShell");
    const filesEl = document.getElementById("files");
    const favoritesEl = document.getElementById("favorites");
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
    const previewModeButtonEl = document.getElementById("previewModeButton");
    const editModeButtonEl = document.getElementById("editModeButton");
    const saveButtonEl = document.getElementById("saveButton");
    const floatingTooltipEl = document.getElementById("floatingTooltip");
    const splitterEl = document.getElementById("splitter");
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
      previewModeButtonEl.classList.toggle("active", state.editorMode === "preview");
      editModeButtonEl.classList.toggle("active", state.editorMode === "edit");
      editModeButtonEl.disabled = !editable || !state.selectedPath;
      saveButtonEl.disabled = !editable || !state.selectedPath || !state.editDirty;
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

    function updateChangedPaths(dir, entries, options = {}) {
      const nextMap = {};
      const nextFlags = {};

      for (const entry of entries) {
        if (state.fileFlags[entry.path]) {
          nextFlags[entry.path] = state.fileFlags[entry.path];
        }
      }

      const firstVisitToDir = !state.seenDirectories[dir];
      for (const entry of entries) {
        const modTime = entry.mod_time || entry.modTime || "";
        nextMap[entry.path] = modTime;
        if (entry.is_dir) {
          continue;
        }

        const dismissedAt = state.dismissedEntryMap[entry.path];
        const previous = state.prevEntryMap[entry.path];

        if (firstVisitToDir) {
          const isRecent = modTime && (Date.parse(modTime) >= state.sessionStartedAt - 5 * 60 * 1000);
          if (!options.silent && isRecent && dismissedAt !== modTime) {
            nextFlags[entry.path] = "recent";
          }
          continue;
        }

        if (typeof previous === "undefined") {
          if (!options.silent && dismissedAt !== modTime) {
            nextFlags[entry.path] = "new";
          }
          continue;
        }

        if (!options.silent && previous !== modTime && dismissedAt !== modTime) {
          nextFlags[entry.path] = "updated";
        }
      }

      for (const path of Object.keys(nextFlags)) {
        if (!(path in nextMap)) {
          delete nextFlags[path];
        }
      }

      state.prevEntryMap = nextMap;
      state.seenDirectories[dir] = true;
      if (!options.silent) {
        state.fileFlags = nextFlags;
      }
    }

    function clearFileFlag(path, modTime = "") {
      if (!path) return;
      const currentMod = modTime || state.prevEntryMap[path] || state.selectedModTime || "";
      if (currentMod) {
        state.dismissedEntryMap[path] = currentMod;
      }
      delete state.fileFlags[path];
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

    async function loadDir(dir = "", options = {}) {
      const query = dir ? "?dir=" + encodeURIComponent(dir) : "";
      const data = await fetchJSON("/api/list" + query);
      state.cwd = data.cwd;
      updateChangedPaths(data.cwd, data.entries, { silent: !!options.silent });
      state.entries = data.entries;
      state.favorites = data.favorites;
      if (options.clearSelection !== false && !options.keepSelection) {
        state.selectedPath = "";
        state.selectedHash = "";
      }
      cwdEl.textContent = shortenDisplayPath(state.cwd);
      cwdEl.dataset.path = state.cwd;
      renderFiles(data.entries);
      renderFavorites();
      statusTextEl.textContent = "Loaded " + data.cwd;
      if (options.historyMode) {
        syncHistory(options.historyMode);
      }
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
      for (const entry of filteredEntries) {
        const button = document.createElement("button");
        const flag = state.fileFlags[entry.path] || "";
        button.className = "file"
          + (entry.path === state.selectedPath ? " active" : "")
          + (flag ? " updated has-flag flag-" + flag : "");
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
        return;
      }
      for (const favorite of state.favorites) {
        const button = document.createElement("button");
        button.className = "favorite" + (favorite === state.cwd ? " active" : "");
        button.dataset.path = favorite;
        button.innerHTML = '<span class="favorite-name"></span>';
        button.querySelector(".favorite-name").textContent = shortenFavoritePath(favorite);
        button.onclick = () => loadDir(favorite, { historyMode: "push" });
        favoritesEl.appendChild(button);
      }
    }

    async function selectFile(path, options = {}) {
      state.selectedPath = path;
      state.selectedHash = options.hash || "";
      if (!state.cwd || !path.startsWith(state.cwd + "/")) {
        await loadDir(path.replace(/\/[^/]*$/, ""), { keepSelection: true });
      }
      renderFiles(state.entries);
      const data = await fetchJSON("/api/file?path=" + encodeURIComponent(path));
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
      renderFiles(state.entries);
      previewTitleEl.textContent = data.name;
      previewMetaEl.textContent = new Date(data.mod_time).toLocaleString() + " · " + humanSize(data.size);
      kindChipEl.textContent = data.kind;
      await renderCurrentView(data);
      if (state.selectedHash) {
        requestAnimationFrame(() => scrollToHash(state.selectedHash));
      } else {
        previewBodyEl.scrollTop = 0;
      }
      statusTextEl.textContent = "Showing " + data.name;
      updateEditorButtons();
      if (options.historyMode) {
        syncHistory(options.historyMode);
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
          const host = document.createElement("div");
          host.className = "mermaid";
          host.textContent = code.textContent;
          code.parentElement.replaceWith(host);
        }
        await mermaid.run({ nodes: previewBodyEl.querySelectorAll(".mermaid") });
        decorateRenderedMarkdown();
        return;
      }
      if (data.kind === "image") {
        previewBodyEl.innerHTML = '<img alt="" src="' + data.raw_url + '&t=' + Date.now() + '" />';
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
      await loadDir(state.cwd, { keepSelection: true });
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
      await fetchJSON("/api/favorites/toggle", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ dir: state.cwd }),
      });
      await loadDir(state.cwd);
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
      const row = event.target.closest(".favorite[data-path]");
      if (!row) return;
      showTooltip(row.dataset.path, event.clientX, event.clientY, { singleLine: true });
    });

    favoritesEl.addEventListener("pointermove", (event) => {
      const row = event.target.closest(".favorite[data-path]");
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
    previewModeButtonEl.onclick = () => setEditorMode("preview");
    editModeButtonEl.onclick = () => {
      if (!canEditKind(state.selectedKind)) return;
      setEditorMode("edit");
    };
    saveButtonEl.onclick = () => saveCurrentFile();
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

    document.getElementById("refreshButton").onclick = () => state.selectedPath
      ? selectFile(state.selectedPath, { hash: state.selectedHash, historyMode: "replace" })
      : loadDir(state.cwd, { historyMode: "replace" });
    document.getElementById("toggleFavorite").onclick = toggleFavorite;
    pathInputEl.addEventListener("keydown", (event) => {
      if (event.key === "Enter") {
        event.preventDefault();
        jumpToPath(pathInputEl.value);
      } else if (event.key === "Escape") {
        pathInputEl.value = "";
        pathInputEl.blur();
      }
    });

    document.addEventListener("keydown", (event) => {
      const lowerKey = event.key.toLowerCase();
      // Cmd/Ctrl+L → focus the "Jump to path" input (URL-bar style).
      if ((event.metaKey || event.ctrlKey) && lowerKey === "l") {
        event.preventDefault();
        pathInputEl.focus();
        pathInputEl.select();
        return;
      }
      const isSaveKey = (event.metaKey || event.ctrlKey) && lowerKey === "s";
      if (!isSaveKey) return;
      if (!state.selectedPath || !canEditKind(state.selectedKind)) return;
      event.preventDefault();
      saveCurrentFile();
    });

    applySidebarLayout();
    updateSortButtons();
    updateEditorButtons();
    setInterval(refreshSelected, 2000);
    setInterval(refreshCurrentDir, 2500);
    restoreRoute(routeFromLocation(), "replace");
  </script>
</body>
</html>`
