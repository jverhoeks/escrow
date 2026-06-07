# `escrow-cli` Cross-Platform Binary Release — Design (retroactive)

**Date:** 2026-06-03
**Status:** Implemented (documented after the fact — commit `219c04e`)

> Retroactive spec: this records the design of work already shipped, so the release model is
> captured alongside the feature specs. Not a forward-looking proposal.

## Problem

`escrow-cli` (the companion that wires a machine's package managers to the proxy, runs the
firewall redirect, and hosts the terminal dashboard) was **build-from-source only** — fine for
contributors with a Go toolchain, but a barrier for two real users:

1. **Operators installing the daemon as a binary** (the `curl … escrow-<os>-<arch>` path) had no
   matching one-line way to get `escrow-cli`.
2. **The GitHub Action** (`setup-escrow`) needs `escrow-cli` on the runner to configure the
   ecosystems; building it from source on every CI run is slow and wasteful.

The daemon already shipped as committed, cross-compiled binaries; `escrow-cli` should follow the
same model.

## Key decisions

| Decision | Choice | Why |
|---|---|---|
| Target platforms | **darwin + linux × amd64 + arm64** (4) — **Unix-only** | `escrow-cli` uses `syscall.Kill`/`Flock` for service control and pf/iptables for the firewall redirect; there is no meaningful Windows behavior. The **daemon** still builds a Windows target (it's a portable server) — the asymmetry is intentional. |
| Distribution | **Commit the built binaries into the repo at tag time**, like the daemon | One consistent release model; `curl raw.githubusercontent…/escrow-cli-<os>-<arch>` works with no release-asset plumbing. |
| Source archives | **`export-ignore` the binaries** in `.gitattributes` | Homebrew builds `escrow-cli` from source (the formula compiles both `cmd/escrow` and `cmd/escrow-cli`), so committed binaries would add ~100 MB of dead weight to every `git archive` tarball. |
| Action wiring | `setup-escrow`/`action.yml` install the released `escrow-cli` and bump their default version on release | CI gets the CLI without a Go build. |
| Release trigger | **Manual, explicit** (`make release-*` / `make tag`) | Per project policy — no auto-release; binaries are built and committed only when a release is explicitly cut. |

## Architecture / mechanism

### Cross-compile — `scripts/build-all.sh`

One `build()` helper cross-compiles every target with `-ldflags "-s -w -X main.version=…"`:

- **Daemon** (`./cmd/escrow`): darwin/linux/windows × amd64/arm64.
- **CLI** (`./cmd/escrow-cli`): darwin/linux × amd64/arm64 — **no Windows**, with an inline
  comment recording why (syscall/flock + firewall are Unix-only).

Output names follow `escrow-<os>-<arch>` and `escrow-cli-<os>-<arch>` (daemon Windows target is
`escrow-windows-amd64.exe`).

### Repo hygiene — `.gitattributes` + `.gitignore`

- `.gitattributes`: `export-ignore` for `escrow`, `escrow-cli`, and all `escrow-*`/`escrow-cli-*`
  binaries + `escrow-cache/` — keeps source archives lean while the binaries still live on
  `main` for direct download.
- `.gitignore`: the local dev-built `escrow`/`escrow-cli` are ignored except where force-added by
  the release script.

### Release pipeline — `scripts/release.sh` + `Makefile`

`make release-patch|minor|major` → `scripts/release.sh`:
1. **Preflight** — refuse if the tag exists or the working tree has uncommitted *source* changes
   (binaries/cache are explicitly ignored in the dirty check, since the script rebuilds them).
2. **Build** all binaries (`build-all.sh`).
3. **Commit** the binaries (`git add -f …`) and bump the default `version` in `action.yml` +
   `.github/actions/setup-escrow/action.yml` to the new tag.
4. **PR or direct** — patch goes direct to `main`; minor/major open a release PR, then `make tag
   VERSION=vX.Y.Z` after merge.
5. **Tag → GitHub release → Homebrew tap** (`tag-release.sh` + `update-homebrew-tap.sh`).

### GitHub Action install

`setup-escrow` resolves `escrow-<os>-<arch>` (and the CLI) from the pinned release tag (default
bumped each release), restores the package cache, starts the proxy, and configures the requested
ecosystems — so a workflow gets a working proxy + CLI with no Go build.

## Testing / verification

- `scripts/build-all.sh <version>` produces all 4 CLI + 5 daemon artifacts (size-reported).
- `smoke-test.sh` exercises a built binary.
- The Action is exercised by `test-action.yml` (which, per `96630c5`, no longer runs the
  example workflow and pins its dependencies — separate CI-hardening change).

## Open risks / notes

- **Binaries on `main`** grow repo size over time; `export-ignore` keeps *archives* lean but the
  Git history still carries them — accepted, same trade-off as the daemon.
- **No Windows `escrow-cli`** is a deliberate platform gap, not an oversight; the daemon covers
  Windows for the server role.
- **Manual release only** — there is intentionally no CI step that tags/releases automatically.
