package gomod_test

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
	"github.com/jverhoeks/escrow/internal/handler/gomod"
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/trust"
)

func makeGoUpstream(version string, publishedAt time.Time) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/golang.org/x/text/@v/list":
			fmt.Fprintf(w, "%s\n", version)
		case r.URL.Path == fmt.Sprintf("/golang.org/x/text/@v/%s.info", version):
			json.NewEncoder(w).Encode(map[string]any{
				"Version": version,
				"Time":    publishedAt.UTC().Format(time.RFC3339),
			})
		case r.URL.Path == fmt.Sprintf("/golang.org/x/text/@v/%s.mod", version):
			fmt.Fprintf(w, "module golang.org/x/text\n\ngo 1.17\n")
		case r.URL.Path == "/golang.org/x/text/@latest":
			json.NewEncoder(w).Encode(map[string]any{
				"Version": version,
				"Time":    publishedAt.UTC().Format(time.RFC3339),
			})
		default:
			http.NotFound(w, r)
		}
	}))
}

func makeGoHandler(upstream *httptest.Server, minAgeDays int) *gomod.Handler {
	c := cache.NewMemory()
	engine := trust.NewEngine(trust.NewAgeSignal(minAgeDays, nil))
	pol := policy.New(&config.PolicyConfig{
		Age: &config.AgePolicyConfig{MinDays: minAgeDays, Action: "block"},
	})
	evLog := eventlog.New(10)
	return gomod.New(upstream.Client(), upstream.URL, engine, pol, c, evLog)
}

func TestGomodHandler_InfoPassOldModule(t *testing.T) {
	upstream := makeGoUpstream("v0.3.0", time.Now().Add(-30*24*time.Hour))
	defer upstream.Close()
	h := makeGoHandler(upstream, 7)

	r := chi.NewRouter()
	h.Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/go/golang.org/x/text/@v/v0.3.0.info", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var info map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&info))
	assert.Equal(t, "v0.3.0", info["Version"])
}

func TestGomodHandler_InfoBlockNewModule(t *testing.T) {
	upstream := makeGoUpstream("v0.14.0", time.Now().Add(-2*24*time.Hour))
	defer upstream.Close()
	h := makeGoHandler(upstream, 7)

	r := chi.NewRouter()
	h.Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/go/golang.org/x/text/@v/v0.14.0.info", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestGomodHandler_ListPassThrough(t *testing.T) {
	upstream := makeGoUpstream("v0.3.0", time.Now().Add(-30*24*time.Hour))
	defer upstream.Close()
	h := makeGoHandler(upstream, 7)

	r := chi.NewRouter()
	h.Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/go/golang.org/x/text/@v/list", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	body, _ := io.ReadAll(rr.Body)
	assert.Contains(t, string(body), "v0.3.0")
}

func TestGomodHandler_ModPassThrough(t *testing.T) {
	upstream := makeGoUpstream("v0.3.0", time.Now().Add(-30*24*time.Hour))
	defer upstream.Close()
	h := makeGoHandler(upstream, 7)

	r := chi.NewRouter()
	h.Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/go/golang.org/x/text/@v/v0.3.0.mod", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	body, _ := io.ReadAll(rr.Body)
	assert.Contains(t, string(body), "module golang.org/x/text")
}

func TestGomodHandler_LatestBlockNew(t *testing.T) {
	upstream := makeGoUpstream("v1.0.0", time.Now().Add(-1*24*time.Hour))
	defer upstream.Close()
	h := makeGoHandler(upstream, 7)

	r := chi.NewRouter()
	h.Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/go/golang.org/x/text/@latest", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestGomodHandler_UnescapesModulePath(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/github.com/!burnt!sushi/toml/@v/v1.6.0.info" {
			json.NewEncoder(w).Encode(map[string]any{
				"Version": "v1.6.0",
				"Time":    time.Now().Add(-100 * 24 * time.Hour).UTC().Format(time.RFC3339),
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()
	h := makeGoHandler(upstream, 7)

	r := chi.NewRouter()
	h.Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/go/github.com/!burnt!sushi/toml/@v/v1.6.0.info", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var info map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&info))
	assert.Equal(t, "v1.6.0", info["Version"])
}
