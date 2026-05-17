package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/config"
)

func TestLoadDefaults_NoFile(t *testing.T) {
	cfg, err := config.Load("/nonexistent/path/sentinel.toml")
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", cfg.Server.Host)
	assert.Equal(t, 8888, cfg.Server.Port)
	assert.Equal(t, "disk", cfg.Storage.Backend)
	assert.Nil(t, cfg.Policy)
}

func TestLoad_ParsesFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "sentinel.toml")
	os.WriteFile(f, []byte(`
[server]
  port = 9999
[policy]
  [policy.age]
    min_days = 3
    action   = "block"
`), 0o644)

	cfg, err := config.Load(f)
	require.NoError(t, err)
	assert.Equal(t, 9999, cfg.Server.Port)
	require.NotNil(t, cfg.Policy)
	require.NotNil(t, cfg.Policy.Age)
	assert.Equal(t, 3, cfg.Policy.Age.MinDays)
	assert.Equal(t, "block", cfg.Policy.Age.Action)
}

func TestWarnings_NoPolicy(t *testing.T) {
	cfg := config.Config{}
	warnings := cfg.Warnings()
	// Expects: "no policy configured" + "no ecosystems are enabled"
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "no policy configured") {
			found = true
		}
	}
	assert.True(t, found, "should warn about missing policy")
}

func TestWarnings_MemoryBackend(t *testing.T) {
	cfg := config.Config{
		Storage:    config.StorageConfig{Backend: "memory"},
		Ecosystems: config.EcosystemConfig{NPM: true}, // enable one ecosystem to silence that warning
	}
	cfg.Policy = &config.PolicyConfig{}
	warnings := cfg.Warnings()
	foundMemory, foundSignals := false, false
	for _, w := range warnings {
		if strings.Contains(w, "memory") {
			foundMemory = true
		}
		if strings.Contains(w, "no signals") {
			foundSignals = true
		}
	}
	assert.True(t, foundMemory, "should warn about memory backend")
	assert.True(t, foundSignals, "should warn about empty policy with no signals")
}

func TestGenerateIfMissing_CreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sentinel.toml")
	generated, msg, err := config.GenerateIfMissing(path)
	require.NoError(t, err)
	assert.True(t, generated)
	assert.Contains(t, msg, "username: admin")
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.True(t, cfg.Dashboard.Enabled)
	assert.Equal(t, "admin", cfg.Dashboard.Username)
	assert.Len(t, cfg.Dashboard.Secret, 64)
}

func TestGenerateIfMissing_SkipsExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sentinel.toml")
	os.WriteFile(path, []byte("[server]\n  port = 9999\n"), 0o644)
	generated, _, err := config.GenerateIfMissing(path)
	require.NoError(t, err)
	assert.False(t, generated)
	cfg, _ := config.Load(path)
	assert.Equal(t, 9999, cfg.Server.Port)
}
