#!/usr/bin/env bash
# Cross-compile escrow for all supported platforms.
# Usage: bash scripts/build-all.sh [version]
set -euo pipefail

VERSION="${1:-$(git describe --tags --abbrev=0 2>/dev/null || echo "dev")}"
LDFLAGS="-s -w -X main.version=${VERSION}"

echo "Building escrow ${VERSION} for all platforms..."

build() {
  local GOOS="$1" GOARCH="$2" OUT="$3"
  printf "  %-30s" "${OUT}"
  GOOS="$GOOS" GOARCH="$GOARCH" go build -ldflags "$LDFLAGS" -o "$OUT" ./cmd/escrow
  echo "$(du -sh "$OUT" | cut -f1)"
}

build darwin  amd64 escrow-darwin-amd64
build darwin  arm64 escrow-darwin-arm64
build linux   amd64 escrow-linux-amd64
build linux   arm64 escrow-linux-arm64
build windows amd64 escrow-windows-amd64.exe

echo ""
echo "✅ All binaries built for ${VERSION}"
