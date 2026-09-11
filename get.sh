#!/bin/sh
# Download one agentpane binary and put it on your PATH.
#
#   curl -fsSL https://raw.githubusercontent.com/simoncoombes/agentpane/main/get.sh | sh
#
# It downloads a release archive for this machine, checks it against the
# published sha256, and installs the binary. It does NOT touch your Claude Code
# settings: wiring the hooks is `agentpane install`, which asks first, and this
# script cannot ask anything because its own stdin is the pipe you fed it.
#
# Environment:
#   AGENTPANE_VERSION   tag to install (default: the latest release)
#   AGENTPANE_BIN_DIR   where to put it (default: ~/.local/bin)
#   AGENTPANE_BASE_URL  where to fetch the assets from (default: the GitHub
#                       release for that tag) — for testing against a mirror

set -eu

REPO="simoncoombes/agentpane"
BIN_DIR="${AGENTPANE_BIN_DIR:-$HOME/.local/bin}"

die() { echo "error: $*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required"; }

need uname
need tar
if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL "$1" -o "$2"; }
  # The tag comes from where /releases/latest redirects to. That is one HEAD
  # request with no API token and no JSON parser, and — unlike the API — it is
  # not rate limited, which matters when everyone behind one office IP installs
  # the same tool.
  latest_tag() {
    url="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest")"
    echo "${url##*/}"
  }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -qO "$2" "$1"; }
  latest_tag() {
    wget -qO- "https://api.github.com/repos/$REPO/releases/latest" |
      sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1
  }
else
  die "curl or wget is required"
fi

# --- what machine is this ----------------------------------------------------

os="$(uname -s)"
case "$os" in
  Darwin) os="darwin" ;;
  Linux)  os="linux" ;;
  *)      die "no prebuilt binary for $os — build from source: go install github.com/$REPO/cmd/agentpane@latest" ;;
esac

arch="$(uname -m)"
case "$arch" in
  arm64|aarch64) arch="arm64" ;;
  x86_64|amd64)  arch="amd64" ;;
  *)             die "no prebuilt binary for $arch — build from source: go install github.com/$REPO/cmd/agentpane@latest" ;;
esac

# --- which release -----------------------------------------------------------

version="${AGENTPANE_VERSION:-}"
if [ -z "$version" ]; then
  version="$(latest_tag)" || version=""
  case "$version" in
    v*) ;;
    *)  die "cannot determine the latest release — set AGENTPANE_VERSION" ;;
  esac
fi

archive="agentpane_${version}_${os}_${arch}.tar.gz"
base="${AGENTPANE_BASE_URL:-https://github.com/$REPO/releases/download/$version}"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "downloading agentpane $version ($os/$arch)"
fetch "$base/$archive" "$tmp/$archive" || die "no such release asset: $archive"

# --- verify ------------------------------------------------------------------

if fetch "$base/checksums.txt" "$tmp/checksums.txt" 2>/dev/null; then
  want="$(grep " $archive\$" "$tmp/checksums.txt" | awk '{print $1}')"
  if [ -n "$want" ]; then
    if command -v sha256sum >/dev/null 2>&1; then
      got="$(sha256sum "$tmp/$archive" | awk '{print $1}')"
    elif command -v shasum >/dev/null 2>&1; then
      got="$(shasum -a 256 "$tmp/$archive" | awk '{print $1}')"
    else
      got=""
      echo "warning: no sha256 tool, skipping checksum" >&2
    fi
    if [ -n "$got" ] && [ "$got" != "$want" ]; then
      die "checksum mismatch for $archive: got $got, expected $want"
    fi
  fi
else
  echo "warning: no checksums.txt in this release, skipping verification" >&2
fi

# --- install -----------------------------------------------------------------

tar -xzf "$tmp/$archive" -C "$tmp"
[ -f "$tmp/agentpane" ] || die "the archive did not contain an agentpane binary"

mkdir -p "$BIN_DIR"
# Replace by rename: an atomic swap, so a running pane keeps the inode it
# started with instead of reading half a new binary.
mv "$tmp/agentpane" "$BIN_DIR/agentpane.new"
chmod 0755 "$BIN_DIR/agentpane.new"
mv "$BIN_DIR/agentpane.new" "$BIN_DIR/agentpane"

echo "installed $BIN_DIR/agentpane ($("$BIN_DIR/agentpane" version))"

case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) echo
     echo "$BIN_DIR is not on your PATH. Add it:"
     echo "    echo 'export PATH=\"$BIN_DIR:\$PATH\"' >> ~/.zshrc && exec zsh"
     ;;
esac

cat <<'NEXT'

Next:
    agentpane install     wire the hooks into ~/.claude/settings.json (it asks first)
    agentpane doctor      check the wiring
    agentpane --demo      see the UI with no Claude Code session at all
NEXT
