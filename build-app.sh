#!/usr/bin/env bash
# Build MdViewer.app — a macOS app bundle that registers MD Viewer as a handler
# for Markdown files, so double-clicking a .md in Finder opens it in the viewer.
#
# The Apple-Event open-file plumbing already lives in menubar_darwin.{go,m};
# this script just produces the .app + Info.plist (with CFBundleDocumentTypes)
# and a colorful .icns so Launch Services can offer MD Viewer for .md files.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

APP="MdViewer.app"
ICON_SVG="assets/app-icon.svg"
ICNS="assets/AppIcon.icns"
BUNDLE_ID="com.neo2544.mdviewer"
APP_NAME="MD Viewer"

if ! command -v iconutil >/dev/null 2>&1; then
  echo "error: iconutil not found (this script targets macOS)." >&2
  exit 1
fi

echo "[1/4] icon → $ICNS"
if command -v rsvg-convert >/dev/null 2>&1; then
  ICONSET="$(mktemp -d)/AppIcon.iconset"
  mkdir -p "$ICONSET"
  for sz in 16 32 128 256 512; do
    rsvg-convert -w "$sz" -h "$sz" "$ICON_SVG" -o "$ICONSET/icon_${sz}x${sz}.png"
    rsvg-convert -w "$((sz * 2))" -h "$((sz * 2))" "$ICON_SVG" -o "$ICONSET/icon_${sz}x${sz}@2x.png"
  done
  iconutil -c icns "$ICONSET" -o "$ICNS"
  rm -rf "$(dirname "$ICONSET")"
elif [ -f "$ICNS" ]; then
  echo "  rsvg-convert missing — reusing committed $ICNS"
else
  echo "error: need rsvg-convert (brew install librsvg) to generate $ICNS" >&2
  exit 1
fi

echo "[2/4] build binary → $APP"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
go build -o "$APP/Contents/MacOS/mdviewer" .
cp "$ICNS" "$APP/Contents/Resources/AppIcon.icns"

echo "[3/4] Info.plist"
cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleExecutable</key><string>mdviewer</string>
  <key>CFBundleIdentifier</key><string>$BUNDLE_ID</string>
  <key>CFBundleName</key><string>$APP_NAME</string>
  <key>CFBundleDisplayName</key><string>$APP_NAME</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleIconFile</key><string>AppIcon</string>
  <key>CFBundleShortVersionString</key><string>1.0</string>
  <key>CFBundleVersion</key><string>1</string>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
  <key>LSUIElement</key><true/>
  <key>NSHighResolutionCapable</key><true/>
  <key>CFBundleDocumentTypes</key>
  <array>
    <dict>
      <key>CFBundleTypeName</key><string>Markdown Document</string>
      <key>CFBundleTypeRole</key><string>Viewer</string>
      <key>LSHandlerRank</key><string>Alternate</string>
      <key>CFBundleTypeExtensions</key>
      <array>
        <string>md</string><string>markdown</string><string>mdown</string>
        <string>mkd</string><string>markdn</string><string>mdwn</string><string>mdtext</string>
      </array>
      <key>LSItemContentTypes</key>
      <array><string>net.daringfireball.markdown</string><string>public.markdown</string></array>
    </dict>
  </array>
</dict>
</plist>
PLIST

plutil -lint "$APP/Contents/Info.plist" >/dev/null

echo "[4/4] register with Launch Services"
LSREGISTER="/System/Library/Frameworks/CoreServices.framework/Versions/A/Frameworks/LaunchServices.framework/Versions/A/Support/lsregister"
[ -x "$LSREGISTER" ] && "$LSREGISTER" -f "$APP" || echo "  (lsregister not run; macOS will pick it up on next login or when moved to /Applications)"

echo
echo "Built $APP"
echo
echo "Make MD Viewer the default app for .md files:"
echo "  • Finder: right-click any .md → Get Info → 'Open with' → $APP_NAME → 'Change All…'"
echo "  • or one-liner:  brew install duti && duti -s $BUNDLE_ID .md all"
echo
echo "Then double-clicking a .md opens it in MD Viewer (menu-bar app + browser)."
