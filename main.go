package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// ────────────────────────────────────────────────────────────────
// Styles
// ────────────────────────────────────────────────────────────────

var (
	sidebarStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("62")).
			Padding(0, 1)

	selectedItem = lipgloss.NewStyle().
			Foreground(lipgloss.Color("212")).
			Bold(true)

	normalItem = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245"))

	dirItem = lipgloss.NewStyle().
		Foreground(lipgloss.Color("39"))

	titleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("212")).
			Bold(true).
			Padding(0, 1)

	statusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			Padding(0, 1)

	topBarStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252")).
			Background(lipgloss.Color("236")).
			Padding(0, 1)

	metaStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("250"))

	badgeStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("230")).
			Background(lipgloss.Color("62")).
			Padding(0, 1).
			Bold(true)

	warnBadgeStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("234")).
			Background(lipgloss.Color("220")).
			Padding(0, 1).
			Bold(true)

	previewBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("62"))

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))
)

// ────────────────────────────────────────────────────────────────
// File entry
// ────────────────────────────────────────────────────────────────

type entry struct {
	name  string
	isDir bool
	path  string
	size  int64
}

type previewKey struct {
	path  string
	width int
}

type fileMeta struct {
	exists  bool
	size    int64
	modTime time.Time
}

type previewLoadedMsg struct {
	seq     int
	path    string
	content string
	status  string
	meta    fileMeta
}

type fileCheckMsg struct{}
type previewTimeoutMsg struct {
	seq int
}

const maxMarkdownRenderBytes = 512 * 1024
const favoritesFileName = ".mdviewer_favorites.json"

var mermaidEdgePattern = regexp.MustCompile(`^\s*([A-Za-z0-9_]+(?:\[[^\]]+\]|\([^)]+\)|\{[^}]+\})?)\s*(?:-->|==>|-.->|---|~~~)\s*([A-Za-z0-9_]+(?:\[[^\]]+\]|\([^)]+\)|\{[^}]+\})?)`)
var toolLookupOnce sync.Once
var hasChafa bool
var hasMMDC bool

// ────────────────────────────────────────────────────────────────
// Model
// ────────────────────────────────────────────────────────────────

type model struct {
	appRoot           string
	cwd               string
	entries           []entry
	cursor            int
	listOffset        int
	favorites         []string
	favCursor         int
	viewport          viewport.Model
	ready             bool
	width             int
	height            int
	status            string
	focus             string // "list" | "preview"
	previewCache      map[previewKey]string
	previewSeq        int
	activePreviewSeq  int
	currentPreviewKey previewKey
	currentFileName   string
	currentFileMeta   fileMeta
	previewLoading    bool
	externalUpdate    bool
	pendingReload     bool

	// path-jump input mode (triggered by `g`): user types an absolute or
	// relative path; on Enter we navigate to its parent directory and
	// preview the file (or just open the directory).
	pathInputActive bool
	pathInput       string

	// pendingHighlight, if non-empty, tells the next loadDir call to
	// include that one hidden filename in the listing (so jumpToPath can
	// reveal dotfiles by absolute path).
	pendingHighlight string
}

func newModel(startDir, appRoot string) model {
	m := model{
		appRoot:      appRoot,
		cwd:          startDir,
		focus:        "list",
		status:       "Navigate with ↑/↓, Enter to open, Tab to switch panes",
		previewCache: make(map[previewKey]string),
	}
	m.loadFavorites()
	m.loadDir(startDir)
	return m
}

func (m *model) loadDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		m.status = "Error: " + err.Error()
		return
	}

	m.entries = []entry{}

	// Add parent dir entry if not root
	if dir != "/" {
		m.entries = append(m.entries, entry{name: "..", isDir: true, path: filepath.Dir(dir)})
	}

	var dirs, files []entry
	for _, e := range entries {
		// Skip hidden files (but allow a single explicitly-requested
		// dotfile through, so path-jump can reveal it).
		if strings.HasPrefix(e.Name(), ".") && e.Name() != m.pendingHighlight {
			continue
		}
		fullPath := filepath.Join(dir, e.Name())
		if e.IsDir() {
			dirs = append(dirs, entry{name: e.Name() + "/", isDir: true, path: fullPath})
		} else {
			var size int64
			if info, err := e.Info(); err == nil {
				size = info.Size()
			}
			files = append(files, entry{name: e.Name(), isDir: false, path: fullPath, size: size})
		}
	}

	sort.Slice(dirs, func(i, j int) bool { return dirs[i].name < dirs[j].name })
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })

	m.entries = append(m.entries, dirs...)
	m.entries = append(m.entries, files...)
	m.cwd = dir
	m.cursor = 0
	m.listOffset = 0
	// Consume the one-shot highlight hint after the directory has loaded.
	m.pendingHighlight = ""
}

// jumpToPath resolves a user-typed path and either opens its directory
// or opens its parent directory with the cursor placed on the file.
func (m *model) jumpToPath(target string) tea.Cmd {
	resolved, err := resolveUserPath(target, m.cwd)
	if err != nil {
		m.status = "Path error: " + err.Error()
		return nil
	}

	info, err := os.Stat(resolved)
	if err != nil {
		m.status = "Not found: " + resolved
		return nil
	}

	if info.IsDir() {
		m.loadDir(resolved)
		m.status = "Jumped to " + resolved
		return m.requestPreview()
	}

	parent := filepath.Dir(resolved)
	name := filepath.Base(resolved)
	// Ensure hidden target files are visible in the listing.
	if strings.HasPrefix(name, ".") {
		m.pendingHighlight = name
	}
	m.loadDir(parent)

	// Place the cursor on the target file (directories are suffixed with "/").
	matched := false
	for i, e := range m.entries {
		if e.path == resolved || e.name == name || e.name == name+"/" {
			m.cursor = i
			matched = true
			break
		}
	}
	m.keepCursorVisible()
	if matched {
		m.status = "Opened " + resolved
	} else {
		m.status = "Loaded folder, file not found in listing: " + resolved
	}
	return m.requestPreview()
}

func renderMarkdown(path string, wrapWidth int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "Error reading file: " + err.Error()
	}
	content := preprocessMarkdown(string(data), wrapWidth)

	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(wrapWidth),
	)
	if err != nil {
		return content
	}

	out, err := r.Render(content)
	if err != nil {
		return content
	}
	return out
}

func renderPlainText(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "Error reading file: " + err.Error()
	}
	return string(data)
}

// resolveUserPath turns a user-supplied path (possibly with ~ expansion,
// quoted, or relative) into an absolute, cleaned filesystem path.
// baseDir is used to resolve relative paths.
func resolveUserPath(input, baseDir string) (string, error) {
	p := strings.TrimSpace(input)
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	// Strip wrapping quotes, e.g. when a user pastes "/foo/bar.md"
	if len(p) >= 2 {
		if (strings.HasPrefix(p, "\"") && strings.HasSuffix(p, "\"")) ||
			(strings.HasPrefix(p, "'") && strings.HasSuffix(p, "'")) {
			p = p[1 : len(p)-1]
			p = strings.TrimSpace(p)
		}
	}
	// Drop a file:// prefix if present
	if strings.HasPrefix(p, "file://") {
		p = strings.TrimPrefix(p, "file://")
	}
	// Expand ~ / ~/...
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if p == "~" {
			p = home
		} else {
			p = filepath.Join(home, p[2:])
		}
	}
	// Resolve relative paths against baseDir
	if !filepath.IsAbs(p) {
		if baseDir == "" {
			baseDir, _ = os.Getwd()
		}
		p = filepath.Join(baseDir, p)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func humanSize(size int64) string {
	if size <= 0 {
		return "-"
	}

	units := []string{"B", "KB", "MB", "GB", "TB"}
	value := float64(size)
	unit := units[0]
	for i := 1; i < len(units) && value >= 1024; i++ {
		value /= 1024
		unit = units[i]
	}
	if unit == "B" {
		return fmt.Sprintf("%d%s", size, unit)
	}
	if value >= 10 {
		return fmt.Sprintf("%.0f%s", value, unit)
	}
	return fmt.Sprintf("%.1f%s", value, unit)
}

func preprocessMarkdown(content string, wrapWidth int) string {
	lines := strings.Split(content, "\n")
	var out strings.Builder
	inMermaid := false
	var mermaidLines []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inMermaid && (trimmed == "```mermaid" || trimmed == "~~~mermaid") {
			inMermaid = true
			mermaidLines = mermaidLines[:0]
			continue
		}
		if inMermaid && (trimmed == "```" || trimmed == "~~~") {
			out.WriteString(renderMermaidBlock(strings.Join(mermaidLines, "\n"), wrapWidth))
			out.WriteString("\n")
			inMermaid = false
			continue
		}
		if inMermaid {
			mermaidLines = append(mermaidLines, line)
			continue
		}
		out.WriteString(line)
		out.WriteString("\n")
	}

	if inMermaid {
		out.WriteString(renderMermaidBlock(strings.Join(mermaidLines, "\n"), wrapWidth))
	}

	return out.String()
}

func renderMermaidBlock(source string, wrapWidth int) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return "### Mermaid Diagram\n\n_No diagram content_\n"
	}

	summary := summarizeMermaid(source)
	var out strings.Builder
	out.WriteString("### Mermaid Diagram\n\n")
	if summary != "" {
		out.WriteString(summary)
		out.WriteString("\n")
	}
	if rendered, ok := renderMermaidWithTools(source, wrapWidth); ok {
		out.WriteString("\n```text\n")
		out.WriteString(rendered)
		if !strings.HasSuffix(rendered, "\n") {
			out.WriteString("\n")
		}
		out.WriteString("```\n")
		return out.String()
	}
	out.WriteString("```text\n")
	out.WriteString(source)
	out.WriteString("\n```\n")
	return out.String()
}

func summarizeMermaid(source string) string {
	lines := strings.Split(source, "\n")
	var summary []string
	for _, line := range lines {
		match := mermaidEdgePattern.FindStringSubmatch(strings.TrimSpace(line))
		if len(match) != 3 {
			continue
		}
		left := cleanMermaidNode(match[1])
		right := cleanMermaidNode(match[2])
		summary = append(summary, fmt.Sprintf("- %s -> %s", left, right))
		if len(summary) >= 8 {
			break
		}
	}
	if len(summary) == 0 {
		return "- Mermaid source block"
	}
	return strings.Join(summary, "\n")
}

func cleanMermaidNode(node string) string {
	node = strings.TrimSpace(node)
	replacer := strings.NewReplacer("[", " ", "]", " ", "(", " ", ")", " ", "{", " ", "}", " ", "\"", "")
	node = replacer.Replace(node)
	return strings.Join(strings.Fields(node), " ")
}

func renderImagePreview(path string, width int) string {
	if rendered, ok := renderImageWithTools(path, width); ok {
		return rendered
	}

	file, err := os.Open(path)
	if err != nil {
		return "Error reading image: " + err.Error()
	}
	defer file.Close()

	img, format, err := image.Decode(file)
	if err != nil {
		return "Image preview not available: " + err.Error()
	}

	bounds := img.Bounds()
	imgWidth := bounds.Dx()
	imgHeight := bounds.Dy()
	targetWidth := min(max(12, width-6), 80)
	if imgWidth < targetWidth {
		targetWidth = imgWidth
	}
	if targetWidth < 1 {
		targetWidth = 1
	}

	aspect := float64(imgHeight) / float64(max(1, imgWidth))
	targetHeight := int(float64(targetWidth) * aspect * 0.5)
	targetHeight = min(max(4, targetHeight), 40)

	chars := " .:-=+*#%@"
	var out strings.Builder
	out.WriteString(fmt.Sprintf("Image: %s\nFormat: %s\nSize: %dx%d\n\n", filepath.Base(path), strings.ToUpper(format), imgWidth, imgHeight))

	for y := 0; y < targetHeight; y++ {
		srcY := bounds.Min.Y + y*imgHeight/targetHeight
		for x := 0; x < targetWidth; x++ {
			srcX := bounds.Min.X + x*imgWidth/targetWidth
			r, g, b, _ := img.At(srcX, srcY).RGBA()
			luminance := (299*uint32(r/257) + 587*uint32(g/257) + 114*uint32(b/257)) / 1000
			idx := int(luminance * uint32(len(chars)-1) / 255)
			out.WriteByte(chars[idx])
		}
		out.WriteByte('\n')
	}

	return out.String()
}

func detectExternalTools() {
	toolLookupOnce.Do(func() {
		_, err := exec.LookPath("chafa")
		hasChafa = err == nil
		_, err = exec.LookPath("mmdc")
		hasMMDC = err == nil
	})
}

func renderImageWithTools(path string, width int) (string, bool) {
	detectExternalTools()
	if !hasChafa {
		return "", false
	}

	sizeArg := fmt.Sprintf("%dx30", min(max(20, width-6), 120))
	cmd := exec.Command("chafa", "--format", "symbols", "--size", sizeArg, path)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", false
	}
	rendered := strings.TrimRight(out.String(), "\n")
	if rendered == "" {
		return "", false
	}
	return rendered, true
}

func renderMermaidWithTools(source string, width int) (string, bool) {
	detectExternalTools()
	if !hasChafa || !hasMMDC {
		return "", false
	}

	tempDir, err := os.MkdirTemp("", "mdviewer-mermaid-*")
	if err != nil {
		return "", false
	}
	defer os.RemoveAll(tempDir)

	inputPath := filepath.Join(tempDir, "diagram.mmd")
	outputPath := filepath.Join(tempDir, "diagram.png")
	if err := os.WriteFile(inputPath, []byte(source), 0o644); err != nil {
		return "", false
	}

	cmd := exec.Command("mmdc", "-i", inputPath, "-o", outputPath, "-b", "transparent", "-t", "neutral")
	if err := cmd.Run(); err != nil {
		return "", false
	}

	return renderImageWithTools(outputPath, width)
}

// resolveAppRoot returns a writable directory for storing app data
// (currently just the favorites file). It prefers the OS user-config
// directory (e.g. ~/Library/Application Support/mdviewer on macOS),
// falls back to the home directory, and finally to the cwd. If a
// legacy favorites file exists next to the binary (the old default
// location), it is migrated into the new location on first run.
func resolveAppRoot() string {
	dir := preferredAppRoot()
	migrateLegacyFavorites(dir)
	return dir
}

func preferredAppRoot() string {
	if base, err := os.UserConfigDir(); err == nil && base != "" {
		dir := filepath.Join(base, "mdviewer")
		if err := os.MkdirAll(dir, 0o755); err == nil {
			return dir
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	if wd, err := os.Getwd(); err == nil && wd != "" {
		return wd
	}
	return "."
}

// migrateLegacyFavorites moves a favorites file from the old default
// location (the process cwd at startup) into the new app-root, but only
// if the new location doesn't already have one. Safe to call multiple
// times; missing files are ignored.
func migrateLegacyFavorites(newRoot string) {
	wd, err := os.Getwd()
	if err != nil || wd == "" || wd == newRoot {
		return
	}
	legacy := filepath.Join(wd, favoritesFileName)
	target := filepath.Join(newRoot, favoritesFileName)
	if _, err := os.Stat(legacy); err != nil {
		return
	}
	if _, err := os.Stat(target); err == nil {
		return // don't clobber an existing file in the new location
	}
	data, err := os.ReadFile(legacy)
	if err != nil {
		return
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		return
	}
	_ = os.Remove(legacy)
}

func (m model) favoritesPath() string {
	return filepath.Join(m.appRoot, favoritesFileName)
}

func (m *model) loadFavorites() {
	data, err := os.ReadFile(m.favoritesPath())
	if err != nil {
		if os.IsNotExist(err) {
			m.favorites = nil
		}
		return
	}

	var favorites []string
	if err := json.Unmarshal(data, &favorites); err != nil {
		m.status = "Could not read favorites file"
		return
	}

	seen := make(map[string]struct{}, len(favorites))
	m.favorites = m.favorites[:0]
	for _, dir := range favorites {
		if dir == "" {
			continue
		}
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		m.favorites = append(m.favorites, dir)
	}
	m.clampFavoriteCursor()
}

func (m *model) saveFavorites() {
	data, err := json.MarshalIndent(m.favorites, "", "  ")
	if err != nil {
		m.status = "Could not encode favorites"
		return
	}
	if err := os.WriteFile(m.favoritesPath(), data, 0o644); err != nil {
		m.status = "Could not save favorites"
	}
}

func statFile(path string) fileMeta {
	info, err := os.Stat(path)
	if err != nil {
		return fileMeta{}
	}
	return fileMeta{
		exists:  true,
		size:    info.Size(),
		modTime: info.ModTime(),
	}
}

func (m model) sidebarWidth() int {
	if m.width <= 0 {
		return 24
	}
	sidebarWidth := m.width / 3
	if sidebarWidth < 24 {
		return 24
	}
	return sidebarWidth
}

func (m model) previewWidth() int {
	width := m.width - m.sidebarWidth() - 4
	if width < 20 {
		return 20
	}
	return width
}

func (m model) paneHeight() int {
	// Keep one extra safety row because some terminal UIs report a height
	// that is slightly taller than the drawable area.
	height := m.height - 6
	if height < 1 {
		return 1
	}
	return height
}

func (m model) listHeight() int {
	return m.paneHeight()
}

func (m model) favoritesHeight() int {
	sidebarHeight := m.paneHeight()
	if sidebarHeight < 8 {
		return 3
	}
	if len(m.favorites) == 0 {
		return 4
	}
	return min(max(4, len(m.favorites)+2), max(4, sidebarHeight/3))
}

func (m model) filesPaneHeight() int {
	return max(1, m.paneHeight()-m.favoritesHeight())
}

func (m model) previewScrollPercent() int {
	if m.viewport.TotalLineCount() <= 0 || m.viewport.Height <= 0 {
		return 0
	}
	visibleEnd := m.viewport.YOffset + m.viewport.Height
	if visibleEnd >= m.viewport.TotalLineCount() {
		return 100
	}
	maxOffset := max(1, m.viewport.TotalLineCount()-m.viewport.Height)
	percent := int(float64(m.viewport.YOffset) / float64(maxOffset) * 100)
	if percent < 0 {
		return 0
	}
	if percent > 100 {
		return 100
	}
	return percent
}

func (m *model) clampCursor() {
	if len(m.entries) == 0 {
		m.cursor = 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.entries) {
		m.cursor = len(m.entries) - 1
	}
}

func (m *model) keepCursorVisible() {
	listHeight := m.filesPaneHeight()
	if listHeight <= 0 {
		m.listOffset = 0
		return
	}
	if m.cursor < m.listOffset {
		m.listOffset = m.cursor
	}
	if m.cursor >= m.listOffset+listHeight {
		m.listOffset = m.cursor - listHeight + 1
	}
	maxOffset := max(0, len(m.entries)-listHeight)
	if m.listOffset > maxOffset {
		m.listOffset = maxOffset
	}
	if m.listOffset < 0 {
		m.listOffset = 0
	}
}

func (m *model) clampFavoriteCursor() {
	if len(m.favorites) == 0 {
		m.favCursor = 0
		return
	}
	if m.favCursor < 0 {
		m.favCursor = 0
	}
	if m.favCursor >= len(m.favorites) {
		m.favCursor = len(m.favorites) - 1
	}
}

func (m model) favoriteLabel(path string) string {
	base := filepath.Base(path)
	if base == "." || base == string(filepath.Separator) || base == "" {
		base = path
	}
	return base + "  " + path
}

func (m model) isFavorite(dir string) bool {
	for _, favorite := range m.favorites {
		if favorite == dir {
			return true
		}
	}
	return false
}

func (m *model) toggleCurrentDirectoryFavorite() {
	dir := m.cwd
	for i, favorite := range m.favorites {
		if favorite == dir {
			m.favorites = append(m.favorites[:i], m.favorites[i+1:]...)
			m.clampFavoriteCursor()
			if m.focus == "favorites" && len(m.favorites) == 0 {
				m.focus = "list"
			}
			m.saveFavorites()
			m.status = "Removed current folder from favorites"
			return
		}
	}

	m.favorites = append(m.favorites, dir)
	sort.Strings(m.favorites)
	for i, favorite := range m.favorites {
		if favorite == dir {
			m.favCursor = i
			break
		}
	}
	m.saveFavorites()
	m.status = "Added current folder to favorites"
}

func (m model) nextFocus() string {
	switch m.focus {
	case "list":
		if len(m.favorites) > 0 {
			return "favorites"
		}
		return "preview"
	case "favorites":
		return "preview"
	default:
		return "list"
	}
}

func (m *model) currentEntry() (entry, bool) {
	if len(m.entries) == 0 {
		return entry{}, false
	}
	m.clampCursor()
	return m.entries[m.cursor], true
}

func (m *model) setPreview(content, status string) {
	if !m.ready {
		return
	}
	m.viewport.SetContent(content)
	m.viewport.GotoTop()
	if m.pendingReload {
		m.status = status + " | External update detected - reload? [y/N]"
		return
	}
	m.status = status
}

func (m *model) requestPreview() tea.Cmd {
	if !m.ready {
		return nil
	}

	e, ok := m.currentEntry()
	if !ok {
		m.activePreviewSeq = 0
		m.previewLoading = false
		m.currentPreviewKey = previewKey{}
		m.currentFileName = ""
		m.currentFileMeta = fileMeta{}
		m.externalUpdate = false
		m.pendingReload = false
		m.setPreview("  No files in this directory.", "Empty directory")
		return nil
	}

	m.keepCursorVisible()

	if e.isDir {
		m.currentPreviewKey = previewKey{}
		m.currentFileName = e.name
		m.currentFileMeta = fileMeta{}
		m.activePreviewSeq = 0
		m.previewLoading = false
		m.externalUpdate = false
		m.pendingReload = false
		m.setPreview("  Directory: "+e.path, "Directory - press Enter to open")
		return nil
	}

	key := previewKey{path: e.path, width: m.previewWidth()}
	m.currentPreviewKey = key
	m.currentFileName = e.name
	m.externalUpdate = false
	m.pendingReload = false
	if cached, ok := m.previewCache[key]; ok {
		m.currentFileMeta = statFile(e.path)
		m.previewLoading = false
		m.setPreview(cached, previewStatus(e.name))
		return nil
	}

	m.previewSeq++
	seq := m.previewSeq
	m.activePreviewSeq = seq
	m.previewLoading = true
	m.setPreview("  Loading preview...", "Loading "+e.name+"...")

	return tea.Batch(loadPreviewCmd(seq, e, key.width), previewTimeoutCmd(seq))
}

func loadPreviewCmd(seq int, e entry, width int) tea.Cmd {
	return func() tea.Msg {
		ext := strings.ToLower(filepath.Ext(e.name))
		var content string
		status := previewStatus(e.name)
		meta := statFile(e.path)
		switch ext {
		case ".md", ".markdown", ".mdx":
			if meta.exists && meta.size > maxMarkdownRenderBytes {
				content = renderPlainText(e.path)
				status = fmt.Sprintf("%s - large file, showing plain text", e.name)
			} else {
				content = renderMarkdown(e.path, max(20, width-4))
			}
		case ".txt", ".go", ".py", ".js", ".ts", ".sh", ".yaml", ".yml", ".json", ".toml":
			content = renderPlainText(e.path)
		case ".png", ".jpg", ".jpeg", ".gif":
			content = renderImagePreview(e.path, width)
		default:
			content = "  Binary or unsupported file type: " + e.name
		}

		return previewLoadedMsg{
			seq:     seq,
			path:    e.path,
			content: content,
			status:  status,
			meta:    meta,
		}
	}
}

func previewStatus(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".md", ".markdown", ".mdx":
		return "Markdown - Tab to switch pane, PgUp/PgDn to scroll"
	case ".txt", ".go", ".py", ".js", ".ts", ".sh", ".yaml", ".yml", ".json", ".toml":
		return name + " - plain text"
	case ".png", ".jpg", ".jpeg", ".gif":
		return name + " - image preview"
	default:
		return "Unsupported preview"
	}
}

func truncateRunes(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= maxWidth {
		return s
	}
	if maxWidth == 1 {
		return "…"
	}

	width := 0
	var b strings.Builder
	for _, r := range s {
		runeWidth := runewidth.RuneWidth(r)
		if width+runeWidth > maxWidth-1 {
			break
		}
		b.WriteRune(r)
		width += runeWidth
	}
	b.WriteRune('…')
	return b.String()
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func metaChanged(oldMeta, newMeta fileMeta) bool {
	if oldMeta.exists != newMeta.exists {
		return true
	}
	if !oldMeta.exists && !newMeta.exists {
		return false
	}
	return oldMeta.size != newMeta.size || !oldMeta.modTime.Equal(newMeta.modTime)
}

func fileWatchCmd() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg {
		return fileCheckMsg{}
	})
}

func previewTimeoutCmd(seq int) tea.Cmd {
	return tea.Tick(1500*time.Millisecond, func(time.Time) tea.Msg {
		return previewTimeoutMsg{seq: seq}
	})
}

func (m model) singleLineBar(style lipgloss.Style, text string) string {
	width := max(1, m.width)
	return style.Width(width).MaxWidth(width).Render(truncateRunes(text, width))
}

func (m model) topBarView() string {
	width := max(1, m.width)
	location := "MD Viewer  " + m.cwd
	name := m.currentFileName
	if name == "" {
		name = "(none)"
	}
	metaParts := []string{name}
	if m.currentFileMeta.exists {
		metaParts = append(metaParts, m.currentFileMeta.modTime.Format("2006-01-02 15:04:05"))
		metaParts = append(metaParts, fmt.Sprintf("%dB", m.currentFileMeta.size))
	}
	if m.previewLoading {
		metaParts = append(metaParts, badgeStyle.Render("LOADING"))
	}
	if m.externalUpdate {
		metaParts = append(metaParts, warnBadgeStyle.Render("UPDATED"))
		if m.pendingReload {
			metaParts = append(metaParts, metaStyle.Render("Reload now? [y/N]"))
		} else {
			metaParts = append(metaParts, metaStyle.Render("Press y to reload or n to dismiss"))
		}
	}

	bar := location + " | " + strings.Join(metaParts, "  ")
	return topBarStyle.Width(width).MaxWidth(width).Render(truncateRunes(bar, width))
}

// ────────────────────────────────────────────────────────────────
// Bubbletea interface
// ────────────────────────────────────────────────────────────────

func (m model) Init() tea.Cmd {
	return fileWatchCmd()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		vpH := m.paneHeight()
		vpW := m.previewWidth()

		if !m.ready {
			m.viewport = viewport.New(vpW, vpH)
			m.ready = true
		} else {
			m.viewport.Width = vpW
			m.viewport.Height = vpH
		}
		return m, m.requestPreview()

	case previewLoadedMsg:
		if msg.seq != m.activePreviewSeq {
			return m, nil
		}

		e, ok := m.currentEntry()
		if !ok || e.path != msg.path {
			return m, nil
		}

		key := previewKey{path: msg.path, width: m.previewWidth()}
		m.previewCache[key] = msg.content
		m.currentPreviewKey = key
		m.currentFileMeta = msg.meta
		m.previewLoading = false
		m.externalUpdate = false
		m.pendingReload = false
		m.setPreview(msg.content, msg.status)
		return m, nil

	case previewTimeoutMsg:
		if msg.seq == m.activePreviewSeq && m.previewLoading {
			m.previewLoading = false
			m.status = "Preview is taking longer than expected - press r to retry or wait"
		}
		return m, nil

	case fileCheckMsg:
		if m.currentPreviewKey.path != "" && !m.previewLoading {
			latestMeta := statFile(m.currentPreviewKey.path)
			if metaChanged(m.currentFileMeta, latestMeta) {
				m.externalUpdate = true
				m.pendingReload = true
				m.status = "Selected file changed on disk - reload now? [y/N]"
			}
		}
		return m, fileWatchCmd()

	case tea.KeyMsg:
		// While the path-jump input is active, intercept all keys
		// before the normal navigation handler so typed characters
		// don't trigger other shortcuts (e.g. q quits, j scrolls).
		if m.pathInputActive {
			switch msg.String() {
			case "ctrl+c":
				return m, tea.Quit
			case "esc":
				m.pathInputActive = false
				m.pathInput = ""
				m.status = "Path input cancelled"
				return m, nil
			case "enter":
				target := strings.TrimSpace(m.pathInput)
				m.pathInputActive = false
				m.pathInput = ""
				if target == "" {
					m.status = "Empty path"
					return m, nil
				}
				return m, m.jumpToPath(target)
			case "backspace":
				if r := []rune(m.pathInput); len(r) > 0 {
					m.pathInput = string(r[:len(r)-1])
				}
				return m, nil
			case "ctrl+u":
				m.pathInput = ""
				return m, nil
			case "space":
				m.pathInput += " "
				return m, nil
			case "tab":
				// ignore tab while typing a path
				return m, nil
			}
			// Otherwise treat as text input — append the typed runes.
			if len(msg.Runes) > 0 {
				m.pathInput += string(msg.Runes)
			}
			return m, nil
		}

		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit

		case "g", ":":
			m.pathInputActive = true
			m.pathInput = ""
			m.status = "Type a path and press Enter (Esc to cancel)"
			return m, nil

		case "tab":
			m.focus = m.nextFocus()

		case "f":
			m.toggleCurrentDirectoryFavorite()

		case "r":
			if m.currentPreviewKey.path != "" {
				delete(m.previewCache, m.currentPreviewKey)
				m.previewLoading = false
				m.externalUpdate = false
				m.pendingReload = false
				return m, m.requestPreview()
			}

		case "y":
			if m.externalUpdate && m.pendingReload && m.currentPreviewKey.path != "" {
				delete(m.previewCache, m.currentPreviewKey)
				m.previewLoading = false
				m.externalUpdate = false
				m.pendingReload = false
				return m, m.requestPreview()
			}

		case "n":
			if m.externalUpdate && m.pendingReload {
				m.pendingReload = false
				m.status = "External update detected - press r or y to reload later"
			}

		case "up", "k":
			if m.focus == "list" && m.cursor > 0 {
				m.cursor--
				m.keepCursorVisible()
				return m, m.requestPreview()
			} else if m.focus == "favorites" && m.favCursor > 0 {
				m.favCursor--
			} else if m.focus == "preview" {
				m.viewport.LineUp(3)
			}

		case "down", "j":
			if m.focus == "list" && m.cursor < len(m.entries)-1 {
				m.cursor++
				m.keepCursorVisible()
				return m, m.requestPreview()
			} else if m.focus == "favorites" && m.favCursor < len(m.favorites)-1 {
				m.favCursor++
			} else if m.focus == "preview" {
				m.viewport.LineDown(3)
			}

		case "enter":
			if m.focus == "favorites" && len(m.favorites) > 0 {
				m.loadDir(m.favorites[m.favCursor])
				m.focus = "list"
				return m, m.requestPreview()
			}
			if len(m.entries) == 0 {
				break
			}
			e := m.entries[m.cursor]
			if e.isDir {
				m.loadDir(e.path)
				return m, m.requestPreview()
			}

		case "pgup":
			m.viewport.HalfViewUp()
		case "pgdown":
			m.viewport.HalfViewDown()
		case "home":
			m.viewport.GotoTop()
		case "end":
			m.viewport.GotoBottom()
		}
	}

	return m, nil
}

func (m model) View() string {
	if !m.ready {
		return "Initializing..."
	}

	// ── Sidebar ──────────────────────────────────────────────────
	sideW := m.sidebarWidth()
	filesH := m.filesPaneHeight()
	favoritesH := m.favoritesHeight()

	var sidebarContent strings.Builder
	start := min(m.listOffset, len(m.entries))
	end := min(start+filesH, len(m.entries))
	for i := start; i < end; i++ {
		e := m.entries[i]
		nameWidth := max(1, sideW-12)
		sizeLabel := ""
		if !e.isDir {
			sizeLabel = humanSize(e.size)
			nameWidth = max(1, sideW-runewidth.StringWidth(sizeLabel)-5)
		}
		line := truncateRunes(e.name, nameWidth)
		if sizeLabel != "" {
			padding := max(1, sideW-4-runewidth.StringWidth(line)-runewidth.StringWidth(sizeLabel))
			line += strings.Repeat(" ", padding) + sizeLabel
		}

		var styled string
		if i == m.cursor {
			prefix := "  "
			if m.focus == "list" {
				prefix = "▶ "
			}
			styled = selectedItem.Render(prefix + line)
		} else if e.isDir {
			styled = dirItem.Render("  " + line)
		} else {
			styled = normalItem.Render("  " + line)
		}
		sidebarContent.WriteString(styled + "\n")
	}

	lines := strings.Count(sidebarContent.String(), "\n")
	for lines < filesH {
		sidebarContent.WriteString("\n")
		lines++
	}
	favoritesTitle := " Favorites"
	if m.isFavorite(m.cwd) {
		favoritesTitle += " ★"
	}
	sidebarContent.WriteString(selectedItem.Render(truncateRunes(favoritesTitle, max(1, sideW-2))) + "\n")
	if len(m.favorites) == 0 {
		sidebarContent.WriteString(helpStyle.Render("  No favorites\n"))
	} else {
		for i, favorite := range m.favorites {
			line := truncateRunes(m.favoriteLabel(favorite), max(1, sideW-4))
			prefix := "  "
			style := normalItem
			if i == m.favCursor && m.focus == "favorites" {
				prefix = "▶ "
				style = selectedItem
			} else if favorite == m.cwd {
				style = dirItem
			}
			sidebarContent.WriteString(style.Render(prefix + line))
			sidebarContent.WriteString("\n")
		}
	}

	totalLines := strings.Count(sidebarContent.String(), "\n")
	targetLines := filesH + favoritesH
	for totalLines < targetLines {
		sidebarContent.WriteString("\n")
		totalLines++
	}

	sidebar := sidebarStyle.
		Width(sideW).
		Height(m.paneHeight()).
		Render(sidebarContent.String())

	// ── Preview pane ─────────────────────────────────────────────
	previewBorderColor := lipgloss.Color("62")
	if m.focus == "preview" {
		previewBorderColor = lipgloss.Color("212")
	}

	preview := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(previewBorderColor).
		Width(m.previewWidth()).
		Height(m.paneHeight()).
		Render(m.viewport.View())

	// ── Layout ───────────────────────────────────────────────────
	title := m.topBarView()
	panes := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, preview)
	help := m.singleLineBar(helpStyle, "  q quit • ↑↓/jk navigate • Enter open dir • Tab switch pane • g go to path • f toggle favorite • r refresh • y/n external reload")

	var statusLine string
	if m.pathInputActive {
		statusLine = fmt.Sprintf("  Go to: %s_", m.pathInput)
		statusLine += "    (Enter=go, Esc=cancel, Ctrl+U=clear)"
	} else {
		statusLine = fmt.Sprintf("  %s  |  Preview %d%%", m.status, m.previewScrollPercent())
	}
	status := m.singleLineBar(statusStyle, statusLine)

	return lipgloss.JoinVertical(lipgloss.Left,
		title,
		panes,
		status,
		help,
	)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ────────────────────────────────────────────────────────────────
// main
// ────────────────────────────────────────────────────────────────

// launchedFromAppBundle returns true when the running binary lives
// inside a macOS .app bundle (i.e. .../<App>.app/Contents/MacOS/…). It
// is used to switch the default run-mode to menubar when Finder/Launch
// Services launches us with no flags.
func launchedFromAppBundle() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	return strings.Contains(exe, ".app/Contents/MacOS/")
}

// existingServerReachable does a 200 ms TCP probe to addr and returns
// true if something is already listening there.
func existingServerReachable(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func main() {
	// appRoot is where we persist app data (favorites). We need a writable
	// location that survives launch context — when mdviewer is started as a
	// menu-bar .app bundle, os.Getwd() can be "/" which is read-only on
	// macOS (SIP). Prefer the OS user-config directory and migrate any
	// legacy favorites file that may exist next to the binary.
	appRoot := resolveAppRoot()

	// Three run modes:
	//   default      → TUI
	//   --web        → HTTP server only (foreground)
	//   --menubar    → macOS menu bar app (also runs the web server)
	// --port and --root configure the web/menubar modes; the first
	// positional arg is also accepted as the root dir for back-compat.
	var (
		webMode     bool
		menubarMode bool
		port        string
		rootDir     string
	)

	args := os.Args[1:]
	positional := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--web" || a == "-web":
			webMode = true
		case a == "--menubar" || a == "-menubar":
			menubarMode = true
		case a == "--port" || a == "-port":
			if i+1 < len(args) {
				port = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--port="):
			port = strings.TrimPrefix(a, "--port=")
		case a == "--root" || a == "-root":
			if i+1 < len(args) {
				rootDir = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--root="):
			rootDir = strings.TrimPrefix(a, "--root=")
		default:
			positional = append(positional, a)
		}
	}

	startDir := "."
	switch {
	case rootDir != "":
		startDir = rootDir
	case len(positional) > 0:
		startDir = positional[0]
	}

	abs, err := filepath.Abs(startDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	addr := ""
	if port != "" {
		addr = "127.0.0.1:" + port
	}

	// If launched without any explicit mode flag AND we were started
	// from inside a .app bundle (so there is no TTY), default to menubar
	// mode. This is what happens when Finder/Launch Services launches us
	// for "Open With → MD Viewer": without this fallback the binary would
	// try to start the TUI, fail to open /dev/tty, and macOS would show
	// "MD Viewer cannot open files in the 'Markdown Document' format".
	if !webMode && !menubarMode && launchedFromAppBundle() {
		menubarMode = true
		// If another instance (typically the launchd-managed one) already
		// owns the menu-bar port, don't try to take it over; just open the
		// browser at its URL and exit cleanly. The Apple-Event handler on
		// the running instance will navigate the page to the right file.
		probeAddr := addr
		if probeAddr == "" {
			probeAddr = "127.0.0.1:8421"
		}
		if existingServerReachable(probeAddr) {
			_ = openInBrowser("http://" + probeAddr + "/")
			return
		}
	}

	if menubarMode {
		if err := runMenuBarApp(abs, appRoot, addr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	if webMode {
		if err := runWebServer(abs, appRoot, addr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	p := tea.NewProgram(newModel(abs, appRoot), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
