package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const launchDaemonPlist = "/Library/LaunchDaemons/com.escrow.proxy.plist"

func runService(args []string) {
	requireRoot("service")

	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: service requires a subcommand: start, stop, restart, status")
		os.Exit(1)
	}

	switch args[0] {
	case "start":
		serviceLoad()
	case "stop":
		serviceUnload()
	case "restart":
		serviceUnload()
		serviceLoad()
	case "status":
		serviceStatus()
	default:
		fmt.Fprintf(os.Stderr, "error: unknown service subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

func serviceLoad() {
	if _, err := os.Stat(launchDaemonPlist); err != nil {
		die("%s not found — register the service first with: sudo brew services start escrow", launchDaemonPlist)
	}
	// launchctl load/unload are deprecated since macOS 10.10; use bootstrap/bootout.
	out, err := exec.Command("launchctl", "bootstrap", "system", launchDaemonPlist).CombinedOutput()
	if err != nil {
		die("launchctl bootstrap: %v\n%s", err, strings.TrimSpace(string(out)))
	}
	fmt.Println("service started")
}

func serviceUnload() {
	if _, err := os.Stat(launchDaemonPlist); err != nil {
		die("%s not found", launchDaemonPlist)
	}
	out, err := exec.Command("launchctl", "bootout", "system", launchDaemonPlist).CombinedOutput()
	if err != nil {
		die("launchctl bootout: %v\n%s", err, strings.TrimSpace(string(out)))
	}
	fmt.Println("service stopped")
}

func serviceStatus() {
	out, err := exec.Command("launchctl", "list").CombinedOutput()
	if err != nil {
		die("launchctl list: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "com.escrow.proxy") {
			fmt.Println(line)
			return
		}
	}
	fmt.Println("service not loaded")
}
