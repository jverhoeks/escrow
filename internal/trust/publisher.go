package trust

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type PublisherSignal struct {
	maxAccountAgeDays int
	client            *http.Client
	npmBaseURL        string
	pypiBaseURL       string
}

func NewPublisherSignal(maxAccountAgeDays int, client *http.Client, npmBaseURL, pypiBaseURL string) *PublisherSignal {
	if npmBaseURL == "" {
		npmBaseURL = "https://registry.npmjs.org"
	}
	if pypiBaseURL == "" {
		pypiBaseURL = "https://pypi.org"
	}
	return &PublisherSignal{
		maxAccountAgeDays: maxAccountAgeDays,
		client:            client,
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
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/-/user/org.couchdb.user/%s", s.npmBaseURL, pkg.Author), nil)
	resp, err := s.client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return SignalReport{Signal: s.Name(), Result: SignalSkip, Reason: "could not fetch publisher info"}, nil
	}
	defer resp.Body.Close()
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
		return SignalReport{
			Signal: s.Name(),
			Result: SignalWarn,
			Reason: fmt.Sprintf("publisher account is %d day(s) old (threshold: %d)", ageDays, s.maxAccountAgeDays),
		}, nil
	}
	// Check if first-ever release
	pkgReq, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/%s", s.npmBaseURL, pkg.Name), nil)
	pkgResp, err := s.client.Do(pkgReq)
	if err == nil && pkgResp.StatusCode == http.StatusOK {
		defer pkgResp.Body.Close()
		var manifest struct {
			Versions map[string]any `json:"versions"`
		}
		if json.NewDecoder(pkgResp.Body).Decode(&manifest) == nil && len(manifest.Versions) == 1 {
			return SignalReport{
				Signal: s.Name(),
				Result: SignalWarn,
				Reason: "first-ever release from this account",
			}, nil
		}
	}
	return SignalReport{Signal: s.Name(), Result: SignalPass, Reason: "established publisher"}, nil
}

func (s *PublisherSignal) checkPyPI(ctx context.Context, pkg Package) (SignalReport, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/pypi/%s/json", s.pypiBaseURL, pkg.Name), nil)
	resp, err := s.client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return SignalReport{Signal: s.Name(), Result: SignalSkip, Reason: "could not fetch PyPI metadata"}, nil
	}
	defer resp.Body.Close()
	var meta struct {
		Releases map[string]any `json:"releases"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return SignalReport{Signal: s.Name(), Result: SignalSkip, Reason: "could not parse PyPI metadata"}, nil
	}
	if len(meta.Releases) == 1 {
		return SignalReport{Signal: s.Name(), Result: SignalWarn, Reason: "first-ever release on PyPI"}, nil
	}
	return SignalReport{Signal: s.Name(), Result: SignalPass, Reason: "established package"}, nil
}
