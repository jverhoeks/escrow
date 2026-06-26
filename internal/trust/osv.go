package trust

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jverhoeks/escrow/internal/cache"
	"github.com/jverhoeks/escrow/internal/metrics"
)

type OSVSignal struct {
	minSeverity string
	client      *http.Client
	cache       cache.Cache
	baseURL     string
}

func NewOSVSignal(minSeverity string, client *http.Client, c cache.Cache, baseURL string) *OSVSignal {
	if baseURL == "" {
		baseURL = "https://api.osv.dev"
	}
	return &OSVSignal{minSeverity: strings.ToUpper(minSeverity), client: client, cache: c, baseURL: baseURL}
}

func (s *OSVSignal) Name() string { return "osv" }

type osvQuery struct {
	Version string     `json:"version"`
	Package osvPackage `json:"package"`
}
type osvPackage struct {
	Name      string `json:"name"`
	Ecosystem string `json:"ecosystem"`
}
type osvSeverity struct {
	Type  string `json:"type"`  // e.g. "CVSS_V3", "CVSS_V4"
	Score string `json:"score"` // CVSS vector string
}
type osvResponse struct {
	Vulns []struct {
		ID               string        `json:"id"`
		Severity         []osvSeverity `json:"severity"`
		DatabaseSpecific *struct {
			Severity string `json:"severity"` // "LOW", "MEDIUM", "HIGH", "CRITICAL"
		} `json:"database_specific"`
	} `json:"vulns"`
}

// severityFromCVSS derives a qualitative band from the highest-confidence CVSS
// vector available (preferring v3, which we can score). Returns "" when no
// scoreable vector is present.
func severityFromCVSS(sevs []osvSeverity) string {
	for _, s := range sevs {
		if s.Type == "CVSS_V3" {
			if score, ok := cvssBaseScore(s.Score); ok {
				return severityBandFromScore(score)
			}
		}
	}
	return ""
}

// severityRank ranks OSV/GHSA severities. GitHub advisories use "MODERATE"
// where the CVSS scale says "MEDIUM" — treat them as equivalent.
var severityRank = map[string]int{
	"CRITICAL": 4, "HIGH": 3, "MEDIUM": 2, "MODERATE": 2, "LOW": 1,
}

// Check returns the cached OSV result if present, else queries upstream.
func (s *OSVSignal) Check(ctx context.Context, pkg Package) (SignalReport, error) {
	cacheKey := fmt.Sprintf("osv/%s/%s/%s", pkg.Ecosystem, pkg.Name, pkg.Version)
	if cached, _ := s.cache.GetMeta(ctx, cacheKey); cached != nil {
		var resp osvResponse
		if json.Unmarshal(cached, &resp) == nil {
			return s.toReport(resp), nil
		}
	}
	return s.query(ctx, pkg, cacheKey)
}

// CheckFresh always queries upstream (ignoring any cached result) and refreshes
// the cache. Used by the background re-scanner so newly-published CVEs are seen.
func (s *OSVSignal) CheckFresh(ctx context.Context, pkg Package) (SignalReport, error) {
	cacheKey := fmt.Sprintf("osv/%s/%s/%s", pkg.Ecosystem, pkg.Name, pkg.Version)
	return s.query(ctx, pkg, cacheKey)
}

// query performs the upstream OSV request, caches the response, and reports.
func (s *OSVSignal) query(ctx context.Context, pkg Package, cacheKey string) (SignalReport, error) {
	// Map escrow ecosystem names to OSV database ecosystem identifiers.
	// https://osv.dev/docs/#tag/api/operation/OSV_QueryAffected
	ecosystem := "npm"
	switch pkg.Ecosystem {
	case EcosystemPyPI:
		ecosystem = "PyPI"
	case EcosystemGo:
		ecosystem = "Go"
	case EcosystemCargo:
		ecosystem = "crates.io"
	case EcosystemComposer:
		ecosystem = "Packagist"
	case EcosystemNuGet:
		ecosystem = "NuGet"
	case EcosystemMaven:
		ecosystem = "Maven"
	}
	body, _ := json.Marshal(osvQuery{
		Version: pkg.Version,
		Package: osvPackage{Name: pkg.Name, Ecosystem: ecosystem},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/v1/query", bytes.NewReader(body))
	if err != nil {
		return SignalReport{Signal: s.Name(), Result: SignalError, Reason: "OSV request build failed"}, nil
	}
	req.Header.Set("Content-Type", "application/json")

	t0 := time.Now()
	resp, err := s.client.Do(req)
	metrics.OSVQueryDuration.Observe(time.Since(t0).Seconds())
	if err != nil {
		return SignalReport{Signal: s.Name(), Result: SignalError, Reason: "OSV query failed"}, nil
	}
	defer resp.Body.Close() // must come before status check to avoid body leak on non-200
	if resp.StatusCode != http.StatusOK {
		return SignalReport{Signal: s.Name(), Result: SignalError, Reason: "OSV query failed"}, nil
	}

	var osvResp osvResponse
	if err := json.NewDecoder(resp.Body).Decode(&osvResp); err != nil {
		return SignalReport{Signal: s.Name(), Result: SignalError, Reason: "failed to decode OSV response"}, nil
	}

	encoded, _ := json.Marshal(osvResp)
	s.cache.SetMeta(ctx, cacheKey, encoded, 24*time.Hour)
	return s.toReport(osvResp), nil
}

func (s *OSVSignal) toReport(resp osvResponse) SignalReport {
	minRank := severityRank[s.minSeverity]
	var matching []Vuln
	for _, v := range resp.Vulns {
		sev := ""
		if v.DatabaseSpecific != nil && v.DatabaseSpecific.Severity != "" {
			sev = strings.ToUpper(v.DatabaseSpecific.Severity)
		} else {
			// No database_specific.severity (common for non-GHSA ecosystems:
			// PYSEC, Go vuln DB, RUSTSEC). Derive the band from a CVSS v3 vector
			// in the severity[] array so min_severity is honored. v4/v2 vectors
			// and advisories with no scoreable vector stay "" (unknown) and are
			// included below — fail closed.
			sev = severityFromCVSS(v.Severity)
		}
		// If severity is unknown or at/above threshold, include it
		rank, known := severityRank[sev]
		if !known || rank >= minRank {
			matching = append(matching, Vuln{ID: v.ID, Severity: sev})
		}
	}
	if len(matching) == 0 {
		return SignalReport{Signal: s.Name(), Result: SignalPass, Reason: "no known vulnerabilities at or above " + s.minSeverity}
	}
	ids := make([]string, 0, len(matching))
	for _, v := range matching {
		ids = append(ids, v.ID)
	}
	limit := 3
	if len(ids) < limit {
		limit = len(ids)
	}
	return SignalReport{
		Signal: s.Name(),
		Result: SignalFail,
		Reason: fmt.Sprintf("%d vulnerability/vulnerabilities at or above %s: %s",
			len(ids), s.minSeverity, strings.Join(ids[:limit], ", ")),
		Vulns: matching,
	}
}
