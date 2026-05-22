package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func runConfigWrite(args []string) {
	fs := flag.NewFlagSet("config write", flag.ExitOnError)
	ecosystems := fs.String("ecosystems", strings.Join(allEcosystems, ","), "comma-separated ecosystems")
	proxyURL := fs.String("proxy-url", "http://127.0.0.1:7888", "base URL of the escrow proxy")
	fs.Parse(args) //nolint:errcheck

	ecos := parseEcosystems(*ecosystems)
	base := strings.TrimRight(*proxyURL, "/")

	for _, eco := range ecos {
		if err := writeEcoConfig(eco, base); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: %v\n", eco, err)
		} else {
			fmt.Printf("✓ %s config written\n", eco)
		}
	}
}

func runConfigRestore(args []string) {
	fs := flag.NewFlagSet("config restore", flag.ExitOnError)
	fs.Parse(args) //nolint:errcheck

	home, err := os.UserHomeDir()
	if err != nil {
		die("getting home dir: %v", err)
	}

	candidates := []string{
		filepath.Join(home, ".npmrc"),
		filepath.Join(home, ".pip", "pip.conf"),
		filepath.Join(home, ".config", "uv", "uv.toml"),
		filepath.Join(home, ".zprofile"),
		filepath.Join(home, ".bash_profile"),
		filepath.Join(home, ".cargo", "config.toml"),
		filepath.Join(home, ".nuget", "NuGet", "NuGet.Config"),
		filepath.Join(home, ".m2", "settings.xml"),
		filepath.Join(home, ".config", "composer", "config.json"),
	}

	restored := 0
	for _, path := range candidates {
		bak := path + ".escrow-backup"
		if _, err := os.Stat(bak); err != nil {
			continue
		}
		data, err := os.ReadFile(bak)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: reading %s: %v\n", bak, err)
			continue
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "warning: restoring %s: %v\n", path, err)
			continue
		}
		os.Remove(bak)
		fmt.Printf("✓ restored %s\n", path)
		restored++
	}

	// Remove GOPROXY marker blocks from shell profiles (may not have a backup if
	// the profile already existed — we append rather than replace the whole file).
	for _, profile := range []string{
		filepath.Join(home, ".zprofile"),
		filepath.Join(home, ".bash_profile"),
	} {
		if removeGoMarkers(profile) {
			fmt.Printf("✓ removed GOPROXY block from %s\n", profile)
			restored++
		}
	}

	if restored == 0 {
		fmt.Println("nothing to restore")
	}
}

func writeEcoConfig(eco, base string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	switch eco {
	case "npm":
		return writeNpmConfig(home, base)
	case "pypi":
		return writePypiConfig(home, base)
	case "go":
		return writeGoConfig(home, base)
	case "cargo":
		return writeCargoConfig(home, base)
	case "nuget":
		return writeNugetConfig(home, base)
	case "maven":
		return writeMavenConfig(home, base)
	case "composer":
		return writeComposerConfig(home, base)
	}
	return fmt.Errorf("unknown ecosystem: %s", eco)
}

// ── npm ───────────────────────────────────────────────────────────────────────

func writeNpmConfig(home, base string) error {
	url := base + "/"
	if _, err := exec.LookPath("npm"); err == nil {
		out, err := exec.Command("npm", "config", "set", "registry", url).CombinedOutput()
		if err != nil {
			return fmt.Errorf("npm config set: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	// Fallback: edit ~/.npmrc directly.
	npmrc := filepath.Join(home, ".npmrc")
	backupFile(npmrc)
	data, _ := os.ReadFile(npmrc)
	lines := strings.Split(string(data), "\n")
	found := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "registry=") {
			lines[i] = "registry=" + url
			found = true
			break
		}
	}
	if !found {
		lines = append(lines, "registry="+url)
	}
	return os.WriteFile(npmrc, []byte(strings.Join(lines, "\n")), 0644)
}

// ── pypi ──────────────────────────────────────────────────────────────────────

func writePypiConfig(home, base string) error {
	indexURL := base + "/pypi/simple/"

	pipConf := filepath.Join(home, ".pip", "pip.conf")
	backupFile(pipConf)
	if err := os.MkdirAll(filepath.Dir(pipConf), 0755); err != nil {
		return err
	}
	pipContent := "[global]\nindex-url = " + indexURL + "\ntrusted-host = 127.0.0.1\n"
	if err := os.WriteFile(pipConf, []byte(pipContent), 0644); err != nil {
		return err
	}

	uvConf := filepath.Join(home, ".config", "uv", "uv.toml")
	backupFile(uvConf)
	if err := os.MkdirAll(filepath.Dir(uvConf), 0755); err != nil {
		return err
	}
	uvContent := "[pip]\nindex-url = \"" + indexURL + "\"\n"
	return os.WriteFile(uvConf, []byte(uvContent), 0644)
}

// ── go ────────────────────────────────────────────────────────────────────────

func writeGoConfig(home, base string) error {
	goProxy := base + "/go,off"
	block := "# BEGIN escrow-go\nexport GOPROXY=" + goProxy + "\nexport GONOSUMDB=*\n# END escrow-go\n"

	profiles := []string{filepath.Join(home, ".zprofile")}
	if _, err := os.Stat(filepath.Join(home, ".bash_profile")); err == nil {
		profiles = append(profiles, filepath.Join(home, ".bash_profile"))
	}
	for _, p := range profiles {
		if err := upsertShellBlock(p, block); err != nil {
			return fmt.Errorf("%s: %v", filepath.Base(p), err)
		}
	}
	return nil
}

func upsertShellBlock(profile, block string) error {
	const (
		begin = "# BEGIN escrow-go"
		end   = "# END escrow-go"
	)
	data, _ := os.ReadFile(profile)
	content := string(data)

	if idx := strings.Index(content, begin); idx >= 0 {
		// Replace existing block in-place.
		endIdx := strings.Index(content, end)
		if endIdx < 0 {
			endIdx = len(content)
		} else {
			endIdx += len(end)
			if endIdx < len(content) && content[endIdx] == '\n' {
				endIdx++
			}
		}
		content = content[:idx] + block + content[endIdx:]
	} else {
		backupFile(profile)
		if len(content) > 0 && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += block
	}
	return os.WriteFile(profile, []byte(content), 0644)
}

func removeGoMarkers(profile string) bool {
	const (
		begin = "# BEGIN escrow-go"
		end   = "# END escrow-go"
	)
	data, err := os.ReadFile(profile)
	if err != nil || !strings.Contains(string(data), begin) {
		return false
	}
	content := string(data)
	startIdx := strings.Index(content, begin)
	endIdx := strings.Index(content, end)
	if endIdx < 0 {
		return false
	}
	endIdx += len(end)
	if endIdx < len(content) && content[endIdx] == '\n' {
		endIdx++
	}
	os.WriteFile(profile, []byte(content[:startIdx]+content[endIdx:]), 0644) //nolint:errcheck
	return true
}

// ── cargo ─────────────────────────────────────────────────────────────────────

func writeCargoConfig(home, base string) error {
	cfgPath := filepath.Join(home, ".cargo", "config.toml")
	backupFile(cfgPath)
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		return err
	}
	existing, _ := os.ReadFile(cfgPath)
	return os.WriteFile(cfgPath, []byte(mergeCargoConfig(string(existing), base+"/cargo/")), 0644)
}

// mergeCargoConfig replaces/appends [source.crates-io] and [source.escrow] sections,
// preserving all other sections (e.g. [net], [build]).
func mergeCargoConfig(existing, registryURL string) string {
	escrowBlock := "\n[source.crates-io]\nreplace-with = \"escrow\"\n\n[source.escrow]\nregistry = \"" + registryURL + "\"\n"
	if strings.TrimSpace(existing) == "" {
		return strings.TrimLeft(escrowBlock, "\n")
	}

	// Remove existing escrow-related sections, keep everything else.
	lines := strings.Split(existing, "\n")
	var result []string
	skip := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "[source.crates-io]" || trimmed == "[source.escrow]" {
			skip = true
			continue
		}
		if skip && strings.HasPrefix(trimmed, "[") {
			skip = false
		}
		if !skip {
			result = append(result, line)
		}
	}

	// Trim trailing blank lines.
	for len(result) > 0 && strings.TrimSpace(result[len(result)-1]) == "" {
		result = result[:len(result)-1]
	}

	base := strings.Join(result, "\n")
	if base != "" {
		base += "\n"
	}
	return base + escrowBlock
}

// ── nuget ─────────────────────────────────────────────────────────────────────

func writeNugetConfig(home, base string) error {
	cfgPath := filepath.Join(home, ".nuget", "NuGet", "NuGet.Config")
	backupFile(cfgPath)
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		return err
	}
	url := base + "/nuget/v3/index.json"
	content := `<?xml version="1.0" encoding="utf-8"?>
<configuration>
  <packageSources>
    <clear />
    <add key="escrow" value="` + url + `" />
  </packageSources>
</configuration>
`
	return os.WriteFile(cfgPath, []byte(content), 0644)
}

// ── maven ─────────────────────────────────────────────────────────────────────

func writeMavenConfig(home, base string) error {
	cfgPath := filepath.Join(home, ".m2", "settings.xml")
	backupFile(cfgPath)
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		return err
	}

	mirrorXML := "    <mirror>\n" +
		"      <id>escrow</id>\n" +
		"      <name>Escrow Proxy</name>\n" +
		"      <url>" + base + "/maven2/</url>\n" +
		"      <mirrorOf>central</mirrorOf>\n" +
		"    </mirror>"

	data, err := os.ReadFile(cfgPath)
	if os.IsNotExist(err) {
		content := "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<settings>\n  <mirrors>\n" +
			mirrorXML + "\n  </mirrors>\n</settings>\n"
		return os.WriteFile(cfgPath, []byte(content), 0644)
	}
	if err != nil {
		return err
	}

	content := string(data)
	if strings.Contains(content, "<id>escrow</id>") {
		return nil // already present
	}
	if idx := strings.Index(content, "<mirrors>"); idx >= 0 {
		ins := idx + len("<mirrors>")
		if ins < len(content) && content[ins] == '\n' {
			ins++
		}
		content = content[:ins] + mirrorXML + "\n" + content[ins:]
	} else if idx := strings.LastIndex(content, "</settings>"); idx >= 0 {
		content = content[:idx] + "  <mirrors>\n" + mirrorXML + "\n  </mirrors>\n" + content[idx:]
	} else {
		content += "  <mirrors>\n" + mirrorXML + "\n  </mirrors>\n"
	}
	return os.WriteFile(cfgPath, []byte(content), 0644)
}

// ── composer ──────────────────────────────────────────────────────────────────

func writeComposerConfig(home, base string) error {
	cfgPath := filepath.Join(home, ".config", "composer", "config.json")
	backupFile(cfgPath)
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		return err
	}
	content := "{\n  \"repositories\": [\n    {\n      \"type\": \"composer\",\n      \"url\": \"" + base + "\"\n    }\n  ]\n}\n"
	return os.WriteFile(cfgPath, []byte(content), 0644)
}
