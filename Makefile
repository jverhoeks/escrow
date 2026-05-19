CURRENT := $(shell git tag --sort=-version:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$$' | head -1 || echo "v0.0.0")
NEXT_PATCH := $(shell bash scripts/bump-version.sh patch $(CURRENT))
NEXT_MINOR := $(shell bash scripts/bump-version.sh minor $(CURRENT))
NEXT_MAJOR := $(shell bash scripts/bump-version.sh major $(CURRENT))

.DEFAULT_GOAL := help

.PHONY: help build test \
        release-patch release-minor release-major \
        tag tap

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
	  | awk 'BEGIN{FS=":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'
	@echo ""
	@echo "  Current version : $(CURRENT)"
	@echo "  Next patch      : $(NEXT_PATCH)"
	@echo "  Next minor      : $(NEXT_MINOR)"
	@echo "  Next major      : $(NEXT_MAJOR)"

# ── Build ──────────────────────────────────────────────────────────────────────

build: ## Cross-compile binaries for all platforms (darwin/linux/windows × amd64/arm64)
	@bash scripts/build-all.sh $(CURRENT)

# ── Test ───────────────────────────────────────────────────────────────────────

test: ## Run integration tests against a live escrow instance (needs ESCROW_DIR)
	@bash tests/test-escrow-full.sh

# ── Release ────────────────────────────────────────────────────────────────────
#
# release-patch  →  direct commit to main, tag, GitHub release, tap  ($(NEXT_PATCH))
# release-minor  →  open PR first; run `make tag` after merge         ($(NEXT_MINOR))
# release-major  →  open PR first; run `make tag` after merge         ($(NEXT_MAJOR))

release-patch: ## Release $(NEXT_PATCH) — build, commit, tag, release, tap (no PR)
	@bash scripts/release.sh $(NEXT_PATCH)

release-minor: ## Release $(NEXT_MINOR) — build, open PR; run 'make tag VERSION=$(NEXT_MINOR)' after merge
	@bash scripts/release.sh $(NEXT_MINOR) --pr

release-major: ## Release $(NEXT_MAJOR) — build, open PR; run 'make tag VERSION=$(NEXT_MAJOR)' after merge
	@bash scripts/release.sh $(NEXT_MAJOR) --pr

tag: ## Tag VERSION, create GitHub release, update tap  (use after merging a release PR)
ifndef VERSION
	$(error VERSION is required — run: make tag VERSION=vX.Y.Z)
endif
	@bash scripts/tag-release.sh $(VERSION)
	@bash scripts/update-homebrew-tap.sh $(VERSION)
	@echo "🎉 Done — https://github.com/jverhoeks/escrow/releases/tag/$(VERSION)"

# ── Tap ────────────────────────────────────────────────────────────────────────

tap: ## Update Homebrew tap to current version ($(CURRENT))
	@bash scripts/update-homebrew-tap.sh $(CURRENT)
