#!/usr/bin/env bash
# Update the Homebrew tap formula for a new escrow release.
#
# Usage:
#   bash scripts/update-homebrew-tap.sh v1.1.0
#
# What it does:
#   1. Downloads the tarball for the given tag from GitHub
#   2. Computes the SHA256
#   3. Copies Formula/escrow.rb from this repo into the tap (full sync),
#      then stamps the correct url + sha256 for the release tag
#   4. Commits and pushes the tap
#
# Prerequisites:
#   - The git tag must already exist and be pushed to github.com/jverhoeks/escrow
#   - The homebrew-tap repo must be cloned at ../homebrew-tap (or set TAP_DIR)
set -euo pipefail

VERSION="${1:-}"
if [[ -z "$VERSION" ]]; then
  echo "Usage: $0 <version>   e.g. $0 v1.1.0" >&2
  exit 1
fi
# Strip leading 'v' for display, keep full tag for URLs
TAG="$VERSION"
[[ "$TAG" != v* ]] && TAG="v$TAG"

TAP_DIR="${TAP_DIR:-$(dirname "$(dirname "$0")")/../homebrew-tap}"
FORMULA="$TAP_DIR/Formula/escrow.rb"

if [[ ! -f "$FORMULA" ]]; then
  echo "ERROR: formula not found at $FORMULA" >&2
  echo "Clone the tap first: git clone git@github.com:jverhoeks/homebrew-tap.git $TAP_DIR" >&2
  exit 1
fi

TARBALL_URL="https://github.com/jverhoeks/escrow/archive/refs/tags/${TAG}.tar.gz"
TMPFILE=$(mktemp /tmp/escrow-XXXXXX.tar.gz)
trap "rm -f $TMPFILE" EXIT

echo "→ Downloading $TARBALL_URL ..."
curl -fL "$TARBALL_URL" -o "$TMPFILE"

SHA256=$(shasum -a 256 "$TMPFILE" | awk '{print $1}')
echo "→ SHA256: $SHA256"

# Full sync: copy the canonical formula from this repo, then stamp url + sha256.
# This keeps the tap's service block, post_install, caveats, and default_config
# in lockstep with the source — patching only url/sha256 caused silent drift.
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cp "$SCRIPT_DIR/../Formula/escrow.rb" "$FORMULA"
sed -i '' \
  -e "s|url \"https://github.com/jverhoeks/escrow/archive/refs/tags/.*\"|url \"$TARBALL_URL\"|" \
  -e "s|sha256 \".*\"|sha256 \"$SHA256\"|" \
  "$FORMULA"

echo "→ Updated formula:"
grep -E "url|sha256|version" "$FORMULA" | head -4

cd "$TAP_DIR"
git diff Formula/escrow.rb
git add Formula/escrow.rb
git commit -m "feat: update escrow to ${TAG}"
git push origin main

echo ""
echo "✅ Tap updated. Users get it with:"
echo "   brew update && brew upgrade escrow"
