package main

import (
	"context"
	_ "embed"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"fyne.io/systray"
)

//go:embed assets/menubar-icon.png
var menubarIconPNG []byte

//go:embed assets/menubar-icon@2x.png
var menubarIconRetinaPNG []byte

// openFileChan delivers file paths that need to be opened in the web
// viewer. macOS Apple-Event handlers (see menubar_darwin.go) push paths
// here, and the menu-bar loop consumes them.
var openFileChan = make(chan string, 16)

// runMenuBarApp starts the embedded web server in a goroutine and then
// hands control to the systray event loop. Returns when the user clicks
// Quit (or the OS terminates the process).
func runMenuBarApp(startDir, appRoot, addr string) error {
	if addr == "" {
		addr = "127.0.0.1:8421"
	}
	serverURL := "http://" + addr

	server := &webServer{
		startDir: startDir,
		appRoot:  appRoot,
	}
	httpSrv := &http.Server{
		Addr:    addr,
		Handler: server.routes(),
	}

	// Run the HTTP server in the background. If it fails to bind (port
	// already in use, for example) log and exit so launchd can react.
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(os.Stderr, "mdviewer web server error:", err)
			// Give the user-visible icon time to remove itself before bailing.
			time.AfterFunc(500*time.Millisecond, func() { systray.Quit() })
		}
	}()

	// Register the Apple-Event handler that turns "Open Document" events
	// (double-click in Finder, drag-onto-icon, `open -a MdViewer foo.md`)
	// into messages on openFileChan. No-op on non-darwin builds.
	registerOpenHandler()

	onReady := func() {
		// Template icons render correctly in both light and dark menu bars
		// (macOS inverts the alpha for us).
		systray.SetTemplateIcon(menubarIconPNG, menubarIconPNG)
		systray.SetTooltip("MD Viewer · " + serverURL)

		mOpen := systray.AddMenuItem("Open in Browser", "Open the viewer in your default browser")
		mReveal := systray.AddMenuItem("Reveal Root Folder in Finder", "Reveal "+startDir)
		mCopy := systray.AddMenuItem("Copy URL", "Copy "+serverURL+" to clipboard")

		systray.AddSeparator()

		mFolderInfo := systray.AddMenuItem("Serving  "+truncateDisplayPath(startDir, 48), startDir)
		mFolderInfo.Disable()
		mAddrInfo := systray.AddMenuItem("Listening on  "+addr, "Local-only address")
		mAddrInfo.Disable()

		systray.AddSeparator()
		mQuit := systray.AddMenuItem("Quit MD Viewer", "Stop the server and quit")

		// Menu-item event loop
		go func() {
			for {
				select {
				case <-mOpen.ClickedCh:
					_ = openInBrowser(serverURL + "/")
				case <-mReveal.ClickedCh:
					_ = exec.Command("open", startDir).Start()
				case <-mCopy.ClickedCh:
					_ = writeClipboard(serverURL + "/")
				case <-mQuit.ClickedCh:
					systray.Quit()
					return
				}
			}
		}()

		// File-open event loop: Apple Events arrive here.
		go func() {
			for path := range openFileChan {
				openFileInViewer(serverURL, path)
			}
		}()
	}

	onExit := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(ctx)
	}

	systray.Run(onReady, onExit)
	return nil
}

// openFileInViewer pushes the desktop browser to the viewer URL with a
// ?path=… query string so the existing client-side router selects the
// file. We resolve to abs first so paths from Apple Events (which may be
// file URLs already converted to POSIX) are normalised.
func openFileInViewer(serverURL, path string) {
	if path == "" {
		return
	}
	abs, err := resolveUserPath(path, "")
	if err != nil {
		abs = path
	}
	target := serverURL + "/?path=" + url.QueryEscape(abs)
	_ = openInBrowser(target)
}

// openInBrowser launches the system browser at the given URL.
func openInBrowser(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "linux":
		cmd = exec.Command("xdg-open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return cmd.Start()
}

// writeClipboard puts the text into the macOS clipboard via pbcopy.
// Fails silently on other platforms.
func writeClipboard(text string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("clipboard helper only implemented for darwin")
	}
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

// truncateDisplayPath shortens a long path for compact menu display.
func truncateDisplayPath(p string, maxLen int) string {
	if len(p) <= maxLen {
		return p
	}
	return "…" + p[len(p)-maxLen+1:]
}
