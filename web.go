package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
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

type recursiveListEntry struct {
	Dir   string     `json:"dir"`
	Files []webEntry `json:"files"`
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
	mux.HandleFunc("/api/favorites/reorder", s.handleReorderFavorites)
	mux.HandleFunc("/api/resolve", s.handleResolve)
	mux.HandleFunc("/api/usage", s.handleUsage)
	mux.HandleFunc("/api/aliases", s.handleAliases)
	mux.HandleFunc("/api/search", s.handleSearch)
	mux.HandleFunc("/api/list-recursive", s.handleListRecursive)
	mux.HandleFunc("/api/git/remotes", s.handleGitRemotes)
	mux.HandleFunc("/api/git/root", s.handleGitRoot)
	mux.HandleFunc("/api/git/filelog", s.handleGitFileLog)
	mux.HandleFunc("/api/git/show", s.handleGitShow)
	mux.HandleFunc("/api/aidlc", s.handleAidlc)
	mux.HandleFunc("/api/version", s.handleVersion)
	mux.HandleFunc("/api/version/check", s.handleVersionCheck)
	mux.HandleFunc("/api/update", s.handleUpdate)
	mux.HandleFunc("/api/memos", s.handleMemos)
	mux.HandleFunc("/api/memos/save", s.handleSaveMemos)
	mux.HandleFunc("/api/memos/delete", s.handleDeleteMemo)
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
	md := usageWebMD
	if r.URL.Query().Get("lang") == "ko" {
		md = usageWebKoMD
	}
	_, _ = w.Write([]byte(md))
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

// Memos: a global notebook of free-form notes shared across all files and
// browser sessions. Stored in their own JSON file alongside favorites/aliases
// so they survive restarts and re-sync when a new session opens. Each memo
// carries a client-generated id and createdAt/updatedAt timestamps; merges are
// last-write-wins per id keyed on updatedAt.

type memo struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Body          string `json:"body"`
	Pinned        bool   `json:"pinned"`
	SourcePath    string `json:"sourcePath,omitempty"`    // file the memo was captured from
	SourceHash    string `json:"sourceHash,omitempty"`    // nearest heading id (backlink anchor)
	SourceHeading string `json:"sourceHeading,omitempty"` // that heading's text (for display)
	SourceQuote   string `json:"sourceQuote,omitempty"`   // selected text snippet, highlighted on backlink
	CreatedAt     string `json:"createdAt"`
	UpdatedAt     string `json:"updatedAt"`
}

func (s *webServer) memosPath() string {
	return filepath.Join(s.appRoot, memosFileName)
}

func (s *webServer) loadMemos() []memo {
	data, err := os.ReadFile(s.memosPath())
	if err != nil {
		return nil
	}
	var memos []memo
	if err := json.Unmarshal(data, &memos); err != nil {
		return nil
	}
	out := make([]memo, 0, len(memos))
	for _, m := range memos {
		if m.ID == "" {
			continue
		}
		out = append(out, m)
	}
	return out
}

func (s *webServer) saveMemos(memos []memo) error {
	if memos == nil {
		memos = []memo{}
	}
	data, err := json.MarshalIndent(memos, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.memosPath(), data, 0o644)
}

// mergeMemos upserts incoming memos into existing using last-write-wins per id:
// an incoming memo replaces an existing one only when its UpdatedAt sorts later
// (RFC3339 strings compare lexicographically). Unknown ids are appended.
// Existing memos absent from incoming are kept — save never deletes.
func mergeMemos(existing, incoming []memo) []memo {
	index := make(map[string]int, len(existing))
	merged := make([]memo, len(existing))
	copy(merged, existing)
	for i := range merged {
		index[merged[i].ID] = i
	}
	for _, in := range incoming {
		if in.ID == "" {
			continue
		}
		if i, ok := index[in.ID]; ok {
			if in.UpdatedAt >= merged[i].UpdatedAt {
				merged[i] = in
			}
		} else {
			index[in.ID] = len(merged)
			merged = append(merged, in)
		}
	}
	return merged
}

// sortMemosByUpdatedDesc orders memos newest-updated first for display.
func sortMemosByUpdatedDesc(memos []memo) {
	sort.SliceStable(memos, func(i, j int) bool {
		return memos[i].UpdatedAt > memos[j].UpdatedAt
	})
}

func (s *webServer) handleMemos(w http.ResponseWriter, r *http.Request) {
	memos := s.loadMemos()
	sortMemosByUpdatedDesc(memos)
	if memos == nil {
		memos = []memo{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"memos": memos})
}

func (s *webServer) handleSaveMemos(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload struct {
		Memos []memo `json:"memos"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	merged := mergeMemos(s.loadMemos(), payload.Memos)
	if err := s.saveMemos(merged); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sortMemosByUpdatedDesc(merged)
	s.writeJSON(w, http.StatusOK, map[string]any{"memos": merged})
}

func (s *webServer) handleDeleteMemo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	memos := s.loadMemos()
	out := memos[:0]
	for _, m := range memos {
		if m.ID == payload.ID {
			continue
		}
		out = append(out, m)
	}
	if err := s.saveMemos(out); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sortMemosByUpdatedDesc(out)
	s.writeJSON(w, http.StatusOK, map[string]any{"memos": out})
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
	case ".txt", ".log", ".csv", ".tsv",
		".go", ".py", ".pyw", ".js", ".mjs", ".cjs", ".jsx", ".gs", ".ts", ".tsx",
		".sh", ".bash", ".zsh", ".ksh", ".fish",
		".ps1", ".psm1", ".bat", ".cmd",
		".yaml", ".yml", ".json", ".toml", ".ini", ".conf", ".cfg", ".env", ".properties",
		".sql",
		".c", ".h", ".cpp", ".cc", ".cxx", ".hpp", ".hxx", ".cs",
		".java", ".jsp", ".gradle", ".kt", ".kts", ".scala", ".sc", ".groovy",
		".rs", ".rb", ".rake", ".php", ".swift", ".dart",
		".lua", ".pl", ".pm", ".r",
		".xml", ".xhtml", ".plist", ".pom", ".csproj",
		".css", ".scss", ".sass", ".less",
		".hs", ".ex", ".exs", ".erl", ".clj", ".fs", ".fsx", ".ml", ".mli",
		".jl", ".cr", ".coffee", ".tf", ".tfvars", ".proto",
		".diff", ".patch", ".dockerfile", ".vim", ".cmake", ".mk":
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
	case ".md", ".markdown", ".mdx",
		".txt", ".log", ".csv", ".tsv",
		".go", ".py", ".pyw", ".js", ".mjs", ".cjs", ".jsx", ".gs", ".ts", ".tsx",
		".sh", ".bash", ".zsh", ".ksh", ".fish",
		".ps1", ".psm1", ".bat", ".cmd",
		".yaml", ".yml", ".json", ".toml", ".ini", ".conf", ".cfg", ".env", ".properties",
		".sql",
		".c", ".h", ".cpp", ".cc", ".cxx", ".hpp", ".hxx", ".cs",
		".java", ".jsp", ".gradle", ".kt", ".kts", ".scala", ".sc", ".groovy",
		".rs", ".rb", ".rake", ".php", ".swift", ".dart",
		".lua", ".pl", ".pm", ".r",
		".xml", ".xhtml", ".plist", ".pom", ".csproj",
		".css", ".scss", ".sass", ".less",
		".hs", ".ex", ".exs", ".erl", ".clj", ".fs", ".fsx", ".ml", ".mli",
		".jl", ".cr", ".coffee", ".tf", ".tfvars", ".proto",
		".diff", ".patch", ".dockerfile", ".vim", ".cmake", ".mk":
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
		// Prepend so a just-added favorite shows at the TOP of the list; the
		// user can still reorder via drag (/api/favorites/reorder).
		favorites = append([]string{dir}, favorites...)
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

// handleReorderFavorites persists a user-defined favorites order. The incoming
// order is filtered to currently-saved favorites; any saved favorite missing
// from the payload is appended (so a stale/partial client can't drop entries).
func (s *webServer) handleReorderFavorites(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload struct {
		Order []string `json:"order"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	current := s.loadFavorites()
	inCurrent := make(map[string]bool, len(current))
	for _, p := range current {
		inCurrent[p] = true
	}
	seen := make(map[string]bool, len(current))
	out := make([]string, 0, len(current))
	for _, p := range payload.Order {
		if inCurrent[p] && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, p := range current { // keep any favorite the client omitted
		if !seen[p] {
			out = append(out, p)
		}
	}
	if err := s.saveFavorites(out); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"favorites": out})
}

type gitRemote struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	WebURL string `json:"web_url"`
}

// gitToWebURL converts a git remote URL (ssh / git+ssh / git:// / https) to
// a browser-openable https URL. Returns "" when conversion fails so the UI
// can hide the button for non-web remotes (e.g. local file paths).
func gitToWebURL(u string) string {
	u = strings.TrimSpace(u)
	u = strings.TrimSuffix(u, ".git")
	switch {
	case strings.HasPrefix(u, "git@"):
		// git@github.com:user/repo → https://github.com/user/repo
		rest := strings.TrimPrefix(u, "git@")
		i := strings.Index(rest, ":")
		if i < 0 {
			return ""
		}
		return "https://" + rest[:i] + "/" + rest[i+1:]
	case strings.HasPrefix(u, "ssh://"):
		// ssh://git@github.com/user/repo → https://github.com/user/repo
		rest := strings.TrimPrefix(u, "ssh://")
		rest = strings.TrimPrefix(rest, "git@")
		return "https://" + rest
	case strings.HasPrefix(u, "git://"):
		return "https://" + strings.TrimPrefix(u, "git://")
	case strings.HasPrefix(u, "http://"), strings.HasPrefix(u, "https://"):
		return u
	default:
		return ""
	}
}

// handleGitRemotes parses `git remote -v` for the given dir and returns a
// deduplicated list with browser-openable URLs. Empty array when the dir is
// not a git repository, git isn't on PATH, or the command fails.
func (s *webServer) handleGitRemotes(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("dir")
	if dir == "" {
		dir = s.startDir
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		http.Error(w, "invalid dir", http.StatusBadRequest)
		return
	}
	out := []gitRemote{}
	// Skip the `.git` existence check: `git remote -v` itself walks up
	// parent directories to find the repo root, so subfolders of a
	// repository work too. A non-git path returns exit-128 which we
	// already swallow as "no remotes".
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", abs, "remote", "-v")
	raw, err := cmd.Output()
	if err != nil {
		s.writeJSON(w, http.StatusOK, out)
		return
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		if seen[parts[0]] {
			continue
		}
		seen[parts[0]] = true
		out = append(out, gitRemote{
			Name:   parts[0],
			URL:    parts[1],
			WebURL: gitToWebURL(parts[1]),
		})
	}
	s.writeJSON(w, http.StatusOK, out)
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
  <script src="https://cdn.jsdelivr.net/gh/highlightjs/cdn-release@11/build/highlight.min.js"></script>
  <!-- KaTeX: math rendering for $…$, $$…$$, \(…\), \[…\] -->
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/katex@0.16.11/dist/katex.min.css">
  <script defer src="https://cdn.jsdelivr.net/npm/katex@0.16.11/dist/katex.min.js"></script>
  <script defer src="https://cdn.jsdelivr.net/npm/katex@0.16.11/dist/contrib/auto-render.min.js"></script>
  <!-- Common bundle above (≈38 languages); load a curated set of extras
       so docs covering ops / infra / less-mainstream languages light up
       too. Each script self-registers with hljs. -->
  <script src="https://cdn.jsdelivr.net/gh/highlightjs/cdn-release@11/build/languages/dockerfile.min.js"></script>
  <script src="https://cdn.jsdelivr.net/gh/highlightjs/cdn-release@11/build/languages/nginx.min.js"></script>
  <script src="https://cdn.jsdelivr.net/gh/highlightjs/cdn-release@11/build/languages/powershell.min.js"></script>
  <script src="https://cdn.jsdelivr.net/gh/highlightjs/cdn-release@11/build/languages/dart.min.js"></script>
  <script src="https://cdn.jsdelivr.net/gh/highlightjs/cdn-release@11/build/languages/scala.min.js"></script>
  <script src="https://cdn.jsdelivr.net/gh/highlightjs/cdn-release@11/build/languages/groovy.min.js"></script>
  <script src="https://cdn.jsdelivr.net/gh/highlightjs/cdn-release@11/build/languages/haskell.min.js"></script>
  <script src="https://cdn.jsdelivr.net/gh/highlightjs/cdn-release@11/build/languages/elixir.min.js"></script>
  <script src="https://cdn.jsdelivr.net/gh/highlightjs/cdn-release@11/build/languages/erlang.min.js"></script>
  <script src="https://cdn.jsdelivr.net/gh/highlightjs/cdn-release@11/build/languages/clojure.min.js"></script>
  <script src="https://cdn.jsdelivr.net/gh/highlightjs/cdn-release@11/build/languages/protobuf.min.js"></script>
  <script src="https://cdn.jsdelivr.net/gh/highlightjs/cdn-release@11/build/languages/properties.min.js"></script>
  <script src="https://cdn.jsdelivr.net/gh/highlightjs/cdn-release@11/build/languages/twig.min.js"></script>
  <script src="https://cdn.jsdelivr.net/gh/highlightjs/cdn-release@11/build/languages/handlebars.min.js"></script>
  <script src="https://cdn.jsdelivr.net/gh/highlightjs/cdn-release@11/build/languages/vim.min.js"></script>
  <script src="https://cdn.jsdelivr.net/gh/highlightjs/cdn-release@11/build/languages/lisp.min.js"></script>
  <script src="https://cdn.jsdelivr.net/gh/highlightjs/cdn-release@11/build/languages/scheme.min.js"></script>
  <script src="https://cdn.jsdelivr.net/gh/highlightjs/cdn-release@11/build/languages/fsharp.min.js"></script>
  <script src="https://cdn.jsdelivr.net/gh/highlightjs/cdn-release@11/build/languages/ocaml.min.js"></script>
  <script src="https://cdn.jsdelivr.net/gh/highlightjs/cdn-release@11/build/languages/julia.min.js"></script>
  <script src="https://cdn.jsdelivr.net/gh/highlightjs/cdn-release@11/build/languages/crystal.min.js"></script>
  <script src="https://cdn.jsdelivr.net/gh/highlightjs/cdn-release@11/build/languages/coffeescript.min.js"></script>
  <link id="hljs-theme-dark" rel="stylesheet" href="https://cdn.jsdelivr.net/gh/highlightjs/cdn-release@11/build/styles/github-dark.min.css">
  <link id="hljs-theme-light" rel="stylesheet" href="https://cdn.jsdelivr.net/gh/highlightjs/cdn-release@11/build/styles/github.min.css" disabled>
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
      --file-meta-width: 4.5rem;
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
      margin-bottom: 10px;
    }
    .brand-mark {
      width: 28px;
      height: 28px;
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
    #cwd.path-chip {
      display: inline-block;
      max-width: 100%;
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
      padding: 5px 10px;
      border-radius: 999px;
      background: color-mix(in oklab, var(--panel-2) 70%, transparent);
      border: 1px solid color-mix(in oklab, var(--line) 55%, transparent);
      color: color-mix(in oklab, var(--text) 75%, var(--muted));
      font-size: 12px;
      font-family: ui-monospace, SFMono-Regular, monospace;
      margin: 0;
    }
    .git-remote-link {
      display: inline-flex;
      align-items: center;
      gap: 4px;
      margin-top: 6px;
      font-size: 12px;
      text-decoration: none;
    }
    .searchbox {
      margin-top: 10px;
    }
    .search-input {
      width: 100%;
      border: 1px solid color-mix(in oklab, var(--line) 60%, transparent);
      background: color-mix(in oklab, var(--panel-2) 86%, transparent);
      color: var(--text);
      border-radius: 10px;
      padding: 8px 12px;
      font: inherit;
      font-size: 13px;
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
    .fav-dragging {
      opacity: 0.5;
      outline: 2px dashed color-mix(in oklab, var(--accent) 60%, transparent);
      outline-offset: -2px;
      border-radius: 8px;
    }
    /* Absolute so it never disturbs the row's flex/grid layout. */
    .fav-reorderable { position: relative; padding-left: 20px; }
    .fav-grip {
      position: absolute;
      left: 0; top: 0; bottom: 0;
      display: flex;
      align-items: center;
      justify-content: center;
      width: 20px;
      color: var(--muted);
      font-size: 14px;
      line-height: 1;
      cursor: grab;
      user-select: none;
      touch-action: none;          /* pointer-drag owns the gesture (no scroll/native DnD) */
      opacity: 0.5;                /* always visible so the handle is discoverable */
      transition: opacity 120ms ease, color 120ms ease;
      z-index: 2;
    }
    .fav-reorderable:hover .fav-grip { opacity: 0.85; }
    .fav-grip:hover { opacity: 1; color: var(--accent); }
    .fav-grip:active { cursor: grabbing; }
    .fav-move {
      flex: 0 0 auto;
      display: flex;
      flex-direction: column;
      align-items: center;
      justify-content: center;
      gap: 0;
      opacity: 0;
      transition: opacity 120ms ease;
    }
    .favorite-row:hover .fav-move,
    .popup-item:hover .fav-move { opacity: 1; }
    .fav-move-btn {
      border: 0;
      background: transparent;
      color: var(--muted);
      cursor: pointer;
      font-size: 8px;
      line-height: 1.1;
      padding: 0 4px;
      border-radius: 3px;
    }
    .fav-move-btn:hover { color: var(--accent); background: color-mix(in oklab, var(--line) 50%, transparent); }
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
      padding: 6px 12px;
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
    /* AI-DLC "folder/file" labels: muted path, bold filename. */
    .file-name .fn-dir { color: var(--muted); font-weight: 400; }
    .file-name .fn-base { color: var(--text); font-weight: 700; }
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
      padding: 8px 12px 9px;
    }
    .section-head {
      display: flex;
      align-items: center;
      justify-content: space-between;
      margin-bottom: 6px;
      padding: 0 8px;
    }
    .section-title {
      color: var(--accent);
      font-weight: 600;
      font-size: 12px;
      letter-spacing: .12em;
      text-transform: uppercase;
    }
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
    .section-toggle .section-title { color: var(--accent); font-weight: 600; font-size: 12px; letter-spacing: .12em; text-transform: uppercase; }
    /* display:inline-flex below would otherwise defeat the [hidden] attribute
       (author display wins over the UA [hidden]{display:none}), keeping the
       switch visible even when no aidlc-docs folder exists. Restore hiding. */
    .aidlc-toggle[hidden] { display: none; }
    .aidlc-toggle {
      flex: 0 0 auto;
      display: inline-flex;
      align-items: center;
      gap: 6px;
      border: 1px solid var(--line);
      background: transparent;
      color: var(--muted);
      font-size: 10px;
      font-weight: 700;
      letter-spacing: .08em;
      text-transform: uppercase;
      padding: 3px 8px 3px 5px;
      border-radius: 999px;
      cursor: pointer;
      white-space: nowrap;
      transition: background 130ms ease, color 130ms ease, border-color 130ms ease;
    }
    /* Sliding switch: knob sits left + track grey when OFF, slides right + track
       accent-2 when ON. Paired with the OFF/ON word for an unambiguous state. */
    .aidlc-switch {
      position: relative;
      flex: 0 0 auto;
      width: 22px;
      height: 12px;
      border-radius: 999px;
      background: color-mix(in oklab, var(--muted) 55%, transparent);
      transition: background 140ms ease;
    }
    .aidlc-knob {
      position: absolute;
      top: 1px;
      left: 1px;
      width: 10px;
      height: 10px;
      border-radius: 50%;
      background: #fff;
      box-shadow: 0 1px 2px rgba(0, 0, 0, .35);
      transition: transform 140ms ease;
    }
    .aidlc-toggle-state { font-variant-numeric: tabular-nums; opacity: .85; }
    .aidlc-toggle:hover { border-color: color-mix(in oklab, var(--accent-2) 40%, var(--line)); }
    .aidlc-toggle.active {
      color: var(--accent-2);
      border-color: color-mix(in oklab, var(--accent-2) 60%, var(--line));
      background: color-mix(in oklab, var(--accent-2) 16%, var(--panel-2));
    }
    .aidlc-toggle.active .aidlc-switch { background: var(--accent-2); }
    .aidlc-toggle.active .aidlc-knob { transform: translateX(10px); }
    .aidlc-toggle.active .aidlc-toggle-state { opacity: 1; }

    /* "업데이트 내역" toggle — same sliding-switch idiom as the AI-DLC toggle. */
    .upd-toggle[hidden] { display: none; }
    .upd-toggle {
      flex: 0 0 auto; display: inline-flex; align-items: center; gap: 6px;
      border: 1px solid var(--line); background: transparent; color: var(--muted);
      font-size: 10px; font-weight: 700; letter-spacing: .04em;
      padding: 4px 9px 4px 6px; border-radius: 999px; cursor: pointer; white-space: nowrap;
      transition: background 130ms ease, color 130ms ease, border-color 130ms ease;
    }
    .upd-switch { position: relative; flex: 0 0 auto; width: 22px; height: 12px; border-radius: 999px; background: color-mix(in oklab, var(--muted) 55%, transparent); transition: background 140ms ease; }
    .upd-knob { position: absolute; top: 1px; left: 1px; width: 10px; height: 10px; border-radius: 50%; background: #fff; box-shadow: 0 1px 2px rgba(0,0,0,.35); transition: transform 140ms ease; }
    .upd-toggle:hover { border-color: color-mix(in oklab, var(--accent) 40%, var(--line)); }
    .upd-toggle.active { color: var(--accent); border-color: color-mix(in oklab, var(--accent) 60%, var(--line)); background: color-mix(in oklab, var(--accent) 14%, var(--panel-2)); }
    .upd-toggle.active .upd-switch { background: var(--accent); }
    .upd-toggle.active .upd-knob { transform: translateX(10px); }
    .upd-nav { display: inline-flex; align-items: center; gap: 4px; }
    .upd-nav[hidden] { display: none; }
    .upd-nav .action { padding: 5px 7px; }
    .upd-nav-count { font-size: 10.5px; color: var(--muted); font-variant-numeric: tabular-nums; min-width: 30px; text-align: center; white-space: nowrap; }
    /* "Changes" toggle + base picker + nav, grouped in one pill like the view-mode seg. */
    .upd-seg[hidden] { display: none; }
    .upd-seg { align-items: center; gap: 3px; padding: 3px 5px; }
    .upd-seg .upd-toggle { border: none; background: transparent; padding: 3px 6px; }
    .upd-seg .upd-toggle:hover { border-color: transparent; background: color-mix(in oklab, var(--text) 7%, transparent); }
    .upd-seg .upd-toggle.active { border-color: transparent; background: transparent; }
    .upd-base-btn[hidden] { display: none; }
    .upd-base-btn { display: inline-flex; align-items: center; gap: 2px; appearance: none; border: none; background: transparent; color: var(--muted); cursor: pointer; padding: 3px 6px; border-radius: 999px; font-size: 10.5px; white-space: nowrap; transition: background 130ms ease, color 130ms ease; }
    .upd-base-btn:hover { background: color-mix(in oklab, var(--text) 7%, transparent); color: var(--text); }
    .upd-base-btn.custom { color: var(--accent); }
    .upd-base-label { font-variant-numeric: tabular-nums; }
    .upd-base-label:empty { display: none; }
    .upd-base-caret { font-size: 9px; opacity: .8; }
    .upd-base-pop[hidden] { display: none; }
    .upd-base-pop { position: absolute; z-index: 60; min-width: 290px; max-width: 380px; padding: 6px; border-radius: 12px; border: 1px solid var(--line); background: var(--panel); box-shadow: 0 10px 34px rgba(0,0,0,.20); font-size: 12px; }
    .upd-base-pop-title { font-size: 10px; color: var(--muted); text-transform: uppercase; letter-spacing: .05em; padding: 4px 8px 6px; }
    .upd-base-list { max-height: 300px; overflow-y: auto; }
    .upd-base-item { display: flex; gap: 8px; align-items: baseline; padding: 6px 8px; border-radius: 8px; cursor: pointer; }
    .upd-base-item:hover { background: color-mix(in oklab, var(--accent) 12%, transparent); }
    .upd-base-item.active { background: color-mix(in oklab, var(--accent) 16%, transparent); color: var(--accent); }
    .upd-base-item .ub-date { color: var(--muted); font-variant-numeric: tabular-nums; flex: 0 0 auto; }
    .upd-base-item.active .ub-date { color: inherit; }
    .upd-base-item .ub-hash { color: var(--muted); font-family: ui-monospace, monospace; font-size: 11px; flex: 0 0 auto; }
    .upd-base-item.active .ub-hash { color: inherit; }
    .upd-base-item .ub-subj { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: 1 1 auto; min-width: 0; }
    .upd-base-date-row { display: flex; align-items: center; gap: 8px; padding: 8px; border-top: 1px solid var(--line); margin-top: 4px; }
    .upd-base-date-label { color: var(--muted); font-size: 11px; flex: 0 0 auto; }
    .upd-base-date { flex: 1 1 auto; padding: 4px 6px; border-radius: 8px; border: 1px solid var(--line); background: var(--panel-2); color: var(--text); font: inherit; }
    .preview-body .upd-flash { animation: updFlash 0.95s ease; border-radius: 4px; }
    @keyframes updFlash {
      0% { outline: 2px solid color-mix(in oklab, var(--accent) 75%, transparent); outline-offset: 2px; }
      100% { outline: 2px solid transparent; outline-offset: 2px; }
    }

    /* Inline update-diff annotations on the rendered preview. */
    .upd-note { margin: 0 0 12px; padding: 6px 10px; border-radius: 8px; font-size: 12px;
      color: var(--muted); background: color-mix(in oklab, var(--accent) 10%, transparent);
      border: 1px solid color-mix(in oklab, var(--accent) 30%, var(--line)); }
    .upd-add { background: color-mix(in oklab, #3fb950 30%, transparent); border-radius: 2px; }
    .upd-del { background: color-mix(in oklab, #e0533f 26%, transparent); color: inherit; text-decoration: line-through; border-radius: 2px; }
    mark.upd-add, mark.upd-del { color: inherit; padding: 0 1px; }
    /* Whole added block / line. */
    .preview-body .upd-add-block { background: color-mix(in oklab, #3fb950 14%, transparent); box-shadow: inset 3px 0 0 color-mix(in oklab, #3fb950 60%, transparent); border-radius: 4px; }
    /* Inserted "removed" block (entire deleted line/paragraph). */
    .upd-removed-block { background: color-mix(in oklab, #e0533f 12%, transparent); box-shadow: inset 3px 0 0 color-mix(in oklab, #e0533f 60%, transparent); border-radius: 4px; text-decoration: line-through; color: color-mix(in oklab, var(--text) 60%, var(--muted)); opacity: .85; margin: 4px 0; padding: 2px 6px; }
    /* Per-line code change rows in the Changes overlay (word marks reuse .upd-add/.upd-del). */
    .preview-body tr.upd-code-chg > td.hljs-ln-code { background: color-mix(in oklab, #d29922 18%, transparent); }
    .preview-body tr.upd-code-add > td.hljs-ln-code { background: color-mix(in oklab, #3fb950 18%, transparent); }
    .preview-body tr.upd-code-removed-line > td.hljs-ln-code { background: color-mix(in oklab, #e0533f 12%, transparent); text-decoration: line-through; color: color-mix(in oklab, var(--text) 60%, var(--muted)); opacity: .85; }
    .preview-body tr.upd-code-removed-line > td.hljs-ln-numbers { color: color-mix(in oklab, #e0533f 80%, var(--muted)); }
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
    .lang-toggle { min-width: 34px; justify-content: center; font-weight: 700; font-size: 11px; letter-spacing: .04em; }
    /* Favorite toggle: accent "Add" vs red "Remove" — element+2-class
       selectors beat the generic .action.active rule below. */
    button.action.fav-add {
      color: var(--accent);
      border-color: color-mix(in oklab, var(--accent) 50%, var(--line));
      background: color-mix(in oklab, var(--accent) 12%, var(--panel-2));
    }
    button.action.fav-add:hover {
      background: color-mix(in oklab, var(--accent) 22%, var(--panel-2));
      border-color: var(--accent);
    }
    button.action.fav-remove {
      color: #c0392b;
      border-color: color-mix(in oklab, #c0392b 45%, var(--line));
      background: color-mix(in oklab, #c0392b 10%, var(--panel-2));
    }
    button.action.fav-remove:hover {
      background: color-mix(in oklab, #c0392b 18%, var(--panel-2));
      border-color: #c0392b;
    }
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
      width: 30px;
      height: 30px;
      min-width: 30px;
      padding: 0;
      display: inline-flex;
      align-items: center;
      justify-content: center;
      font-size: 16px;
      font-weight: 700;
      line-height: 1;
      flex: 0 0 auto;
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
    .split-view {
      display: flex;
      flex-direction: row;
      gap: 12px;
      width: 100%;
      height: 100%;
      min-height: 0;
    }
    .split-editor {
      flex: 1 1 50%;
      min-width: 240px;
      display: flex;
      position: relative;
    }
    /* Overlay band positioned by a hidden CLONE TEXTAREA's scrollHeight
       — only a real textarea wraps identically to the live one (div
       mirrors drifted by sub-pixel widths and metric differences). */
    .editor-line-highlight {
      position: absolute;
      pointer-events: none;
      z-index: 2;
      background: color-mix(in oklab, var(--accent) 22%, transparent);
      box-shadow: 0 0 0 1px color-mix(in oklab, var(--accent) 40%, transparent) inset;
      border-radius: 3px;
    }
    .editor-line-highlight[hidden] { display: none; }
    .split-editor textarea.editor {
      flex: 1 1 auto;
      width: 100%;
      height: 100%;
      resize: none;
    }
    .split-preview {
      flex: 1 1 50%;
      min-width: 0;
      overflow-y: auto;
      padding-right: 4px;
    }
    .split-preview [data-source-line] { transition: background 0.18s ease; border-radius: 6px; }
    .split-preview .source-line-active {
      background: color-mix(in oklab, var(--accent) 14%, transparent);
      box-shadow: 0 0 0 2px color-mix(in oklab, var(--accent) 45%, transparent) inset;
    }
    /* Table rows have their own zebra stripes via more-specific
       selectors; override them so the active row is unambiguous. */
    .split-preview tr.source-line-active,
    .split-preview .preview-body tr.source-line-active {
      background: color-mix(in oklab, var(--accent) 22%, transparent) !important;
      box-shadow: 0 0 0 2px color-mix(in oklab, var(--accent) 55%, transparent) inset;
    }
    /* Individual list items get a tighter pill so the bullet stays
       visible and the highlight doesn't drown the surrounding text. */
    .split-preview li.source-line-active {
      background: color-mix(in oklab, var(--accent) 18%, transparent);
      box-shadow: 0 0 0 2px color-mix(in oklab, var(--accent) 50%, transparent) inset;
      border-radius: 4px;
      padding-inline: 2px;
    }
    @media (max-width: 760px) {
      .split-view { flex-direction: column; }
      .split-editor, .split-preview { flex: 1 1 auto; }
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
      padding: 14px 16px;
      border-radius: 10px;
      background: var(--code);
      border: 0;
    }
    /* hljs's github / github-dark themes paint their own #ffffff /
       #0d1117 background on .hljs, which would clash with the outer
       <pre>'s --code. Suppress it so the whole block is a single tone. */
    .preview-body pre code.hljs {
      background: transparent !important;
      padding: 0 !important;
    }
    /* highlightjs-line-numbers.js gutter — keep digits dim + monospace,
       and align the code column to a comfortable left margin. */
    .preview-body table.hljs-ln {
      border-collapse: collapse;
      width: 100%;
      margin: 0;
    }
    .preview-body table.hljs-ln td {
      padding: 0;
      border: 0;
      background: transparent;
      vertical-align: top;
    }
    .preview-body .hljs-ln-numbers {
      user-select: none;
      text-align: right;
      padding-right: 12px !important;
      color: color-mix(in oklab, var(--text) 35%, var(--muted)) !important;
      border-right: 1px solid color-mix(in oklab, var(--line) 25%, transparent);
      width: 1%;
      white-space: nowrap;
      font-variant-numeric: tabular-nums;
    }
    .preview-body .hljs-ln-code {
      padding-left: 12px !important;
      white-space: pre;
      tab-size: 4;
      -moz-tab-size: 4;
    }
    /* Standalone code-file preview (.c/.java/.py/etc.) takes the whole
       viewport — let it fill the body and wrap nicely. */
    .preview-body > .code-wrap.code-file {
      margin: 0;
    }
    .preview-body > .code-wrap.code-file > pre {
      max-height: none;
      border-radius: 10px;
    }
    .preview-body pre code {
      font-family: ui-monospace, SFMono-Regular, "JetBrains Mono", "Fira Code", Menlo, monospace;
      font-size: 0.82em;
      line-height: 1.55;
    }
    .preview-body :not(pre) > code {
      font-family: ui-monospace, SFMono-Regular, monospace;
      font-size: 0.9em;
      padding: 1px 5px;
      border-radius: 5px;
      background: color-mix(in oklab, var(--code) 90%, transparent);
      border: 1px solid color-mix(in oklab, var(--line) 65%, transparent);
    }
    .preview-body code { font-family: ui-monospace, SFMono-Regular, monospace; }
    /* Wrapper around fenced code blocks: lets us float a copy button
       in the top-right corner without disturbing scroll on the <pre>. */
    .code-wrap { position: relative; }
    /* Reserve a strip at the top of every wrapped <pre> for the language
       tag + copy button so the first line of code never collides with
       them — even on tightly-wrapped diagrams. */
    .code-wrap > pre { padding-top: 30px; }
    .code-wrap .code-copy-btn {
      position: absolute;
      top: 6px;
      right: 8px;
      border: 1px solid color-mix(in oklab, var(--line) 85%, transparent);
      background: color-mix(in oklab, var(--panel-2) 92%, transparent);
      color: var(--text);
      font-size: 11px;
      padding: 4px 8px;
      border-radius: 8px;
      cursor: pointer;
      opacity: 0;
      transition: opacity 0.15s ease;
      box-shadow: 0 2px 6px rgba(0,0,0,0.15);
      backdrop-filter: blur(4px);
      z-index: 1;
    }
    .code-wrap:hover .code-copy-btn,
    .code-wrap:focus-within .code-copy-btn { opacity: 1; }
    .code-wrap .code-copy-btn:hover {
      background: color-mix(in oklab, var(--accent) 18%, var(--panel-2));
    }
    .code-wrap .code-copy-btn.copied {
      background: color-mix(in oklab, oklch(0.7 0.18 150) 30%, var(--panel-2));
      border-color: color-mix(in oklab, oklch(0.7 0.18 150) 60%, transparent);
    }
    .code-wrap .code-lang-tag {
      position: absolute;
      top: 8px;
      left: 14px;
      font-size: 10px;
      font-weight: 600;
      letter-spacing: 0.04em;
      text-transform: uppercase;
      color: color-mix(in oklab, var(--text) 55%, var(--muted));
      pointer-events: none;
      opacity: 0.7;
      z-index: 1;
    }
    #previewTitle, #previewMeta {
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
    }
    #previewTitle.copyable { cursor: pointer; }
    #previewTitle.copyable:hover {
      text-decoration: underline;
      text-decoration-style: dotted;
      text-underline-offset: 3px;
    }
    .preview-body h1, .preview-body h2, .preview-body h3 { line-height: 1.15; }
    .preview-body blockquote {
      margin: 0;
      padding-left: 16px;
      border-left: 3px solid color-mix(in oklab, var(--accent) 40%, transparent);
      color: color-mix(in oklab, var(--text) 78%, var(--muted));
    }
    /* Markdown tables — give them an explicit grid so they stop looking
       like loose runs of text. Header row picks up the accent tint;
       body rows zebra-stripe for legibility. */
    .preview-body table {
      border-collapse: separate;
      border-spacing: 0;
      width: max-content;
      max-width: 100%;
      margin: 10px 0;
      border: 1px solid color-mix(in oklab, var(--line) 85%, transparent);
      border-radius: 10px;
      overflow: hidden;
      font-size: 0.95em;
    }
    .preview-body table th,
    .preview-body table td {
      padding: 8px 12px;
      border-right: 1px solid color-mix(in oklab, var(--line) 60%, transparent);
      border-bottom: 1px solid color-mix(in oklab, var(--line) 60%, transparent);
      text-align: left;
      vertical-align: top;
    }
    .preview-body table th:last-child,
    .preview-body table td:last-child { border-right: none; }
    .preview-body table tr:last-child td { border-bottom: none; }
    .preview-body table thead th {
      background: color-mix(in oklab, var(--accent) 18%, var(--panel));
      color: var(--text);
      font-weight: 600;
      border-bottom: 1px solid color-mix(in oklab, var(--line) 85%, transparent);
    }
    .preview-body table tbody tr:nth-child(odd) {
      background: color-mix(in oklab, var(--panel) 92%, transparent);
    }
    .preview-body table tbody tr:nth-child(even) {
      background: color-mix(in oklab, var(--panel) 80%, var(--code));
    }
    .preview-body table tbody tr:hover {
      background: color-mix(in oklab, var(--accent) 12%, var(--panel));
    }
    .mermaid {
      overflow: auto;
      padding: 12px;
      border-radius: 16px;
      background: white;
      position: relative;
    }
    /* Defensive height-cap kill for tall mermaids in preview — some
       browsers or generic SVG rules can otherwise clip the SVG. The
       overflow:visible matters too: mermaid (especially with
       htmlLabels:false + SVG-native text) sometimes underestimates the
       viewBox height by a few rows, and SVG defaults to clipping at
       viewBox bounds. visible lets the spilled content draw — which
       is what the lightbox already does. */
    .preview-body .mermaid,
    .preview-body .mermaid-wrap {
      max-height: none !important;
      height: auto !important;
      overflow: visible !important;
    }
    .preview-body .mermaid > svg {
      max-height: none !important;
      height: auto !important;
      overflow: visible !important;
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
    /* Default (no Alt held) inside the lightbox: aggressively block text
       selection. The plain user-select:none on .lightbox is inherited by
       the stage, but some browsers still let SVG <text> become selectable
       via dragging — !important on every descendant ends the argument. */
    body:not(.alt-select-mode) .lightbox-stage,
    body:not(.alt-select-mode) .lightbox-stage * {
      user-select: none !important;
      -webkit-user-select: none !important;
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
    .popup-head-actions {
      display: flex;
      align-items: center;
      gap: 8px;
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

    /* ---- Mermaid Playground modal ---- */
    .mermaid-lab-modal {
      position: fixed;
      inset: 0;
      background: rgba(0, 0, 0, 0.55);
      backdrop-filter: blur(6px);
      -webkit-backdrop-filter: blur(6px);
      z-index: 1900;
      display: flex;
      align-items: center;
      justify-content: center;
      padding: 32px;
    }
    .mermaid-lab-modal[hidden] { display: none; }
    .mermaid-lab-card {
      width: min(1200px, 95vw);
      height: min(820px, 90vh);
      background: var(--panel);
      border: 1px solid var(--line);
      border-radius: 12px;
      box-shadow: 0 24px 80px rgba(0,0,0,0.5);
      display: flex;
      flex-direction: column;
      overflow: hidden;
    }
    .mermaid-lab-head {
      display: flex;
      align-items: center;
      justify-content: space-between;
      padding: 14px 16px 10px;
      border-bottom: 1px solid var(--line);
    }
    .mermaid-lab-title {
      font-weight: 700;
      color: var(--accent);
      font-size: 14px;
    }
    .mermaid-lab-head-actions {
      display: flex;
      align-items: center;
      gap: 8px;
    }
    .mermaid-lab-body {
      flex: 1 1 auto;
      display: flex;
      min-height: 0;
    }
    .mermaid-lab-pane {
      flex: 1 1 50%;
      min-width: 0;
      overflow: auto;
    }
    .mermaid-lab-editor-pane {
      border-right: 1px solid var(--line);
      display: flex;
    }
    .mermaid-lab-editor {
      flex: 1 1 auto;
      width: 100%;
      height: 100%;
      border: 0;
      outline: 0;
      padding: 14px 16px;
      background: var(--code);
      color: var(--text);
      font-family: ui-monospace, monospace;
      font-size: 13px;
      line-height: 1.5;
      resize: none;
    }
    .mermaid-lab-preview-pane {
      padding: 18px;
      background: var(--panel-2);
    }
    .mermaid-lab-preview-pane .mermaid {
      display: flex;
      justify-content: center;
    }
    .mermaid-lab-preview-pane .mermaid-error {
      color: oklch(0.6 0.2 25);
      font-family: ui-monospace, monospace;
      white-space: pre-wrap;
      font-size: 12px;
    }
    .mermaid-lab-preview-pane .mermaid-lab-math {
      display: flex;
      justify-content: center;
      align-items: center;
      min-height: 100%;
      font-size: 20px;
      color: var(--text);
      text-align: center;
    }
    .mermaid-lab-foot {
      padding: 10px 16px 12px;
      border-top: 1px solid var(--line);
      font-size: 12px;
    }
    @media (max-width: 720px) {
      .mermaid-lab-body { flex-direction: column; }
      .mermaid-lab-editor-pane { border-right: 0; border-bottom: 1px solid var(--line); }
    }

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

    /* ── Git version compare: full-screen split before/after ── */
    .vcompare {
      position: fixed;
      inset: 0;
      z-index: 1800;
      display: flex;
      flex-direction: column;
      background: var(--bg);
    }
    .vcompare[hidden] { display: none; }
    .vcompare-head {
      flex: 0 0 auto;
      display: flex;
      align-items: center;
      gap: 12px;
      padding: 10px 16px;
      border-bottom: 1px solid var(--line);
      background: color-mix(in oklab, var(--panel) 92%, transparent);
    }
    .vcompare-title { font-size: 13px; font-weight: 700; color: var(--text); flex: 1 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .vcompare-nav { flex: 0 0 auto; display: flex; align-items: center; gap: 6px; }
    .vcompare-nav .action { padding: 5px 8px; }
    .vcompare-nav-count { font-size: 11px; color: var(--muted); font-variant-numeric: tabular-nums; min-width: 64px; text-align: center; white-space: nowrap; }
    .vcompare-close { flex: 0 0 auto; }
    .vcompare-body {
      flex: 1 1 auto;
      min-height: 0;
      display: grid;
      grid-template-columns: 1fr 1fr;
    }
    .vcompare-pane {
      display: flex;
      flex-direction: column;
      min-width: 0;
      min-height: 0;
      border-right: 1px solid var(--line);
    }
    .vcompare-pane:last-child { border-right: none; }
    .vcompare-pane-head {
      flex: 0 0 auto;
      display: flex;
      align-items: center;
      gap: 8px;
      min-width: 0;
      padding: 8px 12px;
      border-bottom: 1px solid color-mix(in oklab, var(--line) 70%, transparent);
      background: color-mix(in oklab, var(--panel-2) 50%, transparent);
    }
    .vcompare-pane-label { font-size: 11px; font-weight: 700; letter-spacing: .03em; flex: 0 0 auto; }
    .vcompare-pane-label.before { color: color-mix(in oklab, #e6604f 70%, var(--muted)); }
    .vcompare-pane-label.after { color: color-mix(in oklab, var(--accent-2) 80%, var(--muted)); }
    .vcompare-select {
      flex: 1 1 auto;
      min-width: 0;
      border: 1px solid color-mix(in oklab, var(--line) 60%, transparent);
      background: color-mix(in oklab, var(--panel-2) 86%, transparent);
      color: var(--text);
      border-radius: 7px;
      padding: 4px 8px;
      font: 12px/1.4 system-ui, -apple-system, sans-serif;
      outline: none;
    }
    .vcompare-select:focus { border-color: color-mix(in oklab, var(--accent) 50%, var(--accent-2)); }
    /* Each side renders markdown normally and scrolls; the two panes are kept
       in proportional scroll sync via JS. Lines that differ between the two
       revisions get a colored-background block highlight. */
    .vcompare-pane-body {
      flex: 1 1 auto;
      min-height: 0;
      overflow: auto;
    }
    .vcompare-pane-body .vcd-chg-block {
      border-radius: 4px;
      scroll-margin: 8px;
    }
    .vcompare-pane-body.side-l .vcd-chg-block {
      background: color-mix(in oklab, #e0533f 13%, transparent);
      box-shadow: inset 3px 0 0 color-mix(in oklab, #e0533f 60%, transparent);
    }
    .vcompare-pane-body.side-r .vcd-chg-block {
      background: color-mix(in oklab, #3fb950 15%, transparent);
      box-shadow: inset 3px 0 0 color-mix(in oklab, #3fb950 60%, transparent);
    }
    .vcompare-pane-body .vcd-flash { animation: vcdFlash 0.95s ease; border-radius: 4px; }
    @keyframes vcdFlash {
      0% { outline: 2px solid color-mix(in oklab, var(--accent) 75%, transparent); outline-offset: 2px; }
      100% { outline: 2px solid transparent; outline-offset: 2px; }
    }
    .vcd-rawwrap { font: 12.5px/1.55 ui-monospace, SFMono-Regular, monospace; tab-size: 4; }
    .vcd-rawline { white-space: pre-wrap; word-break: break-word; overflow-wrap: anywhere; padding: 0 6px; }
    /* Changed code/source lines: raw-line divs and hljs line-number rows. */
    .vcompare-pane-body.side-l div.vcd-chg-line { background: color-mix(in oklab, #e0533f 16%, transparent); }
    .vcompare-pane-body.side-r div.vcd-chg-line { background: color-mix(in oklab, #3fb950 18%, transparent); }
    .vcompare-pane-body.side-l tr.vcd-chg-line > td { background: color-mix(in oklab, #e0533f 16%, transparent); }
    .vcompare-pane-body.side-r tr.vcd-chg-line > td { background: color-mix(in oklab, #3fb950 18%, transparent); }
    /* Intra-line emphasis: the exact removed/added characters within a changed
       line, on top of the subtle whole-line tint. */
    .vcompare-pane-body mark.vcd-ic { color: inherit; border-radius: 2px; padding: 0 1px; }
    .vcompare-pane-body mark.vcd-ic-del { background: color-mix(in oklab, #e0533f 48%, transparent); }
    .vcompare-pane-body mark.vcd-ic-add { background: color-mix(in oklab, #3fb950 52%, transparent); }
    /* Changed mermaid diagram: flag it and offer a source toggle. */
    .vcd-mermaid-card { border-radius: 8px; margin: 6px 0; }
    .vcompare-pane-body.side-l .vcd-mermaid-card { outline: 2px solid color-mix(in oklab, #e0533f 55%, transparent); outline-offset: -2px; }
    .vcompare-pane-body.side-r .vcd-mermaid-card { outline: 2px solid color-mix(in oklab, #3fb950 55%, transparent); outline-offset: -2px; }
    .vcd-mermaid-bar { display: flex; align-items: center; gap: 8px; padding: 4px 6px; }
    .vcd-mermaid-badge { font-size: 10px; font-weight: 700; padding: 1px 7px; border-radius: 999px; }
    .vcompare-pane-body.side-l .vcd-mermaid-badge { color: #e0533f; background: color-mix(in oklab, #e0533f 16%, transparent); }
    .vcompare-pane-body.side-r .vcd-mermaid-badge { color: #2f9e4f; background: color-mix(in oklab, #3fb950 18%, transparent); }
    .vcd-mermaid-toggle { margin-left: auto; font-size: 11px; border: 1px solid color-mix(in oklab, var(--line) 70%, transparent); background: color-mix(in oklab, var(--panel-2) 70%, transparent); color: var(--text); border-radius: 7px; padding: 2px 8px; cursor: pointer; }
    .vcd-mermaid-toggle:hover { border-color: var(--accent); color: var(--accent); }
    .vcd-mermaid-src { padding: 6px 8px; }
    .vcompare-empty { color: var(--muted); font-size: 12px; padding: 12px; }
    /* .action sets display:inline-flex, which would defeat the [hidden] attr. */
    #versionButton[hidden] { display: none; }

    /* Accent-color picker popover (anchored under the 🎨 toolbar button). */
    .accent-popover {
      position: fixed;
      z-index: 2700;
      width: 230px;
      padding: 12px;
      border-radius: 12px;
      border: 1px solid var(--line);
      background: color-mix(in oklab, var(--panel) 96%, transparent);
      box-shadow: 0 12px 34px rgba(0,0,0,0.3);
      backdrop-filter: blur(10px);
      -webkit-backdrop-filter: blur(10px);
    }
    .accent-popover[hidden] { display: none; }
    .accent-pop-title { font-size: 11px; font-weight: 700; letter-spacing: .04em; text-transform: uppercase; color: var(--muted); margin-bottom: 8px; }
    .accent-swatches { display: grid; grid-template-columns: repeat(6, 1fr); gap: 8px; margin-bottom: 10px; }
    .accent-swatch {
      width: 100%;
      aspect-ratio: 1 / 1;
      border-radius: 50%;
      border: 2px solid transparent;
      cursor: pointer;
      padding: 0;
      box-shadow: inset 0 0 0 1px color-mix(in oklab, black 16%, transparent);
    }
    .accent-swatch:hover { transform: scale(1.08); }
    .accent-swatch.active { border-color: var(--text); box-shadow: 0 0 0 2px color-mix(in oklab, var(--text) 35%, transparent); }
    .accent-custom-row { display: flex; align-items: center; justify-content: space-between; gap: 8px; font-size: 12px; color: var(--text); margin-bottom: 10px; }
    .accent-custom-row input[type="color"] {
      width: 38px; height: 26px; padding: 0;
      border: 1px solid var(--line); border-radius: 6px; background: transparent; cursor: pointer;
    }
    .accent-reset {
      width: 100%;
      border: 1px solid color-mix(in oklab, var(--line) 70%, transparent);
      background: color-mix(in oklab, var(--panel-2) 70%, transparent);
      color: var(--text);
      border-radius: 8px;
      padding: 6px 10px;
      font-size: 12px;
      cursor: pointer;
    }
    .accent-reset:hover { border-color: var(--accent); color: var(--accent); }

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
    .lightbox-toolbar-bottom {
      top: auto;
      right: auto;
      bottom: 56px;
      left: 50%;
      transform: translateX(-50%);
      padding: 8px 10px;
      background: color-mix(in oklab, var(--panel) 88%, black);
      border: 1px solid color-mix(in oklab, var(--line) 70%, transparent);
      border-radius: 999px;
      box-shadow: 0 8px 24px rgba(0,0,0,0.35);
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
    .lightbox-toolbar .draw-only[hidden] { display: none !important; }
    .lightbox.eraser-mode .lb-annotation { cursor: pointer; }
    .lightbox.eraser-mode .lb-annotation:hover { stroke-width: 6 !important; filter: drop-shadow(0 0 4px rgba(255,255,255,0.6)); }
    .lightbox.postit-mode .lightbox-stage { cursor: crosshair; }
    .lightbox.postit-mode .lb-postit { cursor: move; }
    .lb-postit-resize, .lb-postit-delete { visibility: hidden; }
    .lightbox.postit-mode .lb-postit-resize { visibility: visible; cursor: nwse-resize; }
    .lightbox.postit-mode .lb-postit-delete { visibility: visible; cursor: pointer; }
    .lightbox.postit-mode .lb-postit-delete:hover circle { fill: #d32f2f; }
    .lb-postit-editor {
      position: fixed;
      background: #fff59d;
      color: #2b2b2b;
      border: 1px solid #fbc02d;
      border-radius: 6px;
      padding: 8px 10px;
      font-family: system-ui, -apple-system, "Helvetica Neue", Arial, sans-serif;
      font-size: 14px;
      line-height: 18px;
      resize: none;
      outline: none;
      z-index: 2100;
      box-shadow: 0 6px 20px rgba(0,0,0,0.25);
    }
    .lightbox-toolbar .lb-anno-color-label {
      display: grid;
      place-items: center;
      width: 38px;
      height: 38px;
      border-radius: 999px;
      border: 1px solid color-mix(in oklab, var(--line) 70%, transparent);
      background: color-mix(in oklab, var(--panel) 92%, black);
      box-shadow: 0 8px 24px rgba(0,0,0,0.35);
      cursor: pointer;
      overflow: hidden;
    }
    .lightbox-toolbar .lb-anno-color-label input[type="color"] {
      width: 28px;
      height: 28px;
      border: 0;
      padding: 0;
      background: transparent;
      cursor: pointer;
    }
    .lightbox-toolbar .lb-anno-opacity-label {
      display: grid;
      place-items: center;
      height: 38px;
      padding: 0 8px;
      border-radius: 999px;
      border: 1px solid color-mix(in oklab, var(--line) 70%, transparent);
      background: color-mix(in oklab, var(--panel) 92%, black);
      box-shadow: 0 8px 24px rgba(0,0,0,0.35);
    }
    .lightbox-toolbar .lb-anno-opacity-label input[type="range"] {
      width: 84px;
      cursor: pointer;
    }
    .lightbox-toolbar button:hover {
      background: color-mix(in oklab, var(--accent) 35%, var(--panel));
    }
    .lightbox-toolbar button.active {
      border-color: var(--accent);
      color: var(--accent);
    }
    .lightbox.draw-mode .lightbox-stage { cursor: crosshair; }
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
    .panel-tabs {
      display: flex;
      gap: 4px;
      padding: 3px;
      border-radius: 10px;
      background: color-mix(in oklab, var(--panel-2) 70%, transparent);
      border: 1px solid color-mix(in oklab, var(--line) 45%, transparent);
    }
    .panel-tab {
      flex: 1 1 0;
      border: 0;
      background: transparent;
      color: var(--muted);
      font-size: 12px;
      font-weight: 600;
      padding: 6px 6px;
      border-radius: 7px;
      cursor: pointer;
      white-space: nowrap;
      transition: background 120ms ease, color 120ms ease;
    }
    .panel-tab:hover { color: var(--text); }
    .panel-tab.active {
      background: color-mix(in oklab, var(--accent) 22%, var(--panel-2));
      color: var(--text);
    }
    .panel-pane { display: flex; flex-direction: column; gap: 14px; }
    .panel-pane[hidden] { display: none; }
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
      font-size: 12.5px;
      font-weight: 700;
      text-transform: uppercase;
      letter-spacing: .12em;
      color: var(--text);
    }
    .sec-ico {
      margin-right: .45em;
      letter-spacing: normal;
      font-size: 1.05em;
      filter: grayscale(0.15);
    }
    .outline-section[hidden] { display: none; }
    .outline-list {
      display: flex;
      flex-direction: column;
      gap: 1px;
      max-height: 40vh;
      overflow-y: auto;
      margin-top: 4px;
    }
    .outline-list.collapsed { display: none; }
    .outline-item {
      /* Keep natural height: without flex:0 0 auto, the overflow:hidden below
         lets the flex column shrink items to unreadable slivers when there are
         many headings. With it, items stay full-size and the list scrolls. */
      flex: 0 0 auto;
      font-size: 12.5px;
      line-height: 1.5;
      color: var(--text);
      padding: 3px 8px;
      border-radius: 6px;
      border-left: 2px solid transparent;
      cursor: pointer;
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
    }
    /* Deeper levels are progressively softer so the hierarchy reads at a glance
       while every item stays legible. */
    .outline-lvl-3, .outline-lvl-4, .outline-lvl-5, .outline-lvl-6 {
      color: color-mix(in oklab, var(--text) 82%, var(--muted));
    }
    .outline-item:hover { background: color-mix(in oklab, var(--panel-2) 60%, transparent); color: var(--text); }
    .outline-item.active {
      color: var(--accent);
      font-weight: 600;
      border-left-color: var(--accent);
      background: color-mix(in oklab, var(--accent) 14%, transparent);
    }
    .outline-lvl-1 { padding-left: 8px; font-weight: 600; }
    .outline-lvl-2 { padding-left: 18px; }
    .outline-lvl-3 { padding-left: 28px; }
    .outline-lvl-4 { padding-left: 38px; }
    .outline-lvl-5 { padding-left: 48px; }
    .outline-lvl-6 { padding-left: 58px; }
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
      display: flex;
      align-items: baseline;
      gap: 8px;
    }
    .search-hit:hover { background: var(--panel-2); }
    .search-hit .search-hit-needle {
      color: var(--accent);
      font-weight: 600;
    }
    .memo-section {
      display: flex;
      flex-direction: column;
      gap: 6px;
    }
    .memo-actions { display: inline-flex; gap: 4px; }
    .memo-area {
      width: 100%;
      min-height: 140px;
      resize: vertical;
      border: 1px solid color-mix(in oklab, var(--line) 55%, transparent);
      background: color-mix(in oklab, var(--panel-2) 86%, transparent);
      color: var(--text);
      border-radius: 10px;
      padding: 10px 12px;
      font: 13px/1.55 ui-monospace, SFMono-Regular, monospace;
      outline: none;
    }
    .memo-area::placeholder { color: var(--muted); }
    .memo-area:focus {
      border-color: color-mix(in oklab, var(--accent) 50%, var(--accent-2));
      box-shadow: 0 0 0 3px color-mix(in oklab, var(--accent) 18%, transparent);
    }
    .memo-list {
      display: flex;
      flex-direction: column;
      gap: 6px;
      max-height: 40vh;
      overflow-y: auto;
      margin: 2px 0;
    }
    .memo-controls { display: flex; flex-direction: column; gap: 5px; margin: 2px 0; }
    .memo-controls[hidden] { display: none; }
    .memo-filter {
      width: 100%;
      border: 1px solid color-mix(in oklab, var(--line) 55%, transparent);
      background: color-mix(in oklab, var(--panel-2) 86%, transparent);
      color: var(--text);
      border-radius: 8px;
      padding: 5px 9px;
      font: 12px/1.4 system-ui, -apple-system, sans-serif;
      outline: none;
    }
    .memo-filter::placeholder { color: var(--muted); }
    .memo-filter:focus {
      border-color: color-mix(in oklab, var(--accent) 50%, var(--accent-2));
      box-shadow: 0 0 0 3px color-mix(in oklab, var(--accent) 18%, transparent);
    }
    .memo-list-item {
      flex: 0 0 auto;
      display: flex;
      align-items: flex-start;
      gap: 6px;
      padding: 7px 9px;
      border-radius: 9px;
      border: 1px solid color-mix(in oklab, var(--line) 38%, transparent);
      background: color-mix(in oklab, var(--panel-2) 50%, transparent);
      cursor: pointer;
      user-select: none;
    }
    .memo-list-item:hover {
      background: color-mix(in oklab, var(--panel-2) 85%, transparent);
      border-color: color-mix(in oklab, var(--line) 60%, transparent);
    }
    .memo-list-item.active {
      background: color-mix(in oklab, var(--accent) 16%, transparent);
      border-color: color-mix(in oklab, var(--accent) 55%, transparent);
      box-shadow: inset 2px 0 0 var(--accent);
    }
    .memo-item-pin {
      flex: 0 0 auto;
      border: none;
      background: transparent;
      cursor: pointer;
      font-size: 12px;
      line-height: 1.3;
      padding: 1px 2px;
      border-radius: 6px;
      opacity: 0;
      filter: grayscale(1);
    }
    .memo-list-item:hover .memo-item-pin { opacity: 0.55; }
    .memo-item-pin.pinned { opacity: 1; filter: none; }
    .memo-item-pin:hover { opacity: 1; background: color-mix(in oklab, var(--line) 50%, transparent); }
    .memo-item-main { flex: 1 1 auto; min-width: 0; }
    .memo-item-title {
      font-size: 12.5px;
      color: var(--text);
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
    }
    .memo-item-title.untitled { color: var(--muted); font-style: italic; }
    .memo-item-source {
      font-size: 10px;
      color: color-mix(in oklab, var(--accent) 70%, var(--muted));
      margin-top: 2px;
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
    }
    .memo-item-snippet {
      font-size: 11px;
      line-height: 1.35;
      color: var(--muted);
      margin-top: 1px;
      display: -webkit-box;
      -webkit-line-clamp: 2;
      -webkit-box-orient: vertical;
      overflow: hidden;
      word-break: break-word;
    }
    .memo-item-right {
      flex: 0 0 auto;
      display: flex;
      flex-direction: column;
      align-items: flex-end;
      gap: 2px;
    }
    .memo-item-time { font-size: 10.5px; color: var(--muted); white-space: nowrap; }
    .memo-item-del {
      flex: 0 0 auto;
      border: none;
      background: transparent;
      color: var(--muted);
      cursor: pointer;
      font-size: 14px;
      line-height: 1;
      padding: 0 3px;
      border-radius: 6px;
      opacity: 0;
    }
    .memo-list-item:hover .memo-item-del { opacity: 1; }
    .memo-item-del:hover { color: var(--text); background: color-mix(in oklab, var(--line) 50%, transparent); }
    .memo-empty {
      font-size: 12px;
      color: var(--muted);
      padding: 8px 4px;
      text-align: center;
    }
    .memo-empty[hidden] { display: none; }
    /* Trash: a collapsible recovery buffer at the bottom of the memo pane. */
    .memo-trash {
      display: flex;
      flex-direction: column;
      gap: 6px;
      margin-top: 8px;
      padding-top: 8px;
      border-top: 1px dashed color-mix(in oklab, var(--line) 60%, transparent);
    }
    .memo-trash[hidden] { display: none; }
    .memo-trash-head { display: flex; align-items: center; gap: 6px; }
    .memo-trash-toggle {
      flex: 1 1 auto;
      display: inline-flex;
      align-items: center;
      gap: 6px;
      border: none;
      background: transparent;
      color: var(--muted);
      cursor: pointer;
      font-size: 12px;
      padding: 2px;
      border-radius: 6px;
      text-align: left;
    }
    .memo-trash-toggle:hover { color: var(--text); }
    .memo-trash-caret { display: inline-block; font-size: 10px; transition: transform 120ms ease; }
    .memo-trash.open .memo-trash-caret { transform: rotate(90deg); }
    .memo-trash-count {
      font-variant-numeric: tabular-nums;
      font-size: 10.5px;
      color: var(--muted);
      background: color-mix(in oklab, var(--line) 45%, transparent);
      border-radius: 999px;
      padding: 0 6px;
      min-width: 16px;
      text-align: center;
    }
    .memo-trash-empty-btn {
      flex: 0 0 auto;
      border: 1px solid color-mix(in oklab, var(--line) 55%, transparent);
      background: transparent;
      color: var(--muted);
      cursor: pointer;
      font-size: 11px;
      padding: 2px 8px;
      border-radius: 7px;
    }
    .memo-trash-empty-btn:hover {
      color: #e6604f;
      border-color: color-mix(in oklab, #c0392b 50%, var(--line));
      background: color-mix(in oklab, #c0392b 10%, transparent);
    }
    .memo-trash-list {
      display: flex;
      flex-direction: column;
      gap: 5px;
      max-height: 30vh;
      overflow-y: auto;
    }
    .memo-trash-list[hidden] { display: none; }
    .memo-trash-item {
      display: flex;
      align-items: center;
      gap: 6px;
      padding: 5px 8px;
      border-radius: 8px;
      border: 1px solid color-mix(in oklab, var(--line) 30%, transparent);
      background: color-mix(in oklab, var(--panel-2) 40%, transparent);
    }
    .memo-trash-item-main { flex: 1 1 auto; min-width: 0; }
    .memo-trash-item-title {
      font-size: 12px;
      color: var(--muted);
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
    }
    .memo-trash-item-time { font-size: 10px; color: var(--muted); margin-top: 1px; }
    .memo-trash-btn {
      flex: 0 0 auto;
      border: none;
      background: transparent;
      cursor: pointer;
      font-size: 13px;
      line-height: 1;
      padding: 2px 5px;
      border-radius: 6px;
      color: var(--muted);
    }
    .memo-trash-btn.restore:hover { color: var(--accent-2); background: color-mix(in oklab, var(--accent-2) 16%, transparent); }
    .memo-trash-btn.purge:hover { color: #e6604f; background: color-mix(in oklab, #c0392b 14%, transparent); }
    .memo-title-input {
      width: 100%;
      border: 1px solid color-mix(in oklab, var(--line) 55%, transparent);
      background: color-mix(in oklab, var(--panel-2) 86%, transparent);
      color: var(--text);
      border-radius: 8px;
      padding: 7px 10px;
      font: 13px/1.4 system-ui, -apple-system, sans-serif;
      outline: none;
    }
    .memo-title-input::placeholder { color: var(--muted); }
    .memo-title-input:focus {
      border-color: color-mix(in oklab, var(--accent) 50%, var(--accent-2));
      box-shadow: 0 0 0 3px color-mix(in oklab, var(--accent) 18%, transparent);
    }
    .memo-editor {
      display: flex;
      flex-direction: column;
      gap: 6px;
      margin-top: 8px;
      padding: 10px;
      border-radius: 11px;
      background: color-mix(in oklab, var(--accent) 7%, var(--panel-2));
      border: 1px solid color-mix(in oklab, var(--accent) 22%, var(--line));
    }
    .memo-editor[hidden] { display: none; }
    .memo-sync-state { font-size: 10.5px; color: var(--muted); min-height: 13px; }
    .backlink-hit {
      border-radius: 3px;
      background: color-mix(in oklab, var(--accent) 45%, transparent);
      box-shadow: 0 0 0 2px color-mix(in oklab, var(--accent) 45%, transparent);
      animation: backlinkFlash 2.4s ease forwards;
    }
    @keyframes backlinkFlash {
      0%, 55% { background: color-mix(in oklab, var(--accent) 50%, transparent); box-shadow: 0 0 0 2px color-mix(in oklab, var(--accent) 50%, transparent); }
      100% { background: transparent; box-shadow: 0 0 0 2px transparent; }
    }
    .memo-backlink {
      display: block;
      font-size: 11.5px;
      color: var(--accent);
      text-decoration: none;
      cursor: pointer;
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
      padding: 2px 0;
    }
    .memo-backlink[hidden] { display: none; }
    .memo-backlink:hover { text-decoration: underline; }
    .sidebar-version {
      display: flex;
      align-items: center;
      gap: 4px;
      margin: 6px 4px 4px;
      padding: 6px 6px;
      width: calc(100% - 8px);
      border-top: 1px solid color-mix(in oklab, var(--line) 40%, transparent);
    }
    .sidebar-version[hidden] { display: none; }
    .version-repo-link {
      flex: 0 0 auto;
      border: 0;
      background: transparent;
      cursor: pointer;
      font-size: 13px;
      line-height: 1;
      padding: 2px 3px;
      border-radius: 5px;
      opacity: 0.8;
    }
    .version-repo-link:hover { opacity: 1; background: color-mix(in oklab, var(--line) 45%, transparent); }
    .version-text {
      flex: 1 1 auto;
      min-width: 0;
      border: 0;
      background: transparent;
      color: var(--muted);
      font: 600 10.5px/1.3 ui-monospace, SFMono-Regular, monospace;
      text-align: left;
      cursor: pointer;
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
    }
    .version-text:hover { color: var(--text); }
    .version-text.update-available { color: var(--accent); font-weight: 700; }
    .update-overlay {
      position: fixed;
      inset: 0;
      z-index: 3000;
      background: color-mix(in oklab, black 50%, transparent);
      backdrop-filter: blur(3px);
      -webkit-backdrop-filter: blur(3px);
      display: flex;
      align-items: center;
      justify-content: center;
    }
    .update-overlay[hidden] { display: none; }
    .update-overlay-card {
      background: var(--panel-2);
      border: 1px solid color-mix(in oklab, var(--line) 70%, transparent);
      border-radius: 14px;
      padding: 26px 32px;
      display: flex;
      flex-direction: column;
      align-items: center;
      gap: 14px;
      color: var(--text);
      font-size: 13px;
      max-width: 70vw;
      text-align: center;
      white-space: pre-wrap;
      box-shadow: 0 20px 60px rgba(0,0,0,0.45);
    }
    .update-spinner {
      width: 28px; height: 28px;
      border-radius: 50%;
      border: 3px solid color-mix(in oklab, var(--accent) 30%, transparent);
      border-top-color: var(--accent);
      animation: updateSpin 0.8s linear infinite;
    }
    @keyframes updateSpin { to { transform: rotate(360deg); } }
    .memo-selection-bar {
      position: fixed;
      z-index: 2600;
      transform: translate(-50%, 6px);
      display: inline-flex;
      gap: 4px;
      padding: 4px;
      border-radius: 10px;
      background: color-mix(in oklab, var(--panel-2) 32%, transparent);
      border: 1px solid color-mix(in oklab, var(--line) 35%, transparent);
      box-shadow: 0 6px 20px rgba(0,0,0,0.18);
      backdrop-filter: blur(7px);
      -webkit-backdrop-filter: blur(7px);
    }
    .memo-selection-bar[hidden] { display: none; }
    /* Rubber-band box drawn while ⌥+dragging inside the lightbox. The text
       fully enclosed by it is copied to the clipboard on release. */
    .lb-select-box {
      position: fixed;
      z-index: 2601;             /* above the lightbox (2000), below toolbars */
      border: 1.5px solid var(--accent);
      background: color-mix(in oklab, var(--accent) 16%, transparent);
      border-radius: 2px;
      pointer-events: none;
    }
    .lb-select-box[hidden] { display: none; }
    /* Selection-style highlight flashed over text that was just copied. */
    .lb-copy-hl {
      position: fixed;
      z-index: 2601;
      pointer-events: none;
      background: color-mix(in oklab, var(--accent) 42%, transparent);
      border-radius: 2px;
      opacity: 1;
      transition: opacity 480ms ease;
    }
    .lb-copy-hl.fade { opacity: 0; }
    /* ⌥ held over the lightbox stage → box-select cursor (not text I-beam). */
    body.alt-select-mode .lightbox-stage,
    body.alt-select-mode .lightbox-stage * { cursor: crosshair !important; }
    .memo-selection-btn {
      border: 1px solid color-mix(in oklab, var(--accent) 35%, transparent);
      background: color-mix(in oklab, var(--accent) 14%, transparent);
      color: var(--text);
      font-size: 12px;
      padding: 6px 10px;
      border-radius: 7px;
      cursor: pointer;
      white-space: nowrap;
    }
    .memo-selection-btn:hover { background: color-mix(in oklab, var(--accent) 30%, transparent); }
    .memo-conflict-count { font-weight: 500; color: var(--muted); font-size: 12px; letter-spacing: 0; }
    .memo-conflict-body { padding: 4px 16px 16px; overflow-y: auto; }
    .memo-conflict-name { font-size: 13px; font-weight: 600; color: var(--text); margin: 6px 0 4px; }
    .memo-conflict-note { font-size: 12px; color: var(--muted); margin-bottom: 10px; }
    .memo-conflict-cols { display: flex; gap: 10px; flex-wrap: wrap; }
    .memo-conflict-col { flex: 1 1 220px; min-width: 0; }
    .memo-conflict-col-label { font-size: 11px; color: var(--muted); margin-bottom: 4px; }
    .memo-conflict-preview {
      border: 1px solid color-mix(in oklab, var(--line) 60%, transparent);
      background: color-mix(in oklab, var(--panel-2) 86%, transparent);
      border-radius: 8px;
      padding: 8px 10px;
      font: 12px/1.5 ui-monospace, SFMono-Regular, monospace;
      color: var(--text);
      white-space: pre-wrap;
      word-break: break-word;
      max-height: 180px;
      overflow-y: auto;
    }
    .memo-conflict-actions { display: flex; gap: 8px; flex-wrap: wrap; margin-top: 14px; }
    .memo-conflict-actions .action { flex: 1 1 auto; }
    .memo-conflict-actions .action.primary {
      background: color-mix(in oklab, var(--accent) 22%, transparent);
      border-color: color-mix(in oklab, var(--accent) 55%, transparent);
      color: var(--text);
    }
    .search-section-head {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 8px;
      margin-bottom: 6px;
    }
    .search-sort {
      display: inline-flex;
      gap: 0;
      border: 1px solid color-mix(in oklab, var(--line) 55%, transparent);
      border-radius: 999px;
      background: color-mix(in oklab, var(--panel-2) 70%, transparent);
      padding: 2px;
    }
    .search-sort-btn {
      border: 0;
      background: transparent;
      color: color-mix(in oklab, var(--text) 60%, var(--muted));
      font-size: 10px;
      font-weight: 600;
      letter-spacing: 0.05em;
      text-transform: uppercase;
      padding: 3px 8px;
      border-radius: 999px;
      cursor: pointer;
      transition: background 120ms ease, color 120ms ease;
    }
    .search-sort-btn:hover { color: var(--text); }
    .search-sort-btn.active {
      background: color-mix(in oklab, var(--accent) 65%, var(--panel-2));
      color: var(--panel);
    }
    .search-sort-btn:disabled,
    .search-sort-btn.disabled {
      opacity: 0.4;
      cursor: not-allowed;
      pointer-events: none;
    }
    .search-hit .search-hit-line {
      flex: 0 0 auto;
      font-family: ui-monospace, SFMono-Regular, monospace;
      font-size: 11px;
      color: color-mix(in oklab, var(--text) 45%, var(--muted));
      background: color-mix(in oklab, var(--panel-2) 70%, transparent);
      padding: 1px 6px;
      border-radius: 999px;
      min-width: 32px;
      text-align: center;
      letter-spacing: 0.02em;
    }
    .search-hit .search-hit-ctx {
      flex: 1 1 auto;
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
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
    .usage-guide-git-link {
      display: inline-block;
      margin-top: 8px;
      font-size: 12px;
      color: var(--accent);
      text-decoration: none;
    }
    .usage-guide-git-link:hover { text-decoration: underline; }
    .usage-guide-git-link[hidden] { display: none; }
    .git-remote-link {
      display: inline-block;
      margin-top: 4px;
      font-size: 11px;
      color: var(--accent);
      text-decoration: none;
      cursor: pointer;
    }
    .git-remote-link:hover { text-decoration: underline; }
    .git-remote-link[hidden] { display: none; }
    /* Brief accent wash on the preview pane after an auto-refresh
       reloads the open file. Lets the user feel the update without
       losing their scroll position. */
    @keyframes preview-refresh-flash {
      0%   { background-color: color-mix(in oklab, var(--accent) 22%, transparent); }
      100% { background-color: transparent; }
    }
    .preview-body.refresh-flash { animation: preview-refresh-flash 900ms ease-out; }
    /* Transient toast notification used for copy/save confirmations. */
    .toast-stack {
      position: fixed;
      bottom: 24px;
      left: 50%;
      transform: translateX(-50%);
      z-index: 5000;
      display: flex;
      flex-direction: column-reverse;
      gap: 8px;
      pointer-events: none;
    }
    .toast {
      pointer-events: auto;
      padding: 10px 16px;
      border-radius: 999px;
      background: color-mix(in oklab, var(--panel-2) 92%, transparent);
      border: 1px solid color-mix(in oklab, var(--line) 55%, transparent);
      color: var(--text);
      font-size: 13px;
      box-shadow: 0 8px 24px rgba(0,0,0,0.25);
      backdrop-filter: blur(6px);
      animation: toast-in 180ms ease-out, toast-out 220ms ease-in 1800ms forwards;
      display: inline-flex;
      align-items: center;
      gap: 8px;
      max-width: 60ch;
    }
    .toast .toast-icon { font-size: 15px; }
    .toast.toast-ok { border-color: color-mix(in oklab, oklch(0.7 0.18 150) 55%, transparent); }
    .toast.toast-err { border-color: color-mix(in oklab, oklch(0.7 0.22 28) 55%, transparent); }
    @keyframes toast-in {
      from { opacity: 0; transform: translateY(8px); }
      to { opacity: 1; transform: translateY(0); }
    }
    @keyframes toast-out {
      to { opacity: 0; transform: translateY(8px); }
    }
    /* Icon-prefixed inputs: the icon sits inside the rounded box and
       the input gets extra left padding so text doesn't run under it. */
    .searchbox.has-icon { position: relative; }
    .searchbox.has-icon .searchbox-icon {
      position: absolute;
      left: 10px;
      top: 50%;
      transform: translateY(-50%);
      width: 14px;
      height: 14px;
      color: var(--muted);
      pointer-events: none;
    }
    .searchbox.has-icon .search-input { padding-left: 32px; }
    .searchbox.has-icon:focus-within .searchbox-icon { color: var(--accent); }
    .searchbox.has-browse-btn .search-input { padding-right: 28px; }
    .searchbox-browse-btn {
      position: absolute;
      right: 5px;
      top: 50%;
      transform: translateY(-50%);
      border: 0;
      background: transparent;
      color: var(--muted);
      cursor: pointer;
      padding: 3px 4px;
      border-radius: 6px;
      line-height: 0;
      display: flex;
      align-items: center;
    }
    .searchbox-browse-btn:hover { color: var(--accent); background: color-mix(in oklab, var(--accent) 12%, transparent); }
    .searchbox-browse-btn svg { width: 13px; height: 13px; }
    /* Folder Browse Modal */
    .fb-search-wrap {
      position: relative;
      border-bottom: 1px solid color-mix(in oklab, var(--line) 70%, transparent);
      display: flex;
      align-items: center;
    }
    .fb-search-wrap .fb-search-icon {
      position: absolute;
      left: 14px;
      width: 13px;
      height: 13px;
      color: var(--muted);
      pointer-events: none;
      flex-shrink: 0;
    }
    #fbSearch {
      width: 100%;
      border: 0;
      outline: 0;
      background: transparent;
      color: inherit;
      font: inherit;
      font-size: 13px;
      padding: 10px 14px 10px 36px;
      box-sizing: border-box;
    }
    .fb-group-header {
      padding: 6px 14px 4px;
      font-size: 10px;
      font-weight: 700;
      letter-spacing: 0.07em;
      text-transform: uppercase;
      color: var(--muted);
      background: color-mix(in oklab, var(--panel-2) 95%, var(--line));
      border-top: 1px solid color-mix(in oklab, var(--line) 50%, transparent);
      position: sticky;
      top: 0;
      z-index: 1;
    }
    .fb-group-header:first-child { border-top: 0; }
    .fb-file {
      display: flex;
      align-items: center;
      gap: 8px;
      padding: 6px 14px;
      cursor: pointer;
      font-size: 13px;
    }
    .fb-file:hover { background: color-mix(in oklab, var(--accent) 12%, var(--panel-2)); }
    .fb-file.active { background: color-mix(in oklab, var(--accent) 18%, var(--panel-2)); color: var(--accent); }
    .fb-file-icon { color: var(--muted); font-size: 12px; flex-shrink: 0; line-height: 1; }
    .fb-file-name { flex: 1 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .fb-file-name mark { background: transparent; color: var(--accent); font-weight: 600; }
    .fb-file-time { font-size: 11px; color: var(--muted); white-space: nowrap; flex-shrink: 0; font-variant-numeric: tabular-nums; }
    .fb-empty, .fb-loading { padding: 32px 16px; text-align: center; color: var(--muted); font-size: 13px; }
    /* Path chip: small folder icon prefix. */
    #cwd.path-chip::before {
      content: "";
      display: inline-block;
      width: 11px;
      height: 9px;
      margin-right: 6px;
      vertical-align: -1px;
      background: currentColor;
      mask: url("data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 11 9' fill='currentColor'><path d='M0 1.5 C0 .7.7 0 1.5 0 H3.6 L4.8 1.2 H9.5 c.8 0 1.5.7 1.5 1.5 V7.5 c0 .8-.7 1.5-1.5 1.5 H1.5 C.7 9 0 8.3 0 7.5 Z'/></svg>") center / contain no-repeat;
      -webkit-mask: url("data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 11 9' fill='currentColor'><path d='M0 1.5 C0 .7.7 0 1.5 0 H3.6 L4.8 1.2 H9.5 c.8 0 1.5.7 1.5 1.5 V7.5 c0 .8-.7 1.5-1.5 1.5 H1.5 C.7 9 0 8.3 0 7.5 Z'/></svg>") center / contain no-repeat;
    }
  </style>
  <script>
    // Apply theme + accent BEFORE first paint to avoid a flash of wrong colors.
    (function() {
      try {
        var t = localStorage.getItem("mdviewer.theme") || "auto";
        if (t === "light" || t === "dark") {
          document.documentElement.setAttribute("data-theme", t);
        }
      } catch (e) {}
      try {
        var a = localStorage.getItem("mdviewer.accent") || "";
        if (a) document.documentElement.style.setProperty("--accent", a);
      } catch (e) {}
    })();

    // Strikethrough only on double tilde (~~text~~), per the GFM spec. marked's
    // default also treats a single "~" as strikethrough, which turns range
    // notation like "0031~0033" or "10~20" into accidental <del>. Override the
    // del tokenizer to require "~~" and consume a lone "~" as literal text.
    (function () {
      try {
        if (typeof marked === "undefined" || !marked.use) return;
        marked.use({
          tokenizer: {
            del: function (src) {
              var m = /^~~(?=\S)([\s\S]*?\S)~~/.exec(src);
              if (m) {
                return { type: "del", raw: m[0], text: m[1], tokens: this.lexer.inlineTokens(m[1]) };
              }
              if (src.charCodeAt(0) === 126) { // a lone "~" → plain text, not <del>
                return { type: "text", raw: "~", text: "~" };
              }
              return false; // not a tilde — let other tokenizers run
            },
          },
        });
      } catch (e) { /* fall back to marked's default strikethrough */ }
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
          <div class="subtle path-chip" id="cwd"></div>
          <a class="git-remote-link" id="gitRemoteLink" href="#" target="_blank" rel="noopener" hidden>↗ open remote</a>
          <div class="searchbox has-icon has-browse-btn">
            <svg class="searchbox-icon" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
              <circle cx="7" cy="7" r="4.5" />
              <line x1="10.5" y1="10.5" x2="13.5" y2="13.5" />
            </svg>
            <input class="search-input" id="searchInput" type="search" data-i18n-ph="phSearchFiles" placeholder="Search files" spellcheck="false" />
            <button class="searchbox-browse-btn" id="browseSubfoldersBtn" data-i18n-title="browseBtnTitle" title="하위 폴더 포함 탐색" type="button" data-i18n-aria="fbTitle" aria-label="하위 폴더 탐색">
              <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                <path d="M1.5 3.5 C1.5 2.7 2.2 2 3 2 H5.5 L6.5 3 H13 c.8 0 1.5.7 1.5 1.5 V12.5 c0 .8-.7 1.5-1.5 1.5 H3 c-.8 0-1.5-.7-1.5-1.5 Z" />
                <line x1="5" y1="7" x2="11" y2="7" />
                <line x1="5" y1="10" x2="9" y2="10" />
              </svg>
            </button>
          </div>
          <div class="searchbox path-jump has-icon">
            <svg class="searchbox-icon" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
              <path d="M2.5 4.5 L8 4.5 L9.5 6 L13.5 6 L13.5 12.5 L2.5 12.5 Z" />
            </svg>
            <input class="search-input" id="pathInput" type="text" data-i18n-ph="phJumpPath" placeholder="Jump to path (Enter)…  e.g. ~/notes/foo.md" spellcheck="false" autocomplete="off" />
          </div>
        </div>
        <button class="action collapse-toggle" id="collapseSidebar" title="Collapse sidebar">‹</button>
      </div>
      <div class="pane">
        <div class="file-header">
          <button class="aidlc-toggle" id="aidlcToggle" type="button" role="switch" aria-checked="false" hidden data-i18n-title="aidlcToggleTitle" title="AI-DLC가 생성한 문서 전체를 최근 수정순으로 모아서 봅니다 (켜면 aidlc-docs 폴더의 모든 문서를 시간순으로 정렬)"><span class="aidlc-switch"><span class="aidlc-knob"></span></span><span class="aidlc-toggle-text">AI-DLC</span><span class="aidlc-toggle-state">OFF</span></button>
          <button class="header-button active" id="sortName" data-direction="asc" type="button">Name</button>
          <button class="header-button size-col" id="sortMod" data-direction="asc" type="button">Updated</button>
        </div>
        <div id="files"></div>
      </div>
      <div class="section" data-section="recentFiles">
        <div class="section-head">
          <button class="section-toggle" type="button" aria-expanded="true" title="Collapse section">
            <span class="section-chevron">▾</span>
            <span class="section-title" data-i18n="secRecentFiles">Recent files</span>
          </button>
          <div class="section-actions">
            <button class="action" id="showAllRecentFiles" data-i18n-title="showAllRecentFilesTitle" title="Show all recent files" hidden>Show all</button>
          </div>
        </div>
        <div class="section-list" id="recentFiles"></div>
      </div>
      <div class="section" data-section="recentDirs">
        <div class="section-head">
          <button class="section-toggle" type="button" aria-expanded="true" title="Collapse section">
            <span class="section-chevron">▾</span>
            <span class="section-title" data-i18n="secRecentFolders">Recent folders</span>
          </button>
          <div class="section-actions">
            <button class="action" id="showAllRecentDirs" data-i18n-title="showAllRecentFoldersTitle" title="Show all recent folders" hidden>Show all</button>
          </div>
        </div>
        <div class="section-list" id="recentDirs"></div>
      </div>
      <div class="section" data-section="favorites">
        <div class="section-head">
          <button class="section-toggle" type="button" aria-expanded="true" title="Collapse section">
            <span class="section-chevron">▾</span>
            <span class="section-title" data-i18n="secFavorites">Favorites</span>
          </button>
          <div class="section-actions">
            <button class="action" id="showAllFavorites" data-i18n-title="showAllFavoritesTitle" title="Show all favorites" hidden>Show all</button>
            <button class="action" id="toggleFavorite">Toggle current</button>
          </div>
        </div>
        <div class="section-list" id="favorites"></div>
      </div>
      <div class="sidebar-version" id="sidebarVersionWrap" hidden>
        <button type="button" class="version-repo-link" id="versionRepoLink" data-i18n-title="versionRepoLinkTitle" title="저장소 페이지 열기">🏷</button>
        <button type="button" class="version-text" id="sidebarVersion" title=""></button>
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
          <button class="action" id="versionButton" type="button" data-i18n-title="versionTitle" title="Compare this file's git revisions side by side" hidden>
            <svg class="ico" viewBox="0 0 24 24" aria-hidden="true"><circle cx="6" cy="6" r="3"/><circle cx="6" cy="18" r="3"/><path d="M6 9v6"/><path d="M18 6a3 3 0 0 1-3 3H9"/><circle cx="18" cy="6" r="3"/></svg>
            <span data-i18n="version">Version</span>
          </button>
          <div class="seg upd-seg" id="updSeg" hidden>
            <button class="upd-toggle" id="updToggle" type="button" role="switch" aria-checked="false" data-i18n-title="updToggleTitle" title="Show what changed since the last version, inline (additions green, deletions red strikethrough)"><span class="upd-switch"><span class="upd-knob"></span></span><span class="upd-toggle-text" data-i18n="updToggleText">Changes</span></button>
            <button class="upd-base-btn" id="updBaseBtn" type="button" hidden data-i18n-title="updBaseTitle" title="Choose the comparison base" aria-label="Comparison base"><span class="upd-base-label" id="updBaseLabel"></span><span class="upd-base-caret">▾</span></button>
            <span class="upd-nav" id="updNav" hidden>
              <button class="action icon-only" id="updPrev" type="button" data-i18n-title="updPrevTitle" title="Previous change (↑)" aria-label="Previous change">▲</button>
              <span class="upd-nav-count" id="updNavCount"></span>
              <button class="action icon-only" id="updNext" type="button" data-i18n-title="updNextTitle" title="Next change (↓)" aria-label="Next change">▼</button>
            </span>
          </div>
          <div class="seg" role="tablist" aria-label="View mode">
            <button class="seg-btn" id="previewModeButton" type="button" role="tab" aria-selected="false" data-i18n-title="previewTitle" title="Preview mode — rendered markdown">
              <svg class="ico" viewBox="0 0 24 24" aria-hidden="true"><path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12Z"/><circle cx="12" cy="12" r="3"/></svg>
              <span data-i18n="preview">Preview</span>
            </button>
            <button class="seg-btn" id="editModeButton" type="button" role="tab" aria-selected="false" data-i18n-title="editTitle" title="Edit mode — raw source in a textarea (⌘S to save)">
              <svg class="ico" viewBox="0 0 24 24" aria-hidden="true"><path d="M12 20h9"/><path d="M16.5 3.5a2.121 2.121 0 1 1 3 3L7 19l-4 1 1-4Z"/></svg>
              <span data-i18n="edit">Edit</span>
            </button>
            <button class="seg-btn" id="splitModeButton" type="button" role="tab" aria-selected="false" data-i18n-title="splitTitle" title="Split mode — editor + live preview side by side">
              <svg class="ico" viewBox="0 0 24 24" aria-hidden="true"><path d="M12 4v16M4 6h6M4 10h6M4 14h6M4 18h6M14 6h6M14 10h6M14 14h6M14 18h6"/></svg>
              <span data-i18n="split">Split</span>
            </button>
          </div>
          <button class="action" id="saveButton" type="button" data-i18n-title="saveTitle" title="Save changes to disk (⌘S)">
            <svg class="ico" viewBox="0 0 24 24" aria-hidden="true"><path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2Z"/><path d="M17 21v-8H7v8M7 3v5h8"/></svg>
            <span data-i18n="save">Save</span>
            <kbd>⌘S</kbd>
          </button>
          <button class="action icon-only" id="refreshButton" type="button" title="Refresh — reload current file from disk (prompts if unsaved changes)" aria-label="Refresh">
            <svg class="ico" viewBox="0 0 24 24" aria-hidden="true"><path d="M3 12a9 9 0 0 1 15.5-6.3L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-15.5 6.3L3 16"/><path d="M3 21v-5h5"/></svg>
          </button>
          <button class="action icon-only" id="mermaidLabBtn" type="button" data-i18n-title="mlBtnTitle" title="Mermaid Playground — paste mermaid source, see it rendered live (click diagram to zoom)" aria-label="Mermaid Playground">
            <svg class="ico" viewBox="0 0 24 24" aria-hidden="true"><circle cx="6" cy="6" r="3"/><circle cx="18" cy="6" r="3"/><circle cx="12" cy="18" r="3"/><line x1="8" y1="7" x2="11" y2="16"/><line x1="16" y1="7" x2="13" y2="16"/><line x1="9" y1="6" x2="15" y2="6"/></svg>
          </button>
          <span class="divider" aria-hidden="true"></span>
          <button class="action icon-only" id="themeToggle" type="button" title="Cycle theme: Auto → Light → Dark (current state shown by icon)" aria-label="Theme">
            <svg class="ico" viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.41-1.41M17.66 6.34l1.41-1.41"/></svg>
          </button>
          <button class="action icon-only" id="accentBtn" type="button" data-i18n-title="accentBtnTitle" title="강조 색상 선택" data-i18n-aria="accentTitle" aria-label="Accent color">
            <svg class="ico" viewBox="0 0 24 24" aria-hidden="true"><circle cx="13.5" cy="6.5" r="1.5"/><circle cx="17.5" cy="10.5" r="1.5"/><circle cx="8.5" cy="7.5" r="1.5"/><circle cx="6.5" cy="12.5" r="1.5"/><path d="M12 2C6.5 2 2 6 2 11c0 4 3 7 7 7 1 0 1.5-.7 1.5-1.5 0-.4-.2-.7-.4-1-.3-.3-.5-.7-.5-1 0-.8.7-1.5 1.5-1.5H13c3.3 0 6-2.5 6-6 0-3.6-3.1-5-7-5Z"/></svg>
          </button>
          <button class="action lang-toggle" id="langToggle" type="button" aria-label="Language">EN</button>
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
    <aside id="searchPanel" class="shell search-panel" aria-label="Panel (Outline · Search · Memo)">
      <button class="action collapse-search-panel" id="collapseSearchPanel" type="button" title="Hide panel">&#x203A;</button>
      <div class="search-panel-body">
        <div class="panel-tabs" role="tablist" aria-label="Panel sections">
          <button type="button" class="panel-tab" id="panelTabOutline" data-tab="outline" role="tab" data-i18n="tabOutline">📑 Outline</button>
          <button type="button" class="panel-tab active" id="panelTabSearch" data-tab="search" role="tab" data-i18n="tabSearch">🔍 Search</button>
          <button type="button" class="panel-tab" id="panelTabMemo" data-tab="memo" role="tab" data-i18n="tabMemo">📝 Memo</button>
        </div>

        <div class="panel-pane" data-pane="outline" hidden>
          <div class="outline-section" id="outlineSection" hidden>
            <div class="search-section-head">
              <div class="search-section-title"><span class="sec-ico">&#x1F4D1;</span><span data-i18n="secOutline">Outline</span></div>
              <div class="memo-actions">
                <button type="button" class="search-sort-btn" id="outlineLevel" data-i18n-title="outlineLevelTitle" title="Heading levels to show (click to change)">H1–3</button>
                <button type="button" class="search-sort-btn" id="outlineToggle" data-i18n-title="outlineCollapseTitle" title="Collapse outline">&#x25BE;</button>
              </div>
            </div>
            <div class="outline-list" id="outlineList"></div>
          </div>
          <div class="search-empty" id="outlineEmpty" data-i18n="outlineEmpty">No headings in this document.</div>
        </div>

        <div class="panel-pane" data-pane="search">
          <input type="search" class="search-input" id="searchPanelInput" data-i18n-ph="phSearchFolder" placeholder="&#x1F50D; Search in this folder&#x2026;" spellcheck="false" autocomplete="off" />
          <div>
            <div class="search-section-head">
              <div class="search-section-title"><span class="sec-ico">&#x1F4C4;</span><span data-i18n="secInThisFile">In this file</span></div>
              <div class="search-sort" role="group" aria-label="Sort hits">
                <button type="button" class="search-sort-btn active" id="searchSortLine" data-sort="line" data-i18n="sortLine" data-i18n-title="sortLineTitle" title="Sort by line position">Line</button>
                <button type="button" class="search-sort-btn" id="searchSortPriority" data-sort="priority" data-i18n="sortPriority" data-i18n-title="sortPriorityTitle" title="Sort by importance (heading first)">Priority</button>
              </div>
            </div>
            <div class="search-summary" id="searchInFileSummary" data-i18n="searchTypeToSearch">Type to search.</div>
            <div class="search-hit-list" id="searchInFileHits"></div>
          </div>
          <div>
            <div class="search-section-head">
              <div class="search-section-title"><span class="sec-ico">&#x1F4C1;</span><span id="searchFolderTitle">Same folder</span></div>
              <div class="search-sort" role="group" aria-label="Search scope">
                <button type="button" class="search-sort-btn active" id="searchScopeFolder" data-scope="folder" data-i18n="scopeFolder" data-i18n-title="scopeFolderTitle" title="Search the current folder only">This folder</button>
                <button type="button" class="search-sort-btn" id="searchScopeGit" data-scope="git" data-i18n="scopeGit" data-i18n-title="scopeGitTitle" title="Search the whole enclosing Git repo">Git repo</button>
              </div>
            </div>
            <div class="search-hit-list" id="searchFolderHits"></div>
          </div>
        </div>

        <div class="panel-pane" data-pane="memo" hidden>
        <div class="memo-section">
          <div class="search-section-head">
            <div class="search-section-title"><span class="sec-ico">&#x1F4DD;</span><span data-i18n="secMemo">Memo</span></div>
            <div class="memo-actions">
              <button type="button" class="search-sort-btn" id="memoNewBtn" data-i18n="memoNew" data-i18n-title="memoNewTitle" title="New memo">＋ New</button>
              <button type="button" class="search-sort-btn" id="memoCopyBtn" data-i18n="memoCopy" data-i18n-title="memoCopyTitle" title="Copy with filename header">📋 Copy</button>
            </div>
          </div>
          <div class="memo-controls" id="memoControls" hidden>
            <input type="search" class="memo-filter" id="memoFilter" data-i18n-ph="phMemoFilter" placeholder="🔍 Search memos…" spellcheck="false" autocomplete="off" />
            <div class="search-sort" role="group" aria-label="Sort memos">
              <button type="button" class="search-sort-btn active" id="memoSortUpdated" data-sort="updated" data-i18n="memoSortUpdated" data-i18n-title="memoSortUpdatedTitle" title="Sort by last modified">Updated</button>
              <button type="button" class="search-sort-btn" id="memoSortCreated" data-sort="created" data-i18n="memoSortCreated" data-i18n-title="memoSortCreatedTitle" title="Sort by created">Created</button>
              <button type="button" class="search-sort-btn" id="memoSortTitle" data-sort="title" data-i18n="memoSortTitle" data-i18n-title="memoSortTitleTitle" title="Sort by title A–Z">Title</button>
            </div>
          </div>
          <div class="memo-list" id="memoList"></div>
          <div class="memo-empty" id="memoEmpty" data-i18n="memoEmpty" hidden>No memos yet. Add one with ＋ New.</div>
          <div class="memo-empty" id="memoNoMatch" data-i18n="memoNoMatch" hidden>No matches</div>
          <div class="memo-editor" id="memoEditor" hidden>
            <input type="text" class="memo-title-input" id="memoTitleInput" spellcheck="false" data-i18n-ph="phMemoTitle" placeholder="Title (optional)" />
            <textarea class="memo-area" id="memoArea" spellcheck="false" data-i18n-ph="phMemoArea" placeholder="A note to remember while reading this file…"></textarea>
            <a class="memo-backlink" id="memoBacklink" hidden></a>
            <div class="memo-sync-state" id="memoSyncState"></div>
          </div>
          <div class="memo-trash" id="memoTrash" hidden>
            <div class="memo-trash-head">
              <button type="button" class="memo-trash-toggle" id="memoTrashToggle" aria-expanded="false" data-i18n-title="memoTrashToggleTitle" title="Deleted memos (click to expand)">
                <span class="memo-trash-caret" id="memoTrashCaret">&#x25B8;</span>
                <span>&#x1F5D1; <span data-i18n="memoTrash">Trash</span></span>
                <span class="memo-trash-count" id="memoTrashCount">0</span>
              </button>
              <button type="button" class="memo-trash-empty-btn" id="memoTrashEmptyBtn" data-i18n="memoTrashEmpty" data-i18n-title="memoTrashEmptyTitle" title="Empty trash (permanent delete)">Empty</button>
            </div>
            <div class="memo-trash-list" id="memoTrashList" hidden></div>
          </div>
        </div>
        </div>
      </div>
    </aside>
  </div>
  <button class="action reveal-sidebar" id="revealSidebar" data-i18n="revealSidebar" data-i18n-title="revealSidebarTitle" title="Show sidebar">☰ Files</button>
  <button class="action reveal-search-panel" id="revealSearchPanel" type="button" data-i18n="revealPanel" data-i18n-title="revealPanelTitle" title="Show panel (Outline · Search · Memo)" hidden>&#x25A4; Panel</button>
  <div class="floating-tooltip" id="floatingTooltip"></div>
  <div class="update-overlay" id="updateOverlay" hidden>
    <div class="update-overlay-card">
      <div class="update-spinner"></div>
      <div id="updateOverlayMsg" data-i18n="updating">Updating…</div>
    </div>
  </div>
  <div class="memo-selection-bar" id="memoSelectionBar" hidden>
    <button type="button" class="memo-selection-btn" id="memoSelectionMemoBtn" data-i18n="selMemo">📝 Memo</button>
    <button type="button" class="memo-selection-btn" id="memoSelectionSearchBtn" data-i18n="selSearch">🔍 Search</button>
    <button type="button" class="memo-selection-btn" id="memoSelectionCopyBtn" data-i18n="selCopy">📋 Copy</button>
  </div>
  <div class="popup-modal" id="listPopup" hidden>
    <div class="popup-card">
      <div class="popup-head">
        <div class="popup-title" id="popupTitle">Items</div>
        <div class="popup-head-actions">
          <button type="button" class="action" id="popupClear" data-i18n="clear" data-i18n-title="clearListTitle" title="Clear list" hidden>Clear</button>
          <button type="button" class="popup-close" id="popupClose" data-i18n-title="closeTitle" title="Close">✕</button>
        </div>
      </div>
      <input type="text" id="popupSearch" data-i18n-ph="phFilter" placeholder="Filter…" autocomplete="off" spellcheck="false" />
      <div id="popupResults" class="popup-results"></div>
      <div class="popup-foot subtle" data-i18n="popupFoot">Click to open · Esc to close</div>
    </div>
  </div>
  <div class="popup-modal" id="folderBrowseModal" hidden>
    <div class="popup-card">
      <div class="popup-head">
        <div class="popup-title" id="folderBrowseTitle" data-i18n="fbTitle">Browse subfolders</div>
        <div class="popup-head-actions">
          <div class="search-sort" role="group" aria-label="Scope">
            <button type="button" class="search-sort-btn active" id="fbScopeFolder" data-i18n="fbScopeFolder" data-i18n-title="fbScopeFolderTitle" title="Browse under the current folder">Subfolders</button>
            <button type="button" class="search-sort-btn" id="fbScopeGit" data-i18n="scopeGit" data-i18n-title="fbScopeGitTitle" title="Browse the whole Git repo">Git repo</button>
          </div>
          <button type="button" class="popup-close" id="folderBrowseClose" data-i18n-title="closeTitle" title="Close">✕</button>
        </div>
      </div>
      <div class="fb-search-wrap">
        <svg class="fb-search-icon" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
          <circle cx="7" cy="7" r="4.5" />
          <line x1="10.5" y1="10.5" x2="13.5" y2="13.5" />
        </svg>
        <input type="search" id="fbSearch" data-i18n-ph="phFbSearch" placeholder="Search filenames…" autocomplete="off" spellcheck="false" />
      </div>
      <div id="fbResults" class="popup-results"></div>
      <div class="popup-foot subtle" data-i18n="fbFoot">Click to open · Esc to close</div>
    </div>
  </div>
  <div class="popup-modal" id="memoConflictModal" hidden>
    <div class="popup-card">
      <div class="popup-head">
        <div class="popup-title">⚠️ <span data-i18n="memoConflict">Memo conflict</span> <span id="memoConflictCount" class="memo-conflict-count"></span></div>
      </div>
      <div id="memoConflictBody" class="memo-conflict-body"></div>
    </div>
  </div>
  <div class="mermaid-lab-modal" id="mermaidLabModal" hidden>
    <div class="mermaid-lab-card">
      <div class="mermaid-lab-head">
        <div class="mermaid-lab-title">&#x1F9EA; Mermaid Playground</div>
        <div class="mermaid-lab-head-actions">
          <button type="button" class="action" id="mermaidLabClearBtn" data-i18n-title="mlClearTitle" title="Clear the editor" data-i18n="mlClear">Clear</button>
          <button type="button" class="action" id="mermaidLabCopyBtn" data-i18n-title="mlCopyTitle" title="Copy text" data-i18n="mlCopy">Copy</button>
          <button type="button" class="popup-close" id="mermaidLabCloseBtn" data-i18n-title="mlCloseTitle" title="Close (Esc)">&#x2715;</button>
        </div>
      </div>
      <div class="mermaid-lab-body">
        <div class="mermaid-lab-pane mermaid-lab-editor-pane">
          <textarea class="mermaid-lab-editor" id="mermaidLabEditor" spellcheck="false" data-i18n-ph="mlPlaceholder" placeholder="Paste or type mermaid source here&#10;e.g.&#10;&#10;flowchart LR&#10;  A --&gt; B"></textarea>
        </div>
        <div class="mermaid-lab-pane mermaid-lab-preview-pane" id="mermaidLabPreview"></div>
      </div>
      <div class="mermaid-lab-foot subtle" data-i18n="mlFoot">Live render &#x2014; Esc to close &#xB7; Click backdrop to dismiss</div>
    </div>
  </div>
  <div class="palette" id="palette" hidden>
    <div class="palette-card">
      <input type="text" id="paletteInput" data-i18n-ph="phPalette" placeholder="Search recent files & folders… (Cmd/Ctrl+K)" autocomplete="off" spellcheck="false" />
      <div class="palette-hint" data-i18n="paletteHint">↑↓ navigate · Enter open · Esc close</div>
      <div id="paletteResults" class="palette-results"></div>
    </div>
  </div>
  <div class="accent-popover" id="accentPopover" hidden role="dialog" data-i18n-aria="accentBtnTitle" aria-label="강조 색상 선택">
    <div class="accent-pop-title" data-i18n="accentTitle">강조 색상</div>
    <div class="accent-swatches" id="accentSwatches"></div>
    <label class="accent-custom-row">
      <span data-i18n="accentCustom">직접 선택</span>
      <input type="color" id="accentCustom" value="#e25aa6" />
    </label>
    <button type="button" class="accent-reset" id="accentReset" data-i18n="accentReset">기본값(핑크)으로</button>
  </div>
  <div class="upd-base-pop" id="updBasePop" hidden role="dialog" data-i18n-aria="updBaseTitle" aria-label="Comparison base">
    <div class="upd-base-pop-title" data-i18n="updBasePopTitle">Compare against</div>
    <div class="upd-base-list" id="updBaseList"></div>
    <div class="upd-base-date-row">
      <label class="upd-base-date-label" for="updBaseDate" data-i18n="updBaseByDate">By date</label>
      <input type="date" id="updBaseDate" class="upd-base-date" />
    </div>
  </div>
  <div class="vcompare" id="vcompare" hidden>
    <div class="vcompare-head">
      <div class="vcompare-title" id="vcompareTitle" data-i18n="vcompareTitle">버전 비교</div>
      <div class="vcompare-nav">
        <button type="button" class="action icon-only" id="vcomparePrev" data-i18n-title="updPrevTitle" title="이전 변경점 (↑)" data-i18n-aria="updPrevTitle" aria-label="이전 변경점">▲</button>
        <span class="vcompare-nav-count" id="vcompareNavCount" data-i18n="vcNoChanges">변경 없음</span>
        <button type="button" class="action icon-only" id="vcompareNext" data-i18n-title="updNextTitle" title="다음 변경점 (↓)" data-i18n-aria="updNextTitle" aria-label="다음 변경점">▼</button>
      </div>
      <button type="button" class="action icon-only vcompare-close" id="vcompareClose" data-i18n-title="closeEscTitle" title="닫기 (Esc)" aria-label="Close">✕</button>
    </div>
    <div class="vcompare-body">
      <div class="vcompare-pane">
        <div class="vcompare-pane-head">
          <span class="vcompare-pane-label before" data-i18n="vcBefore">◀ 이전 (Before)</span>
          <select class="vcompare-select" id="vcompareSelLeft" data-i18n-aria="vcSelLeftAria" aria-label="비교할 이전 버전"></select>
        </div>
        <div class="vcompare-pane-body preview-body side-l" id="vcompareLeft"></div>
      </div>
      <div class="vcompare-pane">
        <div class="vcompare-pane-head">
          <span class="vcompare-pane-label after" data-i18n="vcAfter">이후 (After) ▶</span>
          <select class="vcompare-select" id="vcompareSelRight" data-i18n-aria="vcSelRightAria" aria-label="비교할 이후 버전"></select>
        </div>
        <div class="vcompare-pane-body preview-body side-r" id="vcompareRight"></div>
      </div>
    </div>
  </div>
  <div class="lightbox" id="lightbox" hidden>
    <div class="lightbox-stage" id="lightboxStage"></div>
    <div class="lightbox-scale" id="lightboxScale">100%</div>
    <div class="lightbox-toolbar">
      <button type="button" data-action="zoom-out" title="Zoom out">−</button>
      <button type="button" data-action="reset" title="Reset (Double-click)">⤢</button>
      <button type="button" data-action="zoom-in" title="Zoom in">+</button>
      <button type="button" data-action="annocopy" id="lbAnnoCopyBtn" title="Copy current view as PNG to clipboard">📋</button>
      <button type="button" data-action="annosave" id="lbAnnoSaveBtn" title="Save current view as PNG (with annotations)">💾</button>
      <button type="button" data-action="close" title="Close (Esc)">✕</button>
    </div>
    <div class="lightbox-toolbar lightbox-toolbar-bottom">
      <button type="button" data-action="annodraw" id="lbAnnoDrawBtn" title="Toggle draw mode (left-drag draws)">✎</button>
      <button type="button" class="draw-only" data-action="announdo" id="lbAnnoUndoBtn" title="Undo last action (draw/erase/clear)" hidden>↶</button>
      <button type="button" class="draw-only" data-action="annoredo" id="lbAnnoRedoBtn" title="Redo last undone action" hidden>↷</button>
      <label class="draw-only lb-anno-color-label" id="lbAnnoColorLabel" title="Stroke color" hidden>
        <input type="color" id="lbAnnoColor" value="#ff3b30" />
      </label>
      <label class="draw-only lb-anno-opacity-label" id="lbAnnoOpacityLabel" title="Stroke opacity" hidden>
        <input type="range" id="lbAnnoOpacity" min="0.1" max="1" step="0.05" value="0.5" />
      </label>
      <button type="button" class="draw-only" data-action="annopostit" id="lbAnnoPostitBtn" title="Add a post-it note (each click drops one; drag to move, grip to resize, × to delete)" hidden>📝</button>
      <button type="button" class="draw-only" data-action="annoerase" id="lbAnnoEraseBtn" title="Eraser — click a stroke / post-it to delete" hidden>🩹</button>
      <button type="button" class="draw-only" data-action="annoclear" title="Clear all annotations (undoable)" hidden>🧹</button>
    </div>
    <div class="lightbox-hint" data-i18n="lbHint">Wheel: zoom · Drag: pan · ⌥+Drag: 영역 글자 복사 · Double-click: reset · Esc: close</div>
  </div>
  <div class="toast-stack" id="toastStack" aria-live="polite"></div>

  <script type="module">
    // Pinned to 11.13.0 — the "polished" 11.x release with backward-compat
    // fixes and new diagram types (Venn, Ishikawa). 11.14/11.15 may bring
    // regressions, so bump explicitly after verifying.
    import mermaid from "https://cdn.jsdelivr.net/npm/mermaid@11.13.0/dist/mermaid.esm.min.mjs";
    // htmlLabels:false makes mermaid emit pure SVG <text> labels instead
    // of foreignObject + HTML — required for reliable PNG export, since
    // canvas drawImage drops foreignObject content in most browsers.
    mermaid.initialize({
      startOnLoad: false,
      theme: "default",
      securityLevel: "loose",
      flowchart: { htmlLabels: false },
      class: { htmlLabels: false },
      stateDiagram: { htmlLabels: false },
    });
    // Expose for code that lives outside this module scope (e.g. the
    // Mermaid Playground modal renderer accesses window.mermaid).
    window.mermaid = mermaid;

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
      folderSearchScope: (localStorage.getItem("mdviewer.folderSearchScope") === "git") ? "git" : "folder",
      fbScope: (localStorage.getItem("mdviewer.fbScope") === "git") ? "git" : "folder",
      gitRepoRoot: null, // null = unknown, "" = not a repo, else repo root path
      gitRepoCwd: null,  // cwd the gitRepoRoot was resolved for
      // AI-DLC mode: available only when the repo root holds an aidlc-docs folder.
      // When ON, the file pane shows every aidlc-docs file (recursive), sorted
      // most-recently-updated first. aidlcWanted is the user's persisted intent;
      // aidlcMode is whether it's actually active (intent AND available).
      aidlc: { available: false, root: "", dir: "", files: [] },
      aidlcCwd: null,    // cwd the AI-DLC list was last resolved for
      lang: (function () {
        try { const s = localStorage.getItem("mdviewer.lang"); if (s === "ko" || s === "en") return s; } catch (e) {}
        return (navigator.language || navigator.userLanguage || "").toLowerCase().indexOf("ko") === 0 ? "ko" : "en";
      })(),
      aidlcWanted: localStorage.getItem("mdviewer.aidlcMode") === "1",
      aidlcMode: false,
      updMode: localStorage.getItem("mdviewer.updMode") === "1", // show inline changes vs last version
      updBaseRev: null,          // null = auto (last version); else a commit hash to compare the working copy against
      updBaseLabel: "",          // short label for the chosen base (e.g. "6/4"), shown on the picker button
      gitFileHasHistory: false,                                  // current file is tracked w/ commits
      aidlcPrevSort: null, // sort to restore when leaving AI-DLC mode
      sidebarWidth: Number(localStorage.getItem("mdviewer.sidebarWidth") || 320),
      searchPanelWidth: Number(localStorage.getItem("mdviewer.searchPanelWidth") || 240),
      sidebarCollapsed: localStorage.getItem("mdviewer.sidebarCollapsed") === "1",
      searchPanelCollapsed: localStorage.getItem("mdviewer.searchPanelCollapsed") === "1",
      // Finder-style hidden-file toggle. Persisted; flipped by Cmd/Ctrl+Shift+.
      showHidden: localStorage.getItem("mdviewer.showHidden") === "1",
      searchQueryRight: "",   // distinct from the left-sidebar file-name search
      searchInFileHits: [],   // array of hit objects {marks,line,score,before,after,text}
      searchSortMode: (function () {
        try { return localStorage.getItem("mdviewer.searchSortMode") || "line"; }
        catch (e) { return "line"; }
      })(),
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
    const aidlcToggleEl = document.getElementById("aidlcToggle");
    const recentFilesEl = document.getElementById("recentFiles");
    const recentDirsEl = document.getElementById("recentDirs");
    const searchInputEl = document.getElementById("searchInput");
    const pathInputEl = document.getElementById("pathInput");
    const sortNameEl = document.getElementById("sortName");
    const sortModEl = document.getElementById("sortMod");
    const cwdEl = document.getElementById("cwd");
    const gitRemoteLinkEl = document.getElementById("gitRemoteLink");
    const previewTitleEl = document.getElementById("previewTitle");
    // Click the file name in the preview header to copy it to the clipboard.
    previewTitleEl.addEventListener("click", async () => {
      if (!previewTitleEl.classList.contains("copyable")) return;
      const name = (previewTitleEl.textContent || "").trim();
      if (!name) return;
      const ok = await copyTextToClipboard(name);
      if (ok) showToast(t("toastFileNameCopied") + name, { kind: "ok", icon: "📋" });
      else showToast(t("toastCopyFail"), { kind: "err", icon: "⚠️" });
    });
    const previewMetaEl = document.getElementById("previewMeta");
    const previewBodyEl = document.getElementById("previewBody");
    const kindChipEl = document.getElementById("kindChip");
    const statusTextEl = document.getElementById("statusText");
    const toastStackEl = document.getElementById("toastStack");
    function showToast(message, opts) {
      if (!toastStackEl) return;
      opts = opts || {};
      const t = document.createElement("div");
      t.className = "toast " + (opts.kind === "err" ? "toast-err" : opts.kind === "ok" ? "toast-ok" : "");
      if (opts.icon) {
        const ic = document.createElement("span");
        ic.className = "toast-icon";
        ic.textContent = opts.icon;
        t.appendChild(ic);
      }
      const tx = document.createElement("span");
      tx.textContent = message;
      t.appendChild(tx);
      toastStackEl.appendChild(t);
      // Match the CSS animation total: 180ms in + 1800ms wait + 220ms out
      setTimeout(() => { try { t.remove(); } catch (e) {} }, 2400);
    }
    const scrollTextEl = document.getElementById("scrollText");
    const copyPathBtnEl = document.getElementById("copyPathBtn");
    const copyPathLabelEl = copyPathBtnEl.querySelector(".path-copy-label");
    const copyPathIconEl = copyPathBtnEl.querySelector(".path-copy-icon");
    const previewModeButtonEl = document.getElementById("previewModeButton");
    const editModeButtonEl = document.getElementById("editModeButton");
    const splitModeButtonEl = document.getElementById("splitModeButton");
    const saveButtonEl = document.getElementById("saveButton");
    const floatingTooltipEl = document.getElementById("floatingTooltip");
    const splitterEl = document.getElementById("splitter");
    const rightSplitterEl = document.getElementById("rightSplitter");
    const collapseSidebarEl = document.getElementById("collapseSidebar");
    const revealSidebarEl = document.getElementById("revealSidebar");
    const mermaidLabModalEl = document.getElementById("mermaidLabModal");
    const mermaidLabEditorEl = document.getElementById("mermaidLabEditor");
    const mermaidLabPreviewEl = document.getElementById("mermaidLabPreview");
    const mermaidLabBtnEl = document.getElementById("mermaidLabBtn");
    const mermaidLabCloseBtnEl = document.getElementById("mermaidLabCloseBtn");
    const mermaidLabClearBtnEl = document.getElementById("mermaidLabClearBtn");
    const mermaidLabCopyBtnEl = document.getElementById("mermaidLabCopyBtn");
    const browseSubfoldersBtnEl = document.getElementById("browseSubfoldersBtn");
    const folderBrowseModalEl = document.getElementById("folderBrowseModal");
    const folderBrowseTitleEl = document.getElementById("folderBrowseTitle");
    const fbSearchEl = document.getElementById("fbSearch");
    const fbResultsEl = document.getElementById("fbResults");

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

    // Find a memo's quoted source text in the rendered preview, scroll to it,
    // and flash a highlight. Tries progressively shorter prefixes so it still
    // lands close even if the text shifted slightly. Used by memo backlinks.
    function highlightQuoteInPreview(quote) {
      if (!quote) return false;
      const q = String(quote).replace(/\s+/g, " ").trim();
      if (q.length < 4) return false;
      const lens = [q.length, 60, 40, 24];
      let hit = null;
      walkTextNodes(previewBodyEl, function (node) {
        if (hit) return;
        const txt = node.textContent;
        for (const L of lens) {
          const sub = q.slice(0, Math.min(L, q.length)).trim();
          if (sub.length < 4) continue;
          const i = txt.indexOf(sub);
          if (i >= 0) { hit = { node: node, i: i, len: sub.length }; return; }
        }
      });
      if (!hit) return false;
      try {
        const range = document.createRange();
        range.setStart(hit.node, hit.i);
        range.setEnd(hit.node, hit.i + hit.len);
        const span = document.createElement("span");
        span.className = "backlink-hit";
        range.surroundContents(span);
        span.scrollIntoView({ block: "center", behavior: "smooth" });
        setTimeout(function () {
          const parent = span.parentNode;
          if (!parent) return;
          while (span.firstChild) parent.insertBefore(span.firstChild, span);
          parent.removeChild(span);
          parent.normalize();
        }, 2600);
        return true;
      } catch (e) {
        // Range spanned multiple elements — just scroll to the containing block.
        const el = hit.node.parentElement;
        if (el) el.scrollIntoView({ block: "center", behavior: "smooth" });
        return true;
      }
    }

    // ── Document outline + scroll-spy ──
    // all = every heading; rendered = those within the current level cap.
    const outlineState = { all: [], rendered: [], itemById: {}, activeId: null, maxLevel: 3 };
    try {
      const lv = parseInt(localStorage.getItem("mdviewer.outlineMaxLevel"), 10);
      if (lv === 2 || lv === 3 || lv === 6) outlineState.maxLevel = lv;
    } catch (e) {}

    function buildOutline(headings) {
      const sectionEl = document.getElementById("outlineSection");
      const emptyEl = document.getElementById("outlineEmpty");
      if (!sectionEl) return;
      outlineState.all = Array.prototype.slice.call(headings || []).map(function (h) {
        return { id: h.id, el: h, level: parseInt(h.tagName.slice(1), 10) || 1, text: (h.textContent || "").trim() };
      });
      if (outlineState.all.length < 2) { // not worth an outline
        sectionEl.hidden = true;
        if (emptyEl) emptyEl.hidden = false;
        return;
      }
      sectionEl.hidden = false;
      if (emptyEl) emptyEl.hidden = true;
      renderOutlineItems();
    }
    // (Re)render the visible items honoring the level cap. Deeper headings are
    // omitted from the list; their section still highlights the nearest shown
    // ancestor via scroll-spy.
    function renderOutlineItems() {
      const listEl = document.getElementById("outlineList");
      if (!listEl) return;
      listEl.innerHTML = "";
      outlineState.itemById = {};
      outlineState.rendered = [];
      outlineState.activeId = null;
      for (const h of outlineState.all) {
        if (h.level > outlineState.maxLevel) continue;
        const item = document.createElement("div");
        item.className = "outline-item outline-lvl-" + h.level;
        item.textContent = h.text;
        item.title = h.text;
        (function (id) {
          item.addEventListener("click", function () { scrollToHash(id); setOutlineActive(id); });
        })(h.id);
        listEl.appendChild(item);
        outlineState.itemById[h.id] = item;
        outlineState.rendered.push({ id: h.id, el: h.el });
      }
      updateOutlineActive();
    }
    function setOutlineActive(id) {
      if (outlineState.activeId === id) return;
      const prev = outlineState.itemById[outlineState.activeId];
      if (prev) prev.classList.remove("active");
      const next = outlineState.itemById[id];
      if (next) {
        next.classList.add("active");
        next.scrollIntoView({ block: "nearest" });
      }
      outlineState.activeId = id;
    }
    // Highlight the last *shown* heading at or above the top of the viewport.
    function updateOutlineActive() {
      const hs = outlineState.rendered;
      if (!hs.length) return;
      const top = previewBodyEl.scrollTop + 8;
      let activeId = hs[0].id;
      for (const h of hs) {
        if (h.el.offsetTop <= top) activeId = h.id;
        else break;
      }
      setOutlineActive(activeId);
    }
    const OUTLINE_LEVELS = [2, 3, 6];
    function outlineLevelLabel(lv) { return lv >= 6 ? "All" : ("H1–" + lv); }
    function applyOutlineLevel(lv) {
      outlineState.maxLevel = (lv === 2 || lv === 3 || lv === 6) ? lv : 3;
      try { localStorage.setItem("mdviewer.outlineMaxLevel", String(outlineState.maxLevel)); } catch (e) {}
      const btn = document.getElementById("outlineLevel");
      if (btn) btn.textContent = outlineLevelLabel(outlineState.maxLevel);
      if (outlineState.all.length) renderOutlineItems();
    }

    function decorateRenderedMarkdown() {
      const headings = previewBodyEl.querySelectorAll("h1, h2, h3, h4, h5, h6");
      const seenIds = {};
      for (const heading of headings) {
        let id = heading.id || slugify(heading.textContent || "") || "section";
        // De-duplicate so each heading anchors uniquely (outline + backlinks).
        if (seenIds[id]) { let n = 2; while (seenIds[id + "-" + n]) n++; id = id + "-" + n; }
        seenIds[id] = true;
        heading.id = id;
      }
      buildOutline(headings);

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

      renderMathSafe(previewBodyEl);
    }

    // Render TeX math via KaTeX auto-render. No-op until the deferred KaTeX
    // scripts have loaded; code/pre blocks are ignored so they stay verbatim.
    function renderMathSafe(root) {
      if (!root || typeof window.renderMathInElement !== "function") return;
      try {
        window.renderMathInElement(root, {
          delimiters: [
            { left: "$$", right: "$$", display: true },
            { left: "\\[", right: "\\]", display: true },
            { left: "\\(", right: "\\)", display: false },
            { left: "$", right: "$", display: false },
          ],
          ignoredTags: ["script", "noscript", "style", "textarea", "pre", "code"],
          throwOnError: false,
        });
      } catch (e) { /* malformed math shouldn't break the render */ }
    }
    // KaTeX scripts are deferred; if a file was already rendered before they
    // finished loading, render math once they're available.
    window.addEventListener("load", function () { renderMathSafe(previewBodyEl); });

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

    // For AI-DLC's "folder/file" labels: dim the folder path, bold the
    // filename. Plain filenames (no slash) render unchanged.
    function fileNameHTML(name, query) {
      const idx = name.lastIndexOf("/");
      if (idx < 0) return highlightName(name, query);
      const dir = name.slice(0, idx + 1);
      const base = name.slice(idx + 1);
      return '<span class="fn-dir">' + highlightName(dir, query) + '</span>' +
        '<span class="fn-base">' + highlightName(base, query) + '</span>';
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

    function entryModTimestamp(entry) {
      const v = entry && (entry.mod_time || entry.modTime);
      if (!v) return 0;
      const t = new Date(v).getTime();
      return isNaN(t) ? 0 : t;
    }

    function compareEntries(a, b) {
      if (a.name === "..") return -1;
      if (b.name === "..") return 1;
      if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1;

      let result = 0;
      if (state.sortKey === "mod") {
        const ta = entryModTimestamp(a);
        const tb = entryModTimestamp(b);
        result = ta - tb;
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
      sortModEl.classList.toggle("active", state.sortKey === "mod");
      sortNameEl.dataset.direction = state.sortDirection;
      sortModEl.dataset.direction = state.sortDirection;
    }

    function canEditKind(kind) {
      return kind === "markdown" || kind === "text";
    }

    function updateEditorButtons() {
      const editable = canEditKind(state.selectedKind);
      const previewActive = state.editorMode === "preview";
      const editActive    = state.editorMode === "edit";
      const splitActive   = state.editorMode === "split";
      previewModeButtonEl.classList.toggle("active", previewActive);
      editModeButtonEl   .classList.toggle("active", editActive);
      splitModeButtonEl  .classList.toggle("active", splitActive);
      previewModeButtonEl.setAttribute("aria-selected", previewActive ? "true" : "false");
      editModeButtonEl   .setAttribute("aria-selected", editActive    ? "true" : "false");
      splitModeButtonEl  .setAttribute("aria-selected", splitActive   ? "true" : "false");
      editModeButtonEl .disabled = !editable || !state.selectedPath;
      splitModeButtonEl.disabled = !editable || !state.selectedPath;
      const canSave = editable && state.selectedPath && state.editDirty;
      saveButtonEl.disabled = !canSave;
      saveButtonEl.classList.toggle("is-primary", !!canSave);
      if (typeof updateUpdToggle === "function") updateUpdToggle();
    }

    function setEditorMode(mode) {
      if (mode === "edit" || mode === "split") {
        state.editorMode = mode;
      } else {
        state.editorMode = "preview";
      }
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
      if (entry.name === "..") return entry.path;
      const flag = state.fileFlags[entry.path] || "";
      const lines = [];
      // Filename first so a truncated row in the list still reveals its
      // full name via the hover tooltip.
      if (entry && entry.name) {
        lines.push(entry.name);
      }
      if (flag) {
        lines.push("Status: " + flag.charAt(0).toUpperCase() + flag.slice(1));
      }
      const modVal = entry.mod_time || entry.modTime;
      const modTs = modVal ? new Date(modVal).getTime() : 0;
      const modRel = modTs ? relativeTime(modTs) : "";
      lines.push("Updated: " + formatMetaTime(modVal) + (modRel ? "  (" + modRel + ")" : ""));
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
      // Silent polls + keepSelection (auto-refresh of the file list) must
      // not pop a confirm. Only user-initiated folder navigation that
      // would clear the current file selection triggers the guard.
      const userNav = !options.silent && options.keepSelection !== true;
      if (userNav && !confirmDiscardDirty()) return;
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
      refreshGitRemote();
      refreshGitScope();
      // Refresh the AI-DLC list when the working dir actually changes (skip the
      // 2.5s silent polls, which would otherwise hammer git on every tick).
      if (state.cwd !== state.aidlcCwd) refreshAidlc();
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
      renderFilePane();
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
      el.textContent = isFav ? t("removeFavorite") : t("addToFavorites");
      el.classList.toggle("active", isFav);
      // Distinct colors: accent "add" vs red "remove" so the two states read
      // clearly differently at a glance.
      el.classList.toggle("fav-remove", isFav);
      el.classList.toggle("fav-add", !isFav);
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
        button.querySelector(".file-name").innerHTML = fileNameHTML(entry.name, state.searchQuery.trim());
        button.querySelector(".file-size").textContent = entry.is_dir ? "" : (function () {
          const t = entryModTimestamp(entry);
          return t ? relativeTime(t) : "";
        })();
        button.onclick = () => entry.is_dir
          ? loadDir(entry.path, { historyMode: "push" })
          : selectFile(entry.path, { historyMode: "push" });
        filesEl.appendChild(button);
      }
    }

    // ── Drag-to-reorder favorites (sidebar + All-favorites modal) ──
    // Uses Pointer Events (not HTML5 native drag-and-drop). Native DnD on a
    // tiny absolute-positioned grip proved unreliable; pointer capture works
    // consistently for mouse + touch and is straightforward to reason about.
    function favDragAfter(container, y) {
      const els = Array.from(container.querySelectorAll(".fav-reorderable:not(.fav-dragging)"));
      let closest = { offset: -Infinity, el: null };
      for (const el of els) {
        const box = el.getBoundingClientRect();
        const offset = y - box.top - box.height / 2;
        if (offset < 0 && offset > closest.offset) closest = { offset: offset, el: el };
      }
      return closest.el;
    }
    // Give a row a visible drag handle (⠿) as an affordance. The WHOLE row is
    // draggable (see setupFavReorder) — the grip is just a hint, not the only
    // grab target, so users don't have to hit a 20px sliver to reorder.
    function ensureFavGrip(row) {
      let grip = row.querySelector(":scope > .fav-grip");
      if (grip) return grip;
      grip = document.createElement("span");
      grip.className = "fav-grip";
      grip.textContent = "⠿";
      grip.title = t("favDragReorder");
      row.insertBefore(grip, row.firstChild);
      return grip;
    }
    // Pointer-drag a row within its container. Listeners live on the document so
    // a fast pointer that outruns the row still drives the drag; a small distance
    // threshold distinguishes a reorder from a plain click (which opens the dir).
    function startFavPointerDrag(container, row, downEv) {
      const startX = downEv.clientX, startY = downEv.clientY;
      let dragging = false;
      function onMove(e) {
        if (!dragging) {
          if (Math.abs(e.clientX - startX) < 4 && Math.abs(e.clientY - startY) < 4) return;
          dragging = true;
          row.classList.add("fav-dragging");
        }
        e.preventDefault();
        const after = favDragAfter(container, e.clientY);
        if (after == null) container.appendChild(row);
        else if (after !== row) container.insertBefore(row, after);
      }
      function onUp() {
        document.removeEventListener("pointermove", onMove);
        document.removeEventListener("pointerup", onUp);
        document.removeEventListener("pointercancel", onUp);
        if (!dragging) return; // never crossed the threshold → it was a click
        row.classList.remove("fav-dragging");
        // Swallow the click that fires right after this drag (would open the dir).
        row._favSuppressClick = true;
        setTimeout(function () { row._favSuppressClick = false; }, 0);
        const order = Array.from(container.children)
          .map(function (c) { return c.dataset && c.dataset.path; })
          .filter(Boolean);
        if (typeof container._favOnReorder === "function") container._favOnReorder(order);
      }
      document.addEventListener("pointermove", onMove);
      document.addEventListener("pointerup", onUp);
      document.addEventListener("pointercancel", onUp);
    }
    // Make a list's rows (each with dataset.path) drag-sortable. The entire row
    // is a grab target; action buttons (✎ edit, ▲▼ move) keep their own clicks.
    function setupFavReorder(containerEl, onReorder) {
      containerEl._favOnReorder = onReorder;
      Array.from(containerEl.children).forEach(function (row) {
        if (!row.dataset || !row.dataset.path) return;
        row.classList.add("fav-reorderable");
        ensureFavGrip(row);
        if (row._favBound) return; // rows are rebuilt per render, but guard anyway
        row._favBound = true;
        row.addEventListener("pointerdown", function (e) {
          if (e.button !== 0) return; // left/primary only
          // Let the small action controls handle their own clicks, not a drag.
          if (e.target.closest(".favorite-edit, .fav-move, .popup-edit, .popup-status")) return;
          startFavPointerDrag(containerEl, row, e);
        });
        row.addEventListener("click", function (e) {
          if (row._favSuppressClick) { e.stopPropagation(); e.preventDefault(); }
        }, true);
      });
    }
    // Move a favorite up/down one slot — reliable alternative to drag.
    function moveFavorite(path, delta) {
      const arr = state.favorites.slice();
      const i = arr.indexOf(path);
      const j = i + delta;
      if (i < 0 || j < 0 || j >= arr.length) return;
      const tmp = arr[i]; arr[i] = arr[j]; arr[j] = tmp;
      commitFavoritesOrder(arr);
    }
    // Small ▲▼ reorder control appended to a favorite row.
    function buildFavMoveControl(path) {
      const wrap = document.createElement("div");
      wrap.className = "fav-move";
      const up = document.createElement("button");
      up.type = "button"; up.className = "fav-move-btn"; up.textContent = "▲"; up.title = t("favMoveUp");
      up.addEventListener("click", function (e) { e.stopPropagation(); moveFavorite(path, -1); });
      const down = document.createElement("button");
      down.type = "button"; down.className = "fav-move-btn"; down.textContent = "▼"; down.title = t("favMoveDown");
      down.addEventListener("click", function (e) { e.stopPropagation(); moveFavorite(path, +1); });
      wrap.appendChild(up); wrap.appendChild(down);
      return wrap;
    }
    async function commitFavoritesOrder(order) {
      const seen = {};
      order.forEach(function (p) { seen[p] = true; });
      const full = order.concat(state.favorites.filter(function (p) { return !seen[p]; }));
      state.favorites = full;
      renderFavorites();
      if (!popupEl.hidden && state.popupKind === "favorites") renderPopup();
      try {
        await fetch("/api/favorites/reorder", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ order: full }),
        });
      } catch (e) { /* offline: local order still applied */ }
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
      // Reorder the shown subset; keep any hidden favorites after them.
      setupFavReorder(favoritesEl, function (domOrder) {
        const rest = state.favorites.filter(function (p) { return domOrder.indexOf(p) < 0; });
        commitFavoritesOrder(domOrder.concat(rest));
      });
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

      // Reorder ▲▼ + Edit alias (✎) — visible on hover.
      row.appendChild(buildFavMoveControl(favorite));
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

    // AI-DLC mode: fetch the file list under <gitroot>/aidlc-docs (if present).
    // The list is sorted by the server most-recently-modified first.
    async function refreshAidlc(silent) {
      try {
        const r = await fetch("/api/aidlc?dir=" + encodeURIComponent(state.cwd || ""));
        if (!r.ok) throw 0;
        const data = await r.json();
        const aidlcDir = (data && data.dir) || "";
        const aidlcFiles = (data && Array.isArray(data.files)) ? data.files : [];
        // The flat list spans many subfolders, so label each file by its path
        // relative to aidlc-docs (e.g. "design/components.md") instead of the
        // bare filename — otherwise files sharing a name are indistinguishable.
        for (const f of aidlcFiles) {
          if (aidlcDir && f && f.path && f.path.indexOf(aidlcDir + "/") === 0) {
            f.name = f.path.slice(aidlcDir.length + 1);
          }
        }
        state.aidlc = {
          available: !!(data && data.available),
          root: (data && data.root) || "",
          dir: aidlcDir,
          files: aidlcFiles,
        };
        state.aidlcCwd = state.cwd;
      } catch (e) {
        // Transient poll failure: keep the current list rather than dropping it.
        if (silent) return;
        state.aidlc = { available: false, root: "", dir: "", files: [] };
      }
      applyAidlcMode();
      updateAidlcToggle();
      renderFilePane();
    }

    // applyAidlcMode reconciles state.aidlcMode with the user's intent and the
    // current availability. Entering AI-DLC mode switches the sort to
    // updated-desc (newest first) and remembers the previous sort so it can be
    // restored on exit. Returns true when the active mode actually changed.
    function applyAidlcMode() {
      const avail = !!(state.aidlc && state.aidlc.available);
      const desired = avail && state.aidlcWanted;
      if (desired === state.aidlcMode) return false;
      if (desired) {
        state.aidlcPrevSort = { key: state.sortKey, dir: state.sortDirection };
        state.sortKey = "mod";
        state.sortDirection = "desc";
      } else if (state.aidlcPrevSort) {
        state.sortKey = state.aidlcPrevSort.key;
        state.sortDirection = state.aidlcPrevSort.dir;
        state.aidlcPrevSort = null;
      }
      state.aidlcMode = desired;
      updateSortButtons();
      return true;
    }

    function toggleAidlcMode() {
      state.aidlcWanted = !state.aidlcWanted;
      try { localStorage.setItem("mdviewer.aidlcMode", state.aidlcWanted ? "1" : "0"); } catch (e) {}
      applyAidlcMode();
      updateAidlcToggle();
      renderFilePane();
    }

    function updateAidlcToggle() {
      if (!aidlcToggleEl) return;
      const avail = !!(state.aidlc && state.aidlc.available);
      aidlcToggleEl.hidden = !avail;
      const on = avail && state.aidlcMode;
      aidlcToggleEl.classList.toggle("active", on);
      aidlcToggleEl.setAttribute("aria-checked", on ? "true" : "false");
      const n = (state.aidlc && state.aidlc.files) ? state.aidlc.files.length : 0;
      const stateEl = aidlcToggleEl.querySelector(".aidlc-toggle-state");
      if (stateEl) stateEl.textContent = on ? ("ON · " + n) : "OFF";
    }

    // renderFilePane draws the sidebar file list — the AI-DLC document list when
    // that mode is active, otherwise the current directory.
    function renderFilePane() {
      if (state.aidlcMode && state.aidlc && state.aidlc.available) {
        renderFiles(state.aidlc.files || []);
      } else {
        renderFiles(state.entries);
      }
    }

    function toggleShowAll(buttonId, totalCount) {
      const btn = document.getElementById(buttonId);
      if (!btn) return;
      if (totalCount > SIDEBAR_RECENT_LIMIT) {
        btn.hidden = false;
        btn.textContent = t("showAll") + " (" + totalCount + ")";
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
      // Depth-first IN DOCUMENT ORDER. The previous stack-based version
      // pushed sibling elements onto a LIFO and popped them in reverse,
      // so a doc with H1/H2/H3 ended up visited as H3/H2/H1 — which
      // pushed every "in this file" search result into reverse order
      // (and same-line tiebreaker by idx therefore looked backwards).
      const SKIP = { SCRIPT: 1, STYLE: 1, MARK: 1 };
      function walk(node) {
        for (const child of node.childNodes) {
          if (child.nodeType === 1) {
            // Skip the code line-number gutter — it's not searchable content.
            if (!SKIP[child.tagName] && !(child.classList && child.classList.contains("hljs-ln-numbers"))) walk(child);
          } else if (child.nodeType === 3) {
            if (child.nodeValue && child.nodeValue.length) visit(child);
          }
        }
      }
      walk(root);
    }

    // highlightInFile finds each occurrence of needle in the RENDERED text of
    // previewBodyEl and wraps it in <mark class="search-mark">. It searches the
    // concatenated text of all nodes, so a match that spans inline elements
    // (e.g. **bold**(rest) → <strong>bold</strong>(rest)) is found and may be
    // wrapped across several nodes. Returns hit objects (one per match):
    //   { marks:[el…], line, score, before, after, text }
    function highlightInFile(needle) {
      clearInFileHighlights();
      if (!needle) return [];
      // 1. Concatenate every text node, keeping an offset → node map.
      const map = [];
      let full = "";
      walkTextNodes(previewBodyEl, function (n) {
        const v = n.nodeValue || "";
        map.push({ node: n, start: full.length, end: full.length + v.length });
        full += v;
      });
      // 2. Find all matches in the lowercased concatenation.
      const lowerFull = full.toLowerCase();
      const lowerNeedle = needle.toLowerCase();
      const len = needle.length;
      const matches = [];
      let idx = lowerFull.indexOf(lowerNeedle);
      while (idx >= 0) { matches.push([idx, idx + len]); idx = lowerFull.indexOf(lowerNeedle, idx + len); }
      if (!matches.length) { state.searchInFileHits = []; state.searchInFileFocus = -1; return []; }
      const hits = matches.map(function (m) {
        return { marks: [], line: null, score: 0, text: full.slice(m[0], m[1]),
                 before: full.slice(Math.max(0, m[0] - 40), m[0]), after: full.slice(m[1], m[1] + 40) };
      });
      // 3. Wrap each match's spanning segments per original node. We collected
      //    the map before mutating, and replace each node independently, so the
      //    offsets stay valid throughout.
      for (const entry of map) {
        const node = entry.node, text = node.nodeValue || "";
        const segs = [];
        for (let mi = 0; mi < matches.length; mi++) {
          const a = Math.max(matches[mi][0], entry.start), b = Math.min(matches[mi][1], entry.end);
          if (a < b) segs.push([a - entry.start, b - entry.start, mi]);
        }
        if (!segs.length) continue;
        const parent = node.parentNode;
        if (!parent) continue;
        const frag = document.createDocumentFragment();
        let cur = 0;
        for (const seg of segs) {
          if (seg[0] > cur) frag.appendChild(document.createTextNode(text.slice(cur, seg[0])));
          const mark = document.createElement("mark");
          mark.className = "search-mark";
          mark.textContent = text.slice(seg[0], seg[1]);
          frag.appendChild(mark);
          hits[seg[2]].marks.push(mark);
          cur = seg[1];
        }
        if (cur < text.length) frag.appendChild(document.createTextNode(text.slice(cur)));
        parent.replaceChild(frag, node);
      }
      // 4. Resolve line + priority per hit (from its first mark).
      for (const h of hits) {
        const m0 = h.marks[0];
        h.line = m0 ? lineNumberForHit(m0) : null;
        h.score = m0 ? priorityForHit(h) : 0;
      }
      state.searchInFileHits = hits;
      state.searchInFileFocus = -1;
      return hits;
    }

    // focusHit scrolls to the i-th in-file hit and emphasises it.
    function focusHit(i) {
      const hits = state.searchInFileHits;
      if (!hits.length) return;
      const prev = hits[state.searchInFileFocus];
      if (state.searchInFileFocus >= 0 && prev) {
        for (const m of prev.marks) m.classList.remove("current");
      }
      const clamped = Math.max(0, Math.min(i, hits.length - 1));
      state.searchInFileFocus = clamped;
      const hit = hits[clamped];
      for (const m of hit.marks) m.classList.add("current");
      if (hit.marks[0]) hit.marks[0].scrollIntoView({ behavior: "smooth", block: "center" });
    }

    // Resolve a 1-indexed line number for a hit. Code files: walk up to
    // a <tr> in the .hljs-ln table. Markdown: walk up to any ancestor
    // carrying a data-source-line attribute (set by annotateSourceLines).
    function lineNumberForHit(mark) {
      const tr = mark.closest && mark.closest("tr");
      if (tr) {
        const num = tr.querySelector(".hljs-ln-numbers");
        if (num) {
          const n = parseInt(num.getAttribute("data-line-number") || num.textContent, 10);
          if (!isNaN(n)) return n;
        }
      }
      let el = mark.parentElement;
      while (el && el !== previewBodyEl) {
        const v = el.getAttribute && el.getAttribute("data-source-line");
        if (v != null) {
          const n = parseInt(v, 10);
          if (!isNaN(n)) return n + 1;
        }
        el = el.parentElement;
      }
      return null;
    }

    // Rank a hit by how "important" its containing element looks.
    // Headings outrank emphasis outrank code outrank plain body text.
    // A whole-word boundary bumps the score so matches at term edges
    // float above mid-word matches.
    const _HIT_SCORE_BY_TAG = {
      H1: 100, H2: 80, H3: 65, H4: 55, H5: 50, H6: 45,
      STRONG: 30, B: 30, TH: 25, EM: 18, I: 18,
      CODE: 14, BLOCKQUOTE: 8,
    };
    function _isWordBoundary(c) {
      // ASCII word chars + Hangul syllables are "word"; everything else
      // (spaces, punctuation, end-of-text) is a boundary.
      return !c || !/[A-Za-z0-9_À-ɏ가-힯぀-ヿ一-鿿]/.test(c);
    }
    function priorityForHit(hit) {
      let score = 0;
      for (const mark of hit.marks) {
        let el = mark.parentElement;
        while (el && el !== previewBodyEl) {
          const tag = el.tagName;
          if (tag && _HIT_SCORE_BY_TAG[tag] && score < _HIT_SCORE_BY_TAG[tag]) {
            score = _HIT_SCORE_BY_TAG[tag];
          }
          el = el.parentElement;
        }
      }
      // Whole-word boundary bonus, using the concatenated context around the hit.
      if (_isWordBoundary(hit.before.slice(-1)) && _isWordBoundary(hit.after.slice(0, 1))) {
        score += 20;
      }
      return score;
    }

    // renderInFileResults updates the summary + clickable hit list in the
    // right panel. Each row now shows the line number + a context snippet,
    // and rows are ranked: heading > emphasis > code > body, with a
    // whole-word boundary bonus.
    function renderInFileResults(needle, hits) {
      searchInFileHitsEl.innerHTML = "";
      if (!needle) {
        searchInFileSummaryEl.textContent = t("searchTypeToSearch");
        return;
      }
      if (!hits.length) {
        searchInFileSummaryEl.textContent = t("searchNoMatches");
        return;
      }
      searchInFileSummaryEl.textContent = (state.lang === "ko")
        ? (hits.length + "개 일치")
        : (hits.length + " match" + (hits.length === 1 ? "" : "es"));
      // Build ranked index list, preserving the original hits[] order
      // for focusHit (since it scrolls by index in document order).
      const ranked = hits.map(function (h, i) {
        return { hit: h, idx: i, score: h.score, line: h.line };
      });
      if (state.searchSortMode === "priority") {
        ranked.sort(function (a, b) {
          if (b.score !== a.score) return b.score - a.score;
          return a.idx - b.idx;
        });
      } else {
        // "line" mode (default) — by source line, then by document
        // order within the same line. Hits without a resolvable line
        // sort to the bottom but keep document order.
        ranked.sort(function (a, b) {
          const al = a.line == null ? Infinity : a.line;
          const bl = b.line == null ? Infinity : b.line;
          if (al !== bl) return al - bl;
          return a.idx - b.idx;
        });
      }
      const maxList = 50;
      const shown = ranked.slice(0, maxList);
      for (const item of shown) {
        const h = item.hit;
        const ctxBefore = h.before || "";
        const ctxAfter  = h.after || "";
        const row = document.createElement("div");
        row.className = "search-hit";
        if (item.line != null) {
          const ln = document.createElement("span");
          ln.className = "search-hit-line";
          ln.textContent = "L" + item.line;
          row.appendChild(ln);
        }
        const ctx = document.createElement("span");
        ctx.className = "search-hit-ctx";
        const pre = document.createElement("span");
        pre.textContent = (ctxBefore.length > 30 ? "…" : "") + ctxBefore.slice(-30);
        const hit = document.createElement("span");
        hit.className = "search-hit-needle";
        hit.textContent = h.text;
        const post = document.createElement("span");
        post.textContent = ctxAfter.slice(0, 30) + (ctxAfter.length > 30 ? "…" : "");
        ctx.appendChild(pre);
        ctx.appendChild(hit);
        ctx.appendChild(post);
        row.appendChild(ctx);
        row.addEventListener("click", (function (focusIdx) {
          return function () { focusHit(focusIdx); };
        })(item.idx));
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
      // Git-wide search can take a while; show a loading hint until results land.
      const loadingEl = document.createElement("div");
      loadingEl.className = "search-empty";
      loadingEl.textContent = state.folderSearchScope === "git"
        ? t("searchLoadingGit") : t("searchLoading");
      searchFolderHitsEl.appendChild(loadingEl);
      let results = [];
      try {
        const url = "/api/search?dir=" + encodeURIComponent(state.cwd || "") +
                    "&q=" + encodeURIComponent(needle) +
                    (state.folderSearchScope === "git" ? "&scope=git" : "");
        const r = await fetch(url, { signal: ctrl.signal });
        if (!r.ok) throw new Error(String(r.status));
        results = await r.json();
      } catch (err) {
        if (err && err.name === "AbortError") return;
        searchFolderHitsEl.innerHTML = "";
        const e = document.createElement("div");
        e.className = "search-empty";
        e.textContent = "Search failed.";
        searchFolderHitsEl.appendChild(e);
        return;
      }
      searchFolderHitsEl.innerHTML = ""; // clear the loading hint
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

    // confirmDiscardDirty returns true when it's safe to navigate away
    // (no dirty draft, or the user confirmed losing changes).
    function confirmDiscardDirty() {
      if (!state.editDirty) return true;
      return window.confirm(t("confirmDiscard"));
    }

    async function selectFile(path, options = {}) {
      // Same-file (re-select) skips the guard; only changing files matters.
      if (path !== state.selectedPath && !confirmDiscardDirty()) return;
      // Capture the current scroll BEFORE the render wipes the body, so
      // an auto-refresh of the same file can restore the user's place.
      const prevScrollTop = (options.preserveScroll && path === state.selectedPath)
        ? previewBodyEl.scrollTop
        : null;
      // A different file has its own git history — reset the pinned Changes base.
      if (path !== state.selectedPath) { state.updBaseRev = null; state.updBaseLabel = ""; }
      state.selectedPath = path;
      state.selectedHash = options.hash || "";
      if (!state.cwd || !path.startsWith(state.cwd + "/")) {
        await loadDir(path.replace(/\/[^/]*$/, ""), { keepSelection: true });
      }
      renderFilePane();
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
      renderFilePane();
      previewTitleEl.textContent = data.name;
      previewTitleEl.classList.add("copyable");
      previewTitleEl.title = t("previewClickCopyName");
      previewMetaEl.textContent = new Date(data.mod_time).toLocaleString() + " · " + humanSize(data.size);
      kindChipEl.textContent = data.kind;
      kindChipEl.setAttribute("data-kind", data.kind || "idle");
      updateVersionButton();
      await renderCurrentView(data);
      if (prevScrollTop !== null) {
        // Auto-refresh path: keep the user where they were and flash
        // the pane briefly so the update is noticed.
        previewBodyEl.scrollTop = prevScrollTop;
        previewBodyEl.classList.remove("refresh-flash");
        // Force a reflow so removing + re-adding restarts the animation
        // when refreshes land back-to-back.
        void previewBodyEl.offsetWidth;
        previewBodyEl.classList.add("refresh-flash");
        setTimeout(() => previewBodyEl.classList.remove("refresh-flash"), 1000);
      } else if (state.selectedHash) {
        requestAnimationFrame(() => scrollToHash(state.selectedHash));
      } else {
        previewBodyEl.scrollTop = 0;
      }
      statusTextEl.textContent = (prevScrollTop !== null ? "Updated " : "Showing ") + data.name;
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

    // ── i18n (English / 한국어) ──
    // Auto-detected from the browser, overridable with the header EN/한 toggle.
    // Static markup is localized via data-i18n / data-i18n-title / data-i18n-ph
    // attributes; dynamic strings call t(key). The dictionary grows as more of
    // the UI is converted.
    // Hoisted + lazy so t() works even when called during early init (before
    // this line would otherwise run) — avoids a const temporal-dead-zone error.
    var _I18N;
    function i18nDict() {
      if (_I18N) return _I18N;
      _I18N = {
      en: {
        guideBanner: "Built-in usage guide",
        guideSubtitle: "Select a file from the left to open your content.",
        guideMeta: "Usage guide",
        langTitle: "Language: English — click for 한국어",
        updNoChange: "No changes since the last version.",
        updNoneShown: "no changes",
        version: "Version", versionTitle: "Compare this file's git revisions side by side",
        updToggleText: "Changes", updToggleTitle: "Show what changed since the last version, inline (additions green, deletions red strikethrough)",
        updPrevTitle: "Previous change (↑)", updNextTitle: "Next change (↓)",
        updBaseTitle: "Choose the comparison base", updBasePopTitle: "Compare against",
        updBaseAuto: "Automatic (last version)", updBaseByDate: "By date",
        preview: "Preview", previewTitle: "Preview mode — rendered markdown",
        edit: "Edit", editTitle: "Edit mode — raw source in a textarea (⌘S to save)",
        split: "Split", splitTitle: "Split mode — editor + live preview side by side",
        save: "Save", saveTitle: "Save changes to disk (⌘S)",
        tabOutline: "📑 Outline", tabSearch: "🔍 Search", tabMemo: "📝 Memo",
        secRecentFiles: "Recent files", secRecentFolders: "Recent folders", secFavorites: "Favorites",
        showAll: "Show all", showAllRecentFilesTitle: "Show all recent files",
        showAllRecentFoldersTitle: "Show all recent folders", showAllFavoritesTitle: "Show all favorites",
        toggleCurrent: "Toggle current",
        removeFavorite: "★ Remove favorite", addToFavorites: "Add to favorites",
        phSearchFiles: "Search files", phJumpPath: "Jump to path (Enter)…  e.g. ~/notes/foo.md",
        secOutline: "Outline", outlineLevelTitle: "Heading levels to show (click to change)", outlineCollapseTitle: "Collapse outline", outlineEmpty: "No headings in this document.",
        phSearchFolder: "🔍 Search in this folder…", secInThisFile: "In this file",
        sortLine: "Line", sortLineTitle: "Sort by line position", sortPriority: "Priority", sortPriorityTitle: "Sort by importance (heading first)",
        searchTypeToSearch: "Type to search.", searchNoMatches: "No matches in this file.",
        scopeFolder: "This folder", scopeFolderTitle: "Search the current folder only", scopeGit: "Git repo", scopeGitTitle: "Search the whole enclosing Git repo",
        folderSame: "Same folder", folderGit: "Git repo",
        secMemo: "Memo", memoNew: "＋ New", memoNewTitle: "New memo", memoCopy: "📋 Copy", memoCopyTitle: "Copy with filename header",
        phMemoFilter: "🔍 Search memos…",
        memoSortUpdated: "Updated", memoSortUpdatedTitle: "Sort by last modified", memoSortCreated: "Created", memoSortCreatedTitle: "Sort by created", memoSortTitle: "Title", memoSortTitleTitle: "Sort by title A–Z",
        memoEmpty: "No memos yet. Add one with ＋ New.", memoNoMatch: "No matches",
        phMemoTitle: "Title (optional)", phMemoArea: "A note to remember while reading this file…",
        memoTrash: "Trash", memoTrashEmpty: "Empty", memoTrashEmptyTitle: "Empty trash (permanent delete)", memoTrashToggleTitle: "Deleted memos (click to expand)",
        revealSidebar: "☰ Files", revealSidebarTitle: "Show sidebar", revealPanel: "▤ Panel", revealPanelTitle: "Show panel (Outline · Search · Memo)",
        selMemo: "📝 Memo", selSearch: "🔍 Search", selCopy: "📋 Copy",
        updating: "Updating…", clear: "Clear", clearListTitle: "Clear list", closeTitle: "Close", phFilter: "Filter…", popupFoot: "Click to open · Esc to close",
        fbTitle: "Browse subfolders", fbScopeFolder: "Subfolders", fbScopeFolderTitle: "Browse under the current folder", fbScopeGitTitle: "Browse the whole Git repo", phFbSearch: "Search filenames…", fbFoot: "Click to open · Esc to close",
        memoConflict: "Memo conflict", phPalette: "Search recent files & folders… (Cmd/Ctrl+K)", paletteHint: "↑↓ navigate · Enter open · Esc close",
        toastFileNameCopied: "Copied filename: ", toastCopyFail: "Copy failed",
        toastGitLogFail: "Couldn't load git history", toastNotInGit: "This file isn't in a git repo", toastNoCommits: "This file has no commit history",
        toastMemoRestored: "Memo restored", toastTrashEmptied: "Trash emptied", toastMemoSaved: "Saved the selection as a memo", toastMemoTrashed: "Moved to trash", toastMemoEmpty: "Memo is empty",
        toastSelCopied: "Copied the selection", toastBoxCopied: "Copied the text in the selected region", toastBoxEmpty: "No text in the selected region",
        toastMemoCopiedTitle: "Copied the memo with a title link", toastMemoCopiedFile: "Copied the memo with the filename",
        toastNoText: "No text to copy", toastMermaidCopied: "Copied the mermaid text to the clipboard",
        // batch 4 — diagram/export, version footer, conflicts, mermaid lab, etc.
        toastClipFail: "Copy to clipboard failed",
        toastOtherSession: "{0} change(s) from another session applied",
        toastNoSaveContent: "Nothing to save",
        toastImgSaved: "Saved the image",
        toastNoSaveImg: "Couldn't create an image to save",
        toastPngSaved: "Saved as PNG",
        toastNoImgClip: "This browser doesn't support copying images to the clipboard",
        toastNoCopyContent: "Nothing to copy",
        toastImgClipCopied: "Copied the image to the clipboard",
        toastNoDiagram: "No diagram to copy",
        toastDiagramClipCopied: "Copied the diagram image to the clipboard",
        errSvgEncode: "SVG encoding failed", errCanvasDraw: "Canvas draw failed",
        errPngEncode: "PNG encoding failed (canvas.toBlob null)", errSvgImgLoad: "SVG image load failed",
        errPngConvert: "PNG conversion failed", errImgLoad: "Image load failed", errRender: "Render failed",
        confDeletedNote: "This memo was deleted in another session while you were editing it.",
        confEditNote: "This memo was also edited differently in another session while you were editing it.",
        confMine: "My version", confServer: "Server version",
        confRecreate: "Recreate", confAcceptDelete: "Accept deletion",
        confKeepMine: "Keep mine", confTakeServer: "Use server version", confKeepBoth: "Keep both",
        confRemaining: "({0} left)", confMineTag: "(mine)", confEmpty: "(none)", confEmptyMemo: "(empty memo)",
        relJustNow: "just now", relMinAgo: "{0}m ago", relHourAgo: "{0}h ago", relDayAgo: "{0}d ago",
        trashDeleted: "Deleted", trashRestore: "Restore", trashPurge: "Delete permanently",
        confirmPurgeOne: "Permanently delete this memo?",
        confirmPurgeAll: "Permanently delete {0} memo(s) in the trash?",
        backlinkSource: "Source: ", backlinkTitle: "Go to where this memo was created: ",
        syncEditing: "Editing…", syncSaved: "Saved", syncPending: "Waiting to save…",
        favDragReorder: "Drag to reorder", favMoveUp: "Move up", favMoveDown: "Move down",
        searchLoadingGit: "Searching the whole Git repo… ⏳", searchLoading: "Searching…",
        confirmDiscard: "You have unsaved changes.\n\nIf you continue, your changes will be lost. Leave anyway?",
        beforeUnload: "You have unsaved changes.",
        previewClickCopyName: "Click to copy the file name",
        verRepoOpen: "Open repository: ", verNoRepoURL: "No repository URL",
        verCurrent: "Current version: ", verUpdateDate: "Updated: ", verClickCheck: "Click: check for updates",
        verRecentChanges: "Recent changes:", verDevMode: "Dev mode — self-update unavailable",
        verUpdateBadge: "⬆ Update {0} · ", verUpdateAvail: "{0} new version(s) available",
        verLatest: "Latest: ", verClickPull: "Click to pull · build · restart.",
        verRestartCheckFail: "Couldn't confirm the restart. Please refresh manually.",
        verConfirmUpdate: "Update to the latest version and restart the app?\n(git pull · go build · auto-restart)",
        verUpdating: "Updating… (pull · build)\nPlease wait a moment.",
        verUpdateFail: "Update failed\n\n", verUnknownErr: "Unknown error",
        verUpdateDoneRestart: "Update complete — restarting…\nThe page will refresh automatically.",
        verRestarting: "Restarting…\nThe page will refresh automatically.",
        scopeNotRepo: "The current folder isn't a Git repository",
        vcdChanged: "Changed", vcdShowSource: "</> Source", vcdShowDiagram: "🖼 Diagram",
        vcLoading: "Loading…", vcLoadFail: "Couldn't load this version.",
        vcChangeWord: "changes", vcNoChanges: "No changes",
        vcWorkingCopy: "● Current working copy (saved disk content)", vcLatestTag: "  (latest)",
        vcompareTitle: "Version compare", vcTitlePrefix: "Version compare — ",
        vcBefore: "◀ Before", vcAfter: "After ▶",
        vcSelLeftAria: "Earlier version to compare", vcSelRightAria: "Later version to compare",
        fbLoading: "Loading…", fbLoadFail: "Failed to load",
        fbTitleWith: "{0} subfolders", fbNoMatch: "No matches", fbNoFiles: "No files",
        katexLoading: "(KaTeX loading… type again in a moment to render)",
        mlClearTitle: "Clear the editor", mlClear: "Clear", mlCopyTitle: "Copy text", mlCopy: "Copy",
        mlCloseTitle: "Close (Esc)", mlFoot: "Live render — Esc to close · Click backdrop to dismiss",
        mlPlaceholder: "Paste or type mermaid source here\ne.g.\n\nflowchart LR\n  A --> B",
        mlHint: "Paste mermaid source — or TeX math like $E=mc^2$ — on the left to see it rendered here.",
        mlCopied: "Copied!", mlFailed: "Failed", mlRenderFail: "Render failed:",
        mlBtnTitle: "Mermaid Playground — paste mermaid source, see it rendered live (click diagram to zoom)",
        accentTitle: "Accent color", accentCustom: "Custom", accentReset: "Default (pink)", accentBtnTitle: "Choose accent color",
        browseBtnTitle: "Browse including subfolders",
        aidlcToggleTitle: "View all AI-DLC-generated documents by most-recently-modified (turns on the aidlc-docs folder, sorted by time)",
        versionRepoLinkTitle: "Open repository page",
        closeEscTitle: "Close (Esc)",
        lbHint: "Wheel: zoom · Drag: pan · ⌥+Drag: copy region text · Double-click: reset · Esc: close",
        themeAuto: "Auto", themeLight: "Light", themeDark: "Dark",
        themeTitle: "Theme: {0} (click to cycle)", themeAria: "Theme: {0}",
        lbCloseConfirm: "You have annotations. Closing will discard unsaved drawings.\nSave as PNG (💾) before closing?\n\n[OK] Save now → close\n[Cancel] Close without saving",
      },
      ko: {
        guideBanner: "내장 사용 가이드",
        guideSubtitle: "왼쪽에서 파일을 선택해 내용을 열어보세요.",
        guideMeta: "사용 가이드",
        langTitle: "언어: 한국어 — 클릭 시 English",
        updNoChange: "마지막 버전과 동일 — 변경 없음.",
        updNoneShown: "변경 없음",
        version: "버전", versionTitle: "이 파일의 git 변경 이력을 버전별로 골라 좌/우로 비교",
        updToggleText: "변경 표시", updToggleTitle: "마지막 버전에서 무엇이 바뀌었는지 미리보기에 인라인 표시 (추가=녹색, 삭제=빨강 취소선)",
        updPrevTitle: "이전 변경점 (↑)", updNextTitle: "다음 변경점 (↓)",
        updBaseTitle: "비교 기준 선택", updBasePopTitle: "비교 대상",
        updBaseAuto: "자동 (직전 버전)", updBaseByDate: "날짜로 선택",
        preview: "미리보기", previewTitle: "미리보기 모드 — 렌더된 마크다운",
        edit: "편집", editTitle: "편집 모드 — 원본 텍스트 (⌘S로 저장)",
        split: "분할", splitTitle: "분할 모드 — 편집기 + 실시간 미리보기",
        save: "저장", saveTitle: "디스크에 저장 (⌘S)",
        tabOutline: "📑 개요", tabSearch: "🔍 검색", tabMemo: "📝 메모",
        secRecentFiles: "최근 파일", secRecentFolders: "최근 폴더", secFavorites: "즐겨찾기",
        showAll: "전체 보기", showAllRecentFilesTitle: "최근 파일 전체 보기",
        showAllRecentFoldersTitle: "최근 폴더 전체 보기", showAllFavoritesTitle: "즐겨찾기 전체 보기",
        toggleCurrent: "현재 추가/제거",
        removeFavorite: "★ 즐겨찾기 제거", addToFavorites: "즐겨찾기에 추가",
        phSearchFiles: "파일 검색", phJumpPath: "경로로 점프 (Enter)…  예: ~/notes/foo.md",
        secOutline: "개요", outlineLevelTitle: "표시할 헤딩 레벨 (클릭하여 변경)", outlineCollapseTitle: "개요 접기", outlineEmpty: "이 문서에는 헤딩이 없습니다.",
        phSearchFolder: "🔍 이 폴더에서 검색…", secInThisFile: "이 파일에서",
        sortLine: "줄", sortLineTitle: "줄 위치순 정렬", sortPriority: "중요도", sortPriorityTitle: "중요도순 정렬 (헤딩 우선)",
        searchTypeToSearch: "검색어를 입력하세요.", searchNoMatches: "이 파일에 일치 항목이 없습니다.",
        scopeFolder: "이 폴더", scopeFolderTitle: "현재 폴더만 검색", scopeGit: "Git 전체", scopeGitTitle: "상위 Git 저장소 전체 검색",
        folderSame: "같은 폴더", folderGit: "Git 저장소",
        secMemo: "메모", memoNew: "＋ 새로", memoNewTitle: "새 메모", memoCopy: "📋 복사", memoCopyTitle: "파일명 헤더와 함께 복사",
        phMemoFilter: "🔍 메모 검색…",
        memoSortUpdated: "수정순", memoSortUpdatedTitle: "최근 수정순 정렬", memoSortCreated: "생성순", memoSortCreatedTitle: "생성순 정렬", memoSortTitle: "제목", memoSortTitleTitle: "제목순 정렬 (A–Z)",
        memoEmpty: "메모가 없습니다. ＋ 새로로 추가하세요.", memoNoMatch: "검색 결과 없음",
        phMemoTitle: "제목(선택)", phMemoArea: "이 파일을 보면서 기억해두고 싶은 메모…",
        memoTrash: "휴지통", memoTrashEmpty: "비우기", memoTrashEmptyTitle: "휴지통 비우기 (영구 삭제)", memoTrashToggleTitle: "삭제한 메모 (클릭하여 펼치기)",
        revealSidebar: "☰ 파일", revealSidebarTitle: "사이드바 보기", revealPanel: "▤ 패널", revealPanelTitle: "패널 보기 (개요 · 검색 · 메모)",
        selMemo: "📝 메모", selSearch: "🔍 검색", selCopy: "📋 복사",
        updating: "업데이트 중…", clear: "지우기", clearListTitle: "목록 지우기", closeTitle: "닫기", phFilter: "필터…", popupFoot: "클릭하면 열기 · Esc로 닫기",
        fbTitle: "하위 폴더 탐색", fbScopeFolder: "하위 폴더", fbScopeFolderTitle: "현재 폴더 하위 탐색", fbScopeGitTitle: "Git 저장소 전체 탐색", phFbSearch: "파일명 검색…", fbFoot: "클릭하면 파일 열기 · Esc로 닫기",
        memoConflict: "메모 충돌", phPalette: "최근 파일·폴더 검색… (Cmd/Ctrl+K)", paletteHint: "↑↓ 이동 · Enter 열기 · Esc 닫기",
        toastFileNameCopied: "파일 이름을 복사했어요: ", toastCopyFail: "클립보드 복사 실패",
        toastGitLogFail: "git 이력을 불러오지 못했어요", toastNotInGit: "이 파일은 git 저장소에 없습니다", toastNoCommits: "이 파일의 커밋 이력이 없습니다",
        toastMemoRestored: "메모를 복원했어요", toastTrashEmptied: "휴지통을 비웠어요", toastMemoSaved: "선택한 내용을 메모로 저장했어요", toastMemoTrashed: "휴지통으로 이동했어요", toastMemoEmpty: "메모가 비어 있어요",
        toastSelCopied: "선택한 내용을 복사했어요", toastBoxCopied: "선택 영역의 글자를 복사했어요", toastBoxEmpty: "선택 영역에 글자가 없어요",
        toastMemoCopiedTitle: "메모를 제목 링크와 함께 복사했어요", toastMemoCopiedFile: "메모를 파일명과 함께 복사했어요",
        toastNoText: "복사할 텍스트가 없어요", toastMermaidCopied: "머메이드 텍스트를 클립보드에 복사했어요",
        // batch 4
        toastClipFail: "클립보드 복사 실패",
        toastOtherSession: "다른 세션 변경 {0}건 반영됨",
        toastNoSaveContent: "저장할 콘텐츠가 없어요",
        toastImgSaved: "이미지를 저장했어요",
        toastNoSaveImg: "저장할 이미지를 만들 수 없어요",
        toastPngSaved: "PNG로 저장했어요",
        toastNoImgClip: "이 브라우저는 이미지 클립보드 복사를 지원하지 않아요",
        toastNoCopyContent: "복사할 콘텐츠가 없어요",
        toastImgClipCopied: "이미지를 클립보드에 복사했어요",
        toastNoDiagram: "복사할 다이어그램이 없어요",
        toastDiagramClipCopied: "다이어그램 이미지를 클립보드에 복사했어요",
        errSvgEncode: "SVG 인코딩 실패", errCanvasDraw: "Canvas 그리기 실패",
        errPngEncode: "PNG 인코딩 실패 (canvas.toBlob null)", errSvgImgLoad: "SVG 이미지 로딩 실패",
        errPngConvert: "PNG 변환 실패", errImgLoad: "이미지 로딩 실패", errRender: "렌더 실패",
        confDeletedNote: "이 메모를 편집하는 동안 다른 세션에서 삭제되었습니다.",
        confEditNote: "이 메모를 편집하는 동안 다른 세션에서도 다르게 수정되었습니다.",
        confMine: "내 버전", confServer: "서버 버전",
        confRecreate: "다시 생성", confAcceptDelete: "삭제 수용",
        confKeepMine: "내 버전 유지", confTakeServer: "서버 버전 적용", confKeepBoth: "둘 다 보관",
        confRemaining: "({0}건 남음)", confMineTag: "(내 버전)", confEmpty: "(없음)", confEmptyMemo: "(빈 메모)",
        relJustNow: "방금", relMinAgo: "{0}분 전", relHourAgo: "{0}시간 전", relDayAgo: "{0}일 전",
        trashDeleted: "삭제됨", trashRestore: "복원", trashPurge: "영구 삭제",
        confirmPurgeOne: "이 메모를 영구 삭제할까요?",
        confirmPurgeAll: "휴지통의 메모 {0}개를 영구 삭제할까요?",
        backlinkSource: "출처: ", backlinkTitle: "이 메모를 만든 위치로 이동: ",
        syncEditing: "편집 중…", syncSaved: "저장됨", syncPending: "저장 대기 중…",
        favDragReorder: "드래그하여 순서 변경", favMoveUp: "위로 이동", favMoveDown: "아래로 이동",
        searchLoadingGit: "Git 저장소 전체 검색 중… ⏳", searchLoading: "검색 중…",
        confirmDiscard: "저장되지 않은 변경 사항이 있습니다.\n\n계속 진행하면 변경 내용이 사라집니다. 정말 이동하시겠어요?",
        beforeUnload: "저장되지 않은 변경 사항이 있습니다.",
        previewClickCopyName: "클릭하면 파일 이름 복사",
        verRepoOpen: "저장소 열기: ", verNoRepoURL: "저장소 주소 없음",
        verCurrent: "현재 버전: ", verUpdateDate: "업데이트 날짜: ", verClickCheck: "클릭: 업데이트 확인",
        verRecentChanges: "최근 변경:", verDevMode: "개발 모드 — 자가 업데이트 불가",
        verUpdateBadge: "⬆ 업데이트 {0}개 · ", verUpdateAvail: "새 버전 {0}개 사용 가능",
        verLatest: "최신: ", verClickPull: "클릭하면 pull · 빌드 · 재시작합니다.",
        verRestartCheckFail: "재시작 확인에 실패했어요. 수동으로 새로고침 해주세요.",
        verConfirmUpdate: "최신 버전으로 업데이트하고 앱을 재시작할까요?\n(git pull · go build · 자동 재시작)",
        verUpdating: "업데이트 중… (pull · 빌드)\n잠시만 기다려 주세요.",
        verUpdateFail: "업데이트 실패\n\n", verUnknownErr: "알 수 없는 오류",
        verUpdateDoneRestart: "업데이트 완료 — 재시작 중…\n자동으로 새로고침됩니다.",
        verRestarting: "재시작 중…\n자동으로 새로고침됩니다.",
        scopeNotRepo: "현재 폴더가 Git 저장소가 아닙니다",
        vcdChanged: "변경됨", vcdShowSource: "</> 소스", vcdShowDiagram: "🖼 다이어그램",
        vcLoading: "불러오는 중…", vcLoadFail: "이 버전을 불러올 수 없습니다.",
        vcChangeWord: "변경", vcNoChanges: "변경 없음",
        vcWorkingCopy: "● 현재 작업본 (저장된 디스크 내용)", vcLatestTag: "  (최신)",
        vcompareTitle: "버전 비교", vcTitlePrefix: "버전 비교 — ",
        vcBefore: "◀ 이전 (Before)", vcAfter: "이후 (After) ▶",
        vcSelLeftAria: "비교할 이전 버전", vcSelRightAria: "비교할 이후 버전",
        fbLoading: "불러오는 중…", fbLoadFail: "불러오기 실패",
        fbTitleWith: "{0} 하위 폴더 탐색", fbNoMatch: "검색 결과 없음", fbNoFiles: "파일 없음",
        katexLoading: "(KaTeX 로딩 중… 잠시 후 입력하면 렌더됩니다)",
        mlClearTitle: "에디터 비우기", mlClear: "지우기", mlCopyTitle: "텍스트 복사", mlCopy: "복사",
        mlCloseTitle: "닫기 (Esc)", mlFoot: "실시간 렌더 — Esc로 닫기 · 배경 클릭 시 닫기",
        mlPlaceholder: "여기에 머메이드 소스를 붙여넣거나 입력하세요\n예:\n\nflowchart LR\n  A --> B",
        mlHint: "왼쪽에 머메이드 소스 — 또는 $E=mc^2$ 같은 TeX 수식 — 을 붙여넣으면 여기에 렌더됩니다.",
        mlCopied: "복사됨!", mlFailed: "실패", mlRenderFail: "렌더 실패:",
        mlBtnTitle: "Mermaid Playground — 머메이드 소스를 붙여넣으면 실시간 렌더 (다이어그램 클릭 시 확대)",
        accentTitle: "강조 색상", accentCustom: "직접 선택", accentReset: "기본값(핑크)으로", accentBtnTitle: "강조 색상 선택",
        browseBtnTitle: "하위 폴더 포함 탐색",
        aidlcToggleTitle: "AI-DLC가 생성한 문서 전체를 최근 수정순으로 모아서 봅니다 (켜면 aidlc-docs 폴더의 모든 문서를 시간순으로 정렬)",
        versionRepoLinkTitle: "저장소 페이지 열기",
        closeEscTitle: "닫기 (Esc)",
        lbHint: "휠: 확대 · 드래그: 이동 · ⌥+드래그: 영역 글자 복사 · 더블클릭: 초기화 · Esc: 닫기",
        themeAuto: "자동", themeLight: "밝게", themeDark: "어둡게",
        themeTitle: "테마: {0} (클릭하여 전환)", themeAria: "테마: {0}",
        lbCloseConfirm: "주석이 있어요. 닫으면 저장하지 않은 그림은 사라집니다.\n💾로 PNG 저장 후 닫으시겠어요?\n\n[확인] 지금 저장 → 닫기\n[취소] 그냥 닫기",
      },
      };
      return _I18N;
    }
    function t(key) {
      const M = i18nDict();
      const d = M[state.lang] || M.en;
      if (d && d[key] != null) return d[key];
      return (M.en[key] != null) ? M.en[key] : key;
    }
    // Apply translations to any tagged static element. Re-run on language change.
    function applyI18n() {
      document.querySelectorAll("[data-i18n]").forEach(function (el) { el.textContent = t(el.getAttribute("data-i18n")); });
      document.querySelectorAll("[data-i18n-title]").forEach(function (el) { el.title = t(el.getAttribute("data-i18n-title")); });
      document.querySelectorAll("[data-i18n-ph]").forEach(function (el) { el.setAttribute("placeholder", t(el.getAttribute("data-i18n-ph"))); });
      document.querySelectorAll("[data-i18n-aria]").forEach(function (el) { el.setAttribute("aria-label", t(el.getAttribute("data-i18n-aria"))); });
    }
    function updateLangToggle() {
      const b = document.getElementById("langToggle");
      if (!b) return;
      b.textContent = state.lang === "ko" ? "한" : "EN";
      b.title = t("langTitle");
    }
    function setLang(lang) {
      state.lang = (lang === "ko") ? "ko" : "en";
      try { localStorage.setItem("mdviewer.lang", state.lang); } catch (e) {}
      document.documentElement.setAttribute("lang", state.lang);
      applyI18n();
      updateLangToggle();
      // Refresh labels whose text is set dynamically (not via data-i18n).
      try { if (typeof updateToggleFavoriteLabel === "function") updateToggleFavoriteLabel(); } catch (e) {}
      try { if (typeof renderRecents === "function") renderRecents(); } catch (e) {}
      try { if (typeof renderFavorites === "function") renderFavorites(); } catch (e) {}
      try { if (typeof applyFolderScope === "function") applyFolderScope(state.folderSearchScope); } catch (e) {}
      try { if (typeof renderInFileResults === "function") renderInFileResults(state.searchQueryRight || "", state.searchInFileHits || []); } catch (e) {}
      try { if (typeof updateGitScopeUI === "function") updateGitScopeUI(); } catch (e) {}
      try { if (typeof applyTheme === "function") applyTheme(currentTheme()); } catch (e) {}
      try { if (window.__refreshVersionFooter) window.__refreshVersionFooter(); } catch (e) {}
      try { if (window.__refreshMemoPanel) window.__refreshMemoPanel(); } catch (e) {}
    }

    let usageGuideCache = {}; // keyed by language
    async function showUsageGuide() {
      // Render the embedded usage guide (localized by language) as a friendly
      // welcome / help screen whenever the user has no file selected.
      try {
        const lang = state.lang || "en";
        if (!usageGuideCache[lang]) {
          const res = await fetch("/api/usage?lang=" + encodeURIComponent(lang));
          if (!res.ok) throw new Error(await res.text());
          usageGuideCache[lang] = await res.text();
        }
        const rendered = marked.parse(usageGuideCache[lang]);
        previewBodyEl.innerHTML =
          '<div class="usage-guide">' +
          '<div class="usage-guide-banner">' +
          '<span class="usage-guide-icon">📘</span>' +
          '<div class="usage-guide-text">' +
          '<div class="usage-guide-title">' + escapeHTML(t("guideBanner")) + '</div>' +
          '<div class="usage-guide-subtitle">' + escapeHTML(t("guideSubtitle")) + '</div>' +
          '<a class="usage-guide-git-link" href="https://github.com/neo2544/mdviewer" target="_blank" rel="noopener">↗ MD Viewer on GitHub  ·  github.com/neo2544/mdviewer</a>' +
          '</div>' +
          '</div>' +
          '<div class="usage-guide-body">' + rendered + '</div>' +
          '</div>';
        // marked.parse emits <pre><code class="language-mermaid">...</code></pre>
        // for fenced mermaid blocks — mermaid.run only sees nodes that
        // already have class="mermaid", so we have to wrap them first
        // (same step renderPreview and renderMarkdownInto do).
        const mermaidBlocks = previewBodyEl.querySelectorAll("pre code.language-mermaid");
        for (const code of mermaidBlocks) {
          const wrap = document.createElement("div");
          wrap.className = "mermaid-wrap";
          const host = document.createElement("div");
          host.className = "mermaid";
          host.textContent = code.textContent;
          wrap.appendChild(host);
          code.parentElement.replaceWith(wrap);
        }
        await new Promise((r) => requestAnimationFrame(r));
        try {
          await mermaid.run({ nodes: previewBodyEl.querySelectorAll(".mermaid") });
        } catch (e) {
          console.warn("usage guide mermaid.run failed:", e);
        }
        try { unclipMermaidSvgs(previewBodyEl); } catch (e) {}
        try { highlightCodeBlocks(previewBodyEl); } catch (e) {}
        try { decorateCodeBlocks(previewBodyEl); } catch (e) {}
        decorateRenderedMarkdown();
        try { attachZoomToPreview(); } catch (e) {}
        previewTitleEl.textContent = "Markdown Browser";
        previewTitleEl.classList.remove("copyable");
        previewTitleEl.removeAttribute("title");
        previewMetaEl.textContent = t("guideMeta");
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

      if (state.editorMode === "split" && canEditKind(activeData.kind)) {
        previewBodyEl.innerHTML =
          '<div class="split-view">' +
          '<div class="split-editor">' +
            '<div class="editor-line-highlight" hidden></div>' +
            '<textarea class="editor" spellcheck="false"></textarea>' +
          '</div>' +
          '<div class="split-preview"></div>' +
          '</div>';
        const editorEl = previewBodyEl.querySelector(".editor");
        const splitPrevEl = previewBodyEl.querySelector(".split-preview");
        const editorHighlightEl = previewBodyEl.querySelector(".editor-line-highlight");
        editorEl.value = state.editDraft || activeData.content || "";
        // initial render of the right pane
        await renderMarkdownInto(splitPrevEl, editorEl.value, activeData.kind, { trackSourceLines: true });
        editorEl.focus({ preventScroll: true });
        editorEl.setSelectionRange(editorEl.value.length, editorEl.value.length);

        // ---- Cursor-driven highlight & follow scroll -------------------
        // Compute the editor caret's line and add .source-line-active to
        // the matching block in the preview. The block is scrolled so its
        // mid-height sits roughly at the preview's mid-height.
        function caretLine() {
          const pos = editorEl.selectionStart || 0;
          return editorEl.value.slice(0, pos).split("\n").length - 1;
        }
        function findBlockForLine(line) {
          const nodes = splitPrevEl.querySelectorAll("[data-source-line]");
          let last = null;
          for (const n of nodes) {
            const l = parseInt(n.getAttribute("data-source-line"), 10);
            if (isNaN(l)) continue;
            if (l <= line) last = n;
            else break;
          }
          return last;
        }
        // Whenever WE scroll a pane programmatically (centering the active
        // preview block, etc.), set this flag. Both pane scroll handlers
        // short-circuit while it's true so the proportional sync never
        // bounces a programmatic move into the OTHER pane. The
        // splitSyncSource guard couldn't do this — its !== check passes
        // through re-entries from the same pane that we're driving.
        let _programmaticScroll = false;
        function scrollPreviewBlockToCenter(el) {
          const cRect = splitPrevEl.getBoundingClientRect();
          const eRect = el.getBoundingClientRect();
          const elTopRel = eRect.top - cRect.top + splitPrevEl.scrollTop;
          const target = elTopRel - splitPrevEl.clientHeight / 2 + eRect.height / 2;
          const next = Math.max(0, target);
          if (Math.abs(next - splitPrevEl.scrollTop) < 1) return;
          _programmaticScroll = true;
          splitPrevEl.scrollTop = next;
          requestAnimationFrame(() => { _programmaticScroll = false; });
        }
        // ── caret y measurement via CLONE TEXTAREA ────────────────
        // div mirrors and gradient tricks both bit us — a real textarea
        // is the only thing that wraps identically to the live one (same
        // font metrics, same break algorithm, same scrollbar behavior).
        // Stash one off-screen, copy the relevant styles + value-up-to-
        // caret, and read its scrollHeight to get the exact caret row.
        const _caretClone = document.createElement("textarea");
        _caretClone.setAttribute("aria-hidden", "true");
        _caretClone.tabIndex = -1;
        _caretClone.style.position = "absolute";
        _caretClone.style.top = "0";
        _caretClone.style.left = "-9999px";
        _caretClone.style.visibility = "hidden";
        _caretClone.style.resize = "none";
        _caretClone.style.overflowY = "scroll";
        _caretClone.style.overflowX = "hidden";
        _caretClone.style.height = "1px";
        document.body.appendChild(_caretClone);
        const _cloneProps = ["fontFamily","fontSize","fontWeight","fontStyle","lineHeight","letterSpacing","wordSpacing","tabSize","MozTabSize","whiteSpace","padding","border","boxSizing"];
        function _syncCloneStyles(cs) {
          for (const p of _cloneProps) _caretClone.style[p] = cs[p];
          _caretClone.style.width = editorEl.offsetWidth + "px";
          _caretClone.wrap = editorEl.wrap || "soft";
        }
        function measureCaretContentY() {
          // Returns caret's y position in the textarea's CONTENT
          // coordinate space (paddingTop = 0 at content top).
          const cs = getComputedStyle(editorEl);
          _syncCloneStyles(cs);
          _caretClone.value = editorEl.value.substring(0, editorEl.selectionStart || 0);
          const lineHeight = parseFloat(cs.lineHeight) || 22;
          const paddingTop = parseFloat(cs.paddingTop) || 0;
          const paddingBottom = parseFloat(cs.paddingBottom) || 0;
          // scrollHeight = paddingTop + N*lineHeight + paddingBottom
          // caret sits at the top of the LAST visual row → y in content
          // coords = (N-1)*lineHeight.
          const total = _caretClone.scrollHeight;
          const visualRows = Math.max(1, Math.round((total - paddingTop - paddingBottom) / lineHeight));
          return (visualRows - 1) * lineHeight;
        }

        function offsetOfLineStart(line) {
          const lines = editorEl.value.split("\n");
          let pos = 0;
          for (let i = 0; i < line && i < lines.length; i++) pos += lines[i].length + 1;
          return pos;
        }

        function updateEditorLineHighlight() {
          if (!editorHighlightEl) return;
          const cs = getComputedStyle(editorEl);
          const lineHeight = parseFloat(cs.lineHeight) || 22;
          const borderTop = parseFloat(cs.borderTopWidth) || 0;
          const borderLeft = parseFloat(cs.borderLeftWidth) || 0;
          const paddingTop = parseFloat(cs.paddingTop) || 0;
          const paddingBottom = parseFloat(cs.paddingBottom) || 0;
          const paddingLeft = parseFloat(cs.paddingLeft) || 0;
          const paddingRight = parseFloat(cs.paddingRight) || 0;
          const yInContent = measureCaretContentY();
          const topRel = yInContent - editorEl.scrollTop;
          const innerHeight = editorEl.clientHeight - paddingTop - paddingBottom;
          if (topRel + lineHeight < 0 || topRel > innerHeight) {
            editorHighlightEl.hidden = true;
            return;
          }
          editorHighlightEl.hidden = false;
          editorHighlightEl.style.top = (borderTop + paddingTop + topRel) + "px";
          editorHighlightEl.style.left = (borderLeft + paddingLeft) + "px";
          editorHighlightEl.style.width = (editorEl.clientWidth - paddingLeft - paddingRight) + "px";
          editorHighlightEl.style.height = lineHeight + "px";
        }

        function syncCursorHighlight() {
          // Editor side: reposition the overlay band over the caret line.
          updateEditorLineHighlight();
          if (!splitPrevEl) return;
          const line = caretLine();
          // Preview side shows the WHOLE block that contains the caret —
          // walk the data-source-line markers and take the largest <= line.
          const nodes = splitPrevEl.querySelectorAll("[data-source-line]");
          let activeBlock = null;
          for (const n of nodes) {
            const l = parseInt(n.getAttribute("data-source-line"), 10);
            if (isNaN(l)) continue;
            if (l <= line) activeBlock = n;
            else break;
          }
          const prev = splitPrevEl.querySelectorAll(".source-line-active");
          for (const p of prev) p.classList.remove("source-line-active");
          if (activeBlock) {
            activeBlock.classList.add("source-line-active");
            scrollPreviewBlockToCenter(activeBlock);
          }
        }
        // Hook the standard cursor-movement events: keyup catches arrow
        // keys / Enter; mouseup catches click placement; focus catches
        // returning to the editor.
        editorEl.addEventListener("keyup", syncCursorHighlight);
        editorEl.addEventListener("mouseup", syncCursorHighlight);
        editorEl.addEventListener("focus", syncCursorHighlight);
        // Initial sync once the layout settles.
        requestAnimationFrame(syncCursorHighlight);

        let splitRenderTimer = null;
        editorEl.addEventListener("input", (event) => {
          state.editDraft = event.target.value;
          state.editDirty = state.editDraft !== state.selectedContent;
          updateEditorButtons();
          statusTextEl.textContent = state.editDirty ? "Unsaved changes" : "Editing";
          // Reposition the band on every keystroke so the highlight
          // tracks live edits before the (debounced) preview re-render.
          updateEditorLineHighlight();
          clearTimeout(splitRenderTimer);
          splitRenderTimer = setTimeout(async () => {
            await renderMarkdownInto(splitPrevEl, event.target.value, activeData.kind, { trackSourceLines: true });
            syncCursorHighlight();
          }, 150);
        });

        // --- Proportional scroll sync between the two panes -------------
        // Map each pane's scroll position to a 0..1 ratio of its own
        // scrollable range, then apply the same ratio to the other pane.
        // A reentrancy flag breaks the otherwise-infinite scroll→sync→
        // scroll feedback loop.
        let splitSyncSource = null;
        function syncScrollFrom(src, dst) {
          if (splitSyncSource && splitSyncSource !== src) return;
          splitSyncSource = src;
          // Anchor by TOP of the visible area, not by scrollable-range
          // ratio. This keeps the top line(s) aligned between panes —
          // the bottom may drift when the two have different total
          // heights, which the user accepted as a trade-off.
          if (src.scrollHeight <= 0) { splitSyncSource = null; return; }
          const ratio = src.scrollTop / src.scrollHeight;
          dst.scrollTop = ratio * dst.scrollHeight;
          // Release the source guard on the next frame so the programmatic
          // scroll's own "scroll" event (which will fire) is ignored.
          requestAnimationFrame(() => { splitSyncSource = null; });
        }
        editorEl.addEventListener("scroll", () => {
          updateEditorLineHighlight();
          if (_programmaticScroll) return;
          syncScrollFrom(editorEl, splitPrevEl);
        });
        // The textarea may resize when the splitter is dragged; recompute
        // the band against the new width.
        const resizeObs = new ResizeObserver(() => updateEditorLineHighlight());
        resizeObs.observe(editorEl);
        splitPrevEl.addEventListener("scroll", () => {
          if (_programmaticScroll) return;
          syncScrollFrom(splitPrevEl, editorEl);
        });

        // Clicking a rendered block in the preview moves the editor caret
        // to that source line. Skip clicks on interactive controls
        // (mermaid toolbar buttons, links) so we don't hijack them.
        splitPrevEl.addEventListener("click", (event) => {
          if (event.target.closest && event.target.closest("button, a, input, textarea, [contenteditable]")) return;
          const el = event.target.closest && event.target.closest("[data-source-line]");
          if (!el) return;
          const line = parseInt(el.getAttribute("data-source-line"), 10);
          if (isNaN(line)) return;
          const lines = editorEl.value.split("\n");
          let pos = 0;
          for (let i = 0; i < line && i < lines.length; i++) pos += lines[i].length + 1;
          // Preview-driven click → center the caret in the editor too.
          // Use the clone-textarea measurement (same wrap rules as the
          // live editor) for an accurate caret-y, then scroll so that y
          // sits at the editor's mid-height.
          editorEl.focus({ preventScroll: true });
          editorEl.setSelectionRange(pos, pos);
          const cs2 = getComputedStyle(editorEl);
          const lineHeight = parseFloat(cs2.lineHeight) || 22;
          const paddingTopPx = parseFloat(cs2.paddingTop) || 0;
          const yInContent = measureCaretContentY();
          const desired = yInContent - editorEl.clientHeight / 2 + lineHeight / 2 + paddingTopPx;
          // Guard against bouncing this programmatic scroll back into
          // the preview pane via the proportional sync handler.
          _programmaticScroll = true;
          editorEl.scrollTop = Math.max(0, desired);
          requestAnimationFrame(() => { _programmaticScroll = false; });
          syncCursorHighlight();
        });

        // Disconnect the resize observer and free the clone textarea
        // when the editor leaves the DOM, so we don't leak nodes.
        previewBodyEl.addEventListener("DOMNodeRemoved", function once(e) {
          if (e.target === editorEl || (e.target.contains && e.target.contains(editorEl))) {
            previewBodyEl.removeEventListener("DOMNodeRemoved", once);
            try { resizeObs.disconnect(); } catch (e) {}
            try { if (_caretClone && _caretClone.parentNode) _caretClone.parentNode.removeChild(_caretClone); } catch (e) {}
          }
        });
        return;
      }

      await renderPreview(activeData);
    }

    function annotateSourceLines(container, source) {
      // Map top-level marked tokens (non-space) back to source line numbers
      // and stamp the matching top-level rendered children. Best-effort:
      // marked's token order matches its render output for normal block
      // tokens, which is good enough for the split-view cursor follow.
      try {
        const tokens = marked.lexer(source || "");
        const renderTokens = [];
        let cursor = 0;
        for (const tok of tokens) {
          if (!tok.raw) continue;
          if (tok.type === "space") {
            cursor += tok.raw.length;
            continue;
          }
          const idx = source.indexOf(tok.raw, cursor);
          let line, charOffset;
          if (idx >= 0) {
            line = source.slice(0, idx).split("\n").length - 1;
            charOffset = idx;
            cursor = idx + tok.raw.length;
          } else {
            line = source.slice(0, cursor).split("\n").length - 1;
            charOffset = cursor;
          }
          renderTokens.push({ token: tok, line, charOffset });
        }
        // Recursive walker for list items — handles nested ul/ol too.
        function annotateList(listEl, items, startCharOffset) {
          if (!listEl || !items) return;
          const liNodes = listEl.querySelectorAll(":scope > li");
          let cur = startCharOffset;
          for (let r = 0; r < items.length && r < liNodes.length; r++) {
            const item = items[r];
            if (!item || !item.raw) continue;
            const itemIdx = source.indexOf(item.raw, cur);
            if (itemIdx < 0) continue;
            const itemLine = source.slice(0, itemIdx).split("\n").length - 1;
            liNodes[r].setAttribute("data-source-line", String(itemLine));
            // Look for a nested list token inside this item and recurse.
            if (item.tokens) {
              for (const sub of item.tokens) {
                if (sub && sub.type === "list") {
                  const nestedEl = liNodes[r].querySelector(":scope > ul, :scope > ol");
                  if (nestedEl) annotateList(nestedEl, sub.items, itemIdx);
                }
              }
            }
            cur = itemIdx + item.raw.length;
          }
        }
        const blocks = container.children;
        for (let i = 0; i < blocks.length && i < renderTokens.length; i++) {
          const block = blocks[i];
          const { token, line, charOffset } = renderTokens[i];
          block.setAttribute("data-source-line", String(line));
          // Per-row stamping for tables: header sits on the block's start
          // line, the separator pipe row takes the next line, then data
          // rows follow one per line. Stamping each <tr> lets the
          // cursor-follow zoom into a specific row.
          if (token.type === "table") {
            const tableEl = block.tagName === "TABLE" ? block : block.querySelector("table");
            if (tableEl) {
              const headEl = tableEl.querySelector("thead tr");
              if (headEl) headEl.setAttribute("data-source-line", String(line));
              const bodyRows = tableEl.querySelectorAll("tbody tr");
              for (let r = 0; r < bodyRows.length; r++) {
                bodyRows[r].setAttribute("data-source-line", String(line + 2 + r));
              }
            }
          }
          // Per-item stamping for lists, including nested.
          if (token.type === "list") {
            const listEl = (block.tagName === "UL" || block.tagName === "OL")
              ? block
              : block.querySelector("ul, ol");
            annotateList(listEl, token.items, charOffset);
          }
        }
      } catch (e) { /* annotation is best-effort */ }
    }

    // File-extension → highlight.js language id. Anything not in the
    // table falls back to hljs autodetect, which generally does a good
    // job for popular langs but is worth tightening when we know.
    const EXT_TO_LANG = {
      ".c": "c", ".h": "c",
      ".cpp": "cpp", ".cc": "cpp", ".cxx": "cpp", ".hpp": "cpp", ".hxx": "cpp",
      ".cs": "csharp",
      ".java": "java", ".jsp": "java", ".gradle": "groovy",
      ".kt": "kotlin", ".kts": "kotlin",
      ".scala": "scala", ".sc": "scala",
      ".groovy": "groovy",
      ".go": "go",
      ".rs": "rust",
      ".rb": "ruby", ".rake": "ruby",
      ".php": "php",
      ".swift": "swift",
      ".py": "python", ".pyw": "python",
      ".js": "javascript", ".mjs": "javascript", ".cjs": "javascript", ".jsx": "javascript",
      ".gs": "javascript",
      ".ts": "typescript", ".tsx": "typescript",
      ".dart": "dart",
      ".lua": "lua",
      ".pl": "perl", ".pm": "perl",
      ".r": "r",
      ".sh": "bash", ".bash": "bash", ".zsh": "bash", ".ksh": "bash", ".fish": "bash",
      ".ps1": "powershell", ".psm1": "powershell",
      ".bat": "dos", ".cmd": "dos",
      ".vim": "vim",
      ".xml": "xml", ".xhtml": "xml", ".plist": "xml", ".pom": "xml", ".csproj": "xml",
      ".yaml": "yaml", ".yml": "yaml",
      ".toml": "ini", ".ini": "ini", ".conf": "ini", ".cfg": "ini",
      ".properties": "properties", ".env": "properties",
      ".json": "json",
      ".sql": "sql",
      ".css": "css", ".scss": "scss", ".sass": "scss", ".less": "less",
      ".hs": "haskell",
      ".ex": "elixir", ".exs": "elixir",
      ".erl": "erlang",
      ".clj": "clojure",
      ".fs": "fsharp", ".fsx": "fsharp",
      ".ml": "ocaml", ".mli": "ocaml",
      ".jl": "julia",
      ".cr": "crystal",
      ".coffee": "coffeescript",
      ".tf": "hcl", ".tfvars": "hcl",
      ".proto": "protobuf",
      ".dockerfile": "dockerfile",
      ".diff": "diff", ".patch": "diff",
      ".cmake": "cmake",
      ".mk": "makefile",
      ".log": "plaintext", ".txt": "plaintext", ".csv": "plaintext", ".tsv": "plaintext",
    };
    function langFromPath(path) {
      if (!path) return null;
      const name = path.split("/").pop() || "";
      if (/^Dockerfile(\.|$)/i.test(name)) return "dockerfile";
      if (/^Makefile(\.|$)/i.test(name)) return "makefile";
      if (/^Rakefile(\.|$)/i.test(name)) return "ruby";
      if (/^Gemfile(\.|$)/i.test(name)) return "ruby";
      if (/^CMakeLists\.txt$/i.test(name)) return "cmake";
      const dot = name.lastIndexOf(".");
      if (dot < 0) return null;
      const ext = name.slice(dot).toLowerCase();
      return EXT_TO_LANG[ext] || null;
    }

    function applyLineNumbersManual(code) {
      // Plugin-less line numbering: split innerHTML on \n and rebuild
      // as a 2-column <table>. hljs spans that don't straddle newlines
      // (the common case) keep their highlight; multi-line tokens
      // (block comments, multi-line strings) lose color on rows 2+ —
      // a known trade-off for not pulling in another dep.
      const html = code.innerHTML;
      const lines = html.split("\n");
      const rows = new Array(lines.length);
      for (let i = 0; i < lines.length; i++) {
        const lineHtml = lines[i].length ? lines[i] : "&#8203;"; // ZWSP keeps row height
        rows[i] = '<tr>' +
          '<td class="hljs-ln-line hljs-ln-numbers" data-line-number="' + (i + 1) + '">' + (i + 1) + '</td>' +
          '<td class="hljs-ln-line hljs-ln-code">' + lineHtml + '</td>' +
          '</tr>';
      }
      code.innerHTML = '<table class="hljs-ln"><tbody>' + rows.join("") + '</tbody></table>';
    }

    function renderCodeFile(data) {
      previewBodyEl.innerHTML = "";
      const wrap = document.createElement("div");
      wrap.className = "code-wrap code-file";
      const pre = document.createElement("pre");
      const code = document.createElement("code");
      const lang = langFromPath(state.selectedPath);
      if (lang) code.className = "language-" + lang;
      code.textContent = data.content || "";
      pre.appendChild(code);
      wrap.appendChild(pre);
      previewBodyEl.appendChild(wrap);
      if (typeof hljs !== "undefined") {
        try { hljs.highlightElement(code); } catch (e) {}
      }
      // Always use the manual line-numbering — the plugin requires a
      // ::before CSS rule we don't ship and its async rebuild was
      // racing our checks, which produced the double-numbered output
      // the user reported.
      applyLineNumbersManual(code);
      try { decorateCodeBlocks(previewBodyEl); } catch (e) {}
    }

    function highlightCodeBlocks(container) {
      if (typeof hljs === "undefined") return;
      // Skip mermaid (handled separately) and anything already painted.
      const codes = container.querySelectorAll("pre code:not(.language-mermaid):not(.hljs)");
      for (const code of codes) {
        try { hljs.highlightElement(code); } catch (e) {}
      }
    }

    // Mermaid (especially with htmlLabels:false) sometimes emits a
    // viewBox a few pixels shorter than the actually-drawn content.
    // The SVG defaults to clipping at viewBox bounds, so the bottom
    // row gets chopped in the preview pane. Measure the real content
    // bbox and expand the viewBox to match — the SVG's natural height
    // then grows and the surrounding .mermaid wrapper grows with it,
    // so the white background fully contains the diagram.
    function unclipMermaidSvgs(container) {
      const svgs = container.querySelectorAll(".mermaid > svg");
      for (const svg of svgs) {
        try {
          // Drop any explicit pixel height so the browser derives it
          // from the viewBox + width via aspect ratio. Some mermaid
          // versions set both width and height attrs, which can
          // squash a tall diagram inside a narrower container.
          if (svg.hasAttribute("height") && !svg.getAttribute("height").endsWith("%")) {
            svg.removeAttribute("height");
          }
          // Temporarily disable overflow so getBBox sees the true
          // content extent (not the visible-overflow shadow).
          svg.setAttribute("overflow", "visible");
          svg.style.overflow = "visible";

          let bbox = null;
          try { bbox = svg.getBBox(); } catch (e) {}
          if (!bbox || (!bbox.width && !bbox.height)) continue;
          const vb = svg.viewBox && svg.viewBox.baseVal;
          if (!vb) continue;

          const contentRight = bbox.x + bbox.width;
          const contentBottom = bbox.y + bbox.height;
          const vbRight = vb.x + vb.width;
          const vbBottom = vb.y + vb.height;
          // Only expand — never shrink. Small margin keeps strokes
          // from kissing the edge of the background.
          const margin = 4;
          const newW = Math.max(vb.width, contentRight - vb.x + margin);
          const newH = Math.max(vb.height, contentBottom - vb.y + margin);
          if (newW > vb.width || newH > vb.height) {
            svg.setAttribute("viewBox", vb.x + " " + vb.y + " " + newW + " " + newH);
            // Mermaid's style="max-width:..."px pins the displayed
            // width; widen it too if we grew horizontally so the
            // diagram doesn't get scaled down.
            const styleAttr = svg.getAttribute("style") || "";
            const mw = /max-width\s*:\s*([\d.]+)px/i.exec(styleAttr);
            if (mw && newW > parseFloat(mw[1])) {
              svg.setAttribute("style", styleAttr.replace(/max-width\s*:\s*[\d.]+px/i, "max-width: " + newW + "px"));
            }
          }
        } catch (e) {}
      }
    }

    function decorateCodeBlocks(container) {
      // Wrap every <pre><code> (except mermaid, which is replaced earlier)
      // in a .code-wrap so we can float a copy button + language tag in
      // the corners.
      const pres = container.querySelectorAll("pre");
      for (const pre of pres) {
        if (pre.parentElement && pre.parentElement.classList.contains("code-wrap")) continue;
        const code = pre.querySelector("code");
        if (!code) continue;
        if (code.classList.contains("language-mermaid")) continue;
        const wrap = document.createElement("div");
        wrap.className = "code-wrap";
        // Preserve any data-source-line marker so cursor-follow keeps
        // pointing at this block in split view.
        const srcLine = pre.getAttribute("data-source-line");
        if (srcLine !== null) {
          wrap.setAttribute("data-source-line", srcLine);
          pre.removeAttribute("data-source-line");
        }
        pre.parentNode.insertBefore(wrap, pre);
        wrap.appendChild(pre);

        // Language tag (top-left) — pulled from the language-XYZ class
        // marked emits, ignoring "plaintext" which hljs adds when there
        // was no original hint.
        let lang = "";
        for (const c of code.classList) {
          if (c.indexOf("language-") === 0) {
            const v = c.slice("language-".length);
            if (v && v !== "plaintext" && v !== "undefined") { lang = v; break; }
          }
        }
        if (lang) {
          const tag = document.createElement("span");
          tag.className = "code-lang-tag";
          tag.textContent = lang;
          wrap.appendChild(tag);
        }

        // Copy button.
        const btn = document.createElement("button");
        btn.type = "button";
        btn.className = "code-copy-btn";
        btn.textContent = "Copy";
        btn.title = "Copy code to clipboard";
        btn.addEventListener("click", async (event) => {
          event.preventDefault();
          event.stopPropagation();
          const text = code.innerText;
          try {
            if (navigator.clipboard && navigator.clipboard.writeText) {
              await navigator.clipboard.writeText(text);
            } else {
              const ta = document.createElement("textarea");
              ta.value = text;
              document.body.appendChild(ta);
              ta.select();
              document.execCommand("copy");
              document.body.removeChild(ta);
            }
            const prev = btn.textContent;
            btn.textContent = "Copied!";
            btn.classList.add("copied");
            setTimeout(() => {
              btn.textContent = prev;
              btn.classList.remove("copied");
            }, 1200);
          } catch (e) {
            btn.textContent = "Failed";
            setTimeout(() => { btn.textContent = "Copy"; }, 1200);
          }
        });
        wrap.appendChild(btn);
      }
    }

    async function renderMarkdownInto(container, content, kind, options) {
      options = options || {};
      if (kind !== "markdown" && kind !== "text") {
        container.textContent = content || "";
        return;
      }
      container.innerHTML = marked.parse(content || "");
      if (options.trackSourceLines) annotateSourceLines(container, content || "");
      // mermaid fenced code blocks: wrap and run
      const blocks = container.querySelectorAll("pre code.language-mermaid");
      for (const code of blocks) {
        const wrap = document.createElement("div");
        wrap.className = "mermaid-wrap";
        const host = document.createElement("div");
        host.className = "mermaid";
        host.textContent = code.textContent;
        wrap.appendChild(host);
        // Preserve the source-line marker the wrapper now stands in for.
        const srcLine = code.parentElement.getAttribute("data-source-line");
        if (srcLine !== null) wrap.setAttribute("data-source-line", srcLine);
        code.parentElement.replaceWith(wrap);
      }
      // Let layout settle before mermaid measures. rAF never fires in a hidden
      // tab, so race it with a short timeout to avoid hanging there.
      await new Promise((r) => {
        let done = false;
        const fin = () => { if (!done) { done = true; r(); } };
        requestAnimationFrame(fin);
        setTimeout(fin, 60);
      });
      try {
        await mermaid.run({ nodes: container.querySelectorAll(".mermaid") });
      } catch (e) {
        console.warn("mermaid.run in container failed:", e);
      }
      try { unclipMermaidSvgs(container); } catch (e) {}
      try { highlightCodeBlocks(container); } catch (e) {}
      try { decorateCodeBlocks(container); } catch (e) {}
      try { decorateRenderedMarkdown(); } catch (e) {}
      try { attachZoomToPreview(); } catch (e) {}
    }

    // ── "업데이트 내역": inline diff of the working copy vs the last version ──
    // Renders the working markdown, then overlays change marks: added text/blocks
    // in green, removed text inserted in place with a red strikethrough. Compare
    // target is automatic — uncommitted changes (HEAD→working) when the file is
    // dirty, otherwise the most recent commit (prev→HEAD).
    async function updGitShow(path, rev) {
      try {
        const r = await fetch("/api/git/show?path=" + encodeURIComponent(path) + "&rev=" + encodeURIComponent(rev));
        const j = await r.json();
        return (j && j.ok) ? (j.content || "") : null;
      } catch (e) { return null; }
    }
    function updLcs(a, b) {
      if (!a.length) return b.map(function () { return { t: "add" }; });
      if (!b.length) return a.map(function () { return { t: "del" }; });
      if (a.length * b.length > 4000000) return a.map(function () { return { t: "del" }; }).concat(b.map(function () { return { t: "add" }; }));
      const n = a.length, m = b.length, dp = [];
      for (let i = 0; i <= n; i++) dp.push(new Uint32Array(m + 1));
      for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) dp[i][j] = a[i] === b[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
      const ops = []; let i = 0, j = 0;
      while (i < n && j < m) { if (a[i] === b[j]) { ops.push({ t: "same" }); i++; j++; } else if (dp[i + 1][j] >= dp[i][j + 1]) { ops.push({ t: "del" }); i++; } else { ops.push({ t: "add" }); j++; } }
      while (i < n) { ops.push({ t: "del" }); i++; } while (j < m) { ops.push({ t: "add" }); j++; }
      return ops;
    }
    function updLineDiff(oldText, newText) {
      const a = oldText.split("\n"), b = newText.split("\n");
      let p = 0; while (p < a.length && p < b.length && a[p] === b[p]) p++;
      let sa = a.length, sb = b.length; while (sa > p && sb > p && a[sa - 1] === b[sb - 1]) { sa--; sb--; }
      const ops = updLcs(a.slice(p, sa), b.slice(p, sb));
      const addedNew = new Set(), addedRun = new Map(), pairs = [], removedOld = [];
      let oLine = p, nLine = p, dels = [], adds = [], lastNew = p - 1;
      // Each maximal run of changes (del/add lines uninterrupted by a "same"
      // line) gets one runId, so adjacent changed lines count as a single
      // change point for ▲/▼ navigation — matching the version-compare view.
      let runId = 0;
      function flush() {
        if (!dels.length && !adds.length) return; // no change → no run id
        runId++;
        const k = Math.min(dels.length, adds.length);
        for (let i = 0; i < k; i++) pairs.push({ o: dels[i], n: adds[i], run: runId });
        for (let i = k; i < dels.length; i++) removedOld.push({ o: dels[i], afterNew: lastNew, run: runId });
        for (let i = k; i < adds.length; i++) { addedNew.add(adds[i]); addedRun.set(adds[i], runId); }
        dels = []; adds = [];
      }
      for (const op of ops) {
        if (op.t === "same") { flush(); oLine++; nLine++; lastNew = nLine - 1; }
        else if (op.t === "del") { dels.push(oLine); oLine++; }
        else { adds.push(nLine); nLine++; lastNew = nLine - 1; }
      }
      flush();
      return { addedNew: addedNew, addedRun: addedRun, pairs: pairs, removedOld: removedOld };
    }
    // Split a string into diff tokens so highlighting lands on whole words/
    // numbers instead of fragmenting them char-by-char. A token is a run of
    // word characters (latin/digits/_ + Hangul/CJK/Kana), a run of whitespace,
    // or a single other char (punctuation/symbol). Returns {text, start} so
    // callers can map token spans back to character offsets.
    function tokenizeForDiff(s) {
      const re = /[A-Za-z0-9_À-ɏ가-힯぀-ヿ一-鿿]+|\s+|[^\s]/g;
      const toks = []; let m;
      while ((m = re.exec(s)) !== null) { toks.push({ text: m[0], start: m.index }); }
      return toks;
    }
    function updCharOps(a, b) {
      if (!a.length && !b.length) return [];
      const ops = [];
      function push(t, txt) { const last = ops[ops.length - 1]; if (last && last.t === t) last.text += txt; else ops.push({ t: t, text: txt }); }
      // Token-level LCS: diff whole words/numbers, not individual characters,
      // so a changed token is highlighted as a unit (e.g. 1.58 → 1.59 instead
      // of "1.589"). Trim the common token prefix/suffix first so a small edit
      // in a long string stays within the O(n*m) budget.
      const A = tokenizeForDiff(a).map(function (x) { return x.text; });
      const B = tokenizeForDiff(b).map(function (x) { return x.text; });
      const n = A.length, m = B.length;
      let p = 0; while (p < n && p < m && A[p] === B[p]) p++;
      let ea = n, eb = m; while (ea > p && eb > p && A[ea - 1] === B[eb - 1]) { ea--; eb--; }
      if (p > 0) push("same", A.slice(0, p).join(""));
      const am = A.slice(p, ea), bm = B.slice(p, eb);
      const an = am.length, bn = bm.length;
      if (an * bn > 200000) return null;
      const dp = []; for (let i = 0; i <= an; i++) dp.push(new Uint32Array(bn + 1));
      for (let i = an - 1; i >= 0; i--) for (let j = bn - 1; j >= 0; j--) dp[i][j] = am[i] === bm[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
      let i = 0, j = 0;
      while (i < an && j < bn) { if (am[i] === bm[j]) { push("same", am[i]); i++; j++; } else if (dp[i + 1][j] >= dp[i][j + 1]) { push("del", am[i]); i++; } else { push("add", bm[j]); j++; } }
      while (i < an) { push("del", am[i]); i++; } while (j < bn) { push("add", bm[j]); j++; }
      if (ea < n) push("same", A.slice(ea).join(""));
      return ops;
    }
    function updAnchorForLine(body, line) {
      if (line == null) return null;
      function depthOf(el) { let d = 0; while (el && el !== body) { el = el.parentElement; d++; } return d; }
      const blocks = Array.from(body.querySelectorAll("[data-source-line]"))
        .map(function (el) { return { el: el, line: parseInt(el.getAttribute("data-source-line"), 10), depth: depthOf(el) }; })
        .filter(function (b) { return !isNaN(b.line); })
        .sort(function (a, b) { return (a.line - b.line) || (a.depth - b.depth); });
      if (!blocks.length) return null;
      let lo = 0, hi = blocks.length - 1, idx = 0;
      while (lo <= hi) { const mid = (lo + hi) >> 1; if (blocks[mid].line <= line) { idx = mid; lo = mid + 1; } else hi = mid - 1; }
      return blocks[idx].el;
    }
    function updWrapRanges(container, ranges, cls) {
      if (!ranges || !ranges.length) return;
      const nodes = []; const tw = document.createTreeWalker(container, NodeFilter.SHOW_TEXT, null);
      let nd; while ((nd = tw.nextNode())) nodes.push(nd);
      let pos = 0;
      for (const node of nodes) {
        const text = node.nodeValue; const start = pos, end = pos + text.length; pos = end;
        const local = [];
        for (const r of ranges) { const a = Math.max(r.start, start), b = Math.min(r.end, end); if (a < b) local.push([a - start, b - start]); }
        if (!local.length) continue;
        const frag = document.createDocumentFragment(); let cur = 0;
        for (const seg of local) {
          if (seg[0] > cur) frag.appendChild(document.createTextNode(text.slice(cur, seg[0])));
          const mark = document.createElement("mark"); mark.className = cls; mark.textContent = text.slice(seg[0], seg[1]);
          frag.appendChild(mark); cur = seg[1];
        }
        if (cur < text.length) frag.appendChild(document.createTextNode(text.slice(cur)));
        node.parentNode.replaceChild(frag, node);
      }
    }
    function updInsertAtOffset(container, off, node) {
      const tw = document.createTreeWalker(container, NodeFilter.SHOW_TEXT, null);
      let pos = 0, tn;
      while ((tn = tw.nextNode())) {
        const len = tn.nodeValue.length;
        if (off <= pos + len) {
          const local = off - pos, parent = tn.parentNode;
          if (!parent) return;
          if (local <= 0) { parent.insertBefore(node, tn); return; }
          if (local >= len) { parent.insertBefore(node, tn.nextSibling); return; }
          const after = tn.splitText(local);
          parent.insertBefore(node, after);
          return;
        }
        pos += len;
      }
      container.appendChild(node);
    }
    function updInlineChange(newEl, oldText, newText) {
      const ops = updCharOps(oldText, newText);
      if (!ops) return;
      const addRanges = [], delInserts = [];
      let no = 0;
      for (const op of ops) {
        if (op.t === "same") { no += op.text.length; }
        else if (op.t === "add") { addRanges.push({ start: no, end: no + op.text.length }); no += op.text.length; }
        else { delInserts.push({ off: no, text: op.text }); }
      }
      // Insert deletions first, into the still-plain text, so a deletion never
      // lands inside an addition <mark> — which happens when a whole token is
      // replaced and the deletion offset coincides with the addition's start.
      // Each inserted deletion grows the text, so shift the addition ranges and
      // any later deletions that sit at/after that offset.
      delInserts.sort(function (a, b) { return a.off - b.off; });
      for (let k = 0; k < delInserts.length; k++) {
        const d = delInserts[k];
        const mark = document.createElement("mark"); mark.className = "upd-del"; mark.textContent = d.text;
        updInsertAtOffset(newEl, d.off, mark);
        const grow = d.text.length;
        for (const r of addRanges) { if (r.start >= d.off) { r.start += grow; r.end += grow; } }
        for (let q = k + 1; q < delInserts.length; q++) { if (delInserts[q].off >= d.off) delInserts[q].off += grow; }
      }
      updWrapRanges(newEl, addRanges, "upd-add");
    }
    function updNote(container, msg) {
      const n = document.createElement("div"); n.className = "upd-note"; n.textContent = "📝 " + msg;
      container.insertBefore(n, container.firstChild);
    }
    // Split each fenced code block into per-line rows (like the version-compare
    // view) and stamp each row with its source line, so the Changes overlay can
    // highlight individual code lines instead of tinting the whole <pre>.
    // Code line k maps to source line fence+1+k. Used for both the working copy
    // and the previous-version box.
    function updStampCodeLines(container) {
      const codes = container.querySelectorAll("pre > code");
      for (const code of codes) {
        if (code.classList.contains("language-mermaid")) continue;
        const anc = code.closest("[data-source-line]");
        if (!anc) continue;
        const sl = parseInt(anc.getAttribute("data-source-line"), 10);
        if (isNaN(sl)) continue;
        if (!code.querySelector("table.hljs-ln")) {
          try { applyLineNumbersManual(code); } catch (e) { continue; }
        }
        const rows = code.querySelectorAll("table.hljs-ln tbody tr");
        for (let i = 0; i < rows.length; i++) rows[i].setAttribute("data-source-line", String(sl + 1 + i));
      }
    }
    function updIsCodeRow(el) {
      return !!(el && el.tagName === "TR" && el.closest && el.closest("table.hljs-ln"));
    }
    function annotateUpdateDiff(newContainer, oldContainer, oldContent, newContent) {
      // Break code blocks into per-line rows first so a change inside a fenced
      // block highlights line-by-line (with word-level emphasis) rather than the
      // whole <pre> being treated as one block.
      try { updStampCodeLines(newContainer); } catch (e) {}
      try { updStampCodeLines(oldContainer); } catch (e) {}
      const diff = updLineDiff(oldContent, newContent);
      const oldLines = oldContent.split("\n"), newLines = newContent.split("\n");
      const blank = function (s) { return !s || !s.trim(); };
      const seen = new Set();
      const seenRun = new Set(); // one ▲/▼ anchor per change run (adjacent lines = 1)
      const anchors = []; // change locations, for ▲/▼ navigation
      for (const pr of diff.pairs) {
        if (blank(oldLines[pr.o]) && blank(newLines[pr.n])) continue;
        const ne = updAnchorForLine(newContainer, pr.n), oe = updAnchorForLine(oldContainer, pr.o);
        if (!ne || !oe || seen.has(ne)) continue;
        if ((ne.closest && ne.closest(".mermaid-wrap")) || (ne.classList && ne.classList.contains("mermaid-wrap"))) continue;
        seen.add(ne);
        try {
          if (updIsCodeRow(ne)) {
            // Changed code line: tint the row, emphasize the changed tokens in
            // its code cell only (skip the line-number cell).
            ne.classList.add("upd-code-chg");
            const ncode = ne.querySelector(".hljs-ln-code") || ne;
            const ocode = (updIsCodeRow(oe) ? oe.querySelector(".hljs-ln-code") : null) || oe;
            updInlineChange(ncode, ocode.textContent || "", ncode.textContent || "");
          } else if (ne.tagName === "TR" && oe.tagName === "TR") {
            // Diff each table cell on its own — inserting deleted text directly
            // into a <tr> (across <td> boundaries) corrupts the table layout.
            const nc = ne.children, oc = oe.children;
            for (let c = 0; c < nc.length; c++) {
              const ocell = oc[c];
              if (!ocell) continue;
              updInlineChange(nc[c], ocell.textContent || "", nc[c].textContent || "");
            }
          } else {
            updInlineChange(ne, oe.textContent || "", ne.textContent || "");
          }
        } catch (e) {}
        if (!seenRun.has(pr.run)) { seenRun.add(pr.run); anchors.push(ne); }
      }
      for (const ln of diff.addedNew) {
        if (blank(newLines[ln])) continue;          // blank lines have no rendered block
        const ne = updAnchorForLine(newContainer, ln);
        if (ne && !seen.has(ne)) {
          ne.classList.add(updIsCodeRow(ne) ? "upd-code-add" : "upd-add-block");
          seen.add(ne);
          const run = diff.addedRun.get(ln);
          if (!seenRun.has(run)) { seenRun.add(run); anchors.push(ne); }
        }
      }
      const insertedRemoved = new Set();
      const lastRemovedAfter = new Map(); // afterNew row -> last inserted code row, to keep deletions in order
      for (const rm of diff.removedOld) {
        if (blank(oldLines[rm.o])) continue;        // skip removed blank lines
        const oe = updAnchorForLine(oldContainer, rm.o);
        if (!oe || insertedRemoved.has(oe)) continue; // avoid duplicate inserts for the same block
        insertedRemoved.add(oe);
        if (updIsCodeRow(oe)) {
          // Removed code line: insert a struck-through code row at the matching
          // spot inside the working copy's code table.
          const codeTxt = (oe.querySelector(".hljs-ln-code") || oe).textContent || "";
          if (!codeTxt.trim()) continue;
          const anchorRow = rm.afterNew >= 0 ? updAnchorForLine(newContainer, rm.afterNew) : null;
          if (updIsCodeRow(anchorRow) && anchorRow.parentNode) {
            const tr = document.createElement("tr");
            tr.className = "upd-code-removed-line";
            const numTd = document.createElement("td"); numTd.className = "hljs-ln-line hljs-ln-numbers"; numTd.textContent = "−";
            const codeTd = document.createElement("td"); codeTd.className = "hljs-ln-line hljs-ln-code"; codeTd.textContent = codeTxt;
            tr.appendChild(numTd); tr.appendChild(codeTd);
            const after = lastRemovedAfter.get(anchorRow) || anchorRow; // chain successive deletions in order
            after.parentNode.insertBefore(tr, after.nextSibling);
            lastRemovedAfter.set(anchorRow, tr);
            if (!seenRun.has(rm.run)) { seenRun.add(rm.run); anchors.push(tr); }
            continue;
          }
          // else fall through to the generic removed-block below
        }
        const txt = (oe.textContent || "").replace(/\s+/g, " ").trim();
        if (!txt) continue;
        const block = document.createElement("div");
        block.className = "upd-removed-block";
        block.textContent = txt;
        const anchor = rm.afterNew >= 0 ? updAnchorForLine(newContainer, rm.afterNew) : null;
        if (anchor && anchor.parentNode) anchor.parentNode.insertBefore(block, anchor.nextSibling);
        else newContainer.insertBefore(block, newContainer.firstChild);
        if (!seenRun.has(rm.run)) { seenRun.add(rm.run); anchors.push(block); }
      }
      // Order anchors by document position so ▲/▼ navigation walks top→bottom.
      anchors.sort(function (a, b) {
        const p = a.compareDocumentPosition(b);
        if (p & Node.DOCUMENT_POSITION_FOLLOWING) return -1;
        if (p & Node.DOCUMENT_POSITION_PRECEDING) return 1;
        return 0;
      });
      return anchors;
    }
    // ▲/▼ navigation across the change locations in the Changes overlay.
    let updChanges = [], updNavIdx = -1;
    function buildUpdNav(anchors) {
      updChanges = anchors || [];
      updNavIdx = -1;
      const nav = document.getElementById("updNav");
      const count = document.getElementById("updNavCount");
      const has = state.updMode && updChanges.length > 0;
      if (nav) nav.hidden = !has;
      if (count) count.textContent = updChanges.length ? ("– / " + updChanges.length) : t("updNoneShown");
    }
    function updFlash(el) {
      if (!el) return;
      el.classList.remove("upd-flash"); void el.offsetWidth; el.classList.add("upd-flash");
      setTimeout(function () { try { el.classList.remove("upd-flash"); } catch (e) {} }, 950);
    }
    function updGoToChange(i) {
      if (!updChanges.length) return;
      updNavIdx = (i % updChanges.length + updChanges.length) % updChanges.length;
      const el = updChanges[updNavIdx];
      if (!el) return;
      const br = previewBodyEl.getBoundingClientRect(), er = el.getBoundingClientRect();
      previewBodyEl.scrollTop += (er.top - br.top) - 60;
      updFlash(el);
      const count = document.getElementById("updNavCount");
      if (count) count.textContent = (updNavIdx + 1) + " / " + updChanges.length;
    }
    async function renderUpdateDiff(container, data) {
      const path = state.selectedPath;
      const newContent = (data && data.content) || "";
      // Visible: the working markdown, with source-line tracking for mapping.
      await renderMarkdownInto(container, newContent, "markdown", { trackSourceLines: true });
      let oldContent = null;
      // Pinned base: compare the working copy against the chosen revision.
      if (state.updBaseRev) {
        oldContent = await updGitShow(path, state.updBaseRev);
        if (oldContent == null) {            // rev not in this file's history → revert to auto
          state.updBaseRev = null; state.updBaseLabel = "";
          try { updateUpdBaseLabel(); } catch (e) {}
        }
      }
      // Auto base: dirty → HEAD, clean → previous commit.
      if (oldContent == null && !state.updBaseRev) {
        try {
          const r = await fetch("/api/git/filelog?path=" + encodeURIComponent(path));
          const log = await r.json();
          const norm = function (s) { return (s || "").replace(/\r/g, "").replace(/\s+$/, ""); };
          if (log && log.available && Array.isArray(log.commits) && log.commits.length) {
            const headC = await updGitShow(path, log.commits[0].hash);
            if (headC != null && norm(headC) !== norm(newContent)) {
              oldContent = headC;                                   // dirty: HEAD → working
            } else if (log.commits[1]) {
              oldContent = await updGitShow(path, log.commits[1].hash); // clean: prev → HEAD(=working)
            }
          }
        } catch (e) { /* fall through */ }
      }
      buildUpdNav([]); // reset nav until we have anchors
      if (oldContent == null) { updNote(container, t("updNoChange")); return; }
      // Old version: parse + source-line stamp only (no mermaid/hljs needed — we
      // just read rendered text per line).
      const oldBox = document.createElement("div");
      try { oldBox.innerHTML = marked.parse(oldContent); annotateSourceLines(oldBox, oldContent); } catch (e) { return; }
      let anchors = [];
      try { anchors = annotateUpdateDiff(container, oldBox, oldContent, newContent) || []; } catch (e) {}
      buildUpdNav(anchors);
    }

    async function renderPreview(data) {
      if (data.kind === "markdown") {
        if (state.updMode && state.gitRepoRoot && state.selectedPath) {
          try { await renderUpdateDiff(previewBodyEl, data); return; }
          catch (e) { /* fall back to the normal render below */ }
        }
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
        try { unclipMermaidSvgs(previewBodyEl); } catch (e) {}
        try { highlightCodeBlocks(previewBodyEl); } catch (e) {}
        try { decorateCodeBlocks(previewBodyEl); } catch (e) {}
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
        renderCodeFile(data);
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
      renderFilePane();
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
        // Preserve scroll on auto-refresh and signal the change with a
        // brief flash on the preview surface.
        await selectFile(state.selectedPath, { preserveScroll: true });
      }
    }

    async function refreshCurrentDir() {
      if (!state.cwd) return;
      await loadDir(state.cwd, { keepSelection: true, silent: true });
    }

    // _gitRemoteCwd remembers the dir of the last successful resolve so the
    // 2.5s loadDir poll doesn't re-fetch the remote on every tick.
    var _gitRemoteCwd = null;
    var _currentGitRemote = null; // {name,url,web_url} | null — last resolved

    // applyGitRemoteEverywhere mirrors _currentGitRemote onto the sidebar
    // git remote link. (The welcome banner has a STATIC link to the
    // MD Viewer project; only the sidebar surfaces the *current folder's*
    // remote.)
    function applyGitRemoteEverywhere() {
      var chosen = _currentGitRemote;
      if (!chosen) {
        gitRemoteLinkEl.hidden = true;
        return;
      }
      gitRemoteLinkEl.href = chosen.web_url;
      gitRemoteLinkEl.title = chosen.name + ": " + chosen.url;
      gitRemoteLinkEl.textContent = "↗ " + chosen.name + " on web";
      gitRemoteLinkEl.hidden = false;
    }

    async function refreshGitRemote() {
      if (!state.cwd) {
        _currentGitRemote = null;
        _gitRemoteCwd = null;
        applyGitRemoteEverywhere();
        return;
      }
      // Same folder as the previous resolve → just re-apply (covers the
      // welcome banner being freshly rendered) without a network call.
      if (_gitRemoteCwd === state.cwd) {
        applyGitRemoteEverywhere();
        return;
      }
      var remotes = [];
      try {
        var r = await fetch("/api/git/remotes?dir=" + encodeURIComponent(state.cwd));
        if (!r.ok) return; // network error: keep current visible state
        remotes = await r.json();
      } catch (e) { return; }
      _gitRemoteCwd = state.cwd;
      var chosen = null;
      if (Array.isArray(remotes) && remotes.length) {
        chosen = remotes.find(function (r) { return r.name === "origin" && r.web_url; });
        if (!chosen) chosen = remotes.find(function (r) { return r.web_url; });
      }
      _currentGitRemote = chosen || null;
      applyGitRemoteEverywhere();
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

    let outlineSpyRaf = null;
    previewBodyEl.addEventListener("scroll", () => {
      const maxScroll = Math.max(1, previewBodyEl.scrollHeight - previewBodyEl.clientHeight);
      const percent = Math.min(100, Math.max(0, Math.round(previewBodyEl.scrollTop / maxScroll * 100)));
      scrollTextEl.textContent = "Preview " + percent + "%";
      if (outlineSpyRaf) cancelAnimationFrame(outlineSpyRaf);
      outlineSpyRaf = requestAnimationFrame(updateOutlineActive);
    });

    {
      const outlineToggleEl = document.getElementById("outlineToggle");
      const outlineListEl = document.getElementById("outlineList");
      if (outlineToggleEl && outlineListEl) {
        let collapsed = false;
        try { collapsed = localStorage.getItem("mdviewer.outlineCollapsed") === "1"; } catch (e) {}
        const applyCollapsed = function () {
          outlineListEl.classList.toggle("collapsed", collapsed);
          outlineToggleEl.textContent = collapsed ? "▸" : "▾";
          outlineToggleEl.title = collapsed ? "Expand outline" : "Collapse outline";
        };
        applyCollapsed();
        outlineToggleEl.addEventListener("click", function () {
          collapsed = !collapsed;
          try { localStorage.setItem("mdviewer.outlineCollapsed", collapsed ? "1" : "0"); } catch (e) {}
          applyCollapsed();
        });
      }
      const outlineLevelEl = document.getElementById("outlineLevel");
      if (outlineLevelEl) {
        applyOutlineLevel(outlineState.maxLevel); // set initial label
        outlineLevelEl.addEventListener("click", function () {
          const i = OUTLINE_LEVELS.indexOf(outlineState.maxLevel);
          applyOutlineLevel(OUTLINE_LEVELS[(i + 1) % OUTLINE_LEVELS.length]);
        });
      }
    }

    filesEl.addEventListener("pointerover", (event) => {
      const row = event.target.closest(".file[data-meta]");
      if (!row) return;
      showTooltip(row.dataset.meta, event.clientX, event.clientY, { singleLine: !row.dataset.meta.includes("\n") });
    });

    filesEl.addEventListener("pointermove", (event) => {
      const row = event.target.closest(".file[data-meta]");
      if (!row || !floatingTooltipEl.classList.contains("visible")) return;
      showTooltip(row.dataset.meta, event.clientX, event.clientY, { singleLine: !row.dataset.meta.includes("\n") });
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

    // Browser refresh / tab close: warn if there are unsaved edits.
    // Modern browsers ignore the custom string and show their own dialog,
    // but the returnValue must be set to a truthy string to trigger it.
    window.addEventListener("beforeunload", (event) => {
      if (!state.editDirty) return;
      event.preventDefault();
      event.returnValue = t("beforeUnload");
      return event.returnValue;
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
    {
      const updT = document.getElementById("updToggle");
      if (updT) updT.onclick = async () => {
        state.updMode = !state.updMode;
        try { localStorage.setItem("mdviewer.updMode", state.updMode ? "1" : "0"); } catch (e) {}
        updateUpdToggle();
        // Re-render in place (no reload) and keep the reader's scroll position.
        if (state.editorMode === "preview" && state.selectedPath) {
          const top = previewBodyEl.scrollTop;
          await renderCurrentView();
          previewBodyEl.scrollTop = top;
          // Diff annotations / mermaid can shift layout a frame later; re-apply.
          requestAnimationFrame(() => { previewBodyEl.scrollTop = top; });
        }
      };
      const updPrevB = document.getElementById("updPrev");
      const updNextB = document.getElementById("updNext");
      if (updPrevB) updPrevB.onclick = () => updGoToChange(updNavIdx - 1);
      if (updNextB) updNextB.onclick = () => updGoToChange(updNavIdx + 1);
    }

    // ── "Changes" comparison-base picker (commit list + date) ──
    // null base = auto (last version). Choosing a commit/date pins the overlay
    // to compare the working copy against that revision instead.
    function updateUpdBaseLabel() {
      const btn = document.getElementById("updBaseBtn");
      const lbl = document.getElementById("updBaseLabel");
      if (!btn || !lbl) return;
      lbl.textContent = state.updBaseRev ? (state.updBaseLabel || "") : "";
      btn.classList.toggle("custom", !!state.updBaseRev);
    }
    async function setUpdBase(rev, label) {
      state.updBaseRev = rev || null;
      state.updBaseLabel = rev ? (label || "") : "";
      updateUpdBaseLabel();
      if (state.editorMode === "preview" && state.selectedPath && state.updMode) {
        const top = previewBodyEl.scrollTop;
        await renderCurrentView();
        previewBodyEl.scrollTop = top;
        requestAnimationFrame(() => { previewBodyEl.scrollTop = top; });
      }
    }
    {
      const baseBtn = document.getElementById("updBaseBtn");
      const pop = document.getElementById("updBasePop");
      const listEl = document.getElementById("updBaseList");
      const dateEl = document.getElementById("updBaseDate");
      let baseCommits = []; // newest-first [{hash, short, date, subject}]
      const pad2 = function (n) { return String(n).padStart(2, "0"); };
      function fmtCommitDate(iso) {
        const ts = Date.parse(iso);
        if (isNaN(ts)) return iso || "";
        const d = new Date(ts);
        return d.getFullYear() + "-" + pad2(d.getMonth() + 1) + "-" + pad2(d.getDate()) + " " + pad2(d.getHours()) + ":" + pad2(d.getMinutes());
      }
      function shortDate(iso) {
        const ts = Date.parse(iso);
        if (isNaN(ts)) return "";
        const d = new Date(ts);
        return (d.getMonth() + 1) + "/" + d.getDate();
      }
      function closePop() { if (pop) pop.hidden = true; }
      function renderBaseList() {
        if (!listEl) return;
        listEl.innerHTML = "";
        const auto = document.createElement("div");
        auto.className = "upd-base-item" + (state.updBaseRev ? "" : " active");
        auto.textContent = t("updBaseAuto");
        auto.addEventListener("click", function () { setUpdBase(null); closePop(); });
        listEl.appendChild(auto);
        for (const c of baseCommits) {
          const item = document.createElement("div");
          item.className = "upd-base-item" + (state.updBaseRev === c.hash ? " active" : "");
          const d = document.createElement("span"); d.className = "ub-date"; d.textContent = fmtCommitDate(c.date);
          const h = document.createElement("span"); h.className = "ub-hash"; h.textContent = c.short || (c.hash || "").slice(0, 7);
          const s = document.createElement("span"); s.className = "ub-subj"; s.textContent = c.subject || "";
          item.appendChild(d); item.appendChild(h); item.appendChild(s);
          item.addEventListener("click", (function (cc) { return function () { setUpdBase(cc.hash, shortDate(cc.date)); closePop(); }; })(c));
          listEl.appendChild(item);
        }
      }
      async function openPop() {
        if (!pop || !baseBtn) return;
        const r = baseBtn.getBoundingClientRect();
        pop.hidden = false;                       // unhide first so offsetWidth is real
        const w = pop.offsetWidth || 320;
        pop.style.top = (r.bottom + 6) + "px";
        pop.style.left = Math.max(8, Math.min(r.left, window.innerWidth - w - 8)) + "px";
        if (listEl) listEl.innerHTML = "<div class='upd-base-item'>" + t("searchLoading") + "</div>";
        try {
          const resp = await fetch("/api/git/filelog?path=" + encodeURIComponent(state.selectedPath));
          const log = await resp.json();
          baseCommits = (log && log.available && Array.isArray(log.commits)) ? log.commits : [];
        } catch (e) { baseCommits = []; }
        renderBaseList();
      }
      if (baseBtn) baseBtn.addEventListener("click", function (e) {
        e.stopPropagation();
        if (pop && pop.hidden) openPop(); else closePop();
      });
      if (dateEl) dateEl.addEventListener("change", function () {
        const v = dateEl.value; if (!v) return;
        const target = Date.parse(v + "T23:59:59"); // last commit on/before that day
        let pick = null;
        for (const c of baseCommits) { const ct = Date.parse(c.date); if (!isNaN(ct) && ct <= target) { pick = c; break; } }
        if (pick) { setUpdBase(pick.hash, shortDate(pick.date)); closePop(); }
      });
      document.addEventListener("click", function (e) {
        if (!pop || pop.hidden) return;
        if (pop.contains(e.target) || (baseBtn && baseBtn.contains(e.target))) return;
        closePop();
      });
      document.addEventListener("keydown", function (e) { if (e.key === "Escape" && pop && !pop.hidden) closePop(); });
    }

    editModeButtonEl.onclick = () => {
      if (!canEditKind(state.selectedKind)) return;
      setEditorMode("edit");
    };
    splitModeButtonEl.onclick = () => {
      if (!canEditKind(state.selectedKind)) return;
      setEditorMode("split");
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

    // Folder-search scope: current folder vs the whole enclosing git repo.
    function applyFolderScope(scope) {
      let next = (scope === "git") ? "git" : "folder";
      if (next === "git" && state.gitRepoRoot === "") next = "folder"; // not a repo → ignore git
      state.folderSearchScope = next;
      try { localStorage.setItem("mdviewer.folderSearchScope", state.folderSearchScope); } catch (e) {}
      const btnFolder = document.getElementById("searchScopeFolder");
      const btnGit = document.getElementById("searchScopeGit");
      const titleEl = document.getElementById("searchFolderTitle");
      if (btnFolder) btnFolder.classList.toggle("active", state.folderSearchScope === "folder");
      if (btnGit) btnGit.classList.toggle("active", state.folderSearchScope === "git");
      if (titleEl) titleEl.textContent = state.folderSearchScope === "git" ? t("folderGit") : t("folderSame");
      if (state.searchQueryRight) runFolderSearch(state.searchQueryRight);
    }
    {
      const btnFolder = document.getElementById("searchScopeFolder");
      const btnGit = document.getElementById("searchScopeGit");
      if (btnFolder) btnFolder.addEventListener("click", function () { applyFolderScope("folder"); });
      if (btnGit) btnGit.addEventListener("click", function () { applyFolderScope("git"); });
      applyFolderScope(state.folderSearchScope); // set initial active button + title
    }

    // Right-panel tabs: Outline / Search / Memo.
    function setPanelTab(name) {
      const tab = (name === "outline" || name === "memo") ? name : "search";
      try { localStorage.setItem("mdviewer.panelTab", tab); } catch (e) {}
      const tabs = document.querySelectorAll(".panel-tab");
      for (const t of tabs) t.classList.toggle("active", t.dataset.tab === tab);
      const panes = document.querySelectorAll(".panel-pane");
      for (const p of panes) p.hidden = (p.dataset.pane !== tab);
      if (tab === "search") {
        const inp = document.getElementById("searchPanelInput");
        if (inp) setTimeout(function () { try { inp.focus(); } catch (e) {} }, 0);
      }
    }
    {
      const tabBtns = document.querySelectorAll(".panel-tab");
      for (const btn of tabBtns) {
        btn.addEventListener("click", function () { setPanelTab(btn.dataset.tab); });
      }
      let initial = "search";
      try { initial = localStorage.getItem("mdviewer.panelTab") || "search"; } catch (e) {}
      setPanelTab(initial);
    }

    // ── App self-update (git pull + rebuild + restart) ──
    (function setupSelfUpdate() {
      // Single version/update display — the sidebar footer.
      const wrap = document.getElementById("sidebarVersionWrap");
      const el = document.getElementById("sidebarVersion");
      const repoLink = document.getElementById("versionRepoLink");
      const overlay = document.getElementById("updateOverlay");
      const overlayMsg = document.getElementById("updateOverlayMsg");
      if (!el || !wrap) return;
      let canUpdate = false;       // self-update possible (real/installed binary)
      let updateBehind = 0;        // commits available; >0 → clicking updates
      let repoURL = "";            // origin remote browser URL (for the 🏷 link)

      function fmtDate(iso) {
        if (!iso) return "";
        const t = Date.parse(iso);
        if (isNaN(t)) return iso;
        const d = new Date(t);
        return d.getFullYear() + "-" + String(d.getMonth() + 1).padStart(2, "0") + "-" + String(d.getDate()).padStart(2, "0");
      }
      function showOverlay(msg, spinning) {
        if (overlayMsg) overlayMsg.textContent = msg || "";
        const sp = overlay.querySelector(".update-spinner");
        if (sp) sp.style.display = spinning === false ? "none" : "";
        overlay.hidden = false;
      }
      function hideOverlay() { overlay.hidden = true; }

      async function loadVersion() {
        try {
          const r = await fetch("/api/version");
          const v = await r.json();
          canUpdate = !!v.canUpdate;
          repoURL = v.repoURL || "";
          if (repoLink) {
            repoLink.title = repoURL ? (t("verRepoOpen") + repoURL) : t("verNoRepoURL");
            repoLink.style.cursor = repoURL ? "pointer" : "default";
          }
          if (v.current) {
            const date = fmtDate(v.current.date);
            // "MD Viewer Version : main bc463f2 · 2026-05-31" (🏷 = repo link)
            el.dataset.base = "MD Viewer Version : " + (v.branch ? (v.branch + " ") : "") +
              v.current.hash + (date ? (" · " + date) : "");
            el.textContent = el.dataset.base;
            let tip = t("verCurrent") + (v.branch ? (v.branch + " ") : "") + v.current.hash +
              (v.current.subject ? ("\n" + v.current.subject) : "") +
              (date ? ("\n" + t("verUpdateDate") + date) : "") +
              "\n" + t("verClickCheck");
            if (Array.isArray(v.log) && v.log.length) {
              tip += "\n\n" + t("verRecentChanges");
              for (const c of v.log) tip += "\n" + (c.date || "") + "  " + (c.hash || "") + "  " + (c.subject || "");
            }
            el.title = tip;
            wrap.hidden = false;
          } else {
            el.dataset.base = v.devMode ? "MD Viewer Version : dev" : "";
            el.textContent = el.dataset.base;
            el.title = v.devMode ? t("verDevMode") : "";
            wrap.hidden = !v.devMode;
          }
        } catch (e) {}
      }

      async function checkForUpdate() {
        if (!canUpdate) return;
        try {
          const r = await fetch("/api/version/check");
          const d = await r.json();
          updateBehind = (d && d.behind) ? d.behind : 0;
          if (updateBehind > 0) {
            el.classList.add("update-available");
            el.textContent = t("verUpdateBadge").replace("{0}", updateBehind) + (el.dataset.base || "");
            el.title = t("verUpdateAvail").replace("{0}", updateBehind) +
              (d.latest && d.latest.subject ? ("\n" + t("verLatest") + d.latest.subject) : "") +
              "\n" + t("verClickPull");
          } else {
            el.classList.remove("update-available");
            if (el.dataset.base) el.textContent = el.dataset.base;
          }
        } catch (e) {}
      }

      async function pollUntilBack() {
        for (let i = 0; i < 120; i++) {
          await new Promise(function (r) { setTimeout(r, 1000); });
          try {
            const r = await fetch("/api/version", { cache: "no-store" });
            if (r.ok) { location.reload(); return; }
          } catch (e) { /* still restarting */ }
        }
        showOverlay(t("verRestartCheckFail"), false);
      }

      async function runUpdate() {
        if (!window.confirm(t("verConfirmUpdate"))) return;
        showOverlay(t("verUpdating"), true);
        try {
          const r = await fetch("/api/update", { method: "POST" });
          const d = await r.json();
          if (!d.ok) {
            showOverlay(t("verUpdateFail") + (d.message || t("verUnknownErr")), false);
            setTimeout(hideOverlay, 6000);
            return;
          }
          showOverlay(t("verUpdateDoneRestart"), true);
          pollUntilBack();
        } catch (e) {
          showOverlay(t("verRestarting"), true);
          pollUntilBack();
        }
      }

      // Text click: update if one is available, otherwise re-check.
      el.addEventListener("click", function () {
        if (updateBehind > 0) runUpdate();
        else checkForUpdate();
      });
      // 🏷 click: open the repository page.
      if (repoLink) {
        repoLink.addEventListener("click", function (e) {
          e.stopPropagation();
          if (repoURL) window.open(repoURL, "_blank", "noopener");
        });
      }

      loadVersion().then(checkForUpdate);
      // Re-render the footer text/tooltip in the new language on toggle.
      window.__refreshVersionFooter = function () { loadVersion().then(checkForUpdate); };
    })();

    // Resolve whether the current folder is inside a git repo, then enable or
    // disable the "Git" scope toggles accordingly. Cached per cwd.
    async function refreshGitScope() {
      if (!state.cwd) { state.gitRepoRoot = ""; updateGitScopeUI(); return; }
      if (state.gitRepoCwd === state.cwd && state.gitRepoRoot !== null) { updateGitScopeUI(); return; }
      try {
        const r = await fetch("/api/git/root?dir=" + encodeURIComponent(state.cwd));
        if (!r.ok) return;
        const data = await r.json();
        state.gitRepoRoot = (data && data.root) || "";
        state.gitRepoCwd = state.cwd;
      } catch (e) { return; }
      updateGitScopeUI();
    }
    function updateGitScopeUI() {
      const isRepo = !!state.gitRepoRoot;
      // Content-search "Git 전체" toggle.
      const gitBtn = document.getElementById("searchScopeGit");
      if (gitBtn) {
        gitBtn.disabled = !isRepo;
        gitBtn.classList.toggle("disabled", !isRepo);
        gitBtn.title = isRepo ? t("scopeGitTitle") : t("scopeNotRepo");
      }
      if (!isRepo && state.folderSearchScope === "git") applyFolderScope("folder");
      // File-name browser "Git repo" toggle (only present while the modal exists).
      const fbGitBtn = document.getElementById("fbScopeGit");
      if (fbGitBtn) {
        fbGitBtn.disabled = !isRepo;
        fbGitBtn.classList.toggle("disabled", !isRepo);
        fbGitBtn.title = isRepo ? t("fbScopeGitTitle") : t("scopeNotRepo");
      }
      if (!isRepo && state.fbScope === "git") { state.fbScope = "folder"; }
      updateVersionButton();
    }
    // The Version button needs both a git repo and an open file.
    function updateVersionButton() {
      const btn = document.getElementById("versionButton");
      if (!btn) return;
      btn.hidden = !(state.gitRepoRoot && state.selectedPath);
      updateUpdToggle();
    }
    // The "업데이트 내역" toggle: only for git-managed markdown/text files in
    // preview mode. Reflects state.updMode.
    function updateUpdToggle() {
      const seg = document.getElementById("updSeg");
      const t = document.getElementById("updToggle");
      if (!seg || !t) return;
      const eligible = !!state.gitRepoRoot && !!state.selectedPath &&
        state.selectedKind === "markdown" &&
        state.editorMode === "preview";
      seg.hidden = !eligible;            // the whole pill shows only when eligible
      const on = eligible && state.updMode;
      t.classList.toggle("active", on);
      t.setAttribute("aria-checked", on ? "true" : "false");
      // Base picker + change-nav only make sense while the overlay is on;
      // buildUpdNav re-shows the nav after a diff renders.
      const baseBtn = document.getElementById("updBaseBtn");
      if (baseBtn) baseBtn.hidden = !on;
      if (!on) { const nav = document.getElementById("updNav"); if (nav) nav.hidden = true; }
      try { updateUpdBaseLabel(); } catch (e) {}
    }

    // ── Git version compare (before/after) ──
    // Pick any two revisions of the open file (working copy + each commit) and
    // view them rendered side by side, kept in proportional scroll sync, with
    // changed blocks highlighted. Backed by /api/git/filelog and /api/git/show.
    (function setupVersionCompare() {
      const overlay = document.getElementById("vcompare");
      const btn = document.getElementById("versionButton");
      const closeBtn = document.getElementById("vcompareClose");
      const titleEl = document.getElementById("vcompareTitle");
      const selLeft = document.getElementById("vcompareSelLeft");
      const selRight = document.getElementById("vcompareSelRight");
      const leftBody = document.getElementById("vcompareLeft");
      const rightBody = document.getElementById("vcompareRight");
      const prevBtn = document.getElementById("vcomparePrev");
      const nextBtn = document.getElementById("vcompareNext");
      const countEl = document.getElementById("vcompareNavCount");
      if (!overlay || !btn) return;

      let entries = [];          // [{value, label}] incl. an optional WORKING row
      let contentCache = {};     // rev -> content string (or null on failure)
      let renderToken = 0;       // guards against overlapping async renders
      let groups = [];           // ordered change anchors {left, right} (0-based lines)
      let navIdx = -1;           // current change index for ▲/▼ navigation

      async function fetchContent(rev) {
        if (rev in contentCache) return contentCache[rev];
        let c = null;
        try {
          const r = await fetch("/api/git/show?path=" + encodeURIComponent(state.selectedPath) + "&rev=" + encodeURIComponent(rev));
          const j = await r.json();
          c = (j && j.ok) ? (j.content || "") : null;
        } catch (e) { c = null; }
        contentCache[rev] = c;
        return c;
      }

      // Myers-style LCS line diff on the changed middle (after trimming the
      // common prefix/suffix), capped so huge unrelated revisions stay fast.
      function diffMiddle(a, b) {
        if (!a.length) return b.map((x) => ({ t: "add", b: x }));
        if (!b.length) return a.map((x) => ({ t: "del", a: x }));
        if (a.length * b.length > 4000000) {
          return a.map((x) => ({ t: "del", a: x })).concat(b.map((x) => ({ t: "add", b: x })));
        }
        const n = a.length, m = b.length;
        const dp = [];
        for (let i = 0; i <= n; i++) dp.push(new Uint32Array(m + 1));
        for (let i = n - 1; i >= 0; i--) {
          for (let j = m - 1; j >= 0; j--) {
            dp[i][j] = a[i] === b[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
          }
        }
        const ops = [];
        let i = 0, j = 0;
        while (i < n && j < m) {
          if (a[i] === b[j]) { ops.push({ t: "same", a: a[i], b: b[j] }); i++; j++; }
          else if (dp[i + 1][j] >= dp[i][j + 1]) { ops.push({ t: "del", a: a[i] }); i++; }
          else { ops.push({ t: "add", b: b[j] }); j++; }
        }
        while (i < n) ops.push({ t: "del", a: a[i++] });
        while (j < m) ops.push({ t: "add", b: b[j++] });
        return ops;
      }

      // Diff the two revisions and return the set of changed line numbers
      // (0-based, in each version's own numbering — matching the data-source-line
      // attribute that annotateSourceLines stamps on rendered blocks).
      function computeChanged(lc, rc) {
        const a = lc.split("\n"), b = rc.split("\n");
        let p = 0;
        while (p < a.length && p < b.length && a[p] === b[p]) p++;
        let sa = a.length, sb = b.length;
        while (sa > p && sb > p && a[sa - 1] === b[sb - 1]) { sa--; sb--; }
        const left = new Set(), right = new Set();
        const groups = [];          // each maximal run of changes → one nav anchor
        const pairs = [];           // del-line ↔ add-line, for intra-line emphasis
        const ops = diffMiddle(a.slice(p, sa), b.slice(p, sb));
        let lLine = p, rLine = p, cur = null, dels = [], adds = [];
        function flushRun() {
          if (cur) { groups.push(cur); cur = null; }
          const k = Math.min(dels.length, adds.length);
          for (let i = 0; i < k; i++) pairs.push({ l: dels[i], r: adds[i] });
          dels = []; adds = [];
        }
        for (const op of ops) {
          if (op.t === "same") {
            flushRun();
            lLine++; rLine++;
          } else if (op.t === "del") {
            left.add(lLine);
            if (!cur) cur = { left: null, right: null };
            if (cur.left === null) cur.left = lLine;
            dels.push(lLine);
            lLine++;
          } else {
            right.add(rLine);
            if (!cur) cur = { left: null, right: null };
            if (cur.right === null) cur.right = rLine;
            adds.push(rLine);
            rLine++;
          }
        }
        flushRun();
        return { left: left, right: right, groups: groups, pairs: pairs };
      }

      // Tint the rendered element that owns each changed line. We consider
      // every [data-source-line] element (top-level blocks AND the per-row
      // <tr> / per-item <li> markers annotateSourceLines stamps), then pick the
      // most specific one per changed line — so a single changed table row or
      // list item is highlighted instead of the whole table/list.
      function applyBlockHighlight(bodyEl, changedSet) {
        if (!changedSet || !changedSet.size) return;
        function depthOf(el) { let d = 0; while (el && el !== bodyEl) { el = el.parentElement; d++; } return d; }
        const blocks = Array.from(bodyEl.querySelectorAll("[data-source-line]"))
          .map((el) => ({ el: el, line: parseInt(el.getAttribute("data-source-line"), 10), depth: depthOf(el) }))
          .filter((b) => !isNaN(b.line))
          // line asc, then depth asc: at the same start line the deeper (more
          // specific) element sorts last, so "rightmost start <= line" prefers it.
          .sort((a, b) => (a.line - b.line) || (a.depth - b.depth));
        if (!blocks.length) return;
        const starts = blocks.map((b) => b.line);
        for (const ln of changedSet) {
          let lo = 0, hi = starts.length - 1, idx = -1;
          while (lo <= hi) { const mid = (lo + hi) >> 1; if (starts[mid] <= ln) { idx = mid; lo = mid + 1; } else hi = mid - 1; }
          if (idx >= 0) blocks[idx].el.classList.add("vcd-chg-block");
        }
      }

      // Fenced code blocks render as one <pre> stamped at the opening fence's
      // line, so a whole-block tint hides behind the hljs background. Instead
      // split into per-line rows (reusing the file viewer's line numbering) and
      // tint just the changed lines. Code line k maps to source line fence+1+k.
      function highlightCodeLineDiffs(bodyEl, changedSet) {
        if (!changedSet || !changedSet.size) return;
        // decorateCodeBlocks wraps each <pre> in a .code-wrap that carries the
        // data-source-line, so find code blocks and read the line off whichever
        // ancestor holds it.
        const codes = bodyEl.querySelectorAll("pre > code");
        for (const code of codes) {
          if (code.classList.contains("language-mermaid")) continue;
          const anc = code.closest("[data-source-line]");
          if (!anc) continue;
          const sl = parseInt(anc.getAttribute("data-source-line"), 10);
          if (isNaN(sl)) continue;
          const lineCount = (code.textContent || "").split("\n").length;
          let hit = false;
          for (let i = 0; i < lineCount; i++) { if (changedSet.has(sl + 1 + i)) { hit = true; break; } }
          if (!hit) continue;
          anc.classList.remove("vcd-chg-block");
          (code.closest("pre") || code).classList.add("vcd-code-diff");
          try { applyLineNumbersManual(code); } catch (e) { continue; }
          const rows = code.querySelectorAll("table.hljs-ln tbody tr");
          for (let i = 0; i < rows.length; i++) {
            // Stamp each row's source line so change-navigation can anchor to it.
            rows[i].setAttribute("data-source-line", String(sl + 1 + i));
            if (changedSet.has(sl + 1 + i)) rows[i].classList.add("vcd-chg-line");
          }
        }
      }

      // Two mermaid SVGs can't be diffed by background colour, so for a diagram
      // whose source changed we flag it and offer a "</> 소스" toggle that shows
      // the raw mermaid source with the changed lines tinted.
      function markChangedMermaid(bodyEl, content, changedSet) {
        if (!changedSet || !changedSet.size) return;
        const FENCE = String.fromCharCode(96, 96, 96); // triple-backtick (no literal here: Go raw string)
        const lines = content.split("\n");
        const wraps = bodyEl.querySelectorAll(":scope > .mermaid-wrap, :scope > .mermaid");
        for (const wrap of wraps) {
          const sl = parseInt(wrap.getAttribute("data-source-line"), 10);
          if (isNaN(sl)) continue;
          let end = lines.length;
          for (let k = sl + 1; k < lines.length; k++) { if (lines[k].trim().indexOf(FENCE) === 0) { end = k; break; } }
          let hit = false;
          for (let i = sl; i <= end && i < lines.length; i++) { if (changedSet.has(i)) { hit = true; break; } }
          if (!hit) continue;

          const card = document.createElement("div");
          card.className = "vcd-mermaid-card";
          const bar = document.createElement("div");
          bar.className = "vcd-mermaid-bar";
          const badge = document.createElement("span");
          badge.className = "vcd-mermaid-badge";
          badge.textContent = t("vcdChanged");
          const toggle = document.createElement("button");
          toggle.type = "button";
          toggle.className = "vcd-mermaid-toggle";
          toggle.textContent = t("vcdShowSource");
          bar.appendChild(badge);
          bar.appendChild(toggle);

          const src = document.createElement("div");
          src.className = "vcd-mermaid-src vcd-rawwrap";
          src.hidden = true;
          for (let i = sl + 1; i < end; i++) {
            const d = document.createElement("div");
            d.className = "vcd-rawline" + (changedSet.has(i) ? " vcd-chg-line" : "");
            d.textContent = (lines[i] === "" ? " " : lines[i]);
            src.appendChild(d);
          }

          wrap.parentNode.insertBefore(card, wrap);
          card.appendChild(bar);
          card.appendChild(wrap);
          card.appendChild(src);
          toggle.addEventListener("click", function () {
            const showSrc = wrap.hidden !== true ? true : false;
            wrap.hidden = showSrc;
            src.hidden = !showSrc;
            toggle.textContent = showSrc ? t("vcdShowDiagram") : t("vcdShowSource");
          });
        }
      }

      async function renderSide(bodyEl, content, changedSet) {
        const kind = state.selectedKind;
        if (kind === "markdown" || kind === "text") {
          await renderMarkdownInto(bodyEl, content, kind, { trackSourceLines: true });
          try { applyBlockHighlight(bodyEl, changedSet); } catch (e) {}
          try { highlightCodeLineDiffs(bodyEl, changedSet); } catch (e) {}
          try { markChangedMermaid(bodyEl, content, changedSet); } catch (e) {}
          // Make diagrams/images zoomable just like the main viewer.
          try { attachZoomToPreview(bodyEl); } catch (e) {}
        } else {
          // Code / other: render as plain lines, tinting the changed ones.
          bodyEl.innerHTML = "";
          const wrap = document.createElement("div");
          wrap.className = "vcd-rawwrap";
          const lines = content.split("\n");
          for (let i = 0; i < lines.length; i++) {
            const d = document.createElement("div");
            d.className = "vcd-rawline" + (changedSet.has(i) ? " vcd-chg-block" : "");
            d.textContent = lines[i] === "" ? " " : lines[i];
            wrap.appendChild(d);
          }
          bodyEl.appendChild(wrap);
        }
      }

      async function render() {
        const token = ++renderToken;
        leftBody.innerHTML = "<div class='vcompare-empty'>" + t("vcLoading") + "</div>";
        rightBody.innerHTML = "";
        const [lc, rc] = await Promise.all([fetchContent(selLeft.value), fetchContent(selRight.value)]);
        if (token !== renderToken) return; // a newer render superseded this one
        if (lc === null || rc === null) {
          leftBody.innerHTML = "<div class='vcompare-empty'>" + t("vcLoadFail") + "</div>";
          rightBody.innerHTML = "";
          return;
        }
        const changed = computeChanged(lc, rc);
        groups = changed.groups;
        navIdx = -1;
        await renderSide(leftBody, lc, changed.left);
        if (token !== renderToken) return;
        await renderSide(rightBody, rc, changed.right);
        try { applyWordDiffs(changed.pairs); } catch (e) {}
        leftBody.scrollTop = 0;
        rightBody.scrollTop = 0;
        updateNavUI();
      }

      // Proportional bidirectional scroll sync (reentrancy-guarded). Suspended
      // while we jump to a change so each pane lands on its own anchor.
      let vcSyncSrc = null;
      let vcProgrammatic = false;
      function vcSync(src, dst) {
        if (vcProgrammatic) return;
        if (vcSyncSrc && vcSyncSrc !== src) return;
        vcSyncSrc = src;
        const range = src.scrollHeight - src.clientHeight;
        const ratio = range > 0 ? src.scrollTop / range : 0;
        dst.scrollTop = ratio * (dst.scrollHeight - dst.clientHeight);
        requestAnimationFrame(() => { vcSyncSrc = null; });
      }
      leftBody.addEventListener("scroll", () => vcSync(leftBody, rightBody));
      rightBody.addEventListener("scroll", () => vcSync(rightBody, leftBody));

      // ── Change navigation (▲ prev / ▼ next) ──
      function anchorForLine(body, line) {
        if (line === null || line === undefined) return null;
        // Non-markdown raw render: the whole pane is one .vcd-rawwrap of lines.
        // (Must be a DIRECT child — a mermaid source view also uses .vcd-rawwrap.)
        const rawTop = body.querySelector(":scope > .vcd-rawwrap");
        if (rawTop) { const ch = rawTop.children; return ch[Math.min(line, ch.length - 1)] || null; }
        // Markdown: consider every [data-source-line] element (top-level blocks
        // plus stamped table rows, list items and code lines) and pick the most
        // specific one for this line, so navigation lands inside the block.
        function depthOf(el) { let d = 0; while (el && el !== body) { el = el.parentElement; d++; } return d; }
        const blocks = Array.from(body.querySelectorAll("[data-source-line]"))
          .map((el) => ({ el: el, line: parseInt(el.getAttribute("data-source-line"), 10), depth: depthOf(el) }))
          .filter((b) => !isNaN(b.line))
          .sort((a, b) => (a.line - b.line) || (a.depth - b.depth));
        if (!blocks.length) return null;
        let lo = 0, hi = blocks.length - 1, idx = 0;
        while (lo <= hi) { const mid = (lo + hi) >> 1; if (blocks[mid].line <= line) { idx = mid; lo = mid + 1; } else hi = mid - 1; }
        return blocks[idx].el;
      }
      function scrollBodyTo(body, el) {
        if (!el) return;
        const br = body.getBoundingClientRect(), er = el.getBoundingClientRect();
        body.scrollTop += (er.top - br.top) - 16;
      }

      // ── Intra-line emphasis: within each changed line pair, highlight the
      // tokens (whole words/numbers) that were removed (left) / added (right). ──
      // Token-level LCS over the two rendered strings, returning character
      // offset ranges into a (del) and b (add) so wrapRanges can mark them.
      // Token granularity keeps a changed word intact instead of fragmenting
      // it ("1.58"→"1.59", not "1.589").
      function charDiff(a, b) {
        if (!a.length || !b.length) return null;
        const ta = tokenizeForDiff(a), tb = tokenizeForDiff(b);
        const A = ta.map(function (x) { return x.text; });
        const B = tb.map(function (x) { return x.text; });
        const n = A.length, m = B.length;
        // Trim common token prefix/suffix so a small edit in a long string
        // stays within the O(n*m) budget.
        let p = 0; while (p < n && p < m && A[p] === B[p]) p++;
        let ea = n, eb = m; while (ea > p && eb > p && A[ea - 1] === B[eb - 1]) { ea--; eb--; }
        const am = A.slice(p, ea), bm = B.slice(p, eb);
        const an = am.length, bn = bm.length;
        if (an * bn > 200000) return null; // skip pathological middles
        const del = [], add = [];
        if (!an && !bn) return { del: del, add: add };
        // Map a token-index span [s,e) (in the trimmed-middle coordinates) back
        // to a character range in the original string via the token list.
        function rng(toks, s, e) {
          const first = toks[p + s], last = toks[p + e - 1];
          return { start: first.start, end: last.start + last.text.length };
        }
        const dp = [];
        for (let i = 0; i <= an; i++) dp.push(new Uint32Array(bn + 1));
        for (let i = an - 1; i >= 0; i--) {
          for (let j = bn - 1; j >= 0; j--) {
            dp[i][j] = am[i] === bm[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
          }
        }
        let i = 0, j = 0, ds = -1, as = -1;
        while (i < an && j < bn) {
          if (am[i] === bm[j]) {
            if (ds >= 0) { del.push(rng(ta, ds, i)); ds = -1; }
            if (as >= 0) { add.push(rng(tb, as, j)); as = -1; }
            i++; j++;
          } else if (dp[i + 1][j] >= dp[i][j + 1]) {
            if (ds < 0) ds = i; i++;
          } else {
            if (as < 0) as = j; j++;
          }
        }
        while (i < an) { if (ds < 0) ds = i; i++; }
        while (j < bn) { if (as < 0) as = j; j++; }
        if (ds >= 0) del.push(rng(ta, ds, an));
        if (as >= 0) add.push(rng(tb, as, bn));
        return { del: del, add: add };
      }
      // Wrap the given character ranges (offsets into container.textContent) in
      // <mark class=cls> by walking the container's text nodes.
      function wrapRanges(container, ranges, cls) {
        if (!ranges || !ranges.length) return;
        const nodes = [];
        const tw = document.createTreeWalker(container, NodeFilter.SHOW_TEXT, null);
        let nd; while ((nd = tw.nextNode())) nodes.push(nd);
        let pos = 0;
        for (const node of nodes) {
          const text = node.nodeValue;
          const start = pos, end = pos + text.length;
          pos = end;
          const local = [];
          for (const r of ranges) {
            const a = Math.max(r.start, start), b = Math.min(r.end, end);
            if (a < b) local.push([a - start, b - start]);
          }
          if (!local.length) continue;
          const frag = document.createDocumentFragment();
          let cur = 0;
          for (const seg of local) {
            if (seg[0] > cur) frag.appendChild(document.createTextNode(text.slice(cur, seg[0])));
            const mark = document.createElement("mark");
            mark.className = cls;
            mark.textContent = text.slice(seg[0], seg[1]);
            frag.appendChild(mark);
            cur = seg[1];
          }
          if (cur < text.length) frag.appendChild(document.createTextNode(text.slice(cur)));
          node.parentNode.replaceChild(frag, node);
        }
      }
      // The text container to diff/highlight for an anchored element.
      function textTarget(el) {
        if (el.tagName === "TR") return el.querySelector(".hljs-ln-code") || el;
        return el;
      }
      function wordDiffHighlight(leftEl, rightEl) {
        const lt = textTarget(leftEl), rt = textTarget(rightEl);
        const a = lt.textContent || "", b = rt.textContent || "";
        if (a === b || !a || !b) return;
        const d = charDiff(a, b);
        if (!d) return;
        wrapRanges(lt, d.del, "vcd-ic vcd-ic-del");
        wrapRanges(rt, d.add, "vcd-ic vcd-ic-add");
      }
      function applyWordDiffs(pairs) {
        if (!pairs || !pairs.length) return;
        const seen = new Set();
        for (const p of pairs) {
          const le = anchorForLine(leftBody, p.l), re = anchorForLine(rightBody, p.r);
          if (!le || !re || seen.has(le)) continue;
          // Diagrams aren't text — skip mermaid blocks.
          if (le.closest(".vcd-mermaid-card") || re.closest(".vcd-mermaid-card")) continue;
          if (le.classList.contains("mermaid-wrap") || le.classList.contains("mermaid")) continue;
          seen.add(le);
          try { wordDiffHighlight(le, re); } catch (e) {}
        }
      }
      function flash(el) {
        if (!el) return;
        el.classList.remove("vcd-flash");
        void el.offsetWidth; // restart the animation
        el.classList.add("vcd-flash");
        setTimeout(() => { try { el.classList.remove("vcd-flash"); } catch (e) {} }, 950);
      }
      function updateNavUI() {
        const n = groups.length;
        if (countEl) countEl.textContent = n ? ((navIdx >= 0 ? (navIdx + 1) : "–") + " / " + n + " " + t("vcChangeWord")) : t("vcNoChanges");
        if (prevBtn) prevBtn.disabled = !n;
        if (nextBtn) nextBtn.disabled = !n;
      }
      function goToChange(i) {
        if (!groups.length) return;
        navIdx = (i % groups.length + groups.length) % groups.length;
        const g = groups[navIdx];
        const la = anchorForLine(leftBody, g.left);
        const ra = anchorForLine(rightBody, g.right);
        vcProgrammatic = true;
        scrollBodyTo(leftBody, la);
        scrollBodyTo(rightBody, ra);
        setTimeout(() => { vcProgrammatic = false; }, 120);
        flash(la); flash(ra);
        updateNavUI();
      }
      if (prevBtn) prevBtn.addEventListener("click", () => goToChange(navIdx - 1));
      if (nextBtn) nextBtn.addEventListener("click", () => goToChange(navIdx + 1));

      function buildOptions(selectEl, selectedValue) {
        selectEl.innerHTML = "";
        for (const e of entries) {
          const opt = document.createElement("option");
          opt.value = e.value;
          opt.textContent = e.label;
          if (e.value === selectedValue) opt.selected = true;
          selectEl.appendChild(opt);
        }
      }

      async function open() {
        if (!state.selectedPath) return;
        let data;
        try {
          const r = await fetch("/api/git/filelog?path=" + encodeURIComponent(state.selectedPath));
          data = await r.json();
        } catch (e) { showToast(t("toastGitLogFail"), { kind: "err", icon: "⚠️" }); return; }
        if (!data || !data.available) { showToast(t("toastNotInGit"), { kind: "err", icon: "⚠️" }); return; }
        const commits = Array.isArray(data.commits) ? data.commits : [];
        entries = [];
        if (data.dirty) entries.push({ value: "WORKING", label: t("vcWorkingCopy") });
        commits.forEach(function (c, i) {
          const tag = (i === 0 && !data.dirty) ? t("vcLatestTag") : "";
          entries.push({ value: c.hash, label: c.short + " · " + c.date + " · " + (c.subject || "") + tag });
        });
        if (!entries.length) { showToast(t("toastNoCommits"), { kind: "err", icon: "⚠️" }); return; }

        // Defaults: right = newest, left = the one before it (before/after).
        const rightVal = entries[0].value;
        const leftVal = entries[1] ? entries[1].value : entries[0].value;
        contentCache = {};
        buildOptions(selLeft, leftVal);
        buildOptions(selRight, rightVal);
        titleEl.textContent = t("vcTitlePrefix") + state.selectedPath.split("/").pop();
        overlay.hidden = false;
        document.body.classList.add("lightbox-open");
        render();
      }

      function close() {
        overlay.hidden = true;
        document.body.classList.remove("lightbox-open");
      }

      btn.addEventListener("click", open);
      if (closeBtn) closeBtn.addEventListener("click", close);
      if (selLeft) selLeft.addEventListener("change", render);
      if (selRight) selRight.addEventListener("change", render);
      document.addEventListener("keydown", function (e) {
        if (overlay.hidden) return;
        // A diagram lightbox can sit on top of the compare view — let it own
        // the keyboard (Esc/etc.) so closing it doesn't also close compare.
        const lbEl = document.getElementById("lightbox");
        if (lbEl && !lbEl.hidden) return;
        if (e.key === "Escape") { e.preventDefault(); close(); return; }
        if (e.target && e.target.tagName === "SELECT") return; // let selects use arrows
        if (e.key === "ArrowDown") { e.preventDefault(); goToChange(navIdx + 1); }
        else if (e.key === "ArrowUp") { e.preventDefault(); goToChange(navIdx - 1); }
      });
    })();

    // ── Multi-memo notebook in the right panel ───────────────────
    // A global notebook (not tied to the open file). Memos live in memory,
    // are mirrored to localStorage for instant load/offline, and sync to the
    // server file (.mdviewer_memos.json) so they survive restarts and re-sync
    // when a new session opens. Each memo has an id + createdAt/updatedAt;
    // merges are last-write-wins per id keyed on updatedAt.
    (function setupMemos() {
      const listEl = document.getElementById("memoList");
      const emptyEl = document.getElementById("memoEmpty");
      const noMatchEl = document.getElementById("memoNoMatch");
      const controlsEl = document.getElementById("memoControls");
      const filterEl = document.getElementById("memoFilter");
      const sortUpdatedEl = document.getElementById("memoSortUpdated");
      const sortCreatedEl = document.getElementById("memoSortCreated");
      const sortTitleEl = document.getElementById("memoSortTitle");
      const editorEl = document.getElementById("memoEditor");
      const titleEl = document.getElementById("memoTitleInput");
      const areaEl = document.getElementById("memoArea");
      const syncStateEl = document.getElementById("memoSyncState");
      const newBtnEl = document.getElementById("memoNewBtn");
      const copyBtnEl = document.getElementById("memoCopyBtn");
      const backlinkEl = document.getElementById("memoBacklink");
      const selectionBarEl = document.getElementById("memoSelectionBar");
      const selectionMemoBtnEl = document.getElementById("memoSelectionMemoBtn");
      const selectionSearchBtnEl = document.getElementById("memoSelectionSearchBtn");
      const selectionCopyBtnEl = document.getElementById("memoSelectionCopyBtn");
      const trashEl = document.getElementById("memoTrash");
      const trashToggleEl = document.getElementById("memoTrashToggle");
      const trashCountEl = document.getElementById("memoTrashCount");
      const trashListEl = document.getElementById("memoTrashList");
      const trashEmptyBtnEl = document.getElementById("memoTrashEmptyBtn");
      if (!listEl || !areaEl || !titleEl) return;

      const LS_KEY = "mdviewer.memos";
      const LS_LEGACY = "mdviewer.memo";
      const LS_SORT = "mdviewer.memoSort";
      const LS_TRASH = "mdviewer.memoTrash";
      const m = {
        memos: [], activeId: null, dirty: new Set(), syncTimer: null,
        filter: "", sort: "updated",
        trash: [], trashOpen: false,   // local-only recovery buffer (not synced)
        base: {},            // id -> updatedAt we last knew the server had (conflict baseline)
        conflicts: {},       // id -> { id, kind:"edit"|"deleted", mine, theirs }
        pulling: false, pushing: false, resolving: false,
      };

      function nowISO() { return new Date().toISOString(); }
      function genId() {
        return "m_" + Date.now().toString(36) + "_" + Math.random().toString(36).slice(2, 8);
      }
      function getMemo(id) { return m.memos.find(function (x) { return x.id === id; }) || null; }
      function firstLine(s) {
        const lines = (s || "").split("\n");
        for (let i = 0; i < lines.length; i++) {
          const t = lines[i].trim();
          if (t) return t;
        }
        return "";
      }
      function displayName(memo) {
        const t = (memo.title || "").trim();
        if (t) return { text: t, untitled: false };
        const f = firstLine(memo.body);
        if (f) return { text: f, untitled: false };
        return { text: "Untitled", untitled: true };
      }
      function relTime(iso) {
        const ts = Date.parse(iso);
        if (isNaN(ts)) return "";
        const s = Math.floor((Date.now() - ts) / 1000);
        if (s < 60) return t("relJustNow");
        const mins = Math.floor(s / 60); if (mins < 60) return t("relMinAgo").replace("{0}", mins);
        const h = Math.floor(mins / 60); if (h < 24) return t("relHourAgo").replace("{0}", h);
        const d = Math.floor(h / 24); if (d < 7) return t("relDayAgo").replace("{0}", d);
        const dt = new Date(ts); return (dt.getMonth() + 1) + "/" + dt.getDate();
      }
      function sortMemos() {
        m.memos.sort(function (a, b) { return (b.updatedAt || "").localeCompare(a.updatedAt || ""); });
      }
      // Body preview for the list: drops the line already shown as the title
      // (when the memo has no explicit title) and collapses whitespace.
      function snippet(memo) {
        let body = memo.body || "";
        if (!(memo.title || "").trim()) {
          const lines = body.split("\n");
          let i = 0;
          while (i < lines.length && !lines[i].trim()) i++; // skip leading blanks
          if (i < lines.length) i++;                        // drop the first non-empty (used as title)
          body = lines.slice(i).join("\n");
        }
        return body.replace(/\s+/g, " ").trim();
      }
      function matchesFilter(memo) {
        const q = m.filter.trim().toLowerCase();
        if (!q) return true;
        return ((memo.title || "") + "\n" + (memo.body || "")).toLowerCase().indexOf(q) !== -1;
      }
      // View order: pinned memos first, then the selected sort key.
      function compareMemos(a, b) {
        if (!!a.pinned !== !!b.pinned) return a.pinned ? -1 : 1;
        if (m.sort === "created") return (b.createdAt || "").localeCompare(a.createdAt || "");
        if (m.sort === "title") {
          return displayName(a).text.localeCompare(displayName(b).text, undefined, { sensitivity: "base" });
        }
        return (b.updatedAt || "").localeCompare(a.updatedAt || "");
      }
      function persistLocal() {
        try { localStorage.setItem(LS_KEY, JSON.stringify(m.memos)); } catch (e) {}
      }
      function loadLocal() {
        try { const v = JSON.parse(localStorage.getItem(LS_KEY) || "[]"); return Array.isArray(v) ? v : []; }
        catch (e) { return []; }
      }
      function persistTrash() {
        try { localStorage.setItem(LS_TRASH, JSON.stringify(m.trash)); } catch (e) {}
      }
      function loadTrash() {
        try { const v = JSON.parse(localStorage.getItem(LS_TRASH) || "[]"); return Array.isArray(v) ? v : []; }
        catch (e) { return []; }
      }
      function setSyncState(s) { if (syncStateEl) syncStateEl.textContent = s || ""; }

      function renderList() {
        listEl.innerHTML = "";
        const view = m.memos.filter(matchesFilter).slice().sort(compareMemos);
        for (const memo of view) {
          const row = document.createElement("div");
          row.className = "memo-list-item" + (memo.id === m.activeId ? " active" : "");

          const pin = document.createElement("button");
          pin.type = "button";
          pin.className = "memo-item-pin" + (memo.pinned ? " pinned" : "");
          pin.title = memo.pinned ? "Unpin" : "Pin to top";
          pin.textContent = "📌";
          pin.addEventListener("click", function (ev) { ev.stopPropagation(); togglePin(memo.id); });

          const main = document.createElement("div");
          main.className = "memo-item-main";
          const name = displayName(memo);
          const title = document.createElement("div");
          title.className = "memo-item-title" + (name.untitled ? " untitled" : "");
          title.textContent = name.text;
          main.appendChild(title);
          const snip = snippet(memo);
          if (snip) {
            const sn = document.createElement("div");
            sn.className = "memo-item-snippet";
            sn.textContent = snip;
            main.appendChild(sn);
          }
          if (memo.sourcePath) {
            const srcEl = document.createElement("div");
            srcEl.className = "memo-item-source";
            const fileName = memo.sourcePath.split("/").pop();
            srcEl.textContent = "↩ " + (memo.sourceHeading ? (fileName + " › " + memo.sourceHeading) : fileName);
            srcEl.title = memo.sourcePath + (memo.sourceHash ? ("#" + memo.sourceHash) : "");
            main.appendChild(srcEl);
          }

          const right = document.createElement("div");
          right.className = "memo-item-right";
          const time = document.createElement("div");
          time.className = "memo-item-time";
          time.textContent = relTime(m.sort === "created" ? memo.createdAt : memo.updatedAt);
          const del = document.createElement("button");
          del.type = "button";
          del.className = "memo-item-del";
          del.title = "Delete memo";
          del.textContent = "×";
          del.addEventListener("click", function (ev) { ev.stopPropagation(); deleteMemo(memo.id); });
          right.appendChild(time);
          right.appendChild(del);

          row.appendChild(pin);
          row.appendChild(main);
          row.appendChild(right);
          row.addEventListener("click", function () { setActive(memo.id); });
          listEl.appendChild(row);
        }
        const has = m.memos.length > 0;
        if (controlsEl) controlsEl.hidden = !has;
        if (emptyEl) emptyEl.hidden = has;
        if (editorEl) editorEl.hidden = !has;
        if (noMatchEl) noMatchEl.hidden = !(has && view.length === 0);
        renderTrash();
      }

      // ── trash (local recovery buffer) ──
      function renderTrash() {
        if (!trashEl) return;
        const n = m.trash.length;
        const open = m.trashOpen && n > 0;
        trashEl.hidden = (n === 0);
        trashEl.classList.toggle("open", open);
        if (trashCountEl) trashCountEl.textContent = String(n);
        if (trashToggleEl) trashToggleEl.setAttribute("aria-expanded", open ? "true" : "false");
        if (trashListEl) trashListEl.hidden = !open;
        if (!trashListEl) return;
        trashListEl.innerHTML = "";
        if (!open) return;
        for (const memo of m.trash) {
          const item = document.createElement("div");
          item.className = "memo-trash-item";
          const main = document.createElement("div");
          main.className = "memo-trash-item-main";
          const title = document.createElement("div");
          title.className = "memo-trash-item-title";
          title.textContent = displayName(memo).text;
          const time = document.createElement("div");
          time.className = "memo-trash-item-time";
          time.textContent = t("trashDeleted") + " · " + relTime(memo.deletedAt);
          main.appendChild(title);
          main.appendChild(time);
          const restore = document.createElement("button");
          restore.type = "button";
          restore.className = "memo-trash-btn restore";
          restore.title = t("trashRestore");
          restore.textContent = "↩";
          restore.addEventListener("click", function () { restoreMemo(memo.id); });
          const purge = document.createElement("button");
          purge.type = "button";
          purge.className = "memo-trash-btn purge";
          purge.title = t("trashPurge");
          purge.textContent = "×";
          purge.addEventListener("click", function () { purgeMemo(memo.id); });
          item.appendChild(main);
          item.appendChild(restore);
          item.appendChild(purge);
          trashListEl.appendChild(item);
        }
      }
      function restoreMemo(id) {
        const idx = m.trash.findIndex(function (x) { return x.id === id; });
        if (idx === -1) return;
        const memo = cloneMemo(m.trash[idx]); // cloneMemo drops the deletedAt marker
        memo.updatedAt = nowISO();            // fresh stamp so it re-syncs to the server
        m.trash.splice(idx, 1);
        if (!getMemo(memo.id)) m.memos.unshift(memo);
        m.dirty.add(memo.id);
        delete m.base[memo.id];               // treat as new on the server
        persistTrash();
        persistLocal();
        setActive(memo.id);                   // re-renders list + trash
        scheduleSync();
        showToast(t("toastMemoRestored"), { kind: "ok", icon: "↩" });
      }
      function purgeMemo(id) {
        const memo = m.trash.find(function (x) { return x.id === id; });
        if (!memo) return;
        if (!window.confirm(t("confirmPurgeOne"))) return;
        m.trash = m.trash.filter(function (x) { return x.id !== id; });
        persistTrash();
        renderTrash();
      }
      function emptyTrash() {
        if (!m.trash.length) return;
        if (!window.confirm(t("confirmPurgeAll").replace("{0}", m.trash.length))) return;
        m.trash = [];
        persistTrash();
        renderTrash();
        showToast(t("toastTrashEmptied"), { kind: "ok", icon: "🗑" });
      }

      function syncActiveEditor() {
        const memo = getMemo(m.activeId);
        titleEl.value = memo ? (memo.title || "") : "";
        areaEl.value = memo ? (memo.body || "") : "";
        renderBacklink(memo);
      }

      function renderBacklink(memo) {
        if (!backlinkEl) return;
        if (!memo || !memo.sourcePath) { backlinkEl.hidden = true; return; }
        const fileName = memo.sourcePath.split("/").pop();
        const where = memo.sourceHeading ? (fileName + " › " + memo.sourceHeading) : fileName;
        backlinkEl.textContent = "↩ " + t("backlinkSource") + where;
        backlinkEl.title = t("backlinkTitle") + memo.sourcePath + (memo.sourceHash ? ("#" + memo.sourceHash) : "");
        backlinkEl.hidden = false;
      }

      function setActive(id) {
        m.activeId = id;
        renderList();
        syncActiveEditor();
      }

      function onEdit(field, val) {
        const memo = getMemo(m.activeId);
        if (!memo) return;
        memo[field] = val;
        memo.updatedAt = nowISO();
        m.dirty.add(memo.id);
        persistLocal();
        renderList(); // refresh display name + relative time live
        setSyncState(t("syncEditing"));
        scheduleSync();
      }

      // Source for a new memo: the file currently open, plus the heading
      // closest to the current scroll position (from the outline scroll-spy).
      function currentViewSource() {
        const path = state.selectedPath || "";
        if (!path) return { path: "", hash: "", heading: "" };
        const id = (typeof outlineState !== "undefined") ? outlineState.activeId : "";
        let heading = "";
        if (id && typeof outlineState !== "undefined" && outlineState.all) {
          const h = outlineState.all.find(function (x) { return x.id === id; });
          if (h) heading = h.text || "";
        }
        return { path: path, hash: id || "", heading: heading };
      }

      function newMemo() {
        const t = nowISO();
        const src = currentViewSource();
        const memo = {
          id: genId(), title: "", body: "", pinned: false,
          sourcePath: src.path, sourceHash: src.hash, sourceHeading: src.heading,
          createdAt: t, updatedAt: t,
        };
        m.memos.unshift(memo);
        m.dirty.add(memo.id);
        persistLocal();
        setActive(memo.id);
        titleEl.focus();
        scheduleSync();
      }

      function createMemoFromSelection(text, source) {
        const ts = nowISO();
        // Keep a quote of the selection so the backlink can jump to and
        // highlight the exact spot (capped to stay light).
        const quote = (text || "").replace(/\s+/g, " ").trim().slice(0, 200);
        const memo = {
          id: genId(), title: "", body: text, pinned: false,
          sourcePath: (source && source.path) || "",
          sourceHash: (source && source.hash) || "",
          sourceHeading: (source && source.heading) || "",
          sourceQuote: quote,
          createdAt: ts, updatedAt: ts,
        };
        m.memos.unshift(memo);
        m.dirty.add(memo.id);
        persistLocal();
        setActive(memo.id);
        scheduleSync();
        showToast(t("toastMemoSaved"), { kind: "ok", icon: "📝" });
      }

      function togglePin(id) {
        const memo = getMemo(id);
        if (!memo) return;
        memo.pinned = !memo.pinned;
        memo.updatedAt = nowISO(); // bump so the pin state reliably syncs
        m.dirty.add(memo.id);
        persistLocal();
        renderList();
        scheduleSync();
      }

      async function deleteMemo(id) {
        const memo = getMemo(id);
        if (!memo) return;
        // Move to the local trash instead of deleting outright — recoverable
        // until the user empties the trash. No confirm: it is non-destructive.
        const trashed = cloneMemo(memo);
        trashed.deletedAt = nowISO();
        m.trash.unshift(trashed);
        m.memos = m.memos.filter(function (x) { return x.id !== id; });
        m.dirty.delete(id);
        delete m.base[id];
        delete m.conflicts[id];
        if (m.activeId === id) m.activeId = m.memos[0] ? m.memos[0].id : null;
        persistLocal();
        persistTrash();
        renderList();
        syncActiveEditor();
        showToast(t("toastMemoTrashed"), { kind: "ok", icon: "🗑" });
        // Remove from the server too, so other sessions also drop it. The trash
        // is local-only; restoring re-pushes the memo as new.
        try {
          await fetch("/api/memos/delete", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ id: id }),
          });
        } catch (e) { /* offline: server keeps it; harmless for single user */ }
      }

      // ── sync ──
      function scheduleSync() {
        clearTimeout(m.syncTimer);
        m.syncTimer = setTimeout(flushDirty, 800);
      }

      async function flushDirty() {
        if (!m.dirty.size) return;
        const ids = Array.from(m.dirty);
        const batch = [];
        const sentAt = {};
        for (const id of ids) {
          if (m.conflicts[id]) continue;     // held until the user resolves the conflict
          const memo = getMemo(id);
          if (memo) { batch.push(memo); sentAt[id] = memo.updatedAt; }
          else m.dirty.delete(id); // gone (deleted) — drop from dirty
        }
        if (!batch.length) return;
        m.pushing = true;
        try {
          const r = await fetch("/api/memos/save", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ memos: batch }),
          });
          if (!r.ok) throw new Error("save failed");
          // Clear dirty only for memos untouched since we sent them, and
          // advance the conflict baseline to what the server now holds.
          for (const id of ids) {
            const memo = getMemo(id);
            if (!memo || sentAt[id] === memo.updatedAt) {
              m.dirty.delete(id);
              if (sentAt[id]) m.base[id] = sentAt[id];
            }
          }
          setSyncState(t("syncSaved") + " · " + relTime(nowISO()));
        } catch (e) {
          setSyncState(t("syncPending")); // periodic flush retries
        } finally {
          m.pushing = false;
        }
      }

      function cloneMemo(x) {
        return {
          id: x.id, title: x.title || "", body: x.body || "", pinned: !!x.pinned,
          sourcePath: x.sourcePath || "", sourceHash: x.sourceHash || "",
          sourceHeading: x.sourceHeading || "", sourceQuote: x.sourceQuote || "",
          createdAt: x.createdAt || "", updatedAt: x.updatedAt || "",
        };
      }
      function assignMemo(target, src) {
        target.title = src.title || "";
        target.body = src.body || "";
        target.pinned = !!src.pinned;
        target.sourcePath = src.sourcePath || "";
        target.sourceHash = src.sourceHash || "";
        target.sourceHeading = src.sourceHeading || "";
        target.sourceQuote = src.sourceQuote || "";
        target.createdAt = src.createdAt || target.createdAt;
        target.updatedAt = src.updatedAt || "";
      }
      function sameContent(a, b) {
        return (a.title || "") === (b.title || "") && (a.body || "") === (b.body || "") && !!a.pinned === !!b.pinned;
      }
      function removeLocal(id) {
        m.memos = m.memos.filter(function (x) { return x.id !== id; });
        m.dirty.delete(id);
        delete m.base[id];
      }

      // Pull the server's copy and merge it into local state. Non-conflicting
      // remote changes apply automatically; concurrent edits to a memo this
      // session has unsaved changes on are queued for the conflict popup.
      async function pullAndReconcile(isInitial) {
        if (m.pushing || m.resolving || m.pulling) return;
        m.pulling = true;
        let serverMemos;
        try {
          const r = await fetch("/api/memos");
          if (!r.ok) throw new Error("load failed");
          serverMemos = ((await r.json()) || {}).memos || [];
        } catch (e) { m.pulling = false; return; } // offline: keep local as-is
        try {
          reconcile(serverMemos, !!isInitial);
        } finally {
          m.pulling = false;
        }
      }

      function reconcile(serverMemos, isInitial) {
        const serverById = {};
        for (const sm of serverMemos) if (sm.id) serverById[sm.id] = sm;
        const localById = {};
        for (const lm of m.memos) localById[lm.id] = lm;

        let autoChanges = 0;
        const toPush = [];
        const newConflicts = [];
        let editorNeedsRefresh = false;

        // Server memos: adopt new ones / newer non-dirty ones; flag conflicts.
        for (const sm of serverMemos) {
          if (!sm.id) continue;
          const lm = localById[sm.id];
          const sU = sm.updatedAt || "";
          if (!lm) {
            m.memos.push(cloneMemo(sm));
            m.base[sm.id] = sU;
            autoChanges++;
            continue;
          }
          if (m.conflicts[sm.id]) continue; // already pending resolution
          if (!m.dirty.has(sm.id)) {
            const lU = lm.updatedAt || "";
            if (sU > lU) {
              assignMemo(lm, sm);
              m.base[sm.id] = sU;
              autoChanges++;
              if (sm.id === m.activeId) editorNeedsRefresh = true;
            } else if (lU > sU) {
              if (!(sm.id in m.base)) m.base[sm.id] = sU;
              toPush.push(sm.id); // local is ahead (e.g. offline edit) — push it
            } else if (!(sm.id in m.base)) {
              m.base[sm.id] = sU;
            }
          } else {
            // Local has unsaved edits.
            const base = m.base[sm.id] || "";
            if (sU === base) {
              toPush.push(sm.id);              // server unchanged → our edit wins later
            } else if (sameContent(lm, sm)) {
              m.dirty.delete(sm.id);           // converged → nothing to do
              m.base[sm.id] = sU;
            } else {
              newConflicts.push({ id: sm.id, kind: "edit", mine: cloneMemo(lm), theirs: cloneMemo(sm) });
            }
          }
        }

        // Local-only memos (absent from server).
        for (const lm of m.memos.slice()) {
          if (serverById[lm.id] || m.conflicts[lm.id]) continue;
          const known = (lm.id in m.base);
          if (!known) {
            toPush.push(lm.id);                // brand-new local, never pushed
          } else if (m.dirty.has(lm.id)) {
            newConflicts.push({ id: lm.id, kind: "deleted", mine: cloneMemo(lm) });
          } else {
            removeLocal(lm.id);                // deleted elsewhere, no local edits
            autoChanges++;
          }
        }

        // Hold conflicted memos out of the push queue until resolved.
        for (const c of newConflicts) {
          m.dirty.delete(c.id);
          if (!m.conflicts[c.id]) m.conflicts[c.id] = c;
        }

        if (!getMemo(m.activeId)) m.activeId = m.memos[0] ? m.memos[0].id : null;
        persistLocal();
        renderList();
        if (editorNeedsRefresh) syncActiveEditor();

        for (const id of toPush) if (!m.conflicts[id]) m.dirty.add(id);
        if (toPush.some(function (id) { return !m.conflicts[id]; })) flushDirty();

        if (!isInitial && autoChanges > 0) {
          showToast(t("toastOtherSession").replace("{0}", autoChanges), { kind: "ok", icon: "🔄" });
        }
        if (Object.keys(m.conflicts).length) openConflictPopup();
      }

      // ── conflict resolution ──
      function pendingConflict() {
        const ids = Object.keys(m.conflicts);
        return ids.length ? m.conflicts[ids[0]] : null;
      }
      function finishConflict(id) {
        delete m.conflicts[id];
        persistLocal();
        renderList();
        renderConflictPopup(); // advance to next or close
      }
      function resolveKeepMine(id) {
        const c = m.conflicts[id];
        if (!c) return;
        let memo = getMemo(id);
        if (!memo) { memo = cloneMemo(c.mine); m.memos.push(memo); } // deleted-kind: re-create
        else assignMemo(memo, c.mine);
        memo.updatedAt = nowISO();             // bump so our version wins on the server
        m.dirty.add(id);
        if (id === m.activeId) syncActiveEditor();
        finishConflict(id);
        flushDirty();
      }
      function resolveTakeServer(id) {
        const c = m.conflicts[id];
        if (!c) return;
        if (c.kind === "deleted") {
          removeLocal(id);                     // accept the remote deletion
          if (m.activeId === id) m.activeId = m.memos[0] ? m.memos[0].id : null;
        } else {
          const memo = getMemo(id);
          if (memo) assignMemo(memo, c.theirs);
          m.dirty.delete(id);
          m.base[id] = (c.theirs && c.theirs.updatedAt) || "";
          if (id === m.activeId) syncActiveEditor();
        }
        finishConflict(id);
      }
      function resolveKeepBoth(id) {
        const c = m.conflicts[id];
        if (!c || c.kind !== "edit") return;
        // Fork my version into a new memo; original takes the server version.
        const ts = nowISO();
        const mineTitle = (c.mine.title || "").trim();
        const fork = {
          id: genId(),
          title: mineTitle ? (mineTitle + " " + t("confMineTag")) : t("confMineTag"),
          body: c.mine.body || "",
          pinned: false,
          createdAt: ts, updatedAt: ts,
        };
        m.memos.unshift(fork);
        m.dirty.add(fork.id);
        const memo = getMemo(id);
        if (memo) assignMemo(memo, c.theirs);
        m.dirty.delete(id);
        m.base[id] = (c.theirs && c.theirs.updatedAt) || "";
        m.activeId = fork.id;
        syncActiveEditor();
        finishConflict(id);
        flushDirty();
      }

      function conflictPreview(memo) {
        if (!memo) return t("confEmpty");
        const ttl = (memo.title || "").trim();
        const full = (ttl ? ttl + "\n" : "") + (memo.body || "");
        return full.length > 800 ? full.slice(0, 800) + "…" : (full || t("confEmptyMemo"));
      }
      function makeConflictBtn(label, primary, onClick) {
        const b = document.createElement("button");
        b.type = "button";
        b.className = "action" + (primary ? " primary" : "");
        b.textContent = label;
        b.addEventListener("click", onClick);
        return b;
      }
      function openConflictPopup() { renderConflictPopup(); }
      function renderConflictPopup() {
        const modal = document.getElementById("memoConflictModal");
        const bodyEl = document.getElementById("memoConflictBody");
        const countEl = document.getElementById("memoConflictCount");
        if (!modal || !bodyEl) return;
        const c = pendingConflict();
        if (!c) {
          modal.hidden = true;
          m.resolving = false;
          pullAndReconcile(); // catch anything that changed while resolving
          return;
        }
        m.resolving = true;
        modal.hidden = false;
        const remaining = Object.keys(m.conflicts).length;
        if (countEl) countEl.textContent = remaining > 1 ? t("confRemaining").replace("{0}", remaining) : "";

        bodyEl.innerHTML = "";
        const name = document.createElement("div");
        name.className = "memo-conflict-name";
        name.textContent = displayName(c.mine).text;
        bodyEl.appendChild(name);

        const note = document.createElement("div");
        note.className = "memo-conflict-note";
        const actions = document.createElement("div");
        actions.className = "memo-conflict-actions";

        if (c.kind === "deleted") {
          note.textContent = t("confDeletedNote");
          bodyEl.appendChild(note);
          const col = document.createElement("div");
          col.className = "memo-conflict-col";
          const lbl = document.createElement("div");
          lbl.className = "memo-conflict-col-label";
          lbl.textContent = t("confMine");
          const pre = document.createElement("div");
          pre.className = "memo-conflict-preview";
          pre.textContent = conflictPreview(c.mine);
          col.appendChild(lbl); col.appendChild(pre);
          bodyEl.appendChild(col);
          actions.appendChild(makeConflictBtn(t("confRecreate"), true, function () { resolveKeepMine(c.id); }));
          actions.appendChild(makeConflictBtn(t("confAcceptDelete"), false, function () { resolveTakeServer(c.id); }));
        } else {
          note.textContent = t("confEditNote");
          bodyEl.appendChild(note);
          const cols = document.createElement("div");
          cols.className = "memo-conflict-cols";
          const mk = function (label, memo) {
            const col = document.createElement("div");
            col.className = "memo-conflict-col";
            const lbl = document.createElement("div");
            lbl.className = "memo-conflict-col-label";
            lbl.textContent = label;
            const pre = document.createElement("div");
            pre.className = "memo-conflict-preview";
            pre.textContent = conflictPreview(memo);
            col.appendChild(lbl); col.appendChild(pre);
            return col;
          };
          cols.appendChild(mk(t("confMine"), c.mine));
          cols.appendChild(mk(t("confServer"), c.theirs));
          bodyEl.appendChild(cols);
          actions.appendChild(makeConflictBtn(t("confKeepMine"), true, function () { resolveKeepMine(c.id); }));
          actions.appendChild(makeConflictBtn(t("confTakeServer"), false, function () { resolveTakeServer(c.id); }));
          actions.appendChild(makeConflictBtn(t("confKeepBoth"), false, function () { resolveKeepBoth(c.id); }));
        }
        bodyEl.appendChild(actions);
      }

      function flushBeacon() {
        if (!m.dirty.size || !navigator.sendBeacon) return;
        const batch = [];
        for (const id of m.dirty) { const memo = getMemo(id); if (memo) batch.push(memo); }
        if (!batch.length) return;
        try {
          const blob = new Blob([JSON.stringify({ memos: batch })], { type: "application/json" });
          navigator.sendBeacon("/api/memos/save", blob);
        } catch (e) {}
      }

      // ── wire up ──
      titleEl.addEventListener("input", function () { onEdit("title", titleEl.value); });
      areaEl.addEventListener("input", function () { onEdit("body", areaEl.value); });
      if (newBtnEl) newBtnEl.addEventListener("click", newMemo);

      // Backlink: jump to the file/heading the active memo was captured from.
      if (backlinkEl) {
        backlinkEl.addEventListener("click", async function (ev) {
          ev.preventDefault();
          const memo = getMemo(m.activeId);
          if (!memo || !memo.sourcePath || typeof selectFile !== "function") return;
          await selectFile(memo.sourcePath, memo.sourceHash ? { hash: memo.sourceHash } : {});
          // After the file renders, jump to + flash the exact quoted spot.
          if (memo.sourceQuote) {
            setTimeout(function () { highlightQuoteInPreview(memo.sourceQuote); }, 140);
          }
        });
      }

      // ── selection → memo ──
      // Show a floating "메모로 저장" button when the user selects text in the
      // rendered document; capture the nearest preceding heading as a backlink.
      function nearestHeadingForRange(range) {
        const headings = previewBodyEl.querySelectorAll("h1, h2, h3, h4, h5, h6");
        if (!headings.length) return null;
        let rectTop;
        try { rectTop = range.getBoundingClientRect().top; } catch (e) { return null; }
        const baseTop = previewBodyEl.getBoundingClientRect().top - previewBodyEl.scrollTop;
        const selTop = rectTop - baseTop;
        let best = null;
        for (const h of headings) {
          if (h.offsetTop <= selTop + 1) best = h; else break;
        }
        best = best || headings[0];
        return { hash: best.id, heading: (best.textContent || "").trim() };
      }
      function currentPreviewSelection() {
        const sel = window.getSelection && window.getSelection();
        if (!sel || sel.isCollapsed || sel.rangeCount === 0) return null;
        const text = sel.toString().trim();
        if (!text) return null;
        const range = sel.getRangeAt(0);
        // Selection must be inside the rendered preview body.
        const anc = range.commonAncestorContainer;
        const node = anc.nodeType === 1 ? anc : anc.parentNode;
        if (!node || !previewBodyEl.contains(node)) return null;
        return { text: text, range: range };
      }
      function hideSelectionBtn() { if (selectionBarEl) selectionBarEl.hidden = true; }
      function maybeShowSelectionBtn() {
        if (!selectionBarEl) return;
        const s = currentPreviewSelection();
        if (!s) { hideSelectionBtn(); return; }
        let rect;
        try { rect = s.range.getBoundingClientRect(); } catch (e) { hideSelectionBtn(); return; }
        if (!rect || (!rect.width && !rect.height)) { hideSelectionBtn(); return; }
        selectionBarEl.style.left = (rect.left + rect.width / 2) + "px";
        selectionBarEl.style.top = rect.bottom + "px";
        selectionBarEl.hidden = false;
      }
      if (selectionBarEl) {
        document.addEventListener("mouseup", function () { setTimeout(maybeShowSelectionBtn, 0); });
        document.addEventListener("keyup", function (e) {
          if (e.shiftKey || e.key === "Shift") setTimeout(maybeShowSelectionBtn, 0);
        });
        previewBodyEl.addEventListener("scroll", hideSelectionBtn);
        // mousedown outside the bar hides it (but not when pressing a bar button).
        document.addEventListener("mousedown", function (e) {
          if (!selectionBarEl.contains(e.target)) hideSelectionBtn();
        });
        if (selectionMemoBtnEl) {
          selectionMemoBtnEl.addEventListener("click", function () {
            const s = currentPreviewSelection();
            if (!s) { hideSelectionBtn(); return; }
            const src = nearestHeadingForRange(s.range) || {};
            src.path = state.selectedPath || "";
            createMemoFromSelection(s.text, src);
            // Surface the new memo: reveal the panel and switch to the Memo tab.
            if (state.searchPanelCollapsed) { state.searchPanelCollapsed = false; applySearchPanelCollapsed(); }
            setPanelTab("memo");
            hideSelectionBtn();
            if (window.getSelection) window.getSelection().removeAllRanges();
          });
        }
        if (selectionSearchBtnEl) {
          selectionSearchBtnEl.addEventListener("click", function () {
            const s = currentPreviewSelection();
            if (!s) { hideSelectionBtn(); return; }
            const q = s.text.replace(/\s+/g, " ").trim();
            // Reveal the panel, switch to Search, and run the query for the selection.
            if (state.searchPanelCollapsed) { state.searchPanelCollapsed = false; applySearchPanelCollapsed(); }
            setPanelTab("search");
            if (searchPanelInputEl) searchPanelInputEl.value = q;
            state.searchQueryRight = q;
            runInFileSearch(q);
            runFolderSearch(q);
            hideSelectionBtn();
            if (window.getSelection) window.getSelection().removeAllRanges();
          });
        }
        if (selectionCopyBtnEl) {
          selectionCopyBtnEl.addEventListener("click", async function () {
            const s = currentPreviewSelection();
            if (!s) { hideSelectionBtn(); return; }
            const ok = await copyTextToClipboard(s.text);
            showToast(ok ? t("toastSelCopied") : t("toastCopyFail"),
              ok ? { kind: "ok", icon: "📋" } : { kind: "err", icon: "⚠️" });
            hideSelectionBtn();
            if (window.getSelection) window.getSelection().removeAllRanges();
          });
        }
      }

      if (filterEl) {
        filterEl.addEventListener("input", function () { m.filter = filterEl.value; renderList(); });
      }
      function applyMemoSort(mode) {
        m.sort = (mode === "created" || mode === "title") ? mode : "updated";
        try { localStorage.setItem(LS_SORT, m.sort); } catch (e) {}
        if (sortUpdatedEl) sortUpdatedEl.classList.toggle("active", m.sort === "updated");
        if (sortCreatedEl) sortCreatedEl.classList.toggle("active", m.sort === "created");
        if (sortTitleEl) sortTitleEl.classList.toggle("active", m.sort === "title");
        renderList();
      }
      if (sortUpdatedEl) sortUpdatedEl.addEventListener("click", function () { applyMemoSort("updated"); });
      if (sortCreatedEl) sortCreatedEl.addEventListener("click", function () { applyMemoSort("created"); });
      if (sortTitleEl) sortTitleEl.addEventListener("click", function () { applyMemoSort("title"); });

      if (trashToggleEl) trashToggleEl.addEventListener("click", function () { m.trashOpen = !m.trashOpen; renderTrash(); });
      if (trashEmptyBtnEl) trashEmptyBtnEl.addEventListener("click", emptyTrash);

      if (copyBtnEl) {
        copyBtnEl.addEventListener("click", async function () {
          const memo = getMemo(m.activeId);
          const body = memo ? (memo.body || "") : "";
          if (!body.trim()) {
            showToast(t("toastMemoEmpty"), { kind: "err", icon: "⚠️" });
            return;
          }
          const title = memo ? (memo.title || "").trim() : "";
          // Link target: the memo's source (where it was captured), else the
          // currently-open file. Works as a clickable backlink inside mdviewer.
          const linkTarget = (memo && memo.sourcePath)
            ? (memo.sourcePath + (memo.sourceHash ? ("#" + memo.sourceHash) : ""))
            : (state.selectedPath || "");
          let payload;
          if (title) {
            // Title as a markdown link to the source, then the body.
            const head = linkTarget ? ("[" + title + "](" + linkTarget + ")") : ("# " + title);
            payload = head + "\n\n" + body;
          } else {
            const header = state.selectedPath
              ? (state.selectedPath.split("/").pop() + "  (" + state.selectedPath + ")")
              : "(no file open)";
            payload = header + "\n" + "─".repeat(Math.min(header.length, 60)) + "\n\n" + body;
          }
          try {
            if (navigator.clipboard && navigator.clipboard.writeText) {
              await navigator.clipboard.writeText(payload);
            } else {
              const ta = document.createElement("textarea");
              ta.value = payload;
              document.body.appendChild(ta);
              ta.select();
              document.execCommand("copy");
              document.body.removeChild(ta);
            }
            showToast(title ? t("toastMemoCopiedTitle") : t("toastMemoCopiedFile"), { kind: "ok", icon: "📋" });
          } catch (e) {
            showToast(t("toastClipFail") + ": " + (e && e.message || e), { kind: "err", icon: "⚠️" });
          }
        });
      }

      // ── init ──
      try { m.sort = localStorage.getItem(LS_SORT) || "updated"; } catch (e) {}
      if (m.sort !== "created" && m.sort !== "title") m.sort = "updated";
      m.memos = loadLocal();
      m.trash = loadTrash();
      // One-time migration of the old single-memo scratchpad.
      if (!m.memos.length) {
        let legacy = "";
        try { legacy = localStorage.getItem(LS_LEGACY) || ""; } catch (e) {}
        if (legacy.trim()) {
          const t = nowISO();
          m.memos.push({ id: genId(), title: "", body: legacy, pinned: false, createdAt: t, updatedAt: t });
          m.dirty.add(m.memos[0].id);
          try { localStorage.removeItem(LS_LEGACY); } catch (e) {}
          persistLocal();
        }
      }
      sortMemos();
      m.activeId = m.memos[0] ? m.memos[0].id : null;
      applyMemoSort(m.sort); // sets active toggle button + renders
      syncActiveEditor();
      pullAndReconcile(true);             // initial load: silent merge
      setInterval(flushDirty, 15000);     // push safety net
      setInterval(function () { pullAndReconcile(false); }, 10000); // pull other sessions' changes
      document.addEventListener("visibilitychange", function () {
        if (document.visibilityState === "visible") pullAndReconcile(false);
      });
      window.addEventListener("beforeunload", flushBeacon);
      // Re-render memo list/editor labels (relative time, trash, backlink) on language toggle.
      window.__refreshMemoPanel = function () {
        try { renderList(); } catch (e) {}
        try { syncActiveEditor(); } catch (e) {}
        try { if (m.resolving) renderConflictPopup(); } catch (e) {}
      };
    })();

    // Sort-mode toggle for the in-file search list.
    function applySearchSortMode(mode) {
      state.searchSortMode = (mode === "priority") ? "priority" : "line";
      try { localStorage.setItem("mdviewer.searchSortMode", state.searchSortMode); } catch (e) {}
      const btnLine = document.getElementById("searchSortLine");
      const btnPri  = document.getElementById("searchSortPriority");
      if (btnLine) btnLine.classList.toggle("active", state.searchSortMode === "line");
      if (btnPri)  btnPri .classList.toggle("active", state.searchSortMode === "priority");
      // Re-render the existing list under the new order (no need to
      // re-walk the DOM since hits[] is already populated).
      if (state.searchQueryRight) {
        renderInFileResults(state.searchQueryRight, state.searchInFileHits);
      }
    }
    applySearchSortMode(state.searchSortMode);
    {
      const btnLine = document.getElementById("searchSortLine");
      const btnPri  = document.getElementById("searchSortPriority");
      if (btnLine) btnLine.addEventListener("click", function () { applySearchSortMode("line"); });
      if (btnPri)  btnPri .addEventListener("click", function () { applySearchSortMode("priority"); });
    }
    searchInputEl.addEventListener("input", (event) => {
      state.searchQuery = event.target.value || "";
      renderFilePane();
    });
    if (aidlcToggleEl) {
      aidlcToggleEl.addEventListener("click", toggleAidlcMode);
    }
    sortNameEl.addEventListener("click", () => {
      if (state.sortKey === "name") {
        state.sortDirection = state.sortDirection === "asc" ? "desc" : "asc";
      } else {
        state.sortKey = "name";
        state.sortDirection = "asc";
      }
      updateSortButtons();
      renderFilePane();
    });
    sortModEl.addEventListener("click", () => {
      if (state.sortKey === "mod") {
        state.sortDirection = state.sortDirection === "asc" ? "desc" : "asc";
      } else {
        state.sortKey = "mod";
        state.sortDirection = "desc";
      }
      updateSortButtons();
      renderFilePane();
    });

    // ---------- Mermaid Playground modal ----------
    let mermaidLabRenderTimer = null;
    let mermaidLabIdCounter = 0;

    function openMermaidLab() {
      mermaidLabModalEl.hidden = false;
      setTimeout(function() { mermaidLabEditorEl.focus(); }, 0);
      renderMermaidLab(mermaidLabEditorEl.value);
    }

    function closeMermaidLab() {
      mermaidLabModalEl.hidden = true;
      clearTimeout(mermaidLabRenderTimer);
    }

    // stripMermaidFence accepts inputs wrapped in a markdown fenced code
    // block (triple-backtick or triple-tilde, optionally tagged "mermaid")
    // and returns just the inner mermaid source. Lets the user paste
    // straight from Markdown / chat tools.
    function stripMermaidFence(text) {
      let s = (text || "").trim();
      // Use \x60 to express the backtick safely inside this string (the
      // surrounding Go raw string would otherwise close on a literal
      // backtick in the regex source).
      s = s.replace(/^[\x60~]{3,}[ \t]*(?:mermaid|mmd)?[ \t]*\r?\n?/i, "");
      s = s.replace(/\r?\n?[\x60~]{3,}[ \t]*$/i, "");
      return s.trim();
    }

    // Detect TeX math so the playground can preview formulas as well as
    // diagrams. Mermaid sources practically never contain $…$ delimiters.
    function looksLikeMath(s) {
      return /\$\$[\s\S]+?\$\$/.test(s) ||
        /(^|[^\\])\$[^$\n]+\$/.test(s) ||
        /\\\([\s\S]+?\\\)/.test(s) ||
        /\\\[[\s\S]+?\\\]/.test(s);
    }

    async function renderMermaidLab(src) {
      const text = stripMermaidFence(src);
      mermaidLabPreviewEl.innerHTML = "";
      if (!text) {
        const hint = document.createElement("div");
        hint.className = "subtle";
        hint.textContent = t("mlHint");
        mermaidLabPreviewEl.appendChild(hint);
        return;
      }
      // Math branch: render with KaTeX instead of mermaid.
      if (looksLikeMath(text)) {
        const host = document.createElement("div");
        host.className = "mermaid-lab-math";
        host.textContent = text;
        mermaidLabPreviewEl.appendChild(host);
        if (typeof window.renderMathInElement === "function") {
          renderMathSafe(host);
        } else {
          const note = document.createElement("div");
          note.className = "subtle";
          note.textContent = t("katexLoading");
          mermaidLabPreviewEl.appendChild(note);
        }
        return;
      }
      mermaidLabIdCounter += 1;
      const id = "mermaidLabRender" + mermaidLabIdCounter;
      try {
        if (!window.mermaid || typeof window.mermaid.render !== "function") {
          throw new Error("mermaid library not loaded");
        }
        const result = await window.mermaid.render(id, text);
        const host = document.createElement("div");
        host.className = "mermaid";
        host.innerHTML = result.svg;
        host.title = "Click to zoom in lightbox";
        host.style.cursor = "zoom-in";
        host.addEventListener("click", function () {
          openLightbox(buildLightboxClone(host));
        });
        mermaidLabPreviewEl.appendChild(host);
        if (typeof result.bindFunctions === "function") {
          try { result.bindFunctions(mermaidLabPreviewEl); } catch (e) {}
        }
      } catch (err) {
        const errBox = document.createElement("div");
        errBox.className = "mermaid-error";
        errBox.textContent = t("mlRenderFail") + "\n" + (err && err.message ? err.message : String(err));
        mermaidLabPreviewEl.appendChild(errBox);
      }
    }

    mermaidLabBtnEl.onclick = openMermaidLab;
    mermaidLabCloseBtnEl.onclick = closeMermaidLab;
    mermaidLabClearBtnEl.onclick = function() {
      mermaidLabEditorEl.value = "";
      renderMermaidLab("");
      mermaidLabEditorEl.focus();
    };
    mermaidLabCopyBtnEl.onclick = async function() {
      try {
        await navigator.clipboard.writeText(mermaidLabEditorEl.value || "");
        mermaidLabCopyBtnEl.textContent = t("mlCopied");
        setTimeout(function() { mermaidLabCopyBtnEl.textContent = t("mlCopy"); }, 1000);
      } catch (e) {
        mermaidLabCopyBtnEl.textContent = t("mlFailed");
        setTimeout(function() { mermaidLabCopyBtnEl.textContent = t("mlCopy"); }, 1000);
      }
    };
    mermaidLabEditorEl.addEventListener("input", function(event) {
      const text = event.target.value;
      clearTimeout(mermaidLabRenderTimer);
      mermaidLabRenderTimer = setTimeout(function() { renderMermaidLab(text); }, 150);
    });
    mermaidLabModalEl.addEventListener("click", function(event) {
      if (event.target === mermaidLabModalEl) closeMermaidLab();
    });

    // ---------- Theme toggle (Auto → Light → Dark) ----------
    const themeToggleEl = document.getElementById("themeToggle");
    const THEME_ORDER = ["auto", "light", "dark"];
    function themeLabel(theme) {
      if (theme === "light") return t("themeLight");
      if (theme === "dark") return t("themeDark");
      return t("themeAuto");
    }
    const THEME_ICON = {
      auto:  '<svg class="ico" viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M12 3v18"/><path d="M12 3a9 9 0 0 1 0 18Z" fill="currentColor" stroke="none"/></svg>',
      light: '<svg class="ico" viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.41-1.41M17.66 6.34l1.41-1.41"/></svg>',
      dark:  '<svg class="ico" viewBox="0 0 24 24" aria-hidden="true"><path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8Z"/></svg>',
    };
    function currentTheme() {
      try { return localStorage.getItem("mdviewer.theme") || "auto"; }
      catch (e) { return "auto"; }
    }
    function syncHljsTheme(theme) {
      const dark = (theme === "dark") || (theme === "auto" &&
        window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches);
      const linkDark = document.getElementById("hljs-theme-dark");
      const linkLight = document.getElementById("hljs-theme-light");
      if (linkDark) linkDark.disabled = !dark;
      if (linkLight) linkLight.disabled = dark;
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
        const label = themeLabel(theme);
        themeToggleEl.title = t("themeTitle").replace("{0}", label);
        themeToggleEl.setAttribute("aria-label", t("themeAria").replace("{0}", label));
      }
      syncHljsTheme(theme);
    }
    applyTheme(currentTheme());

    // ── Language toggle init (English / 한국어) ──
    {
      const langBtn = document.getElementById("langToggle");
      if (langBtn) {
        langBtn.addEventListener("click", function () {
          setLang(state.lang === "ko" ? "en" : "ko");
          // Re-render the welcome guide in the new language if it's showing.
          if (!state.selectedPath) showUsageGuide();
        });
      }
      document.documentElement.setAttribute("lang", state.lang);
      applyI18n();
      updateLangToggle();
    }

    // In auto mode the effective theme can flip when the OS toggles dark
    // mode at runtime; keep the hljs stylesheet in step.
    if (window.matchMedia) {
      try {
        window.matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => {
          if (currentTheme() === "auto") syncHljsTheme("auto");
        });
      } catch (e) {}
    }
    if (themeToggleEl) {
      themeToggleEl.onclick = () => {
        const cur = currentTheme();
        const next = THEME_ORDER[(THEME_ORDER.indexOf(cur) + 1) % THEME_ORDER.length];
        applyTheme(next);
      };
    }

    // ---------- Accent color picker ----------
    // Override --accent (used everywhere via var()/color-mix) with a user choice,
    // persisted in localStorage. Empty value = revert to the built-in pink,
    // which stays adaptive to light/dark.
    (function setupAccent() {
      const btn = document.getElementById("accentBtn");
      const pop = document.getElementById("accentPopover");
      const swatchWrap = document.getElementById("accentSwatches");
      const custom = document.getElementById("accentCustom");
      const reset = document.getElementById("accentReset");
      if (!btn || !pop) return;
      const LS = "mdviewer.accent";
      const PRESETS = [
        "#e25aa6", "#7c5cff", "#3b82f6", "#06b6d4",
        "#10b981", "#f59e0b", "#ef4444", "#64748b",
      ];

      function current() { try { return localStorage.getItem(LS) || ""; } catch (e) { return ""; } }
      function apply(color, persist) {
        if (color) document.documentElement.style.setProperty("--accent", color);
        else document.documentElement.style.removeProperty("--accent");
        if (persist) { try { color ? localStorage.setItem(LS, color) : localStorage.removeItem(LS); } catch (e) {} }
        markActive(color);
      }
      function markActive(color) {
        const lc = (color || "").toLowerCase();
        for (const s of swatchWrap.querySelectorAll(".accent-swatch")) {
          s.classList.toggle("active", s.dataset.color.toLowerCase() === lc);
        }
        if (custom && color) custom.value = color;
      }
      // Build preset swatches.
      for (const c of PRESETS) {
        const s = document.createElement("button");
        s.type = "button";
        s.className = "accent-swatch";
        s.dataset.color = c;
        s.style.background = c;
        s.title = c;
        s.addEventListener("click", function () { apply(c, true); });
        swatchWrap.appendChild(s);
      }
      if (custom) custom.addEventListener("input", function () { apply(custom.value, true); });
      if (reset) reset.addEventListener("click", function () { apply("", true); });

      function openPop() {
        markActive(current());
        pop.hidden = false;
        const r = btn.getBoundingClientRect();
        const w = pop.offsetWidth || 230;
        let left = r.right - w;
        if (left < 8) left = 8;
        pop.style.top = (r.bottom + 8) + "px";
        pop.style.left = left + "px";
      }
      function closePop() { pop.hidden = true; }
      btn.addEventListener("click", function (e) {
        e.stopPropagation();
        if (pop.hidden) openPop(); else closePop();
      });
      document.addEventListener("mousedown", function (e) {
        if (!pop.hidden && !pop.contains(e.target) && e.target !== btn && !btn.contains(e.target)) closePop();
      });
      document.addEventListener("keydown", function (e) {
        if (!pop.hidden && e.key === "Escape") { e.preventDefault(); closePop(); }
      });

      // Apply any saved choice on load (the pre-paint script already set the
      // CSS var; this keeps the swatch UI in sync).
      apply(current(), false);
    })();

    document.getElementById("refreshButton").onclick = () => {
      // selectFile's dirty guard exempts same-path navigations, so a
      // refresh (same path → reload from disk) would silently clobber
      // the editor draft. Prompt explicitly here.
      if (!confirmDiscardDirty()) return;
      return state.selectedPath
        ? selectFile(state.selectedPath, { hash: state.selectedHash, historyMode: "replace" })
        : loadDir(state.cwd, { historyMode: "replace" });
    };
    document.getElementById("toggleFavorite").onclick = toggleFavorite;
    // Clear is now inside the "Show all" popup (popupClear button) —
    // wired below in openListPopup based on state.popupKind.
    document.getElementById("popupClear").onclick = () => {
      if (state.popupKind === "recentFiles") {
        if (state.recentFiles.length && confirm("Clear recent files list?")) {
          clearRecents("files");
          closeListPopup();
        }
      } else if (state.popupKind === "recentDirs") {
        if (state.recentDirs.length && confirm("Clear recent folders list?")) {
          clearRecents("dirs");
          closeListPopup();
        }
      }
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
      // Clear is only meaningful for the two recents lists; favorites
      // has no bulk-clear semantics.
      const clearBtn = document.getElementById("popupClear");
      if (clearBtn) {
        clearBtn.hidden = !(kind === "recentFiles" || kind === "recentDirs");
      }
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

        // For favorites, expose reorder ▲▼ + an inline edit-alias button.
        if (it._kind === "favorite") {
          if (!q) row.appendChild(buildFavMoveControl(it.path)); // only on the full list
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
      // Drag-to-reorder favorites in the modal — only when the full,
      // unfiltered list is shown (reordering a filtered subset is ambiguous).
      if (state.popupKind === "favorites" && !q) {
        setupFavReorder(popupResultsEl, function (domOrder) { commitFavoritesOrder(domOrder); });
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

    // ---- Folder Browse Modal ----
    let fbAllGroups = [];

    function fbApplyScope(scope) {
      let next = (scope === "git") ? "git" : "folder";
      if (next === "git" && state.gitRepoRoot === "") next = "folder"; // not a repo
      state.fbScope = next;
      try { localStorage.setItem("mdviewer.fbScope", state.fbScope); } catch (e) {}
      const bf = document.getElementById("fbScopeFolder");
      const bg = document.getElementById("fbScopeGit");
      if (bf) bf.classList.toggle("active", state.fbScope === "folder");
      if (bg) bg.classList.toggle("active", state.fbScope === "git");
    }

    function loadFolderBrowse() {
      fbResultsEl.innerHTML = '<div class="fb-loading">' + t("fbLoading") + '</div>';
      fbAllGroups = [];
      const cwd = state.cwd || "";
      const url = "/api/list-recursive?dir=" + encodeURIComponent(cwd) +
                  (state.fbScope === "git" ? "&scope=git" : "");
      fetch(url)
        .then(function(r) { return r.ok ? r.json() : Promise.reject(r); })
        .then(function(groups) {
          fbAllGroups = groups || [];
          renderFolderBrowse(fbSearchEl.value);
        })
        .catch(function() {
          fbResultsEl.innerHTML = '<div class="fb-empty">' + t("fbLoadFail") + '</div>';
        });
    }

    async function openFolderBrowse() {
      folderBrowseModalEl.hidden = false;
      // Carry over whatever the user already typed in the sidebar file search
      // so the recursive browser opens pre-filtered to the same query.
      const seed = (searchInputEl && searchInputEl.value) ? searchInputEl.value.trim() : "";
      fbSearchEl.value = seed;
      fbResultsEl.innerHTML = '<div class="fb-loading">' + t("fbLoading") + '</div>';
      const cwd = state.cwd || "";
      const shortCwd = cwd ? cwd.replace(/.*\//, "") || cwd : "";
      folderBrowseTitleEl.textContent = shortCwd ? t("fbTitleWith").replace("{0}", shortCwd) : t("fbTitle");
      fbAllGroups = [];
      // Focus and select the seeded text so the user can refine or replace it.
      setTimeout(() => { try { fbSearchEl.focus(); fbSearchEl.select(); } catch (e) {} }, 50);
      await refreshGitScope();   // resolve git availability → gates the Git toggle
      fbApplyScope(state.fbScope);
      loadFolderBrowse();
    }

    function closeFolderBrowse() {
      folderBrowseModalEl.hidden = true;
      fbAllGroups = [];
    }

    function fbHighlightName(name, query) {
      if (!query) return escapeHtml(name);
      const lname = name.toLowerCase();
      const lq = query.toLowerCase();
      const idx = lname.indexOf(lq);
      if (idx < 0) return escapeHtml(name);
      return escapeHtml(name.slice(0, idx)) +
        "<mark>" + escapeHtml(name.slice(idx, idx + query.length)) + "</mark>" +
        escapeHtml(name.slice(idx + query.length));
    }

    function renderFolderBrowse(query) {
      fbResultsEl.innerHTML = "";
      const q = (query || "").trim();
      const lq = q.toLowerCase();
      const cwd = state.cwd || "";
      let hasAny = false;

      for (const group of fbAllGroups) {
        const files = lq
          ? group.files.filter(function(f) { return f.name.toLowerCase().includes(lq); })
          : group.files;
        if (!files.length) continue;
        hasAny = true;

        // Folder header - show relative path from cwd
        let relDir = group.dir;
        if (group.dir === cwd) {
          relDir = "./";
        } else if (group.dir.startsWith(cwd + "/")) {
          relDir = group.dir.slice(cwd.length + 1) + "/";
        }

        const header = document.createElement("div");
        header.className = "fb-group-header";
        header.title = group.dir;
        header.textContent = "📁 " + relDir;
        fbResultsEl.appendChild(header);

        for (const file of files) {
          const row = document.createElement("div");
          row.className = "fb-file" + (file.path === state.selectedPath ? " active" : "");
          row.title = file.path;

          const iconEl = document.createElement("span");
          iconEl.className = "fb-file-icon";
          iconEl.textContent = "📄";
          row.appendChild(iconEl);

          const nameEl = document.createElement("span");
          nameEl.className = "fb-file-name";
          nameEl.innerHTML = fbHighlightName(file.name, q);
          row.appendChild(nameEl);

          const timeEl = document.createElement("span");
          timeEl.className = "fb-file-time";
          if (file.mod_time) {
            try {
              timeEl.textContent = relativeTime(new Date(file.mod_time).getTime());
              timeEl.title = new Date(file.mod_time).toLocaleString();
            } catch (e) {}
          }
          row.appendChild(timeEl);

          row.onclick = function(path) {
            return function() {
              closeFolderBrowse();
              selectFile(path, { historyMode: "push" });
            };
          }(file.path);
          fbResultsEl.appendChild(row);
        }
      }

      if (!hasAny) {
        const empty = document.createElement("div");
        empty.className = "fb-empty";
        empty.textContent = lq ? t("fbNoMatch") : t("fbNoFiles");
        fbResultsEl.appendChild(empty);
      }
    }

    browseSubfoldersBtnEl.addEventListener("click", openFolderBrowse);
    document.getElementById("folderBrowseClose").onclick = closeFolderBrowse;
    {
      const fbF = document.getElementById("fbScopeFolder");
      const fbG = document.getElementById("fbScopeGit");
      if (fbF) fbF.addEventListener("click", function () { fbApplyScope("folder"); loadFolderBrowse(); });
      if (fbG) fbG.addEventListener("click", function () { fbApplyScope("git"); loadFolderBrowse(); });
    }
    folderBrowseModalEl.addEventListener("click", function(e) {
      if (e.target === folderBrowseModalEl) closeFolderBrowse();
    });
    fbSearchEl.addEventListener("input", function() {
      renderFolderBrowse(fbSearchEl.value);
    });
    fbSearchEl.addEventListener("keydown", function(e) {
      if (e.key === "Escape") { e.preventDefault(); closeFolderBrowse(); }
    });

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
      // Esc closes the Mermaid Playground modal.
      if (event.key === "Escape" && !mermaidLabModalEl.hidden) {
        closeMermaidLab();
        return;
      }
      // Esc closes the folder browse modal.
      if (event.key === "Escape" && !folderBrowseModalEl.hidden) {
        closeFolderBrowse();
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
    const lightboxToolbarEls = lightboxEl.querySelectorAll(".lightbox-toolbar");
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

    // Baseline natural dims captured at the first successful fit.
    // Reset (double-click) reuses these — captureLightboxNatural rewrites
    // the SVG viewBox, so a re-measurement runs in a warped frame and
    // produces a different result, which was making the post-zoom-out
    // reset jump to the wrong scale.
    var _lbBaselineW = 0;
    var _lbBaselineH = 0;
    var _lbAnnoActive = null;
    var _lbAnnoStartedAt = 0;
    var _lbDrawMode = false;
    var _lbDrawColor = "#ff3b30";
    var _lbDrawOpacity = 0.5;
    // _lbHistory and _lbRedoStack implement a command-pattern undo/redo:
    // every draw, erase, and clear-all pushes an action; ↶ reverses the
    // last action and shifts it onto _lbRedoStack; ↷ re-applies it.
    var _lbHistory = [];
    var _lbRedoStack = [];
    var _lbEraserMode = false;
    var _lbPostitMode = false;
    var _lbPostitDrag = null; // { g, kind: "move"|"resize", startX, startY, before, didMove }
    var _lbActivePostitEditor = null;
    var _lbEraseDrag = null; // { erased: [el, ...] } while pointer is held in eraser mode
    const POSTIT_W = 160;
    const POSTIT_PAD = 10;
    const POSTIT_FONT = 14;
    const POSTIT_LH = 18;
    // Measure text in post-it user units (font-size renders 1:1 with px at the
    // SVG's natural scale) so the rendered note wraps exactly like the editor.
    let _postitMeasureCtx = null;
    function measurePostitText(s) {
      if (!_postitMeasureCtx) {
        _postitMeasureCtx = document.createElement("canvas").getContext("2d");
        _postitMeasureCtx.font = POSTIT_FONT + "px system-ui, -apple-system, sans-serif";
      }
      return _postitMeasureCtx.measureText(s).width;
    }
    // Word-wrap a single logical line to maxW user units, breaking at spaces
    // when possible and per-character otherwise (handles spaceless CJK runs).
    function wrapPostitLine(line, maxW) {
      if (!line) return [""];
      const out = [];
      let cur = "";
      for (const ch of line) {
        const test = cur + ch;
        if (cur && measurePostitText(test) > maxW) {
          const sp = cur.lastIndexOf(" ");
          if (sp > 0) { out.push(cur.slice(0, sp)); cur = cur.slice(sp + 1) + ch; }
          else { out.push(cur); cur = ch; }
        } else {
          cur = test;
        }
      }
      if (cur) out.push(cur);
      return out.length ? out : [""];
    }
    function fitLightboxContent() {
      const child = lightboxStageEl.firstElementChild;
      if (!child) return;
      const target = findScalableTarget(child);
      if (_lbBaselineW > 0 && _lbBaselineH > 0) {
        const vw = window.innerWidth, vh = window.innerHeight;
        const cap = target && target.kind === "svg" ? 12 : 2;
        const fit = Math.min(cap, Math.min(vw * 0.92 / _lbBaselineW, vh * 0.86 / _lbBaselineH));
        lbState.scale = fit > 0 ? fit : 1;
        lbState.x = (vw - _lbBaselineW * lbState.scale) / 2;
        lbState.y = (vh - _lbBaselineH * lbState.scale) / 2;
        applyLightboxTransform();
        return;
      }
      // Make sure we know the natural dimensions before sizing.
      captureLightboxNatural();
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
        _lbBaselineW = natW;
        _lbBaselineH = natH;
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
          _lbBaselineW = w;
          _lbBaselineH = h;
          // No scalable target → fall back to CSS transform scale on the stage.
          lightboxStageEl.style.transform = "translate(" + lbState.x + "px, " + lbState.y + "px) scale(" + lbState.scale + ")";
          lightboxScaleEl.textContent = Math.round(lbState.scale * 100) + "%";
        }
      });
    }

    // Annotations: polylines added DIRECTLY to the diagram's SVG so they
    // share its user-space coord system (no padding/overlay alignment
    // pitfalls). For raster images we skip drawing — no SVG to draw into.
    function getAnnotationSVG() {
      const child = lightboxStageEl.firstElementChild;
      const target = findScalableTarget(child);
      if (!target || target.kind !== "svg") return null;
      return target.el;
    }

    // screenToSVGUserSpace converts a viewport click into the SVG's own
    // user-space using getScreenCTM — robust against any combination of
    // padding, CSS transforms, viewBox, and zoom on the SVG.
    function screenToSVGUserSpace(svg, clientX, clientY) {
      if (!svg.createSVGPoint || !svg.getScreenCTM) {
        return { x: clientX, y: clientY };
      }
      const pt = svg.createSVGPoint();
      pt.x = clientX;
      pt.y = clientY;
      const ctm = svg.getScreenCTM();
      if (!ctm) return { x: clientX, y: clientY };
      return pt.matrixTransform(ctm.inverse());
    }

    function recordAnnoAction(action) {
      _lbHistory.push(action);
      _lbRedoStack = [];
    }

    function applyAnnoAction(action, reverse) {
      const svg = getAnnotationSVG();
      if (!svg) return;
      if (action.type === "add" || action.type === "remove") {
        const adding = (action.type === "add" && !reverse) || (action.type === "remove" && reverse);
        if (adding) {
          for (const s of action.strokes) svg.appendChild(s);
        } else {
          for (const s of action.strokes) s.remove();
        }
        return;
      }
      if (action.type === "transform") {
        applyPostitState(action.target, reverse ? action.before : action.after);
        return;
      }
      if (action.type === "text") {
        setPostitText(action.target, reverse ? action.before : action.after);
        return;
      }
    }

    function clearAnnotations() {
      const svg = getAnnotationSVG();
      if (!svg) return;
      const marks = Array.from(svg.querySelectorAll(".lb-annotation"));
      if (!marks.length) return;
      for (const el of marks) el.remove();
      _lbAnnoActive = null;
      recordAnnoAction({ type: "remove", strokes: marks });
    }

    function undoAnnotation() {
      if (!_lbHistory.length) return;
      const action = _lbHistory.pop();
      applyAnnoAction(action, true);
      _lbRedoStack.push(action);
    }

    function redoAnnotation() {
      if (!_lbRedoStack.length) return;
      const action = _lbRedoStack.pop();
      applyAnnoAction(action, false);
      _lbHistory.push(action);
    }

    function startAnnotationStroke(event) {
      const svg = getAnnotationSVG();
      if (!svg) return;
      const ns = "http://www.w3.org/2000/svg";
      const poly = document.createElementNS(ns, "polyline");
      poly.setAttribute("class", "lb-annotation");
      poly.setAttribute("fill", "none");
      poly.setAttribute("stroke", _lbDrawColor);
      poly.setAttribute("stroke-opacity", String(_lbDrawOpacity));
      poly.setAttribute("stroke-width", "3");
      poly.setAttribute("stroke-linecap", "round");
      poly.setAttribute("stroke-linejoin", "round");
      poly.setAttribute("vector-effect", "non-scaling-stroke");
      poly.style.pointerEvents = _lbEraserMode ? "stroke" : "none";
      const p = screenToSVGUserSpace(svg, event.clientX, event.clientY);
      poly.setAttribute("points", p.x + "," + p.y);
      svg.appendChild(poly);
      _lbAnnoActive = poly;
      _lbAnnoStartedAt = Date.now();
      try { lightboxEl.setPointerCapture(event.pointerId); } catch (e) {}
    }

    function continueAnnotationStroke(event) {
      if (!_lbAnnoActive) return;
      const svg = getAnnotationSVG();
      if (!svg) return;
      const p = screenToSVGUserSpace(svg, event.clientX, event.clientY);
      const cur = _lbAnnoActive.getAttribute("points") || "";
      _lbAnnoActive.setAttribute("points", cur + " " + p.x + "," + p.y);
    }

    function endAnnotationStroke(event) {
      if (!_lbAnnoActive) return;
      const stroke = _lbAnnoActive;
      const pts = (stroke.getAttribute("points") || "").trim().split(/\s+/);
      if (pts.length < 2) {
        stroke.remove();
      } else {
        recordAnnoAction({ type: "add", strokes: [stroke] });
      }
      _lbAnnoActive = null;
      try { lightboxEl.releasePointerCapture(event.pointerId); } catch (e) {}
    }

    function setDrawControlsVisible(visible) {
      const els = document.querySelectorAll(".lightbox-toolbar .draw-only");
      for (const el of els) {
        if (visible) el.removeAttribute("hidden");
        else el.setAttribute("hidden", "");
      }
    }

    function setAnnotationStrokesInteractive(interactive) {
      const svg = getAnnotationSVG();
      if (!svg) return;
      const marks = svg.querySelectorAll(".lb-annotation");
      // Polylines have fill:none so "all" only fires on the stroke;
      // post-it groups need "all" so the rect+text behave like one target.
      for (const el of marks) el.style.pointerEvents = interactive ? "all" : "none";
    }

    function toggleDrawMode() {
      _lbDrawMode = !_lbDrawMode;
      const btn = document.getElementById("lbAnnoDrawBtn");
      if (btn) btn.classList.toggle("active", _lbDrawMode);
      lightboxEl.classList.toggle("draw-mode", _lbDrawMode);
      setDrawControlsVisible(_lbDrawMode);
      // Leaving draw mode cancels every secondary tool — they only make
      // sense while drawing is active.
      if (!_lbDrawMode) {
        if (_lbEraserMode) {
          _lbEraserMode = false;
          const eb = document.getElementById("lbAnnoEraseBtn");
          if (eb) eb.classList.remove("active");
          lightboxEl.classList.remove("eraser-mode");
        }
        if (_lbPostitMode) {
          _lbPostitMode = false;
          const pb = document.getElementById("lbAnnoPostitBtn");
          if (pb) pb.classList.remove("active");
          lightboxEl.classList.remove("postit-mode");
        }
        setAnnotationStrokesInteractive(false);
      }
    }

    function syncAnnotationInteractive() {
      setAnnotationStrokesInteractive(_lbEraserMode || _lbPostitMode);
    }

    function toggleEraserMode() {
      if (!_lbDrawMode) return; // eraser button is hidden outside draw mode
      _lbEraserMode = !_lbEraserMode;
      if (_lbEraserMode && _lbPostitMode) togglePostitMode();
      const btn = document.getElementById("lbAnnoEraseBtn");
      if (btn) btn.classList.toggle("active", _lbEraserMode);
      lightboxEl.classList.toggle("eraser-mode", _lbEraserMode);
      syncAnnotationInteractive();
    }

    function enterPostitMode() {
      if (_lbPostitMode) return;
      _lbPostitMode = true;
      if (_lbEraserMode) toggleEraserMode();
      const btn = document.getElementById("lbAnnoPostitBtn");
      if (btn) btn.classList.add("active");
      lightboxEl.classList.add("postit-mode");
      syncAnnotationInteractive();
    }

    // The 📝 toolbar button is an "add new post-it" action — each press
    // drops a fresh note at the center of the visible area. We don't
    // open the text editor right away so the user can drag the empty
    // note into place first; a click on the note opens the editor when
    // they're ready to type.
    function addNewPostit() {
      if (!_lbDrawMode) return;
      const svg = getAnnotationSVG();
      if (!svg) return;
      enterPostitMode();
      const cx = window.innerWidth / 2;
      const cy = window.innerHeight / 2;
      const p = screenToSVGUserSpace(svg, cx, cy);
      const x = p.x - POSTIT_W / 2;
      const y = p.y - (POSTIT_PAD * 2 + POSTIT_LH) / 2;
      const g = createPostit(svg, x, y, "");
      recordAnnoAction({ type: "add", strokes: [g] });
    }

    function setPostitSize(g, w, h) {
      const body = g.querySelector(".lb-postit-body");
      if (body) {
        body.setAttribute("width", String(w));
        body.setAttribute("height", String(h));
      }
      const resize = g.querySelector(".lb-postit-resize");
      if (resize) {
        resize.setAttribute("x", String(w - 14));
        resize.setAttribute("y", String(h - 14));
      }
      const delG = g.querySelector(".lb-postit-delete");
      if (delG) delG.setAttribute("transform", "translate(" + w + ",0)");
      g.dataset.w = String(w);
      g.dataset.h = String(h);
    }

    function setPostitText(g, text) {
      const textEl = g.querySelector(".lb-postit-text");
      while (textEl.firstChild) textEl.removeChild(textEl.firstChild);
      const raw = (text == null ? "" : String(text));
      // Wrap to the note's content width so the rendered text never spills past
      // the box edge (matches how the editor textarea wraps).
      const boxW = parseFloat(g.dataset.w) || POSTIT_W;
      const maxW = Math.max(20, boxW - POSTIT_PAD * 2);
      const lines = [];
      const srcLines = raw.length ? raw.split("\n") : [""];
      for (const sl of srcLines) {
        const wrapped = wrapPostitLine(sl, maxW);
        for (const w of wrapped) lines.push(w);
      }
      const ns = "http://www.w3.org/2000/svg";
      for (let i = 0; i < lines.length; i++) {
        const tspan = document.createElementNS(ns, "tspan");
        tspan.setAttribute("x", String(POSTIT_PAD));
        tspan.setAttribute("dy", i === 0 ? "0" : String(POSTIT_LH));
        // Empty line still needs SOME content or the dy is ignored — use a
        // zero-width space so layout advances.
        tspan.textContent = lines[i].length ? lines[i] : "​";
        textEl.appendChild(tspan);
      }
      g.setAttribute("data-text", raw);
      // Auto-grow vertically unless the user has manually resized. Keep the
      // current width (manual or default) so wrapping stays consistent.
      if (!g.dataset.manualSize) {
        setPostitSize(g, boxW, POSTIT_PAD * 2 + lines.length * POSTIT_LH);
      }
    }

    function createPostit(svg, x, y, text) {
      const ns = "http://www.w3.org/2000/svg";
      const g = document.createElementNS(ns, "g");
      g.setAttribute("class", "lb-annotation lb-postit");
      g.setAttribute("transform", "translate(" + x + "," + y + ")");

      const body = document.createElementNS(ns, "rect");
      body.setAttribute("class", "lb-postit-body");
      body.setAttribute("rx", "6");
      body.setAttribute("fill", "#fff59d");
      body.setAttribute("stroke", "#fbc02d");
      body.setAttribute("stroke-width", "1");
      body.setAttribute("vector-effect", "non-scaling-stroke");
      g.appendChild(body);

      const textEl = document.createElementNS(ns, "text");
      textEl.setAttribute("class", "lb-postit-text");
      textEl.setAttribute("x", String(POSTIT_PAD));
      textEl.setAttribute("y", String(POSTIT_PAD + POSTIT_FONT));
      textEl.setAttribute("font-family", "system-ui, -apple-system, sans-serif");
      textEl.setAttribute("font-size", String(POSTIT_FONT));
      textEl.setAttribute("fill", "#2b2b2b");
      g.appendChild(textEl);

      // Resize grip — bottom-right.
      const resize = document.createElementNS(ns, "rect");
      resize.setAttribute("class", "lb-postit-resize");
      resize.setAttribute("width", "14");
      resize.setAttribute("height", "14");
      resize.setAttribute("rx", "2");
      resize.setAttribute("fill", "#fbc02d");
      g.appendChild(resize);

      // Delete button — top-right, anchored at (w,0).
      const delG = document.createElementNS(ns, "g");
      delG.setAttribute("class", "lb-postit-delete");
      const delCircle = document.createElementNS(ns, "circle");
      delCircle.setAttribute("r", "9");
      delCircle.setAttribute("fill", "#ef5350");
      delCircle.setAttribute("stroke", "#ffffff");
      delCircle.setAttribute("stroke-width", "1.5");
      const delText = document.createElementNS(ns, "text");
      delText.setAttribute("text-anchor", "middle");
      delText.setAttribute("dominant-baseline", "central");
      delText.setAttribute("font-size", "14");
      delText.setAttribute("fill", "#ffffff");
      delText.setAttribute("font-weight", "700");
      delText.textContent = "×";
      delG.appendChild(delCircle);
      delG.appendChild(delText);
      g.appendChild(delG);

      setPostitText(g, text || "");
      g.style.pointerEvents = (_lbEraserMode || _lbPostitMode) ? "all" : "none";
      svg.appendChild(g);
      return g;
    }

    function parsePostitTranslate(g) {
      const t = g.getAttribute("transform") || "";
      const m = /translate\(([-\d.]+)[,\s]+([-\d.]+)\)/.exec(t);
      if (!m) return { x: 0, y: 0 };
      return { x: parseFloat(m[1]), y: parseFloat(m[2]) };
    }

    function readPostitState(g) {
      const p = parsePostitTranslate(g);
      return {
        tx: p.x, ty: p.y,
        w: parseFloat(g.dataset.w) || POSTIT_W,
        h: parseFloat(g.dataset.h) || (POSTIT_PAD * 2 + POSTIT_LH),
        manualSize: !!g.dataset.manualSize,
      };
    }

    function applyPostitState(g, s) {
      g.setAttribute("transform", "translate(" + s.tx + "," + s.ty + ")");
      setPostitSize(g, s.w, s.h);
      if (s.manualSize) g.dataset.manualSize = "1";
      else delete g.dataset.manualSize;
    }

    function openPostitEditor(g) {
      const svg = getAnnotationSVG();
      if (!svg) return;
      const body = g.querySelector(".lb-postit-body");
      const ctm = g.getScreenCTM();
      if (!ctm) return;
      const w = parseFloat(body.getAttribute("width")) || POSTIT_W;
      const h = parseFloat(body.getAttribute("height")) || (POSTIT_PAD * 2 + POSTIT_LH);
      const pt = svg.createSVGPoint();
      pt.x = 0; pt.y = 0;
      const tl = pt.matrixTransform(ctm);
      pt.x = w; pt.y = h;
      const br = pt.matrixTransform(ctm);
      // If another editor is already open, force-cancel it before opening
      // a new one (don't leave two editors stacked).
      if (_lbActivePostitEditor && _lbActivePostitEditor._cancel) _lbActivePostitEditor._cancel();
      const editor = document.createElement("textarea");
      editor.className = "lb-postit-editor";
      const before = g.getAttribute("data-text") || "";
      editor.value = before;
      editor.style.left = tl.x + "px";
      editor.style.top = tl.y + "px";
      editor.style.width = Math.max(40, br.x - tl.x) + "px";
      editor.style.height = Math.max(28, br.y - tl.y) + "px";
      // Match the rendered note's on-screen metrics (font/line/padding scale
      // with the lightbox zoom) so the editor wraps exactly where the SVG
      // text will, and the size doesn't jump when editing ends.
      const scale = lbState.scale || 1;
      editor.style.boxSizing = "border-box";
      editor.style.fontSize = (POSTIT_FONT * scale) + "px";
      editor.style.lineHeight = (POSTIT_LH * scale) + "px";
      editor.style.padding = (POSTIT_PAD * scale) + "px";
      document.body.appendChild(editor);
      editor.focus();
      editor.select();
      _lbActivePostitEditor = editor;
      let done = false;
      const commit = (cancelled) => {
        if (done) return;
        done = true;
        const newText = cancelled ? null : editor.value;
        if (editor.parentNode) editor.parentNode.removeChild(editor);
        if (_lbActivePostitEditor === editor) _lbActivePostitEditor = null;
        if (newText !== null && newText !== before) {
          setPostitText(g, newText);
          recordAnnoAction({ type: "text", target: g, before, after: newText });
        }
      };
      editor._cancel = () => commit(true);
      editor.addEventListener("blur", () => commit(false));
      editor.addEventListener("keydown", (e) => {
        if (e.key === "Escape") { e.preventDefault(); commit(true); }
        else if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) { e.preventDefault(); commit(false); }
      });
    }

    // Walk an SVG and inline every meaningful computed style onto each
    // element. The browser's Image-loaded-from-blob pipeline applies
    // <style> blocks inside the SVG inconsistently — particularly for
    // mermaid output where strokes/fills are class-based — so the safest
    // way to bake a faithful PNG is to copy the rendered style values
    // directly to inline attributes. Source-clone is mutated; pass a
    // clone of the live element.
    const _SVG_STYLE_PROPS = [
      "fill","fill-opacity","fill-rule",
      "stroke","stroke-width","stroke-linecap","stroke-linejoin","stroke-opacity","stroke-dasharray","stroke-dashoffset","stroke-miterlimit",
      "opacity","visibility",
      "color",
      "font-family","font-size","font-weight","font-style","font-variant","letter-spacing","word-spacing",
      "text-anchor","dominant-baseline","alignment-baseline",
      "marker-start","marker-mid","marker-end",
    ];
    function inlineSvgStyles(liveRoot, cloneRoot) {
      const liveAll = liveRoot.querySelectorAll("*");
      const cloneAll = cloneRoot.querySelectorAll("*");
      const n = Math.min(liveAll.length, cloneAll.length);
      // Live root + clone root themselves too.
      const pairs = [[liveRoot, cloneRoot]];
      for (let i = 0; i < n; i++) pairs.push([liveAll[i], cloneAll[i]]);
      for (const [live, target] of pairs) {
        try {
          const cs = getComputedStyle(live);
          let extra = "";
          for (const prop of _SVG_STYLE_PROPS) {
            const v = cs.getPropertyValue(prop);
            if (!v || v === "normal" || v === "auto") continue;
            extra += prop + ":" + v + ";";
          }
          if (extra) {
            const existing = target.getAttribute("style") || "";
            target.setAttribute("style", extra + existing);
          }
        } catch (e) { /* skip */ }
      }
    }

    function renderLightboxToBlob(onBlob) {
      // Shared SVG→PNG-blob pipeline used by both Save (download) and
      // Copy (clipboard). Callback receives (blob, mime).
      const child = lightboxStageEl.firstElementChild;
      if (!child) { onBlob(null); return; }
      if (child.tagName === "IMG") {
        // Just hand back the raw image — fetch it from cache as a blob.
        fetch(child.src).then((r) => r.blob()).then((b) => onBlob(b, b.type || "image/png"))
          .catch(() => onBlob(null));
        return;
      }
      const svg = child.tagName && child.tagName.toLowerCase() === "svg" ? child : child.querySelector("svg");
      if (!svg) { onBlob(null); return; }
      // Snapshot via XMLSerializer + canvas: clone first so we don't mutate
      // the live SVG, then ensure xmlns is set (some Mermaid output omits it).
      const clone = svg.cloneNode(true);
      if (!clone.getAttribute("xmlns")) clone.setAttribute("xmlns", "http://www.w3.org/2000/svg");
      if (!clone.getAttribute("xmlns:xlink")) clone.setAttribute("xmlns:xlink", "http://www.w3.org/1999/xlink");
      try { inlineSvgStyles(svg, clone); } catch (e) {}
      // Strip post-it manipulation handles from the export — users want the
      // finished note, not the resize grip and close button.
      clone.querySelectorAll(".lb-postit-resize, .lb-postit-delete").forEach((el) => el.remove());
      const vb = svg.viewBox && svg.viewBox.baseVal;
      const rect = svg.getBoundingClientRect();
      const baseW = (vb && vb.width) || svg.clientWidth || rect.width || 1024;
      const baseH = (vb && vb.height) || svg.clientHeight || rect.height || 768;
      let minX = (vb && vb.x) || 0;
      let minY = (vb && vb.y) || 0;
      let maxX = minX + baseW;
      let maxY = minY + baseH;
      // Strokes are allowed to extend past the original viewBox (overflow
      // is set to visible in openLightbox). Expand the export viewBox to
      // cover every annotation so nothing gets cropped in the PNG.
      // Use getBoundingClientRect + screen→user-space so groups with
      // transforms (post-its) are measured in the right coordinate space.
      const liveMarks = svg.querySelectorAll(".lb-annotation");
      for (const mark of liveMarks) {
        let r = null;
        try { r = mark.getBoundingClientRect(); } catch (e) {}
        if (!r || (!r.width && !r.height)) continue;
        const tl = screenToSVGUserSpace(svg, r.left, r.top);
        const br2 = screenToSVGUserSpace(svg, r.right, r.bottom);
        const x1 = Math.min(tl.x, br2.x), y1 = Math.min(tl.y, br2.y);
        const x2 = Math.max(tl.x, br2.x), y2 = Math.max(tl.y, br2.y);
        if (x1 < minX) minX = x1;
        if (y1 < minY) minY = y1;
        if (x2 > maxX) maxX = x2;
        if (y2 > maxY) maxY = y2;
      }
      // Small margin so strokes that just kiss the edge aren't shaved.
      const margin = Math.max(8, Math.round(Math.min(baseW, baseH) * 0.01));
      minX -= margin; minY -= margin; maxX += margin; maxY += margin;
      const w = maxX - minX;
      const h = maxY - minY;
      clone.setAttribute("viewBox", minX + " " + minY + " " + w + " " + h);
      clone.setAttribute("width", String(w));
      clone.setAttribute("height", String(h));
      const xml = new XMLSerializer().serializeToString(clone);
      // Use a base64 data URL — handles non-ASCII content (Korean text
      // in labels) reliably and avoids blob: URL quirks some browsers
      // hit on large SVGs.
      let dataUrl;
      try {
        dataUrl = "data:image/svg+xml;base64," + btoa(unescape(encodeURIComponent(xml)));
      } catch (e) {
        showToast(t("errSvgEncode") + ": " + (e && e.message || e), { kind: "err", icon: "⚠️" });
        onBlob(null);
        return;
      }
      const img = new Image();
      img.onload = () => {
        const scale = Math.max(2, window.devicePixelRatio || 1);
        // Cap canvas pixel dimensions to stay under browser limits.
        const maxDim = 16384;
        let cw = Math.ceil(w * scale);
        let ch = Math.ceil(h * scale);
        if (cw > maxDim || ch > maxDim) {
          const k = Math.min(maxDim / cw, maxDim / ch);
          cw = Math.ceil(cw * k);
          ch = Math.ceil(ch * k);
        }
        const canvas = document.createElement("canvas");
        canvas.width = cw;
        canvas.height = ch;
        const ctx = canvas.getContext("2d");
        ctx.fillStyle = "#ffffff";
        ctx.fillRect(0, 0, canvas.width, canvas.height);
        try {
          ctx.drawImage(img, 0, 0, canvas.width, canvas.height);
        } catch (e) {
          showToast(t("errCanvasDraw") + ": " + (e && e.message || e), { kind: "err", icon: "⚠️" });
          onBlob(null);
          return;
        }
        canvas.toBlob((b) => {
          if (!b) {
            showToast(t("errPngEncode"), { kind: "err", icon: "⚠️" });
          }
          onBlob(b, "image/png");
        }, "image/png");
      };
      img.onerror = (e) => {
        showToast(t("errSvgImgLoad") + " (data URL " + Math.round(dataUrl.length / 1024) + "KB)", { kind: "err", icon: "⚠️" });
        onBlob(null);
      };
      img.src = dataUrl;
    }

    function saveLightboxImage() {
      const child = lightboxStageEl.firstElementChild;
      if (!child) {
        showToast(t("toastNoSaveContent"), { kind: "err", icon: "⚠️" });
        return;
      }
      const stamp = new Date().toISOString().replace(/[:.]/g, "-");
      // For raw <img> content download the source bytes directly — no
      // canvas round-trip needed (and avoids the IMG → fetch → blob
      // hop that was silently failing in the previous refactor).
      if (child.tagName === "IMG") {
        const a = document.createElement("a");
        a.href = child.src;
        a.download = "image-" + stamp + ".png";
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        showToast(t("toastImgSaved"), { kind: "ok", icon: "💾" });
        return;
      }
      renderLightboxToBlob((blob) => {
        if (!blob) { showToast(t("toastNoSaveImg"), { kind: "err", icon: "⚠️" }); return; }
        const a = document.createElement("a");
        a.href = URL.createObjectURL(blob);
        a.download = "diagram-" + stamp + ".png";
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        setTimeout(() => URL.revokeObjectURL(a.href), 2000);
        showToast(t("toastPngSaved") + " (" + a.download + ")", { kind: "ok", icon: "💾" });
      });
    }

    async function copyLightboxImage() {
      if (!navigator.clipboard || typeof window.ClipboardItem !== "function") {
        showToast(t("toastNoImgClip"), { kind: "err", icon: "⚠️" });
        return;
      }
      const child = lightboxStageEl.firstElementChild;
      if (!child) {
        showToast(t("toastNoCopyContent"), { kind: "err", icon: "⚠️" });
        return;
      }
      // Build the blob lazily inside a Promise. ClipboardItem accepts
      // Promise<Blob> values, which lets navigator.clipboard.write
      // fire SYNCHRONOUSLY inside the user gesture even though the
      // actual PNG rendering is async — Chrome/Safari otherwise reject
      // the write because the gesture context is "lost" by the time
      // canvas.toBlob resolves.
      const blobPromise = new Promise((resolve, reject) => {
        if (child.tagName === "IMG") {
          // For non-SVG images we still need a PNG blob (ClipboardItem
          // only takes image/png reliably). Round through a canvas.
          const tmp = new Image();
          tmp.crossOrigin = "anonymous";
          tmp.onload = () => {
            const canvas = document.createElement("canvas");
            canvas.width = tmp.naturalWidth || tmp.width || 800;
            canvas.height = tmp.naturalHeight || tmp.height || 600;
            const ctx = canvas.getContext("2d");
            ctx.drawImage(tmp, 0, 0);
            canvas.toBlob((b) => b ? resolve(b) : reject(new Error(t("errPngConvert"))), "image/png");
          };
          tmp.onerror = () => reject(new Error(t("errImgLoad")));
          tmp.src = child.src;
        } else {
          renderLightboxToBlob((b) => b ? resolve(b) : reject(new Error(t("errRender"))));
        }
      });
      try {
        const item = new ClipboardItem({ "image/png": blobPromise });
        await navigator.clipboard.write([item]);
        showToast(t("toastImgClipCopied"), { kind: "ok", icon: "📋" });
      } catch (e) {
        showToast(t("toastClipFail") + ": " + (e && e.message || e), { kind: "err", icon: "⚠️" });
      }
    }

    function openLightbox(node) {
      lightboxStageEl.innerHTML = "";
      _lbAnnoActive = null;
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
        // SVG default is overflow:hidden — annotation polylines drawn
        // past the diagram edge would be clipped. Allow them to extend
        // freely beyond the diagram into the surrounding lightbox area.
        svg.style.overflow = "visible";
        svg.setAttribute("overflow", "visible");
      }
      // Defer measurement one frame so getBoundingClientRect on inner
      // shapes returns real values (the SVG was just inserted).
      requestAnimationFrame(() => {
        captureLightboxNatural();
        fitLightboxContent();
      });
    }

    function hasAnnotations() {
      const svg = getAnnotationSVG();
      if (!svg) return false;
      return svg.querySelector(".lb-annotation") !== null;
    }

    function closeLightbox() {
      // Mid-stroke close: just drop it, no confirm — the user is clearly
      // already trying to close.
      _lbAnnoActive = null;
      if (hasAnnotations()) {
        const ok = window.confirm(t("lbCloseConfirm"));
        if (ok) {
          // Bake to PNG, then close once the download is triggered.
          saveLightboxImage();
          // Defer close so the synthesized <a>.click() runs before the
          // stage is wiped.
          setTimeout(closeLightboxNow, 60);
          return;
        }
      }
      closeLightboxNow();
    }

    function closeLightboxNow() {
      _lbAnnoActive = null;
      if (_lbActivePostitEditor && _lbActivePostitEditor._cancel) _lbActivePostitEditor._cancel();
      _lbActivePostitEditor = null;
      _lbDrawMode = false;
      _lbEraserMode = false;
      _lbPostitMode = false;
      _lbHistory = [];
      _lbRedoStack = [];
      const drawBtn = document.getElementById("lbAnnoDrawBtn");
      if (drawBtn) drawBtn.classList.remove("active");
      const eraseBtn = document.getElementById("lbAnnoEraseBtn");
      if (eraseBtn) eraseBtn.classList.remove("active");
      const postitBtn = document.getElementById("lbAnnoPostitBtn");
      if (postitBtn) postitBtn.classList.remove("active");
      lightboxEl.classList.remove("draw-mode");
      lightboxEl.classList.remove("eraser-mode");
      lightboxEl.classList.remove("postit-mode");
      setDrawControlsVisible(false);
      lightboxEl.hidden = true;
      lightboxStageEl.innerHTML = "";
      _lbBoxSel = null;
      clearCopiedHighlights();
      const lbSelBox = document.getElementById("lbSelectBox");
      if (lbSelBox) lbSelBox.hidden = true;
      document.body.classList.remove("lightbox-open");
      // Reset the cached baseline so the NEXT opened diagram re-measures
      // from scratch (different content size).
      _lbBaselineW = 0;
      _lbBaselineH = 0;
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

    // selectstart guard — without Alt held, the lightbox is pan-only; we
    // must beat the browser's default drag-to-select on SVG text so the
    // mouse drag pans the diagram instead of highlighting glyphs.
    // Native text selection is disabled everywhere in the lightbox — ⌥+Drag
    // does a box selection (copyable) instead, so we never want the browser's
    // glyph highlighting to kick in here.
    lightboxEl.addEventListener("selectstart", (event) => {
      if (event.target && event.target.closest && event.target.closest(".lightbox-toolbar")) return;
      event.preventDefault();
    });

    lightboxEl.addEventListener("pointerdown", (event) => {
      if (event.target.closest(".lightbox-toolbar")) return;
      // Alt/Option held → rubber-band box selection over the diagram. The
      // text fully enclosed by the box is copied to the clipboard on release
      // (replaces fiddly precise text selection).
      if (event.altKey || state.altKey) {
        if (event.button !== 0) return;
        startLbBoxSelect(event);
        event.preventDefault();
        try { lightboxEl.setPointerCapture(event.pointerId); } catch (e) {}
        return;
      }
      if (_lbEraserMode && event.button === 0) {
        // Start an erase-drag session. Strokes touched here AND during
        // the subsequent pointermove are all bundled into one undo
        // action, so a single ↶ restores everything wiped in the drag.
        _lbEraseDrag = { erased: [] };
        const stroke = event.target.closest && event.target.closest(".lb-annotation");
        if (stroke) {
          stroke.remove();
          _lbEraseDrag.erased.push(stroke);
        }
        try { lightboxEl.setPointerCapture(event.pointerId); } catch (e) {}
        event.preventDefault();
        event.stopPropagation();
        return;
      }
      if (_lbPostitMode && event.button === 0) {
        const delHandle = event.target.closest && event.target.closest(".lb-postit-delete");
        if (delHandle) {
          const g = delHandle.closest(".lb-postit");
          event.preventDefault();
          event.stopPropagation();
          // Force-close any open editor RIGHT NOW so the deletion lands in
          // a single frame. Without this the textarea's natural blur runs
          // first (committing text + removing the editor), and the user
          // sees the post-it disappear one beat later.
          if (_lbActivePostitEditor && _lbActivePostitEditor._cancel) {
            _lbActivePostitEditor._cancel();
          }
          g.remove();
          recordAnnoAction({ type: "remove", strokes: [g] });
          return;
        }
        const resizeHandle = event.target.closest && event.target.closest(".lb-postit-resize");
        if (resizeHandle) {
          const g = resizeHandle.closest(".lb-postit");
          event.preventDefault();
          event.stopPropagation();
          const svg = getAnnotationSVG();
          if (!svg) return;
          const p = screenToSVGUserSpace(svg, event.clientX, event.clientY);
          _lbPostitDrag = {
            g, kind: "resize",
            startX: p.x, startY: p.y,
            before: readPostitState(g),
            didMove: false,
          };
          try { lightboxEl.setPointerCapture(event.pointerId); } catch (e) {}
          return;
        }
        const existing = event.target.closest && event.target.closest(".lb-postit");
        if (existing) {
          event.preventDefault();
          event.stopPropagation();
          const svg = getAnnotationSVG();
          if (!svg) return;
          const p = screenToSVGUserSpace(svg, event.clientX, event.clientY);
          _lbPostitDrag = {
            g: existing, kind: "move",
            startX: p.x, startY: p.y,
            before: readPostitState(existing),
            didMove: false,
          };
          try { lightboxEl.setPointerCapture(event.pointerId); } catch (e) {}
          return;
        }
        // Empty-space clicks in post-it mode do nothing — to add another
        // note the user presses the 📝 toolbar button. This avoids stray
        // notes when panning or mis-clicking.
        return;
      }
      if (_lbDrawMode && event.button === 0) {
        event.preventDefault();
        startAnnotationStroke(event);
        return;
      }
      // NOTE: do NOT preventDefault on pointerdown — that suppresses the
      // synthesized mousedown→click→dblclick chain on some browsers and
      // breaks the double-click-to-reset gesture. Text selection is
      // already blocked by the selectstart handler above.
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
      if (_lbBoxSel) { updateLbBoxSelect(event); event.preventDefault(); return; }
      if (_lbAnnoActive) { continueAnnotationStroke(event); return; }
      if (_lbEraseDrag) {
        // Walk every element under the pointer (elementsFromPoint, not
        // elementFromPoint) so a stroke buried beneath a hovered overlay
        // can still be wiped.
        const els = document.elementsFromPoint ? document.elementsFromPoint(event.clientX, event.clientY) : [document.elementFromPoint(event.clientX, event.clientY)];
        for (const el of els) {
          if (!el) continue;
          const stroke = el.closest && el.closest(".lb-annotation");
          if (stroke && _lbEraseDrag.erased.indexOf(stroke) === -1) {
            stroke.remove();
            _lbEraseDrag.erased.push(stroke);
          }
        }
        event.preventDefault();
        return;
      }
      if (_lbPostitDrag) {
        const svg = getAnnotationSVG();
        if (!svg) return;
        const p = screenToSVGUserSpace(svg, event.clientX, event.clientY);
        const dx = p.x - _lbPostitDrag.startX;
        const dy = p.y - _lbPostitDrag.startY;
        if (!_lbPostitDrag.didMove && Math.hypot(dx, dy) > 2) _lbPostitDrag.didMove = true;
        if (_lbPostitDrag.kind === "move") {
          const nx = _lbPostitDrag.before.tx + dx;
          const ny = _lbPostitDrag.before.ty + dy;
          _lbPostitDrag.g.setAttribute("transform", "translate(" + nx + "," + ny + ")");
        } else if (_lbPostitDrag.kind === "resize") {
          const nw = Math.max(60, _lbPostitDrag.before.w + dx);
          const nh = Math.max(36, _lbPostitDrag.before.h + dy);
          setPostitSize(_lbPostitDrag.g, nw, nh);
          _lbPostitDrag.g.dataset.manualSize = "1";
        }
        event.preventDefault();
        return;
      }
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

    // _lastLbTap is the timestamp of the previous non-drag pointerup. We
    // synthesize our own double-tap detection because some browsers
    // (Safari with pointer capture, Chrome with selectstart blocked)
    // refuse to emit a dblclick event after the second quick click.
    var _lastLbTap = 0;
    lightboxEl.addEventListener("pointerup", (event) => {
      if (_lbBoxSel) { endLbBoxSelect(event); event.preventDefault(); return; }
      if (_lbAnnoActive) { endAnnotationStroke(event); return; }
      if (_lbEraseDrag) {
        const erased = _lbEraseDrag.erased;
        _lbEraseDrag = null;
        try { lightboxEl.releasePointerCapture(event.pointerId); } catch (e) {}
        if (erased.length > 0) {
          recordAnnoAction({ type: "remove", strokes: erased });
        }
        return;
      }
      if (_lbPostitDrag) {
        const d = _lbPostitDrag;
        _lbPostitDrag = null;
        try { lightboxEl.releasePointerCapture(event.pointerId); } catch (e) {}
        if (d.didMove) {
          const after = readPostitState(d.g);
          recordAnnoAction({ type: "transform", target: d.g, before: d.before, after });
        } else {
          // Treat a click without movement as "open editor".
          openPostitEditor(d.g);
        }
        return;
      }
      const wasDrag = lbState.didDrag;
      lbState.dragging = false;
      lightboxEl.classList.remove("dragging");
      try { lightboxEl.releasePointerCapture(event.pointerId); } catch (e) {}
      // Only close via the ✕ button or the Esc key — clicking the
      // backdrop is reserved for pan/zoom interactions so the user
      // doesn't accidentally close the view mid-inspection.
      if (wasDrag) { _lastLbTap = 0; return; }
      if (event.altKey || state.altKey) { _lastLbTap = 0; return; }
      if (event.target && event.target.closest && event.target.closest(".lightbox-toolbar")) {
        _lastLbTap = 0;
        return;
      }
      const now = Date.now();
      if (now - _lastLbTap < 400) {
        _lastLbTap = 0;
        fitLightboxContent();
        return;
      }
      _lastLbTap = now;
    });

    lightboxStageEl.addEventListener("dblclick", (event) => {
      // Alt/Option held: this gesture belongs to the text-selection flow
      // (user is double-clicking a word in the diagram to select it).
      // Skip the reset and let the browser handle the word selection.
      if (event.altKey || state.altKey) return;
      event.preventDefault();
      fitLightboxContent();
    });

    const handleToolbarClick = (event) => {
      const btn = event.target.closest("button[data-action]");
      if (!btn) return;
      const action = btn.dataset.action;
      const cx = window.innerWidth / 2;
      const cy = window.innerHeight / 2;
      if (action === "close") closeLightbox();
      else if (action === "zoom-in") lightboxZoomAt(cx, cy, 1.25);
      else if (action === "zoom-out") lightboxZoomAt(cx, cy, 1 / 1.25);
      else if (action === "reset") fitLightboxContent();
      else if (action === "annodraw") toggleDrawMode();
      else if (action === "announdo") undoAnnotation();
      else if (action === "annoredo") redoAnnotation();
      else if (action === "annoerase") toggleEraserMode();
      else if (action === "annopostit") addNewPostit();
      else if (action === "annoclear") clearAnnotations();
      else if (action === "annosave") saveLightboxImage();
      else if (action === "annocopy") copyLightboxImage();
    };
    for (const tb of lightboxToolbarEls) tb.addEventListener("click", handleToolbarClick);

    const lbAnnoColorInput = document.getElementById("lbAnnoColor");
    if (lbAnnoColorInput) {
      lbAnnoColorInput.addEventListener("input", (event) => {
        _lbDrawColor = event.target.value || "#ff3b30";
      });
    }
    const lbAnnoOpacityInput = document.getElementById("lbAnnoOpacity");
    if (lbAnnoOpacityInput) {
      lbAnnoOpacityInput.addEventListener("input", (event) => {
        const v = parseFloat(event.target.value);
        _lbDrawOpacity = isNaN(v) ? 0.5 : v;
      });
    }

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

    // ----- Lightbox: ⌥+Drag box selection → copy enclosed text -----
    // Holding Alt/Option and dragging draws a rubber-band rectangle over the
    // diagram. On release, every <text>/<foreignObject> whose box lies fully
    // inside the rectangle is collected (grouped into lines) and copied to the
    // clipboard. This replaces precise glyph selection, which is fiddly on SVG.
    var _lbBoxSel = null;
    function startLbBoxSelect(event) {
      clearCopiedHighlights();
      let box = document.getElementById("lbSelectBox");
      if (!box) {
        box = document.createElement("div");
        box.className = "lb-select-box";
        box.id = "lbSelectBox";
        document.body.appendChild(box);
      }
      _lbBoxSel = { sx: event.clientX, sy: event.clientY, el: box };
      box.style.left = event.clientX + "px";
      box.style.top = event.clientY + "px";
      box.style.width = "0px";
      box.style.height = "0px";
      box.hidden = false;
    }
    function updateLbBoxSelect(event) {
      if (!_lbBoxSel) return;
      const b = _lbBoxSel.el;
      b.style.left = Math.min(_lbBoxSel.sx, event.clientX) + "px";
      b.style.top = Math.min(_lbBoxSel.sy, event.clientY) + "px";
      b.style.width = Math.abs(event.clientX - _lbBoxSel.sx) + "px";
      b.style.height = Math.abs(event.clientY - _lbBoxSel.sy) + "px";
    }
    async function endLbBoxSelect(event) {
      const sel = _lbBoxSel;
      _lbBoxSel = null;
      if (!sel) return;
      if (sel.el) sel.el.hidden = true;
      try { lightboxEl.releasePointerCapture(event.pointerId); } catch (e) {}
      const rect = {
        left: Math.min(sel.sx, event.clientX),
        top: Math.min(sel.sy, event.clientY),
        right: Math.max(sel.sx, event.clientX),
        bottom: Math.max(sel.sy, event.clientY),
      };
      // Ignore an accidental click / micro-drag.
      if ((rect.right - rect.left) < 5 || (rect.bottom - rect.top) < 5) return;
      const svg = lightboxStageEl.querySelector("svg");
      if (!svg) return;
      const res = extractMermaidTextInBox(svg, rect);
      if (!res.text) { showToast(t("toastBoxEmpty"), { kind: "err", icon: "⚠️" }); return; }
      // Show what was captured (selection-style highlight) before copying.
      showCopiedHighlights(res.rects);
      const ok = await copyTextToClipboard(res.text);
      showToast(ok ? t("toastBoxCopied") : t("toastCopyFail"),
        ok ? { kind: "ok", icon: "📋" } : { kind: "err", icon: "⚠️" });
    }
    // Collect text whose bounding box is fully contained in the screen-space
    // rect, grouped into lines by y (same approach as extractMermaidText).
    function extractMermaidTextInBox(svg, rect) {
      const nodes = svg.querySelectorAll("text, foreignObject");
      const items = [];
      for (const node of nodes) {
        if (node.tagName === "text" && node.closest("foreignObject")) continue;
        let r;
        try { r = node.getBoundingClientRect(); } catch (e) { continue; }
        const text = (node.textContent || "").replace(/\s+/g, " ").trim();
        if (!text) continue;
        if (r.left >= rect.left && r.right <= rect.right && r.top >= rect.top && r.bottom <= rect.bottom) {
          items.push({ y: r.top, x: r.left, text, rect: { left: r.left, top: r.top, width: r.width, height: r.height } });
        }
      }
      if (!items.length) return { text: "", rects: [] };
      const rects = items.map((it) => it.rect);
      items.sort((a, b) => a.y - b.y || a.x - b.x);
      const lines = [];
      let current = [];
      let lastY = -Infinity;
      const lineTolerance = 6;
      for (const it of items) {
        if (Math.abs(it.y - lastY) > lineTolerance && current.length) {
          lines.push(current);
          current = [];
        }
        current.push(it);
        lastY = it.y;
      }
      if (current.length) lines.push(current);
      const text = lines
        .map((line) => line.sort((a, b) => a.x - b.x).map((it) => it.text).join("  "))
        .join("\n");
      return { text: text, rects: rects };
    }

    // Briefly paint a selection-style highlight over the text that was just
    // copied, so the user can see exactly what landed on the clipboard.
    var _lbCopyHls = [];
    function clearCopiedHighlights() {
      for (const el of _lbCopyHls) { try { el.remove(); } catch (e) {} }
      _lbCopyHls = [];
    }
    function showCopiedHighlights(rects) {
      clearCopiedHighlights();
      for (const r of rects) {
        const hl = document.createElement("div");
        hl.className = "lb-copy-hl";
        hl.style.left = r.left + "px";
        hl.style.top = r.top + "px";
        hl.style.width = r.width + "px";
        hl.style.height = r.height + "px";
        document.body.appendChild(hl);
        _lbCopyHls.push(hl);
      }
      const mine = _lbCopyHls.slice();
      setTimeout(function () { for (const el of mine) el.classList.add("fade"); }, 900);
      setTimeout(function () {
        for (const el of mine) { try { el.remove(); } catch (e) {} }
        _lbCopyHls = _lbCopyHls.filter((el) => mine.indexOf(el) === -1);
      }, 1400);
    }

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
        showToast(t("toastNoText"), { kind: "err", icon: "⚠️" });
        return;
      }
      try {
        await navigator.clipboard.writeText(text);
        if (btn) flashButton(btn, "Copied ✓", true);
        showToast(t("toastMermaidCopied"), { kind: "ok", icon: "📋" });
      } catch (err) {
        console.error("copy failed:", err);
        if (btn) flashButton(btn, "Copy failed", false);
        showToast(t("toastClipFail") + ": " + (err && err.message || err), { kind: "err", icon: "⚠️" });
      }
    }

    async function copyMermaidImage(el) {
      const svg = el.querySelector("svg");
      if (!svg) { showToast(t("toastNoDiagram"), { kind: "err", icon: "⚠️" }); return; }
      if (!navigator.clipboard || typeof window.ClipboardItem !== "function") {
        showToast(t("toastNoImgClip"), { kind: "err", icon: "⚠️" });
        return;
      }
      // Same lazy-Promise pattern as copyLightboxImage so the
      // clipboard.write fires inside the user gesture even though the
      // SVG-to-PNG rasterization is async.
      const blobPromise = new Promise((resolve, reject) => {
        const clone = svg.cloneNode(true);
        if (!clone.getAttribute("xmlns")) clone.setAttribute("xmlns", "http://www.w3.org/2000/svg");
        if (!clone.getAttribute("xmlns:xlink")) clone.setAttribute("xmlns:xlink", "http://www.w3.org/1999/xlink");
        try { inlineSvgStyles(svg, clone); } catch (e) {}
        const vb = svg.viewBox && svg.viewBox.baseVal;
        const rect = svg.getBoundingClientRect();
        const w = (vb && vb.width) || svg.clientWidth || rect.width || 800;
        const h = (vb && vb.height) || svg.clientHeight || rect.height || 600;
        clone.setAttribute("width", String(w));
        clone.setAttribute("height", String(h));
        const xml = new XMLSerializer().serializeToString(clone);
        let dataUrl;
        try {
          dataUrl = "data:image/svg+xml;base64," + btoa(unescape(encodeURIComponent(xml)));
        } catch (e) {
          reject(new Error(t("errSvgEncode"))); return;
        }
        const img = new Image();
        img.onload = () => {
          const scale = Math.max(2, window.devicePixelRatio || 1);
          const maxDim = 16384;
          let cw = Math.ceil(w * scale);
          let ch = Math.ceil(h * scale);
          if (cw > maxDim || ch > maxDim) {
            const k = Math.min(maxDim / cw, maxDim / ch);
            cw = Math.ceil(cw * k);
            ch = Math.ceil(ch * k);
          }
          const canvas = document.createElement("canvas");
          canvas.width = cw;
          canvas.height = ch;
          const ctx = canvas.getContext("2d");
          ctx.fillStyle = "#ffffff";
          ctx.fillRect(0, 0, canvas.width, canvas.height);
          try { ctx.drawImage(img, 0, 0, canvas.width, canvas.height); }
          catch (e) { reject(new Error(t("errCanvasDraw") + ": " + (e && e.message || e))); return; }
          canvas.toBlob((b) => {
            b ? resolve(b) : reject(new Error(t("errPngEncode")));
          }, "image/png");
        };
        img.onerror = () => { reject(new Error(t("errSvgImgLoad") + " (" + Math.round(dataUrl.length / 1024) + "KB)")); };
        img.src = dataUrl;
      });
      try {
        const item = new ClipboardItem({ "image/png": blobPromise });
        await navigator.clipboard.write([item]);
        showToast(t("toastDiagramClipCopied"), { kind: "ok", icon: "📋" });
      } catch (e) {
        showToast(t("toastClipFail") + ": " + (e && e.message || e), { kind: "err", icon: "⚠️" });
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

      const copyImgBtn = document.createElement("button");
      copyImgBtn.type = "button";
      copyImgBtn.className = "mermaid-tool-btn";
      copyImgBtn.textContent = "Copy image";
      copyImgBtn.title = "Copy this diagram as a PNG image to the clipboard";
      copyImgBtn.addEventListener("pointerdown", stop);
      copyImgBtn.addEventListener("pointerup", stop);
      copyImgBtn.addEventListener("click", (event) => {
        event.stopPropagation();
        event.preventDefault();
        copyMermaidImage(el);
      });
      bar.appendChild(copyImgBtn);

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

    function attachZoomToPreview(root) {
      const scope = root || previewBodyEl;
      const targets = scope.querySelectorAll("img, .mermaid");
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
    refreshGitRemote();
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
    // AI-DLC mode shows the aidlc-docs listing, which the 2.5s dir poll
    // deliberately skips (to avoid hammering git every tick). Re-poll it on a
    // gentler cadence while the mode is on, so edits to those files surface
    // (mod_time / order) without a manual refresh. Keeps the list on a
    // transient failure (silent).
    setInterval(() => { if (state.aidlcMode && state.aidlc && state.aidlc.available) refreshAidlc(true); }, 5000);
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

// searchMaxGitFiles bounds how many files a scope=git search will scan, so a
// huge repository can't hang the request.
const searchMaxGitFiles = 4000

// gitRoot resolves the enclosing git repository root for dir, or "" if dir is
// not inside a repo (or git is unavailable).
func (s *webServer) gitRoot(ctx context.Context, dir string) string {
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(c, "git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// handleGitRoot reports whether dir is inside a git repo and, if so, its root.
// The UI uses this to enable/disable the "Git" search scope.
func (s *webServer) handleGitRoot(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("dir")
	if dir == "" {
		dir = s.startDir
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"root": "", "isRepo": false})
		return
	}
	root := s.gitRoot(r.Context(), abs)
	s.writeJSON(w, http.StatusOK, map[string]any{"root": root, "isRepo": root != ""})
}

// isHexRev reports whether s looks like a git object id (short or full). We feed
// only ids we ourselves produced via `git log`, but validate as defence in depth
// before interpolating into a `git show <rev>:<path>` argument.
func isHexRev(s string) bool {
	if len(s) < 4 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// handleGitFileLog lists the commit history for a single file (most recent
// first), so the UI can offer per-version before/after comparison. Reports
// availability=false when the file is not inside a git repository.
func (s *webServer) handleGitFileLog(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		s.writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	root := s.gitRoot(r.Context(), filepath.Dir(abs))
	if root == "" {
		s.writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	rel = filepath.ToSlash(rel)
	const sep = "\x1f"
	format := "%H" + sep + "%h" + sep + "%ad" + sep + "%an" + sep + "%s"
	out, lerr := gitOutput(r.Context(), root, "log", "--follow",
		"--date=format:%Y-%m-%d %H:%M", "--format="+format, "--", rel)
	commits := []map[string]any{}
	if lerr == nil && out != "" {
		for _, line := range strings.Split(out, "\n") {
			parts := strings.SplitN(line, sep, 5)
			if len(parts) < 5 {
				continue
			}
			commits = append(commits, map[string]any{
				"hash": parts[0], "short": parts[1],
				"date": parts[2], "author": parts[3], "subject": parts[4],
			})
		}
	}
	status, _ := gitOutput(r.Context(), root, "status", "--porcelain", "--", rel)
	s.writeJSON(w, http.StatusOK, map[string]any{
		"available": true, "root": root, "relpath": rel,
		"commits": commits, "dirty": strings.TrimSpace(status) != "",
	})
}

// handleGitShow returns the contents of a file at a given revision. A blank or
// "WORKING" rev returns the current on-disk copy.
func (s *webServer) handleGitShow(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	rev := r.URL.Query().Get("rev")
	if p == "" {
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "missing path"})
		return
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "bad path"})
		return
	}
	if rev == "" || rev == "WORKING" {
		b, rerr := os.ReadFile(abs)
		if rerr != nil {
			s.writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": rerr.Error()})
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "content": string(b)})
		return
	}
	if !isHexRev(rev) {
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "bad rev"})
		return
	}
	root := s.gitRoot(r.Context(), filepath.Dir(abs))
	if root == "" {
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "not a repo"})
		return
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "rel"})
		return
	}
	rel = filepath.ToSlash(rel)
	// Raw stdout — do NOT trim. Trimming would drop the committed file's
	// trailing newline, which makes a clean working copy look "changed"
	// against HEAD and breaks the diff (only an empty trailing line differs).
	raw, gerr := exec.CommandContext(r.Context(), "git", "-C", root, "show", rev+":"+rel).Output()
	if gerr != nil {
		msg := gerr.Error()
		if ee, ok := gerr.(*exec.ExitError); ok {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": msg})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "content": string(raw)})
}

// handleAidlc powers the "AI-DLC" sidebar mode. The mode is available only when
// the enclosing git repo root contains an aidlc-docs folder; when it does, this
// lists every file beneath that folder, most-recently-modified first.
func (s *webServer) handleAidlc(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("dir")
	if dir == "" {
		dir = s.startDir
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	root := s.gitRoot(r.Context(), abs)
	if root == "" {
		s.writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	aidlcDir := filepath.Join(root, "aidlc-docs")
	if info, serr := os.Stat(aidlcDir); serr != nil || !info.IsDir() {
		s.writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}

	const maxFiles = 5000
	const maxDepth = 12
	files := []webEntry{}
	var walk func(d string, depth int)
	walk = func(d string, depth int) {
		if depth > maxDepth || len(files) >= maxFiles {
			return
		}
		entries, rerr := os.ReadDir(d)
		if rerr != nil {
			return
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			full := filepath.Join(d, entry.Name())
			if entry.IsDir() {
				walk(full, depth+1)
				continue
			}
			var size int64
			var modTime string
			if fi, ierr := entry.Info(); ierr == nil {
				size = fi.Size()
				modTime = fi.ModTime().Format(time.RFC3339)
			}
			files = append(files, webEntry{
				Name:    entry.Name(),
				Path:    full,
				IsDir:   false,
				Size:    size,
				ModTime: modTime,
			})
			if len(files) >= maxFiles {
				return
			}
		}
	}
	walk(aidlcDir, 0)
	// Default ordering: most recently updated first. RFC3339 timestamps from the
	// same machine share an offset, so a string compare orders them correctly.
	sort.Slice(files, func(i, j int) bool {
		return files[i].ModTime > files[j].ModTime
	})
	s.writeJSON(w, http.StatusOK, map[string]any{
		"available": true,
		"root":      root,
		"dir":       aidlcDir,
		"files":     files,
	})
}

// ── Self-update (mdviewer app) ──────────────────────────────────────────
// The running binary lives inside its own git checkout, so it can pull the
// latest source, rebuild itself, and re-exec the new binary.

// goModModule returns the module path declared in dir/go.mod, or "".
func goModModule(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(line[len("module "):])
		}
	}
	return ""
}

// appRepoRoot locates the mdviewer source checkout (for version display), the
// resolved executable path, and whether the app can self-update.
//
// canUpdate is true only when running a real binary that lives inside the
// checkout — `go run` compiles a throwaway binary into a temp dir, so it can
// still show the version (resolved via the working directory) but can't
// rebuild-and-re-exec in place.
// appRepoRoot locates the mdviewer source checkout (for version + update), the
// resolved executable path, and whether the app can self-update.
//
// The checkout is resolved from, in order: the binary's own directory (plain
// build), the embedded buildRepo (the installed .app, whose binary lives in
// ~/Applications), then the working directory (`go run`). Self-update needs the
// checkout AND a replaceable on-disk binary — true for both a plain build and
// the installed .app, but not for `go run` (its binary is a temp build).
func (s *webServer) appRepoRoot(ctx context.Context) (repo, exe string, canUpdate bool) {
	exe, _ = os.Executable()
	if resolved, err := filepath.EvalSymlinks(exe); err == nil && resolved != "" {
		exe = resolved
	}
	exeDir := filepath.Dir(exe)
	inTemp := strings.Contains(exe, string(os.PathSeparator)+"go-build") ||
		(os.TempDir() != "" && strings.HasPrefix(exeDir, os.TempDir()))

	candidates := make([]string, 0, 3)
	if exe != "" && !inTemp {
		candidates = append(candidates, exeDir)
	}
	if buildRepo != "" {
		candidates = append(candidates, buildRepo)
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, wd)
	}
	for _, c := range candidates {
		if r := s.gitRoot(ctx, c); r != "" && goModModule(r) == "mdviewer" {
			repo = r
			break
		}
	}
	// Updatable when we found the checkout and the binary is a real file we can
	// rebuild over (a `go run` temp binary is not).
	canUpdate = repo != "" && exe != "" && !inTemp
	return repo, exe, canUpdate
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Version metadata optionally injected at build time via
//   -ldflags "-X main.buildCommit=… -X main.buildDate=… …"
// so a binary running outside its checkout (e.g. the installed .app launched
// by launchd) can still report its version. Empty in plain `go build`/`go run`,
// where we fall back to reading the surrounding git checkout at runtime.
var (
	buildCommit string
	buildDate   string
	buildBranch string
	buildRepo   string
)

func (s *webServer) handleVersion(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	repo, _, canUpdate := s.appRepoRoot(ctx)
	resp := map[string]any{"canUpdate": canUpdate}

	if buildCommit != "" {
		// Built-in version (authoritative, location-independent).
		subject := ""
		if buildRepo != "" {
			subject, _ = gitOutput(ctx, buildRepo, "log", "-1", "--format=%s", buildCommit)
		}
		resp["current"] = map[string]string{"hash": buildCommit, "subject": subject, "date": buildDate}
		resp["branch"] = buildBranch
		resp["devMode"] = false
	} else if repo != "" {
		hash, _ := gitOutput(ctx, repo, "rev-parse", "--short", "HEAD")
		subject, _ := gitOutput(ctx, repo, "log", "-1", "--format=%s")
		date, _ := gitOutput(ctx, repo, "log", "-1", "--format=%cI")
		branch, _ := gitOutput(ctx, repo, "rev-parse", "--abbrev-ref", "HEAD")
		resp["current"] = map[string]string{"hash": hash, "subject": subject, "date": date}
		resp["branch"] = branch
		resp["devMode"] = false
	} else {
		resp["devMode"] = true
	}
	// Recent commit history (for the hover tooltip): last few commits with
	// their dates and subjects.
	if log := recentLog(ctx, repo, 8); len(log) > 0 {
		resp["log"] = log
	}
	// Origin remote → browser URL, for the repo link.
	if repo != "" {
		if u, err := gitOutput(ctx, repo, "remote", "get-url", "origin"); err == nil {
			if web := gitToWebURL(u); web != "" {
				resp["repoURL"] = web
			}
		}
	}
	s.writeJSON(w, http.StatusOK, resp)
}

// upstreamRef returns the branch's tracking ref (e.g. origin/main), or "" .
func upstreamRef(ctx context.Context, repo string) string {
	if u, err := gitOutput(ctx, repo, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"); err == nil && u != "" {
		return u
	}
	return ""
}

func (s *webServer) handleVersionCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	repo, _, _ := s.appRepoRoot(ctx)
	if repo == "" {
		s.writeJSON(w, http.StatusOK, map[string]any{"behind": 0, "devMode": true})
		return
	}
	if _, err := gitOutput(ctx, repo, "fetch", "--quiet"); err != nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"behind": 0, "error": "fetch failed (offline?)"})
		return
	}
	up := upstreamRef(ctx, repo)
	if up == "" {
		up = "origin/main"
	}
	behindStr, err := gitOutput(ctx, repo, "rev-list", "--count", "HEAD.."+up)
	if err != nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"behind": 0, "error": "no upstream"})
		return
	}
	behind := 0
	fmt.Sscanf(behindStr, "%d", &behind)
	latestSubject, _ := gitOutput(ctx, repo, "log", "-1", "--format=%s", up)
	latestHash, _ := gitOutput(ctx, repo, "rev-parse", "--short", up)
	s.writeJSON(w, http.StatusOK, map[string]any{
		"behind":   behind,
		"upstream": up,
		"latest":   map[string]string{"hash": latestHash, "subject": latestSubject},
	})
}

// versionString returns a compact "branch hash" label for the running app,
// preferring the build-time embedded values (works for the installed .app).
func (s *webServer) versionString(ctx context.Context) string {
	if buildCommit != "" {
		return strings.TrimSpace(buildBranch + " " + buildCommit)
	}
	if repo, _, _ := s.appRepoRoot(ctx); repo != "" {
		h, _ := gitOutput(ctx, repo, "rev-parse", "--short", "HEAD")
		br, _ := gitOutput(ctx, repo, "rev-parse", "--abbrev-ref", "HEAD")
		return strings.TrimSpace(br + " " + h)
	}
	return "dev"
}

// recentLog returns the last n commits of repo as {hash,date,subject} maps.
func recentLog(ctx context.Context, repo string, n int) []map[string]string {
	if repo == "" {
		return nil
	}
	out, err := gitOutput(ctx, repo, "log", fmt.Sprintf("-n%d", n), "--format=%h%x1f%cs%x1f%s")
	if err != nil {
		return nil
	}
	var res []map[string]string
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "\x1f", 3)
		if len(parts) < 3 {
			continue
		}
		res = append(res, map[string]string{"hash": parts[0], "date": parts[1], "subject": parts[2]})
	}
	return res
}

// updateBehind fetches and reports how many commits the checkout is behind its
// upstream, plus the latest upstream subject.
func (s *webServer) updateBehind(ctx context.Context) (behind int, latest string) {
	repo, _, _ := s.appRepoRoot(ctx)
	if repo == "" {
		return 0, ""
	}
	if _, err := gitOutput(ctx, repo, "fetch", "--quiet"); err != nil {
		return 0, ""
	}
	up := upstreamRef(ctx, repo)
	if up == "" {
		up = "origin/main"
	}
	if b, err := gitOutput(ctx, repo, "rev-list", "--count", "HEAD.."+up); err == nil {
		fmt.Sscanf(b, "%d", &behind)
	}
	latest, _ = gitOutput(ctx, repo, "log", "-1", "--format=%s", up)
	return behind, latest
}

// selfUpdateBuild pulls (ff-only), rebuilds with version ldflags re-embedded,
// and swaps the running binary in place. On success it returns the executable
// path to re-exec; otherwise ok=false with a user-facing message. Shared by the
// /api/update handler and the menu-bar "Update" item.
func (s *webServer) selfUpdateBuild(ctx context.Context) (exe string, ok bool, message string) {
	repo, exe, canUpdate := s.appRepoRoot(ctx)
	if !canUpdate || repo == "" {
		return exe, false, "자동 업데이트는 빌드된 바이너리/설치 앱에서만 가능합니다 (go run 모드 불가)."
	}
	if out, err := gitOutput(ctx, repo, "pull", "--ff-only"); err != nil {
		return exe, false, "git pull 실패:\n" + out
	}
	nCommit, _ := gitOutput(ctx, repo, "rev-parse", "--short", "HEAD")
	nDate, _ := gitOutput(ctx, repo, "log", "-1", "--format=%cI")
	nBranch, _ := gitOutput(ctx, repo, "rev-parse", "--abbrev-ref", "HEAD")
	ldflags := fmt.Sprintf("-X main.buildCommit=%s -X main.buildDate=%s -X main.buildBranch=%s -X 'main.buildRepo=%s'",
		nCommit, nDate, nBranch, repo)
	tmp := exe + ".new"
	build := exec.CommandContext(ctx, "go", "build", "-ldflags", ldflags, "-o", tmp, ".")
	build.Dir = repo
	build.Env = append(os.Environ(), "CGO_ENABLED=1")
	if out, err := build.CombinedOutput(); err != nil {
		_ = os.Remove(tmp)
		return exe, false, "go build 실패:\n" + strings.TrimSpace(string(out))
	}
	_ = os.Chmod(tmp, 0o755)
	if err := os.Rename(tmp, exe); err != nil {
		_ = os.Remove(tmp)
		return exe, false, "바이너리 교체 실패: " + err.Error()
	}
	return exe, true, "업데이트 완료"
}

func (s *webServer) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	exe, ok, msg := s.selfUpdateBuild(ctx)
	if !ok {
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": msg})
		return
	}
	// Respond, then re-exec the freshly built binary (same args/port).
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "업데이트 완료 — 재시작합니다", "restarting": true})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go func() {
		time.Sleep(500 * time.Millisecond)
		_ = syscall.Exec(exe, os.Args, os.Environ())
	}()
}

// searchFileForNeedle returns a searchResult for full when it contains needle.
func searchFileForNeedle(full, needle string) (searchResult, bool) {
	info, err := os.Stat(full)
	if err != nil || info.Size() > searchMaxFileBytes {
		return searchResult{}, false
	}
	data, err := os.ReadFile(full)
	if err != nil || !isProbablyText(data) {
		return searchResult{}, false
	}
	lower := strings.ToLower(string(data))
	count := strings.Count(lower, needle)
	if count == 0 {
		return searchResult{}, false
	}
	return searchResult{Path: full, Count: count, Snippets: collectSnippets(string(data), lower, needle, searchMaxSnippets)}, true
}

// searchDirShallow searches only the immediate files of dir (the "Same folder"
// scope). searchTreeRecursive walks the whole tree under root (the "Git repo"
// scope), skipping hidden / heavy directories and capping the files scanned.
func searchDirShallow(dir, needle string) []searchResult {
	out := []searchResult{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if res, ok := searchFileForNeedle(filepath.Join(dir, e.Name()), needle); ok {
			out = append(out, res)
		}
	}
	return out
}

func searchTreeRecursive(root, needle string, maxFiles int) []searchResult {
	out := []searchResult{}
	scanned := 0
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if scanned >= maxFiles {
			return filepath.SkipAll
		}
		scanned++
		if res, ok := searchFileForNeedle(path, needle); ok {
			out = append(out, res)
		}
		return nil
	})
	return out
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
	needle := strings.ToLower(q)

	var out []searchResult
	if r.URL.Query().Get("scope") == "git" {
		// Recurse from the enclosing git repo root (fall back to dir).
		root := s.gitRoot(r.Context(), abs)
		if root == "" {
			root = abs
		}
		out = searchTreeRecursive(root, needle, searchMaxGitFiles)
	} else {
		out = searchDirShallow(abs, needle)
	}

	// Most matches first.
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	s.writeJSON(w, http.StatusOK, out)
}

func (s *webServer) handleListRecursive(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("dir")
	if dir == "" {
		dir = s.startDir
	}
	q := strings.ToLower(r.URL.Query().Get("q"))
	abs, err := filepath.Abs(dir)
	if err != nil {
		http.Error(w, "invalid dir", http.StatusBadRequest)
		return
	}
	// scope=git lists from the enclosing repo root (falls back to dir).
	if r.URL.Query().Get("scope") == "git" {
		if root := s.gitRoot(r.Context(), abs); root != "" {
			abs = root
		}
	}

	const maxFiles = 2000
	const maxDepth = 8
	var result []recursiveListEntry
	totalFiles := 0

	var walk func(d string, depth int)
	walk = func(d string, depth int) {
		if depth > maxDepth || totalFiles >= maxFiles {
			return
		}
		entries, err := os.ReadDir(d)
		if err != nil {
			return
		}
		var files []webEntry
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			full := filepath.Join(d, entry.Name())
			if entry.IsDir() {
				walk(full, depth+1)
				continue
			}
			if q != "" && !strings.Contains(strings.ToLower(entry.Name()), q) {
				continue
			}
			var size int64
			var modTime string
			if info, ierr := entry.Info(); ierr == nil {
				size = info.Size()
				modTime = info.ModTime().Format(time.RFC3339)
			}
			files = append(files, webEntry{
				Name:    entry.Name(),
				Path:    full,
				IsDir:   false,
				Size:    size,
				ModTime: modTime,
			})
			totalFiles++
			if totalFiles >= maxFiles {
				break
			}
		}
		if len(files) > 0 {
			result = append(result, recursiveListEntry{Dir: d, Files: files})
		}
	}

	walk(abs, 0)
	if result == nil {
		result = []recursiveListEntry{}
	}
	s.writeJSON(w, http.StatusOK, result)
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
