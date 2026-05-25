package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

func runConfigWrite(args []string) {
	fs := flag.NewFlagSet("config write", flag.ExitOnError)
	ecosystems := fs.String("ecosystems", strings.Join(allEcosystems, ","), "comma-separated ecosystems")
	proxyURL := fs.String("proxy-url", "http://127.0.0.1:7888", "base URL of the escrow proxy")
	fs.Parse(args) //nolint:errcheck

	if err := validateProxyURL(*proxyURL); err != nil {
		die("--proxy-url: %v", err)
	}

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
	ecosystems := fs.String("ecosystems", strings.Join(allEcosystems, ","), "comma-separated ecosystems to restore")
	fs.Parse(args) //nolint:errcheck

	home, err := os.UserHomeDir()
	if err != nil {
		die("getting home dir: %v", err)
	}

	restored := 0
	for _, eco := range parseEcosystems(*ecosystems) {
		for _, path := range ecosystemGlobalPaths(eco, home) {
			bak := path + ".escrow-backup"
			if _, err := os.Stat(bak); err != nil {
				continue
			}
			data, err := os.ReadFile(bak)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: reading %s: %v\n", bak, err)
				continue
			}
			if err := writeAtomic(path, data, 0644); err != nil {
				fmt.Fprintf(os.Stderr, "warning: restoring %s: %v\n", path, err)
				continue
			}
			os.Remove(bak)
			fmt.Printf("✓ restored %s\n", path)
			restored++
		}
		if eco == "go" {
			for _, p := range ecosystemGlobalPaths("go", home) {
				if removeGoMarkers(p) {
					fmt.Printf("✓ removed GOPROXY block from %s\n", p)
					restored++
				}
			}
		}
	}

	if restored == 0 {
		fmt.Println("nothing to restore")
	}
}

// ecosystemGlobalPaths returns the $HOME config file paths owned by the given ecosystem.
func ecosystemGlobalPaths(eco, home string) []string {
	switch eco {
	case "npm":
		return []string{filepath.Join(home, ".npmrc")}
	case "pypi":
		return []string{
			filepath.Join(home, ".pip", "pip.conf"),
			filepath.Join(home, ".config", "uv", "uv.toml"),
		}
	case "go":
		return []string{
			filepath.Join(home, ".zprofile"),
			filepath.Join(home, ".bash_profile"),
		}
	case "cargo":
		return []string{filepath.Join(home, ".cargo", "config.toml")}
	case "nuget":
		return []string{filepath.Join(home, ".nuget", "NuGet", "NuGet.Config")}
	case "maven":
		return []string{filepath.Join(home, ".m2", "settings.xml")}
	case "composer":
		return []string{filepath.Join(home, ".config", "composer", "config.json")}
	}
	return nil
}

// ── config check ─────────────────────────────────────────────────────────────

func runConfigCheck(args []string) {
	fs := flag.NewFlagSet("config check", flag.ExitOnError)
	ecosystems := fs.String("ecosystems", strings.Join(allEcosystems, ","), "comma-separated ecosystems to check")
	fs.Parse(args) //nolint:errcheck

	home, _ := os.UserHomeDir()
	for _, eco := range parseEcosystems(*ecosystems) {
		path, ok := checkEcoGlobal(eco, home)
		if ok {
			fmt.Printf("%-10s ✓  %s\n", eco, path)
		} else {
			fmt.Printf("%-10s –  not configured\n", eco)
		}
	}
}

// checkEcoGlobal returns the first configured file path for the ecosystem and true,
// or ("", false) if escrow is not active for this ecosystem globally.
func checkEcoGlobal(eco, home string) (string, bool) {
	switch eco {
	case "npm":
		p := filepath.Join(home, ".npmrc")
		return p, isEscrowConfig(p, "npm")
	case "pypi":
		if p := filepath.Join(home, ".pip", "pip.conf"); isEscrowConfig(p, "pypi") {
			return p, true
		}
		p := filepath.Join(home, ".config", "uv", "uv.toml")
		return p, isEscrowConfig(p, "uv")
	case "go":
		for _, name := range []string{".zprofile", ".bash_profile"} {
			p := filepath.Join(home, name)
			if isEscrowConfig(p, "go") {
				return p, true
			}
		}
		return "", false
	case "cargo":
		p := filepath.Join(home, ".cargo", "config.toml")
		return p, isEscrowConfig(p, "cargo")
	case "nuget":
		p := filepath.Join(home, ".nuget", "NuGet", "NuGet.Config")
		return p, isEscrowConfig(p, "nuget")
	case "maven":
		p := filepath.Join(home, ".m2", "settings.xml")
		return p, isEscrowConfig(p, "maven")
	case "composer":
		p := filepath.Join(home, ".config", "composer", "config.json")
		return p, isEscrowConfig(p, "composer")
	}
	return "", false
}

// ── config check-local ────────────────────────────────────────────────────────

func runConfigCheckLocal(args []string) {
	fs := flag.NewFlagSet("config check-local", flag.ExitOnError)
	ecosystems := fs.String("ecosystems", strings.Join(allEcosystems, ","), "comma-separated ecosystems to check")
	fs.Parse(args) //nolint:errcheck

	cwd, _ := os.Getwd()
	for _, eco := range parseEcosystems(*ecosystems) {
		switch eco {
		case "go", "maven":
			fmt.Printf("%-10s N/A (no local config)\n", eco)
			continue
		}
		path, ok := checkEcoLocal(eco, cwd)
		if ok {
			fmt.Printf("%-10s ✓  %s\n", eco, path)
		} else {
			fmt.Printf("%-10s –  not configured\n", eco)
		}
	}
}

// checkEcoLocal returns the local config path for the ecosystem in dir and true
// if it is currently pointing at escrow, or ("", false) otherwise.
func checkEcoLocal(eco, dir string) (string, bool) {
	var path, hint string
	switch eco {
	case "npm":
		path, hint = filepath.Join(dir, ".npmrc"), "npm"
	case "cargo":
		path, hint = filepath.Join(dir, ".cargo", "config.toml"), "cargo"
	case "nuget":
		path, hint = filepath.Join(dir, "nuget.config"), "nuget"
	case "pypi":
		path, hint = filepath.Join(dir, "uv.toml"), "uv"
	case "composer":
		path, hint = filepath.Join(dir, "composer.json"), "composer"
	default:
		return "", false
	}
	return path, isEscrowConfig(path, hint)
}

// validateProxyURL rejects proxy URLs that would break config file formats
// or allow injection into XML, JSON, or INI/TOML values.
func validateProxyURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("URL must include a host")
	}
	// Guard against characters that break config file formats.
	if strings.ContainsAny(raw, "\n\r\"'<>") {
		return fmt.Errorf("URL contains characters not safe for config files")
	}
	return nil
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
	npmrc := filepath.Join(home, ".npmrc")
	if err := backupFile(npmrc); err != nil {
		return fmt.Errorf("backing up %s: %v", npmrc, err)
	}
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
	return writeAtomic(npmrc, []byte(strings.Join(lines, "\n")), 0644)
}

// ── pypi ──────────────────────────────────────────────────────────────────────

func writePypiConfig(home, base string) error {
	indexURL := base + "/pypi/simple/"

	pipConf := filepath.Join(home, ".pip", "pip.conf")
	if err := backupFile(pipConf); err != nil {
		return fmt.Errorf("backing up %s: %v", pipConf, err)
	}
	if err := os.MkdirAll(filepath.Dir(pipConf), 0755); err != nil {
		return err
	}
	pipContent := "[global]\nindex-url = " + indexURL + "\ntrusted-host = 127.0.0.1\n"
	if err := writeAtomic(pipConf, []byte(pipContent), 0644); err != nil {
		return err
	}

	uvConf := filepath.Join(home, ".config", "uv", "uv.toml")
	if err := backupFile(uvConf); err != nil {
		return fmt.Errorf("backing up %s: %v", uvConf, err)
	}
	if err := os.MkdirAll(filepath.Dir(uvConf), 0755); err != nil {
		return err
	}
	// Use json.Marshal to safely quote the URL string in a TOML context.
	quoted, _ := json.Marshal(indexURL)
	uvContent := "[pip]\nindex-url = " + string(quoted) + "\n"
	return writeAtomic(uvConf, []byte(uvContent), 0644)
}

// ── go ────────────────────────────────────────────────────────────────────────

func writeGoConfig(home, base string) error {
	goProxy := base + "/go,off"
	block := "# BEGIN escrow-go\nexport GOPROXY=" + goProxy + "\nexport GONOSUMDB=*\n# END escrow-go\n"

	// Only write to profiles that already exist; create .zprofile if neither exists.
	var profiles []string
	for _, name := range []string{".zprofile", ".bash_profile"} {
		p := filepath.Join(home, name)
		if _, err := os.Stat(p); err == nil {
			profiles = append(profiles, p)
		}
	}
	if len(profiles) == 0 {
		profiles = []string{filepath.Join(home, ".zprofile")}
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
		endIdx := strings.Index(content, end)
		if endIdx < 0 {
			// Marker without END — refuse to modify to avoid truncating the file.
			return fmt.Errorf("found %q without %q; remove the stale marker manually", begin, end)
		}
		endIdx += len(end)
		if endIdx < len(content) && content[endIdx] == '\n' {
			endIdx++
		}
		content = content[:idx] + block + content[endIdx:]
	} else {
		if err := backupFile(profile); err != nil {
			return fmt.Errorf("backing up %s: %v", profile, err)
		}
		if len(content) > 0 && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += block
	}
	return writeAtomic(profile, []byte(content), 0644)
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
	writeAtomic(profile, []byte(content[:startIdx]+content[endIdx:]), 0644) //nolint:errcheck
	return true
}

// ── cargo ─────────────────────────────────────────────────────────────────────

func writeCargoConfig(home, base string) error {
	cfgPath := filepath.Join(home, ".cargo", "config.toml")
	if err := backupFile(cfgPath); err != nil {
		return fmt.Errorf("backing up %s: %v", cfgPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		return err
	}

	existing, _ := os.ReadFile(cfgPath)
	merged, err := mergeCargoConfig(existing, base+"/cargo/")
	if err != nil {
		return err
	}
	return writeAtomic(cfgPath, merged, 0644)
}

// mergeCargoConfig uses the TOML library to parse the existing config, set the
// escrow source entries, and re-encode. All non-escrow sections are preserved.
func mergeCargoConfig(existing []byte, registryURL string) ([]byte, error) {
	var cfg map[string]interface{}
	if len(bytes.TrimSpace(existing)) > 0 {
		if err := toml.Unmarshal(existing, &cfg); err != nil {
			return nil, fmt.Errorf("parsing cargo config: %w", err)
		}
	}
	if cfg == nil {
		cfg = make(map[string]interface{})
	}

	source, _ := cfg["source"].(map[string]interface{})
	if source == nil {
		source = make(map[string]interface{})
	}

	cratesIO, _ := source["crates-io"].(map[string]interface{})
	if cratesIO == nil {
		cratesIO = make(map[string]interface{})
	}
	cratesIO["replace-with"] = "escrow"
	source["crates-io"] = cratesIO
	source["escrow"] = map[string]interface{}{"registry": registryURL}
	cfg["source"] = source

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return nil, fmt.Errorf("encoding cargo config: %w", err)
	}
	return buf.Bytes(), nil
}

// ── nuget ─────────────────────────────────────────────────────────────────────

func writeNugetConfig(home, base string) error {
	cfgPath := filepath.Join(home, ".nuget", "NuGet", "NuGet.Config")
	if err := backupFile(cfgPath); err != nil {
		return fmt.Errorf("backing up %s: %v", cfgPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		return err
	}
	// Escape the URL for safe XML attribute embedding.
	escapedURL := xmlEscape(base + "/nuget/v3/index.json")
	content := `<?xml version="1.0" encoding="utf-8"?>
<configuration>
  <packageSources>
    <clear />
    <add key="escrow" value="` + escapedURL + `" />
  </packageSources>
</configuration>
`
	return writeAtomic(cfgPath, []byte(content), 0644)
}

// ── maven ─────────────────────────────────────────────────────────────────────

func writeMavenConfig(home, base string) error {
	cfgPath := filepath.Join(home, ".m2", "settings.xml")
	if err := backupFile(cfgPath); err != nil {
		return fmt.Errorf("backing up %s: %v", cfgPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		return err
	}

	escapedURL := xmlEscape(base + "/maven2/")
	mirrorXML := "    <mirror>\n" +
		"      <id>escrow</id>\n" +
		"      <name>Escrow Proxy</name>\n" +
		"      <url>" + escapedURL + "</url>\n" +
		"      <mirrorOf>central</mirrorOf>\n" +
		"    </mirror>"

	data, err := os.ReadFile(cfgPath)
	if os.IsNotExist(err) {
		content := "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<settings>\n  <mirrors>\n" +
			mirrorXML + "\n  </mirrors>\n</settings>\n"
		return writeAtomic(cfgPath, []byte(content), 0644)
	}
	if err != nil {
		return err
	}

	content := string(data)
	if strings.Contains(content, "<id>escrow</id>") {
		return nil
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
	return writeAtomic(cfgPath, []byte(content), 0644)
}

// ── composer ──────────────────────────────────────────────────────────────────

func writeComposerConfig(home, base string) error {
	cfgPath := filepath.Join(home, ".config", "composer", "config.json")
	if err := backupFile(cfgPath); err != nil {
		return fmt.Errorf("backing up %s: %v", cfgPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		return err
	}
	// Use encoding/json to safely marshal the URL string.
	urlJSON, err := json.Marshal(base)
	if err != nil {
		return err
	}
	content := "{\n  \"repositories\": [\n    {\n      \"type\": \"composer\",\n      \"url\": " +
		string(urlJSON) + "\n    }\n  ]\n}\n"
	return writeAtomic(cfgPath, []byte(content), 0644)
}

// xmlEscape returns s with XML special characters escaped for safe embedding
// in element content or attribute values.
func xmlEscape(s string) string {
	var buf bytes.Buffer
	xml.EscapeText(&buf, []byte(s)) //nolint:errcheck
	return buf.String()
}

// ── config write-local ────────────────────────────────────────────────────────

// runConfigWriteLocal writes per-tool proxy config to the current working directory.
// Supported: npm, cargo, nuget, pypi (uv.toml), composer.
// Skipped:   go (env vars are shell-global), maven (no project-local settings.xml).
func runConfigWriteLocal(args []string) {
	fs := flag.NewFlagSet("config write-local", flag.ExitOnError)
	ecosystems := fs.String("ecosystems", "npm,cargo,nuget,pypi,composer", "comma-separated ecosystems (go/maven not supported locally)")
	proxyURL := fs.String("proxy-url", "http://127.0.0.1:7888", "base URL of the escrow proxy")
	fs.Parse(args) //nolint:errcheck

	if err := validateProxyURL(*proxyURL); err != nil {
		die("--proxy-url: %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		die("getting working directory: %v", err)
	}

	base := strings.TrimRight(*proxyURL, "/")
	for _, eco := range parseEcosystems(*ecosystems) {
		switch eco {
		case "go", "maven":
			fmt.Printf("– %s: no project-local config supported (skipping)\n", eco)
			continue
		}
		if err := writeEcoConfigLocal(eco, base, cwd); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: %v\n", eco, err)
		} else {
			fmt.Printf("✓ %s local config written\n", eco)
		}
	}
}

func writeEcoConfigLocal(eco, base, dir string) error {
	switch eco {
	case "npm":
		return writeNpmConfigLocal(dir, base)
	case "cargo":
		return writeCargoConfigLocal(dir, base)
	case "nuget":
		return writeNugetConfigLocal(dir, base)
	case "pypi":
		return writePypiConfigLocal(dir, base)
	case "composer":
		return writeComposerConfigLocal(dir, base)
	}
	return fmt.Errorf("local config not supported for %s", eco)
}

func writeNpmConfigLocal(dir, base string) error {
	path := filepath.Join(dir, ".npmrc")
	backupFile(path) //nolint:errcheck
	url := base + "/"
	data, _ := os.ReadFile(path)
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
	return writeAtomic(path, []byte(strings.Join(lines, "\n")), 0644)
}

func writeCargoConfigLocal(dir, base string) error {
	cargoDir := filepath.Join(dir, ".cargo")
	if err := os.MkdirAll(cargoDir, 0755); err != nil {
		return err
	}
	path := filepath.Join(cargoDir, "config.toml")
	backupFile(path) //nolint:errcheck
	existing, _ := os.ReadFile(path)
	merged, err := mergeCargoConfig(existing, base+"/cargo/")
	if err != nil {
		return err
	}
	return writeAtomic(path, merged, 0644)
}

func writeNugetConfigLocal(dir, base string) error {
	path := filepath.Join(dir, "nuget.config")
	backupFile(path) //nolint:errcheck
	url := xmlEscape(base + "/nuget/v3/index.json")
	content := `<?xml version="1.0" encoding="utf-8"?>
<configuration>
  <packageSources>
    <clear />
    <add key="escrow" value="` + url + `" />
  </packageSources>
</configuration>
`
	return writeAtomic(path, []byte(content), 0644)
}

func writePypiConfigLocal(dir, base string) error {
	path := filepath.Join(dir, "uv.toml")
	backupFile(path) //nolint:errcheck
	quoted, _ := json.Marshal(base + "/pypi/simple/")
	content := "[pip]\nindex-url = " + string(quoted) + "\n"
	return writeAtomic(path, []byte(content), 0644)
}

func writeComposerConfigLocal(dir, base string) error {
	path := filepath.Join(dir, "composer.json")
	backupFile(path) //nolint:errcheck
	newRepo := map[string]interface{}{
		"type": "composer",
		"url":  base,
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		content := map[string]interface{}{
			"repositories": []interface{}{newRepo},
		}
		out, err := json.MarshalIndent(content, "", "  ")
		if err != nil {
			return err
		}
		return writeAtomic(path, append(out, '\n'), 0644)
	}
	if err != nil {
		return err
	}

	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parsing composer.json: %w", err)
	}
	repoJSON, err := json.MarshalIndent([]interface{}{newRepo}, "", "  ")
	if err != nil {
		return err
	}
	cfg["repositories"] = json.RawMessage(repoJSON)
	merged, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(merged, '\n'), 0644)
}

// ── config restore-local ──────────────────────────────────────────────────────

func runConfigRestoreLocal(args []string) {
	fs := flag.NewFlagSet("config restore-local", flag.ExitOnError)
	ecosystems := fs.String("ecosystems", "npm,cargo,nuget,pypi,composer", "comma-separated ecosystems to restore")
	fs.Parse(args) //nolint:errcheck

	cwd, err := os.Getwd()
	if err != nil {
		die("getting working directory: %v", err)
	}

	restored := 0
	for _, eco := range parseEcosystems(*ecosystems) {
		path, _ := checkEcoLocal(eco, cwd)
		if path == "" {
			continue
		}
		bak := path + ".escrow-backup"
		if _, err := os.Stat(bak); err != nil {
			continue
		}
		data, err := os.ReadFile(bak)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: reading %s: %v\n", bak, err)
			continue
		}
		if err := writeAtomic(path, data, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "warning: restoring %s: %v\n", path, err)
			continue
		}
		os.Remove(bak)
		fmt.Printf("✓ restored %s\n", path)
		restored++
	}
	// Also sweep .cargo/ for any remaining backup files.
	restored += restoreLocalBackups(filepath.Join(cwd, ".cargo"))

	if restored == 0 {
		fmt.Println("nothing to restore in current directory")
	}
}

// restoreLocalBackups restores any .escrow-backup files in dir and returns the count.
func restoreLocalBackups(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	restored := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".escrow-backup") {
			continue
		}
		original := filepath.Join(dir, strings.TrimSuffix(name, ".escrow-backup"))
		backup := filepath.Join(dir, name)
		data, err := os.ReadFile(backup)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: reading %s: %v\n", backup, err)
			continue
		}
		if err := writeAtomic(original, data, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "warning: restoring %s: %v\n", original, err)
			continue
		}
		os.Remove(backup)
		fmt.Printf("✓ restored %s\n", original)
		restored++
	}
	// Walk one level into known local config dirs.
	for _, sub := range []string{".cargo"} {
		subDir := filepath.Join(dir, sub)
		restored += restoreLocalBackups(subDir)
	}
	return restored
}
