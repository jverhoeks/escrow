package nuget_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jverhoeks/escrow/internal/block"
	"github.com/jverhoeks/escrow/internal/cache"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/jverhoeks/escrow/internal/handler/nuget"
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/trust"
)

func TestNuGetHandler_BlockedPackage_403(t *testing.T) {
	c := cache.NewMemory()
	defer c.Close()
	bl, err := block.New("")
	require.NoError(t, err)
	require.NoError(t, bl.Add(block.Entry{Ecosystem: "nuget", Name: "mypkg", Version: "1.0.0"}))
	pol := policy.New(nil).WithBlockList(bl)
	ev := eventlog.New(10)
	h := nuget.New(http.DefaultClient, "http://127.0.0.1:0", trust.NewEngine(), pol, c, ev)

	r := chi.NewRouter()
	h.Mount(r)
	req := httptest.NewRequest(http.MethodGet, "/nuget/v3-flatcontainer/mypkg/1.0.0/mypkg.1.0.0.nupkg", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code)
	events := ev.Events("")
	require.Len(t, events, 1)
	assert.Equal(t, "block", events[0].Action)
	assert.Equal(t, "mypkg@1.0.0", events[0].Package)
}

// A blocked package must not be served even from a warm cache — the download
// gate runs before the blob lookup. This is the core gap #35 closes.
func TestNuGetHandler_BlockedPackage_403_CacheHit(t *testing.T) {
	c := cache.NewMemory()
	defer c.Close()
	require.NoError(t, c.SetBlob(context.Background(), "nuget/pkgs/mypkg/1.0.0/mypkg.1.0.0.nupkg", strings.NewReader("CACHED-NUPKG")))
	bl, err := block.New("")
	require.NoError(t, err)
	require.NoError(t, bl.Add(block.Entry{Ecosystem: "nuget", Name: "mypkg", Version: "1.0.0"}))
	pol := policy.New(nil).WithBlockList(bl)
	h := nuget.New(http.DefaultClient, "http://127.0.0.1:0", trust.NewEngine(), pol, c, eventlog.New(10))

	r := chi.NewRouter()
	h.Mount(r)
	req := httptest.NewRequest(http.MethodGet, "/nuget/v3-flatcontainer/mypkg/1.0.0/mypkg.1.0.0.nupkg", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code)
	assert.NotContains(t, rr.Body.String(), "CACHED-NUPKG")
}

func TestNuGetHandler_CleanPackage_200_RecordsAllow(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "NUPKG-BYTES")
	}))
	defer upstream.Close()
	c := cache.NewMemory()
	defer c.Close()
	ev := eventlog.New(10)
	h := nuget.New(upstream.Client(), upstream.URL, trust.NewEngine(), policy.New(nil), c, ev)
	h.SetFlatcontainerURL(upstream.URL)

	r := chi.NewRouter()
	h.Mount(r)
	req := httptest.NewRequest(http.MethodGet, "/nuget/v3-flatcontainer/mypkg/1.0.0/mypkg.1.0.0.nupkg", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "NUPKG-BYTES", rr.Body.String())
	events := ev.Events("")
	require.Len(t, events, 1)
	assert.Equal(t, "allow", events[0].Action)
	assert.Equal(t, "mypkg@1.0.0", events[0].Package)
}
