# 🗺️ Escrow Roadmap

This document tracks what's shipped, what's in progress, and what's planned.

---

## ✅ v1.0.0 — Shipped

### Core proxy
- [x] **npm / pnpm / yarn / bun** — full registry proxy at `/`
- [x] **PyPI** (pip, uv) — simple index + JSON API at `/pypi/`
- [x] **Go modules** — GOPROXY-compatible proxy at `/go/`
- [x] **Cargo** — sparse registry at `/cargo/`
- [x] **Composer (PHP)** — Packagist v2 metadata proxy at `/composer/`
- [x] **NuGet (.NET)** — v3 service index + registration + flatcontainer at `/nuget/`
- [x] **Maven / Gradle** — Maven 2 layout proxy at `/maven2/`, snapshot upstream support

### Age gate
- [x] Block packages published fewer than `min_days` days ago
- [x] Configurable per-policy action: `block` | `warn` | `allow`
- [x] Fail-open when publish time is unavailable (upstream API down)
- [x] Wired for all 7 ecosystems

### Reputation signals
- [x] **OSV vulnerability scan** — queries osv.dev for all 7 ecosystems with correct ecosystem identifiers
- [x] **Publisher account age** — npm + PyPI (no public API available for others)
- [x] **Download spike detection** — npm + PyPI
- [x] Configurable severity threshold and actions
- [x] All signals fail-open (API down → skip signal, allow package)
- [x] Signal results cached (OSV: 24h, publisher: 1h)

### File caching
- [x] **Disk backend** — atomic rename, temp-in-same-dir, no partial reads
- [x] **Memory backend** — temp dir, in-process (tests/dev only)
- [x] **S3 backend** — MinIO compatible, temp-file streaming
- [x] Blobs cached permanently (tarballs, wheels, JARs, .nupkg, .crate)
- [x] Manifests cached with 5-min TTL
- [x] Singleflight deduplication — N concurrent cold-cache requests → 1 upstream fetch
- [x] Cache writability probe in `/healthz`

### Dashboard
- [x] Real-time SSE event stream (all 7 ecosystems)
- [x] Approve blocked packages (adds to allowlist instantly)
- [x] Block packages manually (adds to blocklist)
- [x] Remove from allowlist / blocklist
- [x] Per-ecosystem filtering
- [x] Stats: blocked / warned / allowed counts, top blocked packages
- [x] Event log persistence to JSONL (optional)
- [x] Subscriber cap (max 100 concurrent SSE connections)

### Allow / block lists
- [x] Wildcard version entries (`"version": ""` matches all versions)
- [x] Allowlist checked before all signals (short-circuits blocklist too)
- [x] Persistence to JSON files (hot-reloaded on next request)
- [x] `added_by` and `added_at` audit fields
- [x] REST API: POST/DELETE for allow and block

### Operations
- [x] `/healthz` — upstream probe per ecosystem + cache writability
- [x] `/metrics` — Prometheus counters for requests, blocks, cache hits, latency
- [x] Startup policy summary log (age gate, OSV severity, publisher threshold)
- [x] Config validation (port range, negative min_days → fatal)
- [x] 12 startup warnings (empty secret, zero min_days, no ecosystems, TLS file missing, etc.)
- [x] TLS support (`tls_cert_file` / `tls_key_file`)
- [x] Per-IP proxy rate limiting
- [x] Configurable timeouts (write, read-header, idle)
- [x] `--host` flag + `127.0.0.1` default
- [x] Per-ecosystem upstream URL override (point at Nexus, Artifactory, etc.)
- [x] Maven snapshot upstream support

### Security
- [x] Timing-safe credential comparison (`crypto/subtle`, `hmac.Equal`)
- [x] Login rate limiting (10 failures → 15-min lockout)
- [x] CSRF protection (Origin header check)
- [x] Security headers (CSP, X-Frame-Options, X-Content-Type-Options)
- [x] HSTS when TLS configured
- [x] `SameSite=Strict` session cookies
- [x] Request body size limits (64 KB on API endpoints)
- [x] Disk cache atomic writes (no partial blobs on disk)
- [x] SSE write deadline disabled (streams don't die after WriteTimeout)

---

## 🚧 v1.1.0 — Near-term

### Composer archive caching
Composer metadata (version lists) is proxied and age-gated. The actual ZIP archives are downloaded directly from Packagist CDN via `dist.url` in the metadata — escrow doesn't see them. Rewriting `dist.url` to route through escrow and caching the archives would complete the air-gap story for PHP.

**Scope:** rewrite `dist.url` in Composer metadata → add `/composer/download?url=...` blob route → cache ZIP on first fetch.

### NuGet: age filter for paged registrations without page fetch
When a paged registration page cannot be fetched (upstream error), we currently omit the page entirely. A better approach: serve paged items unfiltered with an age-gate warning in the event log, rather than silently hiding versions from clients.

### Publisher signal: Go modules
The Go module proxy publishes metadata that includes the committer and commit time. Add a publisher signal for Go that checks whether the module has been published by a known maintainer (based on GOSUM database or module path patterns).

### Configurable event log filter
Currently records every version evaluation (allow + warn + block). On popular packages with hundreds of versions, this floods the event log. Add config option to record only `block` and `warn` events.

```toml
[eventlog]
  record = "blocked_only"  # all | warned_and_blocked | blocked_only
```

---

## 📋 v1.2.0 — Medium-term

### SBOM generation
Generate a Software Bill of Materials for packages flowing through escrow. Integrates with the package install flow without requiring client changes.

### Sigstore / SLSA attestation verification
Verify that packages have a valid Sigstore transparency log entry before serving them. For npm, this uses the `_attestations` field in the package manifest. For PyPI, it uses the new attestations API.

### License filtering
Block or warn on packages with disallowed licenses (e.g., AGPL, GPL). Requires fetching license metadata from each registry.

### REST API for management
Full REST API for programmatic allow/block list management, policy updates, and event log queries. Current dashboard API is HTTP but not fully documented as a public API.

### Horizontal scaling
Multiple escrow instances sharing an S3 cache and a common allow/block list (stored in S3 or a database). Currently works at single-instance scale with disk/S3 cache but allow/block lists are local JSON files.

---

## 🔮 v2.0 — Long-term

- **Docker/OCI registry proxy** — container image layer caching + age gate
- **Private registry auth forwarding** — pass credentials to private npm/PyPI/Maven registries
- **Dependency graph analysis** — detect suspicious transitive dependency additions
- **ML-based anomaly detection** — flag packages with unusual publish patterns
- **Audit log export** — structured export to Splunk, Elasticsearch, S3

---

## ❌ Known limitations (out of scope unless prioritized)

| Limitation | Notes |
|-----------|-------|
| Composer ZIP archives bypass proxy | Metadata is filtered; archives download direct from Packagist CDN |
| Cargo `build.rs` not sandboxed | No off switch for Rust build scripts; use `cargo-vet` + `cargo-deny` |
| NuGet paged registration with fetch failures | On page fetch error, page is omitted (fail-safe for security, but versions become invisible) |
| S3 SetBlob uses temp file | Avoids RAM buffering; temp file on same volume as cache dir |
| Event log not replicated | In-memory ring buffer (5000 events); JSONL persistence optional but single-node |
| Publisher signal npm/PyPI only | No public account-age API for Go, Cargo, Maven, NuGet |
