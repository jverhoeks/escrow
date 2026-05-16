package npm_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestNPMHandler_ExtractsAuthorFromNpmUser(t *testing.T) {
	publisherLookupCalled := 0
	npmUserSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/-/user/") {
			// Verify the lookup was made for the correct author
			assert.Contains(t, r.URL.Path, "newauthor", "publisher lookup should use extracted author name")
			publisherLookupCalled++
			json.NewEncoder(w).Encode(map[string]any{
				"created": time.Now().Add(-5 * 24 * time.Hour).Format(time.RFC3339),
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"name":      "newpkg",
			"dist-tags": map[string]string{"latest": "1.0.0"},
			"versions": map[string]any{
				"1.0.0": map[string]any{
					"_npmUser": map[string]any{"name": "newauthor", "email": "a@b.com"},
				},
			},
			"time": map[string]string{
				"1.0.0": time.Now().Add(-30 * 24 * time.Hour).Format(time.RFC3339),
			},
		})
	}))
	defer npmUserSrv.Close()

	c := cache.NewMemory()
	defer c.Close()
	pubSignal := trust.NewPublisherSignal(30, npmUserSrv.Client(), npmUserSrv.URL, "")
	engine := trust.NewEngine(trust.NewAgeSignal(7, nil), pubSignal)
	pol := policy.New(&config.PolicyConfig{
		Age:       &config.AgePolicyConfig{MinDays: 7, Action: "block"},
		Publisher: &config.PublisherPolicyConfig{MaxAccountAgeDays: 30, Action: "warn"},
	})

	h := npm.New(npmUserSrv.Client(), npmUserSrv.URL, engine, pol, c, nil)
	req := httptest.NewRequest(http.MethodGet, "/newpkg", nil)
	rr := httptest.NewRecorder()
	h.ServeManifest(rr, req, "newpkg")

	require.Equal(t, http.StatusOK, rr.Code)
	var manifest map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&manifest))
	versions := manifest["versions"].(map[string]any)
	assert.Contains(t, versions, "1.0.0", "version should not be stripped on publisher warn")
	assert.Equal(t, 1, publisherLookupCalled, "publisher lookup should have been called with extracted author")
}
