package config

import (
	"crypto/rand"
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Server        ServerConfig    `toml:"server"`
	Storage       StorageConfig   `toml:"storage"`
	Policy        *PolicyConfig   `toml:"policy"`
	Ecosystems    EcosystemConfig `toml:"ecosystems"`
	Alerts        AlertsConfig    `toml:"alerts"`
	Dashboard     DashboardConfig `toml:"dashboard"`
	AllowlistPath string          `toml:"allowlist_path"`
	BlocklistPath string          `toml:"blocklist_path"`
	EventLogPath  string          `toml:"eventlog_path"` // JSONL append file; empty = in-memory only
}

type ServerConfig struct {
	Host                      string `toml:"host"`
	Port                      int    `toml:"port"`
	LogLevel                  string `toml:"log_level"`
	WriteTimeoutSeconds       int    `toml:"write_timeout_seconds"`        // 0 → default 120
	ReadHeaderTimeoutSeconds  int    `toml:"read_header_timeout_seconds"`  // 0 → default 10
	IdleTimeoutSeconds        int    `toml:"idle_timeout_seconds"`         // 0 → default 120
	TLSCertFile               string `toml:"tls_cert_file"`
	TLSKeyFile                string `toml:"tls_key_file"`
	ProxyRateLimitPerMin      int    `toml:"proxy_rate_limit_per_min"` // 0 = disabled
}

type StorageConfig struct {
	Backend string     `toml:"backend"`
	Disk    DiskConfig `toml:"disk"`
	S3      S3Config   `toml:"s3"`
}

type DiskConfig struct {
	Path string `toml:"path"`
}

type S3Config struct {
	Bucket   string `toml:"bucket"`
	Region   string `toml:"region"`
	Endpoint string `toml:"endpoint"`
}

type PolicyConfig struct {
	Age        *AgePolicyConfig        `toml:"age"`
	OSV        *OSVPolicyConfig        `toml:"osv"`
	Publisher  *PublisherPolicyConfig  `toml:"publisher"`
	Popularity *PopularityPolicyConfig `toml:"popularity"`
	PyPI       *PyPIPolicyConfig       `toml:"pypi"`
}

type AgePolicyConfig struct {
	MinDays int    `toml:"min_days"`
	Action  string `toml:"action"`
}

type OSVPolicyConfig struct {
	MinSeverity string `toml:"min_severity"`
	Action      string `toml:"action"`
}

type PublisherPolicyConfig struct {
	MaxAccountAgeDays int    `toml:"max_account_age_days"`
	Action            string `toml:"action"`
}

type PopularityPolicyConfig struct {
	SpikeFactor float64 `toml:"spike_factor"`
	Action      string  `toml:"action"`
}

type PyPIPolicyConfig struct {
	BlockSdist bool `toml:"block_sdist"`
}

type EcosystemConfig struct {
	NPM              bool   `toml:"npm"`
	NPMUpstream      string `toml:"npm_upstream"`       // default https://registry.npmjs.org
	PyPI             bool   `toml:"pypi"`
	PyPIUpstream     string `toml:"pypi_upstream"`      // default https://pypi.org
	Go               bool   `toml:"go"`
	GoUpstream       string `toml:"go_upstream"`        // default https://proxy.golang.org
	Cargo            bool   `toml:"cargo"`
	Composer         bool   `toml:"composer"`
	ComposerUpstream string `toml:"composer_upstream"`  // default https://repo.packagist.org
	NuGet                   bool   `toml:"nuget"`
	NuGetUpstream           string `toml:"nuget_upstream"`            // default https://api.nuget.org/v3
	NuGetFlatcontainerURL   string `toml:"nuget_flatcontainer_url"`   // optional; derived from nuget_upstream if blank
	Maven                bool   `toml:"maven"`
	MavenUpstream        string `toml:"maven_upstream"`          // default https://repo1.maven.org/maven2
	MavenSnapshotUpstream string `toml:"maven_snapshot_upstream"` // default: same as MavenUpstream
}

func (e EcosystemConfig) EffectiveNPMUpstream() string {
	if e.NPMUpstream != "" {
		return e.NPMUpstream
	}
	return "https://registry.npmjs.org"
}

func (e EcosystemConfig) EffectivePyPIUpstream() string {
	if e.PyPIUpstream != "" {
		return e.PyPIUpstream
	}
	return "https://pypi.org"
}

func (e EcosystemConfig) EffectiveGoUpstream() string {
	if e.GoUpstream != "" {
		return e.GoUpstream
	}
	return "https://proxy.golang.org"
}

func (e EcosystemConfig) EffectiveComposerUpstream() string {
	if e.ComposerUpstream != "" {
		return e.ComposerUpstream
	}
	return "https://repo.packagist.org"
}

func (e EcosystemConfig) EffectiveNuGetUpstream() string {
	if e.NuGetUpstream != "" {
		return e.NuGetUpstream
	}
	return "https://api.nuget.org/v3"
}

func (e EcosystemConfig) EffectiveMavenUpstream() string {
	if e.MavenUpstream != "" {
		return e.MavenUpstream
	}
	return "https://repo1.maven.org/maven2"
}

// EffectiveMavenSnapshotUpstream returns the snapshot upstream URL.
// Falls back to the release upstream if no snapshot-specific URL is configured.
func (e EcosystemConfig) EffectiveMavenSnapshotUpstream() string {
	if e.MavenSnapshotUpstream != "" {
		return e.MavenSnapshotUpstream
	}
	return e.EffectiveMavenUpstream()
}

type AlertsConfig struct {
	WebhookURL string `toml:"webhook_url"`
}

type DashboardConfig struct {
	Enabled  bool   `toml:"enabled"`
	Path     string `toml:"path"`
	Username string `toml:"username"`
	Password string `toml:"password"`
	Secret   string `toml:"secret"`
}

func DefaultConfig() Config {
	return Config{
		Server:     ServerConfig{Host: "127.0.0.1", Port: 7888, LogLevel: "info"},
		Storage:    StorageConfig{Backend: "disk", Disk: DiskConfig{Path: "./escrow-cache"}},
		Ecosystems: EcosystemConfig{NPM: true, PyPI: true, Go: false, Cargo: false, Composer: false},
		Dashboard:  DashboardConfig{Enabled: true, Path: "/dashboard"},
	}
}

func Load(path string) (Config, error) {
	cfg := DefaultConfig()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return cfg, nil
	}
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	return cfg, nil
}

func GenerateIfMissing(path string) (bool, string, error) {
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		return false, "", nil
	}
	secret, err := randomHex(32)
	if err != nil {
		return false, "", fmt.Errorf("generate secret: %w", err)
	}
	password, err := randomAlpha(12)
	if err != nil {
		return false, "", fmt.Errorf("generate password: %w", err)
	}
	cfg := DefaultConfig()
	content := fmt.Sprintf(`# Generated by escrow on first boot.
# Use --host=0.0.0.0 or set host below to listen on all interfaces.

[server]
  host                     = %q
  port                     = %d
  log_level                = %q
  # write_timeout_seconds  = 120   # increase for slow clients downloading large archives
  # tls_cert_file          = ""
  # tls_key_file           = ""
  # proxy_rate_limit_per_min = 0   # requests/min per IP; 0 = disabled

[storage]
  backend = "disk"
  [storage.disk]
    path = "./escrow-cache"

[ecosystems]
  npm      = true
  pypi     = true
  go       = false
  cargo    = false
  composer = false
  nuget    = false
  maven    = false  # also covers Gradle via /maven2/

[dashboard]
  enabled  = true
  path     = "/dashboard"
  username = "admin"
  password = %q
  secret   = %q

[alerts]
  webhook_url = ""

allowlist_path = "escrow-allowlist.json"
blocklist_path = "escrow-blocklist.json"
# eventlog_path = "escrow-events.jsonl"  # persist events across restarts
`,
		cfg.Server.Host, cfg.Server.Port, cfg.Server.LogLevel,
		password, secret,
	)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return false, "", fmt.Errorf("write config: %w", err)
	}
	msg := fmt.Sprintf("Generated %s\n  username: admin\n  password: %s\n  url:      http://localhost:%d%s",
		path, password, cfg.Server.Port, cfg.Dashboard.Path)
	return true, msg, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", b), nil
}

func randomAlpha(n int) (string, error) {
	const chars = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b), nil
}

// Validate returns hard errors that must be fixed before escrow can start safely.
func (c Config) Validate() []error {
	var errs []error
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		errs = append(errs, fmt.Errorf("server.port %d is out of range 1–65535", c.Server.Port))
	}
	if c.Policy != nil && c.Policy.Age != nil && c.Policy.Age.MinDays < 0 {
		errs = append(errs, fmt.Errorf("policy.age.min_days %d is negative; negative values allow all packages through the age gate", c.Policy.Age.MinDays))
	}
	return errs
}

func (c Config) Warnings() []string {
	var w []string
	if c.Policy == nil {
		w = append(w, "no policy configured — escrow is proxying transparently without age gate, OSV scanning, or trust checks. Add a [policy] section to escrow.toml.")
	} else {
		noSignals := c.Policy.Age == nil && c.Policy.OSV == nil && c.Policy.Publisher == nil && c.Policy.Popularity == nil
		if noSignals {
			w = append(w, "[policy] section is present but contains no signals — add [policy.age], [policy.osv], etc. to enable filtering")
		}
	}
	noEcosystems := !c.Ecosystems.NPM && !c.Ecosystems.PyPI && !c.Ecosystems.Go &&
		!c.Ecosystems.Cargo && !c.Ecosystems.Composer && !c.Ecosystems.NuGet && !c.Ecosystems.Maven
	if noEcosystems {
		w = append(w, "no ecosystems are enabled — escrow is not proxying any packages. Enable at least one ecosystem in [ecosystems].")
	}
	if c.Storage.Backend == "memory" {
		w = append(w, "storage backend is 'memory' — blobs are written to OS temp dir and grow unboundedly for the process lifetime. Use 'disk' or 's3' for production deployments.")
	}
	if c.Storage.Backend == "disk" && c.Storage.Disk.Path == "" {
		w = append(w, "storage.disk.path is empty — using default path './escrow-cache'. Set an explicit path for production.")
	}
	if c.Alerts.WebhookURL != "" &&
		(strings.Contains(c.Alerts.WebhookURL, "localhost") || strings.Contains(c.Alerts.WebhookURL, "127.0.0.1")) {
		w = append(w, "alerts.webhook_url targets localhost — escrow will POST to itself on every block event, amplifying load. Use an external webhook receiver.")
	}
	if c.Server.TLSCertFile != "" {
		if _, err := os.Stat(c.Server.TLSCertFile); os.IsNotExist(err) {
			w = append(w, fmt.Sprintf("server.tls_cert_file %q does not exist — server will fail to start with TLS", c.Server.TLSCertFile))
		}
	}
	if c.Server.TLSKeyFile != "" {
		if _, err := os.Stat(c.Server.TLSKeyFile); os.IsNotExist(err) {
			w = append(w, fmt.Sprintf("server.tls_key_file %q does not exist — server will fail to start with TLS", c.Server.TLSKeyFile))
		}
	}
	if c.AllowlistPath != "" && c.BlocklistPath != "" && c.AllowlistPath == c.BlocklistPath {
		w = append(w, "allowlist_path and blocklist_path point to the same file — list mutations will overwrite each other")
	}
	if c.EventLogPath != "" && (c.EventLogPath == c.AllowlistPath || c.EventLogPath == c.BlocklistPath) {
		w = append(w, "eventlog_path is the same as allowlist_path or blocklist_path — JSONL appends will corrupt the list file")
	}
	if c.Dashboard.Enabled && c.Dashboard.Secret == "" {
		w = append(w, "dashboard.secret is empty — session cookies are signed with an empty key, making them forgeable. Set a random secret in escrow.toml.")
	}
	if c.Policy != nil && c.Policy.Age != nil && c.Policy.Age.MinDays == 0 {
		w = append(w, "policy.age.min_days is 0 — all packages pass the age gate regardless of publish time. Set min_days >= 1 for meaningful protection.")
	}
	return w
}
