package maven_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/cache"
	"github.com/jverhoeks/escrow/internal/config"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/jverhoeks/escrow/internal/handler/maven"
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/trust"
)

const sampleMetadata = `<?xml version="1.0" encoding="UTF-8"?>
<metadata>
  <groupId>com.example</groupId>
  <artifactId>mylib</artifactId>
  <versioning>
    <latest>2.0.0</latest>
    <release>2.0.0</release>
    <versions>
      <version>1.0.0</version>
      <version>2.0.0</version>
    </versions>
    <lastUpdated>20230101000000</lastUpdated>
  </versioning>
</metadata>`

type upstreamServers struct {
	maven  *httptest.Server
	search *httptest.Server
}

func (u *upstreamServers) Close() {
	u.maven.Close()
	u.search.Close()
}

// makeTestServers creates mock Maven upstream and search API servers.
// oldVersionTimestamp and newVersionTimestamp are publish times for 1.0.0 and 2.0.0.
func makeTestServers(oldTs, newTs time.Time, downloadHit *int) *upstreamServers {
	mavenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "maven-metadata.xml") {
			w.Header().Set("Content-Type", "application/xml")
			w.Write([]byte(sampleMetadata))
			return
		}
		if downloadHit != nil {
			*downloadHit++
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write([]byte("fake jar bytes"))
	}))

	searchSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]any{
			"response": map[string]any{
				"docs": []any{
					map[string]any{"v": "1.0.0", "timestamp": oldTs.UnixMilli()},
					map[string]any{"v": "2.0.0", "timestamp": newTs.UnixMilli()},
				},
			},
		}
		json.NewEncoder(w).Encode(response)
	}))

	return &upstreamServers{maven: mavenSrv, search: searchSrv}
}

func makeHandler(ups *upstreamServers, minAgeDays int) *maven.Handler {
	c := cache.NewMemory()
	engine := trust.NewEngine(trust.NewAgeSignal(minAgeDays, nil))
	pol := policy.New(&config.PolicyConfig{
		Age: &config.AgePolicyConfig{MinDays: minAgeDays, Action: "block"},
	})
	evLog := eventlog.New(10)
	h := maven.New(ups.maven.Client(), ups.maven.URL, engine, pol, c, evLog)
	h.SetSearchURL(ups.search.URL)
	return h
}

func TestMavenHandler_MetadataFiltersNewVersions(t *testing.T) {
	oldTs := time.Now().Add(-30 * 24 * time.Hour)
	newTs := time.Now().Add(-1 * 24 * time.Hour) // too new
	ups := makeTestServers(oldTs, newTs, nil)
	defer ups.Close()

	h := makeHandler(ups, 7)
	r := chi.NewRouter()
	h.Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/maven2/com/example/mylib/maven-metadata.xml", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	body := rr.Body.String()
	assert.Contains(t, body, "1.0.0", "old version should be present")
	assert.NotContains(t, body, "2.0.0", "new version should be filtered")
	assert.Contains(t, body, "<latest>1.0.0</latest>", "latest should be updated to last allowed version")
}

func TestMavenHandler_MetadataAllPassWhenAllOld(t *testing.T) {
	oldTs := time.Now().Add(-30 * 24 * time.Hour)
	ups := makeTestServers(oldTs, oldTs, nil)
	defer ups.Close()

	h := makeHandler(ups, 7)
	r := chi.NewRouter()
	h.Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/maven2/com/example/mylib/maven-metadata.xml", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	body := rr.Body.String()
	assert.Contains(t, body, "1.0.0")
	assert.Contains(t, body, "2.0.0")
}

func TestMavenHandler_MetadataAllVersionsBlockedClearsLatestRelease(t *testing.T) {
	// When all versions are newer than min_days, <latest> and <release> must be cleared.
	// If they're left pointing at blocked versions, Maven/Gradle try to resolve
	// a version not in <versions> and 404.
	newTs := time.Now().Add(-1 * 24 * time.Hour) // too new
	ups := makeTestServers(newTs, newTs, nil)      // both versions too new
	defer ups.Close()

	h := makeHandler(ups, 7)
	r := chi.NewRouter()
	h.Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/maven2/com/example/mylib/maven-metadata.xml", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	body := rr.Body.String()
	assert.NotContains(t, body, "1.0.0", "all versions should be filtered")
	assert.NotContains(t, body, "2.0.0", "all versions should be filtered")
	assert.NotContains(t, body, "<latest>1.0.0</latest>", "<latest> must not reference a blocked version")
	assert.NotContains(t, body, "<release>2.0.0</release>", "<release> must not reference a blocked version")
}

func TestMavenHandler_ArtifactCached(t *testing.T) {
	hitCount := 0
	ups := makeTestServers(time.Now(), time.Now(), &hitCount)
	defer ups.Close()

	h := makeHandler(ups, 7)
	r := chi.NewRouter()
	h.Mount(r)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/maven2/com/example/mylib/1.0.0/mylib-1.0.0.jar", nil)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code)
	}
	assert.Equal(t, 1, hitCount, "second request should come from cache")
}

func TestMavenHandler_ChecksumProxied(t *testing.T) {
	// .sha1 and .md5 files should be proxied as artifacts (no age filtering).
	checksumSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "abc123")
	}))
	defer checksumSrv.Close()
	searchSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"docs": []any{}}})
	}))
	defer searchSrv.Close()

	c := cache.NewMemory()
	engine := trust.NewEngine()
	pol := policy.New(nil)
	h := maven.New(checksumSrv.Client(), checksumSrv.URL, engine, pol, c, nil)
	h.SetSearchURL(searchSrv.URL)

	r := chi.NewRouter()
	h.Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/maven2/com/example/mylib/1.0.0/mylib-1.0.0.jar.sha1", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "abc123", rr.Body.String())
}
