# 📋 Configuration reference

[← Back to README](../README.md) · Related: [Policy & scanning](policy.md) · [Deployment](deployment.md)

Every key escrow reads from `escrow.toml`, with defaults. On first boot escrow generates this
file with a random dashboard password and HMAC secret, and prints the credentials to stdout.

You can edit most of this live from the dashboard's **Settings** page — see
[Settings & hot-reload](policy.md#-settings--hot-reload).

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

[rescan]
  enabled        = true    # re-scan downloaded versions against OSV on a schedule
  interval_hours = 24
  auto_block     = true    # auto-blocklist newly-vulnerable versions; false = alert-only
  min_severity   = ""      # defaults to policy.osv.min_severity

[dashboard]
  enabled  = true
  path     = "/dashboard"
  username = "admin"
  password = ""            # generated on first boot; required (non-empty) if you write the config by hand
  secret   = ""            # HMAC session-cookie secret; empty + enabled aborts startup (fail closed)

[alerts]
  webhook_url = ""

allowlist_path = "escrow-allowlist.json"
blocklist_path = "escrow-blocklist.json"
eventlog_path  = ""        # JSONL file for persistent event log; empty = in-memory only
```

> See [Policy & scanning](policy.md) for what each `[policy]` gate does, and
> [Deployment](deployment.md) for TLS, internal mirrors, storage backends, and webhooks.
