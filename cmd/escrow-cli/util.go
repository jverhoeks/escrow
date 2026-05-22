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

// writeAtomic writes data to dst via a temp file in the same directory,
// then renames atomically. Preserves the same filesystem so rename works.
func writeAtomic(dst string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".escrow-tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
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

// backupFile copies src to src+".escrow-backup" if the backup does not already exist.
// Silently does nothing if src does not exist.
func backupFile(src string) {
	bak := src + ".escrow-backup"
	if _, err := os.Stat(bak); err == nil {
		return // backup already present
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return // file doesn't exist yet, nothing to back up
	}
	info, _ := os.Stat(src)
	mode := os.FileMode(0644)
	if info != nil {
		mode = info.Mode()
	}
	os.WriteFile(bak, data, mode) //nolint:errcheck
}
