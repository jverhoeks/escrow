package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Server     ServerConfig    `toml:"server"`
	Storage    StorageConfig   `toml:"storage"`
	Policy     *PolicyConfig   `toml:"policy"`
	Ecosystems EcosystemConfig `toml:"ecosystems"`
	Alerts     AlertsConfig    `toml:"alerts"`
}

type ServerConfig struct {
	Host     string `toml:"host"`
	Port     int    `toml:"port"`
	LogLevel string `toml:"log_level"`
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
	NPM  bool `toml:"npm"`
	PyPI bool `toml:"pypi"`
}

type AlertsConfig struct {
	WebhookURL string `toml:"webhook_url"`
}

func DefaultConfig() Config {
	return Config{
		Server:     ServerConfig{Host: "0.0.0.0", Port: 8888, LogLevel: "info"},
		Storage:    StorageConfig{Backend: "disk", Disk: DiskConfig{Path: "./sentinel-cache"}},
		Ecosystems: EcosystemConfig{NPM: true, PyPI: true},
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

func (c Config) Warnings() []string {
	var w []string
	if c.Policy == nil {
		w = append(w, "no policy configured — sentinel is proxying without age gate, OSV scanning, or trust checks. Add a [policy] section to sentinel.toml to enable protection.")
	}
	if c.Storage.Backend == "memory" {
		w = append(w, "storage backend is 'memory' — no packages will be cached between restarts.")
	}
	return w
}
