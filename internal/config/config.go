package config

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Server        ServerConfig    `json:"server" toml:"server"`
	Storage       StorageConfig   `json:"storage" toml:"storage"`
	Policy        *PolicyConfig   `json:"policy" toml:"policy"`
	Ecosystems    EcosystemConfig `json:"ecosystems" toml:"ecosystems"`
	Alerts        AlertsConfig    `json:"alerts" toml:"alerts"`
	Dashboard     DashboardConfig `json:"dashboard" toml:"dashboard"`
	AllowlistPath string          `json:"allowlist_path" toml:"allowlist_path"`
	BlocklistPath string          `json:"blocklist_path" toml:"blocklist_path"`
	EventLogPath  string          `json:"eventlog_path" toml:"eventlog_path"` // JSONL append file; empty = in-memory only

	DownloadStatsPath string `json:"download_stats_path" toml:"download_stats_path"` // JSON; empty = default to cache dir on disk backend, else in-memory

	Rescan *RescanConfig `json:"rescan" toml:"rescan"`
}

type RescanConfig struct {
	// Enabled and AutoBlock are *bool so an omitted key keeps the default (true)
	// instead of the zero value (false): a partial [rescan] section that sets
	// only e.g. interval_hours must not silently disable the scanner or
	// auto-block. nil = default true.
	Enabled       *bool  `json:"enabled" toml:"enabled"`
	IntervalHours int    `json:"interval_hours" toml:"interval_hours"` // 0 → 24
	AutoBlock     *bool  `json:"auto_block" toml:"auto_block"`
	MinSeverity   string `json:"min_severity" toml:"min_severity"` // empty → inherit policy.osv.min_severity
}

type ServerConfig struct {
	Host                     string `json:"host" toml:"host"`
	Port                     int    `json:"port" toml:"port"`
	LogLevel                 string `json:"log_level" toml:"log_level"`
	WriteTimeoutSeconds      int    `json:"write_timeout_seconds" toml:"write_timeout_seconds"`             // 0 → default 120
	ReadHeaderTimeoutSeconds int    `json:"read_header_timeout_seconds" toml:"read_header_timeout_seconds"` // 0 → default 10
	IdleTimeoutSeconds       int    `json:"idle_timeout_seconds" toml:"idle_timeout_seconds"`               // 0 → default 120
	TLSCertFile              string `json:"tls_cert_file" toml:"tls_cert_file"`
	TLSKeyFile               string `json:"tls_key_file" toml:"tls_key_file"`
	ProxyRateLimitPerMin     int    `json:"proxy_rate_limit_per_min" toml:"proxy_rate_limit_per_min"` // 0 = disabled
	AccessLogPath            string `json:"access_log_path" toml:"access_log_path"`                   // Apache combined format; empty = disabled
	AccessLogMaxDays         int    `json:"access_log_max_days" toml:"access_log_max_days"`           // rotate+delete logs older than N days; 0 = 30
}

type StorageConfig struct {
	Backend string     `json:"backend" toml:"backend"`
	Disk    DiskConfig `json:"disk" toml:"disk"`
	S3      S3Config   `json:"s3" toml:"s3"`

	// StaleOnErrorMaxAgeM is the number of minutes to serve expired metadata
	// when upstream is unreachable; 0 = disabled. WARNING: serving stale
	// manifests can briefly re-expose a version escrow blocked by
	// manifest-removal.
	StaleOnErrorMaxAgeM int `json:"stale_on_error_max_age_m" toml:"stale_on_error_max_age_m"`
}

type DiskConfig struct {
	Path           string `json:"path" toml:"path"`
	MaxSizeGB      int    `json:"max_size_gb" toml:"max_size_gb"`           // 0 = unlimited
	PurgeIntervalM int    `json:"purge_interval_m" toml:"purge_interval_m"` // minutes between FIFO purge runs; 0 = 60
}

type S3Config struct {
	Bucket   string `json:"bucket" toml:"bucket"`
	Region   string `json:"region" toml:"region"`
	Endpoint string `json:"endpoint" toml:"endpoint"`
}

type PolicyConfig struct {
	Age        *AgePolicyConfig        `json:"age" toml:"age"`
	OSV        *OSVPolicyConfig        `json:"osv" toml:"osv"`
	Publisher  *PublisherPolicyConfig  `json:"publisher" toml:"publisher"`
	Popularity *PopularityPolicyConfig `json:"popularity" toml:"popularity"`
	PyPI       *PyPIPolicyConfig       `json:"pypi" toml:"pypi"`

	// StrictSignals controls fail-open vs fail-closed behavior for transient
	// signal failures (network errors, panics, parse failures the signal
	// itself didn't handle). Valid values: "allow" (default — fail open;
	// transient errors don't block), "warn" (log + emit warn decision), or
	// "block" (fail closed; refuse to install if a signal couldn't run).
	StrictSignals string `json:"strict_signals" toml:"strict_signals"`
}

type AgePolicyConfig struct {
	MinDays int    `json:"min_days" toml:"min_days"`
	Action  string `json:"action" toml:"action"`
}

type OSVPolicyConfig struct {
	MinSeverity string `json:"min_severity" toml:"min_severity"`
	Action      string `json:"action" toml:"action"`
}

type PublisherPolicyConfig struct {
	MaxAccountAgeDays int    `json:"max_account_age_days" toml:"max_account_age_days"`
	Action            string `json:"action" toml:"action"`
}

type PopularityPolicyConfig struct {
	SpikeFactor float64 `json:"spike_factor" toml:"spike_factor"`
	Action      string  `json:"action" toml:"action"`
}

type PyPIPolicyConfig struct {
	BlockSdist bool `json:"block_sdist" toml:"block_sdist"`
}

type EcosystemConfig struct {
	NPM                   bool   `json:"npm" toml:"npm"`
	NPMUpstream           string `json:"npm_upstream" toml:"npm_upstream"` // default https://registry.npmjs.org
	PyPI                  bool   `json:"pypi" toml:"pypi"`
	PyPIUpstream          string `json:"pypi_upstream" toml:"pypi_upstream"` // default https://pypi.org
	Go                    bool   `json:"go" toml:"go"`
	GoUpstream            string `json:"go_upstream" toml:"go_upstream"` // default https://proxy.golang.org
	Cargo                 bool   `json:"cargo" toml:"cargo"`
	Composer              bool   `json:"composer" toml:"composer"`
	ComposerUpstream      string `json:"composer_upstream" toml:"composer_upstream"` // default https://repo.packagist.org
	NuGet                 bool   `json:"nuget" toml:"nuget"`
	NuGetUpstream         string `json:"nuget_upstream" toml:"nuget_upstream"`                   // default https://api.nuget.org/v3
	NuGetFlatcontainerURL string `json:"nuget_flatcontainer_url" toml:"nuget_flatcontainer_url"` // optional; derived from nuget_upstream if blank
	Maven                 bool   `json:"maven" toml:"maven"`
	MavenUpstream         string `json:"maven_upstream" toml:"maven_upstream"`                   // default https://repo1.maven.org/maven2
	MavenSnapshotUpstream string `json:"maven_snapshot_upstream" toml:"maven_snapshot_upstream"` // default: same as MavenUpstream
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
	WebhookURL string `json:"webhook_url" toml:"webhook_url"`
}

type DashboardConfig struct {
	Enabled  bool   `json:"enabled" toml:"enabled"`
	Path     string `json:"path" toml:"path"`
	Username string `json:"username" toml:"username"`
	Password string `json:"password" toml:"password"`
	Secret   string `json:"secret" toml:"secret"`
}

func DefaultConfig() Config {
	return Config{
		Server:     ServerConfig{Host: "127.0.0.1", Port: 7888, LogLevel: "info"},
		Storage:    StorageConfig{Backend: "disk", Disk: DiskConfig{Path: "~/.cache/escrow", MaxSizeGB: 10, PurgeIntervalM: 60}},
		Ecosystems: EcosystemConfig{NPM: true, PyPI: true, Go: true, Cargo: true, Composer: true, NuGet: true, Maven: true},
		Dashboard:  DashboardConfig{Enabled: true, Path: "/dashboard"},
	}
}

// ExpandPath expands a leading ~ to the user home directory.
func ExpandPath(p string) string {
	if !strings.HasPrefix(p, "~/") && p != "~" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[1:])
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
  # access_log_path        = "~/.cache/escrow/access.log"  # Apache combined format; empty = disabled
  # access_log_max_days    = 30    # delete rotated logs older than N days

[storage]
  backend = "disk"
  [storage.disk]
    path            = "~/.cache/escrow"
    max_size_gb     = 10   # FIFO evicts oldest blobs when this limit is reached
    purge_interval_m = 60  # how often the eviction sweep runs (minutes)

[ecosystems]
  npm      = true
  pypi     = true
  go       = true
  cargo    = true
  composer = true
  nuget    = true
  maven    = true   # also covers Gradle via /maven2/

[dashboard]
  enabled  = true
  path     = "/dashboard"
  username = "admin"
  password = %q
  secret   = %q

[alerts]
  webhook_url = ""

allowlist_path = "~/.cache/escrow/allowlist.json"
blocklist_path = "~/.cache/escrow/blocklist.json"
# eventlog_path = "~/.cache/escrow/events.jsonl"  # persist events across restarts
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
	if c.Storage.StaleOnErrorMaxAgeM < 0 {
		errs = append(errs, fmt.Errorf("storage.stale_on_error_max_age_m %d is negative; use 0 to disable", c.Storage.StaleOnErrorMaxAgeM))
	}
	if c.Policy != nil && c.Policy.Age != nil && c.Policy.Age.MinDays < 0 {
		errs = append(errs, fmt.Errorf("policy.age.min_days %d is negative; negative values allow all packages through the age gate", c.Policy.Age.MinDays))
	}
	if c.Policy != nil {
		switch c.Policy.StrictSignals {
		case "", "allow", "warn", "block":
		default:
			errs = append(errs, fmt.Errorf("policy.strict_signals %q is not one of allow/warn/block", c.Policy.StrictSignals))
		}
		actions := map[string]string{}
		if c.Policy.Age != nil {
			actions["age"] = c.Policy.Age.Action
		}
		if c.Policy.OSV != nil {
			actions["osv"] = c.Policy.OSV.Action
			if !validSeverities[c.Policy.OSV.MinSeverity] {
				errs = append(errs, fmt.Errorf("policy.osv.min_severity %q is not one of CRITICAL/HIGH/MEDIUM/LOW", c.Policy.OSV.MinSeverity))
			}
		}
		if c.Policy.Publisher != nil {
			actions["publisher"] = c.Policy.Publisher.Action
		}
		if c.Policy.Popularity != nil {
			actions["popularity"] = c.Policy.Popularity.Action
		}
		for sig, a := range actions {
			if !validActions[a] {
				errs = append(errs, fmt.Errorf("policy.%s.action %q is not one of allow/warn/block", sig, a))
			}
		}
	}
	if c.Rescan != nil && !validSeverities[c.Rescan.MinSeverity] {
		errs = append(errs, fmt.Errorf("rescan.min_severity %q is not one of CRITICAL/HIGH/MEDIUM/LOW", c.Rescan.MinSeverity))
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
