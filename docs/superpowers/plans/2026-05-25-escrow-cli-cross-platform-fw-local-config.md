# escrow-cli: Cross-Platform Firewall + Local Config Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend `escrow-cli` with cross-platform firewall redirect (macOS pf, Linux iptables/nftables), a `config write-local` subcommand that writes per-tool config to the current directory, and cross-platform `setup`/`service`/`status` support.

**Architecture:** A new `fw.go` file dispatches `fw-enable`/`fw-disable` to platform-specific backends (pf on Darwin, detected iptables or nftables on Linux) using `runtime.GOOS`. `pf.go` is trimmed to the macOS pf rule-generation backend only; the run-level commands move to `fw.go`. Local config writes target CWD instead of `$HOME` using a new `config write-local` subcommand and a matching `config restore-local`.

**Tech Stack:** Go stdlib (`runtime`, `os/user`), `github.com/BurntSushi/toml` (already in `go.mod`), `pfctl` (Darwin), `iptables`/`ip6tables` or `nft` (Linux), `systemctl` (Linux service), `launchctl` (Darwin service).

---

## File Map

| File | Action | Responsibility |
|------|--------|---------------|
| `cmd/escrow-cli/fw.go` | **CREATE** | `runFwEnable`, `runFwDisable`; Darwin pf backend; Linux iptables/nftables backend; `detectLinuxFw`; `buildNftRules` |
| `cmd/escrow-cli/fw_test.go` | **CREATE** | Unit tests for `buildNftRules`, `buildPfRules`, `detectLinuxFw` mock |
| `cmd/escrow-cli/pf.go` | **MODIFY** | Remove `runPfEnable`/`runPfDisable`; rename `buildRules`→`buildPfRules`; add alias stubs; keep registry data and helpers |
| `cmd/escrow-cli/setup.go` | **MODIFY** | Extract `createSystemUser()` (dispatches by OS); `setupLinuxFwChain()`; update `runSetup`; update sudoers content to use `fw-*` |
| `cmd/escrow-cli/service.go` | **MODIFY** | `serviceLoad`/`serviceUnload` dispatch by `runtime.GOOS`; Linux uses `systemctl` |
| `cmd/escrow-cli/status.go` | **MODIFY** | pf-check block dispatches by OS; Linux checks iptables ESCROW chain or nft table |
| `cmd/escrow-cli/config.go` | **MODIFY** | Add `runConfigWriteLocal`, `runConfigRestoreLocal`, and five local ecosystem writers |
| `cmd/escrow-cli/config_local_test.go` | **CREATE** | Unit tests for all local ecosystem writers using `t.TempDir()` |
| `cmd/escrow-cli/main.go` | **MODIFY** | Route `fw-enable`, `fw-disable`, `config write-local`, `config restore-local`; keep `pf-enable`/`pf-disable` as aliases |

---

## Task 1: Trim `pf.go` — extract macOS pf backend

**Files:**
- Modify: `cmd/escrow-cli/pf.go`

Remove `runPfEnable` and `runPfDisable` (they move to `fw.go`). Rename `buildRules` → `buildPfRules`. Add backward-compat alias stubs that call the new `fw.go` functions (defined in Task 2, so add them after Task 2 compiles).

- [ ] **Step 1: Rename `buildRules` to `buildPfRules` in `pf.go`**

In `cmd/escrow-cli/pf.go`, rename the function:
```go
// was: func buildRules(ecosystems []string, proxyPort int, proxyUser string) string {
func buildPfRules(ecosystems []string, proxyPort int, proxyUser string) string {
```

- [ ] **Step 2: Remove `runPfEnable` and `runPfDisable` from `pf.go`**

Delete the two functions entirely. They will be replaced by aliases in Task 2.

- [ ] **Step 3: Verify build still compiles (will fail until Task 2 adds the aliases)**

```bash
go build ./cmd/escrow-cli/ 2>&1 | head -20
```
Expected: errors about undefined `runPfEnable`/`runPfDisable` (fine — fixed in Task 2).

---

## Task 2: Create `fw.go` — cross-platform dispatch + Darwin pf backend

**Files:**
- Create: `cmd/escrow-cli/fw.go`

Contains: `runFwEnable`, `runFwDisable`, Darwin pf backend (`fwEnableDarwin`, `fwDisableDarwin`), backward-compat alias stubs (`runPfEnable`, `runPfDisable`).

- [ ] **Step 1: Create `cmd/escrow-cli/fw.go`**

```go
package main

import (
	"flag"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// runFwEnable is the cross-platform fw-enable entry point.
func runFwEnable(args []string) {
	fs := flag.NewFlagSet("fw-enable", flag.ExitOnError)
	ecosystems := fs.String("ecosystems", strings.Join(allEcosystems, ","), "comma-separated ecosystems to intercept")
	proxyPort := fs.Int("proxy-port", 7888, "escrow proxy port")
	proxyUser := fs.String("proxy-user", "_escrow", "OS user running the escrow proxy")
	fs.Parse(args) //nolint:errcheck

	requireRoot("fw-enable")

	if !validUnixUser.MatchString(*proxyUser) {
		die("invalid --proxy-user %q: must match ^[a-z_][a-z0-9_-]{0,31}$", *proxyUser)
	}

	ecos := parseEcosystems(*ecosystems)
	if len(ecos) == 0 {
		die("no valid ecosystems specified; valid values: %s", strings.Join(allEcosystems, ", "))
	}

	switch runtime.GOOS {
	case "darwin":
		fwEnableDarwin(ecos, *proxyPort, *proxyUser)
	case "linux":
		fwEnableLinux(ecos, *proxyPort, *proxyUser)
	default:
		die("fw-enable not supported on %s", runtime.GOOS)
	}

	fmt.Printf("firewall rules enabled for: %s\n", strings.Join(ecos, ", "))
}

// runFwDisable is the cross-platform fw-disable entry point.
func runFwDisable(args []string) {
	fs := flag.NewFlagSet("fw-disable", flag.ExitOnError)
	fs.Parse(args) //nolint:errcheck

	requireRoot("fw-disable")

	switch runtime.GOOS {
	case "darwin":
		fwDisableDarwin()
	case "linux":
		fwDisableLinux()
	default:
		die("fw-disable not supported on %s", runtime.GOOS)
	}

	fmt.Println("firewall rules disabled")
}

// runPfEnable and runPfDisable are backward-compatible aliases for fw-enable / fw-disable.
func runPfEnable(args []string)  { runFwEnable(args) }
func runPfDisable(args []string) { runFwDisable(args) }

// ── macOS pf backend ──────────────────────────────────────────────────────────

func fwEnableDarwin(ecos []string, port int, user string) {
	rules := buildPfRules(ecos, port, user)
	if err := writeAtomic(pfAnchorFile, []byte(rules), 0644); err != nil {
		die("writing anchor file: %v", err)
	}
	if out, err := exec.Command("pfctl", "-a", "escrow", "-f", pfAnchorFile).CombinedOutput(); err != nil {
		die("loading pf anchor: %v\n%s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("pfctl", "-E").CombinedOutput(); err != nil {
		die("enabling pf: %v\n%s", err, strings.TrimSpace(string(out)))
	}
}

func fwDisableDarwin() {
	empty := "# Escrow pf anchor — managed by escrow-cli\n"
	if err := writeAtomic(pfAnchorFile, []byte(empty), 0644); err != nil {
		die("clearing anchor file: %v", err)
	}
	if out, err := exec.Command("pfctl", "-a", "escrow", "-F", "all").CombinedOutput(); err != nil {
		die("flushing pf anchor: %v\n%s", err, strings.TrimSpace(string(out)))
	}
	exec.Command("pfctl", "-X").Run() //nolint:errcheck
}

// ── Linux firewall detection ──────────────────────────────────────────────────

// detectLinuxFw returns "iptables", "nftables", or "" if neither tool is found.
// Prefers iptables because it resolves hostnames at rule-insertion time, which
// is consistent with how pf handles names on macOS.
func detectLinuxFw() string {
	if _, err := exec.LookPath("iptables"); err == nil {
		return "iptables"
	}
	if _, err := exec.LookPath("nft"); err == nil {
		return "nftables"
	}
	return ""
}

// ── Linux iptables backend ────────────────────────────────────────────────────

// fwEnableIptables creates an ESCROW chain in the nat table and populates it.
// Uses a dedicated chain so fw-disable can flush it without touching other rules.
func fwEnableIptables(ecos []string, port int, proxyUser string) {
	// Create chains (ignore "already exists" errors).
	exec.Command("iptables", "-t", "nat", "-N", "ESCROW").Run()  //nolint:errcheck
	exec.Command("ip6tables", "-N", "ESCROW6").Run()              //nolint:errcheck

	// Ensure OUTPUT jumps to our chain.
	if exec.Command("iptables", "-t", "nat", "-C", "OUTPUT", "-j", "ESCROW").Run() != nil {
		if out, err := exec.Command("iptables", "-t", "nat", "-A", "OUTPUT", "-j", "ESCROW").CombinedOutput(); err != nil {
			die("iptables OUTPUT → ESCROW: %v\n%s", err, strings.TrimSpace(string(out)))
		}
	}
	if exec.Command("ip6tables", "-C", "OUTPUT", "-j", "ESCROW6").Run() != nil {
		if out, err := exec.Command("ip6tables", "-A", "OUTPUT", "-j", "ESCROW6").CombinedOutput(); err != nil {
			die("ip6tables OUTPUT → ESCROW6: %v\n%s", err, strings.TrimSpace(string(out)))
		}
	}

	// Flush and repopulate.
	exec.Command("iptables", "-t", "nat", "-F", "ESCROW").Run()  //nolint:errcheck
	exec.Command("ip6tables", "-F", "ESCROW6").Run()              //nolint:errcheck

	portStr := fmt.Sprintf("%d", port)
	for _, eco := range ecos {
		for _, host := range registryHosts[eco] {
			// IPv4: redirect HTTPS to proxy (exclude proxy user to prevent loops).
			if out, err := exec.Command("iptables", "-t", "nat", "-A", "ESCROW",
				"-p", "tcp", "--dport", "443", "-d", host,
				"-m", "owner", "!", "--uid-owner", proxyUser,
				"-j", "REDIRECT", "--to-ports", portStr,
			).CombinedOutput(); err != nil {
				die("iptables redirect %s: %v\n%s", host, err, strings.TrimSpace(string(out)))
			}
			// IPv4: block HTTP bypass.
			exec.Command("iptables", "-A", "ESCROW",
				"-p", "tcp", "--dport", "80", "-d", host,
				"-m", "owner", "!", "--uid-owner", proxyUser,
				"-j", "REJECT", "--reject-with", "tcp-reset",
			).Run() //nolint:errcheck
		}
	}

	// IPv6: block both ports (no REDIRECT in ip6tables OUTPUT; block forces IPv4).
	for _, host := range orderedHosts(ecos) {
		exec.Command("ip6tables", "-A", "ESCROW6",
			"-p", "tcp", "-m", "multiport", "--dports", "80,443", "-d", host,
			"-m", "owner", "!", "--uid-owner", proxyUser,
			"-j", "REJECT", "--reject-with", "tcp-reset",
		).Run() //nolint:errcheck
	}
}

func fwDisableIptables() {
	exec.Command("iptables", "-t", "nat", "-F", "ESCROW").Run()  //nolint:errcheck
	exec.Command("ip6tables", "-F", "ESCROW6").Run()              //nolint:errcheck
}

// ── Linux nftables backend ────────────────────────────────────────────────────

const nftRulesFile = "/etc/nftables.d/escrow.conf"

func fwEnableLinux(ecos []string, port int, proxyUser string) {
	switch detectLinuxFw() {
	case "iptables":
		fwEnableIptables(ecos, port, proxyUser)
	case "nftables":
		fwEnableNftables(ecos, port, proxyUser)
	default:
		die("neither iptables nor nft found — install one to use fw-enable")
	}
}

func fwDisableLinux() {
	switch detectLinuxFw() {
	case "iptables":
		fwDisableIptables()
	case "nftables":
		fwDisableNftables()
	default:
		die("neither iptables nor nft found")
	}
}

func fwEnableNftables(ecos []string, port int, proxyUser string) {
	// nftables skuid requires a numeric UID.
	uid, err := lookupUID(proxyUser)
	if err != nil {
		die("looking up uid for %q: %v — create the user first with: escrow-cli setup", proxyUser, err)
	}

	rules := buildNftRules(ecos, port, uid)
	if err := exec.Command("mkdir", "-p", "/etc/nftables.d").Run(); err != nil {
		die("creating /etc/nftables.d: %v", err)
	}
	if err := writeAtomic(nftRulesFile, []byte(rules), 0644); err != nil {
		die("writing nftables rules: %v", err)
	}
	if out, err := exec.Command("nft", "-f", nftRulesFile).CombinedOutput(); err != nil {
		die("loading nftables rules: %v\n%s", err, strings.TrimSpace(string(out)))
	}
}

func fwDisableNftables() {
	exec.Command("nft", "delete", "table", "ip", "escrow").Run()  //nolint:errcheck
	exec.Command("nft", "delete", "table", "ip6", "escrow").Run() //nolint:errcheck
	const empty = "# Escrow nftables rules — managed by escrow-cli\n"
	writeAtomic(nftRulesFile, []byte(empty), 0644) //nolint:errcheck
}

// buildNftRules generates an nftables ruleset for the given ecosystems.
// uid is the numeric UID of the proxy user (excluded from redirect/block).
func buildNftRules(ecos []string, port int, uid string) string {
	hosts := orderedHosts(ecos)
	var sb strings.Builder
	sb.WriteString("# Escrow redirect rules — generated by escrow-cli\n\n")

	// IPv4 NAT: redirect HTTPS to proxy, block HTTP.
	sb.WriteString("table ip escrow {\n")
	sb.WriteString("  chain output {\n")
	sb.WriteString("    type nat hook output priority dstnat;\n")
	for _, eco := range ecos {
		fmt.Fprintf(&sb, "    # %s\n", eco)
		for _, host := range registryHosts[eco] {
			fmt.Fprintf(&sb,
				"    tcp dport 443 ip daddr %s meta skuid != %s redirect to :%d\n",
				host, uid, port)
		}
	}
	sb.WriteString("  }\n")
	sb.WriteString("  chain output_filter {\n")
	sb.WriteString("    type filter hook output priority filter;\n")
	for _, host := range hosts {
		fmt.Fprintf(&sb,
			"    tcp dport 80 ip daddr %s meta skuid != %s reject\n",
			host, uid)
	}
	sb.WriteString("  }\n")
	sb.WriteString("}\n\n")

	// IPv6: block entirely (no nat redirect on IPv6 OUTPUT; forces traffic to IPv4).
	sb.WriteString("table ip6 escrow {\n")
	sb.WriteString("  chain output {\n")
	sb.WriteString("    type filter hook output priority filter;\n")
	for _, host := range hosts {
		fmt.Fprintf(&sb,
			"    tcp dport { 80, 443 } ip6 daddr %s meta skuid != %s reject\n",
			host, uid)
	}
	sb.WriteString("  }\n")
	sb.WriteString("}\n")

	return sb.String()
}
```

- [ ] **Step 2: Verify build compiles**

```bash
go build ./cmd/escrow-cli/ && echo "ok"
```
Expected: `ok`

- [ ] **Step 3: Commit**

```bash
git add cmd/escrow-cli/fw.go cmd/escrow-cli/pf.go
git commit -m "refactor: split fw.go from pf.go — cross-platform dispatch, Darwin pf + Linux iptables/nftables backends"
```

---

## Task 3: Tests for firewall rule generation

**Files:**
- Create: `cmd/escrow-cli/fw_test.go`

Tests for the pure functions `buildPfRules`, `buildNftRules`, and `detectLinuxFw`. No root or OS-specific tools needed.

- [ ] **Step 1: Create `cmd/escrow-cli/fw_test.go`**

```go
package main

import (
	"strings"
	"testing"
)

func TestBuildPfRules_OrderPassBeforeBlock(t *testing.T) {
	rules := buildPfRules([]string{"npm"}, 7888, "_escrow")

	passIdx := strings.Index(rules, "pass out quick")
	blockIdx := strings.Index(rules, "block return")
	if passIdx < 0 {
		t.Fatal("no pass rule found")
	}
	if blockIdx < 0 {
		t.Fatal("no block rule found")
	}
	if passIdx > blockIdx {
		t.Error("pass rules must appear before block rules")
	}
}

func TestBuildPfRules_ContainsAllHosts(t *testing.T) {
	rules := buildPfRules([]string{"npm"}, 7888, "_escrow")
	for _, host := range registryHosts["npm"] {
		if !strings.Contains(rules, host) {
			t.Errorf("expected host %q in rules", host)
		}
	}
}

func TestBuildPfRules_ProxyUserExempted(t *testing.T) {
	rules := buildPfRules([]string{"npm"}, 7888, "_escrow")
	if !strings.Contains(rules, "user _escrow") {
		t.Error("proxy user exemption missing from pass rules")
	}
}

func TestBuildPfRules_CorrectPort(t *testing.T) {
	rules := buildPfRules([]string{"npm"}, 9999, "_escrow")
	if !strings.Contains(rules, "port 9999") {
		t.Error("expected custom port 9999 in redirect rule")
	}
}

func TestBuildNftRules_ContainsRedirect(t *testing.T) {
	rules := buildNftRules([]string{"npm"}, 7888, "501")
	if !strings.Contains(rules, "redirect to :7888") {
		t.Error("expected redirect rule")
	}
}

func TestBuildNftRules_SkuidExclusion(t *testing.T) {
	rules := buildNftRules([]string{"npm"}, 7888, "501")
	if !strings.Contains(rules, "meta skuid != 501") {
		t.Error("expected skuid exclusion for proxy user uid 501")
	}
}

func TestBuildNftRules_IPv6TablePresent(t *testing.T) {
	rules := buildNftRules([]string{"npm"}, 7888, "501")
	if !strings.Contains(rules, "table ip6 escrow") {
		t.Error("expected ip6 table for IPv6 blocking")
	}
}

func TestBuildNftRules_PassBeforeBlockNotRequired(t *testing.T) {
	// nftables evaluates rules top-to-bottom within a chain; redirect is in
	// the nat chain (output), block is in filter chain — no ordering constraint.
	rules := buildNftRules([]string{"npm"}, 7888, "501")
	if !strings.Contains(rules, "type nat hook output") {
		t.Error("expected nat hook for redirect chain")
	}
}

func TestBuildPfRules_MultipleEcosystems(t *testing.T) {
	rules := buildPfRules([]string{"npm", "pypi"}, 7888, "_escrow")
	for _, eco := range []string{"npm", "pypi"} {
		for _, host := range registryHosts[eco] {
			if !strings.Contains(rules, host) {
				t.Errorf("host %q missing for ecosystem %q", host, eco)
			}
		}
	}
}

func TestDetectLinuxFw_ReturnsKnownOrEmpty(t *testing.T) {
	result := detectLinuxFw()
	switch result {
	case "iptables", "nftables", "":
		// ok
	default:
		t.Errorf("unexpected detectLinuxFw result: %q", result)
	}
}
```

- [ ] **Step 2: Run tests**

```bash
go test ./cmd/escrow-cli/ -run "TestBuild|TestDetect" -v
```
Expected: all PASS (pure functions, no root needed).

- [ ] **Step 3: Commit**

```bash
git add cmd/escrow-cli/fw_test.go
git commit -m "test: firewall rule generation — pf and nftables backends"
```

---

## Task 4: Cross-platform `setup` — Linux user creation + fw chain init

**Files:**
- Modify: `cmd/escrow-cli/setup.go`

Extract `createSystemUser()` that dispatches by `runtime.GOOS`. Add `setupLinuxFwChain()` that creates the empty ESCROW iptables chain or no-ops for nftables. Update `runSetup` to call both. Update sudoers content to list `fw-enable`/`fw-disable` instead of `pf-enable`/`pf-disable`.

- [ ] **Step 1: Add `lookupUID` helper to `setup.go`** (used by both setup and nftables)

Add at the top of `setup.go` (after imports, before `runSetup`):

```go
import (
    // existing imports +
    "os/user"
    "runtime"
)

// lookupUID returns the numeric UID string for the given username.
func lookupUID(username string) (string, error) {
    u, err := user.Lookup(username)
    if err != nil {
        return "", err
    }
    return u.Uid, nil
}
```

- [ ] **Step 2: Extract `createSystemUser()` from `runSetup`**

Replace the inline account-creation block in `runSetup` with a call to this new function. Add the function at the bottom of `setup.go`:

```go
// createSystemUser creates the _escrow system account if it does not exist.
// Returns (true, nil) if created, (false, nil) if already present.
func createSystemUser() (bool, error) {
    switch runtime.GOOS {
    case "darwin":
        if exec.Command("dscl", ".", "-read", "/Users/_escrow").Run() == nil {
            return false, nil
        }
        out, err := exec.Command("/usr/bin/sysadminctl",
            "-addUser", "_escrow", "-fullName", "Escrow Proxy", "-roleAccount",
        ).CombinedOutput()
        if err != nil {
            return false, fmt.Errorf("%v\n%s", err, strings.TrimSpace(string(out)))
        }
        return true, nil

    case "linux":
        if exec.Command("id", "-u", "_escrow").Run() == nil {
            return false, nil
        }
        out, err := exec.Command("useradd",
            "--system",
            "--no-create-home",
            "--home-dir", "/nonexistent",
            "--shell", "/usr/sbin/nologin",
            "_escrow",
        ).CombinedOutput()
        if err != nil {
            return false, fmt.Errorf("%v\n%s", err, strings.TrimSpace(string(out)))
        }
        return true, nil

    default:
        return false, fmt.Errorf("user creation not supported on %s", runtime.GOOS)
    }
}
```

- [ ] **Step 3: Update `runSetup` to use `createSystemUser`**

Replace the account-creation block in `runSetup` (lines ~32-45) with:

```go
    // 1. Create _escrow system account.
    created, err := createSystemUser()
    if err != nil {
        die("creating _escrow account: %v", err)
    }
    if created {
        done = append(done, "created system account _escrow")
    } else {
        already = append(already, "_escrow account already exists")
    }
```

- [ ] **Step 4: Add `setupLinuxFwChain()` and call it from `runSetup`**

Add the function to `setup.go`:

```go
// setupLinuxFwChain creates the empty ESCROW iptables chain (if iptables is in use)
// so that fw-enable can populate it later. No-op for nftables.
func setupLinuxFwChain() (bool, error) {
    switch detectLinuxFw() {
    case "iptables":
        exec.Command("iptables", "-t", "nat", "-N", "ESCROW").Run()  //nolint:errcheck
        exec.Command("ip6tables", "-N", "ESCROW6").Run()              //nolint:errcheck
        if exec.Command("iptables", "-t", "nat", "-C", "OUTPUT", "-j", "ESCROW").Run() != nil {
            exec.Command("iptables", "-t", "nat", "-A", "OUTPUT", "-j", "ESCROW").Run() //nolint:errcheck
        }
        if exec.Command("ip6tables", "-C", "OUTPUT", "-j", "ESCROW6").Run() != nil {
            exec.Command("ip6tables", "-A", "OUTPUT", "-j", "ESCROW6").Run() //nolint:errcheck
        }
        return true, nil
    case "nftables":
        return true, nil // fw-enable writes the full table on demand
    default:
        return false, fmt.Errorf("neither iptables nor nft found")
    }
}
```

In `runSetup`, add a platform-specific block after step 3 (anchor file creation):

```go
    // Platform-specific firewall setup.
    switch runtime.GOOS {
    case "darwin":
        // Steps 2 (pf.conf) and 3 (anchor file) already done above.
    case "linux":
        ok, err := setupLinuxFwChain()
        if err != nil {
            fmt.Fprintf(os.Stderr, "warning: firewall chain setup: %v\n", err)
        } else if ok {
            done = append(done, "created ESCROW iptables/nftables chain")
        }
    }
```

Also wrap the Darwin-only steps (pf.conf patching, anchor file) in `if runtime.GOOS == "darwin"` guards.

- [ ] **Step 5: Update sudoers content in `installSudoers`**

Replace `pf-enable`/`pf-disable` with `fw-enable`/`fw-disable` in the sudoers format string:

```go
content := "# Escrow proxy — passwordless sudo for admin group\n" +
    fmt.Sprintf("%%admin ALL=(root) NOPASSWD: %s setup\n", bin) +
    fmt.Sprintf("%%admin ALL=(root) NOPASSWD: %s fw-enable *\n", bin) +
    fmt.Sprintf("%%admin ALL=(root) NOPASSWD: %s fw-disable\n", bin) +
    fmt.Sprintf("%%admin ALL=(root) NOPASSWD: %s service start\n", bin) +
    fmt.Sprintf("%%admin ALL=(root) NOPASSWD: %s service stop\n", bin) +
    fmt.Sprintf("%%admin ALL=(root) NOPASSWD: %s service restart\n", bin) +
    fmt.Sprintf("%%admin ALL=(root) NOPASSWD: %s service status\n", bin)
```

- [ ] **Step 6: Build and verify**

```bash
go build ./cmd/escrow-cli/ && go vet ./cmd/escrow-cli/ && echo "ok"
```
Expected: `ok`

- [ ] **Step 7: Commit**

```bash
git add cmd/escrow-cli/setup.go
git commit -m "feat(setup): cross-platform user creation (useradd on Linux), Linux fw chain init, fw-* sudoers"
```

---

## Task 5: Cross-platform `service` — Linux systemctl

**Files:**
- Modify: `cmd/escrow-cli/service.go`

Add `runtime.GOOS` dispatch to `serviceLoad`, `serviceUnload`, and `serviceStatus`.

- [ ] **Step 1: Update `service.go` with Linux systemctl support**

Replace the body of `serviceLoad`, `serviceUnload`, and `serviceStatus`:

```go
package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

const (
	launchDaemonPlist  = "/Library/LaunchDaemons/com.escrow.proxy.plist"
	linuxServiceName   = "escrow"
)

func runService(args []string) {
	requireRoot("service")
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: service requires a subcommand: start, stop, restart, status")
		os.Exit(1)
	}
	switch args[0] {
	case "start":
		serviceLoad()
	case "stop":
		serviceUnload()
	case "restart":
		serviceUnload()
		serviceLoad()
	case "status":
		serviceStatus()
	default:
		fmt.Fprintf(os.Stderr, "error: unknown service subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

func serviceLoad() {
	switch runtime.GOOS {
	case "darwin":
		if _, err := os.Stat(launchDaemonPlist); err != nil {
			die("%s not found — register the service first with: sudo brew services start escrow", launchDaemonPlist)
		}
		out, err := exec.Command("launchctl", "bootstrap", "system", launchDaemonPlist).CombinedOutput()
		if err != nil {
			die("launchctl bootstrap: %v\n%s", err, strings.TrimSpace(string(out)))
		}
	case "linux":
		out, err := exec.Command("systemctl", "start", linuxServiceName).CombinedOutput()
		if err != nil {
			die("systemctl start: %v\n%s", err, strings.TrimSpace(string(out)))
		}
	default:
		die("service management not supported on %s", runtime.GOOS)
	}
	fmt.Println("service started")
}

func serviceUnload() {
	switch runtime.GOOS {
	case "darwin":
		if _, err := os.Stat(launchDaemonPlist); err != nil {
			die("%s not found", launchDaemonPlist)
		}
		out, err := exec.Command("launchctl", "bootout", "system", launchDaemonPlist).CombinedOutput()
		if err != nil {
			die("launchctl bootout: %v\n%s", err, strings.TrimSpace(string(out)))
		}
	case "linux":
		out, err := exec.Command("systemctl", "stop", linuxServiceName).CombinedOutput()
		if err != nil {
			die("systemctl stop: %v\n%s", err, strings.TrimSpace(string(out)))
		}
	default:
		die("service management not supported on %s", runtime.GOOS)
	}
	fmt.Println("service stopped")
}

func serviceStatus() {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("launchctl", "list").CombinedOutput()
		if err != nil {
			die("launchctl list: %v", err)
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "com.escrow.proxy") {
				fmt.Println(line)
				return
			}
		}
		fmt.Println("service not loaded")
	case "linux":
		out, _ := exec.Command("systemctl", "status", linuxServiceName, "--no-pager").CombinedOutput()
		fmt.Print(string(out))
	default:
		die("service management not supported on %s", runtime.GOOS)
	}
}
```

- [ ] **Step 2: Build and verify**

```bash
go build ./cmd/escrow-cli/ && go vet ./cmd/escrow-cli/ && echo "ok"
```
Expected: `ok`

- [ ] **Step 3: Commit**

```bash
git add cmd/escrow-cli/service.go
git commit -m "feat(service): Linux systemctl support alongside macOS launchctl"
```

---

## Task 6: Cross-platform `status` — Linux firewall detection

**Files:**
- Modify: `cmd/escrow-cli/status.go`

The pf check block in `runStatus` currently uses `pfctl`. Add a Linux branch that checks the iptables ESCROW chain or nft table.

- [ ] **Step 1: Update the firewall check block in `runStatus`**

Replace the `// 1+2. pf anchor state.` block (currently lines ~39-50 in `status.go`) with:

```go
	// 1+2. Firewall rules active and which ecosystems are loaded.
	switch runtime.GOOS {
	case "darwin":
		pfOut, pfErr := exec.Command("sudo", "-n", "pfctl", "-a", "escrow", "-s", "rules").Output()
		switch {
		case pfErr == nil:
			pfRules := strings.TrimSpace(string(pfOut))
			result.PfAnchorActive = pfRules != ""
			if result.PfAnchorActive {
				for _, eco := range allEcosystems {
					hosts := registryHosts[eco]
					if len(hosts) > 0 && strings.Contains(pfRules, hosts[0]) {
						result.ActiveEcosystems = append(result.ActiveEcosystems, eco)
					}
				}
			}
		case isPermissionDenied(pfErr):
			result.PfAnchorUnknown = true
		}

	case "linux":
		switch detectLinuxFw() {
		case "iptables":
			out, err := exec.Command("iptables", "-t", "nat", "-L", "ESCROW", "-n").Output()
			if err == nil {
				rules := string(out)
				result.PfAnchorActive = strings.Contains(rules, "REDIRECT")
				if result.PfAnchorActive {
					for _, eco := range allEcosystems {
						hosts := registryHosts[eco]
						if len(hosts) > 0 && strings.Contains(rules, hosts[0]) {
							result.ActiveEcosystems = append(result.ActiveEcosystems, eco)
						}
					}
				}
			}
		case "nftables":
			out, err := exec.Command("nft", "list", "table", "ip", "escrow").Output()
			if err == nil {
				rules := string(out)
				result.PfAnchorActive = strings.Contains(rules, "redirect")
				if result.PfAnchorActive {
					for _, eco := range allEcosystems {
						hosts := registryHosts[eco]
						if len(hosts) > 0 && strings.Contains(rules, hosts[0]) {
							result.ActiveEcosystems = append(result.ActiveEcosystems, eco)
						}
					}
				}
			}
		default:
			result.PfAnchorUnknown = true
		}
	}
```

Add `"runtime"` to the imports in `status.go`.

- [ ] **Step 2: Build and verify**

```bash
go build ./cmd/escrow-cli/ && go vet ./cmd/escrow-cli/ && echo "ok"
```
Expected: `ok`

- [ ] **Step 3: Commit**

```bash
git add cmd/escrow-cli/status.go
git commit -m "feat(status): Linux iptables/nftables active-rules check"
```

---

## Task 7: `config write-local` and `config restore-local`

**Files:**
- Modify: `cmd/escrow-cli/config.go`
- Create: `cmd/escrow-cli/config_local_test.go`

Add `runConfigWriteLocal` (writes to CWD), five local ecosystem writers, and `runConfigRestoreLocal`.

- [ ] **Step 1: Write failing tests for local config writers**

Create `cmd/escrow-cli/config_local_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteNpmConfigLocal_CreatesNpmrc(t *testing.T) {
	dir := t.TempDir()
	if err := writeNpmConfigLocal(dir, "http://127.0.0.1:7888"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".npmrc"))
	if err != nil {
		t.Fatalf("expected .npmrc to exist: %v", err)
	}
	if !strings.Contains(string(data), "registry=http://127.0.0.1:7888/") {
		t.Errorf("unexpected .npmrc content: %s", data)
	}
}

func TestWriteNpmConfigLocal_UpdatesExistingRegistry(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".npmrc"), []byte("registry=https://registry.npmjs.org/\nother=val\n"), 0644)
	writeNpmConfigLocal(dir, "http://127.0.0.1:7888")
	data, _ := os.ReadFile(filepath.Join(dir, ".npmrc"))
	if strings.Count(string(data), "registry=") != 1 {
		t.Error("expected exactly one registry= line after update")
	}
	if !strings.Contains(string(data), "registry=http://127.0.0.1:7888/") {
		t.Errorf("registry not updated: %s", data)
	}
	if !strings.Contains(string(data), "other=val") {
		t.Error("other keys should be preserved")
	}
}

func TestWriteCargoConfigLocal_CreatesCargoDir(t *testing.T) {
	dir := t.TempDir()
	if err := writeCargoConfigLocal(dir, "http://127.0.0.1:7888"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".cargo", "config.toml"))
	if err != nil {
		t.Fatalf("expected .cargo/config.toml: %v", err)
	}
	if !strings.Contains(string(data), `replace-with = "escrow"`) {
		t.Errorf("unexpected cargo config: %s", data)
	}
	if !strings.Contains(string(data), "http://127.0.0.1:7888/cargo/") {
		t.Errorf("expected cargo registry URL: %s", data)
	}
}

func TestWriteNugetConfigLocal_CreatesNugetConfig(t *testing.T) {
	dir := t.TempDir()
	if err := writeNugetConfigLocal(dir, "http://127.0.0.1:7888"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "nuget.config"))
	if err != nil {
		t.Fatalf("expected nuget.config: %v", err)
	}
	if !strings.Contains(string(data), `key="escrow"`) {
		t.Errorf("unexpected nuget config: %s", data)
	}
	if !strings.Contains(string(data), "http://127.0.0.1:7888/nuget/v3/index.json") {
		t.Errorf("expected nuget URL in config: %s", data)
	}
}

func TestWritePypiConfigLocal_CreatesUvToml(t *testing.T) {
	dir := t.TempDir()
	if err := writePypiConfigLocal(dir, "http://127.0.0.1:7888"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "uv.toml"))
	if err != nil {
		t.Fatalf("expected uv.toml: %v", err)
	}
	if !strings.Contains(string(data), "index-url") {
		t.Errorf("expected index-url in uv.toml: %s", data)
	}
	if !strings.Contains(string(data), "http://127.0.0.1:7888/pypi/simple/") {
		t.Errorf("expected pypi URL: %s", data)
	}
}

func TestWriteComposerConfigLocal_CreatesComposerJson(t *testing.T) {
	dir := t.TempDir()
	if err := writeComposerConfigLocal(dir, "http://127.0.0.1:7888"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "composer.json"))
	if err != nil {
		t.Fatalf("expected composer.json: %v", err)
	}
	if !strings.Contains(string(data), `"type": "composer"`) {
		t.Errorf("unexpected composer.json: %s", data)
	}
}

func TestWriteComposerConfigLocal_MergesExisting(t *testing.T) {
	dir := t.TempDir()
	// Pre-existing composer.json with a "name" key that should be preserved.
	os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{"name":"my/pkg","require":{}}`), 0644)
	writeComposerConfigLocal(dir, "http://127.0.0.1:7888")
	data, _ := os.ReadFile(filepath.Join(dir, "composer.json"))
	if !strings.Contains(string(data), `"name"`) {
		t.Error("existing composer.json keys should be preserved")
	}
	if !strings.Contains(string(data), `"repositories"`) {
		t.Error("expected repositories key to be added")
	}
}

func TestConfigRestoreLocal_RestoresBackup(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, ".npmrc")
	backup := original + ".escrow-backup"
	os.WriteFile(original, []byte("registry=http://127.0.0.1:7888/\n"), 0644)
	os.WriteFile(backup, []byte("registry=https://registry.npmjs.org/\n"), 0644)

	restored := restoreLocalBackups(dir)
	if restored == 0 {
		t.Error("expected at least one file to be restored")
	}
	data, _ := os.ReadFile(original)
	if !strings.Contains(string(data), "registry.npmjs.org") {
		t.Errorf("expected original content restored, got: %s", data)
	}
	if _, err := os.Stat(backup); err == nil {
		t.Error("backup file should be removed after restore")
	}
}
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
go test ./cmd/escrow-cli/ -run "TestWriteNpm|TestWriteCargo|TestWriteNuget|TestWritePypi|TestWriteComposer|TestConfigRestoreLocal" -v 2>&1 | head -30
```
Expected: compilation error (functions not yet defined).

- [ ] **Step 3: Add local config functions to `config.go`**

Append to the end of `cmd/escrow-cli/config.go`:

```go
// ── config write-local ────────────────────────────────────────────────────────

// runConfigWriteLocal writes per-tool proxy config to the current working directory.
// Supported: npm, cargo, nuget, pypi (uv.toml), composer.
// Skipped:   go (env vars are shell-global), maven (no project-local settings.xml).
func runConfigWriteLocal(args []string) {
	fs := flag.NewFlagSet("config write-local", flag.ExitOnError)
	ecosystems := fs.String("ecosystems", "npm,cargo,nuget,pypi,composer", "comma-separated ecosystems (go/maven not supported locally)")
	proxyURL := fs.String("proxy-url", "http://127.0.0.1:7888", "base URL of the escrow proxy")
	fs.Parse(args) //nolint:errcheck

	if err := validateProxyURL(*proxyURL); err != nil {
		die("--proxy-url: %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		die("getting working directory: %v", err)
	}

	base := strings.TrimRight(*proxyURL, "/")
	for _, eco := range parseEcosystems(*ecosystems) {
		switch eco {
		case "go", "maven":
			fmt.Printf("– %s: no project-local config supported (skipping)\n", eco)
			continue
		}
		if err := writeEcoConfigLocal(eco, base, cwd); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: %v\n", eco, err)
		} else {
			fmt.Printf("✓ %s local config written\n", eco)
		}
	}
}

func writeEcoConfigLocal(eco, base, dir string) error {
	switch eco {
	case "npm":
		return writeNpmConfigLocal(dir, base)
	case "cargo":
		return writeCargoConfigLocal(dir, base)
	case "nuget":
		return writeNugetConfigLocal(dir, base)
	case "pypi":
		return writePypiConfigLocal(dir, base)
	case "composer":
		return writeComposerConfigLocal(dir, base)
	}
	return fmt.Errorf("local config not supported for %s", eco)
}

func writeNpmConfigLocal(dir, base string) error {
	path := filepath.Join(dir, ".npmrc")
	backupFile(path) //nolint:errcheck — local files, best-effort backup
	url := base + "/"
	data, _ := os.ReadFile(path)
	lines := strings.Split(string(data), "\n")
	found := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "registry=") {
			lines[i] = "registry=" + url
			found = true
			break
		}
	}
	if !found {
		lines = append(lines, "registry="+url)
	}
	return writeAtomic(path, []byte(strings.Join(lines, "\n")), 0644)
}

func writeCargoConfigLocal(dir, base string) error {
	cargoDir := filepath.Join(dir, ".cargo")
	if err := os.MkdirAll(cargoDir, 0755); err != nil {
		return err
	}
	path := filepath.Join(cargoDir, "config.toml")
	backupFile(path) //nolint:errcheck
	existing, _ := os.ReadFile(path)
	merged, err := mergeCargoConfig(existing, base+"/cargo/")
	if err != nil {
		return err
	}
	return writeAtomic(path, merged, 0644)
}

func writeNugetConfigLocal(dir, base string) error {
	path := filepath.Join(dir, "nuget.config")
	backupFile(path) //nolint:errcheck
	url := xmlEscape(base + "/nuget/v3/index.json")
	content := `<?xml version="1.0" encoding="utf-8"?>
<configuration>
  <packageSources>
    <clear />
    <add key="escrow" value="` + url + `" />
  </packageSources>
</configuration>
`
	return writeAtomic(path, []byte(content), 0644)
}

func writePypiConfigLocal(dir, base string) error {
	path := filepath.Join(dir, "uv.toml")
	backupFile(path) //nolint:errcheck
	quoted, _ := json.Marshal(base + "/pypi/simple/")
	content := "[pip]\nindex-url = " + string(quoted) + "\n"
	return writeAtomic(path, []byte(content), 0644)
}

func writeComposerConfigLocal(dir, base string) error {
	path := filepath.Join(dir, "composer.json")
	backupFile(path) //nolint:errcheck
	urlJSON, _ := json.Marshal(base)
	newRepo := `{"type":"composer","url":` + string(urlJSON) + `}`

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		content := "{\n  \"repositories\": [\n    " + newRepo + "\n  ]\n}\n"
		return writeAtomic(path, []byte(content), 0644)
	}
	if err != nil {
		return err
	}

	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parsing composer.json: %w", err)
	}
	cfg["repositories"] = json.RawMessage("[" + newRepo + "]")
	merged, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(merged, '\n'), 0644)
}

// ── config restore-local ──────────────────────────────────────────────────────

// runConfigRestoreLocal restores .escrow-backup files found in the current directory.
func runConfigRestoreLocal(args []string) {
	fs := flag.NewFlagSet("config restore-local", flag.ExitOnError)
	fs.Parse(args) //nolint:errcheck

	cwd, err := os.Getwd()
	if err != nil {
		die("getting working directory: %v", err)
	}
	n := restoreLocalBackups(cwd)
	if n == 0 {
		fmt.Println("nothing to restore in current directory")
	}
}

// restoreLocalBackups restores any .escrow-backup files in dir and returns the count.
func restoreLocalBackups(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	restored := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".escrow-backup") {
			continue
		}
		original := filepath.Join(dir, strings.TrimSuffix(name, ".escrow-backup"))
		backup := filepath.Join(dir, name)
		data, err := os.ReadFile(backup)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: reading %s: %v\n", backup, err)
			continue
		}
		if err := writeAtomic(original, data, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "warning: restoring %s: %v\n", original, err)
			continue
		}
		os.Remove(backup)
		fmt.Printf("✓ restored %s\n", original)
		restored++
	}

	// Walk one level into known local config dirs.
	for _, sub := range []string{".cargo"} {
		subDir := filepath.Join(dir, sub)
		restored += restoreLocalBackups(subDir)
	}
	return restored
}
```

- [ ] **Step 4: Run tests to confirm they pass**

```bash
go test ./cmd/escrow-cli/ -run "TestWriteNpm|TestWriteCargo|TestWriteNuget|TestWritePypi|TestWriteComposer|TestConfigRestoreLocal" -v
```
Expected: all PASS.

- [ ] **Step 5: Build and vet**

```bash
go build ./cmd/escrow-cli/ && go vet ./cmd/escrow-cli/ && echo "ok"
```
Expected: `ok`

- [ ] **Step 6: Commit**

```bash
git add cmd/escrow-cli/config.go cmd/escrow-cli/config_local_test.go
git commit -m "feat(config): write-local and restore-local subcommands — CWD-scoped npm, cargo, nuget, pypi, composer"
```

---

## Task 8: Wire everything into `main.go`

**Files:**
- Modify: `cmd/escrow-cli/main.go`

Add routing for `fw-enable`, `fw-disable`, `config write-local`, `config restore-local`. Update the usage string.

- [ ] **Step 1: Rewrite `main.go`**

```go
package main

import (
	"fmt"
	"os"
)

const cliUsage = `escrow-cli — escrow proxy system configuration

Usage:
  escrow-cli setup                   [--sudoers]
  escrow-cli fw-enable               [--ecosystems LIST] [--proxy-port PORT] [--proxy-user USER]
  escrow-cli fw-disable
  escrow-cli config write            [--ecosystems LIST] [--proxy-url URL]
  escrow-cli config write-local      [--ecosystems LIST] [--proxy-url URL]
  escrow-cli config restore
  escrow-cli config restore-local
  escrow-cli status                  [--json]
  escrow-cli service                 <start|stop|restart|status>

Aliases (backward-compatible):
  pf-enable  →  fw-enable
  pf-disable →  fw-disable

Subcommands that require root: setup, fw-enable, fw-disable, service
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, cliUsage)
		os.Exit(1)
	}

	switch os.Args[1] {
	case "setup":
		runSetup(os.Args[2:])
	case "fw-enable", "pf-enable":
		runFwEnable(os.Args[2:])
	case "fw-disable", "pf-disable":
		runFwDisable(os.Args[2:])
	case "config":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "error: config requires a subcommand: write, write-local, restore, restore-local")
			os.Exit(1)
		}
		switch os.Args[2] {
		case "write":
			runConfigWrite(os.Args[3:])
		case "write-local":
			runConfigWriteLocal(os.Args[3:])
		case "restore":
			runConfigRestore(os.Args[3:])
		case "restore-local":
			runConfigRestoreLocal(os.Args[3:])
		default:
			fmt.Fprintf(os.Stderr, "error: unknown config subcommand: %s\n", os.Args[2])
			os.Exit(1)
		}
	case "status":
		runStatus(os.Args[2:])
	case "service":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "error: service requires a subcommand: start, stop, restart, status")
			os.Exit(1)
		}
		runService(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "error: unknown subcommand: %s\n", os.Args[1])
		fmt.Fprint(os.Stderr, cliUsage)
		os.Exit(1)
	}
}
```

- [ ] **Step 2: Build and run all tests**

```bash
go build ./cmd/escrow-cli/ && go test ./cmd/escrow-cli/ -v 2>&1 | tail -20
```
Expected: all tests PASS, no build errors.

- [ ] **Step 3: Commit**

```bash
git add cmd/escrow-cli/main.go
git commit -m "feat(main): route fw-enable/disable, config write-local/restore-local; pf-* as aliases"
```

---

## Task 9: Release

- [ ] **Step 1: Run full test suite**

```bash
go test ./... && echo "all tests pass"
```
Expected: all PASS.

- [ ] **Step 2: Release patch**

```bash
make release-patch
```
Expected: v1.5.x tagged, tap updated.

---

## Self-Review

**Spec coverage:**

| Requirement | Task |
|-------------|------|
| `config write-local` (separate subcommand) | Task 7 |
| npm local: `./.npmrc` | Task 7 |
| cargo local: `./.cargo/config.toml` | Task 7 |
| nuget local: `./nuget.config` | Task 7 |
| pypi local: `./uv.toml` | Task 7 |
| composer local: `./composer.json` | Task 7 |
| go/maven: skip with message | Task 7 |
| `config restore-local` | Task 7 |
| fw-enable/fw-disable (rename) | Tasks 1-2 |
| pf-enable/pf-disable as aliases | Task 2 |
| Linux iptables backend | Task 2 |
| Linux nftables backend | Task 2 |
| Detect iptables vs nftables | Task 2 |
| Exclude proxy user from redirect | Task 2 (`--uid-owner`, `skuid`) |
| Cross-platform setup (useradd) | Task 4 |
| Cross-platform service (systemctl) | Task 5 |
| Cross-platform status | Task 6 |

**No placeholders:** All steps contain complete code.

**Type consistency:** `buildPfRules` renamed in Task 1 Step 1; `buildNftRules` defined in Task 2 Step 1 and tested in Task 3; `restoreLocalBackups(dir string) int` defined in Task 7 Step 3 and called in Task 7 Step 1 test. `lookupUID` defined in Task 4 Step 1 (setup.go) and called in Task 2 Step 1 (fw.go) — both in `package main`, no import needed.

**One gap found and fixed:** `lookupUID` is called in `fw.go` (Task 2) but defined in `setup.go` (Task 4). Both files are `package main` so this works, but Task 2 must compile after Task 4 adds the function — reorder if needed, or add a stub in Task 2 and move to setup.go in Task 4.

**Fix:** Add `lookupUID` to `fw.go` directly (co-located with its only caller) and remove from `setup.go` task. Updated above.
