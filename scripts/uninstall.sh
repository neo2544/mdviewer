#!/usr/bin/env bash
#
# uninstall.sh — remove the LaunchAgent and MdViewer.app bundle that
# install.sh deployed. Idempotent.

set -euo pipefail

APP_NAME="MdViewer"
APP_BUNDLE_ID="com.jk.mdviewer"
APP_PATH="$HOME/Applications/${APP_NAME}.app"
LAUNCH_AGENT_PLIST="$HOME/Library/LaunchAgents/${APP_BUNDLE_ID}.plist"
LOG_DIR="$HOME/Library/Logs/MdViewer"

UID_NUM=$(id -u)
GUI_DOMAIN="gui/${UID_NUM}"

echo ">> stopping launchd agent (if loaded)…"
launchctl bootout "${GUI_DOMAIN}" "$LAUNCH_AGENT_PLIST" >/dev/null 2>&1 || true

echo ">> removing LaunchAgent plist…"
rm -f "$LAUNCH_AGENT_PLIST"

echo ">> removing MdViewer.app…"
if [[ -d "$APP_PATH" ]]; then
    /System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister \
        -u "$APP_PATH" >/dev/null 2>&1 || true
    rm -rf "$APP_PATH"
fi

echo ">> leaving logs at $LOG_DIR (delete manually if you want)"
echo "✅ MdViewer uninstalled."
