package npm_test

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
	"github.com/jverhoeks/escrow/internal/handler/npm"
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/trust"
)

func makeUpstreamNPM(v422Time string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"name":      "lodash",
			"dist-tags": map[string]string{"latest": "4.17.22"},
			"versions": map[string]any{
				"4.17.21": map[string]any{"dist": map[string]any{"tarball": "https://example.com/lodash-4.17.21.tgz"}},
				"4.17.22": map[string]any{"dist": map[string]any{"tarball": "https://example.com/lodash-4.17.22.tgz"}},
			},
			"time": map[string]string{
				"4.17.21": time.Now().Add(-30 * 24 * time.Hour).Format(time.RFC3339),
				"4.17.22": v422Time,
			},
		})
	}))
}

func TestNPMHandler_BlocksNewVersion(t *testing.T) {
	upstream := makeUpstreamNPM(time.Now().Add(-2 * 24 * time.Hour).Format(time.RFC3339))
	defer upstream.Close()

	c := cache.NewMemory()
	defer c.Close()
	engine := trust.NewEngine(trust.NewAgeSignal(7, nil))
	pol := policy.New(&config.PolicyConfig{
		Age: &config.AgePolicyConfig{MinDays: 7, Action: "block"},
	})

	h := npm.New(upstream.Client(), upstream.URL, engine, pol, c, nil)
	req := httptest.NewRequest(http.MethodGet, "/lodash", nil)
	rr := httptest.NewRecorder()
	h.ServeManifest(rr, req, "lodash")

	require.Equal(t, http.StatusOK, rr.Code)
	var manifest map[string]any
	json.NewDecoder(rr.Body).Decode(&manifest)
	versions := manifest["versions"].(map[string]any)
	_, has422 := versions["4.17.22"]
	_, has421 := versions["4.17.21"]
	assert.False(t, has422, "new version should be stripped")
	assert.True(t, has421, "old version should remain")
	distTags := manifest["dist-tags"].(map[string]any)
	assert.Equal(t, "4.17.21", distTags["latest"])
}

func TestNPMHandler_AllVersionsPass(t *testing.T) {
	upstream := makeUpstreamNPM(time.Now().Add(-10 * 24 * time.Hour).Format(time.RFC3339))
	defer upstream.Close()

	c := cache.NewMemory()
	defer c.Close()
	engine := trust.NewEngine(trust.NewAgeSignal(7, nil))
	pol := policy.New(&config.PolicyConfig{
		Age: &config.AgePolicyConfig{MinDays: 7, Action: "block"},
	})
	h := npm.New(upstream.Client(), upstream.URL, engine, pol, c, nil)

	req := httptest.NewRequest(http.MethodGet, "/lodash", nil)
	rr := httptest.NewRecorder()
	h.ServeManifest(rr, req, "lodash")

	require.Equal(t, http.StatusOK, rr.Code)
	var manifest map[string]any
	json.NewDecoder(rr.Body).Decode(&manifest)
	versions := manifest["versions"].(map[string]any)
	assert.Len(t, versions, 2)
}
