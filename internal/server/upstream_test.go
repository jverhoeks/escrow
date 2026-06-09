package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/jverhoeks/escrow/internal/metrics"
	"github.com/jverhoeks/escrow/internal/upstreamlog"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestLoggingTransport_RecordsKnownHostsOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	host := u.Hostname() // typically "127.0.0.1"

	ul := upstreamlog.New(10)

	// Unknown host (empty map) → not recorded.
	c1 := NewLoggingClientWithRecorder(srv.Client(), zerolog.Nop(), ul, map[string]string{})
	resp, err := c1.Get(srv.URL)
	require.NoError(t, err)
	resp.Body.Close()
	require.Len(t, ul.Events(""), 0)

	// Known host (the test server's real host mapped) → recorded. hostEco is baked
	// into the transport at construction, so a second client is needed.
	c2 := NewLoggingClientWithRecorder(srv.Client(), zerolog.Nop(), ul, map[string]string{host: "npm"})
	resp2, err := c2.Get(srv.URL)
	require.NoError(t, err)
	resp2.Body.Close()

	evs := ul.Events("")
	require.Len(t, evs, 1)
	require.Equal(t, "npm", evs[0].Ecosystem)
	require.Equal(t, 200, evs[0].Status)
}

// TestLoggingTransport_RecordsConnReuseMetrics verifies the #17 upstream
// connection-pool instrumentation: per-ecosystem reuse is counted (first request
// dials → reused=false, second reuses the pooled idle conn → reused=true) and the
// acquisition-latency histogram is emitted. It asserts emission + labeling, not a
// reuse ratio — under a healthy pool reuse is ~100%, which is the correct result.
func TestLoggingTransport_RecordsConnReuseMetrics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	host := u.Hostname()

	metrics.UpstreamConnReused.Reset()
	metrics.UpstreamConnAcquireSeconds.Reset()

	// One client (one underlying transport, so the idle pool persists across the
	// two requests). Its own *http.Transport isolates the pool to this test.
	c := NewLoggingClientWithRecorder(&http.Client{Transport: &http.Transport{}}, zerolog.Nop(), nil, map[string]string{host: "testeco"})

	for i := 0; i < 2; i++ {
		resp, err := c.Get(srv.URL)
		require.NoError(t, err)
		_, _ = io.Copy(io.Discard, resp.Body) // drain so the conn returns to the idle pool
		require.NoError(t, resp.Body.Close())
	}

	require.GreaterOrEqual(t, testutil.ToFloat64(metrics.UpstreamConnReused.WithLabelValues("testeco", "false")), 1.0,
		"first request should dial a new connection (reused=false)")
	require.GreaterOrEqual(t, testutil.ToFloat64(metrics.UpstreamConnReused.WithLabelValues("testeco", "true")), 1.0,
		"second request should reuse the pooled idle connection (reused=true)")
	require.GreaterOrEqual(t, testutil.CollectAndCount(metrics.UpstreamConnAcquireSeconds, "escrow_upstream_conn_acquire_seconds"), 1,
		"acquisition-latency histogram should be emitted for the ecosystem")
}
