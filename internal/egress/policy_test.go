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
