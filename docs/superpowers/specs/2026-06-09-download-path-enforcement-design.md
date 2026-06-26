# Design — Download-path policy enforcement (#35)

**Date:** 2026-06-09 · **Issue:** [#35](https://github.com/jverhoeks/escrow/issues/35) · **Branch:** `fix/audit-35-download-enforcement`

## Problem

escrow's request-time enforcement is incomplete in two coupled ways:

1. The full trust engine (age + OSV + publisher + popularity) is wired onto every
   handler as `h.engine`, but every handler overrides it with the age-only
   `listingEngine` (`eng := h.engine; if h.listingEngine != nil { eng = h.listingEngine }`),
   so `h.engine` is **dead code at request time** and OSV/publisher/popularity never run.
2. The artifact-download endpoints (`ServeTarball`, `ServeFile`, cargo/nuget
   `serveDownload`, maven `serveArtifactFrom`, gomod `serveZip`) fetch from
   cache/upstream and serve with **no policy check and no blocklist consultation**,
   recording a hardcoded `Action: "allow"` event.

Consequence: a manually blocklisted version, a rescan-auto-blocked version (retroactive
CVE), or an OSV-flagged version is still downloadable from its artifact endpoint. For
PyPI/NuGet — where escrow rewrites artifact URLs to itself — that endpoint *is* the
normal install path. This contradicts `docs/security.md` ("cannot be installed by
`--force` or by a dependency resolver fallback").

## Goal

Enforce policy on every artifact-download path, on **both** the cache-hit and
cache-miss branches, returning `403` for a blocked version before any artifact bytes are
served. Restore the intended design: **listing = age-only (fast), download = full engine**.

Non-goals (tracked separately): changing OSV fail-open semantics (#49), cache
invalidation on block (#38), OSV CVSS-array severity (#47). This issue keeps current
fail-open behavior and only adds the missing enforcement point.

## Design

### 1. Shared gate helper — `internal/gate`

A new package so the six handlers don't duplicate the gate logic and it can be unit-tested
in isolation.

```go
package gate

// Check evaluates pkg through the full trust engine + policy (allow/blocklist + signals),
// records the real decision as a KindDownloaded event, updates metrics, and returns it.
// Callers serve the artifact only when Action != ActionBlock.
func Check(ctx context.Context, eng *trust.Engine, pol *policy.Engine,
    evlog *eventlog.Log, pkg trust.Package) policy.Decision
```

- Runs `eng.Check(ctx, pkg)` → `pol.Evaluate(result)`.
- Records a `KindDownloaded` event carrying the **true** action/signal/reason/vulns
  (replacing the handler's hardcoded `recordDownload("allow")` — also fixes the
  forensic-log item in #51). Exactly one event per serve attempt.
- Increments `metrics.BlocksTotal` on a block. It does **not** bump
  `metrics.RequestsTotal` — that counter currently means "versions evaluated while
  building a manifest" (the listing path); folding download evaluations into it would
  conflate two different things. A dedicated download-eval metric is deferred to #41.
- Returns the `Decision` so the handler can branch.

Dependencies: `trust`, `policy`, `eventlog`, `metrics`. No import cycle (handlers import
`gate`; `gate` imports the leaf packages).

### 2. Handler integration

In each download handler, at the point where `recordDownload()` is called today (which
already runs on both cache-hit and cache-miss paths), replace the unconditional
record-allow with:

```go
pkg := trust.Package{Ecosystem: ECO, Name: name, Version: version /* + PublishedAt/Author when cached */}
if gate.Check(r.Context(), h.engine, h.policy, h.evlog, pkg).Action == policy.ActionBlock {
    http.Error(w, "blocked by policy", http.StatusForbidden)
    return
}
// ... serve artifact (gate already recorded the allow/warn event) ...
```

The gate must run **before** writing artifact bytes. On a cache hit, evaluate before the
`io.Copy(w, blob)`. On a miss, evaluate before/at the upstream fetch so a blocked version
is never fetched or served.

Handlers stop selecting `listingEngine` on the download path — they use the full
`h.engine`. The listing path is unchanged (still age-only via `listingEngine`).

### 3. Package construction (metadata nuance)

Each handler builds a `trust.Package{Ecosystem, Name, Version}` — all three are already
parsed at the download endpoint for `recordDownload` (npm `versionFromTarball`, pypi
`pkgVersionFromFilename`, cargo/nuget URL params, maven `mavenCoordsFromPath`, gomod
unescape). No `PublishedAt`/`Author` is populated.

Signal behavior that follows:

| Signal | Inputs | At download |
|--------|--------|-------------|
| allowlist / blocklist | eco, name, version | **always enforced** |
| OSV | eco, name, version | **always enforced** (cache-backed; one cold API call then cached) |
| age | PublishedAt | not re-run (PublishedAt zero ⇒ passes) — already enforced at listing |
| publisher | author | not re-run (no author at download ⇒ skip) |
| popularity | download stats | not re-run (skip) |

**Implementation note (revised from the original "enrich from cache" plan):** populating
`PublishedAt`/`Author` at download was dropped as redundant. In the normal flow a
too-young version is already filtered out of the listing, so the client never resolves it
and never requests the artifact; in the direct/pinned-fetch case there is no cached
metadata to enrich from. So the concrete, complete enforcement the download gate adds is
**blocklist + OSV** — the two controls that were actually missing — and that needs only
`(eco, name, version)`. This is the "accept residual" choice, made uniform across all six
handlers (no per-handler metadata parsing, no fetch on the hot path).

### 4. Block response & events

- Response: `403 Forbidden`, short plaintext body, matching gomod `.info`
  (`gomod/handler.go:154-156`). No artifact bytes written.
- Event: `KindDownloaded` with the true `Action` (allow/warn/block), signal, reason, and
  vulns — no more hardcoded `"allow"`.

### 5. Fail semantics & performance

- Fail-open OSV/signal-error behavior is **unchanged** (that is #49). `policy.Evaluate`
  already maps `SignalSkip`→continue and `SignalError`→`strict_signals`.
- Unparseable `(name, version)` → cannot identify → serve, log at debug. Rare, since the
  existing `recordDownload` already depends on this parse.
- Performance: steady-state cost is a cache lookup (OSV cached in the same `cache.Cache`).
  First cold OSV per (name,version) adds one API call, then cached. Cache-hit artifact
  serves now also evaluate — acceptable and necessary (a cached blocked blob must not be
  served).

### Composer

Composer artifacts download from the Packagist/GitHub CDN via `dist.url`, not through
escrow (documented limitation in `docs/security.md`). The composer **metadata** path
already gates via `versionAllowed`. No download-path change applies; out of scope.

## Testing (TDD — red first)

Per handler (npm, pypi, cargo, nuget, maven, gomod), table tests on the artifact endpoint:

1. Blocklisted version → `403` on **cache-miss** and on **cache-hit** (pre-populated blob).
2. Rescan-auto-blocked version (added to blocklist at runtime) → `403` without restart/flush.
3. OSV-flagged version (stub OSV signal returns a vuln ≥ min_severity) → `403`.
4. Clean version → `200` + a `KindDownloaded` event with `Action: allow`.

Plus a `gate` unit test: block decision → block event recorded + metrics bumped; allow →
allow event; warn → allow-to-serve + warn event.

## Acceptance criteria (from #35)

- A blocklisted version returns 403 on its artifact endpoint for npm/pypi/cargo/nuget/maven/go.
- A rescan auto-block is enforced on the next download without a restart or cache flush.
- `docs/security.md` matches implemented behavior (the manifest-removal + download-403 model).
- The dead `h.engine`/`listingEngine` override on the download path is gone; listing stays age-only.

## Files touched (anticipated)

- **New:** `internal/gate/gate.go` (+ `gate_test.go`)
- **Edit:** `internal/handler/{npm,pypi,cargo,nuget,maven,gomod}/handler.go` (+ tests)
- **Edit:** `docs/security.md` (align the guarantee wording with the download-403 model)
