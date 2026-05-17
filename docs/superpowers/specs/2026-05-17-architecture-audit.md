# Escrow Architecture Audit

**Date:** 2026-05-17  
**Status:** Current baseline — reflects codebase after security/performance hardening session

---

## System Overview

`escrow` is a single-binary supply-chain proxy covering npm, PyPI, Go modules, Cargo, and Composer. It enforces trust policies (age gate, OSV vulnerability scan, publisher age, download spike detection) before any package reaches developers or CI. Blocked packages appear in a real-time web dashboard; operators approve packages without restarting.

```
client (npm/pip/go/cargo/composer)
         │
         ▼
┌──────────────────────────────────┐
│  HTTP Server (chi, port 8888)    │
│  Security headers, recovery MW   │
│  /healthz  /metrics  /dashboard  │
└───────┬──────────────────────────┘
        │ routes by URL pattern
   ┌────┴────┬──────┬───────┬──────────┐
   ▼         ▼      ▼       ▼          ▼
  npm      PyPI    Go    Cargo    Composer
  handler handler handler handler  handler
   └────┬────┴──────┴───────┴──────────┘
        │ Package struct per version
        ▼
┌──────────────────────────────────┐
│  Trust Engine                    │
│  AgeSignal | OSVSignal           │
│  PublisherSignal | PopularitySignal│
└──────────────┬───────────────────┘
               ▼
┌──────────────────────────────────┐
│  Policy Engine                   │
│  AllowList → BlockList → Signals │
│  → block | warn | allow          │
└──────────────┬───────────────────┘
               ▼
┌──────────────────────────────────┐
│  Cache (interface)               │
│  disk (default) | memory | S3    │
│  meta: JSON, TTL    blobs: perm  │
└──────────────────────────────────┘
```

---

## Strengths

**Architecture:**
- Clean layer separation — each layer behind an interface; swappable without touching callers
- All 5 ecosystems handled uniformly via the same `trust.Signal` / `policy.Engine` pipeline
- 3 cache backends behind one interface (disk, memory, S3)
- Real-time event log with SSE fan-out; multiple dashboard tabs get independent streams
- Graceful shutdown with 10-second drain window
- First-boot config generation with cryptographically random credentials

**Security (after 2026-05-17 hardening):**
- Timing-safe credential and HMAC comparison (`crypto/subtle`, `hmac.Equal`)
- Login rate limiting (10 failures → 15-minute IP lockout)
- CSRF protection via Origin header check on mutating dashboard endpoints
- Security headers on all responses (CSP, X-Frame-Options, X-Content-Type-Options)
- Request body size cap (64 KB) on all form/API POST endpoints
- `SameSite=Strict`, `HttpOnly`, and conditional `Secure` on session cookie
- Default bind is `127.0.0.1`; public access requires explicit `--host=0.0.0.0` or config

**Performance (after 2026-05-17 hardening):**
- Filtered manifests cached for 5 minutes (npm, PyPI, Composer) — eliminates repeated upstream fetch per install
- Blobs streamed to client and cache simultaneously via `io.TeeReader` + `io.Pipe` — no double-buffering
- Cache write synchronized before handler returns — prevents cache-miss race on back-to-back requests
- OSV results cached 24 hours per version
- Publisher account lookup cached 1 hour per account/package
- Cargo version metadata cached 1 hour per crate

---

## Risk Register

### HIGH

| ID | Risk | Where | Impact |
|----|------|--------|--------|
| H1 | **Cache stampede** — N concurrent cold-cache requests all hit upstream simultaneously | all handlers | Upstream rate-limit, slow response burst on popular packages |
| H2 | **No TLS on proxy or dashboard** | `server/server.go` | Dashboard password sent cleartext unless behind reverse proxy; blocks enterprise deployment |
| H3 | **`/healthz` does not check upstream reachability** | `metrics/metrics.go` | Returns 200 when all upstreams are down; load balancer won't remove unhealthy instance |
| H4 | **Disk cache has no eviction or size limit** | `cache/disk.go` | Disk fills on production instances; no operator visibility into cache size |

### MEDIUM

| ID | Risk | Where | Impact |
|----|------|--------|--------|
| M1 | **Webhook fires once per blocked version, not per package** | npm `filterManifest` | 200 webhook POSTs for a 200-version package with all versions blocked; receiver overloaded |
| M2 | **Event log not persisted** | `eventlog/log.go` | Restart loses all event history; operators lose audit trail |
| M3 | **Go `.mod` and `.zip` files not cached** | `handler/gomod/handler.go` | Repeated upstream fetches for non-`.info` paths; `.mod` files are tiny but `.zip` archives can be large |
| M4 | **No Remove endpoint for allow/block lists** | `dashboard/handlers.go` | Allowlist entries can only be added; operator must edit JSON file manually to remove an entry |
| M5 | **No audit log for dashboard actions** | `dashboard/handlers.go` | No record of who approved/blocked what and when |
| M6 | **Cargo `config.json` hardcodes `http://`** | `handler/cargo/handler.go:96` | Breaks when escrow is behind TLS termination; Cargo rejects mixed HTTP/HTTPS |
| M7 | **No request rate limiting on proxy endpoints** | `server/server.go` | Login is protected; proxy itself can be hammered at upstream-rate; DoS via manifest amplification |

### LOW

| ID | Risk | Where | Impact |
|----|------|--------|--------|
| L1 | **`--host` flag not documented in README** | `README.md` | New operators bind to all interfaces without knowing the safer default exists |
| L2 | **`WriteTimeout` is 60s** | `server/server.go` | Large `.crate` or wheel downloads to slow clients may time out mid-transfer |
| L3 | **Allow/block list: O(n) scan per lookup** | `allow/list.go`, `block/list.go` | Acceptable at current scale; becomes a bottleneck if lists grow to thousands of entries |
| L4 | **`sentinel.toml` was committed with real credentials** | git history | Secret and password are in git history; must be rotated even after `.gitignore` is applied |
| L5 | **S3 `SetBlob` buffers entire body in RAM** | `cache/s3.go` | Large `.zip` or wheel files (up to ~500 MB for some Python packages) fully buffered before S3 upload |
| L6 | **No per-ecosystem upstream URL config** | `cmd/escrow/main.go` | Cannot point npm at an internal Nexus instead of registry.npmjs.org without code change |

---

## Operational Readiness

### Single Points of Failure

Escrow is a single process with no redundancy. If it restarts:
- In-memory event log is lost (allowlist/blocklist survive on disk)
- Manifest cache is cold (5-minute TTL, so only 5 minutes of upstream pressure)
- No failover — all package installs fail until the process is back

**Mitigation path:** Run behind a process supervisor (systemd, Docker restart policy). For HA, run two instances behind a load balancer — they share the disk/S3 cache but have independent in-memory event logs.

### Blast Radius

- Escrow failure → `npm install`, `pip install`, `go get`, `cargo build` all fail (if pointed at escrow only)
- Escrow misconfiguration → builds fail silently or packages are incorrectly blocked
- Dashboard compromise → attacker can approve malicious packages via the allowlist

### Rollback

- Config is a single TOML file; rollback = revert the file and restart
- Allow/block list are JSON files; rollback = restore from backup
- No database migrations
- Binary rollback: keep previous binary, restart with it

### Observability

**Covered:**
- `/healthz` — liveness check (storage backend)
- `/metrics` — Prometheus counters: `escrow_requests_total`, `escrow_blocks_total`, `escrow_cache_hits_total`, `escrow_osv_query_duration_seconds`
- Structured JSON logging via zerolog (all requests at DEBUG, errors at ERROR)

**Missing:**
- No upstream reachability metric (H3)
- No cache size metric (H4)
- No latency histogram for proxy requests (only OSV has a histogram)
- No alert rules documented

### Secrets

- Dashboard password and HMAC secret in `sentinel.toml` (0600, excluded from git)
- No support for env-var or secret manager injection of credentials
- Secret rotation requires restart (new secret invalidates existing sessions)

---

## What Works Well End-to-End (Verified 2026-05-17)

| Test | Result |
|------|--------|
| npm block-all: manifest filtered to 0 versions | PASS |
| npm block-all: `npm install` blocked | PASS |
| PyPI block-all: releases pruned (163→3) | PASS |
| Go block-all: `.info` returns 403 | PASS |
| npm allow-all: `once` installs via escrow | PASS |
| PyPI allow-all: 163 releases proxied | PASS |
| Go allow-all: module proxied successfully | PASS |
| Unit tests (84 tests, 18 packages) | ALL PASS |

---

## Recommended Next Steps

Priority order based on blast radius and operational risk:

1. **[H1] Singleflight for cold-cache manifest requests** — add `golang.org/x/sync/singleflight` to deduplicate concurrent upstream fetches for the same key
2. **[H2] TLS documentation** — add nginx/caddy reverse proxy example to README; consider optional TLS in config
3. **[H3] Upstream health check** — add per-ecosystem reachability probe to `/healthz` (HEAD request to upstream with short timeout)
4. **[H4] Cache metrics + eviction** — add `escrow_cache_disk_bytes` gauge; add configurable max blob age for disk cache
5. **[M1] Webhook deduplication** — group blocked versions by package, send one webhook per package per manifest request
6. **[M4/M5] Allow/block list Remove + audit trail** — `DELETE /api/allow` and `DELETE /api/block` endpoints; log to event log
7. **[M3] Go `.mod`/`.zip` caching** — cache these alongside `.info` trust checks
8. **[M6] Cargo scheme-aware `config.json`** — detect `X-Forwarded-Proto` or a `base_url` config field
9. **[L6] Per-ecosystem upstream URL config** — expose `[ecosystems.npm.upstream_url]` etc. in config

See `2026-05-17-next-iteration.md` for the implementation plan.
