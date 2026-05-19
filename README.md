# 🔐 escrow

A lightweight supply-chain proxy that enforces configurable trust policies before any package reaches your developers or CI pipeline. Covers **7 ecosystems** in a single binary.

```
developer / CI  →  escrow  →  upstream registry
                      │
                policy engine
          ┌───────────┼────────────┐
         age         osv      publisher  popularity
```

Packages that fail policy are **blocked at the proxy level** — they never reach the tool. Operators review blocked events in the real-time dashboard and approve packages with a single click.

---

## 🚀 Quick Install

### 🍺 Homebrew (macOS — recommended)

```bash
brew tap jverhoeks/tap
brew install escrow
```

**Run as a background service** (auto-starts on login):

```bash
brew services start escrow
# → http://localhost:7888/dashboard
# Dashboard credentials are in: $(brew --prefix)/var/log/escrow.log
```

```bash
brew services stop escrow      # stop
brew services restart escrow   # reload config
```

Config lives at `$(brew --prefix)/etc/escrow/escrow.toml` — edit it to enable more ecosystems, then restart the service.

### 🐳 Docker

```bash
docker run -p 7888:7888 ghcr.io/jverhoeks/escrow:latest
```

**Debug config** (all 7 ecosystems, full policy, `admin` / `escrow`):

```bash
cd docker/
mkdir -p data && cp escrow.debug.toml data/escrow.toml
docker compose up -d
# → http://localhost:7888/dashboard
```

### 📦 Binary

```bash
# macOS arm64
curl -L https://github.com/jverhoeks/escrow/releases/latest/download/escrow-darwin-arm64 -o escrow
chmod +x escrow && ./escrow

# macOS amd64
curl -L https://github.com/jverhoeks/escrow/releases/latest/download/escrow-darwin-amd64 -o escrow
chmod +x escrow && ./escrow

# Linux amd64
curl -L https://github.com/jverhoeks/escrow/releases/latest/download/escrow-linux-amd64 -o escrow
chmod +x escrow && ./escrow
```

On first boot escrow generates `escrow.toml` with a random dashboard password and prints credentials to stdout.

### ⚙️ Flags

```bash
./escrow                              # binds to 127.0.0.1:7888 (localhost only)
./escrow --host=0.0.0.0               # listen on all interfaces (team/CI use)
./escrow --config=/etc/escrow/escrow.toml
./escrow --host=0.0.0.0 escrow.toml # flag + positional config path
```

> 💡 On first boot, credentials are printed to stdout. Save them — or find them in the generated `escrow.toml`.

---

## 🌐 Supported Ecosystems

| Ecosystem | Tools | Proxy URL | Config key |
|-----------|-------|-----------|------------|
| npm | npm, pnpm, yarn, bun | `http://localhost:7888/` | `npm = true` |
| PyPI | pip, uv | `http://localhost:7888/pypi/simple/` | `pypi = true` |
| Go modules | go | `http://localhost:7888/go/` | `go = true` |
| Cargo | cargo | `http://localhost:7888/cargo/` | `cargo = true` |
| Composer | composer | `http://localhost:7888/composer/` | `composer = true` |
| NuGet | dotnet, nuget | `http://localhost:7888/nuget/index.json` | `nuget = true` |
| Maven / Gradle | mvn, gradle | `http://localhost:7888/maven2/` | `maven = true` |

---

## ⚡ GitHub Actions

Use escrow as a one-step supply-chain gate in any CI pipeline. Add it before your install steps — no other changes needed:

```yaml
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6

      - uses: jverhoeks/escrow@v1
        with:
          ecosystems: 'npm'
          min-days: '7'
          osv-severity: 'HIGH'

      - uses: actions/setup-node@v6
        with:
          node-version: '20'

      - run: npm install --ignore-scripts
      # npm automatically uses the escrow registry — no other changes needed
```

Escrow sets `NPM_CONFIG_REGISTRY`, `PIP_INDEX_URL`, `GOPROXY`, etc. automatically so every install command routes through the proxy. The package cache is stored in GitHub Actions cache and restored on every run — warm cache runs require zero upstream calls.

| Input | Default | Description |
|---|---|---|
| `ecosystems` | `npm,pypi,go,cargo` | Comma-separated list to enable |
| `min-days` | `7` | Age gate threshold |
| `osv-severity` | `HIGH` | Minimum CVE severity to block (`off` to disable) |
| `version` | `v1.4.1` | Escrow binary version |
| `port` | `7888` | Local proxy port |
| `cache-key-suffix` | `` | Append to cache key for manual busting |

**Output**: `proxy-url` — the base URL (`http://127.0.0.1:7888`).

→ Full guide: [docs/github-actions.md](docs/github-actions.md)

---

## 📖 Per-Tool Quickstarts

Step-by-step guides for global setup, per-project setup, verify, and remove for each tool:

| Tool | Guide |
|------|-------|
| npm | [docs/quickstart/npm.md](docs/quickstart/npm.md) |
| pnpm | [docs/quickstart/pnpm.md](docs/quickstart/pnpm.md) |
| yarn | [docs/quickstart/yarn.md](docs/quickstart/yarn.md) |
| bun | [docs/quickstart/bun.md](docs/quickstart/bun.md) |
| pip | [docs/quickstart/pip.md](docs/quickstart/pip.md) |
| uv | [docs/quickstart/uv.md](docs/quickstart/uv.md) |
| go | [docs/quickstart/go.md](docs/quickstart/go.md) |
| cargo | [docs/quickstart/cargo.md](docs/quickstart/cargo.md) |
| composer | [docs/quickstart/composer.md](docs/quickstart/composer.md) |
| dotnet / NuGet | [docs/quickstart/dotnet.md](docs/quickstart/dotnet.md) |
| maven | [docs/quickstart/maven.md](docs/quickstart/maven.md) |
| gradle | [docs/quickstart/gradle.md](docs/quickstart/gradle.md) |
| **GitHub Actions** | [docs/github-actions.md](docs/github-actions.md) |

---

## 🛡️ What Escrow Protects Against

| Threat | Protected? |
|--------|-----------|
| ✅ Same-day injection attacks (packages published and spread within hours) | blocked by age gate |
| ✅ Packages with known CVEs (MEDIUM/HIGH/CRITICAL by default) | blocked by OSV scan |
| ✅ Packages from brand-new publisher accounts | flagged by publisher signal |
| ✅ Packages with sudden download spikes (possible hijacking) | flagged by popularity signal |
| ✅ Packages on your explicit blocklist | blocked at allowlist/blocklist check |
| ✅ Air-gap: packages that haven't been reviewed never reach developer machines | proxy-level enforcement |
| ❌ Postinstall hooks in packages that do pass the gate | use `ignore-scripts=true` per tool |
| ❌ Typosquatting on packages that pass age/vuln gates | not yet implemented |
| ❌ Git-protocol npm deps (`npm install github:user/pkg`) | bypass the registry entirely |
| ❌ Composer ZIP archives (artifact air-gap) | metadata filtered; archives fetched from CDN |
| ❌ Publisher signal for Go, Cargo, NuGet, Maven | no public API equivalent |

> ⚠️ **Postinstall hooks** are the most important gap. Escrow filters packages from manifests but does not strip `postinstall` hooks from packages that pass. Set `ignore-scripts=true` (npm/pnpm), `enableScripts: false` (yarn), or `only-binary = [":all:"]` (uv) on every developer machine.

---

## 🔧 Point Your Tools at Escrow

### npm / pnpm / yarn / bun

```ini
# .npmrc
registry=http://localhost:7888
ignore-scripts=true
```

### pip / uv

```toml
# uv.toml
[pip]
index-url = "http://localhost:7888/pypi/simple/"
```

### Go modules

```bash
export GOPROXY=http://localhost:7888/go,off   # use ,off not ,direct
```

> ⚠️ **`,off` vs `,direct`:** Using `direct` as fallback means Go silently downloads from the original VCS if escrow is unreachable — bypassing all controls. Use `off` so the build fails loudly instead.

### Cargo (Rust)

```toml
# .cargo/config.toml
[source.crates-io]
replace-with = "escrow"

[source.escrow]
registry = "sparse+http://localhost:7888/cargo/"
```

### Composer (PHP)

```json
{
  "repositories": [
    {"type": "composer", "url": "http://localhost:7888/composer"},
    {"packagist.org": false}
  ]
}
```

### NuGet (.NET)

```xml
<!-- nuget.config -->
<configuration>
  <packageSources>
    <clear />
    <add key="escrow" value="http://localhost:7888/nuget/index.json"
         allowInsecureConnections="true" />
  </packageSources>
</configuration>
```

> `allowInsecureConnections="true"` is required for HTTP sources in NuGet >= 6.8.
> Use TLS (`tls_cert_file` / `tls_key_file` in `escrow.toml`) to avoid it in production.

Must enable in config: `nuget = true` under `[ecosystems]`.

### Maven

```xml
<!-- ~/.m2/settings.xml -->
<settings>
  <mirrors>
    <mirror>
      <id>escrow</id>
      <url>http://localhost:7888/maven2</url>
      <mirrorOf>central</mirrorOf>
      <checksumPolicy>fail</checksumPolicy>
    </mirror>
  </mirrors>
</settings>
```

### Gradle

```kotlin
// settings.gradle.kts
dependencyResolutionManagement {
    repositories { maven(url = "http://localhost:7888/maven2") }
}
```

Both Maven and Gradle share the same `/maven2/` endpoint. Enable with `maven = true`.

---

## 📊 Dashboard

Real-time package event stream with approve/block controls.

```
┌──────────────────────────────────────────────────────────────────┐
│ ESCROW  package proxy                         ● LIVE  admin  logout│
├──────────┬──────┬──────────────────┬─────────────────┬────────────┤
│ TIME     │ ECO  │ PACKAGE          │ SIGNAL          │ STATUS     │
├──────────┼──────┼──────────────────┼─────────────────┼────────────┤
│ 14:03:12 │ npm  │ lodash@4.17.21   │ age · 2d old    │ BLOCK ✓   │
│ 14:03:09 │ pypi │ requests@2.31.0  │ osv · CVE-...   │ BLOCK ✓   │
│ 14:03:07 │ go   │ gin@v1.9.1       │ age · 1825d old │ ALLOW      │
│ 14:02:48 │ nuget│ Newtonsoft@13.0  │ age · 540d old  │ ALLOW      │
│ 14:02:31 │ maven│ spring:core@6.1  │ age · 90d old   │ ALLOW      │
└──────────────────────────────────────────────────────────────────┘
```

Access at `http://localhost:7888/dashboard`. Credentials are printed on first boot.

**Approve a blocked package:** click ✓ next to any blocked event. Added to
`escrow-allowlist.json` immediately. No restart needed.

**Remove from allowlist:** `DELETE /dashboard/api/allow` with `{"ecosystem","name","version"}`.
All changes are recorded in the live feed with the operator's username.

**Block a package manually:** `POST /dashboard/api/block`. Same format.

---

## ⚙️ Policy Configuration

All policy lives in `escrow.toml`. Without a `[policy]` section escrow proxies
transparently (with a startup warning).

### 🗓️ Age gate

Blocks packages published fewer than N days ago. Catches injection attacks that
publish and spread quickly before the community notices.

```toml
[policy.age]
  min_days = 7       # packages must be at least 7 days old
  action   = "block" # block | warn | allow
```

| `min_days` | Use case |
|-----------|----------|
| 1 | Catch same-day injections |
| 7 | Recommended baseline |
| 30 | High-security environments |

### 🔍 OSV vulnerability scan

Checks every package version against the [Open Source Vulnerability database](https://osv.dev).

```toml
[policy.osv]
  min_severity = "MEDIUM"  # LOW | MEDIUM | HIGH | CRITICAL
  action       = "block"
```

Results are cached 24 hours per version.

> 💡 **Fail-open:** If the OSV API is unreachable or returns a non-200 response, the vulnerability signal returns `skip` and the package is **allowed through**. This is intentional — a transient OSV outage should not block all package installs. If you need fail-closed behavior, mirror the OSV database locally or use `action = "warn"` so blocks require an explicit allowlist entry rather than automatic OSV approval.

### 👤 Publisher account age

```toml
[policy.publisher]
  max_account_age_days = 30
  action               = "warn"
```

For npm: reads `_npmUser` (set by the registry at publish time). Publisher lookup
results are cached 1 hour per account.

> 💡 **Fail-open:** If the npm registry API is unreachable, the publisher signal returns `skip` and the package passes through.

### 📈 Download spike detection

```toml
[policy.popularity]
  spike_factor = 10.0  # warn if downloads increased >10× week-over-week
  action       = "warn"
```

### 🐍 Block source distributions (PyPI)

```toml
[policy.pypi]
  block_sdist = true  # wheel-only; never run setup.py at install time
```

### 🚦 Policy actions

| `action` | Effect |
|---------|--------|
| `block` | Removed from manifest/metadata — tools see it as non-existent |
| `warn`  | Allowed through; event logged with WARN status |
| `allow` | Signal evaluated but never blocks (monitoring mode) |

---

## ✅ Allowlist and Blocklist

### Via dashboard

Click **Approve** on any blocked event — added to `escrow-allowlist.json` immediately.
Click **Block** on any allowed event — added to `escrow-blocklist.json`.

### Via JSON files

`escrow-allowlist.json`:
```json
[
  {
    "ecosystem": "npm",
    "name": "lodash",
    "version": "4.17.21",
    "reason": "pinned to known-good version, reviewed by security team",
    "added_by": "admin",
    "added_at": "2026-05-16T14:00:00Z"
  }
]
```

`"version": ""` is a wildcard — approves all versions of the package.

Allowlist is checked **before** any policy signal. Approved packages bypass all
trust checks and are recorded with `signal: override`.

---

## 🚢 Deployment

### 🔒 TLS (optional)

Provide a certificate and key to serve HTTPS directly:

```toml
[server]
  tls_cert_file = "/etc/ssl/escrow.crt"
  tls_key_file  = "/etc/ssl/escrow.key"
```

Or terminate TLS at nginx/caddy and set `X-Forwarded-Proto: https` — escrow
detects this and sets `Secure` on session cookies automatically.

### ⏱️ Write timeout

For large `.crate` or wheel downloads to slow clients, increase the write timeout:

```toml
[server]
  write_timeout_seconds = 300  # default 120
```

### 🚧 Rate limiting

Limit requests per IP on proxy endpoints:

```toml
[server]
  proxy_rate_limit_per_min = 600  # 0 = disabled (default)
```

### 🔗 Internal mirrors

Override the upstream URL per ecosystem to point at Nexus, Artifactory, etc.:

```toml
[ecosystems]
  npm          = true
  npm_upstream = "https://nexus.corp.internal/repository/npm-proxy/"

  pypi          = true
  pypi_upstream = "https://nexus.corp.internal/repository/pypi-proxy"

  go          = true
  go_upstream = "https://nexus.corp.internal/repository/go-proxy"

  maven                  = true
  maven_upstream         = "https://nexus.corp.internal/repository/maven-releases"
  maven_snapshot_upstream = "https://nexus.corp.internal/repository/maven-snapshots"
```

Maven SNAPSHOT requests (`path contains SNAPSHOT`) are routed to `maven_snapshot_upstream`
when set; other requests go to `maven_upstream`. Without it, all requests share one upstream.

### 🩺 Health check

`GET /healthz` probes each enabled upstream with a 3-second HEAD request:

```json
{
  "status": "ok",
  "uptime": "2h34m",
  "storage_backend": "disk",
  "upstream_status": {
    "npm": true, "pypi": true, "go": true,
    "nuget": true, "maven": true
  }
}
```

Returns HTTP 503 with `"status": "degraded"` if any upstream is unreachable.

### 💾 Disk cache

Blobs (tarballs, wheels, JARs) are cached permanently — they never expire. Monitor disk usage and plan capacity accordingly:

```bash
du -sh ./escrow-cache/blobs/    # how much blob storage is used
find ./escrow-cache/meta/ -name "*.json" | wc -l  # number of metadata entries
```

There is no built-in eviction. When disk fills, `SetBlob` fails silently and packages stop being cached (clients still receive them from upstream, but without the cache benefit). The `/healthz` endpoint returns `"cache_writable": false` when the cache directory is not writable — wire this to your alerting.

For long-running deployments, periodically clean old metadata files:
```bash
find ./escrow-cache/meta/ -name "*.json" -mtime +7 -delete
```

> ⚠️ Blobs should not be deleted — they are the cached packages and their keys are content-addressed.

### 🖥️ systemd unit

```ini
[Unit]
Description=Escrow package proxy
After=network.target

[Service]
ExecStart=/usr/local/bin/escrow --config=/etc/escrow/escrow.toml
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
```

---

## 🗄️ Storage

```toml
[storage]
  backend = "disk"         # disk | s3 | memory

  [storage.disk]
    path = "./escrow-cache"

  [storage.s3]
    bucket   = "my-escrow-cache"
    region   = "eu-west-1"
    endpoint = ""            # blank = AWS; set for MinIO/Ceph
                             # S3 uploads use temp files, not RAM buffers
```

**What is cached:**

| Content | Backend | TTL |
|---------|---------|-----|
| npm / PyPI / Composer manifests (filtered) | meta | 5 min |
| NuGet registration (filtered) | meta | 5 min |
| Maven `maven-metadata.xml` (filtered) | meta | 5 min |
| Go `.mod` files | meta | 24h |
| Go `list` responses | meta | 5 min |
| OSV vulnerability results | meta | 24h |
| Publisher account lookups | meta | 1h |
| Maven Central version timestamps | meta | 1h |
| npm / PyPI / Cargo tarballs, wheels, .crate | blob | permanent |
| Go `.zip` source archives | blob | permanent |
| NuGet `.nupkg` files | blob | permanent |
| Maven JARs, POMs, checksums | blob | permanent |

Concurrent cold-cache requests for the same manifest are **deduplicated** via
singleflight — only one upstream fetch runs regardless of how many clients ask simultaneously.

**Event log persistence** — add `eventlog_path` to persist events across restarts:

```toml
eventlog_path = "escrow-events.jsonl"  # JSONL append file; empty = in-memory only
```

Events are loaded from the file on startup (last 500). New events are appended atomically.

---

## 🔔 Alerts

Send a webhook on every block (Slack, Teams, PagerDuty, custom endpoint):

```toml
[alerts]
  webhook_url = "https://hooks.slack.com/services/..."
```

Payload:
```json
{
  "ecosystem": "npm",
  "package": "malicious@1.0.0",
  "signal": "age",
  "reason": "published 0 day(s) ago (minimum: 7)",
  "action": "block",
  "timestamp": "2026-05-17T14:03:12Z"
}
```

Webhooks are **deduplicated per package per signal** during a manifest filter — a
200-version package blocked by age sends one webhook, not 200.

---

## 🔐 Security Model

### Block by removal, not by error

For npm, PyPI, Composer, NuGet, and Maven, escrow filters blocked versions from
the package manifest before returning it. The package manager never learns the
version exists — it cannot be installed by `--force` or by a dependency resolver
fallback. For Go modules, escrow returns HTTP 403 on `.info` and `@latest`
endpoints. For Cargo, blocked versions are omitted from the sparse index NDJSON.

### Trust pipeline

```
request → allowlist → blocklist → age → osv → publisher → popularity → allow/warn/block
```

Allowlist is checked first and short-circuits all other checks — an allowlist entry
(including wildcard `"version": ""`) bypasses the blocklist and all trust signals.
Blocklist is checked second; it can block packages not on the allowlist. Signals run
in order; the first `block` decision terminates the pipeline.

### Dashboard security

- ✅ HMAC-SHA256 session cookies (HttpOnly, `SameSite=Strict`, 24h TTL)
- ✅ Timing-safe credential and HMAC comparison (`crypto/subtle`, `hmac.Equal`)
- ✅ Login rate limiting: 10 failures → 15-minute IP lockout
- ✅ CSRF protection: Origin header checked on all mutating endpoints
- ✅ Request body limit: 64 KB on all POST/DELETE endpoints
- ✅ Security headers: `Content-Security-Policy`, `X-Frame-Options`, `X-Content-Type-Options`

### No credentials forwarded

Escrow does not store, log, or forward authentication tokens. It acts as an
anonymous read-only client to public upstream registries. Private registry
authentication (`.npmrc` tokens, PyPI API keys) is not affected.

### Audit trail

Every package evaluation is recorded in the in-memory event log (last 500 events).
Dashboard allow/block/remove actions are also recorded with the operator's username.
The event stream is available via SSE (`/dashboard/api/stream`) and REST
(`/dashboard/api/events`).

---

## ⚠️ Known Limitations

### What escrow does NOT protect against

**Postinstall hooks** — Escrow filters packages from manifests but does not strip `postinstall` hooks from packages that do pass. You still need `ignore-scripts=true` (npm/pnpm), `enableScripts: false` (yarn), or `only-binary = [":all:"]` (uv) on every developer machine. See the per-tool quickstart guides in `docs/quickstart/`.

**Typosquatting on allowed packages** — If a package passes the age and vulnerability gates, escrow serves it. Detecting typosquatting requires manual allowlisting or an additional signal not yet implemented.

**git dependencies** — npm git-protocol dependencies (`npm install github:user/pkg`) bypass the package registry entirely and are not routed through escrow.

### Ecosystem limitations

**Composer ZIP archives** — The Composer handler proxies and filters the Packagist v2 metadata (which versions are visible). However, the actual ZIP archive downloads happen via `dist.url` values in the metadata, which point directly to Packagist's CDN or GitHub. Composer package archives are NOT routed through escrow and are not cached locally. Metadata air-gap is achieved; artifact air-gap is not. If Packagist CDN is unreachable, Composer installs fail.

**Unknown publish times** — When a package's publish time cannot be determined (e.g., Maven Central Search API unavailable, old Packagist entries without timestamps), the age gate treats the package as "ancient" and allows it through. This is fail-open by design to avoid blocking legitimate packages during upstream API outages.

**Publisher signal** — Publisher account age is checked for npm and PyPI only. No equivalent public API exists for Go, Cargo, NuGet, or Maven.

**OSV vulnerability scan** — When the OSV API is unreachable, the signal returns `skip` and the package passes through (fail-open). See the OSV section above for details.

---

## 🔨 Building from Source

```bash
git clone https://github.com/jverhoeks/escrow
cd escrow
go build -o escrow ./cmd/escrow
go test ./...
```

---

## 📋 Full Configuration Reference

```toml
[server]
  host                     = "127.0.0.1"  # 0.0.0.0 or --host flag for all interfaces
  port                     = 7888
  log_level                = "info"        # debug | info | warn | error
  write_timeout_seconds       = 120  # increase for slow clients downloading large archives
  read_header_timeout_seconds = 10   # time to receive full HTTP request headers (Slowloris defense)
  idle_timeout_seconds        = 120  # keep-alive connection idle limit
  tls_cert_file               = ""   # blank = HTTP only
  tls_key_file                = ""
  proxy_rate_limit_per_min    = 0    # requests/min per IP on proxy endpoints; 0 = disabled

[ecosystems]
  npm      = true
  npm_upstream = ""                  # default https://registry.npmjs.org

  pypi     = true
  pypi_upstream = ""                 # default https://pypi.org

  go       = false
  go_upstream = ""                   # default https://proxy.golang.org

  cargo    = false

  composer = false
  composer_upstream = ""             # default https://repo.packagist.org

  nuget    = false
  nuget_upstream = ""                # default https://api.nuget.org/v3
  nuget_flatcontainer_url = ""       # optional; derived from nuget_upstream for NuGet.org;
                                     # set explicitly for Nexus/Azure Artifacts which use
                                     # different URL schemes (e.g. .../repository/nuget/download)

  maven    = false                   # also covers Gradle
  maven_upstream = ""                # default https://repo1.maven.org/maven2
  maven_snapshot_upstream = ""       # route -SNAPSHOT paths here; default: same as maven_upstream

[storage]
  backend = "disk"         # disk | s3 | memory
  [storage.disk]
    path = "./escrow-cache"
  [storage.s3]
    bucket   = ""
    region   = "eu-west-1"
    endpoint = ""          # blank = AWS S3; set for MinIO

[policy]
  [policy.age]
    min_days = 7
    action   = "block"     # block | warn | allow

  [policy.osv]
    min_severity = "MEDIUM"
    action       = "block"

  [policy.publisher]
    max_account_age_days = 30
    action               = "warn"

  [policy.popularity]
    spike_factor = 10.0
    action       = "warn"

  [policy.pypi]
    block_sdist = false    # true = wheel-only installs

[dashboard]
  enabled  = true
  path     = "/dashboard"
  username = "admin"
  password = ""            # generated on first boot
  secret   = ""            # HMAC session-cookie secret; generated on first boot

[alerts]
  webhook_url = ""

allowlist_path = "escrow-allowlist.json"
blocklist_path = "escrow-blocklist.json"
eventlog_path  = ""        # JSONL file for persistent event log; empty = in-memory only
```
