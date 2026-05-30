package dashboard_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jverhoeks/escrow/internal/allow"
	"github.com/jverhoeks/escrow/internal/config"
	"github.com/jverhoeks/escrow/internal/dashboard"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/jverhoeks/escrow/internal/trust"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestTimeseries_BucketsByHourAndEcosystem(t *testing.T) {
	handler, _ := newTestDashboardWithEvents(t)
	req := authenticatedRequest(t, http.MethodGet, "/dashboard/api/stats/timeseries?window=24h&bucket=1h", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var out struct {
		Buckets []string                    `json:"buckets"`
		Series  map[string]map[string][]int `json:"series"` // action -> ecosystem -> per-bucket counts
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.NotEmpty(t, out.Buckets)
	require.Contains(t, out.Series, "allowed")
	require.Contains(t, out.Series, "denied")
}

// newTestDashboardWithEvents builds a dashboard router pre-seeded with a small
// mix of events (one of them an OSV block carrying a vuln). Shared by the
// timeseries and CVE endpoint tests.
func newTestDashboardWithEvents(t *testing.T) (http.Handler, *eventlog.Log) {
	t.Helper()
	al, err := allow.New("")
	require.NoError(t, err)
	cfg := config.DashboardConfig{Enabled: true, Path: "/dashboard", Username: "admin", Password: "pass", Secret: "aabbccddeeff00112233445566778899"}
	evLog := eventlog.New(100)
	evLog.Record(eventlog.PackageEvent{Ecosystem: "npm", Package: "a@1.0.0", Action: "allow"})
	evLog.Record(eventlog.PackageEvent{Ecosystem: "npm", Package: "b@1.0.0", Action: "block", Signal: "osv",
		Vulns: []trust.Vuln{{ID: "GHSA-x", Severity: "HIGH"}}})
	evLog.Record(eventlog.PackageEvent{Ecosystem: "pypi", Package: "c@2.0.0", Action: "warn"})
	dash := dashboard.New(cfg, evLog, zerolog.Nop(), al, nil, nil, "", 0, nil)
	r := chi.NewRouter()
	dash.Mount(r)
	return r, evLog
}
