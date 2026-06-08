package npm_test

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
	"github.com/jverhoeks/escrow/internal/config"
	"github.com/jverhoeks/escrow/internal/handler/npm"
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/trust"
)

// newNPMHandler builds an npm handler whose upstream always returns 503,
// simulating an unreachable/erroring upstream for the manifest fetch.
func newNPMHandlerWith503(t *testing.T, c cache.Cache) (*npm.Handler, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	engine := trust.NewEngine(trust.NewAgeSignal(7, nil))
	pol := policy.New(&config.PolicyConfig{
		Age: &config.AgePolicyConfig{MinDays: 7, Action: "block"},
	})
	h := npm.New(srv.Client(), srv.URL, engine, pol, c, nil)
	return h, srv
}

func TestNPMHandler_StaleOnError_ServesExpiredManifest(t *testing.T) {
	c := cache.NewMemory()
	defer c.Close()
	c.SetStaleMaxAge(10 * time.Minute)

	// Pre-seed an expired manifest within the grace window.
	staleBody := []byte(`{"name":"lodash","stale":true}`)
	require.NoError(t, c.SetMeta(context.Background(), "npm/meta/lodash", staleBody, -time.Minute))

	h, _ := newNPMHandlerWith503(t, c)
	req := httptest.NewRequest(http.MethodGet, "/lodash", nil)
	rr := httptest.NewRecorder()
	h.ServeManifest(rr, req, "lodash")

	res := rr.Result()
	defer res.Body.Close()
	assert.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, "true", res.Header.Get("X-Escrow-Stale"))
	assert.Equal(t, "application/json", res.Header.Get("Content-Type"))
	got, _ := io.ReadAll(res.Body)
	assert.Equal(t, staleBody, got)
}

func TestNPMHandler_StaleDisabled_Returns502(t *testing.T) {
	c := cache.NewMemory()
	defer c.Close()
	// staleMaxAge stays 0 (disabled), even though an expired manifest is present.
	require.NoError(t, c.SetMeta(context.Background(), "npm/meta/lodash", []byte(`{"name":"lodash"}`), -time.Minute))

	h, _ := newNPMHandlerWith503(t, c)
	req := httptest.NewRequest(http.MethodGet, "/lodash", nil)
	rr := httptest.NewRecorder()
	h.ServeManifest(rr, req, "lodash")

	assert.Equal(t, http.StatusBadGateway, rr.Code)
	assert.Empty(t, rr.Header().Get("X-Escrow-Stale"))
}

func TestNPMHandler_StaleEnabled_NoCachedEntry_Returns502(t *testing.T) {
	c := cache.NewMemory()
	defer c.Close()
	c.SetStaleMaxAge(10 * time.Minute)
	// No cached entry at all.

	h, _ := newNPMHandlerWith503(t, c)
	req := httptest.NewRequest(http.MethodGet, "/lodash", nil)
	rr := httptest.NewRecorder()
	h.ServeManifest(rr, req, "lodash")

	assert.Equal(t, http.StatusBadGateway, rr.Code)
	assert.Empty(t, rr.Header().Get("X-Escrow-Stale"))
}
