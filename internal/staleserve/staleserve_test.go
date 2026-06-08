package staleserve_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jverhoeks/escrow/internal/cache"
	"github.com/jverhoeks/escrow/internal/staleserve"
)

func TestServe_HitWritesStaleResponse(t *testing.T) {
	c := cache.NewMemory()
	defer c.Close()
	c.SetStaleMaxAge(10 * time.Minute)

	body := []byte(`{"name":"lodash"}`)
	// Pre-seed an entry that expired 1 minute ago (within the 10m grace window).
	require.NoError(t, c.SetMeta(context.Background(), "npm/meta/lodash", body, -time.Minute))

	r := httptest.NewRequest(http.MethodGet, "/lodash", nil)
	rr := httptest.NewRecorder()

	ok := staleserve.Serve(rr, r, c, "npm/meta/lodash", "application/json", "npm", "manifest")
	assert.True(t, ok)

	res := rr.Result()
	defer res.Body.Close()
	assert.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, "application/json", res.Header.Get("Content-Type"))
	assert.Equal(t, "true", res.Header.Get("X-Escrow-Stale"))
	assert.Equal(t, `110 - "Response is Stale"`, res.Header.Get("Warning"))
	got, _ := io.ReadAll(res.Body)
	assert.Equal(t, body, got)
}

func TestServe_MissReturnsFalse(t *testing.T) {
	c := cache.NewMemory()
	defer c.Close()
	c.SetStaleMaxAge(10 * time.Minute)
	// No entry seeded.

	r := httptest.NewRequest(http.MethodGet, "/lodash", nil)
	rr := httptest.NewRecorder()

	ok := staleserve.Serve(rr, r, c, "npm/meta/missing", "application/json", "npm", "manifest")
	assert.False(t, ok)
	// Nothing should have been written.
	assert.Empty(t, rr.Header().Get("X-Escrow-Stale"))
	assert.Equal(t, 0, rr.Body.Len())
}

func TestServe_DisabledReturnsFalse(t *testing.T) {
	c := cache.NewMemory()
	defer c.Close()
	// staleMaxAge stays 0 (disabled), even with a recently-expired entry present.
	require.NoError(t, c.SetMeta(context.Background(), "npm/meta/lodash", []byte("x"), -time.Minute))

	r := httptest.NewRequest(http.MethodGet, "/lodash", nil)
	rr := httptest.NewRecorder()

	ok := staleserve.Serve(rr, r, c, "npm/meta/lodash", "application/json", "npm", "manifest")
	assert.False(t, ok)
	assert.Equal(t, 0, rr.Body.Len())
}
