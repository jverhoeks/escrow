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

// ── config write-renovate ─────────────────────────────────────────────────────

// runConfigWriteRenovate generates a renovate.json that routes Renovate's
// datasource lookups through the escrow proxy.
//
// Renovate has its own HTTP client for version lookups — it does NOT use
// ~/.cargo/config.toml, ~/.m2/settings.xml, or most other tool configs.
// The exceptions are: npm (~/.npmrc ✓), PyPI (PIP_INDEX_URL env ✓),
// Go (GOPROXY env ✓). For everything else, renovate.json is required.
//
// Coverage:
//   npm/pnpm    auto via ~/.npmrc       no renovate.json needed
//   PyPI        auto via PIP_INDEX_URL  no renovate.json needed
//   Go          auto via GOPROXY        no renovate.json needed
//   Cargo       hardcoded crates.io     renovate.json required ← this command
//   Maven       hardcoded Maven Central renovate.json required ← this command
//   NuGet       hardcoded nuget.org     renovate.json required ← this command
//   Composer    hardcoded packagist.org renovate.json required ← this command
func runConfigWriteRenovate(args []string) {
	fs := flag.NewFlagSet("config write-renovate", flag.ExitOnError)
	ecosystems := fs.String("ecosystems", strings.Join(allEcosystems, ","), "comma-separated ecosystems")
	proxyURL := fs.String("proxy-url", "http://127.0.0.1:7888", "base URL of the escrow proxy")
	output := fs.String("output", "renovate.json", "output file path (use - for stdout)")
	minAge := fs.Int("min-age", 7, "minimumReleaseAge in days to add to packageRules (0 = omit)")
	ageOnly := fs.Bool("age-only", false, "emit only minimumReleaseAge rules — no proxy registryUrls (for teams without an escrow proxy)")
	fs.Parse(args) //nolint:errcheck

	if !*ageOnly {
		if err := validateProxyURL(*proxyURL); err != nil {
			die("--proxy-url: %v", err)
		}
	}

	ecos := parseEcosystems(*ecosystems)
	base := strings.TrimRight(*proxyURL, "/")
	content := buildRenovateConfig(ecos, base, *minAge, *ageOnly)

	if *output == "-" {
		fmt.Print(content)
		return
	}
	if err := os.WriteFile(*output, []byte(content), 0644); err != nil {
		die("writing %s: %v", *output, err)
	}
	fmt.Printf("✓ wrote %s\n", *output)
	fmt.Println()

	if *ageOnly {
		fmt.Printf("  minimumReleaseAge: %d days (all ecosystems)\n", *minAge)
		fmt.Println("  No proxy registryUrls — age gate only.")
		fmt.Println("  Pair with 'escrow-cli config write' for proxy protection at install time.")
		return
	}

	fmt.Println("Renovate coverage:")
	fmt.Println("  npm/pnpm    ✓  auto via NPM_CONFIG_REGISTRY env / ~/.npmrc")
	fmt.Println("  PyPI        ✓  auto via PIP_INDEX_URL env")
	fmt.Println("  Go          ✓  auto via GOPROXY env")
	fmt.Println("  cargo       ~  Renovate queries crates.io directly (git index,")
	fmt.Println("                  not sparse HTTP). Escrow gates cargo at build time")
	fmt.Println("                  via ~/.cargo/config.toml — run: escrow-cli config write --ecosystems cargo --git")
	for _, eco := range ecos {
		switch eco {
		case "maven":
			fmt.Println("  maven       ✓  via renovate.json registryUrls")
		case "nuget":
			fmt.Println("  nuget       ✓  via renovate.json registryUrls")
		case "composer":
			fmt.Println("  composer    ✓  via renovate.json registryUrls")
		}
	}
	if *minAge > 0 {
		fmt.Printf("\n  minimumReleaseAge: %d days (minor/patch), %d days (major)\n", *minAge, *minAge*2)
	}
	fmt.Println()
	fmt.Println("Note: for cloud-hosted Renovate (GitHub App) the proxy must be")
	fmt.Println("network-accessible. For self-hosted Renovate, 127.0.0.1 works.")
}

// buildRenovateConfig generates renovate.json content.
// minAgeDays > 0 adds minimumReleaseAge packageRules.
// ageOnly = true skips all proxy registryUrls and emits only age rules.
func buildRenovateConfig(ecosystems []string, base string, minAgeDays int, ageOnly bool) string {
	ecoSet := make(map[string]bool)
	for _, e := range ecosystems {
		ecoSet[e] = true
	}

	var sb strings.Builder
	sb.WriteString("{\n")
	sb.WriteString(`  "$schema": "https://docs.renovatebot.com/renovate-schema.json",` + "\n")
	sb.WriteString(`  "extends": ["config:recommended"],` + "\n")

	if !ageOnly {
		sb.WriteString("\n")
		sb.WriteString("  // npm, PyPI (pip/uv), and Go are auto-detected via NPM_CONFIG_REGISTRY,\n")
		sb.WriteString("  // PIP_INDEX_URL, and GOPROXY env vars — no extra config needed here.\n")
		sb.WriteString("\n")
		sb.WriteString("  // Cargo: Renovate uses git-clone for custom registries; escrow serves\n")
		sb.WriteString("  // sparse HTTP. Escrow gates cargo at build time via ~/.cargo/config.toml.\n")
		sb.WriteString("\n")
	}

	hasEntries := false

	if !ageOnly {
		// Maven / Gradle
		if ecoSet["maven"] {
			sb.WriteString(`  "maven": {` + "\n")
			sb.WriteString(`    "registryUrls": ["` + base + `/maven2/"]` + "\n")
			sb.WriteString("  },\n")
			hasEntries = true
		}
		// NuGet
		if ecoSet["nuget"] {
			sb.WriteString(`  "nuget": {` + "\n")
			sb.WriteString(`    "registryUrls": ["` + base + `/nuget/v3/index.json"]` + "\n")
			sb.WriteString("  },\n")
			hasEntries = true
		}
		// Composer
		if ecoSet["composer"] {
			sb.WriteString(`  "packagist": {` + "\n")
			sb.WriteString(`    "registryUrls": ["` + base + `"]` + "\n")
			sb.WriteString("  },\n")
			hasEntries = true
		}
	}

	// minimumReleaseAge packageRules — works for ALL ecosystems including cargo.
	// Tested: Renovate correctly holds back versions younger than the threshold
	// and files them in pendingVersions until they age past it.
	if minAgeDays > 0 {
		sb.WriteString(`  "packageRules": [` + "\n")
		sb.WriteString("    {\n")
		sb.WriteString(`      "matchUpdateTypes": ["minor", "patch"],` + "\n")
		sb.WriteString(fmt.Sprintf(`      "minimumReleaseAge": "%d days"`, minAgeDays) + "\n")
		sb.WriteString("    },\n")
		sb.WriteString("    {\n")
		sb.WriteString(`      "matchUpdateTypes": ["major"],` + "\n")
		sb.WriteString(fmt.Sprintf(`      "minimumReleaseAge": "%d days"`, minAgeDays*2) + "\n")
		sb.WriteString("    },\n")
		sb.WriteString("    {\n")
		sb.WriteString(`      "matchManagers": ["cargo"],` + "\n")
		sb.WriteString(`      "postUpgradeTasks": {` + "\n")
		sb.WriteString(`        "commands": ["cargo audit --deny warnings"],` + "\n")
		sb.WriteString(`        "fileFilters": ["Cargo.lock"],` + "\n")
		sb.WriteString(`        "executionMode": "branch"` + "\n")
		sb.WriteString("      }\n")
		sb.WriteString("    }\n")
		sb.WriteString("  ],\n")
		hasEntries = true
	}

	if hasEntries && !ageOnly {
		sb.WriteString(`  "hostRules": [` + "\n")
		sb.WriteString("    {\n")
		sb.WriteString(`      "matchHost": "127.0.0.1",` + "\n")
		sb.WriteString(`      "insecureRegistry": true` + "\n")
		sb.WriteString("    }\n")
		sb.WriteString("  ]\n")
	} else if !hasEntries {
		sb.WriteString(`  "enabled": true` + "\n")
	} else {
		// age-only: close cleanly after packageRules (trailing comma already written above)
		// remove trailing comma from packageRules close
		result := sb.String()
		result = strings.TrimSuffix(strings.TrimRight(result, "\n"), ",") + "\n"
		return result + "}\n"
	}
	sb.WriteString("}\n")

	return sb.String()
}

func runConfigWrite(args []string) {
	fs := flag.NewFlagSet("config write", flag.ExitOnError)
	ecosystems := fs.String("ecosystems", strings.Join(allEcosystems, ","), "comma-separated ecosystems")
	proxyURL := fs.String("proxy-url", "http://127.0.0.1:7888", "base URL of the escrow proxy")
	gitCLI := fs.Bool("git", false, "set [net] git-fetch-with-cli=true in cargo config so git deps and index updates use the system git, not cargo's libgit2 (prevents git-protocol 404s when a network proxy is active)")
	fs.Parse(args) //nolint:errcheck

	if err := validateProxyURL(*proxyURL); err != nil {
		die("--proxy-url: %v", err)
	}

	ecos := parseEcosystems(*ecosystems)
	base := strings.TrimRight(*proxyURL, "/")

	for _, eco := range ecos {
		var err error
		if eco == "cargo" && *gitCLI {
			err = writeCargoConfigWithGitCLI(base)
		} else {
			err = writeEcoConfig(eco, base)
		}
		if err != nil {
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
		// Remove shell-profile marker blocks.
		for _, p := range shellProfiles(home) {
			if eco == "go" && removeShellBlock(p, "# BEGIN escrow-go", "# END escrow-go") {
				fmt.Printf("✓ removed GOPROXY block from %s\n", p)
				restored++
			}
			if eco == "pypi" && removeShellBlock(p, "# BEGIN escrow-python", "# END escrow-python") {
				fmt.Printf("✓ removed PIP_INDEX_URL block from %s\n", p)
				restored++
			}
		}
	}

	if restored == 0 {
		fmt.Println("nothing to restore")
	}
}

// ecosystemGlobalPaths returns all $HOME config file paths owned by the given ecosystem.
func ecosystemGlobalPaths(eco, home string) []string {
	switch eco {
	case "npm":
		return []string{
			filepath.Join(home, ".npmrc"),
			filepath.Join(home, ".yarnrc"),
			filepath.Join(home, ".yarnrc.yml"),
			filepath.Join(home, ".bunfig.toml"),
		}
	case "pypi":
		return []string{
			filepath.Join(home, ".pip", "pip.conf"),
			filepath.Join(home, ".config", "uv", "uv.toml"),
			// shell profiles handled separately (marker removal, not file restore)
		}
	case "go":
		return shellProfiles(home)
	case "cargo":
		return []string{filepath.Join(home, ".cargo", "config.toml")}
	case "nuget":
		return []string{filepath.Join(home, ".nuget", "NuGet", "NuGet.Config")}
	case "maven":
		return []string{
			filepath.Join(home, ".m2", "settings.xml"),
			filepath.Join(home, ".gradle", "init.d", "escrow-mirror.gradle"),
		}
	case "composer":
		return []string{filepath.Join(home, ".config", "composer", "config.json")}
	}
	return nil
}

// ── config check ─────────────────────────────────────────────────────────────

type toolCheck struct {
	label string // display label, e.g. "npm/pnpm", "yarn v1"
	path  string // config file path or description
	ok    bool
}

func runConfigCheck(args []string) {
	fs := flag.NewFlagSet("config check", flag.ExitOnError)
	ecosystems := fs.String("ecosystems", strings.Join(allEcosystems, ","), "comma-separated ecosystems to check")
	fs.Parse(args) //nolint:errcheck

	home, _ := os.UserHomeDir()
	for _, eco := range parseEcosystems(*ecosystems) {
		for _, c := range checkEcoGlobalAll(eco, home) {
			if c.ok {
				fmt.Printf("%-14s ✓  %s\n", c.label, c.path)
			} else {
				fmt.Printf("%-14s –  %s\n", c.label, c.path)
			}
		}
	}
}

// checkEcoGlobalAll returns a status entry for every tool in the ecosystem.
func checkEcoGlobalAll(eco, home string) []toolCheck {
	switch eco {
	case "npm":
		npmrc := filepath.Join(home, ".npmrc")
		yarnrc := filepath.Join(home, ".yarnrc")
		yarnYml := filepath.Join(home, ".yarnrc.yml")
		bunfig := filepath.Join(home, ".bunfig.toml")
		return []toolCheck{
			{"npm/pnpm", npmrc, isEscrowConfig(npmrc, "npm")},
			{"yarn (v1)", yarnrc, isEscrowConfig(yarnrc, "yarn1")},
			{"yarn (v2+)", yarnYml, isEscrowConfig(yarnYml, "yarnberry")},
			{"bun", bunfig, isEscrowConfig(bunfig, "bun")},
		}
	case "pypi":
		pip := filepath.Join(home, ".pip", "pip.conf")
		uv := filepath.Join(home, ".config", "uv", "uv.toml")
		var poetryOk bool
		for _, p := range shellProfiles(home) {
			if isEscrowConfig(p, "python-env") {
				poetryOk = true
				break
			}
		}
		return []toolCheck{
			{"pip", pip, isEscrowConfig(pip, "pypi")},
			{"uv", uv, isEscrowConfig(uv, "uv")},
			{"poetry", "PIP_INDEX_URL in shell profile", poetryOk},
		}
	case "go":
		var goOk bool
		var goPath string
		for _, p := range shellProfiles(home) {
			if isEscrowConfig(p, "go") {
				goOk = true
				goPath = p
				break
			}
		}
		if goPath == "" {
			goPath = "GOPROXY in shell profile"
		}
		return []toolCheck{{"go", goPath, goOk}}
	case "cargo":
		p := filepath.Join(home, ".cargo", "config.toml")
		return []toolCheck{{"cargo", p, isEscrowConfig(p, "cargo")}}
	case "nuget":
		p := filepath.Join(home, ".nuget", "NuGet", "NuGet.Config")
		return []toolCheck{{"nuget", p, isEscrowConfig(p, "nuget")}}
	case "maven":
		mvn := filepath.Join(home, ".m2", "settings.xml")
		gradle := filepath.Join(home, ".gradle", "init.d", "escrow-mirror.gradle")
		return []toolCheck{
			{"maven", mvn, isEscrowConfig(mvn, "maven")},
			{"gradle", gradle, isEscrowConfig(gradle, "gradle")},
		}
	case "composer":
		p := filepath.Join(home, ".config", "composer", "config.json")
		return []toolCheck{{"composer", p, isEscrowConfig(p, "composer")}}
	}
	return nil
}

// checkEcoGlobal returns the first configured path/true for backward compat with check-local.
func checkEcoGlobal(eco, home string) (string, bool) {
	for _, c := range checkEcoGlobalAll(eco, home) {
		if c.ok {
			return c.path, true
		}
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
		for _, c := range checkEcoLocalAll(eco, cwd) {
			if c.ok {
				fmt.Printf("%-14s ✓  %s\n", c.label, c.path)
			} else {
				fmt.Printf("%-14s –  %s\n", c.label, c.path)
			}
		}
	}
}

// checkEcoLocal returns the first configured local path for the ecosystem, or ("", false).
func checkEcoLocal(eco, dir string) (string, bool) {
	for _, c := range checkEcoLocalAll(eco, dir) {
		if c.ok {
			return c.path, true
		}
	}
	return "", false
}

func checkEcoLocalAll(eco, dir string) []toolCheck {
	switch eco {
	case "npm":
		npmrc := filepath.Join(dir, ".npmrc")
		yarnrc := filepath.Join(dir, ".yarnrc")
		yarnYml := filepath.Join(dir, ".yarnrc.yml")
		bunfig := filepath.Join(dir, "bunfig.toml")
		return []toolCheck{
			{"npm/pnpm", npmrc, isEscrowConfig(npmrc, "npm")},
			{"yarn (v1)", yarnrc, isEscrowConfig(yarnrc, "yarn1")},
			{"yarn (v2+)", yarnYml, isEscrowConfig(yarnYml, "yarnberry")},
			{"bun", bunfig, isEscrowConfig(bunfig, "bun")},
		}
	case "pypi":
		uv := filepath.Join(dir, "uv.toml")
		return []toolCheck{
			{"uv", uv, isEscrowConfig(uv, "uv")},
			{"pip", "no local auto-discovery — use global or PIP_INDEX_URL", false},
			{"poetry", "no local registry config — use global shell block or pyproject.toml source", false},
		}
	case "go":
		return []toolCheck{{"go", "env vars are shell-global — use 'config write'", false}}
	case "cargo":
		p := filepath.Join(dir, ".cargo", "config.toml")
		return []toolCheck{{"cargo", p, isEscrowConfig(p, "cargo")}}
	case "nuget":
		p := filepath.Join(dir, "nuget.config")
		return []toolCheck{{"nuget", p, isEscrowConfig(p, "nuget")}}
	case "maven":
		return []toolCheck{
			{"maven", "no project-local settings.xml — use 'config write' for ~/.m2/settings.xml", false},
			{"gradle", "no project-local init script — use 'config write' for ~/.gradle/init.d/", false},
		}
	case "composer":
		p := filepath.Join(dir, "composer.json")
		return []toolCheck{{"composer", p, isEscrowConfig(p, "composer")}}
	}
	return nil
}

// validateProxyURL rejects proxy URLs that could break config file formats
// or inject into XML, JSON, INI/TOML values, Groovy/shell strings.
// The full set of rejected characters: whitespace, all shell metacharacters,
// all quote characters, redirection symbols, and TOML/Groovy escapes.
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
	// Reject any character that requires escaping in shell, Groovy, XML, JSON, or TOML.
	if strings.ContainsAny(raw, " \t\n\r\"'<>`$;&|()*?[]{}\\") {
		return fmt.Errorf("URL contains characters not safe for config files (whitespace, quotes, or shell metacharacters)")
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

// ── npm / pnpm / yarn / bun ───────────────────────────────────────────────────

// writeNpmConfig configures all JS package managers that are installed or
// already have a config file: npm+pnpm (.npmrc), yarn v1 (.yarnrc),
// yarn berry (.yarnrc.yml), and bun (bunfig.toml).
func writeNpmConfig(home, base string) error {
	url := base + "/"
	var errs []string

	// npm + pnpm: both read from .npmrc
	if err := writeNpmrcRegistry(filepath.Join(home, ".npmrc"), url); err != nil {
		errs = append(errs, "npmrc: "+err.Error())
	}

	// yarn v1 (.yarnrc) — write if installed or file already exists
	yarnrcPath := filepath.Join(home, ".yarnrc")
	if yarnMajorVersion() == 1 || fileExists(yarnrcPath) {
		if err := writeYarnV1Registry(yarnrcPath, url); err != nil {
			errs = append(errs, "yarnrc: "+err.Error())
		}
	}

	// yarn berry (.yarnrc.yml) — write if installed (v2+) or file already exists
	yarnYmlPath := filepath.Join(home, ".yarnrc.yml")
	if yarnMajorVersion() >= 2 || fileExists(yarnYmlPath) {
		if err := writeYarnBerryRegistry(yarnYmlPath, url); err != nil {
			errs = append(errs, "yarnrc.yml: "+err.Error())
		}
	}

	// bun (bunfig.toml) — write if installed or file already exists
	bunfigPath := filepath.Join(home, ".bunfig.toml")
	if toolInPath("bun") || fileExists(bunfigPath) {
		if err := writeBunRegistry(bunfigPath, url); err != nil {
			errs = append(errs, "bunfig.toml: "+err.Error())
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// writeNpmrcRegistry writes or updates the registry= line in an .npmrc file.
// Used by both npm and pnpm (pnpm reads from .npmrc by default).
// Always edits the file at path directly — works for both global and local config.
func writeNpmrcRegistry(path, url string) error {
	if err := backupFile(path); err != nil {
		return fmt.Errorf("backing up %s: %v", path, err)
	}
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

// writeYarnV1Registry writes the registry to a yarn v1 .yarnrc file.
func writeYarnV1Registry(path, url string) error {
	backupFile(path) //nolint:errcheck
	data, _ := os.ReadFile(path)
	lines := strings.Split(string(data), "\n")
	found := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "registry ") {
			lines[i] = `registry "` + url + `"`
			found = true
			break
		}
	}
	if !found {
		lines = append(lines, `registry "`+url+`"`)
	}
	return writeAtomic(path, []byte(strings.Join(lines, "\n")), 0644)
}

// writeYarnBerryRegistry writes or updates npmRegistryServer in a .yarnrc.yml file.
func writeYarnBerryRegistry(path, url string) error {
	backupFile(path) //nolint:errcheck
	data, _ := os.ReadFile(path)
	lines := strings.Split(string(data), "\n")
	found := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "npmRegistryServer:") {
			lines[i] = `npmRegistryServer: "` + url + `"`
			found = true
			break
		}
	}
	if !found {
		lines = append(lines, `npmRegistryServer: "`+url+`"`)
	}
	return writeAtomic(path, []byte(strings.Join(lines, "\n")), 0644)
}

// writeBunRegistry writes or updates [install].registry in a bunfig.toml file.
func writeBunRegistry(path, url string) error {
	backupFile(path) //nolint:errcheck
	existing, _ := os.ReadFile(path)
	var cfg map[string]interface{}
	if len(bytes.TrimSpace(existing)) > 0 {
		if err := toml.Unmarshal(existing, &cfg); err != nil {
			cfg = nil // parse failure — overwrite safely
		}
	}
	if cfg == nil {
		cfg = make(map[string]interface{})
	}
	install, _ := cfg["install"].(map[string]interface{})
	if install == nil {
		install = make(map[string]interface{})
	}
	install["registry"] = url
	cfg["install"] = install
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return err
	}
	return writeAtomic(path, buf.Bytes(), 0644)
}

// yarnMajorVersion returns the major version of the yarn binary (1 for classic,
// 2+ for berry) or 0 if yarn is not installed.
func yarnMajorVersion() int {
	out, err := exec.Command("yarn", "--version").Output()
	if err != nil {
		return 0
	}
	v := strings.TrimSpace(string(out))
	if strings.HasPrefix(v, "1.") {
		return 1
	}
	if v != "" {
		return 2 // berry (2.x / 3.x / 4.x)
	}
	return 0
}

func toolInPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// ── pypi / uv / poetry ───────────────────────────────────────────────────────

func writePypiConfig(home, base string) error {
	indexURL := base + "/pypi/simple/"

	// pip
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

	// uv
	uvConf := filepath.Join(home, ".config", "uv", "uv.toml")
	if err := backupFile(uvConf); err != nil {
		return fmt.Errorf("backing up %s: %v", uvConf, err)
	}
	if err := os.MkdirAll(filepath.Dir(uvConf), 0755); err != nil {
		return err
	}
	quoted, _ := json.Marshal(indexURL)
	uvContent := "[pip]\nindex-url = " + string(quoted) + "\n"
	if err := writeAtomic(uvConf, []byte(uvContent), 0644); err != nil {
		return err
	}

	// poetry: no global "default registry" setting exists; inject PIP_INDEX_URL
	// into shell profiles — poetry respects this env var as a fallback source.
	// shellQuote prevents shell-metacharacter injection from the proxy URL.
	q := shellQuote(indexURL)
	block := "# BEGIN escrow-python\nexport PIP_INDEX_URL=" + q +
		"\nexport UV_INDEX_URL=" + q + "\n# END escrow-python\n"
	profiles := shellProfiles(home)
	for _, p := range profiles {
		if err := upsertShellBlock(p, block, "# BEGIN escrow-python", "# END escrow-python"); err != nil {
			return fmt.Errorf("%s: %v", filepath.Base(p), err)
		}
	}
	return nil
}

// shellProfiles returns the shell profiles to write to (existing only, or .zprofile as fallback).
func shellProfiles(home string) []string {
	var profiles []string
	for _, name := range []string{".zprofile", ".bash_profile"} {
		p := filepath.Join(home, name)
		if fileExists(p) {
			profiles = append(profiles, p)
		}
	}
	if len(profiles) == 0 {
		profiles = []string{filepath.Join(home, ".zprofile")}
	}
	return profiles
}

// ── go ────────────────────────────────────────────────────────────────────────

func writeGoConfig(home, base string) error {
	goProxy := base + "/go,off"
	// shellQuote prevents shell injection from a proxy URL containing $, ;, &, etc.
	block := "# BEGIN escrow-go\nexport GOPROXY=" + shellQuote(goProxy) + "\nexport GONOSUMDB='*'\n# END escrow-go\n"
	for _, p := range shellProfiles(home) {
		if err := upsertShellBlock(p, block, "# BEGIN escrow-go", "# END escrow-go"); err != nil {
			return fmt.Errorf("%s: %v", filepath.Base(p), err)
		}
	}
	return nil
}

// upsertShellBlock writes or replaces a named block in a shell profile.
// The block is identified by begin/end marker strings.
func upsertShellBlock(profile, block, begin, end string) error {
	data, _ := os.ReadFile(profile)
	content := string(data)

	if idx := strings.Index(content, begin); idx >= 0 {
		endIdx := strings.Index(content, end)
		if endIdx < 0 {
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

// removeShellBlock removes a begin/end marker block from a shell profile.
func removeShellBlock(profile, begin, end string) bool {
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

func removeGoMarkers(profile string) bool {
	return removeShellBlock(profile, "# BEGIN escrow-go", "# END escrow-go")
}

// ── cargo ─────────────────────────────────────────────────────────────────────

func writeCargoConfig(home, base string) error {
	return writeCargoConfigOpts(home, base, false)
}

// writeCargoConfigWithGitCLI writes the escrow cargo config AND sets
// [net] git-fetch-with-cli = true.
//
// With git-fetch-with-cli enabled, all git operations (git dependencies in
// Cargo.toml, git-based index updates) use the system git binary instead of
// cargo's embedded libgit2. This prevents git-protocol 404s when a network
// layer (pf redirect, corporate proxy) intercepts TCP and the escrow proxy
// does not speak the git smart-HTTP protocol.
func writeCargoConfigWithGitCLI(base string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if err := writeCargoConfigOpts(home, base, true); err != nil {
		return err
	}
	fmt.Println("  [net] git-fetch-with-cli = true  (git deps use system git, not libgit2)")
	return nil
}

func writeCargoConfigOpts(home, base string, gitFetchWithCLI bool) error {
	cfgPath := filepath.Join(home, ".cargo", "config.toml")
	if err := backupFile(cfgPath); err != nil {
		return fmt.Errorf("backing up %s: %v", cfgPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		return err
	}
	existing, _ := os.ReadFile(cfgPath)
	merged, err := mergeCargoConfig(existing, base+"/cargo/", gitFetchWithCLI)
	if err != nil {
		return err
	}
	return writeAtomic(cfgPath, merged, 0644)
}

// mergeCargoConfig uses the TOML library to parse the existing config, set the
// escrow source entries, and re-encode. All non-escrow sections are preserved.
// If gitFetchWithCLI is true, [net] git-fetch-with-cli = true is also set.
func mergeCargoConfig(existing []byte, registryURL string, gitFetchWithCLI bool) ([]byte, error) {
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

	if gitFetchWithCLI {
		net, _ := cfg["net"].(map[string]interface{})
		if net == nil {
			net = make(map[string]interface{})
		}
		net["git-fetch-with-cli"] = true
		cfg["net"] = net
	}

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

// ── maven / gradle ────────────────────────────────────────────────────────────

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
	if err := writeAtomic(cfgPath, []byte(content), 0644); err != nil {
		return err
	}

	// Gradle: write init script that redirects all Maven repos through escrow.
	return writeGradleConfig(home, base)
}

// writeGradleConfig writes a Gradle init script that redirects all Maven
// repository URLs to the escrow proxy. The script is placed in
// ~/.gradle/init.d/ so it applies globally to all Gradle projects.
func writeGradleConfig(home, base string) error {
	dir := filepath.Join(home, ".gradle", "init.d")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	path := filepath.Join(dir, "escrow-mirror.gradle")
	backupFile(path) //nolint:errcheck
	url := base + "/maven2/"
	content := `// Escrow supply-chain proxy — managed by escrow-cli
// Redirects all Maven repository requests through the escrow proxy.
allprojects {
    buildscript {
        repositories {
            all { ArtifactRepository repo ->
                if (repo instanceof MavenArtifactRepository
                        && !repo.url.toString().startsWith('file:')) {
                    repo.url = new URI('` + url + `')
                }
            }
        }
    }
    repositories {
        all { ArtifactRepository repo ->
            if (repo instanceof MavenArtifactRepository
                    && !repo.url.toString().startsWith('file:')) {
                repo.url = new URI('` + url + `')
            }
        }
    }
}
`
	return writeAtomic(path, []byte(content), 0644)
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

// writeNpmConfigLocal writes .npmrc, .yarnrc, .yarnrc.yml, and bunfig.toml
// to the project directory — whichever tools are installed or already configured.
func writeNpmConfigLocal(dir, base string) error {
	url := base + "/"
	var errs []string

	if err := writeNpmrcRegistry(filepath.Join(dir, ".npmrc"), url); err != nil {
		errs = append(errs, "npmrc: "+err.Error())
	}
	if yarnrc := filepath.Join(dir, ".yarnrc"); yarnMajorVersion() == 1 || fileExists(yarnrc) {
		if err := writeYarnV1Registry(yarnrc, url); err != nil {
			errs = append(errs, "yarnrc: "+err.Error())
		}
	}
	if yarnYml := filepath.Join(dir, ".yarnrc.yml"); yarnMajorVersion() >= 2 || fileExists(yarnYml) {
		if err := writeYarnBerryRegistry(yarnYml, url); err != nil {
			errs = append(errs, "yarnrc.yml: "+err.Error())
		}
	}
	if bunfig := filepath.Join(dir, "bunfig.toml"); toolInPath("bun") || fileExists(bunfig) {
		if err := writeBunRegistry(bunfig, url); err != nil {
			errs = append(errs, "bunfig.toml: "+err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func writeCargoConfigLocal(dir, base string) error {
	cargoDir := filepath.Join(dir, ".cargo")
	if err := os.MkdirAll(cargoDir, 0755); err != nil {
		return err
	}
	path := filepath.Join(cargoDir, "config.toml")
	backupFile(path) //nolint:errcheck
	existing, _ := os.ReadFile(path)
	merged, err := mergeCargoConfig(existing, base+"/cargo/", false)
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
