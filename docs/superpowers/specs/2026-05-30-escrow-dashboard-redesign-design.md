# Escrow Dashboard Redesign — Design

**Date:** 2026-05-30
**Status:** Approved (pending spec review)

## Problem

The current web dashboard works but is hard to read: dark-only, monospace, and status
is carried by red/green icons — exactly the pairing that fails for color-blind users.
It has only two views (Live Feed, Packages) and a single-select ecosystem filter. It
exposes none of the access/upstream activity and no historical trend.

## Goals

1. Full redesign with **light/dark mode** (follow OS default + persistent manual toggle)
   and a **color-blind-safe** status system.
2. **Live log** of package events (allowed / warned / blocked).
3. Two **24-hour trend charts** — allowed per ecosystem and denied per ecosystem —
   with **multi-select ecosystem** filtering (default all).
4. **Access log** viewer and **upstream fetch log** viewer.
5. **Package tree**: ecosystem → namespace/package → version, showing size, CVE,
   block/allow action, and reason.
6. Dedicated **CVE page** for packages blocked because of vulnerabilities.

## Non-goals

- Dependency-graph ("transitive sub-package") tracking — the package tree groups by
  *namespace*, not by who-required-whom.
- Full per-package size coverage across all ecosystems (see Caveats).
- Any change to the proxy/trust decision logic itself.
- A frontend build pipeline — the dashboard stays vanilla JS/CSS embedded via `go:embed`.

## Key decisions (from brainstorming)

| Decision | Choice |
|---|---|
| Scope | Full-stack (frontend + new backend instrumentation/endpoints) + dedicated CVE page |
| "Upstream logs" | New escrow→upstream **fetch log** (target, status, bytes, latency); plus surface the existing access log |
| Package tree | **Namespace grouping** (eco → namespace/name → version), collapses to package→version for flat/unscoped ecosystems |
| Navigation | **Left sidebar** (icon + label), top bar for global controls |
| Status colors | **Traffic-light, CVD-tuned** hues, *always* paired with icon (✓ / ! / ✕) + text label |
| Default theme | **Follow OS** (`prefers-color-scheme`), manual toggle persisted in `localStorage` |
| 24h charts | **Stacked columns** (hourly), multi-select ecosystems, default all |
| Package size | **Best-effort** from cache blobs (npm/cargo/nuget only) — see Caveats |
| CVE data source | **Durable**: structured vulns captured onto the event at decision time |

## Architecture

### Build order

The design is one cohesive redesign, but built backend-first:

1. Backend data model + capture (size, structured vulns, upstream log ring buffer).
2. New/changed API endpoints, tested with the existing `internal/dashboard/handlers_test.go` pattern.
3. Frontend rewrite consuming the real endpoints.

This avoids debugging mock-vs-real API shape mismatches during the UI work.

### Backend

#### Data model — `eventlog.PackageEvent`

The event log is append-only JSONL; new fields are additive and backward-compatible
(old lines simply lack them).

- Add `Vulns []Vuln` where `Vuln = { ID string, Severity string }`.
- Captured at trust-decision time so the CVE page survives the OSV cache's 24h TTL
  (the page is a security audit surface and must show packages last seen long ago).
- Namespace is **derived at read time** from the package name; it is not stored.

To populate `Vulns`:

- Add a structured `Vulns []Vuln` field to `trust.SignalReport`.
- `OSVSignal.toReport` already computes matching IDs + severities — have it fill `Vulns`
  in addition to the existing free-text `Reason`.
- The handler that records the `PackageEvent` copies `Vulns` from the OSV report onto the event.

#### New package — `internal/upstreamlog`

In-memory ring buffer mirroring `internal/eventlog` (same capacity/subscribe/snapshot
shape, no persistence required).

- `UpstreamEvent = { Timestamp, Ecosystem, Method, URL, Status, Bytes, MS }`.
- `server.LoggingTransport.RoundTrip` records an event **only for known package-registry
  hosts** (registry.npmjs.org, pypi.org, crates.io, repo1.maven.org, packagist.org,
  api.nuget.org, proxy.golang.org, etc.). This filter is required because the same
  logging-wrapped `http.Client` is shared with the OSV/popularity/publisher signals, whose
  calls (e.g. `api.osv.dev`) do not map to an ecosystem.
- Ecosystem is derived from the request host.
- Everything recorded here is a cache **MISS** by definition (cache hits never reach upstream).

#### Cache — size support

- Add `BlobSize(ctx, key) int64` to the `cache.Cache` interface.
- Implement in `memory`, `disk`, and `s3` backends (returns 0 / -1 when absent).

#### API endpoints (all under existing dashboard auth)

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/stats/timeseries?window=24h&bucket=1h` | Per-ecosystem, per-action hourly buckets for the stacked-column charts |
| GET | `/api/accesslog?n=&since=` | Parse the on-disk Apache-combined access log to JSON, **filtering out the dashboard's own path** |
| GET | `/api/upstreamlog?n=&eco=` | Read the upstream-fetch ring buffer |
| GET | `/api/cves` | Block events where `signal=osv`, grouped by vuln ID / package / severity |
| GET | `/api/packages` (extended) | Namespace-tree shape + `size`, collapsing to package→version for flat ecosystems |

Wiring: the dashboard must receive the access-log path and the upstream-log buffer from
`cmd/escrow/main.go` (today it gets neither).

### Frontend (full rewrite, vanilla, `go:embed`)

- **Layout**: left sidebar (icon + label per view); top bar holds global controls
  (multi-select ecosystem filter, theme toggle, LIVE badge).
- **Theme**: CSS custom properties + `data-theme` attribute. Defaults to
  `prefers-color-scheme`; manual toggle persists in `localStorage`. **Applied to
  `login.html` as well as `index.html`.**
- **Status coding**: traffic-light CVD-tuned hues, *always* icon (✓ allowed / ! warned /
  ✕ blocked) + text label + color. Color is never the sole signal.
- **Views**:
  - **Overview** — live feed + allowed/warned/blocked counts + two 24h stacked-column charts.
  - **Packages** — namespace tree (eco → namespace/name → version) with size, CVE, action, reason.
  - **CVEs** — dedicated page of CVE-blocked packages.
  - **Access log** — readable table of client→escrow requests.
  - **Upstream log** — table of escrow→upstream fetches (cache misses).
  - **Allow / Block** — management of allow/block lists.
- **Multi-select ecosystem filter** (default all): the SSE stream sends all events; filtering
  is done client-side (replaces today's single-`eco` stream param).
- **Charts**: hand-rolled SVG stacked columns (no chart library), ecosystems stacked in a
  fixed order with a labelled legend.

## Caveats

### Package size is best-effort

The trust *decision* event fires on the **manifest/metadata** fetch, while the artifact
bytes arrive on a **separate, later download** request — they are different requests, so
size cannot simply be attached to the decision event. The reliable, cheap path is to read
size from the **cache blob**, mirroring how the existing `Cached` flag works in
`handlePackages` / `blobCached`. Consequence:

- Size is shown **only for cached packages in npm / cargo / nuget** (predictable cache keys).
- pypi / go / maven show "—", exactly as `Cached` does today.

Full per-package size coverage would require instrumenting all seven handler download paths
and correlating downloads back to packages — deferred to a later iteration.

### Access-log self-reference

Because the access-log middleware is mounted globally (before the dashboard), the
dashboard's own SSE/polling traffic appears in the access log. The viewer endpoint filters
out the configured dashboard path so the log is readable.

## Testing & verification

- Go unit tests for: `BlobSize` (each backend), the timeseries aggregation, the access-log
  parser (incl. dashboard-path filtering), the upstream-log host filter, the CVE grouping,
  and the namespace-tree shaping — following the existing `handlers_test.go` style.
- **Playwright** screenshots of light + dark themes and the CVD status palette, to verify
  color-blind readability (a visual requirement unit tests cannot catch) before claiming done.

## Open risks

- Exact set of "known registry hosts" must match the configured upstream URLs; derive from
  `opts.UpstreamURLs` where possible rather than hard-coding.
- Namespace derivation rules differ per ecosystem (npm `@scope/name`, Maven `groupId/artifactId`,
  Go module path, NuGet dotted names); flat ecosystems (pypi, cargo) must not render an empty parent.
