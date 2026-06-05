// Package egress is escrow's Docker-build egress proxy: a host/IP-policy
// forward proxy (rules 2+3 of the build-protection design). No TLS interception.
package egress

import (
	"fmt"
	"net"
	"strings"

	"github.com/jverhoeks/escrow/internal/config"
)

// Decision is the outcome of a policy check.
type Decision struct {
	Allow  bool
	Reason string
}

// Policy decides whether egress to a host/IP is permitted.
type Policy struct {
	whitelist  bool // true => deny-by-default; false => forward-everything
	allowHosts []string
	blockHosts []string
	allowNets  []*net.IPNet
	blockNets  []*net.IPNet
}

// NewPolicy builds a Policy from config. CIDRs are parsed up front (fail fast).
func NewPolicy(cfg config.EgressProxyConfig) (*Policy, error) {
	p := &Policy{
		whitelist:  strings.EqualFold(cfg.Policy, "whitelist"),
		allowHosts: normHosts(cfg.AllowHosts),
		blockHosts: normHosts(cfg.BlockHosts),
	}
	var err error
	if p.allowNets, err = parseCIDRs(cfg.AllowCIDRs); err != nil {
		return nil, err
	}
	if p.blockNets, err = parseCIDRs(cfg.BlockCIDRs); err != nil {
		return nil, err
	}
	return p, nil
}

// Check decides egress for a host (always present) and an optional dst IP
// (nil in explicit mode unless the target is an IP literal).
func (p *Policy) Check(host string, ip net.IP) Decision {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if hostMatch(p.blockHosts, host) || ipMatch(p.blockNets, ip) {
		return Decision{Allow: false, Reason: "blacklisted"}
	}
	if p.whitelist {
		if hostMatch(p.allowHosts, host) || ipMatch(p.allowNets, ip) {
			return Decision{Allow: true, Reason: "whitelisted"}
		}
		return Decision{Allow: false, Reason: "not in whitelist"}
	}
	return Decision{Allow: true, Reason: "forward"}
}

func normHosts(in []string) []string {
	out := make([]string, len(in))
	for i, h := range in {
		out[i] = strings.ToLower(strings.TrimSuffix(h, "."))
	}
	return out
}

func parseCIDRs(in []string) ([]*net.IPNet, error) {
	nets := make([]*net.IPNet, 0, len(in))
	for _, c := range in {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			return nil, fmt.Errorf("egress: invalid CIDR %q: %w", c, err)
		}
		nets = append(nets, n)
	}
	return nets, nil
}

// hostMatch: exact match, or suffix match when the pattern starts with "."
// (".pypi.org" matches "files.pypi.org" and the apex "pypi.org").
func hostMatch(patterns []string, host string) bool {
	for _, p := range patterns {
		if strings.HasPrefix(p, ".") {
			if host == p[1:] || strings.HasSuffix(host, p) {
				return true
			}
		} else if host == p {
			return true
		}
	}
	return false
}

func ipMatch(nets []*net.IPNet, ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
