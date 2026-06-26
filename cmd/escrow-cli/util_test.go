package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeAtomic must preserve an existing file's mode so a rewrite of a 0600
// config holding an auth token isn't downgraded to a world-readable 0644. A
// brand-new file uses the passed default mode. See #52.
func TestWriteAtomic_PreservesExistingMode(t *testing.T) {
	dir := t.TempDir()

	// Existing 0600 file: rewrite must keep 0600 even though we pass 0644.
	existing := filepath.Join(dir, ".npmrc")
	if err := os.WriteFile(existing, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(existing, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(existing)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("rewrite downgraded mode to %o, want 0600 preserved", got)
	}

	// New file: uses the passed default mode.
	fresh := filepath.Join(dir, "new.conf")
	if err := writeAtomic(fresh, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err = os.Stat(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o644 {
		t.Errorf("new file mode = %o, want 0644", got)
	}
}

func TestValidServiceName(t *testing.T) {
	for _, ok := range []string{"web", "api-1", "my_svc", "a.b"} {
		if !validServiceName.MatchString(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"web:\n  evil", "a b", "x$(whoami)", ""} {
		if validServiceName.MatchString(bad) {
			t.Errorf("%q should be rejected", bad)
		}
	}
}
