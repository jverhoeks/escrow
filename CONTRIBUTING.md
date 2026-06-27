# Contributing to escrow

## Getting started

1. **Fork** the repo on GitHub.
2. **Clone** your fork and verify it builds:
   ```bash
   go build ./...
   ```
3. **Run unit tests** before and after your changes:
   ```bash
   make test-unit
   ```
4. **Run `go vet`** to catch suspicious code:
   ```bash
   go vet ./...
   ```
5. **Install pre-commit hooks** (optional but recommended):
   ```bash
   pre-commit install
   ```

## What to work on

Open issues are tagged by area: `[PERF]`, `[SECURITY]`, `[TEST]`, `[DOCS]`, `[OPS]`. If you're starting out, look for `good first issue` or `help wanted` labels.

The codebase-review.md has a full audit with labelled findings (C1–C5 critical, H1–H9 high, M1–M10 medium, L1–L7 low). Any of these are fair game.

## Before you submit a PR

- The code must pass `go vet ./...` and `make test-unit`.
- New code should have tests. Exceptions: documentation, trivial wrappers.
- Follow the existing code style — no `gofmt` reformatting (the tree uses non-standard import grouping). When in doubt, match the surrounding file.
- Keep dependencies minimal. If you need a new library, open a discussion first.

## Commit messages

Use conventional commits with scope when relevant:

```
area: short description

Longer explanation if needed. Wrap at 72 chars.
```

Valid areas: `npm`, `pypi`, `gomod`, `cargo`, `composer`, `nuget`, `maven`, `cache`, `dashboard`, `cli`, `egress`, `policy`, `config`, `docs`, `ci`, `tests`, `perf`, `security`.

Examples:

```
dashboard: add CSRF session token to state-changing endpoints

egress: log webhook send errors instead of suppressing them

pypi: fix hyphenated sdist version parsing
```

## PR process

1. Push to your fork and open a PR against `main`.
2. The title should match the commit-message format (`area: description`). If the PR fixes an issue, include `Closes #N` in the body.
3. CI runs `go test -race ./...` and `go vet ./...`. Both must pass.
4. Keep PRs focused on one thing. A PR that fixes a bug and refactors the same area is fine; a PR that fixes a bug in PyPI *and* adds a Composer feature is not.

## Reporting issues

Include the escrow version (`escrow --version` or the binary's `version` label on /healthz), your config (redact secrets), and steps to reproduce. If escrow crashed, include the stack trace from the log.
