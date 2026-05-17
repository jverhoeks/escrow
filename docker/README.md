# 🐳 Docker — Debug / Demo Setup

Pre-configured escrow instance with all 7 ecosystems enabled and full policy active.

> ⚠️ **Not for production.** Credentials are `admin` / `escrow` and the HMAC secret is hardcoded.
> Run `./escrow` directly with a generated `sentinel.toml` for production.

---

## Quick start

```bash
cd docker/

# 1. Create the data directory and copy the debug config
mkdir -p data
cp sentinel.debug.toml data/sentinel.toml

# 2. Start escrow
docker compose up -d

# 3. Open the dashboard
open http://localhost:7888/dashboard
# Login: admin / escrow
```

---

## What's enabled

| Setting | Value |
|---------|-------|
| Port | 7888 |
| Log level | debug |
| Ecosystems | npm, PyPI, Go, Cargo, Composer, NuGet, Maven |
| Age gate | 7 days — block |
| OSV scan | MEDIUM+ — block |
| Publisher check | 30-day account age — warn |
| Popularity spike | 10× — warn |
| Dashboard | http://localhost:7888/dashboard |
| Credentials | admin / escrow |
| Event log | persisted to `data/escrow-events.jsonl` |

---

## Point your tools at the debug instance

```ini
# npm / pnpm / bun — .npmrc
registry=http://localhost:7888
```

```toml
# uv / pip — uv.toml
[pip]
index-url = "http://localhost:7888/pypi/simple/"
```

```bash
# Go
export GOPROXY=http://localhost:7888/go,off
```

```toml
# Cargo — .cargo/config.toml
[source.crates-io]
replace-with = "escrow"
[source.escrow]
registry = "sparse+http://localhost:7888/cargo/"
```

```xml
<!-- NuGet — nuget.config -->
<configuration>
  <packageSources>
    <clear />
    <add key="escrow" value="http://localhost:7888/nuget/index.json" />
  </packageSources>
</configuration>
```

```xml
<!-- Maven — settings.xml -->
<mirror>
  <id>escrow</id>
  <url>http://localhost:7888/maven2</url>
  <mirrorOf>central</mirrorOf>
</mirror>
```

---

## Data layout

```
docker/
├── data/                        ← mounted as /data in the container
│   ├── sentinel.toml            ← config (copied from sentinel.debug.toml)
│   ├── escrow-cache/            ← blob + manifest cache
│   ├── escrow-allowlist.json    ← approved packages
│   ├── escrow-blocklist.json    ← manually blocked packages
│   └── escrow-events.jsonl      ← persistent event log
├── sentinel.debug.toml          ← source config (edit this, then re-copy)
├── docker-compose.yml
└── README.md
```

The `data/` directory is gitignored — your cache and lists stay local.

---

## Useful commands

```bash
# View live logs
docker compose logs -f

# Check health and upstream status
curl -s http://localhost:7888/healthz | python3 -m json.tool

# Stop
docker compose down

# Wipe cache and restart fresh
docker compose down && rm -rf data/escrow-cache && docker compose up -d

# Update to latest image
docker compose pull && docker compose up -d
```
