---
title: "Renovate + Supply Chain: What It Actually Intercepts (and What It Doesn't)"
description: "We tested Renovate's registry behaviour for every ecosystem — npm, PyPI, Go, Cargo, Maven, NuGet, Composer — and found that minimumReleaseAge works for all of them, but how each datasource resolves its registry is surprisingly different. Here's what we found and how to build a complete defence."
pubDate: "2026-05-27"
author: "Jacob Verhoeks"
tags:
  - "renovate"
  - "supply-chain"
  - "cargo"
  - "rust"
  - "npm"
  - "python"
  - "go"
  - "security"
  - "escrow"
---

Renovate is a popular automated dependency update tool. When combined with an age-gating proxy like [escrow](https://github.com/jverhoeks/escrow), it can be part of a strong supply chain defence. But Renovate's registry behaviour varies significantly by ecosystem — and some of those differences matter for security.

We ran live tests against every ecosystem. Here's what we found.

---

## TL;DR

| Ecosystem | Renovate version lookup | `minimumReleaseAge` works? | Auto-uses proxy? |
|---|---|---|---|
| **npm** | Direct to npmjs.org | ✅ | ❌ rejects localhost |
| **PyPI** | Via `PIP_INDEX_URL` env | ✅ | ✅ auto-detected |
| **Go** | Via `GOPROXY` env | ✅ | ✅ auto-detected |
| **Cargo** | Direct to crates.io | ✅ tested | ❌ git-clone only |
| **Maven** | Via pom.xml `<repositories>` | ✅ | ⚠️ needs pom.xml |
| **NuGet** | Merge strategy | ✅ | ⚠️ needs config |
| **Composer** | Hunt strategy | ✅ | ⚠️ needs config |

`minimumReleaseAge` works for **all ecosystems** — it's applied after Renovate fetches release data, regardless of where that data came from.

The proxy routing is what varies.

---

## Testing `minimumReleaseAge` for Cargo

We set up a test with tokio, pinning to `=1.51.0`, and configured `minimumReleaseAge: "21 days"`. At test time the release landscape was:

| Version | Age | Expected |
|---|---|---|
| 1.52.3 | 19 days | ❌ blocked by age gate |
| 1.52.2 | 23 days | ✅ should be proposed |
| 1.51.3 | 19 days | ❌ blocked |
| 1.51.2 | 23 days | ✅ available |

Renovate's output:

```json
"newVersion": "1.52.2",
"newVersionAgeInDays": 23,
"pendingVersions": ["1.52.3"]
```

**Result: correct.** Renovate proposed 1.52.2, filed 1.52.3 as `pendingVersions` (will propose when it ages past 21 days), and skipped 1.51.3. The age gate works exactly as designed.

---

## Why npm rejects localhost

Renovate has an explicit security check in its npm datasource that **rejects any `.npmrc` file containing a localhost registry**:

```js
if (key.endsWith('registry') && val.includes('localhost')) {
    logger.debug('Detected localhost registry - rejecting npmrc file');
    npmrc = existingNpmrc;
    return;
}
```

This is intentional. Without it, a malicious `renovate.json` in a repository could redirect Renovate's npm version lookups to a local attacker-controlled server. So even if your `~/.npmrc` has `registry=http://127.0.0.1:7888/`, Renovate will ignore it and go directly to `registry.npmjs.org`.

**What this means for you:** `minimumReleaseAge` still works — Renovate fetches from npmjs.org and applies the age filter there. The escrow proxy still protects the actual `npm install` step. Renovate just won't use the proxy for *version discovery*.

---

## Why Cargo can't use a sparse HTTP proxy

Renovate's crate datasource tries to **git-clone** custom registry URLs to fetch the crate index. Escrow serves a **sparse HTTP registry** (the protocol `cargo install` uses). These are incompatible:

```
Renovate:     git clone http://127.0.0.1:7888/cargo/  ← fails, not a git repo
cargo build:  GET http://127.0.0.1:7888/cargo/...     ← works, sparse HTTP
```

For version discovery, Renovate always goes directly to `https://crates.io/api/v1/`. For downloading crates, `cargo build` goes through escrow via `~/.cargo/config.toml` source replacement.

**The right model:**

```
Renovate: "tokio 1.52.3 exists" → queries crates.io directly (discovery)
CI/dev:   cargo build            → escrow checks age + CVE (enforcement)
```

---

## The complete defence for Cargo

Since Renovate bypasses escrow for version discovery, the defence needs to be layered:

### 1. `minimumReleaseAge` in renovate.json (blocks PR before it opens)

```json
{
  "packageRules": [{
    "matchManagers": ["cargo"],
    "minimumReleaseAge": "7 days"
  }]
}
```

### 2. `cargo audit` postUpgradeTasks (CVE check before PR opens)

```json
{
  "postUpgradeTasks": {
    "commands": ["cargo audit --deny warnings"],
    "fileFilters": ["Cargo.lock"],
    "executionMode": "branch"
  }
}
```

### 3. escrow in CI (age + CVE gate before PR merges)

```yaml
# .github/workflows/cargo-escrow.yml
- uses: jverhoeks/escrow@v1
  with:
    ecosystems: 'cargo'
    min-days: '7'
    osv-severity: 'HIGH'

- run: cargo build --locked
```

### 4. escrow on dev machine (final gate at actual build)

```bash
# ~/.cargo/config.toml (written by: escrow-cli config write --ecosystems cargo)
[source.crates-io]
replace-with = "escrow"

[source.escrow]
registry = "sparse+http://127.0.0.1:7888/cargo/"
```

The combined timeline:

```
Renovate: suggests tokio 1.52.3 (19d old)
  → minimumReleaseAge: "7 days" → ✅ passes (> 7d)
  → cargo audit → ✅ no CVEs
  → PR opened
  → CI: cargo build through escrow
       → escrow age gate: 19 days ✓
       → escrow OSV scan: ✓
  → PR mergeable
  → developer merges
  → dev machine: cargo build through escrow → ✓ again
```

A package has to pass two independent age checks (Renovate + escrow) and two independent CVE checks (cargo audit + escrow OSV) before it gets installed.

---

## PyPI and Go: the happy path

These two ecosystems work automatically with the escrow proxy:

- **PyPI**: Renovate reads `PIP_INDEX_URL` env var as its default datasource URL. When the escrow GitHub Action exports `PIP_INDEX_URL=http://127.0.0.1:7888/pypi/simple/`, Renovate in CI will use the proxy for version lookups.
- **Go**: Same pattern via `GOPROXY`.

No extra configuration needed for these.

---

## Maven: only works if pom.xml says so

Renovate's Maven datasource uses a `registryStrategy: 'merge'` — it queries all configured registries plus the default Maven Central. The configured registries come from the pom.xml `<repositories>` section, not from `~/.m2/settings.xml`.

If your pom.xml has:
```xml
<repositories>
  <repository>
    <id>escrow</id>
    <url>http://127.0.0.1:7888/maven2/</url>
  </repository>
</repositories>
```

Renovate will query your proxy **and** Maven Central. Without the `<repositories>` section, Renovate goes directly to Maven Central regardless of `~/.m2/settings.xml`.

For teams using escrow server-side (not on `localhost`), the `renovate.json` entry works:
```json
{
  "maven": { "registryUrls": ["https://escrow.internal/maven2/"] }
}
```

---

## Renovate for localhost proxies: what actually works

| Config approach | Works? | Notes |
|---|---|---|
| `~/.npmrc registry=http://localhost:7888/` | ❌ | Renovate explicitly rejects localhost |
| `NPM_CONFIG_REGISTRY` env | ❌ | Same rejection |
| `PIP_INDEX_URL` env | ✅ | Auto-detected |
| `GOPROXY` env | ✅ | Auto-detected |
| `renovate.json registryUrls` for cargo | ❌ | git-clone, not sparse HTTP |
| `renovate.json registryUrls` for maven | ✅ | If escrow is network-accessible |
| `minimumReleaseAge` in renovate.json | ✅ | Works for all ecosystems |
| `postUpgradeTasks: cargo audit` | ✅ | Works for cargo |
| escrow in CI workflow | ✅ | Full protection for any ecosystem |

**The core insight:** Renovate is a version *discovery* tool. Your proxy is a version *enforcement* tool. They don't need to be the same service. Wire them together through CI, not through registry routing.
