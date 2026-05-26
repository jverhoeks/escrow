# escrow-cli reference

`escrow-cli` configures your development environment to route package manager traffic through the escrow proxy. It provides five interception layers, each covering a different set of tools and processes.

```
escrow proxy at 127.0.0.1:7888
        ↑
┌───────┴────────────────────────────────────────────────┐
│ Layer 5  Network redirect (pf / iptables)              │  catches everything
│ Layer 4  launchctl setenv (macOS LaunchAgent)          │  catches GUI apps
│ Layer 3  Shell env vars (.zshrc / .bashrc)             │  catches new terminals
│ Layer 2  Local project config (CWD files)              │  per-project opt-in
│ Layer 1  Global tool config (~/.npmrc etc.)            │  standard CLI tools
└────────────────────────────────────────────────────────┘
```

Use one layer or combine several. Each layer adds more coverage but more complexity.

---

## Quick reference

```bash
# One-time system setup (macOS)
sudo escrow-cli setup [--sudoers] [--dry-run]

# Layer 1 — global tool config
escrow-cli config write           [--ecosystems LIST] [--proxy-url URL]
escrow-cli config check           [--ecosystems LIST]
escrow-cli config restore         [--ecosystems LIST]

# Layer 2 — local project config
escrow-cli config write-local     [--ecosystems LIST] [--proxy-url URL]
escrow-cli config check-local     [--ecosystems LIST]
escrow-cli config restore-local   [--ecosystems LIST]

# Layer 3 — shell env vars (.zshrc / .bashrc)
escrow-cli config write-shell     [--ecosystems LIST] [--profiles LIST] [--proxy-url URL]
escrow-cli config check-shell
escrow-cli config restore-shell   [--profiles LIST]

# Layer 4 — launchctl / profile.d
escrow-cli config write-env       [--ecosystems LIST] [--proxy-url URL]
escrow-cli config check-env
escrow-cli config restore-env

# Layer 5 — network redirect
sudo escrow-cli fw-enable         [--ecosystems LIST] [--proxy-port PORT] [--proxy-user USER]
sudo escrow-cli fw-disable
     escrow-cli fw-test           [--ecosystems LIST]

# Service management
sudo escrow-cli service start|stop|restart|status

# Overall status
     escrow-cli status [--json]
```

---

## Layer 1 — Global tool config

Writes per-tool registry config to your home directory. Covers every package manager that honours standard config files. No root required. Works on all platforms.

### `escrow-cli config write`

```bash
escrow-cli config write [--ecosystems LIST] [--proxy-url URL]

# Defaults:
#   --ecosystems  npm,pypi,go,cargo,nuget,maven,composer
#   --proxy-url   http://127.0.0.1:7888
```

**Files written:**

| Tool | File | What is written |
|------|------|----------------|
| npm, pnpm | `~/.npmrc` | `registry=http://127.0.0.1:7888/` |
| yarn v1 | `~/.yarnrc` | `registry "http://127.0.0.1:7888/"` |
| yarn v2+ | `~/.yarnrc.yml` | `npmRegistryServer: "http://127.0.0.1:7888/"` |
| bun | `~/.bunfig.toml` | `[install]\nregistry = "http://127.0.0.1:7888/"` |
| pip | `~/.pip/pip.conf` | `[global]\nindex-url = http://127.0.0.1:7888/pypi/simple/` |
| uv | `~/.config/uv/uv.toml` | `[pip]\nindex-url = "http://127.0.0.1:7888/pypi/simple/"` |
| poetry | shell profile | `# BEGIN escrow-python` block with `PIP_INDEX_URL` |
| go | shell profile | `# BEGIN escrow-go` block with `GOPROXY` and `GONOSUMDB` |
| cargo | `~/.cargo/config.toml` | `[source.crates-io]` replace-with escrow |
| nuget | `~/.nuget/NuGet/NuGet.Config` | `<add key="escrow" value="…/nuget/v3/index.json">` |
| maven | `~/.m2/settings.xml` | `<mirror>` for central pointing to `…/maven2/` |
| gradle | `~/.gradle/init.d/escrow-mirror.gradle` | init script redirecting all Maven repos |
| composer | `~/.config/composer/config.json` | `repositories[0]` pointing to proxy |

Each file is backed up to `<file>.escrow-backup` before being written. Yarn and bun configs are only written if the tool is installed or the config file already exists.

> ⚠️ **Go:** always use `GOPROXY=http://127.0.0.1:7888/go,off` — the trailing `,off` makes the build fail loudly if escrow is unreachable. `,direct` would silently bypass it.

### `escrow-cli config check`

Shows the current state of every tool config, one line per tool:

```
npm/pnpm       ✓  /Users/you/.npmrc
yarn (v1)      –  /Users/you/.yarnrc
yarn (v2+)     –  /Users/you/.yarnrc.yml
bun            ✓  /Users/you/.bunfig.toml
pip            –  /Users/you/.pip/pip.conf
uv             ✓  /Users/you/.config/uv/uv.toml
poetry         ✓  PIP_INDEX_URL in shell profile
go             ✓  /Users/you/.zprofile
cargo          –  /Users/you/.cargo/config.toml
...
```

### `escrow-cli config restore`

Restores all `.escrow-backup` files and removes shell-profile marker blocks.

```bash
escrow-cli config restore                          # restore all ecosystems
escrow-cli config restore --ecosystems npm,pypi    # restore specific ecosystems
```

---

## Layer 2 — Local project config

Writes config files into the **current working directory**. Tools discover these automatically — local config overrides global config for that project.

```bash
cd my-project/
escrow-cli config write-local [--ecosystems LIST] [--proxy-url URL]

# Defaults:
#   --ecosystems  npm,cargo,nuget,pypi,composer  (go/maven have no local equivalent)
```

**Files written in CWD:**

| Tool | File | Note |
|------|------|------|
| npm, pnpm | `.npmrc` | auto-discovered by npm/pnpm going up the directory tree |
| yarn v1 | `.yarnrc` | auto-discovered |
| yarn v2+ | `.yarnrc.yml` | auto-discovered |
| bun | `bunfig.toml` | auto-discovered |
| uv | `uv.toml` | auto-discovered |
| cargo | `.cargo/config.toml` | auto-discovered going up the directory tree |
| nuget | `nuget.config` | auto-discovered |
| composer | `composer.json` | merges `repositories[0]` |

**Not supported locally:**

| Tool | Reason |
|------|--------|
| pip | No local auto-discovery for `pip.conf` |
| poetry | No project-level global registry config (use `pyproject.toml` source manually) |
| go | `GOPROXY` is process-wide, not per-directory |
| maven | `settings.xml` is always user-level |
| gradle | Init scripts are user-global; local init requires `--init-script` flag |

```bash
escrow-cli config check-local      # show state of local configs in CWD
escrow-cli config restore-local    # restore all local backups in CWD
escrow-cli config restore-local --ecosystems npm   # restore specific tool
```

---

## Layer 3 — Shell environment variables

Adds an `export` block to your shell RC files so every new terminal session has the proxy env vars. Covers tools that read env vars instead of (or in addition to) config files.

```bash
escrow-cli config write-shell [--ecosystems LIST] [--profiles LIST] [--proxy-url URL]

# Defaults:
#   --ecosystems  npm,pypi,go
#   --profiles    zshrc,bashrc
```

**Supported profiles:** `zshrc`, `bashrc`, `zprofile`, `bash_profile`, `profile`

**Block written to each profile:**

```bash
# BEGIN escrow-env
export NPM_CONFIG_REGISTRY=http://127.0.0.1:7888/
export YARN_REGISTRY=http://127.0.0.1:7888/
export PIP_INDEX_URL=http://127.0.0.1:7888/pypi/simple/
export UV_INDEX_URL=http://127.0.0.1:7888/pypi/simple/
export GOPROXY=http://127.0.0.1:7888/go,off
export GONOSUMDB='*'
# END escrow-env
```

The block uses `# BEGIN escrow-env` / `# END escrow-env` markers so it is idempotent — running `write-shell` twice produces exactly one block.

**Activate immediately without opening a new terminal:**

```bash
source ~/.zshrc    # zsh
source ~/.bashrc   # bash
```

```bash
escrow-cli config check-shell                      # show which profiles have the block
escrow-cli config restore-shell                    # remove block from all profiles
escrow-cli config restore-shell --profiles zshrc   # remove from specific profile
```

**Limitation:** Shell RC files are not sourced for GUI apps launched from the Dock or Spotlight. Use Layer 4 (launchctl) to cover those.

---

## Layer 4 — launchctl / profile.d

Injects proxy env vars into the OS launch environment at login so **every process** — including GUI apps (VSCode, Zed, Cursor) and bundled runtimes — inherits them automatically.

### macOS — LaunchAgent

```bash
escrow-cli config write-env [--ecosystems LIST] [--proxy-url URL]

# Defaults:
#   --ecosystems  npm,pypi,go
```

Writes `~/Library/LaunchAgents/com.escrow.environment.plist` and loads it immediately. The agent runs at every login via `launchctl setenv`, making the vars available to all new processes in the session.

> **Why LaunchAgent, not LaunchDaemon?**  
> macOS System Integrity Protection (SIP) blocks `launchctl setenv` in the system domain (LaunchDaemon). User-domain LaunchAgents are exempt from this restriction and do not require root.

**Env vars injected:**

| Variable | Value | Covers |
|----------|-------|--------|
| `NPM_CONFIG_REGISTRY` | `http://127.0.0.1:7888/` | npm, pnpm |
| `YARN_REGISTRY` | `http://127.0.0.1:7888/` | yarn v1 |
| `PIP_INDEX_URL` | `http://127.0.0.1:7888/pypi/simple/` | pip, poetry |
| `UV_INDEX_URL` | `http://127.0.0.1:7888/pypi/simple/` | uv |
| `GOPROXY` | `http://127.0.0.1:7888/go,off` | go |
| `GONOSUMDB` | `*` | go |

**Important:** Processes already running when `write-env` is called will not see the new vars. Relaunch GUI apps (Cmd+Q, then reopen) to pick them up.

```bash
escrow-cli config check-env     # show LaunchAgent status + current launchctl values
escrow-cli config restore-env   # unload and remove LaunchAgent, unset env vars
```

### Linux — profile.d

On Linux, `write-env` writes to `/etc/profile.d/escrow.sh` (requires root). This file is sourced for all login shells and most desktop sessions.

```bash
sudo escrow-cli config write-env
# → writes /etc/profile.d/escrow.sh
# → takes effect on next login or: source /etc/profile.d/escrow.sh
```

**Limitation:** `profile.d` is not sourced for all processes on all Linux desktop environments. Systemd user services need a separate env file. Use Layer 5 for full coverage on Linux.

---

## Layer 5 — Network redirect (pf / iptables)

The network backstop. Intercepts all TCP connections to registry hosts at the kernel level — no config files, no env vars needed. Catches every process including bundled runtimes, scripts that hardcode registry URLs, and tools that ignore all config.

Requires root for enable/disable. Requires one-time system setup.

### One-time setup

```bash
# Preview without making changes:
escrow-cli setup --dry-run

# Apply (creates _escrow account, patches pf.conf / iptables chain):
sudo escrow-cli setup

# Also install passwordless sudo for fw-enable / fw-disable:
sudo escrow-cli setup --sudoers
```

**What setup does:**

| Step | macOS | Linux |
|------|-------|-------|
| Create `_escrow` service account | `dscl .` (local directory, not OD) | `useradd --system` |
| Firewall hook | Patches `/etc/pf.conf` with `rdr-anchor "escrow"` | Creates `ESCROW` iptables chain |
| Anchor file | Creates `/etc/pf.anchors/escrow-npm` | — |
| Sudoers (optional) | `/etc/sudoers.d/escrow` | `/etc/sudoers.d/escrow` |

### Enable / disable

```bash
sudo escrow-cli fw-enable [--ecosystems LIST] [--proxy-port 7888] [--proxy-user _escrow]
sudo escrow-cli fw-disable
```

`fw-enable` resolves each registry hostname to its current IP addresses at rule-load time, then writes rules that redirect those IPs to `127.0.0.1:7888`. The proxy user (`_escrow`) is exempted from redirection to prevent the proxy's own outbound connections from being redirected back into itself.

**IPv6:** Rules automatically include `block return out inet6` for registry hosts that have AAAA records (checked at rule-load time). IPv4-only hosts skip inet6 rules to avoid the "rule expands to no valid combination" error.

### Verify

```bash
escrow-cli fw-test [--ecosystems LIST]
```

Makes a plain HTTP request to each registry host on port 443. If the proxy intercepts it, the proxy responds with HTTP — confirming the redirect is active. Results:

```
proxy:     ✓  127.0.0.1:7888 reachable

npm        ✓  registry.npmjs.org:443 → proxy      (redirect confirmed)
npm        ~  npm.pkg.github.com:443  rule loaded, CDN IP rotated (likely OK)
pypi       ~  pypi.org:443  rule loaded, CDN IP rotated (likely OK)
nuget      ✓  api.nuget.org:443 → proxy
```

| Symbol | Meaning |
|--------|---------|
| `✓` | Redirect confirmed via live TCP test |
| `~` | pf rule is loaded but CDN rotated IPs since fw-enable ran; redirect works when IP aligns |
| `✗` | No rule loaded — run `sudo escrow-cli fw-enable` |

### Known limitations

#### Service user

The proxy must run as the `_escrow` OS user (created by `setup`) for the pf `pass out quick … user _escrow` exemption to work. If the proxy runs as a different user:

- Its outbound connections to real registries will be redirected back to port 7888 → infinite loop
- Run `escrow-cli status` to verify `proxyUser` and that the pf anchor is loaded

#### IP rotation (CDN hosts)

pf and iptables resolve hostnames to IPs **once at rule-load time** and store them statically. CDN-hosted registries serve different IPs to different resolvers and rotate them frequently:

| Host | Typical TTL | Notes |
|------|-------------|-------|
| `proxy.golang.org` | **8 seconds** | Google — changes almost immediately |
| `repo.packagist.org` | **35 seconds** | Rotates quickly |
| `static.crates.io` | ~230 seconds | Fastly anycast |
| `registry.npmjs.org` | ~300 seconds | Cloudflare — all 12 IPs loaded |
| `pypi.org` | ~48,000 seconds | 4 stable Fastly IPs |

**Mitigation:** Re-run `sudo escrow-cli fw-enable` after each network change (VPN connect/disconnect, Wi-Fi switch). For `proxy.golang.org` specifically, rules will be stale almost immediately — use Layer 1/3/4 for Go rather than relying solely on network redirect.

A launchd job watching `com.apple.network.change` can automate the refresh:

```xml
<!-- ~/Library/LaunchAgents/com.escrow.fwrefresh.plist -->
<key>WatchPaths</key>
<array><string>/etc/resolv.conf</string></array>
<key>ProgramArguments</key>
<array>
  <string>/bin/sh</string><string>-c</string>
  <string>sudo /usr/local/bin/escrow-cli fw-enable 2>/dev/null</string>
</array>
```

#### HTTP/3 / QUIC

pf and iptables only redirect TCP. HTTP/3 runs over QUIC (UDP port 443) and bypasses TCP-based redirect entirely. Current package manager CLIs (npm, pip, cargo, go, maven) do not use HTTP/3 by default — this is a future concern as CDNs push adoption.

#### VPN split-tunnelling

Corporate VPNs may mark registry IP ranges as "direct" (bypassing the VPN tunnel) in their server-side split-tunnel config. These IPs leave the machine without passing through the pf redirect. Layers 1–3 are not affected.

---

## System setup details

### `escrow-cli setup`

```bash
sudo escrow-cli setup [--sudoers] [--dry-run]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--sudoers` | false | Install `/etc/sudoers.d/escrow` granting the `admin` group passwordless sudo for `fw-enable`, `fw-disable`, `service` |
| `--dry-run` | false | Preview what would happen without making any changes. Does not require root |

### `escrow-cli service`

```bash
sudo escrow-cli service start|stop|restart|status
```

Wraps `launchctl bootstrap/bootout` (macOS) or `systemctl start/stop` (Linux) for the escrow LaunchDaemon/service.

### `escrow-cli status`

```bash
escrow-cli status [--json]
```

Shows:
- Firewall anchor active and which ecosystems are in the loaded rules
- Which tool config files are pointing to escrow
- Proxy service running and reachable (`/healthz`)

---

## Coverage matrix

| Tool | Layer 1 (global) | Layer 2 (local) | Layer 3 (shell) | Layer 4 (launchctl) | Layer 5 (network) |
|------|:---:|:---:|:---:|:---:|:---:|
| npm | ✅ | ✅ | ✅ | ✅ | ✅ |
| pnpm | ✅ | ✅ | ✅ | ✅ | ✅ |
| yarn v1 | ✅ | ✅ | ✅ | ✅ | ✅ |
| yarn v2+ | ✅ | ✅ | – | – | ✅ |
| bun | ✅ | ✅ | – | – | ✅ |
| pip | ✅ | – | ✅ | ✅ | ✅ |
| uv | ✅ | ✅ | ✅ | ✅ | ✅ |
| poetry | ✅ (env) | – | ✅ | ✅ | ✅ |
| go | ✅ | – | ✅ | ✅ | ⚠️ IP rotates |
| cargo | ✅ | ✅ | – | – | ✅ |
| nuget | ✅ | ✅ | – | – | ✅ |
| maven | ✅ | – | – | – | ✅ |
| gradle | ✅ | – | – | – | ✅ |
| composer | ✅ | ✅ | – | – | ✅ |
| VSCode bundled npm | – | – | – | ✅ | ✅ |
| Any process | – | – | – | – | ⚠️ IP rotation |

⚠️ = covered with caveats (see IP rotation section)

---

## Recommended setup for a developer machine

```bash
# 1. Start the proxy (Homebrew)
brew services start escrow

# 2. Global tool config — covers all CLI tools immediately
escrow-cli config write

# 3. LaunchAgent — covers GUI apps (VSCode, Zed) after next relaunch
escrow-cli config write-env

# 4. Shell profiles — covers new terminal sessions immediately
escrow-cli config write-shell
source ~/.zshrc   # activate in current terminal without opening a new one

# 5. Network redirect — backstop for everything else (macOS)
sudo escrow-cli setup
sudo escrow-cli fw-enable

# Verify everything is wired up
escrow-cli status
escrow-cli config check
escrow-cli config check-env
```
