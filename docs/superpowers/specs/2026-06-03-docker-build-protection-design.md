# Docker Build Protection — Policy-Aware Egress Proxy (Selective MITM) — Design

**Date:** 2026-06-03
**Status:** Approved (pending spec review)

## Problem

Docker is the one place escrow can't currently reach. All four routing methods
([routing.md](../../routing.md)) act on the **host**: `escrow-cli config write` edits the host's
`~/.npmrc`/`pip.conf`/…, the env methods set the host's environment, and the pf/iptables redirect
rewrites the host's TCP. A `docker build` runs each `RUN` step in an **isolated container** — a
fresh filesystem (none of those configs), a separate network namespace (host pf doesn't apply),
and on Docker Desktop a Linux VM where `localhost` is the container, not the Mac. So `RUN npm
install` / `pip install` / `apk add` inside a Dockerfile bypasses escrow entirely — exactly the
supply-chain surface escrow exists to gate. Runtime (`compose up`) is the easy half; the **build**
is the hard half and the focus here.

## The model: one proxy, three rules (selective MITM)

escrow gains a single **policy-aware egress proxy** — one endpoint that build traffic is pointed
at (or transparently routed through). For each connection it decides by destination host:

| Rule | Match | Action |
|---|---|---|
| **1** | **known registry host/path** (npm/PyPI/Go/Cargo/NuGet/Maven/Composer) | **decrypt (MITM)** → run the **existing cache + filter + package policy** (age/OSV/publisher) |
| **2** | **blacklisted host** | **reject** at `CONNECT` — no decrypt |
| **3** | **whitelisted, or no match** | **forward (tunnel) opaquely — no decrypt, no cache** |

This is **selective MITM**: escrow terminates TLS *only* for the registry hosts it already
mirrors — because that's the only traffic where it needs the path to apply package policy.
Everything else is decided by **SNI/host** at `CONNECT` time and either rejected or tunnelled
straight through, never decrypted, never cached. That maps 1:1 to the three rules and resolves
the "no cache" intent: **only registry artifacts cache** (existing behavior); all other egress is
pass-through.

### Why this needs a CA — and exactly what that does and doesn't bound

Over HTTPS a proxy sees `CONNECT host:443` then a blind TLS tunnel — **hostname only, never the
path**. To apply package policy to registry traffic on the proxied path (rule 1), escrow must
terminate TLS with **its own CA**, trusted inside the build image. That CA is the price of
"package policy on unmodified Dockerfiles via one proxy setting" — the single goal that justifies
it. State the limit precisely so it isn't over-sold:

- **Selective decryption bounds what escrow *reads by policy*** — only registry hosts are
  decrypted; everything else stays an opaque tunnel.
- **It does *not* bound the CA's trust.** The CA installed in the image is trusted for **all**
  hosts; if escrow is compromised, the blast radius is "impersonate anything to this build." The
  limit is **operational, not cryptographic.** This is a deliberate, opt-in trade for a controlled
  build/CI environment — consistent with escrow being honest about its trust boundaries.

The **registry path-mirror stays** as the front-end for explicitly-configured tools (today's
`escrow-cli config write` model, full policy, **no CA**). The MITM proxy is an **added** front-end
over the **same** cache+policy engine — not a teardown.

## Two things the design must deliver

**(A) Get the traffic to escrow.** Two enforcement levels, same proxy core:

- **Transparent / forced** — where escrow is the **network gateway**: a docker-compose network
  routed through escrow, or the host via the existing **Method 4** (pf/iptables). Intercept by
  original destination (`SO_ORIGINAL_DST` v4 / `IP6T_SO_ORIGINAL_DST` v6) and handle **DNS**, so
  rules apply by **domain *and* IP/CIDR** over **IPv4 + IPv6** — closing the "raw-IP / own-resolver
  bypass" hole. Non-bypassable for anything crossing the boundary.
- **Advisory `HTTP_PROXY`/`HTTPS_PROXY`** — inside a plain build `RUN`, where escrow can't be the
  gateway (no kernel redirect without a non-default insecure entitlement). Honored only by tools
  that read proxy env; a tool that ignores it egresses directly. For traffic that *does* traverse
  the proxy, escrow still enforces the **full host + CIDR policy**: it resolves the destination and
  dials the **vetted IP** (anti-DNS-rebinding), so `block_cidrs` (e.g. cloud-metadata ranges) apply
  to hostname targets too, not just IP literals. What advisory mode can't do is *force* a bypassing
  tool through the proxy.

**(B) Trust the CA in the build** (rule 1 only). Per-ecosystem trust injection delivered by
`escrow-cli docker`: `NODE_EXTRA_CA_CERTS`, `PIP_CERT`/`REQUESTS_CA_BUNDLE`, `CARGO_HTTP_CAINFO`,
the JVM truststore (maven/gradle), and the **OS store** (apk/apt/curl). Tools that refuse a custom
CA (cert-pinning) are a **documented gap**, same honesty as the existing coverage matrix.

## Phasing (preserves "no-CA first")

| Phase | Scope | CA? |
|---|---|---|
| **Phase 1** | **Rules 2 + 3** — SNI/IP host allow/block + opaque tunnel; fast, no cache; DNS + IPv4/IPv6; transparent where escrow is the gateway, advisory `HTTP_PROXY` otherwise | **No** |
| **Phase 2** | **Rule 1** — selective registry decrypt → reuse the cache/filter/policy engine; upstream-host → mirror-handler mapping; per-ecosystem CA injection | **Yes** |

Phase 1 is a useful product on its own (a transparent egress firewall); Phase 2 turns it into the
unified policy-aware proxy. The existing path-mirror is untouched throughout.

## Empirical findings (verified on this machine, Docker 29.5.2, BuildKit)

| Finding | Result | Consequence |
|---|---|---|
| `RUN wget http://host.docker.internal:7888` from a BuildKit step | **reachable, no `--add-host` needed** on Desktop, even with escrow on `127.0.0.1` only | Desktop forwards host loopback; don't rely on it for Linux |
| `--network=host` + `127.0.0.1:7888` | reachable on Desktop | not portable (host == VM on Linux) |
| Proxy-env auto-injection from `~/.docker/config.json` `proxies` | **did NOT inject** `HTTP_PROXY` into `RUN` (classic *and* buildx driver) | **don't build on it** — unreliable on 29.x |
| Explicit `--build-arg HTTP_PROXY=… HTTPS_PROXY=…` | **works** without an `ARG` line; BuildKit mirrors to lowercase too | proxy env is **auto-propagated** into `RUN` — predeclared args |
| Explicit `--build-arg NPM_CONFIG_REGISTRY=…` (any non-proxy arg) **without** a matching `ARG` in the Dockerfile | **dropped** — *not* in the `RUN` env (verified: `reg=[]`); present only when `ARG NPM_CONFIG_REGISTRY` is declared | **registry-env build-args silently no-op on unmodified Dockerfiles** — see below |

> ⚠️ **Registry lane in Phase 1 needs Dockerfile cooperation.** Only the proxy vars
> (`HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` + lowercase) auto-propagate into `RUN`. A registry env
> var (`NPM_CONFIG_REGISTRY`, `PIP_INDEX_URL`, `GOPROXY`, …) reaches `RUN` **only if the Dockerfile
> declares a matching `ARG`**. So on a *stock* third-party Dockerfile, `escrow-cli docker build`
> delivers the **egress** lane (auto-propagated `HTTP_PROXY`) but **not** the registry lane — a
> `RUN npm install` over HTTPS then hits rule 3 and tunnels to the real registry with **no package
> policy**. Phase-1 registry policy therefore requires one of: a **cooperating Dockerfile** (declared
> `ARG`s), a **base stage** that writes the tool config files, or **explicitly-configured tools**.
> **Unmodified-Dockerfile registry policy is a Phase-2 (MITM) deliverable** — there `HTTP_PROXY`
> (which *does* auto-propagate) carries the registry traffic and escrow decrypts it. This is the
> single strongest argument for Phase 2.

> ⚠️ Verified on **Docker Desktop (macOS)** only. The Linux-engine transparent path
> (`SO_ORIGINAL_DST` + REDIRECT/TPROXY, `--add-host=…:host-gateway`, escrow on `0.0.0.0`) is
> **designed, not yet tested on Linux** — confirm it first thing in implementation.

## Architecture

### Server — `internal/egress` (new): the policy-aware proxy

A fast pass-through front-end, separate from the dashboard/mirror server, with two ingress modes
feeding **one** SNI router + policy core:

```go
type Proxy struct { /* listeners, router, policy (host+CIDR, v4+v6), ca, mirror engine, eventlog, cfg */ }

func New(cfg EgressConfig, log *eventlog.Log, policy *Policy, mirror MirrorEngine, ca *CA) *Proxy
func (p *Proxy) Serve(ctx context.Context) error

// Ingress:
//   Transparent (gateway): accept() -> SO_ORIGINAL_DST (v4/v6) -> route(dstIP, SNI)
//   Explicit (advisory):   CONNECT host:port / absolute-URI    -> route(host)
// route():
//   rule 1 registry SNI -> terminate TLS w/ ca -> map URL to mirror handler -> cache/filter/policy
//   rule 2 blacklist    -> 403 / refuse CONNECT (no decrypt)
//   rule 3 else         -> splice opaque tunnel (no decrypt, no cache)
// DNS: resolver/sniffer maps name<->IP so domain rules apply and IP-literal egress is IP/CIDR-checked.
```

- **Rule 1 reuses the engine.** On a registry SNI match escrow presents a CA-signed cert, reads
  the real upstream URL, and dispatches to the **existing** mirror handler (`internal/upstream`
  already knows these registry hosts and how to fetch/cache/filter them). No new policy code — a
  **mapping** layer from `(host, path)` to the right handler.
- **Rules 2/3 need no CA** — decided from SNI + dst IP/CIDR; tunnelled with `io.Copy`, no cache.
- **Default forward-everything**; optional **blacklist** (deny specific) or **whitelist**
  (deny-by-default) for extra security. Every decision emits a `kind=egress` event (name, IP,
  verb, allow/block/decrypt, reason).
- **Listeners / ports** — separate from the dashboard/mirror port, config-gated
  (`[egress_proxy] enabled=false` default), bound to the boundary each serves (never an accidental
  open relay):

  | Listener | Mode | Handles | Notes |
  |---|---|---|---|
  | `forward_port` | explicit (advisory) | HTTP **and** HTTPS | **one** port — `HTTP_PROXY` + `HTTPS_PROXY` both point here; HTTP via absolute-URI, HTTPS via `CONNECT`. No split needed. |
  | `intercept_http_port` | transparent | redirected `:80` | parses the plaintext `Host` header (REDIRECT of port 80). |
  | `intercept_https_port` | transparent | redirected `:443` | peeks the TLS **ClientHello SNI**, then MITM-or-tunnel (REDIRECT of port 443). |

  The **split is only for transparent mode** — there the `:80` (plaintext HTTP) and `:443` (TLS
  ClientHello) wire formats differ, so two intercept ports beat sniffing the protocol on one
  (the standard squid `intercept` vs `ssl-bump` split). The **explicit** forward-proxy needs just
  one port because `CONNECT` already frames HTTPS. The CA is generated/stored under the data dir
  and rotates.

### Server — main listener reachability

For builds to reach the **mirror** the main server must listen on a Docker-reachable interface;
escrow already supports `--host=0.0.0.0`. Docs/wrapper standardize on it + `--add-host` rather
than depending on Desktop's loopback forwarding. (Binding `0.0.0.0` exposes the dashboard — see
Open risks; auth remains.)

### Client — `escrow-cli docker`

- `cmd/escrow-cli/docker.go` — `runDocker(args)`: `build`, `compose build`, `compose init` (writes
  the override incl. the **escrow-gatewayed network** for forced egress), `check` (resolved
  args + reachability + CA fingerprint).
- Reuses the per-ecosystem URL/env derivation from `config.go`/`env.go` — one source of truth.
- Assembles `--add-host`, the proxy env (`HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY`), the **CA-trust**
  build-args (`NODE_EXTRA_CA_CERTS=…` etc. + a `COPY`/secret of the CA), and registry env where a
  tool is configured directly rather than via the proxy.

## Error handling / edge cases

- **escrow unreachable:** registry traffic **fails closed** (the mirror/`GOPROXY=…,off` pattern,
  and a proxy-respecting tool errors). The advisory `HTTP_PROXY` path **can't be forced** — a tool
  that ignores proxy env egresses directly; only the transparent/gateway placement is
  non-bypassable. `escrow-cli docker check` is a pre-flight convenience, **not** a runtime guarantee.
- **CA not trusted by a tool:** rule-1 decrypt of that tool's registry traffic fails (TLS error) —
  loud, not silent. Documented; cert-pinning tools are a known gap.
- **Default forward-everything** is not a hard boundary; the **whitelist/deny-by-default** mode is
  the opt-in for a hard egress wall.
- **Open-relay / data-path risk:** escrow now sits in the egress data path; off by default, bound
  to its boundary, fast pass-through, fail-fast.
- **Desktop vs Linux:** transparent intercept (`SO_ORIGINAL_DST`/TPROXY) is **Linux**; on macOS
  the host path is pf (Method 4) and Desktop runs Linux in a VM. Compose-gateway + Linux/CI are the
  transparent targets.
- **BuildKit cache:** proxy + CA build-args are predeclared and don't bust the cache; registry env
  build-args may — documented.

## Testing

- `internal/egress` policy core: decide from host **and** IP/CIDR, IPv4 **and** IPv6; default
  forward-everything vs whitelist deny-by-default; blacklist ⇒ refuse + event (table tests).
- Routing: registry SNI ⇒ decrypt-and-dispatch (stub CA + stub upstream, assert policy/cache
  invoked); non-registry ⇒ opaque tunnel carries bytes end-to-end and is **not** decrypted;
  blacklist ⇒ refused pre-decrypt. DNS name↔IP applies a domain rule to a later IP connection.
- Transparent original-dst extraction behind an interface (real `SO_ORIGINAL_DST` in a Linux-only
  integration test, skipped elsewhere).
- `cmd/escrow-cli/docker`: assembled args (add-host, proxy env, CA-trust vars, NO_PROXY) match
  golden expectations; `compose init` writes a valid override; `check` reports reachability + CA fp.
- **End-to-end (manual):** with the CA trusted, `RUN npm install <fresh pkg>` over HTTPS through the
  proxy is **blocked by the age gate** (rule 1); `RUN curl https://blocked.example` is **rejected**
  (rule 2); `RUN curl https://allowed.example` **tunnels** (rule 3). All three surface in the dashboard.

## Build sequencing (phased)

1. **`internal/egress` — Phase 1 (rules 2+3, explicit mode)** + `[egress_proxy]` config + main.go
   wiring + host/IP/CIDR policy (v4+v6) + `kind=egress` events. (Standalone; `HTTP_PROXY=… curl`
   from the host. Lowest risk, no CA.)
2. **`escrow-cli docker build` + `check`** — arg assembly reusing `config.go`; advisory path.
3. **`escrow-cli docker compose build` + `compose init`** — override generator incl. the
   escrow-gatewayed network so compose egress is **forced** (the "we can compose" path).
4. **`internal/egress` — transparent mode** — `SO_ORIGINAL_DST`/v6 intercept + DNS; deployable as
   the compose/host gateway. (Linux; the forced path.)
5. **`internal/egress` — Phase 2 (rule 1)** — selective registry decrypt + CA generation +
   upstream-host→mirror-handler mapping + per-ecosystem CA-trust injection in `escrow-cli docker`.
6. **Dashboard egress surfacing** — an "Egress" view over `kind=egress` (name + IP), one-click
   block (allow in whitelist mode).
7. **Docs** — `docs/docker.md` + a routing.md "Method 5: Docker / containers" row, honest about
   transparent-vs-advisory, the CA trade, and the cert-pinning gap.

## Open risks

- **The CA is the trust trade** — trusted for all hosts in the image; escrow-compromise ⇒
  impersonate-anything. Selective decryption is an *operational* scope limit, not a cryptographic
  one. Opt-in, controlled-environment only; documented prominently.
- **Enforcement strength depends on placement** — transparent/gateway = forced (incl. IP-literal &
  IPv6); advisory `HTTP_PROXY` = bypassable. Frame each context honestly.
- **escrow in the egress data path** — performance/availability dependency; hence no-cache,
  pass-through, fail-fast on the non-registry lane.
- **`--host=0.0.0.0` exposes the dashboard** — acceptable on a dev/CI box with auth; advanced setup
  binds the mirror to the Docker gateway IP only.
- **Per-ecosystem CA injection coverage** — env vars cover most tools; cert-pinning tools and some
  bundled runtimes won't accept the CA (documented gap).
- **Transparent intercept is platform-specific** (Linux `SO_ORIGINAL_DST`/TPROXY); designed,
  unverified on Linux here.

---

## Adjacent context: Docker Sandbox (`docker sandbox` / `sbx`) — compose, don't duplicate

> Distinct context from `docker build`; does **not** change the proxy design above.

`docker sandbox` runs isolated VM environments for AI agents (`docker sandbox run claude .`).
Unlike a BuildKit `RUN`, a sandbox **controls its own network boundary** (a VM), and Docker
already uses it: `docker sandbox network proxy <box>` is a built-in **MITM** with
`--allow-host`/`--block-host`, `--allow-cidr`/`--block-cidr`, `--bypass-host`, `--policy
allow|deny`. `sandbox exec` accepts `-e/--env`.

- **escrow can't be the authoritative egress gate in a sandbox** — Docker's sandbox proxy owns
  that boundary and exposes **no upstream/parent-proxy hook**; stacking escrow's proxy via
  `--env HTTP_PROXY` behind Docker's MITM is worse, not better.
- **Docker's sandbox proxy filters host/CIDR only — not package age/OSV/publisher.** escrow's
  package policy stays unique.
- **Compose, don't redirect:** point package tools at escrow's mirror with `--bypass-host
  host.docker.internal`; let Docker's proxy be the egress firewall.

```bash
docker sandbox network proxy mybox --policy deny --allow-host registry.npmjs.org --allow-host pypi.org
docker sandbox network proxy mybox --bypass-host host.docker.internal
docker sandbox exec mybox \
  -e NPM_CONFIG_REGISTRY=http://host.docker.internal:7888/ \
  -e PIP_INDEX_URL=http://host.docker.internal:7888/pypi/simple/ \
  -e GOPROXY=http://host.docker.internal:7888/go,off \
  -- <command>
```

A thin **`escrow-cli sandbox <name>`** helper could emit the `--bypass-host` + `-e` block from the
existing config derivation. Small, optional fast-follow. The `--bypass-host` + mirror interaction
is **designed, not verified** — check with `docker sandbox exec <box> -- wget -qO-
http://host.docker.internal:7888/healthz` once a template is cached.

**Unifying principle:** a *forced, non-bypassable* egress redirect requires controlling the
network boundary. A sandbox **has** one (a VM) and Docker uses it; a BuildKit `RUN` does **not**,
which is why the build's advisory `HTTP_PROXY` path can't be forced. Same problem, two boundaries.
