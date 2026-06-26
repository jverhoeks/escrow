# 🔐 Security model & threat coverage

[← Back to README](../README.md) · Related: [Policy & scanning](policy.md) · [Why escrow?](../README.md#-why-escrow)

escrow is deliberately explicit about what it does and does **not** defend against. Lead with
the honest version — it's the strongest reason to trust the rest.

---

## 🛡️ What escrow protects against

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

## Security model

### Block by removal, not by error

For npm, PyPI, Composer, NuGet, and Maven, escrow filters blocked versions from the package
manifest before returning it. The package manager never learns the version exists — it cannot
be installed by `--force` or by a dependency resolver fallback. For Go modules, escrow returns
HTTP 403 on `.info` and `@latest` endpoints. For Cargo, blocked versions are omitted from the
sparse index NDJSON.

### Enforcement on the download path

Manifest filtering hides a blocked version from resolution, but the artifact endpoint is
reachable directly — a pinned lockfile URL, a version auto-blocked by a rescan after it was
already cached, or a rewritten artifact URL. So the download endpoints (npm tarball, PyPI
wheel/sdist, Cargo crate, NuGet `.nupkg`, Maven jar/war/ear/aar, Go `.zip`) independently
re-evaluate the full trust engine and policy before serving any bytes, on both cache-miss and
cache-hit paths. A blocked or known-vulnerable version returns **HTTP 403** with no artifact
bytes, even when warm in the cache, and the block is recorded as a download event. This closes
the gap where a pinned or auto-blocked version could still be fetched. Concretely the download
gate enforces **blocklist + OSV** — the age gate's job (hiding fresh versions from resolution)
is already done at listing, and no metadata fetch is added to the download hot path. Composer
artifacts download from the Packagist/GitHub CDN, not through escrow, so this enforcement does
not apply (see below).

### Stale-on-error serving vs. blocked versions

When `storage.stale_on_error_max_age_m` is set, escrow may serve recently-expired metadata if
the upstream is unreachable. This does **not** re-expose a blocked version: adding a block (or a
rescan auto-block) invalidates cached metadata, so the stale copy is dropped and a stale listing
can never include the just-blocked version; and even if a stale manifest listed a version that
later became blocked, the **download gate still returns 403** for it. The only residual is a
signal-drift case — a version newly filtered by the *age* or *OSV* signal (no blocklist entry,
so no cache invalidation) can remain listed in a stale manifest until the metadata TTL elapses —
which is bounded by the TTL and still blocked at download.

### Artifact integrity verification

For **PyPI**, escrow verifies a downloaded wheel/sdist against the upstream-declared `sha256`
(from the JSON API, the same digest PyPI publishes in the PEP 503 href) before caching or
serving it: bytes are streamed to a temp file while hashing, and a mismatch is **rejected** —
never cached, never served. This is defense-in-depth (clients run their own checks) against a
tampered/MITM'd upstream or a corrupted cache blob. When no digest is available (a pinned/cold
fetch or an old release) escrow serves unverified and logs it (fail open). Digest verification
for npm (`dist.integrity`), NuGet, and Maven (`.sha1`) is not yet implemented.

### Trust pipeline

```
request → allowlist → blocklist → age → osv → publisher → popularity → allow/warn/block
```

Allowlist is checked first and short-circuits all other checks — an allowlist entry (including
wildcard `"version": ""`) bypasses the blocklist and all trust signals. Blocklist is checked
second; it can block packages not on the allowlist. Signals run in order; the first `block`
decision terminates the pipeline.

### Dashboard security

- ✅ HMAC-SHA256 session cookies (HttpOnly, `SameSite=Strict`, 24h TTL)
- ✅ Timing-safe credential and HMAC comparison (`crypto/subtle`, `hmac.Equal`)
- ✅ Login rate limiting: 10 failures → 15-minute IP lockout
- ✅ CSRF protection: Origin header checked on all mutating endpoints
- ✅ Request body limit: 64 KB on all POST/DELETE endpoints
- ✅ Security headers: `Content-Security-Policy`, `X-Frame-Options`, `X-Content-Type-Options`

### No credentials forwarded

Escrow does not store, log, or forward authentication tokens. It acts as an anonymous
read-only client to public upstream registries. Private registry authentication (`.npmrc`
tokens, PyPI API keys) is not affected.

### Audit trail

Every package evaluation is recorded in the in-memory event log (last 500 events). Dashboard
allow/block/remove actions are also recorded with the operator's username. The event stream is
available via SSE (`/dashboard/api/stream`) and REST (`/dashboard/api/events`).

---

## ⚠️ Known limitations

### What escrow does NOT protect against

**Postinstall hooks** — Escrow filters packages from manifests but does not strip `postinstall` hooks from packages that do pass. You still need `ignore-scripts=true` (npm/pnpm), `enableScripts: false` (yarn), or `only-binary = [":all:"]` (uv) on every developer machine. See the per-tool quickstart guides in [`quickstart/`](quickstart/).

**Typosquatting on allowed packages** — If a package passes the age and vulnerability gates, escrow serves it. Detecting typosquatting requires manual allowlisting or an additional signal not yet implemented.

**git dependencies** — npm git-protocol dependencies (`npm install github:user/pkg`) bypass the package registry entirely and are not routed through escrow.

### Ecosystem limitations

**Composer ZIP archives** — The Composer handler proxies and filters the Packagist v2 metadata (which versions are visible). However, the actual ZIP archive downloads happen via `dist.url` values in the metadata, which point directly to Packagist's CDN or GitHub. Composer package archives are NOT routed through escrow and are not cached locally. Metadata air-gap is achieved; artifact air-gap is not. If Packagist CDN is unreachable, Composer installs fail.

**Unknown publish times** — When a package's publish time cannot be determined (e.g., Maven Central Search API unavailable, old Packagist entries without timestamps), the age gate treats the package as "ancient" and allows it through. This is fail-open by design to avoid blocking legitimate packages during upstream API outages.

**Publisher signal** — Publisher account age is checked for npm and PyPI only. No equivalent public API exists for Go, Cargo, NuGet, or Maven.

**OSV vulnerability scan** — When the OSV API is unreachable, the signal returns `skip` and the package passes through (fail-open). See the [OSV section](policy.md#-osv-vulnerability-scan) for details.

---

## 🆚 Comparison with alternatives

| Feature | JFrog Curation | escrow |
|---------|:--------------:|:------:|
| Server-side age gate | ✅ configurable | ✅ `min_days` |
| OSV / malware scan | ✅ via Xray | ✅ osv.dev |
| npm | ✅ | ✅ |
| PyPI | ✅ | ✅ |
| Go modules | ✅ | ✅ |
| Maven / Gradle | ✅ | ✅ |
| Cargo / Rust | ⚠️ "varying levels of support" | ✅ full age + OSV |
| NuGet | ❓ not confirmed | ✅ |
| Composer | ❓ not confirmed | ✅ |
| On block | silently substitutes safe older version | blocks + dashboard approval |
| Cost | 💰 commercial (Artifactory add-on) | **free / OSS** |
| Self-hosted | ✅ | ✅ |

**Notable difference:** Curation silently swaps a blocked package for an older safe version — frictionless but invisible to developers. Escrow blocks outright and surfaces it in the dashboard, requiring an explicit human approval decision. Which is better depends on your workflow.

**Cargo is the key gap to watch.** JFrog documents Cargo as having "varying levels of support", which likely means its time-delay policy does not apply to Cargo yet. Escrow is currently the only proxy with confirmed server-side age enforcement for Cargo.
