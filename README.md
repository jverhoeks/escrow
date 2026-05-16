# escrow

A lightweight supply-chain proxy for **npm**, **PyPI**, and **Go modules** that enforces configurable trust policies before any package reaches your developers or CI pipeline.

```
developer / CI  →  escrow proxy  →  upstream registry
                        │
                  policy engine
                  ┌─────┼─────────┐
                  age  osv  publisher  popularity
```

Packages that fail policy are **blocked at the proxy level** — they never reach the tool. Operators review blocked events in the real-time dashboard and can approve individual packages with a single click.

---

## Quick Start

### Docker

```bash
docker run -p 8888:8888 ghcr.io/jverhoeks/escrow:latest
```

### Binary

```bash
# macOS arm64
curl -L https://github.com/jverhoeks/escrow/releases/latest/download/escrow-darwin-arm64 -o escrow
chmod +x escrow
./escrow
```

On first boot escrow generates `sentinel.toml` with a random dashboard password and prints the credentials to stdout.

### Point your tools at escrow

**npm / pnpm / yarn**
```ini
# .npmrc
registry=http://localhost:8888/
```

**pip / uv**
```toml
# uv.toml
[pip]
index-url = "http://localhost:8888/pypi/simple/"
```

**Go modules**
```bash
export GOPROXY=http://localhost:8888/go,direct
```

**Cargo (Rust)**
```toml
# .cargo/config.toml
[source.crates-io]
replace-with = "escrow"

[source.escrow]
registry = "sparse+http://localhost:8888/cargo/"
```

**Composer (PHP)**
```json
{
    "repositories": [
        {
            "type": "composer",
            "url": "http://localhost:8888/composer"
        },
        {
            "packagist.org": false
        }
    ]
}
```

---

## Dashboard

The dashboard streams every package event in real-time and lets operators approve blocked packages without restarting the proxy.

```
┌─────────────────────────────────────────────────────────────────────────┐
│ ESCROW  package proxy                              ● LIVE  admin  logout │
├───────────────────────────────────┬─────────────────────────────────────┤
│ Live Feed          ECOSYSTEM: All  npm  PyPI                            │
├──────────┬──────┬─────────────────┬──────────────────────┬──────────────┤
│ TIME     │ ECO  │ PACKAGE         │ SIGNAL / REASON      │ STATUS       │
├──────────┼──────┼─────────────────┼──────────────────────┼──────────────┤
│ 14:03:12 │ npm  │ lodash@4.17.21  │ age · 2 day(s) ago   │ BLOCK Approve│
│ 14:03:09 │ pypi │ requests@2.31.0 │ osv · CVE-2023-xxxx  │ BLOCK Approve│
│ 14:03:07 │ go   │ gin@v1.9.1      │ age · 1825 days old  │ ALLOW        │
│ 14:02:55 │ npm  │ express@4.18.2  │ age · 540 days old   │ ALLOW        │
│ 14:02:48 │ npm  │ malicious@1.0.0 │ publisher · acct 2d  │ BLOCK Approve│
│ 14:02:31 │ pypi │ numpy@1.26.0    │                      │ ALLOW        │
├──────────┴──────┴─────────────────┴──────────────────────┴──────────────┤
│ age 7d  osv MEDIUM  publisher 30d  popularity 10x                       │
└─────────────────────────────────────────────────────────────────────────┘
                                         ┌────────────┐
                                         │ Blocked  3 │
                                         │ Warned   1 │
                                         │ Allowed 12 │
                                         ├────────────┤
                                         │ TOP BLOCKED│
                                         │ lodash   2x│
                                         │ malicious 1x│
                                         └────────────┘
```

Access at `http://localhost:8888/dashboard` after first boot. Credentials are printed to stdout on first run.

**Approve a blocked package:** click **Approve** next to any blocked event. The entry is added to the persistent allowlist (`escrow-allowlist.json`) immediately. The status changes to **OVERRIDE** in purple. No restart needed.

---

## Policy Configuration

All policy is configured in `sentinel.toml`. Without a `[policy]` section escrow proxies transparently (with a startup warning).

### Age gate

Block packages published fewer than N days ago. Catches typosquatting attacks that publish and spread quickly before being detected.

```toml
[policy.age]
  min_days = 7       # require packages to be at least 7 days old
  action   = "block" # block | warn | allow
```

| `min_days` | Effect |
|-----------|--------|
| 1 | Catch same-day injections |
| 7 | Recommended baseline — most legitimate releases spread within a week |
| 30 | High-security environments |

### OSV vulnerability scan

Check every package version against the [Open Source Vulnerability database](https://osv.dev) before allowing it through.

```toml
[policy.osv]
  min_severity = "MEDIUM"  # LOW | MEDIUM | HIGH | CRITICAL
  action       = "block"
```

Escrow queries OSV at request time with a per-package cache. Packages with known vulnerabilities at or above the configured severity are blocked. The signal reports the CVE ID and severity in the dashboard.

### Publisher account age

Block packages whose publisher account was created too recently. New accounts are a common vector for supply-chain attacks.

```toml
[policy.publisher]
  max_account_age_days = 30  # block if publisher account is younger than this
  action               = "warn"
```

For npm, escrow reads `_npmUser` (set by the registry at publish time) and falls back to `maintainers[0]`. The account creation date is fetched from the npm registry API.

### Download spike detection

Warn when a package's download count spikes suddenly — a signal that a package may have been hijacked or is being used in a coordinated campaign.

```toml
[policy.popularity]
  spike_factor = 10.0  # alert if downloads increased >10x week-over-week
  action       = "warn"
```

### Block source distributions (PyPI)

Source distributions run arbitrary code at install time via `setup.py`. Wheel-only mode forces pip to use pre-built binary distributions.

```toml
[policy.pypi]
  block_sdist = false  # set true to enforce wheel-only installs
```

### Policy actions

| `action` | Effect |
|---------|--------|
| `block` | Package version is removed from the manifest. Tools see it as non-existent. |
| `warn`  | Package is allowed through. Event logged with `WARN` status. |
| `allow` | Signal is evaluated but never triggers a block (useful for monitoring). |

---

## Allowlist — Approving Packages

When a package is blocked you have two ways to approve it:

### Via the dashboard (recommended)

Click **Approve** on any blocked event. The package version is added to the allowlist and takes effect immediately.

### Via the JSON file

Edit `escrow-allowlist.json` (hot-reloaded on next request):

```json
[
  {
    "ecosystem": "npm",
    "name": "lodash",
    "version": "4.17.21",
    "reason": "pinned to known-good version, reviewed by security team",
    "added_by": "admin",
    "added_at": "2026-05-16T14:00:00Z"
  },
  {
    "ecosystem": "pypi",
    "name": "requests",
    "version": "",
    "reason": "all versions approved — CVE does not apply to our usage",
    "added_by": "admin",
    "added_at": "2026-05-16T14:01:00Z"
  }
]
```

**`version: ""`** is a wildcard — it approves all versions of that package.

The allowlist is checked **before** any policy signal. Approved packages bypass all trust checks and are recorded with `signal: override` in the event log.

---

## Ecosystem Support

| Ecosystem | Proxy path | Tool configuration |
|-----------|-----------|-------------------|
| npm | `/` | `.npmrc`: `registry=http://localhost:8888/` |
| PyPI (pip) | `/pypi/simple/` | `pip.conf` or `uv.toml`: `index-url = http://localhost:8888/pypi/simple/` |
| Go modules | `/go/` | `GOPROXY=http://localhost:8888/go,direct` |
| Cargo (Rust) | `/cargo/` | `.cargo/config.toml`: `registry = "sparse+http://localhost:8888/cargo/"` |
| Composer (PHP) | `/composer/` | `composer.json`: `{"repositories":[{"type":"composer","url":"http://localhost:8888/composer"},{"packagist.org":false}]}` |

Enable each ecosystem in config:

```toml
[ecosystems]
  npm      = true
  pypi     = true
  go       = false  # off by default
  cargo    = false  # off by default
  composer = false  # off by default
```

---

## Storage

Escrow caches upstream responses locally to reduce latency and upstream load.

```toml
[storage]
  backend = "disk"   # disk | s3 | memory

  [storage.disk]
    path = "./escrow-cache"

  [storage.s3]
    bucket   = "my-escrow-cache"
    region   = "eu-west-1"
    endpoint = ""       # leave blank for AWS; set for MinIO/Ceph
```

---

## Alerts

Send a webhook on every block event (Slack, Teams, PagerDuty, custom):

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
  "action": "block"
}
```

---

## Security Model

### Transparent proxy — no client changes needed

Escrow speaks the native protocol for each ecosystem. Developers and CI pipelines configure the proxy URL once; no additional tooling or agents are required.

### Block by removal, not by error

For npm and PyPI, escrow filters blocked versions out of the package manifest before returning it. The package manager never learns that the version exists — it cannot be installed by accident, by `--force`, or by a dependency resolver fallback. For Go modules, the proxy returns HTTP 403 on `.info` and `@latest` endpoints, which the Go toolchain treats as "unavailable".

### Allowlist is checked first, evaluated once

The policy engine evaluates allowlist entries before any trust signal. An approved package is never re-evaluated against age, OSV, or publisher checks — it is fast-pathed through. Allowlist changes take effect on the next request without a restart.

### No credentials stored or forwarded

Escrow does not store, log, or forward authentication tokens. It acts as an anonymous read-only client to public upstream registries. Private registry authentication (`.npmrc` tokens, PyPI API keys) is handled between the developer's tool and upstream — escrow sits in front of the public registry only.

### Dashboard is isolated from proxy

The dashboard (`/dashboard`) is served on the same port but authenticated separately with HMAC-SHA256 session cookies (HttpOnly, SameSite=Lax, 24h TTL). Unauthenticated requests to dashboard API endpoints return 401. The dashboard has no write access to proxy internals; the only mutation it can perform is adding allowlist entries.

### XSS-safe dashboard

The dashboard HTML is built entirely with DOM methods (`createElement`, `textContent`, `appendChild`). Dynamic data from the event stream is never interpolated into `innerHTML`. This prevents stored XSS even if a malicious package name contains HTML or JavaScript.

### Audit trail

Every package evaluation is recorded in the in-memory event log (last 500 events) and available via the SSE stream and `/api/events`. The allowlist records who approved each entry and when. Webhook events provide an external audit trail.

---

## Building from Source

```bash
git clone https://github.com/jverhoeks/escrow
cd escrow
go build -o escrow ./cmd/escrow
go test ./...
bash tests/e2e.sh ./escrow
```

---

## Full Configuration Reference

```toml
[server]
  host      = "0.0.0.0"
  port      = 8888
  log_level = "info"    # debug | info | warn | error

[ecosystems]
  npm      = true
  pypi     = true
  go       = false
  cargo    = false
  composer = false

[storage]
  backend = "disk"      # disk | s3 | memory
  [storage.disk]
    path = "./escrow-cache"
  [storage.s3]
    bucket   = ""
    region   = "eu-west-1"
    endpoint = ""

[policy]
  [policy.age]
    min_days = 7
    action   = "block"

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
    block_sdist = false

[dashboard]
  enabled  = true
  path     = "/dashboard"
  username = "admin"
  password = "generated-on-first-boot"
  secret   = "generated-on-first-boot"

[alerts]
  webhook_url = ""

allowlist_path = "escrow-allowlist.json"
```
