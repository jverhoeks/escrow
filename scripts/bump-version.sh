#!/usr/bin/env bash
# Bump a semver tag.  Usage: bump-version.sh <patch|minor|major> <vX.Y.Z>
set -euo pipefail
TYPE="${1:-patch}"
TAG="${2:-v0.0.0}"
VERSION="${TAG#v}"
IFS='.' read -r MAJOR MINOR PATCH <<< "$VERSION"
case "$TYPE" in
  major) echo "v$((MAJOR+1)).0.0" ;;
  minor) echo "v${MAJOR}.$((MINOR+1)).0" ;;
  patch) echo "v${MAJOR}.${MINOR}.$((PATCH+1))" ;;
  *) echo "Usage: $0 <patch|minor|major> <vX.Y.Z>" >&2; exit 1 ;;
esac
