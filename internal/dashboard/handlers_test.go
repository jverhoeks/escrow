package dashboard_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jverhoeks/escrow/internal/allow"
	"github.com/jverhoeks/escrow/internal/config"
	"github.com/jverhoeks/escrow/internal/dashboard"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestDashboard creates a Dashboard with an in-memory allowlist and returns
// the chi router, the auth helper, and the allowlist for inspection.
func newTestDashboard(t *testing.T) (http.Handler, *allow.List) {
	t.Helper()
	al, err := allow.New("") // in-memory
	require.NoError(t, err)

	cfg := config.DashboardConfig{
		Enabled:  true,
		Path:     "/dashboard",
		Username: "admin",
		Password: "pass",
		Secret:   "aabbccddeeff00112233445566778899",
	}
	evLog := eventlog.New(50)
	logger := zerolog.Nop()

	dash := dashboard.New(cfg, evLog, logger, al, nil)
	r := chi.NewRouter()
	dash.Mount(r)
	return r, al
}

// authenticatedRequest creates a GET/POST request pre-loaded with a valid
// session cookie so it passes the auth middleware.
func authenticatedRequest(t *testing.T, method, path string, body []byte) *http.Request {
	t.Helper()
	auth := dashboard.NewAuth("admin", "pass", "aabbccddeeff00112233445566778899")

	// Obtain a valid cookie by calling SetCookie on a recorder.
	rec := httptest.NewRecorder()
	auth.SetCookie(rec, "admin")
	cookie := rec.Result().Cookies()[0]

	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.AddCookie(cookie)
	return req
}

func TestHandleAllow_AddsEntry(t *testing.T) {
	handler, al := newTestDashboard(t)

	payload, _ := json.Marshal(map[string]string{
		"ecosystem": "npm",
		"name":      "lodash",
		"version":   "4.17.21",
		"reason":    "approved in test",
	})
	req := authenticatedRequest(t, http.MethodPost, "/dashboard/api/allow", payload)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var result map[string]bool
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&result))
	assert.True(t, result["ok"])

	entries := al.Entries()
	require.Len(t, entries, 1)
	assert.Equal(t, "npm", entries[0].Ecosystem)
	assert.Equal(t, "lodash", entries[0].Name)
	assert.Equal(t, "4.17.21", entries[0].Version)
	assert.Equal(t, "admin", entries[0].AddedBy)
}

func TestHandleAllow_MissingFields(t *testing.T) {
	handler, _ := newTestDashboard(t)

	payload, _ := json.Marshal(map[string]string{
		"ecosystem": "npm",
		// name is missing
	})
	req := authenticatedRequest(t, http.MethodPost, "/dashboard/api/allow", payload)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleAllowList_ReturnsEntries(t *testing.T) {
	handler, al := newTestDashboard(t)

	// Pre-populate the list.
	require.NoError(t, al.Add(allow.Entry{
		Ecosystem: "pypi",
		Name:      "requests",
		Version:   "2.31.0",
		Reason:    "pre-approved",
		AddedBy:   "ci",
	}))

	req := authenticatedRequest(t, http.MethodGet, "/dashboard/api/allowlist", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var entries []allow.Entry
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&entries))
	require.Len(t, entries, 1)
	assert.Equal(t, "pypi", entries[0].Ecosystem)
	assert.Equal(t, "requests", entries[0].Name)
}

func TestHandleAllowList_NilAllowList(t *testing.T) {
	// Create a Dashboard with a nil allowlist to verify graceful degradation.
	cfg := config.DashboardConfig{
		Enabled:  true,
		Path:     "/dashboard",
		Username: "admin",
		Password: "pass",
		Secret:   "aabbccddeeff00112233445566778899",
	}
	evLog := eventlog.New(50)
	logger := zerolog.Nop()
	dash := dashboard.New(cfg, evLog, logger, nil, nil)
	r := chi.NewRouter()
	dash.Mount(r)

	req := authenticatedRequest(t, http.MethodGet, "/dashboard/api/allowlist", nil)
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	// Should return empty JSON array, not an error.
	assert.Contains(t, rec.Body.String(), "[]")
}

func TestHandleAllow_NilAllowList(t *testing.T) {
	// Dashboard with nil allowList should return 503
	cfg := config.DashboardConfig{
		Enabled:  true,
		Path:     "/dashboard",
		Username: "admin",
		Password: "pass",
		Secret:   "aabbccddeeff00112233445566778899",
	}
	evLog := eventlog.New(50)
	logger := zerolog.Nop()
	dash := dashboard.New(cfg, evLog, logger, nil, nil)
	r := chi.NewRouter()
	dash.Mount(r)

	body, _ := json.Marshal(map[string]string{
		"ecosystem": "npm",
		"name":      "lodash",
		"version":   "4.17.21",
		"reason":    "test",
	})
	req := authenticatedRequest(t, http.MethodPost, "/dashboard/api/allow", body)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestHandleAllow_UnauthenticatedRejected(t *testing.T) {
	cfg := config.DashboardConfig{
		Enabled:  true,
		Path:     "/dashboard",
		Username: "admin",
		Password: "pw",
		Secret:   "secret123456789012345678901234",
	}
	al, err := allow.New("")
	require.NoError(t, err)
	dash := dashboard.New(cfg, eventlog.New(10), zerolog.Nop(), al, nil)
	r := chi.NewRouter()
	dash.Mount(r)

	body, _ := json.Marshal(map[string]string{
		"ecosystem": "npm",
		"name":      "lodash",
		"version":   "4.17.21",
		"reason":    "test",
	})
	// No session cookie — unauthenticated request
	req := httptest.NewRequest(http.MethodPost, cfg.Path+"/api/allow", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	// Auth middleware redirects to login (302) or returns 401 — either way, not 200
	assert.NotEqual(t, http.StatusOK, rec.Code, "unauthenticated request should not succeed")
}
