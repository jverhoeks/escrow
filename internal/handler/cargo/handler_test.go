package cargo_test

import (
	"encoding/json"
	"fmt"
	"io"
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
	"github.com/jverhoeks/escrow/internal/handler/cargo"
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/trust"
)

// cargoUpstreamServers groups the three mock upstream servers Cargo needs.
type cargoUpstreamServers struct {
	index    *httptest.Server // index.crates.io
	download *httptest.Server // static.crates.io
	api      *httptest.Server // crates.io API
}

func (s *cargoUpstreamServers) Close() {
	s.index.Close()
	s.download.Close()
	s.api.Close()
}

// makeUpstream creates mock upstream servers for index, download, and the crates.io API.
// oldCreatedAt and newCreatedAt are the publish times for version 1.0.0 and 0.9.0 respectively.
func makeUpstream(oldCreatedAt, newCreatedAt time.Time) *cargoUpstreamServers {
	// crates.io API server
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/api/v1/crates/") && strings.HasSuffix(r.URL.Path, "/versions") {
			json.NewEncoder(w).Encode(map[string]any{
				"versions": []any{
					map[string]any{
						"num":          "1.0.0",
						"created_at":   oldCreatedAt.UTC().Format(time.RFC3339),
						"published_by": map[string]any{"login": "testuser"},
					},
					map[string]any{
						"num":          "0.9.0",
						"created_at":   newCreatedAt.UTC().Format(time.RFC3339),
						"published_by": map[string]any{"login": "testuser"},
					},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))

	// index server (NDJSON lines for the crate)
	indexSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Path looks like /se/rd/serde or /3/s/syn etc.
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, `{"name":"testcrate","vers":"1.0.0","deps":[],"cksum":"abc123","yanked":false}`+"\n")
		fmt.Fprintf(w, `{"name":"testcrate","vers":"0.9.0","deps":[],"cksum":"def456","yanked":false}`+"\n")
	}))

	// download server
	dlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write([]byte("fake crate bytes"))
	}))

	return &cargoUpstreamServers{
		index:    indexSrv,
		download: dlSrv,
		api:      apiSrv,
	}
}

// makeHandler wires up a cargo.Handler against the mock servers with the given age policy.
func makeHandler(ups *cargoUpstreamServers, minAgeDays int) *cargo.Handler {
	c := cache.NewMemory()
	engine := trust.NewEngine(trust.NewAgeSignal(minAgeDays, nil))
	pol := policy.New(&config.PolicyConfig{
		Age: &config.AgePolicyConfig{MinDays: minAgeDays, Action: "block"},
	})
	evLog := eventlog.New(10)
	h := cargo.New(ups.index.Client(), engine, pol, c, evLog)
	// Override upstream URLs to point to our mocks.
	h.SetUpstreamURL(ups.index.URL)
	h.SetDownloadURL(ups.download.URL)
	h.SetAPIURL(ups.api.URL)
	return h
}

func TestCargoHandler_ServeConfig_ReturnsHost(t *testing.T) {
	ups := makeUpstream(
		time.Now().Add(-30*24*time.Hour),
		time.Now().Add(-30*24*time.Hour),
	)
	defer ups.Close()
	h := makeHandler(ups, 1)

	r := chi.NewRouter()
	h.Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/cargo/config.json", nil)
	req.Host = "proxy.example.com:7888"
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var cfg map[string]string
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&cfg))
	assert.Contains(t, cfg["dl"], "proxy.example.com:7888", "dl field must contain the request host")
	assert.Contains(t, cfg["dl"], "/cargo/crates/", "dl field must contain cargo crates path")
}

func TestCargoHandler_ServeIndex_FiltersBlockedVersions(t *testing.T) {
	// 1.0.0 is old enough (30 days), 0.9.0 is very new (1 day) — should be blocked with 99999-day policy.
	// Use a policy that blocks packages newer than 99999 days — both should be blocked. Actually,
	// per spec: 99999-day min_age for block-all tests. Here we want 1 blocked and 1 allowed:
	// 1.0.0 published 30 days ago → old enough with min 7 days
	// 0.9.0 published 2 days ago → too new with min 7 days
	ups := makeUpstream(
		time.Now().Add(-30*24*time.Hour), // 1.0.0: 30 days old
		time.Now().Add(-2*24*time.Hour),  // 0.9.0: 2 days old
	)
	defer ups.Close()
	h := makeHandler(ups, 7) // block if newer than 7 days

	r := chi.NewRouter()
	h.Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/cargo/te/st/testcrate", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	body, err := io.ReadAll(rr.Body)
	require.NoError(t, err)

	lines := nonEmptyLines(string(body))
	require.Len(t, lines, 1, "only 1 version should pass through (0.9.0 blocked)")

	var entry map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &entry))
	assert.Equal(t, "1.0.0", entry["vers"], "the old version must be kept")
}

func TestCargoHandler_ServeIndex_AllowsOldVersions(t *testing.T) {
	// Both versions published 30 days ago — both pass with 7-day min.
	ups := makeUpstream(
		time.Now().Add(-30*24*time.Hour),
		time.Now().Add(-30*24*time.Hour),
	)
	defer ups.Close()
	h := makeHandler(ups, 7)

	r := chi.NewRouter()
	h.Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/cargo/te/st/testcrate", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	body, err := io.ReadAll(rr.Body)
	require.NoError(t, err)

	lines := nonEmptyLines(string(body))
	assert.Len(t, lines, 2, "both versions should pass through")
}

func TestCargoHandler_ServeIndex_BlocksAllWithHighMinAge(t *testing.T) {
	// Both versions are "new" relative to a 99999-day minimum — both blocked.
	ups := makeUpstream(
		time.Now().Add(-30*24*time.Hour),
		time.Now().Add(-2*24*time.Hour),
	)
	defer ups.Close()
	h := makeHandler(ups, 99999)

	r := chi.NewRouter()
	h.Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/cargo/te/st/testcrate", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	body, err := io.ReadAll(rr.Body)
	require.NoError(t, err)

	lines := nonEmptyLines(string(body))
	assert.Len(t, lines, 0, "all versions should be blocked with 99999-day min age")
}

func TestCargoHandler_ServeDownload_Proxies(t *testing.T) {
	ups := makeUpstream(
		time.Now().Add(-30*24*time.Hour),
		time.Now().Add(-30*24*time.Hour),
	)
	defer ups.Close()
	h := makeHandler(ups, 1)

	r := chi.NewRouter()
	h.Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/cargo/crates/testcrate/1.0.0/download", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	body, err := io.ReadAll(rr.Body)
	require.NoError(t, err)
	assert.Equal(t, "fake crate bytes", string(body))
}

func TestCargoHandler_ServeDownload_Cached(t *testing.T) {
	// The download server is hit once; second request is served from cache.
	hitCount := 0
	dlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitCount++
		w.Write([]byte("cached crate bytes"))
	}))
	defer dlSrv.Close()

	ups := &cargoUpstreamServers{
		index:    httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})),
		download: dlSrv,
		api:      httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})),
	}
	defer ups.index.Close()
	defer ups.api.Close()

	h := makeHandler(ups, 1)
	r := chi.NewRouter()
	h.Mount(r)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/cargo/crates/testcrate/1.0.0/download", nil)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code)
	}
	assert.Equal(t, 1, hitCount, "download server should only be hit once — second request from cache")
}

// nonEmptyLines splits a string on newlines and returns non-empty lines.
func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}
