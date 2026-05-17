#!/usr/bin/env bash
#
# install.sh — build mdviewer, package it as MdViewer.app, install it to
# ~/Applications, and register a launchd LaunchAgent so the menu-bar app
# starts at every login and is restarted if it crashes.
#
# Usage:   scripts/install.sh [--root <dir>] [--port <n>]
# Default: --root <repo root>  --port 8421
#
# Re-running is safe: the LaunchAgent is unloaded, the .app is replaced,
# and the agent is loaded again.

set -euo pipefail

# ---- arg parsing --------------------------------------------------------

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ROOT_DIR="$REPO_ROOT"
PORT="8421"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --root) ROOT_DIR="$2"; shift 2;;
        --port) PORT="$2"; shift 2;;
        -h|--help)
            sed -n '2,12p' "$0"; exit 0;;
        *) echo "unknown arg: $1" >&2; exit 2;;
    esac
done

# Normalize ROOT_DIR to absolute
ROOT_DIR="$(python3 -c 'import os,sys; print(os.path.realpath(sys.argv[1]))' "$ROOT_DIR")"

if [[ ! -d "$ROOT_DIR" ]]; then
    echo "Root folder does not exist: $ROOT_DIR" >&2
    exit 1
fi

GO="${GO:-$(command -v go || echo /opt/homebrew/bin/go)}"
if ! "$GO" version >/dev/null 2>&1; then
    echo "go toolchain not found (set GO=/path/to/go)" >&2
    exit 1
fi

APP_NAME="MdViewer"
APP_BUNDLE_ID="com.jk.mdviewer"
APPS_DIR="$HOME/Applications"
APP_PATH="$APPS_DIR/${APP_NAME}.app"
LAUNCH_AGENT_DIR="$HOME/Library/LaunchAgents"
LAUNCH_AGENT_PLIST="$LAUNCH_AGENT_DIR/${APP_BUNDLE_ID}.plist"
LOG_DIR="$HOME/Library/Logs/MdViewer"

echo ">> repo:    $REPO_ROOT"
echo ">> root:    $ROOT_DIR"
echo ">> port:    $PORT"
echo ">> bundle:  $APP_PATH"
echo ">> agent:   $LAUNCH_AGENT_PLIST"

# ---- build --------------------------------------------------------------

echo ">> building mdviewer binary…"
cd "$REPO_ROOT"
CGO_ENABLED=1 "$GO" build -o "$REPO_ROOT/mdviewer" .

# ---- assemble .app bundle ----------------------------------------------

echo ">> assembling app bundle…"
mkdir -p "$APPS_DIR"
TMP_APP="$(mktemp -d)/${APP_NAME}.app"
mkdir -p "$TMP_APP/Contents/MacOS" "$TMP_APP/Contents/Resources"

cp "$REPO_ROOT/mdviewer" "$TMP_APP/Contents/MacOS/${APP_NAME}"
chmod +x "$TMP_APP/Contents/MacOS/${APP_NAME}"

# Include the menu-bar icon as the app icon too (handy when listed in
# Spotlight; the menu-bar icon itself is embedded in the binary already).
if [[ -f "$REPO_ROOT/assets/menubar-icon@2x.png" ]]; then
    cp "$REPO_ROOT/assets/menubar-icon@2x.png" "$TMP_APP/Contents/Resources/AppIcon.png"
fi

cat > "$TMP_APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleIdentifier</key>
    <string>${APP_BUNDLE_ID}</string>
    <key>CFBundleName</key>
    <string>MD Viewer</string>
    <key>CFBundleDisplayName</key>
    <string>MD Viewer</string>
    <key>CFBundleExecutable</key>
    <string>${APP_NAME}</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleVersion</key>
    <string>0.3.0</string>
    <key>CFBundleShortVersionString</key>
    <string>0.3.0</string>
    <key>CFBundleIconFile</key>
    <string>AppIcon</string>
    <key>LSMinimumSystemVersion</key>
    <string>11.0</string>
    <key>LSUIElement</key>
    <true/>
    <key>NSHighResolutionCapable</key>
    <true/>
    <key>CFBundleDocumentTypes</key>
    <array>
        <dict>
            <key>CFBundleTypeName</key>
            <string>Markdown Document</string>
            <key>CFBundleTypeRole</key>
            <string>Viewer</string>
            <key>LSHandlerRank</key>
            <string>Alternate</string>
            <key>LSItemContentTypes</key>
            <array>
                <string>net.daringfireball.markdown</string>
            </array>
        </dict>
    </array>
    <key>UTExportedTypeDeclarations</key>
    <array>
        <dict>
            <key>UTTypeIdentifier</key>
            <string>net.daringfireball.markdown</string>
            <key>UTTypeDescription</key>
            <string>Markdown Document</string>
            <key>UTTypeConformsTo</key>
            <array>
                <string>public.plain-text</string>
            </array>
            <key>UTTypeIconFile</key>
            <string>AppIcon</string>
            <key>UTTypeTagSpecification</key>
            <dict>
                <key>public.filename-extension</key>
                <array>
                    <string>md</string>
                    <string>markdown</string>
                    <string>mdx</string>
                </array>
                <key>public.mime-type</key>
                <array>
                    <string>text/markdown</string>
                    <string>text/x-markdown</string>
                </array>
            </dict>
        </dict>
    </array>
</dict>
</plist>
PLIST

# Replace any existing bundle atomically.
if [[ -d "$APP_PATH" ]]; then
    rm -rf "$APP_PATH"
fi
mv "$TMP_APP" "$APP_PATH"

# Strip any quarantine xattrs (otherwise macOS marks the bundle as
# "launch-disabled" in Launch Services and it disappears from Open With).
xattr -dr com.apple.quarantine "$APP_PATH" 2>/dev/null || true

# Ad-hoc codesign the bundle (the linker only produces a partial signature
# for the binary). Without a full bundle signature, Launch Services still
# refuses to surface the app in "Open With".
echo ">> ad-hoc codesigning…"
codesign --force --deep --sign - "$APP_PATH" >/dev/null 2>&1 || \
    echo "   (codesign failed — Open With visibility may be limited)"

# Tell Launch Services about the new bundle. -f forces re-registration
# (so a version bump in Info.plist is picked up) and -R walks the bundle.
# We do NOT nuke the LS database (no -kill / -domain user) so other apps'
# associations stay intact.
LSREGISTER=/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister
"$LSREGISTER" -f -R "$APP_PATH" >/dev/null 2>&1 || true

# Verify Launch Services now sees our UTI mapping for .md files. We use
# the repo README as a probe (it should exist for anyone running this).
README_PATH="$REPO_ROOT/README.md"
if [[ -f "$README_PATH" ]]; then
    # Force Spotlight to re-read so it picks up the new UTI declaration.
    mdimport "$README_PATH" >/dev/null 2>&1 || true
    sleep 1
    md_uti="$(mdls -name kMDItemContentType -raw "$README_PATH" 2>/dev/null || echo '')"
    if [[ "$md_uti" == "net.daringfireball.markdown" ]]; then
        echo ">> .md UTI registered: $md_uti  ✓"
    else
        echo ">> .md UTI is still '$md_uti' — Launch Services DB may be cached."
        echo "   If 'Open With → MD Viewer' is missing, run this once, then re-install:"
        echo "     $LSREGISTER -kill -r -domain local -domain user"
    fi
fi

# Finder caches the Open-With submenu per session; restart it so the new
# binding shows up without a logout. Cheap and reversible.
killall Finder >/dev/null 2>&1 || true

# ---- write LaunchAgent --------------------------------------------------

echo ">> writing LaunchAgent…"
mkdir -p "$LAUNCH_AGENT_DIR" "$LOG_DIR"
cat > "$LAUNCH_AGENT_PLIST" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>${APP_BUNDLE_ID}</string>
    <key>ProgramArguments</key>
    <array>
        <string>${APP_PATH}/Contents/MacOS/${APP_NAME}</string>
        <string>--menubar</string>
        <string>--root</string>
        <string>${ROOT_DIR}</string>
        <string>--port</string>
        <string>${PORT}</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>ProcessType</key>
    <string>Interactive</string>
    <key>StandardOutPath</key>
    <string>${LOG_DIR}/mdviewer.out.log</string>
    <key>StandardErrorPath</key>
    <string>${LOG_DIR}/mdviewer.err.log</string>
</dict>
</plist>
PLIST

# ---- (re)load the agent -------------------------------------------------

UID_NUM=$(id -u)
GUI_DOMAIN="gui/${UID_NUM}"

echo ">> reloading launchd agent…"
launchctl bootout "${GUI_DOMAIN}" "$LAUNCH_AGENT_PLIST" >/dev/null 2>&1 || true
launchctl bootstrap "${GUI_DOMAIN}" "$LAUNCH_AGENT_PLIST"
launchctl enable "${GUI_DOMAIN}/${APP_BUNDLE_ID}" >/dev/null 2>&1 || true
launchctl kickstart -k "${GUI_DOMAIN}/${APP_BUNDLE_ID}" >/dev/null 2>&1 || true

echo
echo "✅ MdViewer installed."
echo "   • Menu-bar icon should appear within a few seconds."
echo "   • Web view: http://127.0.0.1:${PORT}/"
echo "   • Logs:     ${LOG_DIR}/"
echo "   • Uninstall: scripts/uninstall.sh"
