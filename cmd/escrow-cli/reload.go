package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/jverhoeks/escrow/internal/config"
)

// processLooksLikeEscrow reports whether pid's process command appears to be
// escrow. It returns true when it cannot determine (best-effort — don't block a
// legitimate reload just because the check is unavailable); it returns false
// only when it can positively read a non-escrow command, which is the
// stale-runtime.json → reused-PID case we want to refuse. (#52)
func processLooksLikeEscrow(pid int) bool {
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid)); err == nil { // Linux
		return strings.Contains(strings.ToLower(strings.TrimSpace(string(data))), "escrow")
	}
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output() // macOS/BSD
	if err != nil {
		return true // can't determine → leave it to syscall.Kill (ESRCH if gone)
	}
	return strings.Contains(strings.ToLower(string(out)), "escrow")
}

// runReload signals the running escrow proxy to re-read its config (SIGHUP).
func runReload(args []string) {
	fs := flag.NewFlagSet("reload", flag.ExitOnError)
	pidPath := fs.String("pid", "", "path to escrow.pid (default: search common locations)")
	fs.Parse(args) //nolint:errcheck

	pid := 0
	// Prefer the running proxy's published PID (CWD-independent).
	if *pidPath == "" {
		if rt, err := config.ReadRuntime(); err == nil && rt.PID > 0 {
			pid = rt.PID
		}
	}
	if pid == 0 {
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
			fmt.Fprintln(os.Stderr, "escrow-cli reload: could not locate the running proxy; is escrow running? or pass --pid /path/to/escrow.pid")
			os.Exit(1)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "escrow-cli reload: read %s: %v\n", path, err)
			os.Exit(1)
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			fmt.Fprintf(os.Stderr, "escrow-cli reload: invalid pid in %s\n", path)
			os.Exit(1)
		}
	}
	if !processLooksLikeEscrow(pid) {
		fmt.Fprintf(os.Stderr, "escrow-cli reload: pid %d is not an escrow process (stale runtime/pid file?); refusing to signal it\n", pid)
		os.Exit(1)
	}
	if err := syscall.Kill(pid, syscall.SIGHUP); err != nil {
		fmt.Fprintf(os.Stderr, "escrow-cli reload: signal pid %d: %v\n", pid, err)
		os.Exit(1)
	}
	fmt.Printf("sent SIGHUP to escrow (pid %d) — config reloaded; check the proxy log for restart-required fields\n", pid)
}
