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
type osvResponse struct {
	Vulns []struct {
		ID       string `json:"id"`
		Severity []struct {
			Type  string `json:"type"`
			Score string `json:"score"`
		} `json:"severity"`
		DatabaseSpecific *struct {
			Severity string `json:"severity"` // "LOW", "MEDIUM", "HIGH", "CRITICAL"
		} `json:"database_specific"`
	} `json:"vulns"`
}

var severityRank = map[string]int{
	"CRITICAL": 4, "HIGH": 3, "MEDIUM": 2, "LOW": 1,
}

func (s *OSVSignal) Check(ctx context.Context, pkg Package) (SignalReport, error) {
	cacheKey := fmt.Sprintf("osv/%s/%s/%s", pkg.Ecosystem, pkg.Name, pkg.Version)
	if cached, _ := s.cache.GetMeta(ctx, cacheKey); cached != nil {
		var resp osvResponse
		if json.Unmarshal(cached, &resp) == nil {
			return s.toReport(resp), nil
		}
	}

	ecosystem := "npm"
	switch pkg.Ecosystem {
	case EcosystemPyPI:
		ecosystem = "PyPI"
	case EcosystemGo:
		ecosystem = "Go"
	}
	body, _ := json.Marshal(osvQuery{
		Version: pkg.Version,
		Package: osvPackage{Name: pkg.Name, Ecosystem: ecosystem},
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/v1/query", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return SignalReport{Signal: s.Name(), Result: SignalSkip, Reason: "OSV query failed"}, nil
	}
	defer resp.Body.Close()

	var osvResp osvResponse
	if err := json.NewDecoder(resp.Body).Decode(&osvResp); err != nil {
		return SignalReport{Signal: s.Name(), Result: SignalSkip, Reason: "failed to decode OSV response"}, nil
	}

	encoded, _ := json.Marshal(osvResp)
	s.cache.SetMeta(ctx, cacheKey, encoded, 24*time.Hour)
	return s.toReport(osvResp), nil
}

func (s *OSVSignal) toReport(resp osvResponse) SignalReport {
	minRank := severityRank[s.minSeverity]
	var matchingIDs []string
	for _, v := range resp.Vulns {
		sev := ""
		if v.DatabaseSpecific != nil && v.DatabaseSpecific.Severity != "" {
			sev = strings.ToUpper(v.DatabaseSpecific.Severity)
		}
		// If severity is unknown or at/above threshold, include it
		rank, known := severityRank[sev]
		if !known || rank >= minRank {
			matchingIDs = append(matchingIDs, v.ID)
		}
	}
	if len(matchingIDs) == 0 {
		return SignalReport{Signal: s.Name(), Result: SignalPass, Reason: "no known vulnerabilities at or above " + s.minSeverity}
	}
	limit := 3
	if len(matchingIDs) < limit {
		limit = len(matchingIDs)
	}
	return SignalReport{
		Signal: s.Name(),
		Result: SignalFail,
		Reason: fmt.Sprintf("%d vulnerability/vulnerabilities at or above %s: %s",
			len(matchingIDs), s.minSeverity, strings.Join(matchingIDs[:limit], ", ")),
	}
}
