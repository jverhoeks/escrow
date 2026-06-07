package egress

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExposedBind(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"127.0.0.1", false},
		{"::1", false},
		{"localhost", false},
		{"", false},
		{"0.0.0.0", true},
		{"::", true},
		{"192.168.1.10", true},
	}
	for _, tc := range cases {
		t.Run(tc.host, func(t *testing.T) {
			assert.Equal(t, tc.want, ExposedBind(tc.host), "ExposedBind(%q)", tc.host)
		})
	}
}
