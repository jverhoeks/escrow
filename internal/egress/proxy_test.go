package egress

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/jverhoeks/escrow/internal/config"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func startProxy(t *testing.T, cfg config.EgressProxyConfig, evlog *eventlog.Log) string {
	t.Helper()
	pol, err := NewPolicy(cfg)
	require.NoError(t, err)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	p := New(ln.Addr().String(), pol, evlog)
	go func() { _ = p.serveListener(ln) }()
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String()
}

func proxyClient(addr string) *http.Client {
	u, _ := url.Parse("http://" + addr)
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u)}, Timeout: 5 * time.Second}
}

func TestProxy_ForwardsHTTPAndRecordsEvent(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello")
	}))
	defer upstream.Close()

	evlog := eventlog.New(10)
	events, unsub := evlog.Subscribe()
	defer unsub()

	addr := startProxy(t, config.EgressProxyConfig{Policy: "forward"}, evlog)
	resp, err := proxyClient(addr).Get(upstream.URL)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, "hello", string(body))

	select {
	case e := <-events:
		assert.Equal(t, eventlog.KindEgress, e.Kind)
		assert.Equal(t, "allow", e.Action)
	case <-time.After(2 * time.Second):
		t.Fatal("no egress event recorded")
	}
}

func TestProxy_BlocksHTTP(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "should not reach")
	}))
	defer upstream.Close()
	host := mustHost(t, upstream.URL)

	addr := startProxy(t, config.EgressProxyConfig{Policy: "forward", BlockHosts: []string{host}}, eventlog.New(10))
	resp, err := proxyClient(addr).Get(upstream.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestProxy_ConnectTunnelAllowed(t *testing.T) {
	up, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer up.Close()
	go func() {
		c, err := up.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = io.Copy(c, c) // echo
	}()

	addr := startProxy(t, config.EgressProxyConfig{Policy: "forward"}, eventlog.New(10))
	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer conn.Close()
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", up.Addr(), up.Addr())

	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	require.NoError(t, err)
	assert.Contains(t, status, "200")
	for {
		line, err := br.ReadString('\n')
		require.NoError(t, err)
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	fmt.Fprint(conn, "ping")
	got := make([]byte, 4)
	_, err = io.ReadFull(br, got)
	require.NoError(t, err)
	assert.Equal(t, "ping", string(got))
}

func mustHost(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u.Hostname()
}
