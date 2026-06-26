package npm_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jverhoeks/escrow/internal/block"
	"github.com/jverhoeks/escrow/internal/cache"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/jverhoeks/escrow/internal/handler/npm"
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/trust"
)

func tgzUpstream(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		io.WriteString(w, body)
	}))
}

func blocklistWith(t *testing.T, e block.Entry) *block.List {
	t.Helper()
	bl, err := block.New("")
	require.NoError(t, err)
	require.NoError(t, bl.Add(e))
	return bl
}

// A blocklisted version must 403 on the artifact endpoint even on a cold cache
// (cache-miss path), and never be fetched/served.
func TestNPMHandler_BlockedTarball_403_CacheMiss(t *testing.T) {
	upstream := tgzUpstream(t, "TARBALL-BYTES")
	defer upstream.Close()
	c := cache.NewMemory()
	defer c.Close()
	pol := policy.New(nil).WithBlockList(blocklistWith(t, block.Entry{Ecosystem: "npm", Name: "lodash", Version: "4.17.21"}))
	ev := eventlog.New(10)
	h := npm.New(upstream.Client(), upstream.URL, trust.NewEngine(), pol, c, ev)

	req := httptest.NewRequest(http.MethodGet, "/lodash/-/lodash-4.17.21.tgz", nil)
	rr := httptest.NewRecorder()
	h.ServeTarball(rr, req, "lodash", "lodash-4.17.21.tgz")

	require.Equal(t, http.StatusForbidden, rr.Code)
	assert.NotContains(t, rr.Body.String(), "TARBALL-BYTES", "blocked artifact must not be served")
	events := ev.Events("")
	require.Len(t, events, 1)
	assert.Equal(t, "block", events[0].Action)
	assert.Equal(t, eventlog.KindDownloaded, events[0].Kind)
}

// A blocklisted version must 403 even when its blob is already cached — the gate
// runs before the cache-hit serve.
func TestNPMHandler_BlockedTarball_403_CacheHit(t *testing.T) {
	c := cache.NewMemory()
	defer c.Close()
	require.NoError(t, c.SetBlob(context.Background(), "npm/lodash/-/lodash-4.17.21.tgz", strings.NewReader("CACHED-BYTES")))
	pol := policy.New(nil).WithBlockList(blocklistWith(t, block.Entry{Ecosystem: "npm", Name: "lodash", Version: "4.17.21"}))
	ev := eventlog.New(10)
	// No upstream needed; if the gate fails to short-circuit, the nil client would surface.
	h := npm.New(http.DefaultClient, "http://127.0.0.1:0", trust.NewEngine(), pol, c, ev)

	req := httptest.NewRequest(http.MethodGet, "/lodash/-/lodash-4.17.21.tgz", nil)
	rr := httptest.NewRecorder()
	h.ServeTarball(rr, req, "lodash", "lodash-4.17.21.tgz")

	require.Equal(t, http.StatusForbidden, rr.Code)
	assert.NotContains(t, rr.Body.String(), "CACHED-BYTES", "cached blocked artifact must not be served")
}

// A clean (unblocked) version is served and recorded as an allow download event.
func TestNPMHandler_CleanTarball_200_RecordsAllow(t *testing.T) {
	upstream := tgzUpstream(t, "GOOD-BYTES")
	defer upstream.Close()
	c := cache.NewMemory()
	defer c.Close()
	pol := policy.New(nil) // nothing blocked
	ev := eventlog.New(10)
	h := npm.New(upstream.Client(), upstream.URL, trust.NewEngine(), pol, c, ev)

	req := httptest.NewRequest(http.MethodGet, "/lodash/-/lodash-4.17.21.tgz", nil)
	rr := httptest.NewRecorder()
	h.ServeTarball(rr, req, "lodash", "lodash-4.17.21.tgz")

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "GOOD-BYTES", rr.Body.String())
	events := ev.Events("")
	require.Len(t, events, 1)
	assert.Equal(t, "allow", events[0].Action)
	assert.Equal(t, eventlog.KindDownloaded, events[0].Kind)
	assert.Equal(t, "lodash@4.17.21", events[0].Package)
}
