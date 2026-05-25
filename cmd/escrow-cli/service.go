package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

const (
	launchDaemonPlist = "/Library/LaunchDaemons/com.escrow.proxy.plist"
	linuxServiceName  = "escrow"
)

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
	switch runtime.GOOS {
	case "darwin":
		if _, err := os.Stat(launchDaemonPlist); err != nil {
			die("%s not found — register the service first with: sudo brew services start escrow", launchDaemonPlist)
		}
		out, err := exec.Command("launchctl", "bootstrap", "system", launchDaemonPlist).CombinedOutput()
		if err != nil {
			die("launchctl bootstrap: %v\n%s", err, strings.TrimSpace(string(out)))
		}
	case "linux":
		out, err := exec.Command("systemctl", "start", linuxServiceName).CombinedOutput()
		if err != nil {
			die("systemctl start: %v\n%s", err, strings.TrimSpace(string(out)))
		}
	default:
		die("service management not supported on %s", runtime.GOOS)
	}
	fmt.Println("service started")
}

func serviceUnload() {
	switch runtime.GOOS {
	case "darwin":
		if _, err := os.Stat(launchDaemonPlist); err != nil {
			die("%s not found", launchDaemonPlist)
		}
		out, err := exec.Command("launchctl", "bootout", "system", launchDaemonPlist).CombinedOutput()
		if err != nil {
			die("launchctl bootout: %v\n%s", err, strings.TrimSpace(string(out)))
		}
	case "linux":
		out, err := exec.Command("systemctl", "stop", linuxServiceName).CombinedOutput()
		if err != nil {
			die("systemctl stop: %v\n%s", err, strings.TrimSpace(string(out)))
		}
	default:
		die("service management not supported on %s", runtime.GOOS)
	}
	fmt.Println("service stopped")
}

func serviceStatus() {
	switch runtime.GOOS {
	case "darwin":
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
	case "linux":
		out, _ := exec.Command("systemctl", "status", linuxServiceName, "--no-pager").CombinedOutput()
		fmt.Print(string(out))
	default:
		die("service management not supported on %s", runtime.GOOS)
	}
}
