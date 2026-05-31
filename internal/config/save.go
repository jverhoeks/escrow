package config

import (
	"bytes"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// validActions / validSeverities are the allowed enum values for policy actions
// and OSV severity thresholds, used by Config.Validate (in config.go).
var validActions = map[string]bool{"": true, "allow": true, "warn": true, "block": true}
var validSeverities = map[string]bool{"": true, "CRITICAL": true, "HIGH": true, "MEDIUM": true, "LOW": true}

// Save backs up path to path+".bak" (best-effort) and writes the TOML encoding
// of cfg with a generated header. Re-encoding does not preserve comments; the
// backup is the recovery path.
func Save(path string, cfg Config) error {
	if existing, err := os.ReadFile(path); err == nil {
		_ = os.WriteFile(path+".bak", existing, 0o600)
	}
	var buf bytes.Buffer
	buf.WriteString("# Written by the escrow dashboard. Comments are not preserved on save;\n")
	buf.WriteString("# the previous file is kept at this path + \".bak\".\n\n")
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	return os.WriteFile(path, buf.Bytes(), 0o600)
}
