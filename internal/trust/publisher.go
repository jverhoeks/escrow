package trust

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jverhoeks/escrow/internal/cache"
)

const publisherCacheTTL = 1 * time.Hour

type PublisherSignal struct {
	maxAccountAgeDays int
	client            *http.Client
	cache             cache.Cache
	npmBaseURL        string
	pypiBaseURL       string
}

func NewPublisherSignal(maxAccountAgeDays int, client *http.Client, c cache.Cache, npmBaseURL, pypiBaseURL string) *PublisherSignal {
	if npmBaseURL == "" {
		npmBaseURL = "https://registry.npmjs.org"
	}
	if pypiBaseURL == "" {
		pypiBaseURL = "https://pypi.org"
	}
	return &PublisherSignal{
		maxAccountAgeDays: maxAccountAgeDays,
		client:            client,
		cache:             c,
		npmBaseURL:        npmBaseURL,
		pypiBaseURL:       pypiBaseURL,
	}
}

func (s *PublisherSignal) Name() string { return "publisher" }

func (s *PublisherSignal) Check(ctx context.Context, pkg Package) (SignalReport, error) {
	switch pkg.Ecosystem {
	case EcosystemNPM:
		return s.checkNPM(ctx, pkg)
	case EcosystemPyPI:
		return s.checkPyPI(ctx, pkg)
	}
	return SignalReport{Signal: s.Name(), Result: SignalSkip, Reason: "unsupported ecosystem"}, nil
}

func (s *PublisherSignal) checkNPM(ctx context.Context, pkg Package) (SignalReport, error) {
	if pkg.Author == "" {
		return SignalReport{Signal: s.Name(), Result: SignalSkip, Reason: "no author info"}, nil
	}

	cacheKey := "publisher/npm/" + pkg.Author
	if s.cache != nil {
		if cached, _ := s.cache.GetMeta(ctx, cacheKey); cached != nil {
			var report SignalReport
			if json.Unmarshal(cached, &report) == nil {
				return report, nil
			}
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/-/user/org.couchdb.user/%s", s.npmBaseURL, pkg.Author), nil)
	if err != nil {
		return SignalReport{Signal: s.Name(), Result: SignalSkip, Reason: "could not fetch publisher info"}, nil
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return SignalReport{Signal: s.Name(), Result: SignalSkip, Reason: "could not fetch publisher info"}, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return SignalReport{Signal: s.Name(), Result: SignalSkip, Reason: "could not fetch publisher info"}, nil
	}
	var user struct {
		Created string `json:"created"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil || user.Created == "" {
		return SignalReport{Signal: s.Name(), Result: SignalSkip, Reason: "could not parse publisher info"}, nil
	}
	created, err := time.Parse(time.RFC3339, user.Created)
	if err != nil {
		return SignalReport{Signal: s.Name(), Result: SignalSkip, Reason: "could not parse created date"}, nil
	}
	ageDays := int(time.Since(created).Hours() / 24)
	if ageDays < s.maxAccountAgeDays {
		report := SignalReport{
			Signal: s.Name(),
			Result: SignalWarn,
			Reason: fmt.Sprintf("publisher account is %d day(s) old (threshold: %d)", ageDays, s.maxAccountAgeDays),
		}
		s.cacheReport(ctx, cacheKey, report)
		return report, nil
	}
	// Check if first-ever release
	pkgReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/%s", s.npmBaseURL, pkg.Name), nil)
	if err != nil {
		// Cannot verify first-ever release (invalid URL — unusual package name); skip signal.
		return SignalReport{Signal: s.Name(), Result: SignalSkip, Reason: "could not build package request"}, nil
	}
	pkgResp, err := s.client.Do(pkgReq)
	if err == nil {
		defer pkgResp.Body.Close() // close regardless of status to prevent body leak
		if pkgResp.StatusCode == http.StatusOK {
			var manifest struct {
				Versions map[string]any `json:"versions"`
			}
			if json.NewDecoder(pkgResp.Body).Decode(&manifest) == nil && len(manifest.Versions) == 1 {
				report := SignalReport{
					Signal: s.Name(),
					Result: SignalWarn,
					Reason: "first-ever release from this account",
				}
				s.cacheReport(ctx, cacheKey, report)
				return report, nil
			}
		}
	}
	report := SignalReport{Signal: s.Name(), Result: SignalPass, Reason: "established publisher"}
	s.cacheReport(ctx, cacheKey, report)
	return report, nil
}

func (s *PublisherSignal) checkPyPI(ctx context.Context, pkg Package) (SignalReport, error) {
	cacheKey := "publisher/pypi/" + pkg.Name
	if s.cache != nil {
		if cached, _ := s.cache.GetMeta(ctx, cacheKey); cached != nil {
			var report SignalReport
			if json.Unmarshal(cached, &report) == nil {
				return report, nil
			}
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/pypi/%s/json", s.pypiBaseURL, pkg.Name), nil)
	if err != nil {
		return SignalReport{Signal: s.Name(), Result: SignalSkip, Reason: "could not fetch PyPI metadata"}, nil
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return SignalReport{Signal: s.Name(), Result: SignalSkip, Reason: "could not fetch PyPI metadata"}, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return SignalReport{Signal: s.Name(), Result: SignalSkip, Reason: "could not fetch PyPI metadata"}, nil
	}
	var meta struct {
		Releases map[string]any `json:"releases"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return SignalReport{Signal: s.Name(), Result: SignalSkip, Reason: "could not parse PyPI metadata"}, nil
	}
	if len(meta.Releases) == 1 {
		report := SignalReport{Signal: s.Name(), Result: SignalWarn, Reason: "first-ever release on PyPI"}
		s.cacheReport(ctx, cacheKey, report)
		return report, nil
	}
	report := SignalReport{Signal: s.Name(), Result: SignalPass, Reason: "established package"}
	s.cacheReport(ctx, cacheKey, report)
	return report, nil
}

func (s *PublisherSignal) cacheReport(ctx context.Context, key string, r SignalReport) {
	if s.cache == nil {
		return
	}
	if data, err := json.Marshal(r); err == nil {
		s.cache.SetMeta(ctx, key, data, publisherCacheTTL)
	}
}
