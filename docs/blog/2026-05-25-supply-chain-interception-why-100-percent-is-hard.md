---
title: "The Last Mile Problem: Why 100% Supply Chain Coverage is Hard"
slug: supply-chain-interception-why-100-percent-is-hard
date: 2026-05-25
tags: [supply-chain, security, pf, macos, npm, proxy, devtools]
summary: >
  Every time a developer runs npm install, they're implicitly trusting hundreds
  of strangers. Intercepting that trust is straightforward in theory — and
  surprisingly subtle in practice.
readingTime: 14 min
---

Every time a developer runs `npm install`, they're implicitly trusting hundreds of strangers. Intercepting that trust is straightforward in theory — and surprisingly subtle in practice.

Supply chain attacks have gone from theoretical concern to recurring headline. **SolarWinds** compromised 18,000 organisations through a single build step. **xz-utils** nearly backdoored OpenSSH across every major Linux distribution. **event-stream** stole cryptocurrency from a specific npm package by becoming a dependency of a dependency. The **colors.js** and **left-pad** incidents showed that even intent-to-harm isn't required — a maintainer having a bad day is enough.

The attack surface is the package registry. Your code pulls from npm, PyPI, crates.io, pkg.go.dev, Maven Central, NuGet, Packagist. Each one is a trust decision you make dozens of times a day without thinking about it.

> The question isn't whether to intercept package traffic. It's how to do it without breaking the development experience — and how close to 100% you can actually get.

This post documents what we learned building **escrow**, a local package proxy that applies age gates and OSV vulnerability scanning before allowing packages through. We tried every interception approach available on macOS and Linux, and each one has a ceiling.

---

## The Trust Chain You're Defending

Before picking an interception strategy, it helps to understand what you're actually sitting in front of. When a developer runs `npm install lodash`, the chain looks like this:

```
developer
  └─ npm CLI (resolves ~/.npmrc for registry URL)
       └─ DNS lookup: registry.npmjs.org → 104.16.x.34 (Cloudflare CDN)
            └─ TLS handshake + HTTP/1.1 GET /lodash
                 └─ tarball download → local cache → node_modules/
```

An interception layer can sit at any of these steps: the tool config, the DNS resolution, the TCP connection, or the TLS layer. The catch is that each insertion point has different coverage, different reliability, and different compatibility with the rest of a developer's environment — particularly VPNs.

---

## The Options

### Option 1 — Per-tool registry config ✅

Write the proxy URL directly into each tool's config file. npm uses `~/.npmrc`, pip uses `~/.pip/pip.conf`, cargo uses `~/.cargo/config.toml`, Go uses `GOPROXY`, and so on. All seven major ecosystems support this natively.

**Works for:** CLI tools using standard config paths  
**Doesn't catch:** Bundled runtimes (VSCode), GUI apps with no shell env, hardcoded registry URLs

This is the most reliable approach for the happy path — no kernel involvement, works with any VPN, and is hostname-aware by nature. It's what enterprise proxies (Artifactory, Nexus) use. The escrow CLI's `config write` command automates it for all seven ecosystems.

---

### Option 2 — launchctl environment injection ✅ (macOS)

On macOS, apps launched from the Dock or Spotlight don't inherit shell environment variables. `~/.zprofile` exports are invisible to VSCode.app. A LaunchDaemon running at boot fixes this by calling `launchctl setenv` before anything else launches:

```bash
# /Library/LaunchDaemons/com.escrow.environment.plist — RunAtLoad
launchctl setenv NPM_CONFIG_REGISTRY  http://127.0.0.1:7888/
launchctl setenv PIP_INDEX_URL        http://127.0.0.1:7888/pypi/simple/
launchctl setenv UV_INDEX_URL         http://127.0.0.1:7888/pypi/simple/
launchctl setenv GOPROXY             http://127.0.0.1:7888/go,off
launchctl setenv GONOSUMDB           '*'
```

Every process — including VSCode, Zed, and their extension hosts — inherits these from boot. Persists across reboots, survives VPN connect/disconnect.

**Works for:** GUI apps that honour the env vars  
**Doesn't catch:** Truly bundled npm that ignores env, hardcoded registry calls

---

### Option 3 — pf / iptables network redirect ⚠️

Intercept at the kernel network layer. Any TCP connection to port 443 destined for a registry host gets redirected to `127.0.0.1:7888` regardless of which tool made it, which config it reads, or whether it inherits env vars.

```
# /etc/pf.anchors/escrow-npm
rdr pass inet proto tcp from any to registry.npmjs.org port 443 \
  -> 127.0.0.1 port 7888

# Pass the proxy's own outbound connections (by UID, not username —
# os/user.Lookup misses Open Directory accounts when CGO is disabled)
pass out quick proto tcp from any to registry.npmjs.org \
  port {80, 443} user 499

# Block port-80 and IPv6 to prevent bypass
block return out inet  proto tcp from any to registry.npmjs.org port 80
block return out inet6 proto tcp from any to registry.npmjs.org port 80
block return out inet6 proto tcp from any to registry.npmjs.org port 443
```

Works with VPN — the redirect to `127.0.0.1` happens before routing, so VPN tunnel config doesn't affect it. The proxy's outbound connections go through the VPN as intended.

**Works for:** Every process, regardless of config or env  
**Doesn't catch:** IPs that rotated after rule-load, HTTP/3 (UDP), future CDN IPs

Two gotchas worth knowing:

1. **Use numeric UID, not username** in `pass out ... user` rules. On macOS, `sysadminctl -roleAccount` creates users in Open Directory, which `pf`'s `getpwnam()` lookup doesn't see. Numeric UIDs bypass this.
2. **inet6 block rules fail** for hostnames with no AAAA record (pf error: "rule expands to no valid combination"). Resolve each host's DNS at rule-load time and only emit inet6 rules for hosts that actually have IPv6 addresses.

---

### Option 4 — macOS Network Extension ❌ (for most teams)

Apple's `NETransparentProxyProvider` / `NEFilterDataProvider` operates at the flow level before DNS resolution. `NEFilterSocketFlow.remoteHostname` gives you the original hostname — no IP involved, no CDN rotation issue. This is what Little Snitch, Proxyman, and corporate MDM tools use.

The wall: Cisco AnyConnect installs its own DNS proxy extension, and macOS only allows one active DNS proxy extension at a time. If your developers use AnyConnect, a Network Extension will conflict. Beyond that, the entitlement (`com.apple.developer.networking.networkextension`) requires emailing Apple, and the extension must be written in Swift or Objective-C — Go can't call the framework directly.

**Works for:** Everything, hostname-native  
**Doesn't work with:** Cisco AnyConnect VPN, teams without Apple developer entitlements

---

### Option 5 — Global HTTPS_PROXY with TLS MITM ❌ (too invasive)

Set `HTTPS_PROXY=http://127.0.0.1:7888` globally. All HTTPS traffic flows through the proxy, which decrypts, inspects, and re-encrypts using a locally-trusted CA. Universal coverage, hostname-aware — but the cost is high:

- Generate a local CA and install it in: macOS system keychain, Python's certifi bundle, Java's cacerts, Rust's webpki roots, Git's ssl.cainfo
- Every tool that pins certificates will break
- HTTP/3 (QUIC/UDP) still bypasses it
- Introducing a MITM CA is itself a security footprint

This is how Zscaler and enterprise DLP tools work. It's appropriate for managed corporate devices; it's too invasive for a developer tooling proxy.

---

## Why 100% is Hard

Even with all three practical layers deployed simultaneously, structural gaps remain.

### CDN IP rotation

pf resolves hostnames to IPs at rule-load time and stores them statically. We measured the actual DNS TTLs:

| Host | TTL | Notes |
|------|-----|-------|
| `proxy.golang.org` | **8s** | Google — IPs rotated between two lookups 5 min apart |
| `repo.packagist.org` | **35s** | IP changed between runs |
| `packagist.org` | **35s** | IP changed between runs |
| `static.crates.io` | 231s | Fastly — IP changed between runs |
| `api.nuget.org` | 168s | Azure — stable in practice |
| `registry.npmjs.org` | 299s | Cloudflare anycast — all 12 IPs loaded by pf |
| `pypi.org` | 48,557s | Fastly — 4 stable IPs for ~13 hours |

`proxy.golang.org` at 8 seconds means pf rules are stale almost immediately after `fw-enable`. Every `go get` is a coin flip on whether the redirect fires. Refreshing rules on every network change event (launchd watching `com.apple.network.change`) is the only mitigation short of a Network Extension.

### HTTP/3 / QUIC

QUIC is UDP. pf redirects TCP. An HTTPS proxy speaks TCP. A package manager that negotiates HTTP/3 sends packets on UDP/443 that no TCP-based interception layer will see. Most current package manager CLIs don't use HTTP/3 — but CDNs are pushing it harder every year.

### Bundled runtimes

VSCode bundles its own Node.js. When an extension triggers `npm install`, that bundled npm may read from `~/.npmrc` — or it may not, depending on version, the `--userconfig` flag, and extension isolation policy. pf catches it at the network layer; env injection catches it if the bundled npm honours env vars; neither is guaranteed.

### VPN split-tunnelling

Corporate VPNs can mark registry IP ranges as "direct" (bypass the tunnel) in server-side split-tunnel config. Traffic to those IPs leaves the machine without touching pf or the proxy — and the developer can't change it.

### DNS-over-HTTPS

`/etc/hosts` and dnsmasq interception are a potential alternative layer, but Chrome, Firefox, and newer curl can bypass the system resolver entirely using hardcoded DoH endpoints. Not a package manager concern today, but it closes the DNS door for the future.

### Hardcoded IPs

Malicious packages occasionally connect to exfiltration endpoints using hardcoded IP addresses, bypassing DNS entirely. A pf rule for `registry.npmjs.org` doesn't help if the package calls `104.16.0.34` directly.

---

## The Layered Defence Model

No single layer reaches 100%. Three layers together cover every ordinary installation scenario.

| Layer | CLI tools | GUI apps | Bundled runtimes | Rogue scripts |
|-------|-----------|----------|-----------------|---------------|
| 1. Per-tool config | ✅ | ❌ | ～ | ❌ |
| 2. launchctl env injection | ✅ | ✅ | ～ | ❌ |
| 3. pf / iptables | ✅ | ✅ | ✅ | ～ |

The `～` on pf for rogue scripts is the CDN IP rotation issue. Refreshing `fw-enable` on network change events brings that closer to ✅.

What this model doesn't catch: a package that connects to an exfiltration server on an IP not in the ruleset, or a tool that uses HTTP/3. For those, you need egress filtering at the network edge or a genuine Network Extension.

---

## The Honest Conclusion

Perfect supply chain interception on a developer machine requires either becoming the OS (Network Extension with Apple's blessing) or accepting that determined adversaries have escape routes. The good news is that real supply chain attacks don't require exotic evasion — they rely on developers installing a package without checking. Intercepting the happy path is achievable, and the happy path is where 99.9% of the risk lives.

The layered model above covers every ordinary installation scenario: `npm install` in a terminal, a VSCode extension auto-installing its dependencies, a CI job running in a local runner, a Zed plugin downloading a language server. That's the threat model worth defending.

> You don't need to catch every possible evasion. You need to catch the attack that's actually happening — and today, that attack goes through `npm install`.

The remaining gap is worth documenting honestly so teams don't build false confidence into their security posture. Escrow is a layer, not a silver bullet. Treat it that way — and it's genuinely useful.

---

*Part of the [escrow](https://github.com/jverhoeks/escrow) open-source supply chain proxy project.*
