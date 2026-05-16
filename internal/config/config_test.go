package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/config"
)

func TestLoadDefaults_NoFile(t *testing.T) {
	cfg, err := config.Load("/nonexistent/path/sentinel.toml")
	require.NoError(t, err)
	assert.Equal(t, "0.0.0.0", cfg.Server.Host)
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
	assert.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "no policy configured")
}

func TestWarnings_MemoryBackend(t *testing.T) {
	cfg := config.Config{
		Storage: config.StorageConfig{Backend: "memory"},
	}
	cfg.Policy = &config.PolicyConfig{}
	warnings := cfg.Warnings()
	assert.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "memory")
}
