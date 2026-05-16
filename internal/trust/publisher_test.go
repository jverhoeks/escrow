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
	sig := trust.NewPublisherSignal(30, srv.Client(), srv.URL, "")
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
	sig := trust.NewPublisherSignal(30, srv.Client(), srv.URL, "")
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
