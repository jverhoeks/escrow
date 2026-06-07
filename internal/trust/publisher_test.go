package trust_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/trust"
)

func TestPublisherSignal_NewAccount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/-/user/") {
			json.NewEncoder(w).Encode(map[string]any{
				"created": time.Now().Add(-10 * 24 * time.Hour).Format(time.RFC3339),
			})
			return
		}
		// package has only 1 version (not first-ever, so only account age triggers)
		json.NewEncoder(w).Encode(map[string]any{
			"versions": map[string]any{
				"1.0.0": map[string]any{},
				"2.0.0": map[string]any{},
			},
		})
	}))
	defer srv.Close()
	sig := trust.NewPublisherSignal(30, srv.Client(), nil, srv.URL, "")
	pkg := trust.Package{
		Ecosystem: trust.EcosystemNPM,
		Name:      "new-pkg",
		Version:   "1.0.0",
		Author:    "newbie",
	}
	report, err := sig.Check(context.Background(), pkg)
	require.NoError(t, err)
	assert.Equal(t, trust.SignalWarn, report.Result)
	assert.Contains(t, report.Reason, "account")
}

func TestPublisherSignal_EstablishedAccount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/-/user/") {
			json.NewEncoder(w).Encode(map[string]any{
				"created": time.Now().Add(-365 * 24 * time.Hour).Format(time.RFC3339),
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"versions": map[string]any{
				"1.0.0": map[string]any{},
				"2.0.0": map[string]any{},
			},
		})
	}))
	defer srv.Close()
	sig := trust.NewPublisherSignal(30, srv.Client(), nil, srv.URL, "")
	pkg := trust.Package{
		Ecosystem: trust.EcosystemNPM,
		Name:      "established-pkg",
		Version:   "2.0.0",
		Author:    "veteran",
	}
	report, err := sig.Check(context.Background(), pkg)
	require.NoError(t, err)
	assert.Equal(t, trust.SignalPass, report.Result)
}

// TestPublisherSignal_UnsupportedEcosystem_Skips verifies that an ecosystem with
// no publisher API (e.g. Go) is not-applicable and returns SignalSkip — NOT
// SignalError. A not-applicable signal must never block, even under
// strict_signals=block.
func TestPublisherSignal_UnsupportedEcosystem_Skips(t *testing.T) {
	sig := trust.NewPublisherSignal(30, http.DefaultClient, nil, "", "")
	pkg := trust.Package{
		Ecosystem: trust.EcosystemGo,
		Name:      "github.com/foo/bar",
		Version:   "1.0.0",
	}
	report, err := sig.Check(context.Background(), pkg)
	require.NoError(t, err)
	assert.Equal(t, trust.SignalSkip, report.Result,
		"unsupported ecosystem is not-applicable and must stay SignalSkip")
}

// TestPublisherSignal_UpstreamError_Errors verifies that a transient upstream
// failure (HTTP 500 fetching publisher info) surfaces as SignalError so
// strict_signals can decide fail-open vs fail-closed.
func TestPublisherSignal_UpstreamError_Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	sig := trust.NewPublisherSignal(30, srv.Client(), nil, srv.URL, "")
	pkg := trust.Package{
		Ecosystem: trust.EcosystemNPM,
		Name:      "some-pkg",
		Version:   "1.0.0",
		Author:    "someone",
	}
	report, err := sig.Check(context.Background(), pkg)
	require.NoError(t, err)
	assert.Equal(t, trust.SignalError, report.Result,
		"transient publisher fetch failure should surface as SignalError")
}

// TestPublisherSignal_ManifestFetchError_Errors verifies that an established
// npm account whose first-release MANIFEST request fails transiently (HTTP 500)
// surfaces as SignalError — not a silent SignalPass "established publisher".
// Previously the second-request Do-error/non-200/decode-failure all fell through
// to SignalPass, masking a configured publisher.action=block on a transient
// failure.
func TestPublisherSignal_ManifestFetchError_Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Account endpoint: established account (old created date).
		if strings.Contains(r.URL.Path, "/-/user/") {
			json.NewEncoder(w).Encode(map[string]any{
				"created": time.Now().Add(-365 * 24 * time.Hour).Format(time.RFC3339),
			})
			return
		}
		// Package-manifest endpoint: transient failure.
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	sig := trust.NewPublisherSignal(30, srv.Client(), nil, srv.URL, "")
	pkg := trust.Package{
		Ecosystem: trust.EcosystemNPM,
		Name:      "established-pkg",
		Version:   "2.0.0",
		Author:    "veteran",
	}
	report, err := sig.Check(context.Background(), pkg)
	require.NoError(t, err)
	assert.Equal(t, trust.SignalError, report.Result,
		"manifest fetch failure must surface as SignalError, not silent established-publisher PASS")
}
