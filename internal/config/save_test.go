package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jverhoeks/escrow/internal/config"
	"github.com/stretchr/testify/require"
)

func TestValidate(t *testing.T) {
	// An enabled dashboard now requires credentials for a valid config.
	c := config.DefaultConfig()
	c.Server.Port = 7888
	c.Dashboard.Password = "pass"
	c.Dashboard.Secret = "aabbccddeeff00112233445566778899"
	require.Empty(t, c.Validate())

	bad := config.DefaultConfig()
	bad.Server.Port = 70000
	require.NotEmpty(t, bad.Validate())

	badAction := config.DefaultConfig()
	badAction.Server.Port = 7888
	badAction.Policy = &config.PolicyConfig{OSV: &config.OSVPolicyConfig{Action: "nuke"}}
	require.NotEmpty(t, badAction.Validate())

	badSev := config.DefaultConfig()
	badSev.Server.Port = 7888
	badSev.Policy = &config.PolicyConfig{OSV: &config.OSVPolicyConfig{Action: "block", MinSeverity: "SEVERE"}}
	require.NotEmpty(t, badSev.Validate())
}

func TestSave_BackupAndRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "escrow.toml")
	require.NoError(t, os.WriteFile(path, []byte("[server]\n  port = 1\n"), 0o600))

	c := config.DefaultConfig()
	c.Server.Port = 9999
	require.NoError(t, config.Save(path, c))

	bak, err := os.ReadFile(path + ".bak")
	require.NoError(t, err)
	require.Contains(t, string(bak), "port = 1")

	reloaded, err := config.Load(path)
	require.NoError(t, err)
	require.Equal(t, 9999, reloaded.Server.Port)
}
