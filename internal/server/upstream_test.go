package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/jverhoeks/escrow/internal/upstreamlog"
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
