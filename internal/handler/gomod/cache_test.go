package gomod_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGomodHandler_InfoCacheHit(t *testing.T) {
	var hitCount int64
	pub := time.Now().Add(-30 * 24 * time.Hour)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/golang.org/x/text/@v/v0.3.0.info" {
			atomic.AddInt64(&hitCount, 1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"Version":"v0.3.0","Time":"%s"}`, pub.UTC().Format(time.RFC3339))
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()
	h := makeGoHandler(upstream, 7)
	r := chi.NewRouter()
	h.Mount(r)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/go/golang.org/x/text/@v/v0.3.0.info", nil)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code)
	}
	assert.Equal(t, int64(1), atomic.LoadInt64(&hitCount), "second .info request should be served from cache")
}

func TestGomodHandler_ModCacheHit(t *testing.T) {
	var hitCount int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/golang.org/x/text/@v/v0.3.0.mod" {
			atomic.AddInt64(&hitCount, 1)
			fmt.Fprint(w, "module golang.org/x/text\n\ngo 1.17\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()
	h := makeGoHandler(upstream, 7)
	r := chi.NewRouter()
	h.Mount(r)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/go/golang.org/x/text/@v/v0.3.0.mod", nil)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code)
	}
	assert.Equal(t, int64(1), atomic.LoadInt64(&hitCount), "second .mod request should be served from cache")
}

func TestGomodHandler_LatestCacheHit(t *testing.T) {
	var hitCount int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hitCount, 1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"Version":"v1.0.0","Time":"%s"}`,
			time.Now().Add(-30*24*time.Hour).UTC().Format(time.RFC3339))
	}))
	defer upstream.Close()
	h := makeGoHandler(upstream, 7)
	r := chi.NewRouter()
	h.Mount(r)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/go/golang.org/x/text/@latest", nil)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code)
	}
	assert.Equal(t, int64(1), atomic.LoadInt64(&hitCount), "second @latest request should be served from cache")
}

func TestGomodHandler_BlockedResponseHasJSONContentType(t *testing.T) {
	upstream := makeGoUpstream("v0.14.0", time.Now().Add(-1*24*time.Hour)) // too new → blocked
	defer upstream.Close()
	h := makeGoHandler(upstream, 7)
	r := chi.NewRouter()
	h.Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/go/golang.org/x/text/@v/v0.14.0.info", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))
}

func TestGomodHandler_ZipCacheHit(t *testing.T) {
	var hitCount int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hitCount, 1)
		w.Header().Set("Content-Type", "application/zip")
		io.WriteString(w, "fake zip bytes")
	}))
	defer upstream.Close()
	h := makeGoHandler(upstream, 7)
	r := chi.NewRouter()
	h.Mount(r)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/go/golang.org/x/text/@v/v0.3.0.zip", nil)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code)
	}
	assert.Equal(t, int64(1), atomic.LoadInt64(&hitCount), "second .zip request should be served from cache")
}
