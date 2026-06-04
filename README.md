# mdviewer — Web · Menubar · TUI Markdown Viewer

A markdown viewer you can browse and edit in **three modes from a single binary**: a browser-based web viewer, a macOS menu-bar app that auto-starts, and a terminal TUI.

> 한국어 README: [README.ko.md](README.ko.md)

- **Web (recommended)** — `http://127.0.0.1:8421/` · file browser, Preview/Edit/Split, search, memos, outline, Mermaid, math, themes, git version compare, self-update
- **Menubar** — auto-start at login, click the menu bar to open the browser + `.md` double-click association
- **TUI** — terminal mode built on `bubbletea + glamour`

The UI is bilingual (**English / 한국어**): it follows your browser language and can be switched anytime with the EN/한 toggle in the header.

---

## 🚀 Quick install (macOS menu-bar app)

```bash
git clone https://github.com/neo2544/mdviewer.git
cd mdviewer
scripts/install.sh            # one line (requires Xcode CLT)
```

What `install.sh` does:

- ✅ Builds the `mdviewer` binary with version metadata embedded
- ✅ Registers `~/Applications/MdViewer.app` as a menu-bar app (`LSUIElement` — no Dock icon)
- ✅ Installs a LaunchAgent — auto-start at login + restart if it dies
- ✅ Associates `.md` / `.markdown` / `.mdx` double-click → opens in the browser (Apple Event)
- ✅ Keeps `http://127.0.0.1:8421/` always running

Options / removal:

```bash
scripts/install.sh --root ~/Documents --port 8421   # different root / port
scripts/install.sh --help                            # all options
scripts/uninstall.sh                                 # uninstall
```

Clicking the menu-bar icon (M↓):

![Menu bar dropdown](assets/menubar-screenshot.png)

- **Open in Browser** / **Reveal Root Folder in Finder** / **Copy URL**
- **Version · Check for Updates · ⬆ Update & Restart** — update straight from the menu when a new version exists
- **Quit MD Viewer**

Logs: `~/Library/Logs/MdViewer/mdviewer.{out,err}.log`

---

## ✨ Key features (web)

### File browsing
- Sidebar file browser with **Recent files / Recent folders / Favorites**
- **Drag to reorder favorites** via the `⠿` handle (sidebar + full-list modal), with per-item aliases
- **Jump to path** — `Cmd/Ctrl+L` or the sidebar "Jump to path" (absolute / relative / `~`)
- **Subfolder browse modal** — find by filename, toggle `this folder / Git repo` scope (disabled outside a git repo)

### Preview & edit
- **Preview / Edit / Split** modes, `⌘S` to save, click the filename in the header to copy its path
- Markdown rendering + **code syntax highlighting** (copy button per block), image lightbox
- **Theme** toggle (Auto / Light / Dark) and a custom **accent color** picker

### Git-aware
- **Version compare** — pick any two revisions of a file and view them side by side with a synchronized, color-coded diff (added = green, removed = red), change navigation, table/code/mermaid-aware highlighting, and intra-line character emphasis
- **Changes overlay** — an inline "what changed since the last version" view in the preview (additions green, deletions red strikethrough)
- **AI-DLC mode** — when the repo root has an `aidlc-docs` folder, list every doc under it, newest first

### Search (Search tab)
- **In this file** — searches the rendered content (matches across inline markup), `Line / Priority` ordering, line numbers
- **Same folder / Git repo** — shallow current folder ↔ recursive enclosing git repo, with a loading indicator

### Outline (Outline tab)
- Heading list with click-to-jump + **scroll spy**, and an `H1–2 / H1–3 / All` level toggle

### Memo notebook (Memo tab)
- **Multiple memos** — `＋ New`, card list (title/first line · modified time · source), search filter · sort · pin
- **Server sync** — stored in `.mdviewer_memos.json`, shared across sessions; conflicts surface a popup (mine / server / keep both)
- **Select → memo/search/copy** — selecting text in the body shows floating actions; memos keep a source backlink
- **Trash** — deleted memos move to a recoverable trash (restore / empty)

### Mermaid & diagrams
- Auto-renders `mermaid` code blocks; click to zoom in a lightbox (annotate, save PNG, ⌥+drag to copy text)
- **Mermaid Playground** (🧪) — paste and live-render, draw annotations, post-its, PNG export; math like `$E=mc^2$` is auto-detected

### Math (KaTeX)
- Renders `$…$`, `$$…$$`, `\(…\)`, `\[…\]` in the body (code blocks excluded)

### Version & self-update
- Current version (branch + short hash) in the header/sidebar — **hover for the recent commit log**
- When origin has new commits, an **`⬆ Update (N)`** button runs `git pull --ff-only → build (re-embed version) → swap binary → restart`
- `go run` (`run.sh`/`run-web.sh`) shows the version only; self-update is disabled (temp binary)

---

## Run modes

| Mode | Command | Description |
|---|---|---|
| TUI | `mdviewer [path]` | Browse inside the terminal |
| Web | `mdviewer --web [--port 8421] [--root <dir>]` | Local web server |
| Menubar | `mdviewer --menubar [--port 8421] [--root <dir>]` | Menu-bar icon + web server |

> For self-update, run a binary **built inside the repo** (or the app installed via `install.sh`).

## Build & dev

```bash
go mod tidy
go run .                  # current folder (TUI)
go run . ~/Documents      # a specific folder
./run-web.sh [path]       # web mode (go run)
go build -o mdviewer .    # build
./mdviewer --web .        # web from the built binary (self-update enabled)
```

## TUI shortcuts

| Key | Action |
|---|---|
| `↑`/`k`, `↓`/`j` | Move in list / preview |
| `Enter` | Open directory |
| `Tab` | Focus list ↔ preview |
| `PgUp`/`PgDn`, `Home`/`End` | Scroll preview |
| `g` or `:` | Jump-to-path mode |
| `q` / `Ctrl+C` | Quit |

## Supported preview formats

- **Markdown** (`.md`, `.markdown`, `.mdx`)
- **Text/code** (`.txt`, `.go`, `.py`, `.js`, `.ts`, `.sh`, `.yaml`, `.json`, `.toml`, …)
- Images · Mermaid · others rendered per format

---

## Layout

```
mdviewer/
├── main.go                # entry point · flag parsing · TUI (bubbletea)
├── web.go                 # web server + embedded HTML/CSS/JS (single-page app)
├── menubar.go             # menu-bar app (systray) + self-update
├── menubar_darwin.{go,m}  # macOS Apple Event (.md open) handler
├── assets/                # menu-bar icons, etc.
└── scripts/               # install.sh · uninstall.sh
```

Runtime data (stored in the root folder): `.mdviewer_favorites.json` · `.mdviewer_aliases.json` · `.mdviewer_memos.json`

## Dependencies

```
github.com/charmbracelet/bubbletea / bubbles / lipgloss / glamour   — TUI · rendering
fyne.io/systray                                                     — menu-bar icon
```
The web frontend loads marked.js · highlight.js · mermaid · KaTeX from CDNs.
