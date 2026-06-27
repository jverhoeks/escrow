package npm_test

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/jverhoeks/escrow/internal/cache"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/jverhoeks/escrow/internal/handler/npm"
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/trust"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #98: an unsafe package name (traversal / injection chars) is rejected with 400
// before any upstream fetch.
func TestNpmHandler_RejectsUnsafePackageName(t *testing.T) {
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
	}))
	defer upstream.Close()
	h := npm.New(upstream.Client(), upstream.URL, trust.NewEngine(), policy.New(nil), cache.NewMemory(), eventlog.New(10))

	for _, bad := range []string{"../etc/passwd", "pkg?evil=1", "a/../b"} {
		rr := httptest.NewRecorder()
		h.ServeManifest(rr, httptest.NewRequest(http.MethodGet, "/x", nil), bad)
		assert.Equal(t, http.StatusBadRequest, rr.Code, "name %q must be rejected", bad)
	}
	require.Zero(t, atomic.LoadInt32(&hits), "upstream must not be contacted for an invalid name")
}
