#!/usr/bin/env bash
# Cross-compile escrow for all supported platforms.
# Usage: bash scripts/build-all.sh [version]
set -euo pipefail

VERSION="${1:-$(git describe --tags --abbrev=0 2>/dev/null || echo "dev")}"
LDFLAGS="-s -w -X main.version=${VERSION}"

echo "Building escrow ${VERSION} for all platforms..."

build() {
  local GOOS="$1" GOARCH="$2" OUT="$3" PKG="${4:-./cmd/escrow}"
  printf "  %-30s" "${OUT}"
  GOOS="$GOOS" GOARCH="$GOARCH" go build -ldflags "$LDFLAGS" -o "$OUT" "$PKG"
  echo "$(du -sh "$OUT" | cut -f1)"
}

# escrow daemon
build darwin  amd64 escrow-darwin-amd64
build darwin  arm64 escrow-darwin-arm64
build linux   amd64 escrow-linux-amd64
build linux   arm64 escrow-linux-arm64
build windows amd64 escrow-windows-amd64.exe

# escrow-cli — system configuration + terminal dashboard.
# Unix-only (uses syscall.Kill/Flock for service control + firewall setup),
# so no Windows target — unlike the daemon above.
build darwin  amd64 escrow-cli-darwin-amd64 ./cmd/escrow-cli
build darwin  arm64 escrow-cli-darwin-arm64 ./cmd/escrow-cli
build linux   amd64 escrow-cli-linux-amd64  ./cmd/escrow-cli
build linux   arm64 escrow-cli-linux-arm64  ./cmd/escrow-cli

echo ""
echo "✅ All binaries built for ${VERSION}"
