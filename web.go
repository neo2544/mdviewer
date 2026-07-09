package main

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type webServer struct {
	startDir string
	appRoot  string
	csv      csvCache
}

type webEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	ModTime string `json:"mod_time"`
}

type listResponse struct {
	Cwd       string            `json:"cwd"`
	Entries   []webEntry        `json:"entries"`
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
	mux.HandleFunc("/api/csv", s.handleCSV)
	mux.HandleFunc("/api/drawio", s.handleDrawio)
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
	mux.HandleFunc("/api/ai/providers", s.handleAIProviders)
	mux.HandleFunc("/api/ai/models", s.handleAIModels)
	mux.HandleFunc("/api/ai/config", s.handleAIConfig)
	mux.HandleFunc("/api/ai/run", s.handleAIRun)
	return mux
}

// ────────────────────────────────────────────────────────────────
// Security: local-only hardening
// ────────────────────────────────────────────────────────────────
//
// The viewer binds to 127.0.0.1, but a local HTTP endpoint is still
// reachable by two vectors that don't need network exposure:
//
//   1. DNS rebinding — an attacker page whose domain re-resolves to
//      127.0.0.1 arrives with a spoofed Host header (evil.example.com).
//   2. CSRF — another origin open in the user's browser issuing requests
//      to http://127.0.0.1:8421/... .
//
// handler() wraps the API mux with two guards that close both WITHOUT
// restricting normal use:
//   - Host must name a loopback host we recognise (blocks rebinding).
//   - State-changing requests (POST/PUT/PATCH/DELETE) must not carry a
//     positive cross-origin signal (blocks browser CSRF).
//
// It deliberately does NOT confine file paths to the root: browsing and
// opening files anywhere on the filesystem (Finder "open .md", Jump to
// path, favorites outside root) is a core feature of this tool, so the
// same-origin browser session keeps working exactly as before.

var allowedLoopbackHosts = map[string]struct{}{
	"127.0.0.1": {},
	"localhost": {},
	"::1":       {},
}

// hostAllowed reports whether a Host (or URL) host names a loopback host
// we recognise. The port is ignored; only the hostname is checked.
func hostAllowed(hostHeader string) bool {
	if hostHeader == "" {
		return false
	}
	host := hostHeader
	if h, _, err := net.SplitHostPort(hostHeader); err == nil {
		host = h
	}
	// Strip any leftover IPv6 brackets (e.g. a bracketed host with no port).
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	_, ok := allowedLoopbackHosts[host]
	return ok
}

// originHostAllowed parses an Origin/Referer URL and reports whether its
// host is one of our loopback hosts.
func originHostAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	return hostAllowed(u.Host)
}

// crossOriginBlocked returns true only when there is positive evidence a
// request came from a different origin. Requests carrying no origin signal
// at all (native clients, the OS "open" flow, older tooling) are allowed so
// nothing that works today breaks.
func crossOriginBlocked(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return false // modern browser, same origin (or user-initiated)
	case "cross-site", "same-site":
		return true // modern browser, different origin
	}
	// Older browsers without Sec-Fetch-Site: fall back to Origin/Referer.
	if origin := r.Header.Get("Origin"); origin != "" {
		return !originHostAllowed(origin)
	}
	if referer := r.Header.Get("Referer"); referer != "" {
		return !originHostAllowed(referer)
	}
	return false
}

func isStateChangingMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// withSecurity wraps next with the loopback Host check and the CSRF guard.
func (s *webServer) withSecurity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(r.Host) {
			http.Error(w, "forbidden: unexpected Host header", http.StatusForbidden)
			return
		}
		if isStateChangingMethod(r.Method) && crossOriginBlocked(r) {
			http.Error(w, "forbidden: cross-origin request", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handler returns the fully-wrapped HTTP handler (security guards + routes).
// Both the standalone web server and the menu-bar app serve through this.
func (s *webServer) handler() http.Handler {
	return s.withSecurity(s.routes())
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
	return http.ListenAndServe(addr, server.handler())
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
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (s *webServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
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
	case ".csv", ".tsv":
		resp.Kind = "csv"
		// Table data is fetched separately via /api/csv (paginated).
	case ".drawio", ".dio":
		resp.Kind = "drawio"
		// Rendered read-only by /api/drawio inside a sandboxed iframe.
		resp.RawURL = "/api/drawio?path=" + url.QueryEscape(absPath)
	case ".txt", ".log",
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

// handleDrawio serves a minimal HTML page that renders a .drawio file
// read-only via the official draw.io GraphViewer. The heavy viewer script
// and its global mxGraph CSS stay isolated inside the preview iframe.
func (s *webServer) handleDrawio(w http.ResponseWriter, r *http.Request) {
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
	ext := strings.ToLower(filepath.Ext(absPath))
	if ext != ".drawio" && ext != ".dio" {
		http.Error(w, "not a drawio file", http.StatusBadRequest)
		return
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	// GraphViewer reads its config (diagram XML included) from the
	// data-mxgraph attribute as JSON. Keep "<" literal in the JSON
	// (SetEscapeHTML(false)) and rely on HTML attribute escaping only,
	// so the test/debug view stays readable as &lt;mxfile...
	cfg := map[string]any{
		"xml":       string(data),
		"toolbar":   "pages zoom layers lightbox",
		"nav":       true,
		"resize":    true,
		"auto-fit":  true,
		"highlight": "#4f8ff7",
	}
	var cfgJSON strings.Builder
	enc := json.NewEncoder(&cfgJSON)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(cfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	attr := html.EscapeString(strings.TrimSpace(cfgJSON.String()))

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, "<!DOCTYPE html>\n<html><head><meta charset=\"utf-8\">"+
		"<style>html,body{margin:0;min-height:100%;background:#fff}"+
		"body{padding:12px}.mxgraph{max-width:100%}</style></head><body>"+
		"<div class=\"mxgraph\" data-mxgraph=\""+attr+"\"></div>"+
		"<script src=\"https://viewer.diagrams.net/js/viewer-static.min.js\"></script>"+
		"</body></html>")
}

// scanCSVRecordOffsets returns the byte offset of the start of each CSV
// record in r. Newlines inside double-quoted fields are not treated as
// record terminators. A trailing newline does not produce a final empty
// record. Blank lines are skipped so the offsets stay in lock-step with
// encoding/csv, which silently ignores blank lines. The first offset
// corresponds to the header record.
func scanCSVRecordOffsets(r io.Reader) ([]int64, error) {
	br := bufio.NewReader(r)
	var offsets []int64
	var pos int64
	inQuote := false
	atLineStart := true // next byte begins a new line
	recorded := false   // offset already appended for the current record
	var lineStart int64 // candidate start offset of the current line
	for {
		b, err := br.ReadByte()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if atLineStart {
			lineStart = pos
			atLineStart = false
		}
		switch {
		case b == '\n' && !inQuote:
			// End of line. If the line had no content (blank line), no
			// offset was recorded, matching encoding/csv's behavior.
			atLineStart = true
			recorded = false
		case b == '\r' && !inQuote:
			// Treat CR as a non-content byte (handles \r\n line endings);
			// do not start a record on it.
		default:
			if b == '"' {
				inQuote = !inQuote
			}
			if !recorded {
				offsets = append(offsets, lineStart)
				recorded = true
			}
		}
		pos++
	}
	return offsets, nil
}

const csvCacheCap = 16

type csvIndex struct {
	modTime time.Time
	size    int64
	header  []string
	offsets []int64 // byte offset of each DATA record start (header excluded)
	total   int     // data rows (== len(offsets))
	delim   rune
}

type csvCache struct {
	mu     sync.Mutex
	m      map[string]*csvIndex
	order  []string // LRU; most-recently-used at the end
	builds int      // test instrumentation: number of (re)builds
}

// get returns a cached index for absPath, rebuilding if the file's modTime or
// size changed since the cached entry. Safe for concurrent use; zero value ready.
func (c *csvCache) get(absPath string, delim rune) (*csvIndex, error) {
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = make(map[string]*csvIndex)
	}
	if idx, ok := c.m[absPath]; ok &&
		idx.modTime.Equal(info.ModTime()) && idx.size == info.Size() && idx.delim == delim {
		c.touch(absPath)
		return idx, nil
	}
	idx, err := buildCSVIndex(absPath, delim, info)
	if err != nil {
		return nil, err
	}
	c.builds++
	c.m[absPath] = idx
	c.touch(absPath)
	c.evict()
	return idx, nil
}

func (c *csvCache) touch(key string) {
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
	c.order = append(c.order, key)
}

func (c *csvCache) evict() {
	for len(c.order) > csvCacheCap {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.m, oldest)
	}
}

// buildCSVIndex scans the whole file once to build the offset index and parse
// the header.
func buildCSVIndex(absPath string, delim rune, info os.FileInfo) (*csvIndex, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	all, err := scanCSVRecordOffsets(f)
	if err != nil {
		return nil, err
	}

	idx := &csvIndex{
		modTime: info.ModTime(),
		size:    info.Size(),
		delim:   delim,
		header:  []string{}, // non-nil so JSON renders [] not null
	}
	if len(all) == 0 {
		return idx, nil // empty file: no header, no rows
	}

	// Parse the header record (record 0).
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	hr := csv.NewReader(f)
	hr.Comma = delim
	hr.FieldsPerRecord = -1
	hr.LazyQuotes = true
	header, err := hr.Read()
	if err != nil && err != io.EOF {
		return nil, err
	}
	idx.header = header

	// Data record offsets exclude the header.
	idx.offsets = all[1:]
	idx.total = len(idx.offsets)
	return idx, nil
}

// readCSVPage seeks to data row `offset` and returns up to `limit` rows.
// Returns an empty slice if offset is at or beyond the end.
func readCSVPage(absPath string, idx *csvIndex, offset, limit int) ([][]string, error) {
	if offset < 0 || offset >= len(idx.offsets) || limit <= 0 {
		return [][]string{}, nil
	}
	f, err := os.Open(absPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(idx.offsets[offset], io.SeekStart); err != nil {
		return nil, err
	}
	rd := csv.NewReader(f)
	rd.Comma = idx.delim
	rd.FieldsPerRecord = -1
	rd.LazyQuotes = true

	rows := make([][]string, 0, limit)
	for len(rows) < limit {
		rec, err := rd.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		rows = append(rows, rec)
	}
	return rows, nil
}

type csvResponse struct {
	Path      string     `json:"path"`
	Delimiter string     `json:"delimiter"`
	Header    []string   `json:"header"`
	Rows      [][]string `json:"rows"`
	Page      int        `json:"page"`
	PageSize  int        `json:"page_size"`
	TotalRows int        `json:"total_rows"`
}

var csvPageSizes = map[int]bool{50: true, 100: true, 500: true}

func (s *webServer) handleCSV(w http.ResponseWriter, r *http.Request) {
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

	delim := ','
	if strings.ToLower(filepath.Ext(absPath)) == ".tsv" {
		delim = '\t'
	}

	page := 1
	if v, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && v >= 1 {
		page = v
	}
	pageSize := 100
	if v, err := strconv.Atoi(r.URL.Query().Get("page_size")); err == nil && csvPageSizes[v] {
		pageSize = v
	}

	idx, err := s.csv.get(absPath, delim)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	offset := (page - 1) * pageSize
	rows, err := readCSVPage(absPath, idx, offset, pageSize)
	if err != nil {
		// Index/seek mismatch (rare LazyQuotes edge): drop cache and full re-parse.
		rows, err = fallbackCSVPage(absPath, delim, offset, pageSize)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	delimStr := ","
	if delim == '\t' {
		delimStr = "\t"
	}
	s.writeJSON(w, http.StatusOK, csvResponse{
		Path:      absPath,
		Delimiter: delimStr,
		Header:    idx.header,
		Rows:      rows,
		Page:      page,
		PageSize:  pageSize,
		TotalRows: idx.total,
	})
}

// fallbackCSVPage re-parses from the start, skipping `offset` data rows. Used
// when the cached offset index does not align with csv.Reader parsing.
func fallbackCSVPage(absPath string, delim rune, offset, limit int) ([][]string, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rd := csv.NewReader(f)
	rd.Comma = delim
	rd.FieldsPerRecord = -1
	rd.LazyQuotes = true

	// Skip header.
	if _, err := rd.Read(); err != nil {
		if err == io.EOF {
			return [][]string{}, nil
		}
		return nil, err
	}
	// Skip `offset` data rows.
	for i := 0; i < offset; i++ {
		if _, err := rd.Read(); err != nil {
			if err == io.EOF {
				return [][]string{}, nil
			}
			return nil, err
		}
	}
	rows := make([][]string, 0, limit)
	for len(rows) < limit {
		rec, err := rd.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		rows = append(rows, rec)
	}
	return rows, nil
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
		".txt", ".log",
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

//go:embed web_app.html
var webAppHTML string

const searchMaxSnippets = 3
const searchSnippetLen = 60
const searchMaxFileBytes = 2 * 1024 * 1024 // skip files larger than 2 MB

type searchResult struct {
	Path         string   `json:"path"`
	Count        int      `json:"count"`
	Snippets     []string `json:"snippets"`
	MatchedTerms []string `json:"matchedTerms"`
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
// The UI uses this to enable/disable the "Git" search scope and to display the
// current branch next to the folder path (helps tell worktrees apart).
func (s *webServer) handleGitRoot(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("dir")
	if dir == "" {
		dir = s.startDir
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"root": "", "isRepo": false, "branch": ""})
		return
	}
	root := s.gitRoot(r.Context(), abs)
	branch := ""
	if root != "" {
		branch = s.gitBranch(r.Context(), abs)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"root": root, "isRepo": root != "", "branch": branch})
}

// gitBranch returns the current branch name for dir. In detached-HEAD state
// (e.g. a checked-out tag or specific commit) it returns "@<shorthash>". Empty
// when the branch cannot be resolved.
func (s *webServer) gitBranch(ctx context.Context, dir string) string {
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(c, "git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	b := strings.TrimSpace(string(raw))
	if b == "HEAD" { // detached HEAD → show the short commit id instead
		if h, herr := exec.CommandContext(c, "git", "-C", dir, "rev-parse", "--short", "HEAD").Output(); herr == nil {
			return "@" + strings.TrimSpace(string(h))
		}
	}
	return b
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
//
//	-ldflags "-X main.buildCommit=… -X main.buildDate=… …"
//
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
	// Baseline the "behind" count on what is actually RUNNING, not the
	// checkout's HEAD. For an installed binary (the .app launched by launchd),
	// buildCommit is the version baked in at build time; the checkout HEAD may
	// have moved ahead independently (e.g. a local merge/pull without a
	// reinstall), which would otherwise make this report 0 while the running
	// app is stale. Plain `go build`/`go run` leaves buildCommit empty, so
	// developers still compare against HEAD.
	base := "HEAD"
	if buildCommit != "" {
		base = buildCommit
	}
	behindStr, err := gitOutput(ctx, repo, "rev-list", "--count", base+".."+up)
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

// searchFileForExpr returns a searchResult for full when at least one line
// satisfies expr. Count is the number of satisfying lines; MatchedTerms lists
// which distinct terms appear anywhere in the file (for the UI color chips).
func searchFileForExpr(full string, expr *exprNode, terms []string) (searchResult, bool) {
	info, err := os.Stat(full)
	if err != nil || info.Size() > searchMaxFileBytes {
		return searchResult{}, false
	}
	data, err := os.ReadFile(full)
	if err != nil || !isProbablyText(data) {
		return searchResult{}, false
	}
	text := string(data)
	count := 0
	matched := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		ll := strings.ToLower(line)
		if expr.eval(ll) {
			count++
		}
		for _, term := range terms {
			if !matched[term] && strings.Contains(ll, term) {
				matched[term] = true
			}
		}
	}
	if count == 0 {
		return searchResult{}, false
	}
	mt := []string{}
	for _, term := range terms {
		if matched[term] {
			mt = append(mt, term)
		}
	}
	snippetNeedle := ""
	if len(mt) > 0 {
		snippetNeedle = mt[0]
	}
	lower := strings.ToLower(text)
	return searchResult{
		Path:         full,
		Count:        count,
		Snippets:     collectSnippets(text, lower, snippetNeedle, searchMaxSnippets),
		MatchedTerms: mt,
	}, true
}

func searchDirShallow(dir string, expr *exprNode, terms []string) []searchResult {
	out := []searchResult{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if res, ok := searchFileForExpr(filepath.Join(dir, e.Name()), expr, terms); ok {
			out = append(out, res)
		}
	}
	return out
}

func searchTreeRecursive(dirRoot string, expr *exprNode, terms []string, maxFiles int, docsOnly bool) []searchResult {
	out := []searchResult{}
	scanned := 0
	_ = filepath.WalkDir(dirRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if path != dirRoot && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if docsOnly && !isDocExt(d.Name()) {
			return nil
		}
		if scanned >= maxFiles {
			return filepath.SkipAll
		}
		scanned++
		if res, ok := searchFileForExpr(path, expr, terms); ok {
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
	expr, terms := parseSearchExpr(q)
	allFiles := r.URL.Query().Get("allFiles") == "1"

	var out []searchResult
	switch r.URL.Query().Get("scope") {
	case "git":
		root := s.gitRoot(r.Context(), abs)
		if root == "" {
			root = abs
		}
		out = searchTreeRecursive(root, expr, terms, searchMaxGitFiles, !allFiles)
	case "tree":
		out = searchTreeRecursive(abs, expr, terms, searchMaxGitFiles, !allFiles)
	default:
		out = searchDirShallow(abs, expr, terms)
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
