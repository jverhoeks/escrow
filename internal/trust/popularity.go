package trust

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jverhoeks/escrow/internal/cache"
)

type PopularitySignal struct {
	spikeFactor float64
	client      *http.Client
	cache       cache.Cache
	npmBaseURL  string
	pypiBaseURL string
}

func NewPopularitySignal(spikeFactor float64, client *http.Client, c cache.Cache, npmBaseURL, pypiBaseURL string) *PopularitySignal {
	if npmBaseURL == "" {
		npmBaseURL = "https://api.npmjs.org"
	}
	if pypiBaseURL == "" {
		pypiBaseURL = "https://pypistats.org/api"
	}
	return &PopularitySignal{spikeFactor: spikeFactor, client: client, cache: c, npmBaseURL: npmBaseURL, pypiBaseURL: pypiBaseURL}
}

func (s *PopularitySignal) Name() string { return "popularity" }

func (s *PopularitySignal) Check(ctx context.Context, pkg Package) (SignalReport, error) {
	var currentDownloads int
	var fetchErr error
	switch pkg.Ecosystem {
	case EcosystemNPM:
		currentDownloads, fetchErr = s.fetchNPMDownloads(ctx, pkg.Name)
	case EcosystemPyPI:
		currentDownloads, fetchErr = s.fetchPyPIDownloads(ctx, pkg.Name)
	default:
		return SignalReport{Signal: s.Name(), Result: SignalSkip, Reason: "unsupported ecosystem"}, nil
	}
	if fetchErr != nil {
		return SignalReport{Signal: s.Name(), Result: SignalSkip, Reason: "could not fetch download stats"}, nil
	}

	baselineKey := fmt.Sprintf("pop/%s/%s/baseline", pkg.Ecosystem, pkg.Name)
	baselineData, _ := s.cache.GetMeta(ctx, baselineKey)

	newBaseline, _ := json.Marshal(map[string]int{"downloads": currentDownloads})
	s.cache.SetMeta(ctx, baselineKey, newBaseline, 7*24*time.Hour)

	if baselineData == nil {
		return SignalReport{Signal: s.Name(), Result: SignalSkip, Reason: "no baseline yet (stored for next check)"}, nil
	}
	var baseline struct {
		Downloads int `json:"downloads"`
	}
	if err := json.Unmarshal(baselineData, &baseline); err != nil || baseline.Downloads == 0 {
		return SignalReport{Signal: s.Name(), Result: SignalSkip, Reason: "no usable baseline"}, nil
	}
	ratio := float64(currentDownloads) / float64(baseline.Downloads)
	if ratio > s.spikeFactor && baseline.Downloads < 100 {
		return SignalReport{
			Signal: s.Name(),
			Result: SignalWarn,
			Reason: fmt.Sprintf("download spike: %.0fx increase (baseline: %d, current: %d)", ratio, baseline.Downloads, currentDownloads),
		}, nil
	}
	return SignalReport{Signal: s.Name(), Result: SignalPass, Reason: fmt.Sprintf("%d downloads/week", currentDownloads)}, nil
}

func (s *PopularitySignal) fetchNPMDownloads(ctx context.Context, name string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/downloads/point/last-week/%s", s.npmBaseURL, name), nil)
	if err != nil {
		return 0, fmt.Errorf("npm download stats unavailable")
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("npm download stats unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("npm download stats unavailable")
	}
	var data struct {
		Downloads int `json:"downloads"`
	}
	json.NewDecoder(resp.Body).Decode(&data)
	return data.Downloads, nil
}

func (s *PopularitySignal) fetchPyPIDownloads(ctx context.Context, name string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/packages/%s/recent", s.pypiBaseURL, name), nil)
	if err != nil {
		return 0, fmt.Errorf("PyPI download stats unavailable")
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("PyPI download stats unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("PyPI download stats unavailable")
	}
	var data struct {
		Data struct {
			LastWeek int `json:"last_week"`
		} `json:"data"`
	}
	json.NewDecoder(resp.Body).Decode(&data)
	return data.Data.LastWeek, nil
}
