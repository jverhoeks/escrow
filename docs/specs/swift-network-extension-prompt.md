# Prompt: EscrowManager macOS Companion App

## Context

You are building **EscrowManager.app**, a macOS companion app for the **escrow** supply-chain proxy. The escrow proxy is a Go binary (`escrow-cli` and `escrow`) that intercepts package manager traffic (npm, PyPI, Go modules, Cargo, NuGet, Maven, Composer) and scans packages for CVE vulnerabilities and supply-chain age before allowing them through. It listens on `127.0.0.1:7888`.

The companion app has one primary job: ensure **all traffic to package registries flows through the escrow proxy**, regardless of which tool sends it, whether it reads config files, or whether it was launched from a terminal. It does this using a macOS **Network Extension** — no pf rules, no cert injection, no TLS MITM.

The escrow CLI already handles:
- Creating the `_escrow` system account (`escrow-cli setup`)
- Writing per-tool registry config files (`escrow-cli config write`)
- pf rules as a fallback (`escrow-cli fw-enable`)
- Status reporting (`escrow-cli status --json`)

The app calls the CLI for those operations. It owns the Network Extension layer.

---

## Architecture

```
EscrowManager.app
├── Main app target        (menu bar UI, settings, calls escrow-cli)
└── EscrowProxy.appex      (System Extension — NETransparentProxyProvider)
     └── Communicates with main app via XPC
```

The System Extension **does not inspect TLS**. It works at the TCP connection level:
- If `remoteHostname` matches a known registry host → redirect flow to `127.0.0.1:7888`
- All other flows → pass through unchanged

The escrow Go proxy receives plain HTTP from tools that are configured (via `escrow-cli config write`). For tools that bypass config, the extension ensures their TCP connection is still redirected — the Go proxy then receives a TLS ClientHello and can handle it as a CONNECT tunnel (see Proxy Behaviour section below).

---

## Network Extension Spec

### Type
`NETransparentProxyProvider` in a System Extension bundle.

### Entitlements required
```
com.apple.developer.networking.networkextension: [transparent-proxy]
com.apple.security.network.client: true
com.apple.security.network.server: true  (main app)
com.apple.security.application-groups: group.com.escrow.shared
```

### Flow matching

Intercept flows where `remoteHostname` (case-insensitive) matches any host in the active registry list. The list is configurable via XPC from the main app.

Default registry hostnames (grouped by ecosystem):

```swift
let registryHosts: [String: [String]] = [
    "npm":      ["registry.npmjs.org", "npm.pkg.github.com"],
    "pypi":     ["pypi.org", "files.pythonhosted.org"],
    "go":       ["proxy.golang.org", "sum.golang.org"],
    "cargo":    ["static.crates.io", "index.crates.io"],
    "nuget":    ["api.nuget.org"],
    "maven":    ["repo.maven.apache.org", "repo1.maven.org"],
    "composer": ["repo.packagist.org", "packagist.org"],
]
```

Matching must also check for suffix matches (e.g. `*.npmjs.org` should catch future subdomains), but exact matching is sufficient for the initial implementation.

### Redirect destination

All matched flows redirect to `127.0.0.1:7888` (configurable via XPC; stored in shared App Group UserDefaults).

### Pass-through

Flows that don't match any registry hostname are passed through with `.allow()` immediately. The extension must not introduce latency on non-registry traffic.

### Protocol support

- TCP port 443 (HTTPS): intercept and redirect
- TCP port 80 (HTTP): intercept and redirect
- UDP/QUIC: log a warning, allow through (HTTP/3 not supported yet — document this as a known gap)

### VPN coexistence

The extension must not conflict with `NEVPNManager`-based VPN providers (Cisco AnyConnect, etc.). `NETransparentProxyProvider` is a different extension type and does not compete for routing tables. Do not install a DNS proxy (`NEDNSProxyProvider`) — this is the extension type that conflicts with Cisco. The transparent proxy type is safe.

---

## Proxy Behaviour (Go side — for reference, not implemented here)

When the extension redirects a TLS connection (tool configured to speak raw HTTPS), the Go proxy at port 7888 receives a TLS ClientHello. The proxy:
1. Reads the SNI from the ClientHello without terminating TLS
2. Verifies the SNI matches a known registry host
3. Opens a new TLS connection to the real registry
4. Splices the two TCP streams (no MITM, no cert)

This gives hostname-level routing without requiring a CA cert. Package-level inspection (OSV scan, age gate) is done by tools that are configured to speak plain HTTP to the proxy via `escrow-cli config write` — those tools never initiate TLS to the proxy.

The app does not need to implement this — it is already handled in the Go binary. Document it here so the extension implementer understands why no cert is needed.

---

## XPC Interface

The main app and System Extension communicate over a named XPC service (`com.escrow.proxy.xpc`).

### App → Extension messages

```swift
protocol EscrowProxyXPC {
    // Update the active hostname list (call after user changes ecosystem toggles)
    func setEnabledHostnames(_ hostnames: [String], reply: @escaping (Bool) -> Void)

    // Update the proxy redirect target
    func setProxyEndpoint(_ host: String, port: Int, reply: @escaping (Bool) -> Void)

    // Enable or disable interception entirely
    func setEnabled(_ enabled: Bool, reply: @escaping (Bool) -> Void)

    // Request current stats snapshot
    func getStats(reply: @escaping (ProxyStats) -> Void)
}
```

### Extension → App messages (reverse XPC or notifications)

```swift
struct ProxyStats: Codable {
    var interceptedCount: Int      // flows redirected to proxy this session
    var passedThroughCount: Int    // flows allowed directly
    var activeFlowCount: Int       // currently open intercepted connections
    var lastInterceptedAt: Date?
}
```

### Shared state (App Group UserDefaults: `group.com.escrow.shared`)

```
escrow.proxyHost          String    "127.0.0.1"
escrow.proxyPort          Int       7888
escrow.enabled            Bool      true
escrow.enabledEcosystems  [String]  ["npm","pypi","go","cargo","nuget","maven","composer"]
```

---

## Main App UI

### Menu bar app (no Dock icon)

The app lives in the menu bar only. Use `NSStatusItem` with a custom icon (a shield or lock glyph). Icon states:
- Active (green tint): proxy running, extension enabled, escrow service reachable
- Partial (amber tint): extension enabled but escrow service unreachable (port 7888 not listening)
- Inactive (grey): extension disabled or not installed

### Popover (click on menu bar icon)

```
┌─────────────────────────────────────┐
│  🛡 Escrow Proxy          ● Active  │
├─────────────────────────────────────┤
│  Intercepting                        │
│  npm    ● registry.npmjs.org        │
│  npm    ● npm.pkg.github.com        │
│  pypi   ● pypi.org                  │
│  pypi   ● files.pythonhosted.org    │
│  go     ● proxy.golang.org          │
│  …                                   │
│                                      │
│  12 connections intercepted          │
│  proxy 127.0.0.1:7888  ✓ reachable  │
├─────────────────────────────────────┤
│  Settings…        Disable    Quit   │
└─────────────────────────────────────┘
```

### Settings window (opens from popover)

Tabs:
1. **Ecosystems** — toggle each ecosystem on/off (checkboxes); updates XPC immediately
2. **Proxy** — proxy host (default 127.0.0.1), proxy port (default 7888)
3. **System** — buttons to run `escrow-cli setup`, `escrow-cli config write`, show `escrow-cli status` output; shows whether `_escrow` account exists; shows last fw-enable time

### System tab — escrow-cli integration

Run `escrow-cli` commands using `Process` / `NSTask`:

```swift
func runEscrowCLI(_ subcommand: [String]) async -> (output: String, exitCode: Int) {
    let process = Process()
    process.executableURL = URL(fileURLWithPath: "/opt/homebrew/bin/escrow-cli")
    // fall back to /usr/local/bin/escrow-cli if not found
    process.arguments = subcommand
    // capture stdout + stderr
}
```

Privileged commands (`setup`, `fw-enable`) use `AuthorizationExecuteWithPrivileges` or prompt via `sudo` through an embedded helper. Do not ship with hardcoded sudo — use `SMJobBless` for the privileged helper or `AuthorizationExecuteWithPrivileges`.

---

## Installation Flow

### First launch

1. Check if System Extension is installed (`NEExtensionManager`)
2. If not: show onboarding sheet explaining what the extension does and why it needs approval
3. Call `NEExtensionManager.shared().loadAllExtensions { … }` to trigger the system approval dialog
4. User approves in System Settings → Privacy & Security → Network Extensions
5. Extension activates; XPC channel opens

### Subsequent launches

1. Extension already loaded — just open XPC channel
2. Check `escrow-cli` is installed (`which escrow-cli`)
3. If not: show banner "escrow-cli not found — install via `brew install jverhoeks/tap/escrow`"
4. Check port 7888 is listening; if not: show banner "escrow proxy not running — start with `brew services start escrow`"

---

## Extension Installation & Update

Use `NEExtensionManager` (macOS 11+) for installation. On update (new app version with new extension version), call `loadAllExtensions` again — macOS handles the extension version comparison.

The extension bundle identifier: `com.escrow.proxy.extension`  
The main app bundle identifier: `com.escrow.manager`

---

## Minimum Requirements

- macOS 12.0 (Monterey) — `NETransparentProxyProvider` stable, `NEExtensionManager` available
- Swift 5.9 / Xcode 15+
- Signed with Apple Developer ID (not App Store — System Extensions require Developer ID)
- Notarized

---

## Known Gaps to Document in UI

- **HTTP/3 / QUIC** (UDP/443): not intercepted — shown as a warning in Settings if a registry host is known to support it
- **Tools that hardcode IP addresses**: not intercepted (bypasses hostname matching)
- **VPN split-tunnel exclusions**: if corporate VPN excludes registry IPs, traffic bypasses the extension before it can be intercepted

---

## What the App Does NOT Do

- No TLS MITM
- No certificate injection
- No DNS proxying (`NEDNSProxyProvider` — would conflict with Cisco AnyConnect)
- No inspection of package content — that is the Go proxy's job
- No pf rule management — `escrow-cli fw-enable` handles that; the app just calls the CLI

---

## File Structure

```
EscrowManager/
├── EscrowManager.xcodeproj
├── EscrowManager/              # Main app target
│   ├── AppDelegate.swift
│   ├── StatusBarController.swift
│   ├── PopoverViewController.swift
│   ├── SettingsWindowController.swift
│   ├── EscrowCLIRunner.swift   # wraps Process calls to escrow-cli
│   ├── ExtensionManager.swift  # NEExtensionManager wrapper
│   ├── XPCClient.swift         # talks to extension
│   └── SharedDefaults.swift    # App Group UserDefaults helpers
├── EscrowProxy/                # System Extension target
│   ├── main.swift
│   ├── TransparentProxyProvider.swift   # NETransparentProxyProvider subclass
│   ├── FlowHandler.swift                # per-flow redirect logic
│   ├── XPCServer.swift                  # receives config from main app
│   └── Info.plist
└── Shared/
    ├── RegistryHosts.swift     # the ecosystem → hostname map
    ├── ProxyStats.swift        # Codable stats struct
    └── EscrowProxyXPC.swift    # shared XPC protocol definition
```
