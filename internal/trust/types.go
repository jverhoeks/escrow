package trust

import (
	"context"
	"time"
)

type Ecosystem string

const (
	EcosystemNPM      Ecosystem = "npm"
	EcosystemPyPI     Ecosystem = "pypi"
	EcosystemGo       Ecosystem = "go"
	EcosystemCargo    Ecosystem = "cargo"
	EcosystemComposer Ecosystem = "composer"
	EcosystemNuGet    Ecosystem = "nuget"
	EcosystemMaven    Ecosystem = "maven"
)

// Package is everything the trust engine needs to assess a specific release.
type Package struct {
	Ecosystem   Ecosystem
	Name        string
	Version     string
	PublishedAt time.Time
	Author      string // npm: first maintainer username; PyPI: author field
}

type SignalResult string

const (
	SignalPass  SignalResult = "pass"
	SignalFail  SignalResult = "fail"
	SignalWarn  SignalResult = "warn"
	SignalSkip  SignalResult = "skip"
	SignalError SignalResult = "error" // signal couldn't run (network/parse failure); policy decides fail-open vs fail-closed
)

// Vuln is a single vulnerability advisory matched against a package version.
type Vuln struct {
	ID       string `json:"id"`
	Severity string `json:"severity"` // "CRITICAL"|"HIGH"|"MEDIUM"|"LOW"|"" (unknown)
}

type SignalReport struct {
	Signal string
	Result SignalResult
	Reason string
	Vulns  []Vuln // populated by the OSV signal when Result == SignalFail
}

// TrustResult collects all signal reports for one package version.
type TrustResult struct {
	Package Package
	Reports []SignalReport
}

// Signal is the interface every trust check implements.
type Signal interface {
	Name() string
	Check(ctx context.Context, pkg Package) (SignalReport, error)
}
