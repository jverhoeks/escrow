# 🚀 GitHub Actions — Escrow Quickstart

Use escrow as a supply-chain gate in any CI pipeline with a single step.  
All subsequent `npm ci`, `pip install`, `go build`, `cargo build`, `dotnet restore`,
and `mvn` commands transparently route through the proxy with age-gate and CVE
enforcement — no changes to your build steps required.

> **Integration test repo**: [jverhoeks/escrow-test](https://github.com/jverhoeks/escrow-test)
> confirms all 7 ecosystems pass on every push.

---

## Minimal setup

```yaml
- uses: jverhoeks/escrow@v1
  with:
    ecosystems: 'npm,pypi,go,cargo'
    min-days: '7'
    osv-severity: 'HIGH'
```

## Full example

```yaml
name: CI

on: [push, pull_request]

jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6

      - uses: jverhoeks/escrow@v1
        with:
          ecosystems: 'npm,pypi,go,cargo,nuget,maven'
          min-days: '7'
          osv-severity: 'HIGH'

      # Your build steps — unchanged
      - uses: actions/setup-node@v6
        with:
          node-version: '20'
      - run: npm ci

      - uses: actions/setup-python@v6
        with:
          python-version: '3.12'
      - run: pip install -r requirements.txt

      - uses: actions/setup-go@v6
        with:
          go-version: '1.22'
      - run: go build ./...
```

---

## Inputs

| Input | Default | Description |
|---|---|---|
| `ecosystems` | `npm,pypi,go,cargo` | Comma-separated ecosystems to enable: `npm`, `pypi`, `go`, `cargo`, `nuget`, `maven`, `composer` |
| `min-days` | `7` | Age gate — block packages published fewer than N days ago |
| `osv-severity` | `HIGH` | Block packages with CVEs at this severity or above: `LOW`, `MEDIUM`, `HIGH`, `CRITICAL`, or `off` |
| `version` | `v1.4.1` | Escrow binary version to install |
| `port` | `7888` | Local proxy port |
| `cache-key-suffix` | | Append to the cache key for manual cache busting |

## Outputs

| Output | Example | Description |
|---|---|---|
| `proxy-url` | `http://127.0.0.1:7888` | Escrow proxy base URL |

---

## What it configures automatically

| Ecosystem | Env var / file set |
|---|---|
| npm | `NPM_CONFIG_REGISTRY=http://127.0.0.1:7888` |
| pypi | `PIP_INDEX_URL`, `UV_INDEX_URL` → `/pypi/simple/` |
| go | `GOPROXY=http://127.0.0.1:7888/go,direct` |
| cargo | `~/.cargo/config.toml` — sparse registry added |
| nuget | `~/.nuget/NuGet.Config` — escrow source, `allowInsecureConnections=true` |
| maven | `~/.m2/settings.xml` — central mirror |

---

## Caching

The action stores all downloaded packages in `~/.escrow-cache` and restores it
via `actions/cache@v5` on every run.

**Cache key** (auto-generated):

```
escrow-{os}-days{min-days}-{hash of lockfiles}
```

Lockfiles hashed: `package.json`, `package-lock.json`, `requirements*.txt`,
`pyproject.toml`, `go.sum`, `Cargo.lock`, `pom.xml`, `build.gradle*`.

- **Cache hit** → packages served from disk, zero upstream calls, instant
- **Cache miss** → fetched through escrow (age-gated + CVE-scanned), cached for next run
- **Lockfile change** → automatic cache bust, new packages re-evaluated

---

## Age gate enforcement

Set `min-days: '99999'` to block **all** packages (useful in a dedicated
policy-check job):

```yaml
- uses: jverhoeks/escrow@v1
  with:
    ecosystems: 'npm,pypi'
    min-days: '99999'
    osv-severity: 'off'

- run: npm install   # fails — age gate blocks all versions
```

Blocked packages appear in the dashboard with an **Approve** button.  
Approved packages are added to `escrow-allowlist.json` and pass immediately.

---

## Version pinning

```yaml
# Exact version — recommended for production
uses: jverhoeks/escrow@v1.4.1

# Floating major — gets patch/minor updates automatically
uses: jverhoeks/escrow@v1
```

---

## Troubleshooting

**`Process completed with exit code 2` on "Install escrow"**  
The binary for your platform is missing from the release. Open an issue at
[jverhoeks/escrow](https://github.com/jverhoeks/escrow/issues).

**`npm install` succeeds despite age gate**  
Check that `NPM_CONFIG_REGISTRY` is set to the escrow URL. If you have a
`.npmrc` committed with `registry=...`, it takes precedence — remove it or
add `registry=http://127.0.0.1:7888` to it.

**Go checksum mismatch**  
Your `go.sum` was generated against a different version of the module. Run
`GOPROXY=https://proxy.golang.org go mod tidy` locally to regenerate it, then
commit the updated `go.sum`.

**NuGet `CS0103: Console does not exist`**  
Add `<ImplicitUsings>enable</ImplicitUsings>` to your `.csproj`.
