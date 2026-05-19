#!/usr/bin/env bash
# Create git tag, floating major tag, and GitHub release.
# Usage: bash scripts/tag-release.sh v1.5.0
set -euo pipefail

VERSION="${1:-}"
[[ -z "$VERSION" ]] && { echo "Usage: $0 <vX.Y.Z>" >&2; exit 1; }
[[ "$VERSION" != v* ]] && VERSION="v${VERSION}"

REPO="jverhoeks/escrow"
MAJOR="v$(echo "${VERSION#v}" | cut -d. -f1)"
PREV=$(git tag --sort=-version:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -1 \
        || git rev-list --max-parents=0 HEAD)

# Changelog from previous tag
CHANGELOG=$(git log "${PREV}..HEAD" --oneline | head -30)

# Exact version tag
git tag "$VERSION"
git push origin "$VERSION"

# Floating major tag (e.g. v1 → always points to latest v1.x)
git tag -f "$MAJOR"
git push origin "$MAJOR" --force

echo "  tagged ${VERSION} and ${MAJOR}"

# Write release notes to temp file to avoid heredoc/quoting issues
NOTES_FILE=$(mktemp /tmp/release-notes-XXXX.md)
trap "rm -f $NOTES_FILE" EXIT
{
  echo "## What's changed"
  echo ""
  echo "${CHANGELOG}"
  echo ""
  echo "---"
  echo "**Full changelog**: https://github.com/${REPO}/compare/${PREV}...${VERSION}"
} > "$NOTES_FILE"

gh release create "$VERSION" \
  --repo "$REPO" \
  --title "$VERSION" \
  --notes-file "$NOTES_FILE"

echo "  release created: https://github.com/${REPO}/releases/tag/${VERSION}"
