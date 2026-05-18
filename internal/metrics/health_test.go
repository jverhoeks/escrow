package metrics_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/metrics"
)

func TestHealthHandler_CacheWritable(t *testing.T) {
	dir := t.TempDir()
	h := metrics.HealthHandler("dev", "disk", nil, dir)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var resp metrics.HealthResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.True(t, resp.CacheWritable, "writable temp dir should report cache_writable: true")
	assert.Equal(t, "ok", resp.Status)
	assert.Equal(t, "dev", resp.Version)
}

func TestHealthHandler_CacheNotWritable(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o555))
	defer os.Chmod(dir, 0o755)

	h := metrics.HealthHandler("dev", "disk", nil, dir)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	if os.Getuid() == 0 {
		t.Skip("root user bypasses read-only permission; skipping")
	}
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
	var resp metrics.HealthResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.False(t, resp.CacheWritable)
	assert.Equal(t, "degraded", resp.Status)
}

func TestHealthHandler_EmptyCacheDir_NonDisk(t *testing.T) {
	h := metrics.HealthHandler("dev", "memory", nil, "")
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var resp metrics.HealthResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.True(t, resp.CacheWritable, "non-disk backend should always report writable")
}

func TestHealthHandler_NonExistentCacheDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nonexistent")
	h := metrics.HealthHandler("dev", "disk", nil, dir)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
	var resp metrics.HealthResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.False(t, resp.CacheWritable, "non-existent dir should report not writable")
}
