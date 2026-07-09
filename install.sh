#!/usr/bin/env sh
set -eu

REPO_URL="${PRS_REPO_URL:-}"
INSTALL_DIR="${PRS_INSTALL_DIR:-$HOME/.prs}"
BIN_DIR="${PRS_BIN_DIR:-$HOME/.local/bin}"

echo "Installing prs..."

if [ -x /usr/local/go/bin/go ]; then
  GO=/usr/local/go/bin/go
else
  GO=go
fi

if ! command -v "$GO" >/dev/null 2>&1; then
  echo "go toolchain is required (looked for /usr/local/go/bin/go and 'go' on PATH)" >&2
  exit 1
fi

# Detect whether this script is being run from inside an already-checked-out
# copy of the repo (a go.mod sits next to it). When piped via `curl | sh`,
# $0 is generally not a real file path, so this naturally falls through to
# the "not a local checkout" case.
SCRIPT_DIR=""
if [ -f "$0" ]; then
  SCRIPT_DIR=$(cd "$(dirname "$0")" 2>/dev/null && pwd) || SCRIPT_DIR=""
fi

if [ -n "$SCRIPT_DIR" ] && [ -f "$SCRIPT_DIR/go.mod" ]; then
  echo "Building from local checkout at $SCRIPT_DIR"
  BUILD_DIR="$SCRIPT_DIR"
  ( cd "$BUILD_DIR" && "$GO" build -ldflags "-s -w" -o prs ./cmd/prs )
else
  if [ -z "$REPO_URL" ]; then
    echo "PRS_REPO_URL is not set and no local checkout was detected." >&2
    echo "Either set PRS_REPO_URL to the git URL for prs, or run 'make install' from a local checkout instead." >&2
    exit 1
  fi

  if ! command -v git >/dev/null 2>&1; then
    echo "git is required" >&2
    exit 1
  fi

  if [ -d "$INSTALL_DIR/.git" ]; then
    echo "Updating existing installation in $INSTALL_DIR"
    git -C "$INSTALL_DIR" fetch --depth=1 origin
    git -C "$INSTALL_DIR" reset --hard origin/HEAD
  else
    echo "Cloning $REPO_URL to $INSTALL_DIR"
    rm -rf "$INSTALL_DIR"
    git clone --depth=1 "$REPO_URL" "$INSTALL_DIR"
  fi

  BUILD_DIR="$INSTALL_DIR"
  ( cd "$BUILD_DIR" && "$GO" build -ldflags "-s -w" -o prs ./cmd/prs )
fi

mkdir -p "$BIN_DIR"
install -m 0755 "$BUILD_DIR/prs" "$BIN_DIR/prs"

echo "prs installed to $BIN_DIR/prs"
echo "Ensure $BIN_DIR is on PATH"
