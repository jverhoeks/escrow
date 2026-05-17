# Escrow — Next Iteration Plan

**Date:** 2026-05-17  
**Baseline:** After security + performance hardening (84/84 tests, 7/7 integration tests)  
**Source:** Architecture audit `specs/2026-05-17-architecture-audit.md`

---

## Completed Since v0.1

All items from `2026-05-16-sentinel.md` and `2026-05-16-dashboard.md` are done. Additionally:

- [x] Security headers on all responses (CSP, X-Frame-Options, X-Content-Type-Options)
- [x] Timing-safe auth (`crypto/subtle`, `hmac.Equal`)
- [x] Login rate limiter (10 failures → 15-min lockout per IP)
- [x] CSRF guard on mutating dashboard endpoints (Origin header check)
- [x] Request body size cap (64 KB on POST endpoints)
- [x] `?n=` bounded to 1000 on `/api/events`
- [x] S3 cache key sanitization
- [x] `SameSite=Strict`, `Secure` flag conditional on TLS
- [x] `.gitignore` for `sentinel.toml` and binaries
- [x] Default bind `127.0.0.1`; `--host` flag for override
- [x] Filtered manifest caching (npm, PyPI, Composer) — 5-minute TTL
- [x] TeeReader + synchronized blob caching — eliminates double-buffering and cache-miss race
- [x] Publisher signal cached per account (1h TTL)
- [x] Cargo `serveDownload` updated to TeeReader
- [x] Dead code removed from `cargo/handler.go` (`responseStarted`, `buildIndexLine`, `makeNDJSON`)
- [x] README `GOPROXY` corrected from `,direct` to `,off`

---

## Iteration 2 Tasks

### Task 1: Singleflight for cold-cache manifest requests [H1]

**Risk:** 100 concurrent cold-cache npm installs = 100 upstream requests to npmjs.org.

**Files:**
- Add `golang.org/x/sync` to `go.mod`
- `internal/handler/npm/handler.go` — wrap `ServeManifest` upstream fetch in singleflight group keyed by package name
- `internal/handler/pypi/handler.go` — same for `ServeJSON` and `ServeSimpleIndex`
- `internal/handler/composer/handler.go` — same for `servePackage`
- `internal/handler/gomod/handler.go` — same for `serveVersioned` (`.info` path)

**Pattern:**
```go
// One singleflight group per handler, keyed by package name.
// All concurrent requests for the same manifest wait on the single inflight fetch.
var g singleflight.Group
result, err, _ := g.Do(name, func() (any, error) {
    // fetch + filter + cache
    return data, nil
})
```

- [ ] Add `golang.org/x/sync/singleflight` to go.mod
- [ ] Add `singleflight.Group` field to each handler struct
- [ ] Wrap manifest upstream fetch in `g.Do(name, ...)` in npm, pypi, composer, gomod handlers
- [ ] Test: concurrent requests for same package hit upstream only once

---

### Task 2: Upstream health probe in /healthz [H3]

**Risk:** `/healthz` returns 200 even when all upstreams are unreachable.

**Files:**
- `internal/metrics/metrics.go` — extend `HealthResponse` and `HealthHandler`
- `cmd/escrow/main.go` — pass enabled upstreams to health handler

**Design:** Health check performs a HEAD or lightweight GET against each enabled upstream (5s timeout). Result is `upstream_ok: {npm: true, pypi: false, ...}`. If any upstream fails, overall status is `degraded` (not `ok`). Process stays alive but load balancers can observe degradation.

- [ ] Add `UpstreamStatus map[string]bool` to `HealthResponse`
- [ ] Add `upstreamProbe(ctx, client, url) bool` function with 5s timeout
- [ ] Pass upstream URLs to `HealthHandler` via a `map[string]string`
- [ ] Change overall `Status` to `"degraded"` when any upstream fails
- [ ] Test: mock upstream returns 503, assert healthz returns `degraded`

---

### Task 3: Webhook deduplication per package [M1]

**Risk:** 200-version npm package with all versions blocked = 200 webhook POSTs.

**Files:**
- `internal/handler/npm/handler.go` — `filterManifest`: collect all blocked versions, send one webhook
- `internal/alerts/webhook.go` — add `SendBatch(pkgs []trust.Package, d policy.Decision)` or change payload

**Design:** After the filter loop, if any versions were blocked, send a single webhook with the package name, how many versions were blocked, and the first signal/reason. Individual version details go in an array field.

- [ ] Add `BatchPayload` type to `alerts/webhook.go` with `BlockedVersions []string`
- [ ] Update npm `filterManifest` to collect blocked versions, call `SendBatch` once
- [ ] Update pypi `ServeJSON` similarly
- [ ] Update tests

---

### Task 4: Allow/block list Remove endpoints + audit trail [M4, M5]

**Risk:** Entries can only be added; operator must hand-edit JSON to remove. No audit trail.

**Files:**
- `internal/allow/list.go` — add `Remove(ecosystem, name, version string) error`
- `internal/block/list.go` — same
- `internal/dashboard/handlers.go` — add `DELETE /api/allow` and `DELETE /api/block`
- `internal/eventlog/log.go` — add `ActionAllow`/`ActionBlock`/`ActionRemove` constants for dashboard actions
- `internal/dashboard/handlers.go` — log allow/block/remove actions to event log

**Design:** `Remove` finds entries matching ecosystem+name (all versions if version is empty, exact version if specified) and removes them atomically. Dashboard records the action as a `PackageEvent` with `action="allowlist-add"` or `"blocklist-remove"` so it appears in the live feed.

- [ ] Add `Remove` method to `allow.List` and `block.List`
- [ ] Add `DELETE /api/allow` and `DELETE /api/block` handlers with same CSRF guard and body limit
- [ ] Log allow/block/remove operations to event log (what, who, when)
- [ ] Update `handleAllowList` and `handleBlockList` to include `added_at` and `added_by` in response
- [ ] Test: add entry, remove entry, verify list is empty

---

### Task 5: Go `.mod` and `.zip` caching [M3]

**Risk:** `.mod` and `.zip` paths are proxied but not cached, causing repeated upstream fetches.

**Files:**
- `internal/handler/gomod/handler.go` — extend `serveVersioned` to cache non-`.info` responses

**Design:** For `.mod` files (small, stable): cache as meta with 24h TTL. For `.zip` files (potentially large): cache as blob (permanent). Both only after trust check passes on `.info` (Go fetches `.info` before `.zip`, so if `.info` is blocked, `.zip` is never requested).

- [ ] Cache `.mod` responses using `SetMeta` with 24h TTL, keyed `go/mod/{module}@{version}`
- [ ] Cache `.zip` blobs using TeeReader pattern, keyed `go/zip/{module}@{version}`
- [ ] Add cache hit metrics for go ecosystem
- [ ] Test: two requests for same `.mod` — upstream hit once

---

### Task 6: Cargo `config.json` scheme detection [M6]

**Risk:** `config.json` hardcodes `http://` even when escrow is behind TLS termination.

**Files:**
- `internal/handler/cargo/handler.go` — `serveConfig`

**Design:** Check `X-Forwarded-Proto` header first, then `r.TLS != nil`. Fall back to `http` for localhost.

```go
scheme := "http"
if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
    scheme = "https"
}
cfg := map[string]string{
    "dl": fmt.Sprintf("%s://%s/cargo/crates/{crate}/{version}/download", scheme, host),
    ...
}
```

- [ ] Detect scheme from `r.TLS` and `X-Forwarded-Proto`
- [ ] Apply to `dl` field in `serveConfig`
- [ ] Test: request with `X-Forwarded-Proto: https` produces `https://` dl URL

---

### Task 7: Per-ecosystem upstream URL config [L6]

**Risk:** Cannot point individual ecosystems at an internal Nexus/Artifactory instance.

**Files:**
- `internal/config/config.go` — add `UpstreamURL string` to each ecosystem config
- `cmd/escrow/main.go` — pass ecosystem upstream URL when constructing each handler

**Design:**
```toml
[ecosystems]
  npm      = true
  npm_upstream = "https://registry.npmjs.org"   # optional override

  pypi     = true
  pypi_upstream = "https://pypi.org"

  go       = true
  go_upstream = "https://proxy.golang.org"
```

- [ ] Add `NPMUpstream`, `PyPIUpstream`, `GoUpstream`, `CargoUpstream`, `ComposerUpstream` fields to `EcosystemConfig`
- [ ] Default each to the current hardcoded URL
- [ ] Wire overrides in `main.go` when constructing handlers
- [ ] Update `config.example.toml` with commented-out override examples
- [ ] Test: point npm handler at mock upstream via config

---

## Deferring to v0.3

These are valid but lower priority than the items above:

- **Disk cache eviction** (H4) — requires a background cleanup goroutine with configurable max-age; non-trivial to test safely
- **TLS in config** — operational concern; document nginx/caddy approach instead
- **Allow/block list O(n) scan** (L3) — not a problem below 10k entries; optimize if needed
- **S3 streaming upload** (L5) — only matters for very large wheels; leave for when an S3 user reports it
- **`WriteTimeout` increase for large downloads** (L2) — configurable via `[server] write_timeout_seconds`

---

## Acceptance criteria for Iteration 2

- [ ] `go test ./...` — all 84+ tests pass
- [ ] `bash tests/test-escrow.sh` — 7/7 pass
- [ ] Concurrent manifest test: 10 goroutines requesting cold-cache `lodash` → upstream hit once
- [ ] Webhook dedup test: 200-version package blocked → exactly 1 webhook POST
- [ ] Remove test: add entry to allowlist, remove it, list is empty
- [ ] Go caching test: two `.mod` requests → upstream hit once
