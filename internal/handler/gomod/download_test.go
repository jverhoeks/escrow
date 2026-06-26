package gomod_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jverhoeks/escrow/internal/block"
	"github.com/jverhoeks/escrow/internal/cache"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/jverhoeks/escrow/internal/handler/gomod"
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/trust"
)

// failBlobCache wraps a real cache but fails every SetBlob without draining the
// reader — simulating a disk-full / S3 error mid-stream.
type failBlobCache struct{ cache.Cache }

func (failBlobCache) SetBlob(context.Context, string, io.Reader) error {
	return errors.New("simulated disk full")
}

// A SetBlob failure mid-stream must fail the request promptly, not hang until
// WriteTimeout holding the client + upstream connections. See #45.
func TestGoModHandler_BlobWriteError_NoHang(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Repeat("Z", 1<<16)) // 64 KiB body
	}))
	defer upstream.Close()
	mem := cache.NewMemory()
	defer mem.Close()
	h := gomod.New(upstream.Client(), upstream.URL, trust.NewEngine(), policy.New(nil), failBlobCache{mem}, eventlog.New(10))

	r := chi.NewRouter()
	h.Mount(r)
	req := httptest.NewRequest(http.MethodGet, "/go/example.com/clean/@v/v1.0.0.zip", nil)
	rr := httptest.NewRecorder()

	done := make(chan struct{})
	go func() { r.ServeHTTP(rr, req); close(done) }()
	select {
	case <-done:
		// returned promptly — the pipe writer was unblocked on the cache error.
	case <-time.After(5 * time.Second):
		t.Fatal("handler hung after SetBlob failure (pipe writer not unblocked)")
	}
}

func TestGoModHandler_BlockedZip_403(t *testing.T) {
	c := cache.NewMemory()
	defer c.Close()
	bl, err := block.New("")
	require.NoError(t, err)
	require.NoError(t, bl.Add(block.Entry{Ecosystem: "go", Name: "example.com/mod", Version: "v1.0.0"}))
	pol := policy.New(nil).WithBlockList(bl)
	ev := eventlog.New(10)
	h := gomod.New(http.DefaultClient, "http://127.0.0.1:0", trust.NewEngine(), pol, c, ev)

	r := chi.NewRouter()
	h.Mount(r)
	req := httptest.NewRequest(http.MethodGet, "/go/example.com/mod/@v/v1.0.0.zip", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code)
	events := ev.Events("")
	require.Len(t, events, 1)
	assert.Equal(t, "block", events[0].Action)
	assert.Equal(t, "example.com/mod@v1.0.0", events[0].Package)
}

// A blocked version must not be served even from a warm cache — the download
// gate runs before the blob lookup. This is the core gap #35 closes (a version
// auto-blocked by a rescan after it was already cached).
func TestGoModHandler_BlockedZip_403_CacheHit(t *testing.T) {
	c := cache.NewMemory()
	defer c.Close()
	require.NoError(t, c.SetBlob(context.Background(), "go/zip/example.com/mod/@v/v1.0.0.zip", strings.NewReader("CACHED-ZIP")))
	bl, err := block.New("")
	require.NoError(t, err)
	require.NoError(t, bl.Add(block.Entry{Ecosystem: "go", Name: "example.com/mod", Version: "v1.0.0"}))
	pol := policy.New(nil).WithBlockList(bl)
	h := gomod.New(http.DefaultClient, "http://127.0.0.1:0", trust.NewEngine(), pol, c, eventlog.New(10))

	r := chi.NewRouter()
	h.Mount(r)
	req := httptest.NewRequest(http.MethodGet, "/go/example.com/mod/@v/v1.0.0.zip", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code)
	assert.NotContains(t, rr.Body.String(), "CACHED-ZIP")
}

func TestGoModHandler_CleanZip_200_RecordsAllow(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ZIP-BYTES")
	}))
	defer upstream.Close()
	c := cache.NewMemory()
	defer c.Close()
	ev := eventlog.New(10)
	h := gomod.New(upstream.Client(), upstream.URL, trust.NewEngine(), policy.New(nil), c, ev)

	r := chi.NewRouter()
	h.Mount(r)
	req := httptest.NewRequest(http.MethodGet, "/go/example.com/clean/@v/v1.0.0.zip", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "ZIP-BYTES", rr.Body.String())
	events := ev.Events("")
	require.Len(t, events, 1)
	assert.Equal(t, "allow", events[0].Action)
	assert.Equal(t, "example.com/clean@v1.0.0", events[0].Package)
}
