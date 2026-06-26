package egress

import (
	"net"
	"testing"

	"github.com/jverhoeks/escrow/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPolicy_ForwardMode(t *testing.T) {
	pol, err := NewPolicy(config.EgressProxyConfig{
		Policy:     "forward",
		BlockHosts: []string{"evil.example", ".tracker.net"},
		BlockCIDRs: []string{"169.254.0.0/16"},
	})
	require.NoError(t, err)

	cases := []struct {
		name  string
		host  string
		ip    net.IP
		allow bool
	}{
		{"unknown forwarded", "registry.npmjs.org", nil, true},
		{"exact block", "evil.example", nil, false},
		{"suffix block", "ads.tracker.net", nil, false},
		{"suffix block apex", "tracker.net", nil, false},
		{"blocked v4 cidr", "169.254.169.254", net.ParseIP("169.254.169.254"), false},
		{"allowed ip", "1.1.1.1", net.ParseIP("1.1.1.1"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.allow, pol.Check(tc.host, tc.ip).Allow)
		})
	}
}

// In forward mode, SSRF-sensitive ranges (link-local incl. cloud metadata,
// loopback, RFC1918, IPv6 ULA/link-local) are denied by default even with no
// explicit block_cidrs — unless the operator explicitly allows them.
func TestPolicy_ForwardMode_DefaultDenySSRF(t *testing.T) {
	pol, err := NewPolicy(config.EgressProxyConfig{Policy: "forward"})
	require.NoError(t, err)

	cases := []struct {
		name  string
		host  string
		ip    net.IP
		allow bool
	}{
		{"cloud metadata", "metadata.internal", net.ParseIP("169.254.169.254"), false},
		{"rfc1918 10/8", "internal.svc", net.ParseIP("10.1.2.3"), false},
		{"rfc1918 172.16/12", "internal.svc", net.ParseIP("172.16.5.5"), false},
		{"rfc1918 192.168/16", "router", net.ParseIP("192.168.1.1"), false},
		{"loopback", "localhost", net.ParseIP("127.0.0.1"), false},
		{"ipv6 loopback", "localhost", net.ParseIP("::1"), false},
		{"ipv6 link-local", "x", net.ParseIP("fe80::1"), false},
		{"ipv6 ula", "x", net.ParseIP("fd00:ec2::254"), false},
		{"public ip allowed", "registry.npmjs.org", net.ParseIP("104.16.0.1"), true},
		{"hostname no ip allowed", "registry.npmjs.org", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.allow, pol.Check(tc.host, tc.ip).Allow)
		})
	}
}

// An explicit allow_cidrs / allow_hosts entry overrides the default SSRF deny.
func TestPolicy_ForwardMode_ExplicitAllowOverridesDefaultDeny(t *testing.T) {
	pol, err := NewPolicy(config.EgressProxyConfig{
		Policy:     "forward",
		AllowCIDRs: []string{"10.0.0.0/8"},
		AllowHosts: []string{"metadata.allowed"},
	})
	require.NoError(t, err)
	assert.True(t, pol.Check("internal.svc", net.ParseIP("10.1.2.3")).Allow, "explicitly allowed CIDR should pass")
	assert.True(t, pol.Check("metadata.allowed", net.ParseIP("169.254.169.254")).Allow, "explicitly allowed host should pass")
	assert.False(t, pol.Check("other", net.ParseIP("192.168.1.1")).Allow, "non-allowed private IP still denied")
}

func TestPolicy_WhitelistMode(t *testing.T) {
	pol, err := NewPolicy(config.EgressProxyConfig{
		Policy:     "whitelist",
		AllowHosts: []string{"registry.npmjs.org", ".pypi.org"},
		AllowCIDRs: []string{"2001:db8::/32"},
	})
	require.NoError(t, err)
	assert.True(t, pol.Check("registry.npmjs.org", nil).Allow)
	assert.True(t, pol.Check("files.pypi.org", nil).Allow)
	assert.False(t, pol.Check("random.example", nil).Allow)
	assert.True(t, pol.Check("x", net.ParseIP("2001:db8::1")).Allow)
}

func TestNewPolicy_BadCIDR(t *testing.T) {
	_, err := NewPolicy(config.EgressProxyConfig{BlockCIDRs: []string{"not-a-cidr"}})
	require.Error(t, err)
}
