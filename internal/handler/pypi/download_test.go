package pypi_test

import (
	"context"
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
