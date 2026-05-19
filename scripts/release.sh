#!/usr/bin/env bash
# Full release pipeline: build → commit → PR (optional) → tag → GitHub release → tap.
#
# Usage:
#   bash scripts/release.sh v1.5.0          # direct commit to main, no PR
#   bash scripts/release.sh v1.5.0 --pr     # open a PR first; tag/release after merge
#
# Called via Makefile:
#   make release-patch   →  scripts/release.sh $(NEXT_PATCH)
#   make release-minor   →  scripts/release.sh $(NEXT_MINOR) --pr
#   make release-major   →  scripts/release.sh $(NEXT_MAJOR) --pr
set -euo pipefail

VERSION="${1:-}"
MODE="${2:-}"   # --pr or empty

if [[ -z "$VERSION" ]]; then
  echo "Usage: $0 <vX.Y.Z> [--pr]" >&2; exit 1
fi
[[ "$VERSION" != v* ]] && VERSION="v${VERSION}"

REPO="jverhoeks/escrow"
BRANCH="release/${VERSION}"

# ── Preflight ──────────────────────────────────────────────────────────────────
echo "→ Release ${VERSION}  (mode: ${MODE:-direct})"
if git tag | grep -q "^${VERSION}$"; then
  echo "ERROR: tag ${VERSION} already exists" >&2; exit 1
fi
# Check for uncommitted source changes — ignore binaries and cache files
# (binaries are built fresh by this script; cache files are runtime artefacts)
DIRTY=$(git status --porcelain \
  | grep -v '^??' \
  | grep -vE '^.M (escrow$|escrow-darwin|escrow-linux|escrow-windows|escrow-cache/)' \
  | grep -vE '^ D escrow-cache/' \
  || true)
if [[ -n "$DIRTY" ]]; then
  echo "ERROR: working tree has uncommitted source changes:" >&2
  echo "$DIRTY" >&2
  echo "Commit or stash them before releasing." >&2
  exit 1
fi

# ── 1. Build all binaries ─────────────────────────────────────────────────────
echo ""
echo "── 1/5  Building binaries ──────────────────────────────────────────────"
bash scripts/build-all.sh "$VERSION"

# ── 2. Commit binaries + update action version defaults ───────────────────────
echo ""
echo "── 2/5  Committing binaries + updating action version defaults ─────────"

# Bump the default version in both action.yml files so new users get this version
PREV_VERSION=$(git tag --sort=-version:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -1 || echo "v0.0.0")
for f in action.yml .github/actions/setup-escrow/action.yml; do
  sed -i '' "s|default: '${PREV_VERSION}'|default: '${VERSION}'|g" "$f" 2>/dev/null \
    || sed -i   "s|default: '${PREV_VERSION}'|default: '${VERSION}'|g" "$f"
done

git add -f escrow-darwin-amd64 escrow-darwin-arm64 \
             escrow-linux-amd64 escrow-linux-arm64 \
             escrow-windows-amd64.exe \
             action.yml \
             .github/actions/setup-escrow/action.yml

if [[ "$MODE" == "--pr" ]]; then
  git checkout -b "$BRANCH"
fi

git commit -m "chore: build binaries for ${VERSION}"

# ── 3. Push & PR (optional) or push direct ────────────────────────────────────
echo ""
echo "── 3/5  Pushing ────────────────────────────────────────────────────────"

if [[ "$MODE" == "--pr" ]]; then
  git push -u origin "$BRANCH"
  PR_URL=$(gh pr create \
    --repo "$REPO" \
    --base main \
    --head "$BRANCH" \
    --title "release: ${VERSION}" \
    --body "$(cat <<EOF
## Release ${VERSION}

Automated release PR — built binaries included.

### Changes since last release
$(git log "$(git describe --tags --abbrev=0 HEAD~1 2>/dev/null || git rev-list --max-parents=0 HEAD)"..HEAD --oneline | head -20)

---
_Merge this PR, then the release tag and Homebrew tap update will be created automatically._
EOF
    )")
  echo ""
  echo "✅ PR opened: ${PR_URL}"
  echo ""
  echo "After merging, run:"
  echo "  git checkout main && git pull && make tag VERSION=${VERSION}"
  exit 0
fi

# Direct mode: push to main
git push origin main

# ── 4. Tag + GitHub release ───────────────────────────────────────────────────
echo ""
echo "── 4/5  Tagging + GitHub release ───────────────────────────────────────"
bash scripts/tag-release.sh "$VERSION"

# ── 5. Homebrew tap ───────────────────────────────────────────────────────────
echo ""
echo "── 5/5  Updating Homebrew tap ──────────────────────────────────────────"
bash scripts/update-homebrew-tap.sh "$VERSION"

echo ""
echo "🎉 Released ${VERSION}"
echo "   https://github.com/${REPO}/releases/tag/${VERSION}"
