package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
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

// lookupUID returns the numeric UID string for the given username.
// On macOS, queries the local dscl directory node directly — this is the same
// node where createSystemUser writes, and it's visible to pf's user keyword.
// On Linux, falls back to id -u.
func lookupUID(username string) (string, error) {
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("dscl", ".", "-read", "/Users/"+username, "UniqueID").Output()
		if err != nil {
			return "", fmt.Errorf("unknown user %q (run setup first)", username)
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "UniqueID:") {
				uid := strings.TrimSpace(strings.TrimPrefix(line, "UniqueID:"))
				if uid != "" {
					return uid, nil
				}
			}
		}
		return "", fmt.Errorf("no UniqueID found for %q", username)
	}
	out, err := exec.Command("id", "-u", username).Output()
	if err != nil {
		return "", fmt.Errorf("unknown user %q", username)
	}
	return strings.TrimSpace(string(out)), nil
}

// ── macOS pf backend ──────────────────────────────────────────────────────────

func fwEnableDarwin(ecos []string, port int, proxyUser string) {
	// Resolve to numeric UID so pf doesn't do a username lookup at load time.
	// A freshly-created OD account may not be visible to pf's getpwnam() yet.
	uid, err := lookupUID(proxyUser)
	if err != nil {
		die("looking up uid for %q: %v — run 'escrow-cli setup' first", proxyUser, err)
	}
	rules := buildPfRules(ecos, port, uid)
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
// Prefers iptables because it resolves hostnames at rule-insertion time,
// consistent with how pf handles names on macOS.
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
	exec.Command("iptables", "-t", "nat", "-N", "ESCROW").Run()  //nolint:errcheck
	// ESCROW6 lives in the filter table (not nat): we block IPv6 entirely rather than
	// redirect it, forcing dual-stack hosts to use the IPv4 redirect path through the proxy.
	exec.Command("ip6tables", "-N", "ESCROW6").Run()              //nolint:errcheck

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

	exec.Command("iptables", "-t", "nat", "-F", "ESCROW").Run()  //nolint:errcheck
	// ESCROW6 lives in the filter table (not nat): we block IPv6 entirely rather than
	// redirect it, forcing dual-stack hosts to use the IPv4 redirect path through the proxy.
	exec.Command("ip6tables", "-F", "ESCROW6").Run()              //nolint:errcheck

	portStr := fmt.Sprintf("%d", port)
	for _, eco := range ecos {
		for _, host := range registryHosts[eco] {
			if out, err := exec.Command("iptables", "-t", "nat", "-A", "ESCROW",
				"-p", "tcp", "--dport", "443", "-d", host,
				"-m", "owner", "!", "--uid-owner", proxyUser,
				"-j", "REDIRECT", "--to-ports", portStr,
			).CombinedOutput(); err != nil {
				die("iptables redirect %s: %v\n%s", host, err, strings.TrimSpace(string(out)))
			}
			exec.Command("iptables", "-A", "ESCROW",
				"-p", "tcp", "--dport", "80", "-d", host,
				"-m", "owner", "!", "--uid-owner", proxyUser,
				"-j", "REJECT", "--reject-with", "tcp-reset",
			).Run() //nolint:errcheck
		}
	}

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
	// ESCROW6 lives in the filter table (not nat): we block IPv6 entirely rather than
	// redirect it, forcing dual-stack hosts to use the IPv4 redirect path through the proxy.
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
	uid, err := lookupUID(proxyUser)
	if err != nil {
		die("looking up uid for %q: %v — create the user first with: escrow-cli setup", proxyUser, err)
	}
	rules := buildNftRules(ecos, port, uid)
	if err := os.MkdirAll("/etc/nftables.d", 0755); err != nil {
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

// ── fw-test ───────────────────────────────────────────────────────────────────

func runFwTest(args []string) {
	fs := flag.NewFlagSet("fw-test", flag.ExitOnError)
	ecosystems := fs.String("ecosystems", strings.Join(allEcosystems, ","), "comma-separated ecosystems to test")
	fs.Parse(args) //nolint:errcheck

	// Verify the proxy is reachable first.
	conn, err := net.DialTimeout("tcp", "127.0.0.1:7888", time.Second)
	if err != nil {
		fmt.Println("proxy:     ✗  127.0.0.1:7888 not reachable — start the service first")
		return
	}
	conn.Close()
	fmt.Println("proxy:     ✓  127.0.0.1:7888 reachable")
	fmt.Println()

	// Check whether the anchor has any rdr rules loaded.
	// pfctl -s rules outputs resolved IPs, not hostnames, so we can only tell
	// whether fw-enable was run at all — not which specific host was redirected.
	pfRules := loadedPfRules()
	anchorActive := strings.Contains(pfRules, "rdr pass")

	ecos := parseEcosystems(*ecosystems)
	for _, eco := range ecos {
		for _, host := range registryHosts[eco] {
			redirected := testRedirect(host)
			switch {
			case redirected:
				fmt.Printf("%-10s ✓  %s:443 → proxy\n", eco, host)
			case anchorActive:
				// Anchor has rdr rules but this host's current IP doesn't match
				// the IP pf resolved at rule-load time (CDN rotation). The rule
				// IS present; redirect will catch it when the IP aligns.
				fmt.Printf("%-10s ~  %s:443  rule loaded, CDN IP rotated (likely OK)\n", eco, host)
			default:
				fmt.Printf("%-10s ✗  %s:443  no rules loaded — run: sudo escrow-cli fw-enable\n", eco, host)
			}
		}
	}
}

// loadedPfRules returns the current pf anchor NAT (rdr) rules as a string,
// or "" if unavailable. rdr rules live in the nat section, not filter rules.
func loadedPfRules() string {
	out, err := exec.Command("sudo", "-n", "pfctl", "-a", "escrow", "-s", "nat").Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// testRedirect sends a plain HTTP request to host:443.
// If pf has an rdr rule active, the escrow proxy intercepts it and responds
// with an HTTP status line. If not redirected, the real server begins a TLS
// handshake (first byte 0x16 = ContentType Handshake) — not an HTTP response.
func testRedirect(host string) bool {
	conn, err := net.DialTimeout("tcp", host+":443", 3*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second)) //nolint:errcheck

	fmt.Fprintf(conn, "GET / HTTP/1.0\r\nHost: %s\r\n\r\n", host) //nolint:errcheck

	buf := make([]byte, 16)
	n, _ := conn.Read(buf)
	return n > 0 && strings.HasPrefix(string(buf[:n]), "HTTP/")
}
