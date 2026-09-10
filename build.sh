#!/bin/bash
# Build script: builds the Vue frontend and embeds the static assets into the
# Go backend binary, then compiles the single self-contained terminal backend.
#
#   ./build.sh            # dev (default, non-stripped)
#   ./build.sh release 1.0.1   # release build (strip + -ldflags + version)
#
# The backend serves these embedded assets over its unix socket under the
# configured baseurl (TERMINAL_ADMIN_BASEURL) fronted by nginx/other proxies.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BACKEND_DIR="${ROOT}/backend"
FRONTEND_DIR="${ROOT}/frontend"
EMBED_DIR="${BACKEND_DIR}/embed"

export GOCACHE="${GOCACHE:-${ROOT}/.gocache}"
export GOPATH="${GOPATH:-${ROOT}/.gopath}"
export GOFLAGS="-buildvcs=false"
mkdir -p "$GOCACHE" "$GOPATH"

echo "==> Building frontend..."
# The baseurl is handled at runtime by the backend (not baked into the binary),
# so build with vite's relative base (./assets/...).
( cd "$FRONTEND_DIR" && npm install --no-audit --no-fund && npm run build )

echo "==> Copying frontend dist into embed dir..."
rm -rf "$EMBED_DIR"
mkdir -p "$EMBED_DIR"
cp -r "$FRONTEND_DIR"/dist/* "$EMBED_DIR"/

echo "==> Building Go binary..."
TERMINAL_VERSION="${2:-1.0.0}"
LDFLAGS="-X main.terminalVersion=${TERMINAL_VERSION}"
OUT="${BACKEND_DIR}/terminal"
if [ "${1:-}" = "release" ]; then
  LDFLAGS="-s -w -linkmode=external -X main.terminalVersion=${TERMINAL_VERSION}"
fi
go clean -cache
( cd "$BACKEND_DIR" && go build -trimpath -ldflags "$LDFLAGS" -o "$OUT" . )

echo "==> Cleaning embed dir..."
rm -rf "$EMBED_DIR"

echo "==> Done: ${OUT}"
ls -lh "$OUT"