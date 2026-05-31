package dashboard_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jverhoeks/escrow/internal/accesslog"
	"github.com/jverhoeks/escrow/internal/allow"
	"github.com/jverhoeks/escrow/internal/config"
	"github.com/jverhoeks/escrow/internal/dashboard"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestAccessLog_ReadsRingAndFiltersDashboardPath(t *testing.T) {
	ring := accesslog.New(10)
	ring.Record(accesslog.Entry{Host: "127.0.0.1", Method: "GET", Path: "/npm/lodash", Proto: "HTTP/1.1", Status: 200, Bytes: 1234, UserAgent: "npm/11"})
	ring.Record(accesslog.Entry{Host: "127.0.0.1", Method: "GET", Path: "/dashboard/api/stream", Proto: "HTTP/1.1", Status: 200, Bytes: 0, UserAgent: "Mozilla"})

	al, _ := allow.New("")
	cfg := config.DashboardConfig{Enabled: true, Path: "/dashboard", Username: "admin", Password: "pass", Secret: "aabbccddeeff00112233445566778899"}
	dash := dashboard.New(cfg, eventlog.New(10), zerolog.Nop(), al, nil, nil, ring, nil)
	r := chi.NewRouter()
	dash.Mount(r)

	req := authenticatedRequest(t, http.MethodGet, "/dashboard/api/accesslog?n=100", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var out []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out, 1) // dashboard's own /dashboard/... request filtered out
	require.Equal(t, "/npm/lodash", out[0]["path"])
	require.Equal(t, float64(200), out[0]["status"])
}
