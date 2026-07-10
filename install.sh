#!/usr/bin/env sh
set -eu

# prs installer.
#
# Default (e.g. `curl -fsSL .../install.sh | sh`): download the prebuilt
# release binary matching this machine's OS/arch and install it — no Go
# toolchain required.
#
# Falls back to building from source (which requires Go 1.26+) when:
#   - run from inside a local checkout of the repo (builds your working tree),
#   - no prebuilt binary matches this OS/arch,
#   - no release has been published yet, or the download fails, or
#   - PRS_FROM_SOURCE=1 is set.
#
# Env knobs:
#   PRS_BIN_DIR      install location            (default: ~/.local/bin)
#   PRS_VERSION      release tag to install      (default: latest release)
#   PRS_FROM_SOURCE  set to 1 to force a source build
#   PRS_REPO_URL     git URL for source builds   (default: the public repo)
#   PRS_INSTALL_DIR  where source builds clone to (default: ~/.prs)

REPO="cosmicbuffalo/prs"
BIN_DIR="${PRS_BIN_DIR:-$HOME/.local/bin}"
REPO_URL="${PRS_REPO_URL:-https://github.com/${REPO}.git}"
INSTALL_DIR="${PRS_INSTALL_DIR:-$HOME/.prs}"

echo "Installing prs..."

# ---- helpers --------------------------------------------------------------

have() { command -v "$1" >/dev/null 2>&1; }

fetch_stdout() {
  # $1 = url; body to stdout, non-zero on failure
  if have curl; then
    curl -fsSL "$1"
  elif have wget; then
    wget -qO- "$1"
  else
    echo "need curl or wget to download" >&2
    return 1
  fi
}

fetch_file() {
  # $1 = url, $2 = dest
  if have curl; then
    curl -fsSL -o "$2" "$1"
  elif have wget; then
    wget -qO "$2" "$1"
  else
    echo "need curl or wget to download" >&2
    return 1
  fi
}

sha256_of() {
  if have sha256sum; then
    sha256sum "$1" | awk '{print $1}'
  elif have shasum; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    echo ""
  fi
}

detect_os() {
  case "$(uname -s)" in
    Linux) echo linux ;;
    Darwin) echo darwin ;;
    *) echo "" ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64 | amd64) echo amd64 ;;
    arm64 | aarch64) echo arm64 ;;
    *) echo "" ;;
  esac
}

go_bin() {
  if [ -x /usr/local/go/bin/go ]; then
    echo /usr/local/go/bin/go
  else
    echo go
  fi
}

install_binary() {
  # $1 = path to a prs binary to install
  mkdir -p "$BIN_DIR"
  install -m 0755 "$1" "$BIN_DIR/prs"
  echo "prs installed to $BIN_DIR/prs"
  echo "Ensure $BIN_DIR is on PATH"
}

# ---- source build (fallback) ----------------------------------------------

build_from_source() {
  GO=$(go_bin)
  if ! have "$GO"; then
    echo "the Go toolchain is required to build from source (looked for /usr/local/go/bin/go and 'go' on PATH)" >&2
    exit 1
  fi

  # Build a local checkout if this script sits next to a go.mod; otherwise
  # clone the repo first.
  script_dir=""
  if [ -f "$0" ]; then
    script_dir=$(cd "$(dirname "$0")" 2>/dev/null && pwd) || script_dir=""
  fi

  if [ -n "$script_dir" ] && [ -f "$script_dir/go.mod" ]; then
    echo "Building from local checkout at $script_dir"
    build_dir="$script_dir"
  else
    if ! have git; then
      echo "git is required to build from source" >&2
      exit 1
    fi
    if [ -d "$INSTALL_DIR/.git" ]; then
      echo "Updating existing checkout in $INSTALL_DIR"
      git -C "$INSTALL_DIR" fetch --depth=1 origin
      git -C "$INSTALL_DIR" reset --hard origin/HEAD
    else
      echo "Cloning $REPO_URL to $INSTALL_DIR"
      rm -rf "$INSTALL_DIR"
      git clone --depth=1 "$REPO_URL" "$INSTALL_DIR"
    fi
    build_dir="$INSTALL_DIR"
  fi

  ( cd "$build_dir" && "$GO" build -ldflags "-s -w" -o prs ./cmd/prs )
  install_binary "$build_dir/prs"
}

# ---- prebuilt binary (default) --------------------------------------------

# Returns non-zero if a prebuilt install isn't possible or fails, so the
# caller can fall back to a source build.
install_prebuilt() {
  os=$(detect_os)
  arch=$(detect_arch)
  if [ -z "$os" ] || [ -z "$arch" ]; then
    echo "no prebuilt binary for $(uname -s)/$(uname -m)" >&2
    return 1
  fi

  tag="${PRS_VERSION:-}"
  if [ -z "$tag" ]; then
    tag=$(fetch_stdout "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null \
      | grep '"tag_name"' | head -1 \
      | sed -E 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/') || tag=""
  fi
  if [ -z "$tag" ]; then
    echo "no published release found" >&2
    return 1
  fi

  asset="prs_${os}_${arch}.tar.gz"
  base="https://github.com/${REPO}/releases/download/${tag}"
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT

  echo "Downloading ${asset} (${tag})"
  if ! fetch_file "${base}/${asset}" "$tmp/$asset"; then
    echo "failed to download ${asset}" >&2
    return 1
  fi

  # Best-effort checksum verification (hard-fail only on an actual mismatch).
  if fetch_file "${base}/checksums.txt" "$tmp/checksums.txt" 2>/dev/null; then
    expected=$(grep " ${asset}\$" "$tmp/checksums.txt" | awk '{print $1}')
    actual=$(sha256_of "$tmp/$asset")
    if [ -n "$expected" ] && [ -n "$actual" ]; then
      if [ "$expected" != "$actual" ]; then
        echo "checksum mismatch for ${asset}" >&2
        return 1
      fi
      echo "checksum verified"
    fi
  fi

  tar -xzf "$tmp/$asset" -C "$tmp"
  if [ ! -f "$tmp/prs" ]; then
    echo "archive did not contain a prs binary" >&2
    return 1
  fi
  install_binary "$tmp/prs"
}

# ---- main -----------------------------------------------------------------

if [ "${PRS_FROM_SOURCE:-}" = "1" ]; then
  build_from_source
  exit 0
fi

# A local checkout means a developer probably wants their working tree built.
main_script_dir=""
if [ -f "$0" ]; then
  main_script_dir=$(cd "$(dirname "$0")" 2>/dev/null && pwd) || main_script_dir=""
fi
if [ -n "$main_script_dir" ] && [ -f "$main_script_dir/go.mod" ]; then
  build_from_source
  exit 0
fi

if install_prebuilt; then
  exit 0
fi

echo "Falling back to building from source..."
build_from_source
