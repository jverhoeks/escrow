package cargo_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/cache"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/jverhoeks/escrow/internal/handler/cargo"
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/trust"
)

func makeMinimalHandler() *cargo.Handler {
	c := cache.NewMemory()
	engine := trust.NewEngine()
	pol := policy.New(nil)
	evLog := eventlog.New(10)
	return cargo.New(http.DefaultClient, engine, pol, c, evLog)
}

func TestCargoHandler_ServeConfig_HTTP(t *testing.T) {
	h := makeMinimalHandler()
	r := chi.NewRouter()
	h.Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/cargo/config.json", nil)
	req.Host = "localhost:8888"
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var cfg map[string]string
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&cfg))
	assert.Contains(t, cfg["dl"], "http://localhost:8888/cargo/crates/{crate}/{version}/download")
}

func TestCargoHandler_ServeConfig_HTTPSViaForwardedProto(t *testing.T) {
	h := makeMinimalHandler()
	r := chi.NewRouter()
	h.Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/cargo/config.json", nil)
	req.Host = "escrow.corp.internal"
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var cfg map[string]string
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&cfg))
	assert.Contains(t, cfg["dl"], "https://escrow.corp.internal/cargo/crates/{crate}/{version}/download",
		"dl URL should use https:// when X-Forwarded-Proto is https")
}

func TestCargoHandler_ServeConfig_HTTPSViaFallback(t *testing.T) {
	h := makeMinimalHandler()
	r := chi.NewRouter()
	h.Mount(r)

	// No X-Forwarded-Proto and no r.TLS → should produce http://
	req := httptest.NewRequest(http.MethodGet, "/cargo/config.json", nil)
	req.Host = "localhost:8888"
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	var cfg map[string]string
	json.NewDecoder(rr.Body).Decode(&cfg)
	assert.Contains(t, cfg["dl"], "http://", "should default to http without TLS signals")
}
