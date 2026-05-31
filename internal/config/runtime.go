package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Runtime is a small discovery record the running proxy writes to a fixed,
// CWD-independent location so the CLI can find the live process and its event
// log regardless of where escrow's working directory is (e.g. a brew service).
type Runtime struct {
	EventLogPath string `json:"eventlog_path"` // absolute; empty if events aren't persisted
	PID          int    `json:"pid"`
	Port         int    `json:"port"`
}

// RuntimePath returns the per-user discovery file path
// (<user cache dir>/escrow/runtime.json).
func RuntimePath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "escrow", "runtime.json"), nil
}

// WriteRuntime persists the discovery record (best-effort; creates the dir).
func WriteRuntime(r Runtime) error {
	p, err := RuntimePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

// ReadRuntime loads the discovery record written by a running proxy.
func ReadRuntime() (Runtime, error) {
	var r Runtime
	p, err := RuntimePath()
	if err != nil {
		return r, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return r, err
	}
	return r, json.Unmarshal(data, &r)
}
