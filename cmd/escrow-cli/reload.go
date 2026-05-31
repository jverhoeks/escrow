package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// runReload signals the running escrow proxy to re-read its config (SIGHUP).
func runReload(args []string) {
	fs := flag.NewFlagSet("reload", flag.ExitOnError)
	pidPath := fs.String("pid", "", "path to escrow.pid (default: search common locations)")
	fs.Parse(args) //nolint:errcheck

	path := *pidPath
	if path == "" {
		for _, c := range []string{"./escrow-cache/escrow.pid", "escrow.pid"} {
			if _, err := os.Stat(c); err == nil {
				path = c
				break
			}
		}
	}
	if path == "" {
		fmt.Fprintln(os.Stderr, "escrow-cli reload: could not find escrow.pid; pass --pid /path/to/escrow.pid")
		os.Exit(1)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "escrow-cli reload: read %s: %v\n", path, err)
		os.Exit(1)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "escrow-cli reload: invalid pid in %s\n", path)
		os.Exit(1)
	}
	if err := syscall.Kill(pid, syscall.SIGHUP); err != nil {
		fmt.Fprintf(os.Stderr, "escrow-cli reload: signal pid %d: %v\n", pid, err)
		os.Exit(1)
	}
	fmt.Printf("sent SIGHUP to escrow (pid %d) at %s — config reloaded; check the proxy log for restart-required fields\n", pid, filepath.Clean(path))
}
