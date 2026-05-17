package nuget_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetFlatcontainerURL_OverridesDerivation(t *testing.T) {
	// Non-standard upstream (e.g. Nexus): not a /v3 URL, so flatcontainerBase would produce a wrong path.
	var downloadPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloadPath = r.URL.Path
		w.Write([]byte("nupkg bytes"))
	}))
	defer upstream.Close()

	h := makeHandler(upstream, 7)
	// Override the flatcontainer URL to point at the test upstream root
	h.SetFlatcontainerURL(upstream.URL)

	r := chi.NewRouter()
	h.Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/nuget/v3-flatcontainer/mypkg/1.0.0/mypkg.1.0.0.nupkg", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "/mypkg/1.0.0/mypkg.1.0.0.nupkg", downloadPath,
		"download should use the overridden flatcontainer URL, not a derived one")
}

// TestRegistrationRewritesPackageContentWithCustomUpstreamFC verifies that when
// SetFlatcontainerURL is configured, packageContent URLs pointing at the custom
// upstream are rewritten to the escrow proxy — not left pointing at the upstream.
func TestRegistrationRewritesPackageContentWithCustomUpstreamFC(t *testing.T) {
	const customFCBase = "https://nexus.corp.internal/repository/nuget-flatcontainer"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return a registration with a packageContent URL pointing at the custom upstream.
		json.NewEncoder(w).Encode(map[string]any{
			"count": 1,
			"items": []any{
				map[string]any{
					"@type": "catalog:CatalogPage",
					"count": 1, "lower": "1.0.0", "upper": "1.0.0",
					"items": []any{
						map[string]any{
							"@type": "Package",
							"catalogEntry": map[string]any{
								"id": "mypkg", "version": "1.0.0",
								"published": time.Now().Add(-90 * 24 * time.Hour).UTC().Format(time.RFC3339),
							},
							"packageContent": customFCBase + "/mypkg/1.0.0/mypkg.1.0.0.nupkg",
						},
					},
				},
			},
		})
	}))
	defer upstream.Close()

	h := makeHandler(upstream, 7)
	h.SetFlatcontainerURL(customFCBase)
	r := chi.NewRouter()
	h.Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/nuget/v3/registration5-semver1/mypkg/index.json", nil)
	req.Host = "escrow.corp:8888"
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	body := rr.Body.String()
	assert.NotContains(t, body, customFCBase,
		"packageContent URL should be rewritten away from the custom upstream flatcontainer")
	assert.Contains(t, body, "escrow.corp:8888/nuget/v3-flatcontainer/",
		"packageContent URL should point at the escrow proxy")
}

// TestNuGetHandler_PagedRegistrationRewritesPackageContentURLs verifies that items
// from fetched paged pages also have their packageContent URLs rewritten to the proxy.
func TestNuGetHandler_PagedRegistrationRewritesPackageContentURLs(t *testing.T) {
	var pageURL string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/index.json") {
			json.NewEncoder(w).Encode(map[string]any{
				"count": 1,
				"items": []any{
					map[string]any{
						"@id": pageURL, "@type": "catalog:CatalogPage",
						"count": 1, "lower": "1.0.0", "upper": "1.0.0",
						// items absent = paged
					},
				},
			})
			return
		}
		// The page endpoint
		json.NewEncoder(w).Encode(map[string]any{
			"items": []any{
				map[string]any{
					"@type": "Package",
					"catalogEntry": map[string]any{
						"id": "mypkg", "version": "1.0.0",
						"published": time.Now().Add(-90 * 24 * time.Hour).UTC().Format(time.RFC3339),
					},
					"packageContent": "https://api.nuget.org/v3-flatcontainer/mypkg/1.0.0/mypkg.1.0.0.nupkg",
				},
			},
		})
	}))
	defer upstream.Close()
	pageURL = upstream.URL + "/page/1.0.0/1.0.0.json"

	h := makeHandler(upstream, 7)
	r := chi.NewRouter()
	h.Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/nuget/v3/registration5-semver1/mypkg/index.json", nil)
	req.Host = "localhost:8888"
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	body := rr.Body.String()
	assert.NotContains(t, body, "api.nuget.org",
		"packageContent from paged page should be rewritten away from api.nuget.org")
	assert.Contains(t, body, "localhost:8888/nuget/v3-flatcontainer/",
		"packageContent from paged page should point at the escrow proxy")
}

func TestNuGetHandler_VersionListFetchesCorrectRegistrationPath(t *testing.T) {
	// Verify the version list request fetches registration from the per-package URL (not a generic path).
	var requestedPaths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPaths = append(requestedPaths, r.URL.Path)
		if strings.Contains(r.URL.Path, "/registration5-semver1/specificpkg/index.json") {
			reg := makeRegistration([]struct {
				Ver       string
				Published time.Time
			}{
				{"1.0.0", time.Now().Add(-90 * 24 * time.Hour)},
			})
			json.NewEncoder(w).Encode(reg)
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()
	h := makeHandler(upstream, 7)
	r := chi.NewRouter()
	h.Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/nuget/v3-flatcontainer/specificpkg/index.json", nil)
	req.Host = "localhost:8888"
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)

	var result map[string][]string
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&result))
	assert.Equal(t, []string{"1.0.0"}, result["versions"])

	// Verify the upstream was queried for the correct package-specific path
	assert.True(t, func() bool {
		for _, p := range requestedPaths {
			if strings.Contains(p, "specificpkg") {
				return true
			}
		}
		return false
	}(), "upstream should have been queried for specificpkg, got: %v", requestedPaths)
}
