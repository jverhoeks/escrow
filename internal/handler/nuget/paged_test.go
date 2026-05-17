package nuget_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNuGetHandler_RegistrationPagedFetchesAndFilters verifies that when a
// registration index contains a paged page (items: null), escrow fetches that
// page from upstream and applies the age filter to its items.
func TestNuGetHandler_RegistrationPagedFetchesAndFilters(t *testing.T) {
	oldPub := time.Now().Add(-90 * 24 * time.Hour).UTC().Format(time.RFC3339)
	newPub := time.Now().Add(-1 * 24 * time.Hour).UTC().Format(time.RFC3339)

	var pageURL string // set after server starts

	// The upstream registration index has ONE paged page (items is null).
	// The separate page endpoint contains two versions: one old, one too new.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v3/registration5-semver1/mypkg/index.json":
			// Return a paged registration index (items: null in the page).
			resp := map[string]any{
				"count": 1,
				"items": []any{
					map[string]any{
						"@id":   pageURL,
						"@type": "catalog:CatalogPage",
						"count": 2,
						"lower": "1.0.0",
						"upper": "2.0.0",
						// items intentionally absent (nil) to simulate paged response
					},
				},
			}
			json.NewEncoder(w).Encode(resp)

		case r.URL.Path == "/v3/registration5-semver1/mypkg/page/1.0.0/2.0.0.json":
			// The actual page with two versions.
			page := map[string]any{
				"items": []any{
					map[string]any{
						"@type": "Package",
						"catalogEntry": map[string]any{
							"id": "mypkg", "version": "1.0.0",
							"published": oldPub,
						},
						"packageContent": fmt.Sprintf("https://api.nuget.org/v3-flatcontainer/mypkg/1.0.0/mypkg.1.0.0.nupkg"),
					},
					map[string]any{
						"@type": "Package",
						"catalogEntry": map[string]any{
							"id": "mypkg", "version": "2.0.0",
							"published": newPub, // too new
						},
						"packageContent": fmt.Sprintf("https://api.nuget.org/v3-flatcontainer/mypkg/2.0.0/mypkg.2.0.0.nupkg"),
					},
				},
			}
			json.NewEncoder(w).Encode(page)

		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	pageURL = upstream.URL + "/v3/registration5-semver1/mypkg/page/1.0.0/2.0.0.json"

	h := makeHandler(upstream, 7)
	r := chi.NewRouter()
	h.Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/nuget/v3/registration5-semver1/mypkg/index.json", nil)
	req.Host = "localhost:8888"
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var result map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&result))

	pages := result["items"].([]any)
	require.Len(t, pages, 1, "one page should remain")
	page := pages[0].(map[string]any)
	items := page["items"].([]any)
	require.Len(t, items, 1, "only the old version should remain after age filtering")
	ce := items[0].(map[string]any)["catalogEntry"].(map[string]any)
	assert.Equal(t, "1.0.0", ce["version"])
}
