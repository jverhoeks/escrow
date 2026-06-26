package npm_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jverhoeks/escrow/internal/cache"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/jverhoeks/escrow/internal/handler/npm"
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/trust"
	"github.com/jverhoeks/escrow/internal/upstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #73: a slow metadata fetch is bounded by the metadata timeout, but a slower
// blob (tarball) download is NOT — blobs intentionally have no total timeout.
func TestNpmHandler_MetadataTimeoutButBlobUnbounded(t *testing.T) {
	orig := upstream.MetadataTimeout
	upstream.MetadataTimeout = 100 * time.Millisecond
	defer func() { upstream.MetadataTimeout = orig }()

	// Metadata upstream that trickles past the timeout.
	slowMeta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		io.WriteString(w, `{"versions":{}}`)
	}))
	defer slowMeta.Close()
	hMeta := npm.New(slowMeta.Client(), slowMeta.URL, trust.NewEngine(), policy.New(nil), cache.NewMemory(), eventlog.New(10))

	start := time.Now()
	rr := httptest.NewRecorder()
	hMeta.ServeManifest(rr, httptest.NewRequest(http.MethodGet, "/lodash", nil), "lodash")
	require.Equal(t, http.StatusBadGateway, rr.Code, "slow metadata fetch must time out → 502")
	require.Less(t, time.Since(start), 400*time.Millisecond, "should return at ~the metadata timeout, not wait for the body")

	// Blob upstream slower than the metadata timeout — must still serve (no total timeout on blobs).
	slowBlob := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(250 * time.Millisecond)
		io.WriteString(w, "TGZ-BYTES")
	}))
	defer slowBlob.Close()
	hBlob := npm.New(slowBlob.Client(), slowBlob.URL, trust.NewEngine(), policy.New(nil), cache.NewMemory(), eventlog.New(10))
	rr2 := httptest.NewRecorder()
	hBlob.ServeTarball(rr2, httptest.NewRequest(http.MethodGet, "/lodash/-/lodash-4.17.21.tgz", nil), "lodash", "lodash-4.17.21.tgz")
	require.Equal(t, http.StatusOK, rr2.Code, "blob slower than the metadata timeout must still serve")
	assert.Equal(t, "TGZ-BYTES", rr2.Body.String())
}
