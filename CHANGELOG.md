# 📋 Changelog

All notable changes to escrow are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

---

## [1.0.0] — 2026-05-17 🎉

First production-ready release. Covers all 7 major package ecosystems with age gating, vulnerability scanning, file caching, and a real-time operator dashboard.

### Added

**Ecosystems**
- NuGet (.NET) proxy at `/nuget/` — v3 service index, age-filtered registration, flatcontainer version list, `.nupkg` blob caching
- Maven / Gradle proxy at `/maven2/` — `maven-metadata.xml` age filtering via Maven Central Search API, artifact blob caching, snapshot upstream support
- Per-ecosystem upstream URL override (`npm_upstream`, `pypi_upstream`, etc.) — point at Nexus, Artifactory, or any registry mirror

**Age gate improvements**
- Age gate wired for all 7 ecosystems (npm, PyPI, Go, Cargo, Composer, NuGet, Maven)
- OSV ecosystem identifiers corrected for NuGet (`"NuGet"`) and Maven (`"Maven"`)
- NuGet OSV queries use canonical package name casing (`Newtonsoft.Json` not `newtonsoft.json`)
- Documented fail-open behavior when publish time is unavailable

**Reputation signals**
- OSV vulnerability scan for all 7 ecosystems (was 5)
- Publisher account age cached 1 hour per account
- All signals fail-open (API unavailable → skip, allow package)

**File caching**
- Disk cache writes via atomic rename (temp file in same dir → `os.Rename`)
- Memory cache uses same atomic rename pattern
- S3 backend streams via temp file (avoids full blob RAM buffer)
- Singleflight deduplication for manifest fetches (N concurrent cold-cache → 1 upstream hit)
- Go `.info`, `.mod`, `.zip`, and `list` responses all cached
- `cache_writable` field in `/healthz` (probes disk with temp file write)
- `ProxyRequestDuration` histogram instrumented for all 7 ecosystems

**Operations**
- Startup policy summary log (age gate min_days, OSV severity, publisher threshold)
- Warning when no ecosystems enabled or `[policy]` has no signals
- Warning when `allowlist_path == blocklist_path` or `eventlog_path` collides with list paths
- Warning when `tls_cert_file` / `tls_key_file` don't exist on disk
- Warning when `webhook_url` targets localhost (self-amplification risk)
- Event log cap increased from 500 to 5,000 events
- Event log optional JSONL persistence (`eventlog_path`)
- `ReadHeaderTimeoutSeconds` and `IdleTimeoutSeconds` configurable
- `MaxIdleConns: 100` on upstream HTTP transport
- ROADMAP.md added

**Security hardening**
- `ReadHeaderTimeout: 10s` prevents Slowloris attacks
- `IdleTimeout: 120s` on keep-alive connections
- HSTS header when TLS configured
- SSE write deadline disabled per-connection (`SetWriteDeadline(time.Time{})`) — streams no longer killed by `WriteTimeout`
- SSE subscriber cap (100 max concurrent dashboard connections)
- SSE `Subscribe()` called before headers are flushed — 503 is correctly returned (not garbled SSE)
- `Config.Validate()` added — fatal errors for negative `min_days`, invalid port
- `NewRequestWithContext` errors now checked (no nil-pointer panics on invalid URLs)
- `proxyBase(r)` fallback when `r.Host` is empty

**Dashboard**
- Allow/block list `Remove` endpoints (`DELETE /api/allow`, `DELETE /api/block`)
- Audit trail: allow/block/remove actions recorded to event log with operator username
- `blobCached` covers npm, cargo, and NuGet (shows cache status in Packages tab)

**Documentation**
- 12 quickstart guides in `docs/quickstart/` (npm, pnpm, yarn, bun, pip, uv, go, cargo, composer, dotnet, maven, gradle)
- Known Limitations section in README
- Fail-open behavior documented for OSV and publisher signals
- Allowlist/blocklist precedence corrected (wildcard allowlist supersedes blocklist)

### Fixed

- NuGet `serveDownload` dead first `upURL` assignment removed
- NuGet `serveVersionList` used `r.Context()` for cache writes → partial result on client cancel
- NuGet registration cache was host-specific (proxy hostname baked into cached `packageContent` URLs)
- NuGet paged registration fetch failure now omits page (fail-safe) rather than proxying unfiltered
- NuGet `rewritePackageContent` only handled `api.nuget.org` — custom upstreams now supported via `NuGetFlatcontainerURL`
- SSE connected comment flushed before subscriber cap check — 503 was a no-op after flush
- `unsub()` closed channel while `Record()` may still be sending → panic on closed channel
- `allow/block.Remove` used `entries[:0]` aliasing
- Rate limiter cleanup goroutine had no stop channel (goroutine leak)
- Maven `filterMetadata` left `<latest>`/`<release>` pointing at blocked versions when all versions blocked
- Composer `time.Now()` fallback for unknown timestamps (blocked ancient packages) → changed to `time.Date(2000,...)` 
- Disk `SetBlob` not atomic (concurrent writers could corrupt file)
- Memory `SetBlob` had same race
- Disk `SetBlob` temp file leaked on `os.Rename` failure
- `disk.SetBlob` left partial file when client disconnected mid-download
- OSV response body leaked when `resp.StatusCode != 200` (defer after status check)
- Same body leak in publisher.go (3 locations) and popularity.go (2 locations)
- npm `ServeTarball`, pypi `ServeFile`/`fetchReleases`, composer `serveRoot` all had same leak
- `upstreamURLs` for NuGet/Maven populated after `server.New` (fragile map aliasing)
- Cargo `config.json` hardcoded `http://` (now scheme-aware via `X-Forwarded-Proto`)
- Go `.info` responses never cached (every request hit upstream after singleflight cleared)
- Go `@latest` responses never cached
- Webhook fired per-version during manifest filter (now deduplicated per signal type per package)
- Publisher signal fell through to `SignalPass` when pkg URL was invalid → now returns `SignalSkip`
- S3 cache keys not sanitized (path traversal possible with crafted package names)
- Dashboard `blobCached` check used wrong cache key for NuGet downloads

### Security

- X-Forwarded-For no longer trusted for rate limiting (spoofable — now uses `r.RemoteAddr`)
- Timing-safe credential + HMAC comparison (`crypto/subtle`, `hmac.Equal`)
- `SameSite=Strict` session cookies (upgraded from Lax)
- CSRF Origin check on all mutating dashboard endpoints
- Login rate limiting (10 failures → 15-min IP lockout)
- Request body limits (64 KB) on dashboard API endpoints
- Security headers on all responses (CSP, X-Frame-Options, X-Content-Type-Options)
- `Secure` cookie flag when behind TLS proxy
- `.gitignore` excludes `sentinel.toml` and built binaries

### Changed

- Default bind address: `0.0.0.0` → `127.0.0.1` (use `--host=0.0.0.0` for team use)
- Upstream HTTP client: removed 15s global timeout (was aborting large artifact downloads); `ResponseHeaderTimeout: 30s`
- `GenerateIfMissing` template updated with all new config fields
- `GOPROXY` examples in docs corrected from `,direct` to `,off`
- Event log cap: 500 → 5,000

---

## [0.3.0] — 2026-05-14

Extended ecosystem coverage and dashboard improvements.

### Added
- Cargo sparse registry handler at `/cargo/`
- Composer/Packagist v2 metadata handler at `/composer/`
- NuGet (.NET) v3 handler at `/nuget/`
- Maven / Gradle Maven 2 handler at `/maven2/`
- Dashboard: Packages tab with per-ecosystem inventory and cache status
- Dashboard: manual block endpoint (`POST /api/block`)
- Allow/block list persistence and API
- Event log with SSE fan-out and subscriber management
- OSV and age gate wired for all 5 ecosystems active at this point

### Fixed
- Composer metadata-URL rewrite for proxied package paths
- Composer time parsing for old Packagist entries without timestamps
- E2E test coverage for all supported ecosystems

---

## [0.2.0] — 2026-05-12

Dashboard, metrics, and first-boot config generation.

### Added
- Go modules proxy at `/go/` (GOPROXY protocol) with age gate and OSV
- Dashboard with real-time SSE event stream and approve controls
- First-boot config generation: `sentinel.toml` with random password and HMAC secret
- npm package allowlist: config-driven override of blocked packages
- Dashboard allowlist UI: approve blocked packages from the web interface
- OSV vulnerability scanning with 24h cache
- Publisher account age signal (npm/PyPI)
- Popularity spike detection (download volume week-over-week)
- S3 cache backend (MinIO/Ceph compatible)
- Prometheus metrics

### Fixed
- npm publisher extraction: `_npmUser` → `maintainers` fallback
- Go module handler: unescape in unit test, 502 on body read error, webhook wiring
- E2E integration test suite for npm, PyPI, and Go
- Guard empty name in maintainers fallback

---

## [0.1.0] — 2026-05-10

Initial release.

### Added
- npm / pnpm / yarn / bun proxy with 7-day age gate and manifest filtering
- PyPI simple index + JSON proxy with age gate
- Disk and memory cache backends
- Policy engine: `allowlist → blocklist → signals`
- Webhook alerts on block events
- Configuration via TOML (`sentinel.toml`)
- Health check endpoint (`/healthz`)
