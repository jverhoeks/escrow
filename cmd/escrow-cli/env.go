package main

// Launch-environment injection: injects proxy env vars into the macOS launch
// environment via a LaunchDaemon (RunAtLoad), so all processes — including
// GUI apps and bundled runtimes — inherit them from boot.
//
// On Linux, writes to /etc/profile.d/escrow.sh which is sourced by bash/sh
// on login (covers terminal sessions and some GUI apps depending on DE).
//
// These env vars complement config-file-based setup (config write) by covering
// tools that read from environment rather than config files.

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

const (
	launchDaemonEnvPlist = "/Library/LaunchDaemons/com.escrow.environment.plist"
	linuxProfileScript   = "/etc/profile.d/escrow.sh"
)

// ecoEnvVar maps an ecosystem to the env vars it contributes.
type ecoEnvVar struct {
	key   string
	value string
	tool  string // human label
}

func buildEnvVars(ecosystems []string, base string) []ecoEnvVar {
	var out []ecoEnvVar
	for _, eco := range ecosystems {
		switch eco {
		case "npm":
			url := base + "/"
			out = append(out,
				ecoEnvVar{"NPM_CONFIG_REGISTRY", url, "npm/pnpm"},
				ecoEnvVar{"YARN_REGISTRY", url, "yarn v1"},
			)
		case "pypi":
			idx := base + "/pypi/simple/"
			out = append(out,
				ecoEnvVar{"PIP_INDEX_URL", idx, "pip/poetry"},
				ecoEnvVar{"UV_INDEX_URL", idx, "uv"},
			)
		case "go":
			out = append(out,
				ecoEnvVar{"GOPROXY", base + "/go,off", "go"},
				ecoEnvVar{"GONOSUMDB", "*", "go (sum db)"},
			)
		// cargo, maven, nuget, composer: no standard env var for registry URL
		}
	}
	return out
}

// ── config write-env ──────────────────────────────────────────────────────────

func runConfigWriteEnv(args []string) {
	fs := flag.NewFlagSet("config write-env", flag.ExitOnError)
	ecosystems := fs.String("ecosystems", "npm,pypi,go", "comma-separated ecosystems (cargo/maven/nuget: use config write)")
	proxyURL := fs.String("proxy-url", "http://127.0.0.1:7888", "base URL of the escrow proxy")
	fs.Parse(args) //nolint:errcheck

	if err := validateProxyURL(*proxyURL); err != nil {
		die("--proxy-url: %v", err)
	}

	ecos := parseEcosystems(*ecosystems)
	base := strings.TrimRight(*proxyURL, "/")
	vars := buildEnvVars(ecos, base)
	if len(vars) == 0 {
		die("no env-var-capable ecosystems specified (try: npm,pypi,go)")
	}

	switch runtime.GOOS {
	case "darwin":
		requireRoot("config write-env")
		if err := writeLaunchDaemonEnv(vars); err != nil {
			die("writing LaunchDaemon: %v", err)
		}
		fmt.Printf("✓ %s written and loaded\n", launchDaemonEnvPlist)
		fmt.Println("  Env vars are now active for all processes (no reboot needed).")
	case "linux":
		requireRoot("config write-env")
		if err := writeLinuxProfileEnv(vars); err != nil {
			die("writing profile script: %v", err)
		}
		fmt.Printf("✓ %s written\n", linuxProfileScript)
		fmt.Println("  Env vars take effect on next login or: source /etc/profile.d/escrow.sh")
	default:
		die("config write-env not supported on %s", runtime.GOOS)
	}

	fmt.Println()
	for _, v := range vars {
		fmt.Printf("  %-26s = %s\n", v.key, v.value)
	}
}

func writeLaunchDaemonEnv(vars []ecoEnvVar) error {
	// Build the shell command that calls launchctl setenv for each var.
	var cmds []string
	for _, v := range vars {
		cmds = append(cmds, fmt.Sprintf("launchctl setenv %s %s", v.key, v.value))
	}
	script := strings.Join(cmds, " && ")

	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.escrow.environment</string>
  <key>RunAtLoad</key>
  <true/>
  <key>ProgramArguments</key>
  <array>
    <string>/bin/sh</string>
    <string>-c</string>
    <string>` + xmlEscape(script) + `</string>
  </array>
</dict>
</plist>
`
	if err := os.MkdirAll("/Library/LaunchDaemons", 0755); err != nil {
		return err
	}
	if err := writeAtomic(launchDaemonEnvPlist, []byte(plist), 0644); err != nil {
		return err
	}

	// Unload first if already loaded (ignore error — may not be loaded).
	exec.Command("launchctl", "unload", launchDaemonEnvPlist).Run() //nolint:errcheck

	// Load it so env vars are active immediately without a reboot.
	if out, err := exec.Command("launchctl", "load", launchDaemonEnvPlist).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl load: %v\n%s", err, strings.TrimSpace(string(out)))
	}

	// Apply immediately by running the script directly.
	if out, err := exec.Command("/bin/sh", "-c", script).CombinedOutput(); err != nil {
		return fmt.Errorf("setting env vars: %v\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func writeLinuxProfileEnv(vars []ecoEnvVar) error {
	var sb strings.Builder
	sb.WriteString("# Escrow supply-chain proxy — managed by escrow-cli\n")
	for _, v := range vars {
		fmt.Fprintf(&sb, "export %s=%s\n", v.key, v.value)
	}
	return writeAtomic(linuxProfileScript, []byte(sb.String()), 0644)
}

// ── config check-env ──────────────────────────────────────────────────────────

func runConfigCheckEnv(args []string) {
	fs := flag.NewFlagSet("config check-env", flag.ExitOnError)
	fs.Parse(args) //nolint:errcheck

	switch runtime.GOOS {
	case "darwin":
		installed := fileExists(launchDaemonEnvPlist)
		if installed {
			fmt.Printf("LaunchDaemon   ✓  %s\n", launchDaemonEnvPlist)
		} else {
			fmt.Printf("LaunchDaemon   –  not installed (run: sudo escrow-cli config write-env)\n")
		}
		fmt.Println()
	case "linux":
		installed := fileExists(linuxProfileScript)
		if installed {
			fmt.Printf("profile.d      ✓  %s\n", linuxProfileScript)
		} else {
			fmt.Printf("profile.d      –  not installed (run: sudo escrow-cli config write-env)\n")
		}
		fmt.Println()
	}

	// Check env vars that are active in the current process.
	// If the LaunchDaemon ran before this process started they will be set.
	allVars := buildEnvVars(allEcosystems, "http://127.0.0.1:7888")
	for _, v := range allVars {
		current := os.Getenv(v.key)
		if current != "" {
			fmt.Printf("%-28s ✓  %s\n", v.key, current)
		} else {
			// Try launchctl getenv on macOS for a more accurate reading.
			if runtime.GOOS == "darwin" {
				out, err := exec.Command("launchctl", "getenv", v.key).Output()
				if err == nil && strings.TrimSpace(string(out)) != "" {
					fmt.Printf("%-28s ✓  %s  (launch env, not yet in this shell)\n", v.key, strings.TrimSpace(string(out)))
					continue
				}
			}
			fmt.Printf("%-28s –  not set\n", v.key)
		}
	}
}

// ── config restore-env ────────────────────────────────────────────────────────

func runConfigRestoreEnv(args []string) {
	fs := flag.NewFlagSet("config restore-env", flag.ExitOnError)
	fs.Parse(args) //nolint:errcheck

	requireRoot("config restore-env")

	switch runtime.GOOS {
	case "darwin":
		if !fileExists(launchDaemonEnvPlist) {
			fmt.Println("nothing to remove (LaunchDaemon not installed)")
			return
		}
		exec.Command("launchctl", "unload", launchDaemonEnvPlist).Run() //nolint:errcheck
		if err := os.Remove(launchDaemonEnvPlist); err != nil {
			die("removing %s: %v", launchDaemonEnvPlist, err)
		}
		// Unset the vars in the running launch environment.
		for _, v := range buildEnvVars(allEcosystems, "") {
			exec.Command("launchctl", "unsetenv", v.key).Run() //nolint:errcheck
		}
		fmt.Printf("✓ removed %s and unset env vars\n", launchDaemonEnvPlist)
	case "linux":
		if !fileExists(linuxProfileScript) {
			fmt.Println("nothing to remove (profile script not installed)")
			return
		}
		if err := os.Remove(linuxProfileScript); err != nil {
			die("removing %s: %v", linuxProfileScript, err)
		}
		fmt.Printf("✓ removed %s\n", linuxProfileScript)
		fmt.Println("  Env vars will be gone on next login.")
	default:
		die("config restore-env not supported on %s", runtime.GOOS)
	}
}
