package pypi_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jverhoeks/escrow/internal/block"
	"github.com/jverhoeks/escrow/internal/cache"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/jverhoeks/escrow/internal/handler/pypi"
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/trust"
)

func pypiBlocklist(t *testing.T, e block.Entry) *block.List {
	t.Helper()
	bl, err := block.New("")
	require.NoError(t, err)
	require.NoError(t, bl.Add(e))
	return bl
}

const wheel = "requests-2.31.0-py3-none-any.whl"

func TestPyPIHandler_BlockedFile_403_CacheMiss(t *testing.T) {
	c := cache.NewMemory()
	defer c.Close()
	pol := policy.New(nil).WithBlockList(pypiBlocklist(t, block.Entry{Ecosystem: "pypi", Name: "requests", Version: "2.31.0"}))
	ev := eventlog.New(10)
	h := pypi.New(http.DefaultClient, "http://127.0.0.1:0", trust.NewEngine(), pol, c, false, ev)

	req := httptest.NewRequest(http.MethodGet, "/pypi/packages/"+wheel, nil)
	rr := httptest.NewRecorder()
	h.ServeFile(rr, req, wheel)

	require.Equal(t, http.StatusForbidden, rr.Code)
	events := ev.Events("")
	require.Len(t, events, 1)
	assert.Equal(t, "block", events[0].Action)
	assert.Equal(t, "requests@2.31.0", events[0].Package)
}

func TestPyPIHandler_BlockedFile_403_CacheHit(t *testing.T) {
	c := cache.NewMemory()
	defer c.Close()
	require.NoError(t, c.SetBlob(context.Background(), "pypi/packages/"+wheel, strings.NewReader("CACHED-WHEEL")))
	pol := policy.New(nil).WithBlockList(pypiBlocklist(t, block.Entry{Ecosystem: "pypi", Name: "requests", Version: "2.31.0"}))
	h := pypi.New(http.DefaultClient, "http://127.0.0.1:0", trust.NewEngine(), pol, c, false, eventlog.New(10))

	req := httptest.NewRequest(http.MethodGet, "/pypi/packages/"+wheel, nil)
	rr := httptest.NewRecorder()
	h.ServeFile(rr, req, wheel)

	require.Equal(t, http.StatusForbidden, rr.Code)
	assert.NotContains(t, rr.Body.String(), "CACHED-WHEEL")
}

func TestPyPIHandler_CleanFile_200_RecordsAllow(t *testing.T) {
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "WHEEL-BYTES")
	}))
	defer cdn.Close()
	c := cache.NewMemory()
	defer c.Close()
	// Pre-resolve the CDN URL the way a prior index fetch would have.
	require.NoError(t, c.SetMeta(context.Background(), "pypi/fileurl/"+wheel, []byte(cdn.URL), time.Hour))
	ev := eventlog.New(10)
	h := pypi.New(cdn.Client(), cdn.URL, trust.NewEngine(), policy.New(nil), c, false, ev)

	req := httptest.NewRequest(http.MethodGet, "/pypi/packages/"+wheel, nil)
	rr := httptest.NewRecorder()
	h.ServeFile(rr, req, wheel)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "WHEEL-BYTES", rr.Body.String())
	events := ev.Events("")
	require.Len(t, events, 1)
	assert.Equal(t, "allow", events[0].Action)
}

// #46: a wheel whose bytes don't match the upstream-declared sha256 must be
// rejected — not served and not cached.
func TestPyPIHandler_DigestMismatch_Rejected(t *testing.T) {
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "WHEEL-BYTES")
	}))
	defer cdn.Close()
	c := cache.NewMemory()
	defer c.Close()
	ctx := context.Background()
	require.NoError(t, c.SetMeta(ctx, "pypi/fileurl/"+wheel, []byte(cdn.URL), time.Hour))
	require.NoError(t, c.SetMeta(ctx, "pypi/digest/"+wheel, []byte("0000000000000000000000000000000000000000000000000000000000000000"), time.Hour))
	h := pypi.New(cdn.Client(), cdn.URL, trust.NewEngine(), policy.New(nil), c, false, eventlog.New(10))

	rr := httptest.NewRecorder()
	h.ServeFile(rr, httptest.NewRequest(http.MethodGet, "/pypi/packages/"+wheel, nil), wheel)

	require.Equal(t, http.StatusBadGateway, rr.Code, "mismatch must be rejected")
	assert.NotContains(t, rr.Body.String(), "WHEEL-BYTES", "bad bytes must not be served")
	blob, _ := c.GetBlob(ctx, "pypi/packages/"+wheel)
	assert.Nil(t, blob, "bad bytes must not be cached")
}

// #46: a wheel whose bytes match the declared sha256 is served and cached.
func TestPyPIHandler_DigestMatch_ServedAndCached(t *testing.T) {
	const body = "WHEEL-BYTES"
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, body)
	}))
	defer cdn.Close()
	c := cache.NewMemory()
	defer c.Close()
	ctx := context.Background()
	digest := sha256.Sum256([]byte(body))
	require.NoError(t, c.SetMeta(ctx, "pypi/fileurl/"+wheel, []byte(cdn.URL), time.Hour))
	require.NoError(t, c.SetMeta(ctx, "pypi/digest/"+wheel, []byte(hex.EncodeToString(digest[:])), time.Hour))
	h := pypi.New(cdn.Client(), cdn.URL, trust.NewEngine(), policy.New(nil), c, false, eventlog.New(10))

	rr := httptest.NewRecorder()
	h.ServeFile(rr, httptest.NewRequest(http.MethodGet, "/pypi/packages/"+wheel, nil), wheel)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, body, rr.Body.String())
	blob, _ := c.GetBlob(ctx, "pypi/packages/"+wheel)
	require.NotNil(t, blob, "verified bytes must be cached")
	blob.Close()
}

// #46: with no declared digest (pinned/cold fetch, old release), serve unverified
// (fail open) rather than rejecting legitimate traffic.
func TestPyPIHandler_NoDigest_FailsOpen(t *testing.T) {
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "WHEEL-BYTES")
	}))
	defer cdn.Close()
	c := cache.NewMemory()
	defer c.Close()
	require.NoError(t, c.SetMeta(context.Background(), "pypi/fileurl/"+wheel, []byte(cdn.URL), time.Hour))
	h := pypi.New(cdn.Client(), cdn.URL, trust.NewEngine(), policy.New(nil), c, false, eventlog.New(10))

	rr := httptest.NewRecorder()
	h.ServeFile(rr, httptest.NewRequest(http.MethodGet, "/pypi/packages/"+wheel, nil), wheel)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "WHEEL-BYTES", rr.Body.String())
}

// #35/#54 gate-bypass regression: a code-bearing artifact whose name/version
// can't be parsed (e.g. a legacy .egg/.exe/.msi distribution) must fail closed —
// it cannot be policy-checked, so it must not be served even from a warm cache.
// Previously the gate was skipped when coords were empty, serving the bytes 200.
func TestPyPIHandler_UnparseableArtifact_FailsClosed_CacheHit(t *testing.T) {
	const egg = "evil-1.0-py3.7.egg"
	c := cache.NewMemory()
	defer c.Close()
	require.NoError(t, c.SetBlob(context.Background(), "pypi/packages/"+egg, strings.NewReader("MALICIOUS-EGG-BYTES")))
	// No blocklist entry at all: an unidentifiable artifact must still fail closed.
	h := pypi.New(http.DefaultClient, "http://127.0.0.1:0", trust.NewEngine(), policy.New(nil), c, false, eventlog.New(10))

	rr := httptest.NewRecorder()
	h.ServeFile(rr, httptest.NewRequest(http.MethodGet, "/pypi/packages/"+egg, nil), egg)

	require.Equal(t, http.StatusForbidden, rr.Code, "unparseable artifact must fail closed, not serve ungated bytes")
	assert.NotContains(t, rr.Body.String(), "MALICIOUS-EGG-BYTES")
}

// #67: a blocklisted package with a hyphenated name must be 403'd on its sdist.
// The old first-'-' split parsed "django-allauth-0.50.0.tar.gz" as name
// "django", so gate.Check never matched the "django-allauth" blocklist entry.
func TestPyPIHandler_BlockedHyphenatedSdist_403(t *testing.T) {
	const sdist = "django-allauth-0.50.0.tar.gz"
	c := cache.NewMemory()
	defer c.Close()
	pol := policy.New(nil).WithBlockList(pypiBlocklist(t, block.Entry{Ecosystem: "pypi", Name: "django-allauth", Version: "0.50.0"}))
	ev := eventlog.New(10)
	h := pypi.New(http.DefaultClient, "http://127.0.0.1:0", trust.NewEngine(), pol, c, false, ev)

	rr := httptest.NewRecorder()
	h.ServeFile(rr, httptest.NewRequest(http.MethodGet, "/pypi/packages/"+sdist, nil), sdist)

	require.Equal(t, http.StatusForbidden, rr.Code, "blocked hyphenated sdist must be rejected")
	events := ev.Events("")
	require.Len(t, events, 1)
	assert.Equal(t, "block", events[0].Action)
	assert.Equal(t, "django-allauth@0.50.0", events[0].Package)
}
