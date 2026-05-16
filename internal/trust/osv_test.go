package trust_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/cache"
	"github.com/jverhoeks/escrow/internal/trust"
)

func TestOSVSignal_VulnerabilityFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/query", r.URL.Path)
		json.NewEncoder(w).Encode(map[string]any{
			"vulns": []map[string]any{{"id": "GHSA-xxxx-yyyy-zzzz"}},
		})
	}))
	defer srv.Close()
	c := cache.NewMemory()
	defer c.Close()
	sig := trust.NewOSVSignal("MEDIUM", srv.Client(), c, srv.URL)
	pkg := trust.Package{Ecosystem: trust.EcosystemNPM, Name: "vulnerable-pkg", Version: "1.0.0"}
	report, err := sig.Check(context.Background(), pkg)
	require.NoError(t, err)
	assert.Equal(t, trust.SignalFail, report.Result)
	assert.Contains(t, report.Reason, "GHSA-xxxx-yyyy-zzzz")
}

func TestOSVSignal_Clean(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"vulns": []any{}})
	}))
	defer srv.Close()
	c := cache.NewMemory()
	defer c.Close()
	sig := trust.NewOSVSignal("MEDIUM", srv.Client(), c, srv.URL)
	pkg := trust.Package{Ecosystem: trust.EcosystemNPM, Name: "safe-pkg", Version: "2.0.0"}
	report, err := sig.Check(context.Background(), pkg)
	require.NoError(t, err)
	assert.Equal(t, trust.SignalPass, report.Result)
}

func TestOSVSignal_CachesResult(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(map[string]any{"vulns": []any{}})
	}))
	defer srv.Close()
	c := cache.NewMemory()
	defer c.Close()
	sig := trust.NewOSVSignal("MEDIUM", srv.Client(), c, srv.URL)
	pkg := trust.Package{Ecosystem: trust.EcosystemNPM, Name: "cached-pkg", Version: "1.0.0"}
	sig.Check(context.Background(), pkg)
	sig.Check(context.Background(), pkg)
	assert.Equal(t, 1, calls, "second check should use cache")
}

func TestOSVSignal_UpstreamError_Skips(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	c := cache.NewMemory()
	defer c.Close()
	sig := trust.NewOSVSignal("MEDIUM", srv.Client(), c, srv.URL)
	pkg := trust.Package{Ecosystem: trust.EcosystemNPM, Name: "errored-pkg", Version: "1.0.0"}
	report, err := sig.Check(context.Background(), pkg)
	require.NoError(t, err)
	assert.Equal(t, trust.SignalSkip, report.Result)
}

func TestOSVSignal_LowSeverityFiltered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"vulns": []map[string]any{
				{
					"id": "GHSA-low-severity",
					"database_specific": map[string]any{"severity": "LOW"},
				},
			},
		})
	}))
	defer srv.Close()
	c := cache.NewMemory()
	defer c.Close()
	sig := trust.NewOSVSignal("MEDIUM", srv.Client(), c, srv.URL)
	pkg := trust.Package{Ecosystem: trust.EcosystemNPM, Name: "low-sev-pkg", Version: "1.0.0"}
	report, err := sig.Check(context.Background(), pkg)
	require.NoError(t, err)
	assert.Equal(t, trust.SignalPass, report.Result, "LOW severity should be filtered when minSeverity=MEDIUM")
}
