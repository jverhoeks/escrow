# Dashboard: Egress Observability + Tabbed Settings — Design

**Date:** 2026-06-05
**Status:** Approved (pending spec review)

## Problem

Three dashboard/UI gaps, all surfacing now that the egress proxy ([2026-06-03 spec](2026-06-03-docker-build-protection-design.md)) has shipped:

1. **Settings page is too big.** `renderSettings()` (`internal/dashboard/static/index.html`) renders *every* config section flat in one scroll — `[server]`, `[storage]`, `[dashboard]`, policy, allow/block, rescan, alerts, cache, egress_proxy… It's unwieldy.
2. **The egress proxy has no metrics.** `internal/egress` emits zero Prometheus metrics, and there's no at-a-glance view of egress request/allow/block/volume.
3. **Egress events have no home of their own.** They're recorded into the **shared** `eventlog` as `kind=egress`, mixed into the package-decision Live Feed. A build hitting hundreds of hosts floods that ring (evicting package decisions) and bloats `events.jsonl`, yet there's no dedicated egress log view. Egress is also conceptually distinct from the **access log** (client→escrow HTTP) and **upstream log** (escrow→registry).

## Goals

1. **Tab the Settings page** into grouped sub-tabs (frontend only; `/api/settings` shape unchanged).
2. **Egress metrics** — dashboard cards (total/allow/block/distinct-hosts/bytes), top allowed + top blocked hosts, an allow-vs-block requests-over-time chart, plus **Prometheus counters** at `/metrics`.
3. **Dedicated egress log** (`internal/egresslog`) with a **live** dashboard view (SSE), separate from the access/upstream/event logs.
4. **TUI parity** — an Egress view in `escrow-cli tui`.

## Non-goals

- Splitting the monolithic `index.html` into multiple files (unrelated refactor — extend in place).
- Changing egress proxy *behavior* (policy, transparent mode, MITM) — this is observability only.
- Transparent-mode-specific metrics — Phase-1 egress (explicit/forward + CONNECT) is the scope.

## Key decisions (from brainstorming)

| Decision | Choice |
|---|---|
| Egress storage | **Dedicated `internal/egresslog` store** (ring + optional file), *not* the shared eventlog |
| Egress ↔ eventlog | Egress records **leave** the shared eventlog — the package Live Feed no longer mixes them in; they get their own view |
| Metrics surfaced | **All**: cards (total/allow/block/distinct/bytes) + top-hosts tables + requests-over-time chart + Prometheus counters |
| Recording timing | **Record allow at connection *open*** (symmetric with blocks) so the live view/SSE reflects long-lived allowed connections immediately — not only at teardown |
| Bytes | An **aggregate counter** (egresslog accumulator `AddBytes` + Prometheus `EgressBytesTotal`), incremented at close — *not* a per-event field (the per-event record fires at open, before bytes are known). The "bytes" card reads `Stats.Bytes`. Must preserve the half-close teardown |
| Settings | **Sub-tabs inside the Settings view** (client-side), no API change |
| TUI | **Include egress parity** this iteration |

## Architecture

### 1. `internal/egresslog` (new) — dedicated egress log store

Mirrors `internal/upstreamlog`/`eventlog` (newest-first ring, optional JSONL file, SSE subscribers):

```go
type Event struct {
	Timestamp time.Time `json:"timestamp"`
	Host      string    `json:"host"`              // SNI/CONNECT host or absolute-URI host
	IP        string    `json:"ip,omitempty"`      // resolved/dialed dst IP (from dialChecked)
	Verb      string    `json:"verb"`              // "CONNECT" | "GET" | "POST" | …
	Action    string    `json:"action"`            // "allow" | "block"
	Reason    string    `json:"reason"`            // "tunnel" | "forward" | "blacklisted" | "not in whitelist" | "unresolvable"
	Bytes     int64     `json:"bytes,omitempty"`   // bytes transferred (allow path)
}

type Stats struct {
	Total, Allowed, Blocked int
	DistinctHosts           int
	Bytes                   int64
	TopAllowed, TopBlocked  []HostCount      // host + count, sorted desc
	Series                  []Bucket         // {t, allow, block} per bucket
}

func New(cap int) *Log
func NewWithPath(cap int, path string) (*Log, error)         // optional persistence
func (l *Log) Record(e Event)                                 // prepend, trim to cap, broadcast to subscribers
func (l *Log) Recent(n int, action string) []Event           // newest-first, optional allow/block filter
func (l *Log) Subscribe() (<-chan Event, func())             // SSE fan-out (bounded buffered chan)
func (l *Log) Stats(window time.Duration, bucket time.Duration) Stats
```

- Default cap ~5000 (like upstreamlog); optional `egress_log_path` (top-level config, like `eventlog_path`) for persistence — in-memory otherwise.
- `Stats` is computed over the in-memory ring (bounded), mirroring how `timeseries.go` derives from eventlog.

### 2. Egress proxy + metrics wiring

- `egress.New(addr, policy, egressLog)` — the proxy's `record()` now writes an `egresslog.Event` (host/IP/verb/action/reason/bytes) **instead of** `eventlog.PackageEvent{Kind:egress}`. Remove the `eventlog` dependency from the proxy.
- **Bytes:** capture from the `io.Copy` return values on the allow path. CONNECT tunnel = sum of both directions; HTTP forward = response-body bytes. Must not alter the half-close teardown (the goroutine-closes-other-conn fix) — capture the `int64` each `io.Copy` returns, sum after both complete, then one `Record`.
- **Prometheus** (`internal/metrics`, existing promauto pattern): `EgressRequestsTotal` (`CounterVec{action}`) + `EgressBytesTotal` (`Counter`), incremented in the proxy at decision/teardown.
- `cmd/escrow/main.go`: construct the egresslog (cap + optional path), pass it to **both** `egress.New(...)` and `dashboard.New(...)`.

### 3. Dashboard — Egress view

New protected routes in `Dashboard.Mount()` (mirroring `handleUpstreamLog`/`handleStream`/`handleTimeseries`):

- `GET /api/egress/log?n=&action=` → recent egress events (JSON).
- `GET /api/egress/stats?window=24h&bucket=1h` → `egresslog.Stats` for the cards/top-hosts/chart.
- `GET /api/egress/stream` → SSE from `egresslog.Subscribe()` (reuse the no-write-deadline + 15s-ping pattern of `handleStream`).

New `view-egress` in `index.html` + an "Egress" nav tab:
- **Cards:** total / allowed / blocked / distinct hosts / bytes.
- **Top hosts:** two tables (top allowed, top blocked).
- **Chart:** allow-vs-block requests over time (reuse the Analytics chart lib/pattern).
- **Live log:** table fed by initial `/api/egress/log` + `/api/egress/stream` SSE, with an all/allow/block filter (mirrors the Live Feed wiring: `connect()`/`prependRow()`).

`dashboard.New(...)` gains an `*egresslog.Log` parameter.

### 4. Tabbed Settings (frontend only)

Refactor `renderSettings()` to render a horizontal sub-tab strip inside `view-settings` and show one group at a time (local `settingsTab` state; same save flow). Groups:

- **General** — server, storage, dashboard
- **Policy** — policy (age/osv/publisher/popularity), allow, block, rescan
- **Egress** — egress_proxy
- **Advanced** — alerts/webhooks, cireport, cache, paths (eventlog_path, egress_log_path, …)

No backend change — `/api/settings` still returns the full nested config; grouping is presentation only. Unknown/new sections fall back to an "Advanced/Other" group so nothing disappears.

### 5. TUI parity (`cmd/escrow-cli/tui`)

- `client.go`: typed fetchers `EgressLog(n, action)`, `EgressStats()`, and `EgressStream(ctx)` (the SSE one uses the **separate no-timeout stream client** from `b15701e`).
- `model.go`/`views.go`: an "Egress" tab — compact stats header (total/allow/block + top hosts) + a live egress log list, consuming `/api/egress/*`.

## Data flow

```
egress decision (allow/block) + bytes
        │
        ▼
egresslog.Record(Event) ──► in-memory ring (+ optional file)
        │                  ──► SSE broadcast ──► dashboard /api/egress/stream + TUI EgressStream
        └──► metrics: EgressRequestsTotal{action}.Inc(), EgressBytesTotal.Add(n)
dashboard/TUI: initial Recent() + Stats(); subscribe for live.
```

## Error handling / edge cases

- **Bytes vs half-close:** the allow event is `Record`ed at open; byte capture happens *after* teardown via `AddBytes(up+dn)` and must not reintroduce the tunnel goroutine/connection leak — keep the existing teardown (goroutine closes the other conn), read `up` after `<-done` (race-free: `close(done)` happens-before the read).
- **Egress leaves the eventlog:** the package Live Feed + `/api/events` no longer include `kind=egress` (acceptable — egress was only added recently; it now has a dedicated view). `eventlog.KindEgress` may be retired or left unused.
- **nil-safe:** proxy guards a nil egresslog (as it does today for the eventlog).
- **High volume:** bounded ring (cap); optional file append for retention; `Stats` computed over the ring only.
- **SSE:** dashboard reuses the no-deadline + ping pattern; TUI uses its no-timeout stream client (avoids the b15701e 10s drop).
- **Settings tabs:** purely client-side; a new/unknown config section lands in "Advanced" rather than vanishing.

## Testing

- `internal/egresslog`: Record/Recent/Subscribe/Stats (ring trim, allow/block filter, top-hosts aggregation, time buckets, concurrency); file round-trip for `NewWithPath`.
- `internal/egress`: `record()` writes the right egresslog fields incl. bytes; CONNECT bytes = both directions; Prometheus counters incremented; half-close still leak-free under `-race`.
- Dashboard: `/api/egress/log` + `/api/egress/stats` shapes + auth; `/api/egress/stream` delivers an event over SSE.
- TUI: client fetchers parse the endpoints; egress view renders (smoke, no-TTY guard).
- Settings tabs + egress view: verify via a running instance with Playwright (tab switching, live egress rows, cards/chart populate).

## Build sequencing (phased)

1. `internal/egresslog` store + `Stats` + tests.
2. Wire egress proxy → egresslog (+ bytes) + Prometheus counters; `main.go` construct/pass; remove egress-from-eventlog; update egress tests.
3. Dashboard endpoints (`/api/egress/log` `/stats` `/stream`) + handlers + tests.
4. Dashboard `view-egress` (cards, top hosts, chart, live log) + nav tab.
5. Tabbed Settings (frontend refactor).
6. TUI egress tab (client fetchers + view).
7. Docs — `docs/dashboard.md` egress view; cross-link from `docs/docker.md`.

## Open risks

- **`index.html` growth** — already 2358 lines; this adds a view + settings-tab logic. Accepted (extend in place; a split is unrelated scope) but noted.
- **Byte-counting correctness** in the tunnel is the trickiest bit — it touches the carefully-fixed half-close path; covered by a `-race` regression test.
- **Stats cost** — computed per request over the ring; bounded by cap, but if cap grows large, memoize/window. Fine at ~5000.
- **Dropping egress from the shared feed** — anything that consumed `kind=egress` via `/api/events`/`/api/stream` stops seeing it; this is new enough that the dedicated view is strictly better.
