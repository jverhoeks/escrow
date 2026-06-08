package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jverhoeks/escrow/internal/egresslog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleEgressLog(t *testing.T) {
	el := egresslog.New(10)
	el.Record(egresslog.Event{Host: "a.com", Action: "allow", Verb: "CONNECT"})
	el.Record(egresslog.Event{Host: "evil.com", Action: "block", Verb: "CONNECT"})
	d := &Dashboard{egressLog: el}
	req := httptest.NewRequest(http.MethodGet, "/api/egresslog?action=block", nil)
	rr := httptest.NewRecorder()
	d.handleEgressLog(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	var out []egresslog.Event
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&out))
	require.Len(t, out, 1)
	assert.Equal(t, "evil.com", out[0].Host)
}

func TestHandleEgressTimeseries(t *testing.T) {
	el := egresslog.New(10)
	el.Record(egresslog.Event{Host: "a.com", Action: "allow", Verb: "CONNECT"})
	d := &Dashboard{egressLog: el}
	req := httptest.NewRequest(http.MethodGet, "/api/egress/stats/timeseries", nil)
	rr := httptest.NewRecorder()
	d.handleEgressTimeseries(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	var s egresslog.Stats
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&s))
	assert.Equal(t, 1, s.Total)
}

// TestHandleEgressStream_NilLog verifies a 200 SSE response is sent even when
// egressLog is nil (the connection terminates once context is cancelled).
func TestHandleEgressStream_NilLog(t *testing.T) {
	d := &Dashboard{}
	req := httptest.NewRequest(http.MethodGet, "/api/egress/stream", nil)
	rr := httptest.NewRecorder()
	d.handleEgressStream(rr, req)
	// nil guard returns 503 when no egressLog
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

// TestHandleEgressTimeseries_DefaultWindow tests parsing with default params.
func TestHandleEgressTimeseries_DefaultWindow(t *testing.T) {
	el := egresslog.New(10)
	// Record event within default 24h window
	el.Record(egresslog.Event{Host: "x.io", Action: "block", Verb: "CONNECT", Timestamp: time.Now().UTC()})
	d := &Dashboard{egressLog: el}
	req := httptest.NewRequest(http.MethodGet, "/api/egress/stats/timeseries?window=1h&bucket=5m", nil)
	rr := httptest.NewRecorder()
	d.handleEgressTimeseries(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	var s egresslog.Stats
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&s))
	assert.Equal(t, 1, s.Total)
	assert.Equal(t, 1, s.Blocked)
}
