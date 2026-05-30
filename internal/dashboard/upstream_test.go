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
	"github.com/jverhoeks/escrow/internal/upstreamlog"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestUpstreamLog_ReturnsEvents(t *testing.T) {
	al, _ := allow.New("")
	cfg := config.DashboardConfig{Enabled: true, Path: "/dashboard", Username: "admin", Password: "pass", Secret: "aabbccddeeff00112233445566778899"}
	ul := upstreamlog.New(10)
	ul.Record(upstreamlog.Event{Ecosystem: "npm", URL: "https://registry.npmjs.org/a", Status: 200, Bytes: 123})
	dash := dashboard.New(cfg, eventlog.New(10), zerolog.Nop(), al, nil, nil, "", 0, ul)
	r := chi.NewRouter()
	dash.Mount(r)

	req := authenticatedRequest(t, http.MethodGet, "/dashboard/api/upstreamlog?n=10", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var out []upstreamlog.Event
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out, 1)
	require.Equal(t, "npm", out[0].Ecosystem)
}
