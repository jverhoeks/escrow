package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jverhoeks/escrow/internal/config"
)

// #101: loading an arbitrary config file must never panic (only error).
func FuzzLoad(f *testing.F) {
	f.Add([]byte("[server]\n  port = 7888\n  log_level = \"info\"\n"))
	f.Add([]byte("not valid toml ["))
	f.Add([]byte(""))
	f.Fuzz(func(t *testing.T, data []byte) {
		p := filepath.Join(t.TempDir(), "escrow.toml")
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Skip()
		}
		_, _ = config.Load(p) // must not panic
	})
}
