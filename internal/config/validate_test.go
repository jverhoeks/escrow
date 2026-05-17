package config_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/config"
)

func TestValidate_ValidConfig(t *testing.T) {
	cfg := config.DefaultConfig()
	assert.Empty(t, cfg.Validate(), "default config should have no validation errors")
}

func TestValidate_NegativeMinDays(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Policy = &config.PolicyConfig{
		Age: &config.AgePolicyConfig{MinDays: -1, Action: "block"},
	}
	errs := cfg.Validate()
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "negative")
}

func TestValidate_PortZero(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.Port = 0
	errs := cfg.Validate()
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "out of range")
}

func TestValidate_PortTooLarge(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.Port = 99999
	errs := cfg.Validate()
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "out of range")
}

func TestValidate_ValidPort(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.Port = 7888
	assert.Empty(t, cfg.Validate())
}

func TestWarnings_EmptySecret(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Dashboard.Enabled = true
	cfg.Dashboard.Secret = ""
	warnings := cfg.Warnings()
	found := false
	for _, w := range warnings {
		if contains(w, "secret") {
			found = true
		}
	}
	assert.True(t, found, "should warn about empty dashboard secret")
}

func TestWarnings_ZeroMinDays(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Policy = &config.PolicyConfig{
		Age: &config.AgePolicyConfig{MinDays: 0, Action: "block"},
	}
	warnings := cfg.Warnings()
	found := false
	for _, w := range warnings {
		if contains(w, "min_days") && contains(w, "0") {
			found = true
		}
	}
	assert.True(t, found, "should warn when min_days is 0")
}

func TestWarnings_MemoryBackendUnsuitable(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Storage.Backend = "memory"
	cfg.Policy = &config.PolicyConfig{}
	warnings := cfg.Warnings()
	found := false
	for _, w := range warnings {
		if contains(w, "memory") && contains(w, "production") {
			found = true
		}
	}
	assert.True(t, found, "should warn that memory backend is unsuitable for production")
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
