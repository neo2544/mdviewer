package main

import (
	"context"
	_ "embed"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"fyne.io/systray"
)

//go:embed assets/menubar-icon.png
var menubarIconPNG []byte

//go:embed assets/menubar-icon@2x.png
var menubarIconRetinaPNG []byte

// menubarIconHiresPNG is the 512×512 master. The systray doesn't need it
// (macOS renders the @2x at 44px), but the web server hands it to browsers
// as /favicon.ico / /icon.png so Retina tabs and the in-app sidebar logo
// stay crisp.
//
//go:embed assets/menubar-icon@hires.png
var menubarIconHiresPNG []byte

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

	// Pre-bind the listener synchronously so we fail FAST on port
	// conflicts (typically "another mdviewer is already running").
	// Falling through with a deferred error would leave the menu-bar
	// icon flashing on screen and produce a crash loop under launchd's
	// KeepAlive — exit immediately instead.
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("cannot bind %s: %w (another mdviewer instance is probably running)", addr, err)
	}
	httpSrv := &http.Server{Handler: server.handler()}

	// Run the HTTP server in the background once we own the port.
	go func() {
		if err := httpSrv.Serve(listener); err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(os.Stderr, "mdviewer web server error:", err)
			systray.Quit()
		}
	}()

	// Translate POSIX signals (launchd's bootout, Ctrl+C, etc.) into a
	// clean systray shutdown so the menu-bar icon disappears and the
	// HTTP server is gracefully drained before the process exits.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		<-sigCh
		systray.Quit()
	}()

	onReady := func() {
		// Register the Apple-Event handler INSIDE onReady, after systray
		// has spun up NSApplication. Doing it earlier loses the
		// registration because AppKit's default kAEOpenDocuments handler
		// is installed during [NSApplication finishLaunching] and would
		// overwrite ours, sending Open With events to NSDocument's
		// "document opening session" machinery — which our app doesn't
		// implement, producing the silent "session already exists" log.
		registerOpenHandler()
		// Colorful icon (not a template): the sunset M↓ reads the same in light
		// and dark menu bars, so we don't want macOS inverting it. Use the @2x
		// bitmap so it stays crisp on Retina displays.
		systray.SetIcon(menubarIconRetinaPNG)
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
		mVersion := systray.AddMenuItem("Version  "+server.versionString(context.Background()), "Current app version")
		mVersion.Disable()
		mUpdate := systray.AddMenuItem("⬆ Update & Restart", "Pull the latest, rebuild, and restart")
		mUpdate.Hide()
		mCheck := systray.AddMenuItem("Check for Updates", "Check origin for newer commits")

		systray.AddSeparator()
		mQuit := systray.AddMenuItem("Quit MD Viewer", "Stop the server and quit")

		// Reflect available updates in the menu: show the Update item when the
		// checkout is behind its upstream.
		refreshUpdate := func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			behind, latest := server.updateBehind(ctx)
			if behind > 0 {
				mUpdate.SetTitle(fmt.Sprintf("⬆ Update & Restart (%d)", behind))
				if latest != "" {
					mUpdate.SetTooltip("Latest: " + latest)
				}
				mUpdate.Show()
			} else {
				mUpdate.Hide()
			}
		}

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
				case <-mCheck.ClickedCh:
					mCheck.SetTitle("Checking…")
					go func() {
						refreshUpdate()
						mCheck.SetTitle("Check for Updates")
					}()
				case <-mUpdate.ClickedCh:
					mUpdate.SetTitle("Updating…")
					mUpdate.Disable()
					go func() {
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
						defer cancel()
						exe, ok, msg := server.selfUpdateBuild(ctx)
						if ok {
							_ = syscall.Exec(exe, os.Args, os.Environ()) // replaces this process
							return
						}
						fmt.Fprintln(os.Stderr, "mdviewer update failed:", msg)
						mUpdate.SetTitle("Update failed — see logs")
						mUpdate.Enable()
					}()
				case <-mQuit.ClickedCh:
					systray.Quit()
					return
				}
			}
		}()

		// Check for updates shortly after launch, then periodically.
		go func() {
			time.Sleep(3 * time.Second)
			refreshUpdate()
			t := time.NewTicker(30 * time.Minute)
			defer t.Stop()
			for range t.C {
				refreshUpdate()
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
	// Log so the launchd .out log shows whether Apple Events actually
	// reached us — useful when debugging "Open With" pipeline issues.
	fmt.Fprintln(os.Stdout, "mdviewer: opening", abs, "→", target)
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
