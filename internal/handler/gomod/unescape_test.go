package gomod

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUnescape(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"github.com/!burnt!sushi/toml", "github.com/BurntSushi/toml"},
		{"golang.org/x/text", "golang.org/x/text"},
		{"!a", "A"},
		{"!z", "Z"},
		{"abc", "abc"},
		{"!!", "!!"},  // invalid sequence: ! not followed by a-z, passed through
		{"!1", "!1"},  // invalid sequence: passed through
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, unescape(tc.in), "unescape(%q)", tc.in)
	}
}
