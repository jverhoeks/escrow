# 🔌 Routing traffic to escrow

[← Back to README](../README.md) · Related: [escrow-cli reference](escrow-cli.md) · [Per-tool quickstarts](quickstart/)

`escrow-cli` is a companion tool (installed alongside `escrow` via Homebrew) that makes pointing
your tools at the proxy a **one-command** job — no hand-editing `.npmrc`, `pip.conf`,
`settings.xml`, `config.toml`, and friends, and every file is backed up before it's touched.
It routes your whole development environment's package traffic through the proxy. Four methods
are available — use one, or combine several for complete coverage.

| | Method | Catches | Root? | Platform |
|---|---|---|---|---|
| **1** | [Global config files](#method-1--global-config-files) | CLI tools reading standard configs | No | All |
| **2** | [Local project config](#method-2--local-project-config) | Per-project, checked-in | No | All |
| **3** | [Shell / launch env](#method-3--shell--launch-environment) | CLI + GUI apps (VSCode, Zed...) | No | macOS / Linux |
| **4** | [Network redirect](#method-4--network-redirect) | Every process, no config needed | Yes | macOS / Linux |
| **5** | [Docker / containers](#method-5--docker--containers) | `docker build` & container egress, via the escrow egress proxy | No | All |

### Recommended combination for a developer machine

```bash
escrow-cli config write          # 1. write tool config files globally
escrow-cli config write-env      # 3. LaunchAgent / profile.d — covers GUI apps
escrow-cli config write-shell    # 3. .zshrc + .bashrc for new terminals
sudo escrow-cli setup            # 4. system account + pf anchor (macOS)
sudo escrow-cli fw-enable        # 4. network-level redirect rules
```

---

### Method 1 — Global config files

Writes per-tool registry config to your home directory. Covers every package manager that honours standard config files.

```bash
escrow-cli config write [--ecosystems npm,pypi,go,cargo,nuget,maven,composer] \
                        [--proxy-url http://127.0.0.1:7888]
```

**What gets written:**

| Tool | File written |
|------|-------------|
| npm, pnpm | `~/.npmrc` |
| yarn v1 | `~/.yarnrc` |
| yarn v2+ | `~/.yarnrc.yml` |
| bun | `~/.bunfig.toml` |
| pip | `~/.pip/pip.conf` |
| uv | `~/.config/uv/uv.toml` |
| poetry | `PIP_INDEX_URL` block in shell profile |
| go | `GOPROXY` block in shell profile |
| cargo | `~/.cargo/config.toml` |
| nuget | `~/.nuget/NuGet/NuGet.Config` |
| maven | `~/.m2/settings.xml` |
| gradle | `~/.gradle/init.d/escrow-mirror.gradle` |
| composer | `~/.config/composer/config.json` |

Each file is backed up to `<file>.escrow-backup` before being written.

```bash
escrow-cli config check          # show which tools are configured
escrow-cli config restore        # restore all backups
escrow-cli config restore --ecosystems npm,pypi   # restore specific ecosystems
```

> ⚠️ **Go:** use `GOPROXY=http://127.0.0.1:7888/go,off` not `,direct`. The `off` fallback causes builds to fail loudly when escrow is unreachable rather than silently bypassing it.

---

### Method 2 — Local project config

Writes config files into the **current working directory**. Useful for per-project opt-in without changing global settings.

```bash
cd your-project/
escrow-cli config write-local [--ecosystems npm,cargo,nuget,pypi,composer]
```

**Files written in CWD:**

| Tool | File |
|------|------|
| npm, pnpm | `.npmrc` |
| yarn v1 | `.yarnrc` |
| yarn v2+ | `.yarnrc.yml` |
| bun | `bunfig.toml` |
| uv | `uv.toml` |
| cargo | `.cargo/config.toml` |
| nuget | `nuget.config` |
| composer | `composer.json` |

Go, pip, maven, gradle have no project-local config equivalent — use Method 1 for those.

```bash
escrow-cli config check-local    # show which local files are configured
escrow-cli config restore-local  # restore all local backups
```

---

### Method 3 — Shell / launch environment

Injects proxy env vars at the OS level so **GUI apps** (VSCode, Zed, Cursor) and processes launched outside a terminal also see the proxy settings.

#### macOS LaunchAgent (recommended — survives reboot, covers GUI apps)

```bash
escrow-cli config write-env [--ecosystems npm,pypi,go]

# Check what's active in the launch environment:
escrow-cli config check-env
```

Writes `~/Library/LaunchAgents/com.escrow.environment.plist`. The agent runs at every login and injects these env vars into the macOS launch environment so every spawned process inherits them — including VSCode, Zed, and bundled runtimes.

#### Shell profiles (.zshrc / .bashrc)

```bash
escrow-cli config write-shell [--profiles zshrc,bashrc] [--ecosystems npm,pypi,go]

# Activate in the current terminal immediately (no new window needed):
source ~/.zshrc

# Check which profiles have the block:
escrow-cli config check-shell
```

`--profiles` accepts: `zshrc`, `bashrc`, `zprofile`, `bash_profile`, `profile`.

**Env vars injected by both commands:**

```bash
NPM_CONFIG_REGISTRY=http://127.0.0.1:7888/     # npm, pnpm
YARN_REGISTRY=http://127.0.0.1:7888/           # yarn v1
PIP_INDEX_URL=http://127.0.0.1:7888/pypi/simple/ # pip, poetry
UV_INDEX_URL=http://127.0.0.1:7888/pypi/simple/  # uv
GOPROXY=http://127.0.0.1:7888/go,off           # go
GONOSUMDB=*
```

**Undo:**
```bash
escrow-cli config restore-env    # remove LaunchAgent
escrow-cli config restore-shell  # remove shell profile block
```

---

### Method 4 — Network redirect

The network backstop: intercepts all TCP connections to registry hosts at the kernel level using **pf** (macOS) or **iptables / nftables** (Linux). Catches every process regardless of config files or environment variables.

#### One-time system setup (run once per machine)

```bash
# Preview what will happen without making changes:
escrow-cli setup --dry-run

# Apply (creates _escrow service account, patches pf.conf, sets up iptables chain):
sudo escrow-cli setup

# Optional: install passwordless sudo so EscrowManager.app can enable/disable without prompting:
sudo escrow-cli setup --sudoers
```

#### Enable / disable redirect rules

```bash
sudo escrow-cli fw-enable [--ecosystems npm,pypi,go,cargo,nuget,maven,composer] \
                          [--proxy-port 7888] [--proxy-user _escrow] [--block-ipv6]
sudo escrow-cli fw-disable
```

`--block-ipv6` blocks **all** IPv6 egress to `:80`/`:443` (except the proxy user) instead of only
the registry hosts that have an AAAA record at enable time. Use it on locked-down CI hosts where a
later-acquired AAAA (dual-stack rollout, CDN change) must not become an IPv6 bypass — at the cost
of the host's general IPv6 web traffic on those ports. See the IPv6 caveat below.

#### Verify interception is working

```bash
escrow-cli fw-test [--ecosystems npm,pypi]
```

Output:
```
proxy:     ✓  127.0.0.1:7888 reachable

npm        ✓  registry.npmjs.org:443 → proxy
npm        ~  npm.pkg.github.com:443  rule loaded, CDN IP rotated (likely OK)
pypi       ~  pypi.org:443  rule loaded, CDN IP rotated (likely OK)
```

- `✓` — redirect confirmed via live TCP test
- `~` — pf rule is loaded, CDN IP changed since `fw-enable` ran (redirect will work when IP aligns again)
- `✗` — no rule loaded, run `sudo escrow-cli fw-enable`

#### Overall status

```bash
escrow-cli status          # pf rules, config files, proxy health
escrow-cli status --json   # machine-readable
```

#### Known limitations

pf and iptables resolve hostnames to IP addresses at rule-load time. This means:

| Limitation | Impact | Mitigation |
|---|---|---|
| CDN IP rotation | Rules pin IPs at load time; new connections to a rotated IP are not redirected | Re-run `fw-enable` after network change, or **pair with the egress proxy** (matches hostname at connect time) |
| IPv6 (later-acquired AAAA) | By default IPv6 is blocked only for registry hosts that have an AAAA *at enable time*; a host that gains one afterward can be reached directly over IPv6 | Run `fw-enable --block-ipv6` for a hard IPv6 cutoff, or pair with the egress proxy |
| HTTP/3 / QUIC | UDP port 443 bypasses TCP redirect | Package managers use TCP today; monitor as HTTP/3 adoption grows |
| VPN split-tunnelling | Corporate VPN may mark registry IPs as "direct", bypassing redirect | Methods 1–3 remain effective |
| New bundled runtimes | Tool that ignores config and bypasses TCP (e.g. custom go binary) | Methods 1–3 provide defence-in-depth |

> For complete hostname-based interception immune to IP rotation, a macOS Network Extension (`NETransparentProxyProvider`) is the path forward. See [`specs/swift-network-extension-prompt.md`](specs/swift-network-extension-prompt.md).

---

### Method 5 — Docker / containers

Methods 1–4 act on the **host**; a `docker build` runs in an isolated container that sees none of
them. escrow adds a dedicated **egress proxy** for builds and containers, with two lanes:

- **Registry traffic** → escrow's path-mirror (full age/OSV/publisher policy), delivered into the
  build via `escrow-cli docker build` / a compose override.
- **Everything else** → the egress proxy with an optional host + IP/CIDR allow/blocklist.

```bash
# enable the egress proxy in escrow.toml: [egress_proxy] enabled = true
escrow-cli docker build --ecosystems npm,pypi,go -- -t myimg .
escrow-cli docker compose init --service web --ecosystems npm
escrow-cli docker check                      # print resolved build-args + reachability
```

> ⚠️ Inside a plain `docker build` `RUN`, the egress proxy is **advisory** (`HTTP_PROXY`) — forced
> only when escrow is the container's gateway. And registry **package policy** needs a cooperating
> `ARG`/base stage (or the future MITM phase). Full details, security model, and limitations:
> **[Docker & docker build protection →](docker.md)**.

---

### Coverage summary

| Tool | Method 1 (config) | Method 2 (local) | Method 3 (env) | Method 4 (network) |
|------|:-:|:-:|:-:|:-:|
| npm | ✅ | ✅ | ✅ | ✅ |
| pnpm | ✅ | ✅ | ✅ | ✅ |
| yarn v1 | ✅ | ✅ | ✅ | ✅ |
| yarn v2+ | ✅ | ✅ | – | ✅ |
| bun | ✅ | ✅ | – | ✅ |
| pip | ✅ | – | ✅ | ✅ |
| uv | ✅ | ✅ | ✅ | ✅ |
| poetry | ✅ (env) | – | ✅ | ✅ |
| go | ✅ | – | ✅ | ✅ |
| cargo | ✅ | ✅ | – | ✅ |
| nuget | ✅ | ✅ | – | ✅ |
| maven | ✅ | – | – | ✅ |
| gradle | ✅ | – | – | ✅ |
| composer | ✅ | ✅ | – | ✅ |
| VSCode bundled npm | – | – | ✅ | ✅ |
| Any rogue script | – | – | – | ✅ |
