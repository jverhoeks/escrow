package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func requireRoot(cmd string) {
	if os.Getuid() != 0 {
		fmt.Fprintf(os.Stderr, "error: %s must run as root — try: sudo escrow-cli %s\n", cmd, cmd)
		os.Exit(1)
	}
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", a...)
	os.Exit(1)
}

// writeAtomic writes data to dst via a temp file in the same directory.
// Permissions are set before data is written so the file is never visible
// at an incorrect mode, then renamed atomically into place.
func writeAtomic(dst string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".escrow-tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	// Set permissions BEFORE writing so data is never exposed with wrong mode.
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, dst)
}

// backupFile copies src to src+".escrow-backup" if the backup does not already
// exist. Returns an error if the backup cannot be written; callers should abort
// the intended write when backup fails to avoid an unrecoverable state.
func backupFile(src string) error {
	bak := src + ".escrow-backup"
	if _, err := os.Stat(bak); err == nil {
		return nil // backup already present
	}
	data, err := os.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing to back up
		}
		return err
	}
	info, _ := os.Stat(src)
	mode := os.FileMode(0644)
	if info != nil {
		mode = info.Mode()
	}
	return os.WriteFile(bak, data, mode)
}
