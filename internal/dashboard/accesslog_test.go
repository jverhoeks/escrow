package dashboard_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jverhoeks/escrow/internal/allow"
	"github.com/jverhoeks/escrow/internal/config"
	"github.com/jverhoeks/escrow/internal/dashboard"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestAccessLog_ParsesAndFiltersDashboardPath(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "access.log")
	lines := `127.0.0.1 - - [30/May/2026:09:17:46 +0000] "GET /npm/lodash HTTP/1.1" 200 1234 "-" "npm/11"
127.0.0.1 - - [30/May/2026:09:17:47 +0000] "GET /dashboard/api/stream HTTP/1.1" 200 0 "-" "Mozilla"
`
	require.NoError(t, os.WriteFile(logPath, []byte(lines), 0o644))

	al, _ := allow.New("")
	cfg := config.DashboardConfig{Enabled: true, Path: "/dashboard", Username: "admin", Password: "pass", Secret: "aabbccddeeff00112233445566778899"}
	dash := dashboard.New(cfg, eventlog.New(10), zerolog.Nop(), al, nil, nil, logPath, 0, nil)
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
