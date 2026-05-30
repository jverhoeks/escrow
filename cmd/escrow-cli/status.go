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
	"runtime"
	"sort"
	"strings"
	"time"
)

type statusResult struct {
	PfAnchorActive      bool          `json:"pfAnchorActive"`
	PfAnchorUnknown     bool          `json:"pfAnchorUnknown,omitempty"`
	ActiveEcosystems    []string      `json:"activeEcosystems"`
	ConfigFilesWritten  []string      `json:"configFilesWritten"`
	ProxyServiceRunning bool          `json:"proxyServiceRunning"`
	ProxyReachable      bool          `json:"proxyReachable"`
	ProxyUser           string        `json:"proxyUser"`
	ProxyPort           int           `json:"proxyPort"`
	MinAgeSettings      []minAgeStat  `json:"minAgeSettings"`
}

// minAgeStat reports a min-release-age setting from a particular tool/file.
type minAgeStat struct {
	Tool   string `json:"tool"`
	Value  string `json:"value"`  // human-readable, e.g. "7 days"
	Source string `json:"source"` // file path
	Set    bool   `json:"set"`
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

	// 1+2. Firewall rules active and which ecosystems are loaded.
	switch runtime.GOOS {
	case "darwin":
		// rdr rules live in the nat section (not filter rules).
		pfOut, pfErr := exec.Command("sudo", "-n", "pfctl", "-a", "escrow", "-s", "nat").Output()
		switch {
		case pfErr == nil:
			pfRules := strings.TrimSpace(string(pfOut))
			result.PfAnchorActive = strings.Contains(pfRules, "rdr pass")
			if result.PfAnchorActive {
				// pfctl -s nat shows resolved IPs, not hostnames.
				// Read the anchor file instead — we wrote it with hostnames.
				anchorData, _ := os.ReadFile(pfAnchorFile)
				anchor := string(anchorData)
				for _, eco := range allEcosystems {
					for _, host := range registryHosts[eco] {
						if strings.Contains(anchor, host) {
							result.ActiveEcosystems = append(result.ActiveEcosystems, eco)
							break
						}
					}
				}
			}
		case isPermissionDenied(pfErr):
			result.PfAnchorUnknown = true
		}

	case "linux":
		switch detectLinuxFw() {
		case "iptables":
			out, err := exec.Command("iptables", "-t", "nat", "-L", "ESCROW", "-n").Output()
			if err == nil {
				rules := string(out)
				result.PfAnchorActive = strings.Contains(rules, "REDIRECT")
				if result.PfAnchorActive {
					for _, eco := range allEcosystems {
						hosts := registryHosts[eco]
						if len(hosts) > 0 && strings.Contains(rules, hosts[0]) {
							result.ActiveEcosystems = append(result.ActiveEcosystems, eco)
						}
					}
				}
			}
		case "nftables":
			out, err := exec.Command("nft", "list", "table", "ip", "escrow").Output()
			if err == nil {
				rules := string(out)
				result.PfAnchorActive = strings.Contains(rules, "redirect")
				if result.PfAnchorActive {
					for _, eco := range allEcosystems {
						hosts := registryHosts[eco]
						if len(hosts) > 0 && strings.Contains(rules, hosts[0]) {
							result.ActiveEcosystems = append(result.ActiveEcosystems, eco)
						}
					}
				}
			}
		default:
			result.PfAnchorUnknown = true
		}
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

	// 4. Min-age settings from various tools.
	result.MinAgeSettings = collectMinAgeSettings(home)

	// 5. Proxy port open.
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

	fmt.Println()
	fmt.Println("min-age:")
	for _, s := range result.MinAgeSettings {
		if s.Set {
			fmt.Printf("  %-12s ✓  %-12s  %s\n", s.Tool, s.Value, s.Source)
		} else {
			fmt.Printf("  %-12s –  not set      %s\n", s.Tool, s.Source)
		}
	}

	if len(result.ConfigFilesWritten) > 0 {
		fmt.Println()
		fmt.Println("config files (escrow proxy):")
		sort.Strings(result.ConfigFilesWritten)
		for _, f := range result.ConfigFilesWritten {
			fmt.Println("  ", f)
		}
	} else {
		fmt.Println()
		fmt.Println("config files: none (run: escrow-cli config write)")
	}
}

// collectMinAgeSettings reads min-release-age configuration from tool configs
// and renovate.json, giving operators a single-pane view of age gating.
func collectMinAgeSettings(home string) []minAgeStat {
	cwd, _ := os.Getwd()
	var stats []minAgeStat

	// renovate.json — check CWD first, then repo root heuristic
	for _, rj := range []string{
		filepath.Join(cwd, "renovate.json"),
		filepath.Join(cwd, ".github", "renovate.json"),
	} {
		if val, ok := readRenovateMinAge(rj); ok || fileExists(rj) {
			stats = append(stats, minAgeStat{"renovate", val, rj, ok})
			break
		}
	}

	// npm: min-release-age (npm >= 11) in ~/.npmrc
	npmrc := filepath.Join(home, ".npmrc")
	if v := readNpmrcKey(npmrc, "min-release-age"); v != "" {
		stats = append(stats, minAgeStat{"npm", v, npmrc, true})
	} else {
		stats = append(stats, minAgeStat{"npm", "", npmrc + " (min-release-age)", false})
	}

	// pnpm: minimumReleaseAge (minutes) in ~/.npmrc
	if v := readNpmrcKey(npmrc, "minimumReleaseAge"); v != "" {
		stats = append(stats, minAgeStat{"pnpm", v + "m", npmrc, true})
	} else {
		stats = append(stats, minAgeStat{"pnpm", "", npmrc + " (minimumReleaseAge)", false})
	}

	// uv: exclude-newer in ~/.config/uv/uv.toml
	uvConf := filepath.Join(home, ".config", "uv", "uv.toml")
	if v := readTomlKey(uvConf, "exclude-newer"); v != "" {
		stats = append(stats, minAgeStat{"uv", v, uvConf, true})
	} else {
		stats = append(stats, minAgeStat{"uv", "", uvConf + " (exclude-newer)", false})
	}

	return stats
}

// readRenovateMinAge reads the first minimumReleaseAge from packageRules in a renovate.json.
func readRenovateMinAge(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	s := string(data)
	const key = `"minimumReleaseAge"`
	idx := strings.Index(s, key)
	if idx < 0 {
		return "", false
	}
	// Extract the value after the key and ": "
	rest := s[idx+len(key):]
	colon := strings.Index(rest, ":")
	if colon < 0 {
		return "", false
	}
	rest = strings.TrimSpace(rest[colon+1:])
	if len(rest) == 0 {
		return "", false
	}
	if rest[0] == '"' {
		end := strings.Index(rest[1:], `"`)
		if end >= 0 {
			return rest[1 : end+1], true
		}
	}
	return "", false
}

// readNpmrcKey reads a key=value from an .npmrc file.
func readNpmrcKey(path, key string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, key+"=") {
			return strings.TrimPrefix(line, key+"=")
		}
	}
	return ""
}

// readTomlKey reads a simple top-level `key = "value"` from a TOML file.
// Skips comment lines and inline TOML comments. Requires the key to be
// followed by `=` or whitespace to avoid prefix collisions.
func readTomlKey(path, key string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Match `key` followed by `=` or whitespace, not a longer key like `keyX`.
		if !strings.HasPrefix(line, key) {
			continue
		}
		rest := line[len(key):]
		if rest == "" || (rest[0] != '=' && rest[0] != ' ' && rest[0] != '\t') {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		v := strings.TrimSpace(parts[1])
		// Strip inline TOML comment after the value.
		if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		// Strip surrounding quotes if present.
		if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
			v = v[1 : len(v)-1]
		}
		if v != "" {
			return v
		}
	}
	return ""
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

// escrowProxy matches any localhost proxy URL regardless of whether the
// host is written as 127.0.0.1 or localhost.
func escrowProxy(s string) bool {
	return strings.Contains(s, "127.0.0.1:7888") || strings.Contains(s, "localhost:7888")
}

func isEscrowConfig(path, hint string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	s := string(data)
	switch hint {
	case "npm":
		return strings.Contains(s, "registry=http://127.0.0.1:7888") ||
			strings.Contains(s, "registry=http://localhost:7888")
	case "yarn1":
		return strings.Contains(s, `registry "http://127.0.0.1:7888`) ||
			strings.Contains(s, `registry "http://localhost:7888`)
	case "yarnberry":
		return strings.Contains(s, "npmRegistryServer:") && escrowProxy(s)
	case "bun":
		return strings.Contains(s, "[install]") && escrowProxy(s)
	case "pypi", "uv":
		return escrowProxy(s)
	case "python-env":
		return strings.Contains(s, "BEGIN escrow-python")
	case "go":
		return strings.Contains(s, "BEGIN escrow-go")
	case "cargo":
		return strings.Contains(s, `replace-with = "escrow"`)
	case "nuget":
		return strings.Contains(s, `key="escrow"`)
	case "maven":
		return strings.Contains(s, "<id>escrow</id>")
	case "gradle":
		return strings.Contains(s, "escrow-mirror") || strings.Contains(s, "escrow-cli")
	case "composer":
		return strings.Contains(s, `"type": "composer"`) && escrowProxy(s)
	}
	return false
}
