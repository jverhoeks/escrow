package nuget_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/cache"
	"github.com/jverhoeks/escrow/internal/config"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/jverhoeks/escrow/internal/handler/nuget"
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/trust"
)

func makeRegistration(versions []struct {
	Ver       string
	Published time.Time
}) map[string]any {
	items := make([]any, 0, len(versions))
	for _, v := range versions {
		items = append(items, map[string]any{
			"@type": "Package",
			"catalogEntry": map[string]any{
				"id":        "Newtonsoft.Json",
				"version":   v.Ver,
				"published": v.Published.UTC().Format(time.RFC3339),
			},
			"packageContent": fmt.Sprintf("https://api.nuget.org/v3-flatcontainer/newtonsoft.json/%s/newtonsoft.json.%s.nupkg", v.Ver, v.Ver),
		})
	}
	return map[string]any{
		"count": 1,
		"items": []any{
			map[string]any{
				"@type": "catalog:CatalogPage",
				"count": len(versions),
				"lower": "1.0.0",
				"upper": "13.0.0",
				"items": items,
			},
		},
	}
}

func makeHandler(upstream *httptest.Server, minAgeDays int) *nuget.Handler {
	c := cache.NewMemory()
	engine := trust.NewEngine(trust.NewAgeSignal(minAgeDays, nil))
	pol := policy.New(&config.PolicyConfig{
		Age: &config.AgePolicyConfig{MinDays: minAgeDays, Action: "block"},
	})
	evLog := eventlog.New(10)
	return nuget.New(upstream.Client(), upstream.URL+"/v3", engine, pol, c, evLog)
}

func TestNuGetHandler_Index(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	h := makeHandler(upstream, 7)

	r := chi.NewRouter()
	h.Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/nuget/index.json", nil)
	req.Host = "localhost:7888"
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var index map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&index))
	resources, _ := index["resources"].([]any)
	require.NotEmpty(t, resources)
	// Verify registration resource points at our proxy
	found := false
	for _, res := range resources {
		rm := res.(map[string]any)
		if id, _ := rm["@id"].(string); id != "" {
			if t2, _ := rm["@type"].(string); t2 == "RegistrationsBaseUrl/3.6.0" {
				assert.Contains(t, id, "localhost:7888/nuget/v3/registration5-semver1/")
				found = true
			}
		}
	}
	assert.True(t, found, "registration resource should be present in index")
}

func TestNuGetHandler_RegistrationFiltersNewVersions(t *testing.T) {
	oldVersion := struct {
		Ver       string
		Published time.Time
	}{"13.0.1", time.Now().Add(-30 * 24 * time.Hour)}
	newVersion := struct {
		Ver       string
		Published time.Time
	}{"13.0.2", time.Now().Add(-1 * 24 * time.Hour)}

	reg := makeRegistration([]struct {
		Ver       string
		Published time.Time
	}{oldVersion, newVersion})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(reg)
	}))
	defer upstream.Close()
	h := makeHandler(upstream, 7)

	r := chi.NewRouter()
	h.Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/nuget/v3/registration5-semver1/newtonsoft.json/index.json", nil)
	req.Host = "localhost:7888"
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var result map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&result))

	pages := result["items"].([]any)
	require.Len(t, pages, 1)
	page := pages[0].(map[string]any)
	items := page["items"].([]any)
	require.Len(t, items, 1, "only old version should remain")
	item := items[0].(map[string]any)
	ce := item["catalogEntry"].(map[string]any)
	assert.Equal(t, "13.0.1", ce["version"])
}

func TestNuGetHandler_RegistrationRewritesPackageContentURL(t *testing.T) {
	reg := makeRegistration([]struct {
		Ver       string
		Published time.Time
	}{{"13.0.1", time.Now().Add(-30 * 24 * time.Hour)}})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(reg)
	}))
	defer upstream.Close()
	h := makeHandler(upstream, 7)

	r := chi.NewRouter()
	h.Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/nuget/v3/registration5-semver1/newtonsoft.json/index.json", nil)
	req.Host = "localhost:7888"
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	body, _ := io.ReadAll(rr.Body)
	// packageContent URL should be rewritten to our proxy
	assert.Contains(t, string(body), "localhost:7888/nuget/v3-flatcontainer/")
	assert.NotContains(t, string(body), "api.nuget.org")
}

func TestNuGetHandler_VersionListFiltered(t *testing.T) {
	reg := makeRegistration([]struct {
		Ver       string
		Published time.Time
	}{
		{"1.0.0", time.Now().Add(-90 * 24 * time.Hour)},
		{"2.0.0", time.Now().Add(-2 * 24 * time.Hour)}, // blocked
	})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(reg)
	}))
	defer upstream.Close()
	h := makeHandler(upstream, 7)

	r := chi.NewRouter()
	h.Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/nuget/v3-flatcontainer/mypkg/index.json", nil)
	req.Host = "localhost:7888"
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var result map[string][]string
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&result))
	assert.Equal(t, []string{"1.0.0"}, result["versions"])
}

func TestNuGetHandler_DownloadCached(t *testing.T) {
	hitCount := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v3-flatcontainer/mypkg/1.0.0/mypkg.1.0.0.nupkg" {
			hitCount++
			w.Write([]byte("fake nupkg bytes"))
		}
	}))
	defer upstream.Close()
	h := makeHandler(upstream, 7)

	r := chi.NewRouter()
	h.Mount(r)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/nuget/v3-flatcontainer/mypkg/1.0.0/mypkg.1.0.0.nupkg", nil)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code)
	}
	assert.Equal(t, 1, hitCount, "second request should come from cache")
}
