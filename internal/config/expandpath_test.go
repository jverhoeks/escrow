package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jverhoeks/escrow/internal/config"
)

// #88: ExpandPath expands environment variables and ~, so documented paths
// like "$TMPDIR/escrow" resolve.
func TestExpandPath_EnvAndTilde(t *testing.T) {
	t.Setenv("ESCROW_TEST_DIR", "/var/data")
	if got := config.ExpandPath("$ESCROW_TEST_DIR/cache"); got != "/var/data/cache" {
		t.Errorf("env expansion: got %q", got)
	}
	home, _ := os.UserHomeDir()
	if got := config.ExpandPath("~/x"); got != filepath.Join(home, "x") {
		t.Errorf("tilde expansion: got %q", got)
	}
	if got := config.ExpandPath("/abs/path"); got != "/abs/path" {
		t.Errorf("plain path must be unchanged: got %q", got)
	}
}
