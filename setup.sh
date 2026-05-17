#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

need_cmd() {
  command -v "$1" >/dev/null 2>&1
}

install_homebrew_package() {
  local pkg="$1"
  if need_cmd brew; then
    if brew list "$pkg" >/dev/null 2>&1; then
      echo "[skip] brew package already installed: $pkg"
    else
      echo "[install] brew install $pkg"
      brew install "$pkg"
    fi
  else
    echo "[error] Homebrew is not installed. Install Homebrew first: https://brew.sh"
    exit 1
  fi
}

install_npm_package() {
  local pkg="$1"
  if need_cmd npm; then
    if npm list -g "$pkg" >/dev/null 2>&1; then
      echo "[skip] npm package already installed: $pkg"
    else
      echo "[install] npm install -g $pkg"
      npm install -g "$pkg"
    fi
  else
    echo "[error] npm is not installed. Install Node.js first: https://nodejs.org"
    exit 1
  fi
}

echo "Checking mdviewer preview toolchain..."

install_homebrew_package chafa
install_npm_package @mermaid-js/mermaid-cli

echo
echo "Tool status:"
command -v chafa >/dev/null 2>&1 && echo "  chafa: $(command -v chafa)"
command -v mmdc >/dev/null 2>&1 && echo "  mmdc:  $(command -v mmdc)"

echo
echo "Done. Run the viewer with:"
echo "  ./run.sh"
echo "  ./run-web.sh"
