# 🐳 Docker & `docker build` protection

[← Back to README](../README.md) · Related: [Routing traffic to escrow](routing.md) · [Security model](security.md) · Design: [`specs/2026-06-03-docker-build-protection-design.md`](superpowers/specs/2026-06-03-docker-build-protection-design.md)

> **Status:** Phase 1 (the egress firewall) is being implemented per
> [`plans/2026-06-03-docker-build-protection-phase1.md`](superpowers/plans/2026-06-03-docker-build-protection-phase1.md).
> This page documents the **security model and design** — read the "what it does *not* protect"
> sections as carefully as the rest. Phase 2 (selective MITM) is designed but not yet built; it is
> marked **(Phase 2)** throughout.

---

## Why Docker is the hard case

escrow's four routing methods all act on the **host**: `escrow-cli config write` edits the host's
`~/.npmrc`/`pip.conf`/…, the env methods set the host's environment, and the pf/iptables redirect
([Method 4](routing.md#method-4--network-redirect)) rewrites the host's TCP. A `docker build` runs
each `RUN` step in an **isolated container**:

- a **fresh filesystem** — none of those config files exist;
- a **separate network namespace** — the host's pf/iptables rules don't apply;
- on Docker Desktop, a **Linux VM** — `localhost` inside the build is the container, not your Mac.

So `RUN npm install` / `pip install` / `apk add` inside a Dockerfile **bypasses escrow entirely** —
exactly the supply-chain surface escrow exists to gate. This page is how we close that gap, and
where we honestly can't.

---

## The two lanes

Docker build traffic splits into two classes, gated by two different mechanisms with **different
strengths**:

| Lane | Traffic | Mechanism | Guarantee |
|---|---|---|---|
| **Registry** | npm / PyPI / Go / Cargo / NuGet / Maven / Composer | escrow's **path-mirror** (caches + applies package policy) | **Full package policy** — age / OSV / publisher; **fail-closed** |
| **Egress** | apk/apt, `curl\|bash`, git, telemetry, any other host | escrow's **egress proxy** — host/IP allow-block, fast pass-through, no cache | **Egress firewall** — name/IP-level; *advisory or forced* (see below) |

The reason for the split is a protocol fact: over HTTPS a proxy sees `CONNECT host:443` then a
**blind TLS tunnel** — the **hostname, never the path or package**. Applying package policy needs
the path, which over HTTPS needs decryption (a CA — **Phase 2**). So registries ride escrow's
mirror (full policy, no CA); everything else gets host/IP egress control.

---

## 🛡️ What it protects against

| Threat | Protected? | By |
|---|---|---|
| ✅ Same-day malware in a fresh registry version pulled **during a build** | yes¹ | registry lane — age gate |
| ✅ Known-CVE package version pulled during a build | yes¹ | registry lane — OSV |
| ✅ Build reaching out to a **known-bad host** (C2, exfil, typo-CDN) | yes² | egress lane — blacklist |
| ✅ Locking a build to an **allowlist of hosts** (deny-by-default) | yes² | egress lane — whitelist mode |
| ✅ Bypass by connecting to a **raw IP**, or a hostname that resolves into a blocked **CIDR** | yes² | egress lane — CIDR checked on the literal **and** resolved IP; escrow dials the **vetted IP** (anti-DNS-rebinding) |
| ✅ **IPv6** egress | yes² | egress lane — dual-stack (v4 + v6) |
| ❌ Package policy on an **unmodified third-party Dockerfile** | **not in Phase 1** — see ⁴ | needs cooperating `ARG` / base stage, or Phase 2 MITM |
| ❌ A tool that **ignores `HTTP_PROXY`** in a plain `RUN` | no² | not forceable in a build `RUN` — see below |
| ❌ **Package** policy on arbitrary HTTPS egress (non-registry) | no | host/IP only without MITM |
| ❌ Postinstall hooks / typosquatting / git deps | no | same gaps as [security.md](security.md) — escrow filters versions, not behavior |

¹ **Registry lane only takes effect when the build actually uses escrow's mirror** — see ⁴.
² Egress enforcement strength depends on **placement** — forced only when escrow is the gateway; advisory otherwise (a tool that ignores `HTTP_PROXY` skips the proxy, and with it all egress checks). CIDR rules apply to traffic that *does* pass through the proxy: escrow resolves the host and dials the vetted IP. See "Enforcement strength".
⁴ See "The `ARG` requirement" — the most important Phase-1 caveat.

---

## How it works — three rules (selective MITM)

escrow's egress proxy decides each connection by destination host:

| Rule | Match | Action |
|---|---|---|
| **1** | a **known registry** host/path | **decrypt (MITM, Phase 2)** → run the existing cache + age/OSV/publisher policy |
| **2** | a **blacklisted** host/IP | **reject** at `CONNECT` — no decrypt |
| **3** | **whitelisted, or no match** | **forward (tunnel) opaquely — no decrypt, no cache** |

Only known registries are ever decrypted (Phase 2); everything else is decided by **SNI/host + dst
IP** and tunnelled straight through. **Phase 1 ships rules 2 + 3 (no CA).** Rule 1 is Phase 2.

---

## ⚠️ Enforcement strength depends on placement, not configuration

This is the single most important thing to understand. A proxy only **forces** traffic through
escrow where escrow controls the **network boundary**:

| Placement | How traffic reaches escrow | Forced? |
|---|---|---|
| **Compose / runtime** | a docker network routed through escrow (escrow as gateway) **(transparent mode)** | ✅ **forced** — non-bypassable, incl. raw-IP & IPv6 |
| **Host** | pf / iptables redirect ([Method 4](routing.md#method-4--network-redirect)) | ✅ **forced** |
| **Plain `docker build` `RUN`** | `HTTP_PROXY`/`HTTPS_PROXY` env (advisory) | ⚠️ **best-effort** |

Inside a BuildKit `RUN` there is **no kernel-level redirect** available without a non-default
**insecure entitlement**, so `HTTP_PROXY` is the ceiling — and it is honored **only by tools that
read it**. A statically-linked binary, a raw socket, or a tool that ignores proxy env **egresses
directly**, in *every* policy mode (forward *and* whitelist). The egress lane in a plain build is
**visibility + a best-effort host gate, not an inescapable boundary.** Treat it accordingly.

> The registry lane, by contrast, is **fail-closed**: `GOPROXY=…,off` and a replaced HTTP index
> *error* when escrow is unreachable — they do not silently fall through to the public registry.

---

## ⚠️ The `ARG` requirement (verified — Phase-1 registry caveat)

**Only the proxy variables auto-propagate into a build.** BuildKit injects `HTTP_PROXY`,
`HTTPS_PROXY`, `NO_PROXY` (and lowercase) into every `RUN` automatically. **Every other build-arg —
including `NPM_CONFIG_REGISTRY`, `PIP_INDEX_URL`, `GOPROXY` — is dropped unless the Dockerfile
declares a matching `ARG`.** (Verified on Docker 29.x: `RUN echo $NPM_CONFIG_REGISTRY` prints empty
without an `ARG NPM_CONFIG_REGISTRY` line; populated with it.)

**Consequence:** on a **stock, unmodified Dockerfile**, `escrow-cli docker build` delivers the
**egress lane** (auto-propagated `HTTP_PROXY`) but the **registry env is silently dropped** — so a
`RUN npm install` over HTTPS hits rule 3 and **tunnels to the real registry with no package
policy**.

**To get registry package policy in Phase 1, choose one:**

1. **Cooperating Dockerfile** — declare the args above the install step:
   ```dockerfile
   FROM node:20-alpine
   ARG NPM_CONFIG_REGISTRY        # ← without this line, the registry env is ignored
   RUN npm install --ignore-scripts
   ```
2. **Base stage** — `FROM` an escrow base image that writes the tool config files (`.npmrc`,
   `pip.conf`, …); no `ARG` needed because the tool reads its config file.
3. **Explicitly-configured tools** — the tool already reads escrow's config (e.g. a checked-in
   project `.npmrc` from `escrow-cli config write-local`).

**Unmodified-Dockerfile registry policy is a Phase-2 (MITM) deliverable** — there `HTTP_PROXY`
(which *does* auto-propagate) carries the registry traffic and escrow decrypts it. This is the
single strongest argument for Phase 2.

---

## 🔐 The CA trade-off (Phase 2)

Rule 1 (package policy on the proxied path) requires escrow to terminate TLS with **its own CA**,
trusted inside the build image (`NODE_EXTRA_CA_CERTS`, `PIP_CERT`/`REQUESTS_CA_BUNDLE`,
`CARGO_HTTP_CAINFO`, the JVM truststore, the OS store for apk/apt/curl). Understand the bound
precisely:

- **Selective decryption** means escrow only *reads by policy* the registry hosts — everything else
  stays an opaque tunnel.
- **It does *not* bound the CA's trust.** The CA is trusted for **all** hosts in the image; if
  escrow is compromised, the blast radius is "impersonate anything to this build." The limit is
  **operational, not cryptographic.** Use it only in a controlled build/CI environment, opt-in.
- **Cert-pinning tools** that refuse a custom CA are a **documented gap** — their registry decrypt
  fails loudly (TLS error), it does not silently bypass.

---

## Coverage matrix

| Ecosystem | Egress lane (host/IP) | Registry policy (Phase 1) | Registry policy (Phase 2) |
|---|:-:|:-:|:-:|
| npm / pnpm / yarn / bun | ✅ | ✅ with `ARG`/base stage | ✅ unmodified (MITM) |
| pip / uv | ✅ | ✅ with `ARG`/base stage | ✅ unmodified (MITM) |
| go | ✅ | ✅ with `ARG`/base stage | ✅ unmodified (MITM) |
| cargo / nuget / maven / gradle / composer | ✅ | ⚠️ base stage only (no std env var) | ✅ unmodified (MITM) |
| arbitrary egress (apk, curl, git…) | ✅ host/IP | — (no package concept) | — |

---

## Usage

### Configure the egress proxy (`escrow.toml`)

```toml
[egress_proxy]
enabled      = true
forward_port = 7889          # explicit forward-proxy: one port, HTTP + HTTPS via CONNECT
policy       = "forward"     # "forward" (default-allow) | "whitelist" (deny-by-default)
block_hosts  = ["telemetry.example", ".ads.example"]   # exact or ".suffix"
allow_hosts  = []            # used in whitelist mode
block_cidrs  = ["169.254.0.0/16"]                       # e.g. block cloud metadata
allow_cidrs  = []
# (transparent-mode intercept ports are added in the transparent/Phase-2 work)
```

The egress proxy is **off by default**, on its own listener, separate from the dashboard/mirror
port — never an accidental open relay. Run it only on a controlled dev/CI host.

### Plain `docker build`

```bash
escrow-cli docker build --ecosystems npm,pypi,go -- -t myimg .
```

Injects `--add-host=host.docker.internal:host-gateway`, the proxy env (`HTTP_PROXY`/`HTTPS_PROXY`/
`NO_PROXY`), and the registry env build-args. **Remember the [`ARG` requirement](#️-the-arg-requirement-verified--phase-1-registry-caveat)** for the registry lane.

### Compose

```bash
escrow-cli docker compose init --service web --service worker --ecosystems npm
docker compose -f docker-compose.yml -f docker-compose.escrow.yml build
```

Writes a `docker-compose.escrow.yml` override adding `build.args` + `build.extra_hosts` to the named
services. (Forced/transparent egress via an escrow-gatewayed compose network is part of the
transparent-mode work.)

### Check

```bash
escrow-cli docker check --ecosystems npm,pypi,go
```

Prints the resolved `--add-host` + build-args and verifies escrow is reachable. **Pre-flight
convenience only — not a runtime guarantee** (it can't make a mid-build bypass fail-closed).

---

## 🧪 Docker Sandbox (`docker sandbox` / `sbx`)

`docker sandbox` (isolated VM environments for AI agents) **controls its own network boundary** and
already ships a built-in **MITM egress proxy**: `docker sandbox network proxy <box>` with
`--allow-host`/`--block-host`, `--allow-cidr`/`--block-cidr`, `--bypass-host`, `--policy allow|deny`.

- **escrow can't be the authoritative egress gate in a sandbox** — Docker owns that boundary and
  exposes no upstream-proxy hook. Don't stack escrow's proxy behind it via `--env HTTP_PROXY`.
- **Docker's sandbox proxy filters host/CIDR only — not package age/OSV/publisher.** escrow's
  package policy stays unique and complementary.
- **Compose, don't redirect:** point package tools at escrow's mirror and bypass it from the
  sandbox MITM:

```bash
docker sandbox network proxy mybox --policy deny --allow-host registry.npmjs.org --allow-host pypi.org
docker sandbox network proxy mybox --bypass-host host.docker.internal
docker sandbox exec mybox \
  -e NPM_CONFIG_REGISTRY=http://host.docker.internal:7888/ \
  -e PIP_INDEX_URL=http://host.docker.internal:7888/pypi/simple/ \
  -e GOPROXY=http://host.docker.internal:7888/go,off \
  -- <command>
```

**Unifying principle:** a forced, non-bypassable egress redirect requires controlling the network
boundary. A sandbox **has** one (a VM) and Docker uses it; a BuildKit `RUN` does **not**, which is
why the build's `HTTP_PROXY` path is advisory. Same problem, two boundaries.

---

## See also

- [Security model & threat coverage](security.md) — escrow's overall guarantees and gaps (postinstall hooks, typosquatting, git deps).
- [Routing traffic to escrow](routing.md) — the four host-level methods; Docker is "Method 5".
- [Design spec](superpowers/specs/2026-06-03-docker-build-protection-design.md) — full rationale, empirical findings, phasing.
