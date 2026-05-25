package main

import (
	"runtime"
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

func TestBuildNftRules_NatHookPresent(t *testing.T) {
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
