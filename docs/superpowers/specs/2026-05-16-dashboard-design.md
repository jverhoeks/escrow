# Escrow Dashboard + Metrics Wiring Design

**Date:** 2026-05-16  
**Status:** Approved

---

## Goal

1. Wire Prometheus counters (`escrow_requests_total`, `escrow_blocks_total`) into the npm and PyPI handler call sites so metrics reflect real traffic.
2. Add a password-protected web dashboard showing real-time package events with ecosystem filtering, dark terminal visual style, and SSE-based live updates.

---

## Part 1: Metrics Wiring (small, mechanical)

In both `internal/handler/npm/handler.go` (`filterManifest`) and `internal/handler/pypi/handler.go` (`versionAllowed`), after the policy decision is computed, add:

```go
metrics.RequestsTotal.WithLabelValues(string(pkg.Ecosystem), string(decision.Action)).Inc()
if decision.Action == policy.ActionBlock {
    metrics.BlocksTotal.WithLabelValues(string(pkg.Ecosystem), decision.Signal).Inc()
}
```

No new files. No design needed beyond the call site placement.

---

## Part 2: Dashboard

### New packages

```
internal/
  eventlog/
    log.go          Ring buffer (cap 500) of PackageEvent; fan-out to SSE subscribers
  dashboard/
    auth.go         Login/logout + HMAC-SHA256 session cookie
    handlers.go     Dashboard HTTP routes (page, SSE stream, events API, stats)
    embed.go        go:embed static/ directory into the binary
    static/
      index.html    Single-page dashboard (dark terminal style, vanilla JS)
```

### Config addition

New `[dashboard]` section in `sentinel.toml`:

```toml
[dashboard]
  enabled  = true
  path     = "/dashboard"     # URL prefix
  username = "admin"
  password = "changeme"
  secret   = "<32-char random string for cookie HMAC>"
```

**First-boot config generation:** If `sentinel.toml` does not exist on startup, escrow generates it with safe defaults, a random 32-byte hex secret, and prints to stdout:

```
Generated sentinel.toml — dashboard credentials:
  username: admin
  password: <random 12-char password>
  url:      http://0.0.0.0:8888/dashboard
```

The generated file is written once and never overwritten.

---

### EventLog (`internal/eventlog/log.go`)

```go
type PackageEvent struct {
    Timestamp  time.Time
    Ecosystem  string          // "npm" | "pypi"
    Package    string          // "lodash@4.17.21"
    Action     string          // "block" | "warn" | "allow"
    Signal     string          // "age" | "osv" | "publisher" | "popularity" | ""
    Reason     string          // human-readable reason, empty on allow
}

type Log struct {
    mu          sync.RWMutex
    events      []PackageEvent  // ring buffer, cap 500, newest first
    subscribers []chan PackageEvent
}

func New() *Log
func (l *Log) Record(e PackageEvent)     // append + broadcast to all subscribers
func (l *Log) Events(eco string) []PackageEvent  // filter by ecosystem ("" = all)
func (l *Log) Subscribe() (<-chan PackageEvent, func())  // returns channel + unsubscribe func
func (l *Log) Stats() Stats             // blocked/warned/allowed counts + top blocked
```

**Ring buffer behaviour:** When capacity (500) is exceeded, oldest event is dropped. Thread-safe. Subscribers receive events via buffered channels (capacity 64); slow subscribers are dropped silently rather than blocking.

---

### Auth (`internal/dashboard/auth.go`)

- Session cookie name: `escrow_session`
- Cookie value: `HMAC-SHA256(username + ":" + expiry_unix, secret)` — base64-encoded
- Cookie lifetime: 24h, `HttpOnly`, `SameSite=Lax`
- Routes:
  - `POST /dashboard/login` — verify username/password from config, set cookie, redirect to `/dashboard/`
  - `GET  /dashboard/logout` — clear cookie, redirect to login
  - All other `/dashboard/*` routes: middleware checks cookie validity, redirects to login on failure

No external auth library. The HMAC verification is ~10 lines of Go.

---

### Routes (`internal/dashboard/handlers.go`)

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/dashboard/` | ✓ | Serves `index.html` (the SPA) |
| `GET` | `/dashboard/login` | — | Login form |
| `POST` | `/dashboard/login` | — | Authenticate, set cookie |
| `GET` | `/dashboard/logout` | ✓ | Clear cookie |
| `GET` | `/dashboard/api/stream?eco=` | ✓ | SSE stream of PackageEvents, filtered by ecosystem |
| `GET` | `/dashboard/api/events?eco=&n=100` | ✓ | Last N events as JSON (initial page load) |
| `GET` | `/dashboard/api/stats` | ✓ | Blocked/warned/allowed counts + top blocked |

---

### Frontend (`internal/dashboard/static/index.html`)

Single HTML file, embedded in the binary with `go:embed`. Vanilla JS only — no framework, no build step.

**Layout:**
- Top bar: `ESCROW` logo + "● LIVE" badge + username + logout button
- Nav row (same line): tabs (`Live Feed | Packages | Stats`) + ecosystem filter chips (`All | npm | PyPI`)
- Main area: feed panel (flex:1) + stats sidebar (240px fixed)
- Feed panel: column headers + scrolling event rows (time, eco tag, package, signal/reason, status)
- Stats sidebar: three counters (blocked/warned/allowed) + top blocked list
- Policy strip at bottom: active policy settings in dim text

**Ecosystem filter:** Clicking a chip sets `currentEco` in JS state; if an SSE connection is open, it reconnects with `?eco=npm` (or `pypi`, or empty for all). Chips visually toggle (green for npm, blue for PyPI).

**SSE handling:**
```js
function connect(eco) {
    const url = '/dashboard/api/stream' + (eco ? '?eco=' + eco : '');
    const es = new EventSource(url);
    es.onmessage = e => prependEvent(JSON.parse(e.data));
    es.onerror = () => setTimeout(() => connect(eco), 3000); // auto-reconnect
}
```

**Initial load:** On page load, fetch `/dashboard/api/events` (last 100 events) to populate the feed before SSE connects.

**Status colours:**
- `BLOCKED` → `#ef4444` (red)
- `WARNED`  → `#f59e0b` (amber)
- `ALLOWED` → `#22c55e` (green)

**Ecosystem tag colours:**
- `npm`  → green tint (`#4ade80` text, dark green border)
- `pypi` → blue tint (`#93c5fd` text, dark blue border)

---

### Wiring into main.go

```go
// Create event log (shared between handlers and dashboard)
evLog := eventlog.New()

// Pass to npm/pypi handlers — they call evLog.Record() after each decision
npmHandler := npm.New(..., evLog)
pypiHandler := pypi.New(..., evLog)

// Mount dashboard if enabled
if cfg.Dashboard.Enabled {
    dash := dashboard.New(cfg.Dashboard, evLog, log.Logger)
    dash.Mount(r)
}
```

---

## Config generation on first boot

If `sentinel.toml` is missing:

1. Generate random 32-byte hex secret (`crypto/rand`)
2. Generate random 12-char alphanumeric password (`crypto/rand`)
3. Write `sentinel.toml` with all defaults filled in
4. Print credentials to stdout (once, clearly formatted)
5. Start normally using the generated config

The generated config includes all sections so the operator can see the full schema.

---

## What is NOT in scope

- Multiple users / user management
- Package allowlist management via UI
- Historical data persistence (ring buffer is in-memory, lost on restart)
- Charts or graphs (counters and top-blocked list only)
- Dark/light theme toggle

---

## Self-review

- No TBDs or placeholders
- Config generation path is explicit (missing file → generate + print + start)
- SSE disconnect and reconnect covered in JS spec
- Ring buffer drop behaviour (slow subscriber dropped, not blocking) is explicit
- Auth cookie spec includes all relevant attributes (HttpOnly, SameSite, expiry)
- Ecosystem filter reconnects SSE rather than client-side filtering — correct for server-side fan-out
- `index.html` fetches initial events before SSE so the feed isn't empty on load
