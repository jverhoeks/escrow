# 🚢 Deployment, storage & alerts

[← Back to README](../README.md) · Related: [Configuration reference](configuration.md) · [Security model](security.md)

Running escrow as a shared/team instance: TLS, timeouts, internal mirrors, health checks,
caching, storage backends, and webhook alerts.

---

## Deployment

### 🔒 TLS (optional)

Provide a certificate and key to serve HTTPS directly:

```toml
[server]
  tls_cert_file = "/etc/ssl/escrow.crt"
  tls_key_file  = "/etc/ssl/escrow.key"
```

Or terminate TLS at nginx/caddy and set `X-Forwarded-Proto: https` — escrow detects this and
sets `Secure` on session cookies automatically.

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

Concurrent cold-cache requests for the same manifest are **deduplicated** via singleflight —
only one upstream fetch runs regardless of how many clients ask simultaneously.

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

Webhooks are **deduplicated per package per signal** during a manifest filter — a 200-version
package blocked by age sends one webhook, not 200.
