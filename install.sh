#!/bin/sh
# Install the artifacta CLI on Linux or macOS.
#
#   curl -fsSL https://raw.githubusercontent.com/agarwalvivek29/artifacta/main/install.sh | sh
#
# Options (environment variables or --flags via `sh -s -- <flags>`):
#   ARTIFACTA_VERSION=0.0.1      --version 0.0.1   pin a version (highest precedence)
#   ARTIFACTA_DEPLOYMENT=<url>   --url <url>        install the version your deployment
#                                                   runs (queries <url>/version) — the
#                                                   guaranteed-compatible choice
#   ARTIFACTA_INSTALL_DIR=/usr/local/bin           install location (default:
#                                                   /usr/local/bin, then sudo, then
#                                                   ~/.local/bin)
#
# Match a deployment (recommended):
#   curl -fsSL .../install.sh | ARTIFACTA_DEPLOYMENT=https://artifacta.example sh
#
# Windows users: download the .zip from the GitHub Releases page, or use the
# container image ghcr.io/agarwalvivek29/artifacta-cli.
set -eu

REPO="agarwalvivek29/artifacta"
BIN="artifacta"
INSTALL_DIR="${ARTIFACTA_INSTALL_DIR:-/usr/local/bin}"

err() { echo "install: $*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || err "required tool not found: $1"; }
need curl
need tar

# ── detect OS / architecture ────────────────────────────────────────────────
os=$(uname -s)
arch=$(uname -m)
case "$os" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) err "unsupported OS: $os — use the Windows release asset or the container image" ;;
esac
case "$arch" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) err "unsupported architecture: $arch" ;;
esac

# ── resolve version ─────────────────────────────────────────────────────────
# Precedence: explicit pin (ARTIFACTA_VERSION / --version) > deployment match
# (ARTIFACTA_DEPLOYMENT / --url, queries <url>/version) > latest GitHub release.
ver="${ARTIFACTA_VERSION:-}"
url="${ARTIFACTA_DEPLOYMENT:-}"
while [ $# -gt 0 ]; do
  case "$1" in
    --version) ver="${2:-}"; shift 2 ;;
    --url) url="${2:-}"; shift 2 ;;
    *) shift ;;
  esac
done

if [ -z "$ver" ] && [ -n "$url" ]; then
  echo "Querying deployment ${url} for its version…"
  ver=$(curl -fsSL "${url%/}/version" \
    | grep -o '"version"[[:space:]]*:[[:space:]]*"[^"]*"' \
    | sed -E 's/.*"([^"]*)"$/\1/' | head -1)
  [ -n "$ver" ] || err "could not read the deployed version from ${url%/}/version"
  echo "Deployment runs ${ver#v} — installing the matching CLI for guaranteed compatibility."
fi
if [ -z "$ver" ]; then
  ver=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
    | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name": *"v?([^"]+)".*/\1/')
  [ -n "$ver" ] || err "could not determine the latest version; set ARTIFACTA_VERSION"
fi
ver="${ver#v}"

asset="artifacta_${ver}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/v${ver}"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "Downloading artifacta ${ver} (${os}/${arch})…"
curl -fsSL "$base/$asset" -o "$tmp/$asset" || err "download failed: $base/$asset"

# ── verify checksum (best effort) ───────────────────────────────────────────
if curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt" 2>/dev/null; then
  if command -v sha256sum >/dev/null 2>&1; then
    got=$(sha256sum "$tmp/$asset" | awk '{print $1}')
  elif command -v shasum >/dev/null 2>&1; then
    got=$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')
  else
    got=""
  fi
  if [ -n "$got" ]; then
    want=$(grep "$asset" "$tmp/checksums.txt" | awk '{print $1}' | head -1)
    [ -n "$want" ] && [ "$got" = "$want" ] || err "checksum verification failed for $asset"
    echo "Checksum verified."
  fi
fi

# ── extract + install ───────────────────────────────────────────────────────
tar -xzf "$tmp/$asset" -C "$tmp"
[ -f "$tmp/$BIN" ] || err "archive did not contain the $BIN binary"
chmod +x "$tmp/$BIN"

if mkdir -p "$INSTALL_DIR" 2>/dev/null && [ -w "$INSTALL_DIR" ]; then
  mv "$tmp/$BIN" "$INSTALL_DIR/$BIN"
elif command -v sudo >/dev/null 2>&1; then
  echo "Installing to $INSTALL_DIR (needs sudo)…"
  sudo mkdir -p "$INSTALL_DIR" && sudo mv "$tmp/$BIN" "$INSTALL_DIR/$BIN"
else
  INSTALL_DIR="$HOME/.local/bin"
  mkdir -p "$INSTALL_DIR"
  mv "$tmp/$BIN" "$INSTALL_DIR/$BIN"
  echo "Note: installed to $INSTALL_DIR — make sure it is on your PATH."
fi

echo "Installed $("$INSTALL_DIR/$BIN" version 2>/dev/null || echo "artifacta") to $INSTALL_DIR/$BIN"
echo "Get started:  artifacta help"
