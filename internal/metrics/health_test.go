package metrics_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jverhoeks/escrow/internal/metrics"
)

func TestHealthHandler_CacheHealthy(t *testing.T) {
	healthy := func(context.Context) error { return nil }
	h := metrics.HealthHandler("dev", "disk", nil, healthy)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var resp metrics.HealthResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.True(t, resp.CacheWritable, "nil cacheHealth error should report cache_writable: true")
	assert.Equal(t, "ok", resp.Status)
	assert.Equal(t, "dev", resp.Version)
}

func TestHealthHandler_CacheUnhealthy(t *testing.T) {
	unhealthy := func(context.Context) error { return errors.New("probe failed") }
	h := metrics.HealthHandler("dev", "disk", nil, unhealthy)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
	var resp metrics.HealthResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.False(t, resp.CacheWritable)
	assert.Equal(t, "degraded", resp.Status)
}

// A non-disk backend (memory) returns nil from Healthy → always ok. This is the
// #14a fix: status is driven by the closure, not a hard-coded "non-disk = true".
func TestHealthHandler_NonDiskAlwaysHealthy(t *testing.T) {
	h := metrics.HealthHandler("dev", "memory", nil, func(context.Context) error { return nil })
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var resp metrics.HealthResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.True(t, resp.CacheWritable)
}

// A nil cacheHealth must not panic — the handler defaults to healthy.
func TestHealthHandler_NilCacheHealth(t *testing.T) {
	h := metrics.HealthHandler("dev", "memory", nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var resp metrics.HealthResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.True(t, resp.CacheWritable)
}

// A single flaky upstream must NOT make /healthz return 503: liveness reflects
// escrow's own ability to serve (cache writable), not upstream reachability.
// See #40.
func TestHealthHandler_FlakyUpstreamStays200(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer down.Close()
	healthy := func(context.Context) error { return nil }
	h := metrics.HealthHandler("dev", "disk", map[string]string{"npm": down.URL}, healthy)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "flaky upstream must not force 503")
	var resp metrics.HealthResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, "ok", resp.Status)
	assert.False(t, resp.UpstreamStatus["npm"], "upstream status is reported as informational")
}
