package pypi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/cache"
	"github.com/jverhoeks/escrow/internal/config"
	"github.com/jverhoeks/escrow/internal/handler/pypi"
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/trust"
)

func makeUpstreamPyPI(uploadTime string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"info": map[string]any{"name": "requests", "version": "2.32.0"},
			"releases": map[string]any{
				"2.31.0": []map[string]any{
					{"filename": "requests-2.31.0-py3-none-any.whl",
						"upload_time": time.Now().Add(-30 * 24 * time.Hour).Format("2006-01-02T15:04:05")},
				},
				"2.32.0": []map[string]any{
					{"filename": "requests-2.32.0-py3-none-any.whl", "upload_time": uploadTime},
				},
			},
		})
	}))
}

func TestPyPIHandler_BlocksNewVersionInSimpleIndex(t *testing.T) {
	upstream := makeUpstreamPyPI(time.Now().Add(-1 * 24 * time.Hour).Format("2006-01-02T15:04:05"))
	defer upstream.Close()

	c := cache.NewMemory()
	defer c.Close()
	engine := trust.NewEngine(trust.NewAgeSignal(7, nil))
	pol := policy.New(&config.PolicyConfig{
		Age: &config.AgePolicyConfig{MinDays: 7, Action: "block"},
	})
	h := pypi.New(upstream.Client(), upstream.URL, engine, pol, c, false, nil)

	req := httptest.NewRequest(http.MethodGet, "/pypi/simple/requests/", nil)
	rr := httptest.NewRecorder()
	h.ServeSimpleIndex(rr, req, "requests")

	require.Equal(t, http.StatusOK, rr.Code)
	body := rr.Body.String()
	assert.NotContains(t, body, "2.32.0", "new version should be stripped")
	assert.Contains(t, body, "2.31.0", "old version should remain")
}

func TestPyPIHandler_BlockSdist(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("tarball"))
	}))
	defer upstream.Close()

	c := cache.NewMemory()
	defer c.Close()
	h := pypi.New(upstream.Client(), upstream.URL, trust.NewEngine(), policy.New(nil), c, true, nil)

	req := httptest.NewRequest(http.MethodGet, "/pypi/packages/requests-2.31.0.tar.gz", nil)
	rr := httptest.NewRecorder()
	h.ServeFile(rr, req, "requests-2.31.0.tar.gz")

	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "sdist")
}

