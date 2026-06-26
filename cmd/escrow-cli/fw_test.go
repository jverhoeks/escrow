package main

import (
	"runtime"
	"strings"
	"testing"
)

func TestBuildPfRules_OrderPassBeforeBlock(t *testing.T) {
	rules := buildPfRules([]string{"npm"}, 7888, "501", false)
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
	rules := buildPfRules([]string{"npm"}, 7888, "501", false)
	for _, host := range registryHosts["npm"] {
		if !strings.Contains(rules, host) {
			t.Errorf("expected host %q in rules", host)
		}
	}
}

func TestBuildPfRules_ProxyUserExempted(t *testing.T) {
	// buildPfRules takes a numeric UID string, not a username.
	rules := buildPfRules([]string{"npm"}, 7888, "501", false)
	if !strings.Contains(rules, "user 501") {
		t.Error("proxy user UID exemption missing from pass rules")
	}
}

func TestBuildPfRules_CorrectPort(t *testing.T) {
	rules := buildPfRules([]string{"npm"}, 9999, "501", false)
	if !strings.Contains(rules, "port 9999") {
		t.Error("expected custom port 9999 in redirect rule")
	}
}

func TestBuildNftRules_ContainsRedirect(t *testing.T) {
	rules := buildNftRules([]string{"npm"}, 7888, "501", false)
	if !strings.Contains(rules, "redirect to :7888") {
		t.Error("expected redirect rule")
	}
}

func TestBuildNftRules_SkuidExclusion(t *testing.T) {
	rules := buildNftRules([]string{"npm"}, 7888, "501", false)
	if !strings.Contains(rules, "meta skuid != 501") {
		t.Error("expected skuid exclusion for proxy user uid 501")
	}
}

func TestBuildNftRules_IPv6TablePresent(t *testing.T) {
	rules := buildNftRules([]string{"npm"}, 7888, "501", false)
	if !strings.Contains(rules, "table ip6 escrow") {
		t.Error("expected ip6 table for IPv6 blocking")
	}
}

func TestBuildNftRules_NatHookPresent(t *testing.T) {
	rules := buildNftRules([]string{"npm"}, 7888, "501", false)
	if !strings.Contains(rules, "type nat hook output") {
		t.Error("expected nat hook for redirect chain")
	}
}

func TestBuildPfRules_MultipleEcosystems(t *testing.T) {
	rules := buildPfRules([]string{"npm", "pypi"}, 7888, "501", false)
	for _, eco := range []string{"npm", "pypi"} {
		for _, host := range registryHosts[eco] {
			if !strings.Contains(rules, host) {
				t.Errorf("host %q missing for ecosystem %q", host, eco)
			}
		}
	}
}

func TestDetectLinuxFw_ReturnsKnownOrEmpty(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("detectLinuxFw only meaningful on Linux")
	}
	result := detectLinuxFw()
	switch result {
	case "iptables", "nftables", "":
		// ok
	default:
		t.Errorf("unexpected detectLinuxFw result: %q", result)
	}
}

// --block-ipv6 off (default): no host-independent IPv6 cutoff is emitted; the
// pf anchor only carries per-host inet6 blocks (for hosts with an AAAA).
func TestBuildPfRules_BlockIPv6Off_NoBroadCutoff(t *testing.T) {
	rules := buildPfRules([]string{"npm"}, 7888, "501", false)
	if strings.Contains(rules, "to any port {80, 443}") {
		t.Error("default mode must not emit a broad IPv6 cutoff")
	}
}

// --block-ipv6 on: a host-independent IPv6 cutoff blocks all IPv6 :80/:443
// egress except the proxy UID, so a host that gains an AAAA later cannot bypass.
func TestBuildPfRules_BlockIPv6On_BroadCutoff(t *testing.T) {
	rules := buildPfRules([]string{"npm"}, 7888, "501", true)
	if !strings.Contains(rules, "block return out inet6 proto tcp from any to any port {80, 443}") {
		t.Error("--block-ipv6 must emit a broad inet6 block")
	}
	if !strings.Contains(rules, "pass out quick inet6 proto tcp from any to any port {80, 443} user 501") {
		t.Error("--block-ipv6 must exempt the proxy UID over IPv6")
	}
}

func TestBuildNftRules_BlockIPv6On_HostIndependent(t *testing.T) {
	rules := buildNftRules([]string{"npm"}, 7888, "501", true)
	// Host-independent: a dport rule with no ip6 daddr, skuid-excluded.
	if !strings.Contains(rules, "tcp dport { 80, 443 } meta skuid != 501 reject") {
		t.Error("--block-ipv6 must emit a host-independent ip6 reject")
	}
}

func TestBuildNftRules_BlockIPv6Off_PerHost(t *testing.T) {
	rules := buildNftRules([]string{"npm"}, 7888, "501", false)
	if !strings.Contains(rules, "ip6 daddr") {
		t.Error("default mode must emit per-host ip6 daddr rules")
	}
}
