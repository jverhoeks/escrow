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

---

## 🛡️ Availability, restart impact & SPOF

escrow runs as a **single process with a single listener**. There is no built-in clustering,
leader election, or automatic failover — a stopped process is a full outage for the lanes it
serves. This is by design (escrow is a *policy gate*, not a CDN), but it has operational
consequences you should plan for.

### What a restart costs

The registry lane is **fail-closed**: while escrow is down, package managers pointed at it get
connection errors and builds fail — they do **not** silently fall back to the public registry
(that is the point — no unvetted packages slip through). In-flight requests are dropped.

So a restart is a brief, deliberate build outage. Minimise how often you need one by knowing
what applies **live** versus what needs a restart:

| Change | How it applies |
|--------|----------------|
| `policy` (age / osv / publisher / popularity), `strict_signals` | **Live** — SIGHUP / `escrow-cli reload` / dashboard **Save** |
| `rescan` (CVE re-scanner) | **Live** |
| `alerts.webhook_url` (if a webhook existed at startup) | **Live** |
| **dashboard credentials** (`username` / `password` / `secret`) | **Live** — rotating `secret` invalidates existing sessions (re-login) |
| allow / block list entries | **Live** (edited via dashboard/CLI, persisted to file) |
| `server` (host / port / TLS cert+key), `storage` backend/path, `ecosystems`, file `paths`, `egress_proxy` | **Restart required** |

A live reload (`escrow-cli reload`, SIGHUP, or dashboard **Save**) re-reads `escrow.toml`, applies
the live subset **without dropping connections**, and reports any restart-required sections back
to you. TLS cert rotation and port/binding changes are the main restart triggers.

### State is file-based (and mostly reconstructible)

| State | Reconstructible? | Notes |
|-------|------------------|-------|
| Disk/S3 cache — blobs (content-addressed) + filtered metadata | **Yes** | Re-fetched from upstream on demand; safe to lose |
| **Allow list / block list files** | **No** | This *is* your security policy — **back these up** |
| `escrow.toml` | No | Back up (or keep in config management) |
| Event log / access log JSONL | Yes (observability only) | Append-only history |
| `escrow.pid` | Yes | Recreated at startup |

### Running without a single point of failure

Because the cache is only an optimisation and policy evaluation is deterministic, you can run
**2+ instances behind a reverse proxy / load balancer / VIP** without shared session state:

- **Shared cache (optional):** use the **S3 backend** — it is safe for concurrent instances
  (blobs are content-addressed; metadata is short-TTL). A disk cache on a shared network FS also
  works; concurrent metadata writes are last-writer-wins, which is harmless (short TTL, re-fetched).
  Or give each instance its own cache — they stay consistent because filtering is deterministic.
- **Identical policy across instances:** the allow/block lists and `escrow.toml` **must match** on
  every instance. Keep them in config management (or a shared read path) and `escrow-cli reload`
  each instance after a policy change.
- **Rolling upgrades / cert rotation with zero downtime:** drain one instance at the LB, restart
  it, wait for `/healthz` to return `ok`, then move to the next. No global outage.

Even single-instance, run under `systemd` with `Restart=on-failure` (see the unit above) and
alert on `/healthz` so a crash self-heals and pages you.

---

## 🚨 Runbook

Incident playbook, capacity guidance, and backup/restore for the on-call who didn't write escrow.

### 3 AM playbook

| Symptom | What's happening | Do this |
|---------|------------------|---------|
| **Builds failing, `/healthz` shows an upstream `false`** | That registry's upstream is unreachable; cached **blobs** still serve, but uncached metadata 502s | Check upstream/network. To ride out the outage, set `storage.stale_on_error_max_age_m` > 0 to serve recently-expired metadata stale (opt-in — see caveat below), then `escrow-cli reload`. |
| **Lots of blocks / unexpected blocks after a trust source (OSV) outage** | A failed OSV/publisher/popularity fetch is a *signal error*. With `policy.strict_signals = true` escrow **fails closed** (blocks); with it `false` the package passes without that one check | OSV results are cached 24h, so brief outages are absorbed. For a prolonged outage decide strict-vs-lenient for your risk posture and `escrow-cli reload`. Note: `/healthz` probes **registry** upstreams only — trust-source errors surface in the logs and the dashboard event feed. |
| **`/healthz` returns `"cache_writable": false`** (disk full) | `SetBlob`/`SetMeta` are failing (logged at WARN; `escrow_cache_write_failures_total` climbs). Clients are still served from upstream — just uncached, so latency rises | Free space: `find <cache>/meta -name '*.json' -mtime +7 -delete`; expand the volume; or set `[storage.disk] max_size_gb` to cap with FIFO purge. **Do not** hand-delete blobs unless you accept re-download. |
| **S3 backend: cache writes failing, `/healthz` degraded** | S3 unreachable (the health probe does a `HeadBucket`). Reads miss and fall through to upstream | Check S3 credentials / endpoint / network. escrow keeps serving from upstream meanwhile. |
| **Process won't start / crash-looping** | Usually a config error or a port/bind conflict | Startup logs show `Validate()` errors. Check the port isn't taken, the TLS files exist, and the cache path is writable. Fix config, then start. |

> ⚠️ **Stale-on-error caveat:** `storage.stale_on_error_max_age_m` trades a sliver of safety for
> availability — a stale manifest can briefly re-expose a version you blocked by manifest-removal
> (if its blob was cached before the block). It is **disabled (0) by default**. Enable it only if
> surviving upstream outages matters more than that window.

### Capacity & headroom

- **Blobs grow unbounded by default** and never expire — this is the dominant growth. Cap it with
  `[storage.disk] max_size_gb` (FIFO purge) or use S3. Watch `du -sh <cache>/blobs`.
- **Upstream connection pool:** escrow keeps up to **256** idle upstream connections (20 per host)
  — ample for a handful of ecosystems; raise it if you front many distinct upstream hosts.
- **Request body limit:** `server.max_request_body_mb` (default **100**) caps client upload bodies;
  set `0` for unlimited.
- **Rate limits:** `server.proxy_rate_limit_per_min` (registry lane) and
  `egress_proxy.rate_limit_per_min` (egress lane) bound per-IP request rate.
- **Memory:** the in-memory `memory` storage backend is **unbounded** — dev/test only, never a
  shared instance. Log rings (event/access/upstream/egress) are bounded.

### Backup & restore

Back up the **policy**, not the cache:

```bash
# The durable, non-reconstructible state:
cp escrow.toml          /backup/
cp <allowlist_path>     /backup/   # e.g. allowlist.toml
cp <blocklist_path>     /backup/   # e.g. blocklist.toml
```

Keep these in version control or config management — they *are* your security posture, and the
dashboard/CLI edit them live, so snapshot after any policy change. **Restore** = drop the three
files back, start escrow; the cache re-warms from upstream on first use (nothing else to restore).

### Monitoring & alerting

- **`/metrics`** (Prometheus): wire alerts on `escrow_cache_write_failures_total` > 0, a sustained
  5xx ratio (`escrow_responses_total{class="5xx"}`), a spike in `escrow_blocks_total`, and the
  `escrow_proxy_request_duration_seconds` latency histogram.
- **`/healthz`**: alert on HTTP 503 (`"status":"degraded"`) and `"cache_writable": false`.
- **Webhook alerts** (`[alerts] webhook_url`): one webhook per block, deduplicated per package/signal.
