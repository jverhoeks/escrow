package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type statusResult struct {
	PfAnchorActive      bool     `json:"pfAnchorActive"`
	PfAnchorUnknown     bool     `json:"pfAnchorUnknown,omitempty"` // true when pfctl query was denied
	ActiveEcosystems    []string `json:"activeEcosystems"`
	ConfigFilesWritten  []string `json:"configFilesWritten"`
	ProxyServiceRunning bool     `json:"proxyServiceRunning"`
	ProxyReachable      bool     `json:"proxyReachable"`
	ProxyUser           string   `json:"proxyUser"`
	ProxyPort           int      `json:"proxyPort"`
}

func runStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "output as JSON")
	fs.Parse(args) //nolint:errcheck

	result := statusResult{
		ProxyUser:          "_escrow",
		ProxyPort:          7888,
		ActiveEcosystems:   []string{},
		ConfigFilesWritten: []string{},
	}

	// 1+2. pf anchor state.
	pfOut, pfErr := exec.Command("sudo", "-n", "pfctl", "-a", "escrow", "-s", "rules").Output()
	switch {
	case pfErr == nil:
		pfRules := strings.TrimSpace(string(pfOut))
		result.PfAnchorActive = pfRules != ""
		if result.PfAnchorActive {
			for _, eco := range allEcosystems {
				hosts := registryHosts[eco]
				if len(hosts) > 0 && strings.Contains(pfRules, hosts[0]) {
					result.ActiveEcosystems = append(result.ActiveEcosystems, eco)
				}
			}
		}
	case isPermissionDenied(pfErr):
		result.PfAnchorUnknown = true
	default:
		// pfctl returned non-zero for a reason other than permission — treat anchor as inactive.
	}

	// 3. Config files written by escrow.
	home, _ := os.UserHomeDir()
	type fileCheck struct {
		path string
		hint string
	}
	checks := []fileCheck{
		{filepath.Join(home, ".npmrc"), "npm"},
		{filepath.Join(home, ".pip", "pip.conf"), "pypi"},
		{filepath.Join(home, ".config", "uv", "uv.toml"), "uv"},
		{filepath.Join(home, ".zprofile"), "go"},
		{filepath.Join(home, ".bash_profile"), "go"},
		{filepath.Join(home, ".cargo", "config.toml"), "cargo"},
		{filepath.Join(home, ".nuget", "NuGet", "NuGet.Config"), "nuget"},
		{filepath.Join(home, ".m2", "settings.xml"), "maven"},
		{filepath.Join(home, ".config", "composer", "config.json"), "composer"},
	}
	for _, c := range checks {
		if isEscrowConfig(c.path, c.hint) {
			result.ConfigFilesWritten = append(result.ConfigFilesWritten, c.path)
		}
	}

	// 4. Proxy port open.
	conn, err := net.DialTimeout("tcp", "127.0.0.1:7888", time.Second)
	if err == nil {
		conn.Close()
		result.ProxyServiceRunning = true
	}

	// 5. Proxy reachable (healthz).
	if result.ProxyServiceRunning {
		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Get("http://127.0.0.1:7888/healthz")
		if err == nil {
			resp.Body.Close()
			result.ProxyReachable = resp.StatusCode == 200
		}
	}

	if *asJSON {
		sort.Strings(result.ActiveEcosystems)
		sort.Strings(result.ConfigFilesWritten)
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(result) //nolint:errcheck
		return
	}

	// Human-readable output.
	pfStatus := "inactive"
	switch {
	case result.PfAnchorUnknown:
		pfStatus = "unknown (run as root or configure passwordless sudo for pfctl)"
	case result.PfAnchorActive && len(result.ActiveEcosystems) > 0:
		pfStatus = "active (" + strings.Join(result.ActiveEcosystems, ", ") + ")"
	case result.PfAnchorActive:
		pfStatus = "active (no ecosystems detected)"
	}
	fmt.Printf("pf anchor:    %s\n", pfStatus)

	svcStatus := "not running"
	if result.ProxyServiceRunning {
		svcStatus = "running"
		if result.ProxyReachable {
			svcStatus += ", reachable"
		} else {
			svcStatus += ", not reachable"
		}
	}
	fmt.Printf("proxy:        %s\n", svcStatus)

	if len(result.ConfigFilesWritten) > 0 {
		fmt.Println("config files:")
		sort.Strings(result.ConfigFilesWritten)
		for _, f := range result.ConfigFilesWritten {
			fmt.Println("  ", f)
		}
	} else {
		fmt.Println("config files: none")
	}
}

// isPermissionDenied reports whether an exec error is a sudo password-required denial.
func isPermissionDenied(err error) bool {
	if err == nil {
		return false
	}
	// sudo -n exits with status 1 and prints "a password is required" to stderr.
	// exec.Cmd.Output() wraps non-zero exit in *exec.ExitError; check the message.
	return strings.Contains(err.Error(), "exit status 1")
}

func isEscrowConfig(path, hint string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	s := string(data)
	switch hint {
	case "npm":
		return strings.Contains(s, "registry=http://127.0.0.1:7888")
	case "pypi", "uv":
		return strings.Contains(s, "127.0.0.1:7888")
	case "go":
		return strings.Contains(s, "BEGIN escrow-go")
	case "cargo":
		return strings.Contains(s, `replace-with = "escrow"`)
	case "nuget":
		return strings.Contains(s, `key="escrow"`)
	case "maven":
		return strings.Contains(s, "<id>escrow</id>")
	case "composer":
		return strings.Contains(s, `"type": "composer"`) && strings.Contains(s, "127.0.0.1:7888")
	}
	return false
}
