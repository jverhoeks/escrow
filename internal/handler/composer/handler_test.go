package composer_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jverhoeks/escrow/internal/cache"
	"github.com/jverhoeks/escrow/internal/config"
	"github.com/jverhoeks/escrow/internal/handler/composer"
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/trust"
)

// mockPackagistRoot returns a mock Packagist packages.json server.
func mockPackagistRoot() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"metadata-url":  "/p2/%package%.json",
			"providers-url": "/p/%package%$%hash%.json",
		})
	}))
}

// mockPackagistP2 returns a mock Packagist p2 metadata server with two versions:
// one old (should always pass age checks), one recent (published 2 days ago).
func mockPackagistP2(recentTime string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"packages": map[string]any{
				"vendor/test": []any{
					map[string]any{
						"name":    "vendor/test",
						"version": "1.0.0",
						"time":    "2020-01-01T00:00:00+00:00",
						"authors": []any{map[string]any{"name": "testauthor"}},
					},
					map[string]any{
						"name":    "vendor/test",
						"version": "2.0.0",
						"time":    recentTime,
						"authors": []any{map[string]any{"name": "newauthor"}},
					},
				},
			},
		})
	}))
}

func buildHandler(upstream *httptest.Server, minDays int) *composer.Handler {
	c := cache.NewMemory()
	engine := trust.NewEngine(trust.NewAgeSignal(minDays, nil))
	pol := policy.New(&config.PolicyConfig{
		Age: &config.AgePolicyConfig{MinDays: minDays, Action: "block"},
	})
	return composer.New(upstream.Client(), upstream.URL, engine, pol, c, nil)
}

// doGet issues a GET against the handler mounted on a chi router.
func doGet(t *testing.T, h *composer.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	h.Mount(r)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

// TestComposerHandler_ServeRoot_RewritesMetadataURL checks that the proxy rewrites
// metadata-url and providers-url to have the /composer prefix.
func TestComposerHandler_ServeRoot_RewritesMetadataURL(t *testing.T) {
	upstream := mockPackagistRoot()
	defer upstream.Close()

	h := buildHandler(upstream, 7)
	rr := doGet(t, h, "/composer/packages.json")

	require.Equal(t, http.StatusOK, rr.Code)
	var root map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&root))

	assert.Equal(t, "/composer/p2/%package%.json", root["metadata-url"],
		"metadata-url should be rewritten to include /composer prefix")
	assert.Equal(t, "/composer/p/%package%$%hash%.json", root["providers-url"],
		"providers-url should be rewritten to include /composer prefix")
}

// TestComposerHandler_ServePackage_FiltersBlockedVersions checks that a recently
// published version (2 days old) is removed when the age policy requires 7 days.
func TestComposerHandler_ServePackage_FiltersBlockedVersions(t *testing.T) {
	recentTime := time.Now().Add(-2 * 24 * time.Hour).Format(time.RFC3339)
	upstream := mockPackagistP2(recentTime)
	defer upstream.Close()

	h := buildHandler(upstream, 7)
	rr := doGet(t, h, "/composer/p2/vendor/test.json")

	require.Equal(t, http.StatusOK, rr.Code)
	var payload map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&payload))

	packages := payload["packages"].(map[string]any)
	versions := packages["vendor/test"].([]any)

	require.Len(t, versions, 1, "only the old version should remain after filtering")
	v := versions[0].(map[string]any)
	assert.Equal(t, "1.0.0", v["version"], "old version 1.0.0 should be kept")
}

// TestComposerHandler_ServePackage_AllowsOldVersions checks that both versions
// pass through when both are older than the minimum age requirement.
func TestComposerHandler_ServePackage_AllowsOldVersions(t *testing.T) {
	// Both versions are old enough (more than 7 days ago).
	oldTime := time.Now().Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	upstream := mockPackagistP2(oldTime)
	defer upstream.Close()

	h := buildHandler(upstream, 7)
	rr := doGet(t, h, "/composer/p2/vendor/test.json")

	require.Equal(t, http.StatusOK, rr.Code)
	var payload map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&payload))

	packages := payload["packages"].(map[string]any)
	versions := packages["vendor/test"].([]any)

	assert.Len(t, versions, 2, "both old versions should pass through")
}

// TestComposerHandler_ServePackage_EmptyAuthors verifies that versions lacking
// an authors array are handled without panic and can still be filtered by policy.
func TestComposerHandler_ServePackage_EmptyAuthors(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"packages": map[string]any{
				"vendor/noauth": []any{
					map[string]any{
						"name":    "vendor/noauth",
						"version": "1.0.0",
						"time":    "2020-01-01T00:00:00+00:00",
						// no "authors" field
					},
					map[string]any{
						"name":    "vendor/noauth",
						"version": "2.0.0",
						"time":    "2020-01-01T00:00:00+00:00",
						"authors": []any{}, // empty slice
					},
				},
			},
		})
	}))
	defer upstream.Close()

	h := buildHandler(upstream, 7)

	// Should not panic; both versions are old enough to pass the age check.
	require.NotPanics(t, func() {
		rr := doGet(t, h, "/composer/p2/vendor/noauth.json")
		require.Equal(t, http.StatusOK, rr.Code)

		var payload map[string]any
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&payload))
		packages := payload["packages"].(map[string]any)
		versions := packages["vendor/noauth"].([]any)
		assert.Len(t, versions, 2, "both versions without authors should pass through")
	})
}
