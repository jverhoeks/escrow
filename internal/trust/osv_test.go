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

func TestOSVSignal_CargoEcosystem(t *testing.T) {
	var capturedEcosystem string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Package struct {
				Ecosystem string `json:"ecosystem"`
			} `json:"package"`
		}
		json.NewDecoder(r.Body).Decode(&q)
		capturedEcosystem = q.Package.Ecosystem
		json.NewEncoder(w).Encode(map[string]any{"vulns": []any{}})
	}))
	defer srv.Close()
	c := cache.NewMemory()
	defer c.Close()
	sig := trust.NewOSVSignal("MEDIUM", srv.Client(), c, srv.URL)
	pkg := trust.Package{Ecosystem: trust.EcosystemCargo, Name: "serde", Version: "1.0.0"}
	_, err := sig.Check(context.Background(), pkg)
	require.NoError(t, err)
	assert.Equal(t, "crates.io", capturedEcosystem, "cargo ecosystem should map to crates.io in OSV query")
}

func TestOSVSignal_ComposerEcosystem(t *testing.T) {
	var capturedEcosystem string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Package struct {
				Ecosystem string `json:"ecosystem"`
			} `json:"package"`
		}
		json.NewDecoder(r.Body).Decode(&q)
		capturedEcosystem = q.Package.Ecosystem
		json.NewEncoder(w).Encode(map[string]any{"vulns": []any{}})
	}))
	defer srv.Close()
	c := cache.NewMemory()
	defer c.Close()
	sig := trust.NewOSVSignal("MEDIUM", srv.Client(), c, srv.URL)
	pkg := trust.Package{Ecosystem: trust.EcosystemComposer, Name: "symfony/console", Version: "6.0.0"}
	_, err := sig.Check(context.Background(), pkg)
	require.NoError(t, err)
	assert.Equal(t, "Packagist", capturedEcosystem, "composer ecosystem should map to Packagist in OSV query")
}

func TestOSVSignal_NuGetEcosystem(t *testing.T) {
	var capturedEcosystem string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Package struct {
				Ecosystem string `json:"ecosystem"`
			} `json:"package"`
		}
		json.NewDecoder(r.Body).Decode(&q)
		capturedEcosystem = q.Package.Ecosystem
		json.NewEncoder(w).Encode(map[string]any{"vulns": []any{}})
	}))
	defer srv.Close()
	c := cache.NewMemory()
	defer c.Close()
	sig := trust.NewOSVSignal("MEDIUM", srv.Client(), c, srv.URL)
	pkg := trust.Package{Ecosystem: trust.EcosystemNuGet, Name: "Newtonsoft.Json", Version: "13.0.3"}
	_, err := sig.Check(context.Background(), pkg)
	require.NoError(t, err)
	assert.Equal(t, "NuGet", capturedEcosystem, "nuget ecosystem should map to NuGet in OSV query")
}

func TestOSVSignal_MavenEcosystem(t *testing.T) {
	var capturedEcosystem string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Package struct {
				Ecosystem string `json:"ecosystem"`
			} `json:"package"`
		}
		json.NewDecoder(r.Body).Decode(&q)
		capturedEcosystem = q.Package.Ecosystem
		json.NewEncoder(w).Encode(map[string]any{"vulns": []any{}})
	}))
	defer srv.Close()
	c := cache.NewMemory()
	defer c.Close()
	sig := trust.NewOSVSignal("MEDIUM", srv.Client(), c, srv.URL)
	pkg := trust.Package{Ecosystem: trust.EcosystemMaven, Name: "org.springframework:spring-core", Version: "6.1.0"}
	_, err := sig.Check(context.Background(), pkg)
	require.NoError(t, err)
	assert.Equal(t, "Maven", capturedEcosystem, "maven ecosystem should map to Maven in OSV query")
}

// TestOSVSignal_NuGetPreservesCase verifies that the OSV query sends the canonical
// package name case (e.g. "Newtonsoft.Json", not "newtonsoft.json").
// OSV's NuGet ecosystem is case-sensitive.
func TestOSVSignal_NuGetPreservesCase(t *testing.T) {
	var capturedName string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Package struct {
				Name string `json:"name"`
			} `json:"package"`
		}
		json.NewDecoder(r.Body).Decode(&q)
		capturedName = q.Package.Name
		json.NewEncoder(w).Encode(map[string]any{"vulns": []any{}})
	}))
	defer srv.Close()
	c := cache.NewMemory()
	defer c.Close()
	sig := trust.NewOSVSignal("MEDIUM", srv.Client(), c, srv.URL)
	pkg := trust.Package{Ecosystem: trust.EcosystemNuGet, Name: "Newtonsoft.Json", Version: "13.0.3"}
	_, err := sig.Check(context.Background(), pkg)
	require.NoError(t, err)
	assert.Equal(t, "Newtonsoft.Json", capturedName,
		"OSV query must preserve original case for NuGet package names")
}
