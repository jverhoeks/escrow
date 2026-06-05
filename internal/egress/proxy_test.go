package egress

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/jverhoeks/escrow/internal/config"
	"github.com/jverhoeks/escrow/internal/egresslog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func startProxy(t *testing.T, cfg config.EgressProxyConfig, el *egresslog.Log) string {
	t.Helper()
	pol, err := NewPolicy(cfg)
	require.NoError(t, err)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	p := New(ln.Addr().String(), pol, el)
	go func() { _ = p.serveListener(ln) }()
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String()
}

func proxyClient(addr string) *http.Client {
	u, _ := url.Parse("http://" + addr)
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u)}, Timeout: 5 * time.Second}
}

func TestProxy_ForwardsHTTPAndRecordsEgress(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello")
	}))
	defer upstream.Close()
	el := egresslog.New(10)
	sub, unsub := el.Subscribe()
	defer unsub()
	addr := startProxy(t, config.EgressProxyConfig{Policy: "forward"}, el)
	resp, err := proxyClient(addr).Get(upstream.URL)
	require.NoError(t, err)
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	select {
	case e := <-sub:
		assert.Equal(t, "allow", e.Action)
		assert.Equal(t, mustHost(t, upstream.URL), e.Host)
		assert.Equal(t, "GET", e.Verb)
		assert.False(t, e.Timestamp.IsZero())
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

	addr := startProxy(t, config.EgressProxyConfig{Policy: "forward", BlockHosts: []string{host}}, egresslog.New(10))
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

	addr := startProxy(t, config.EgressProxyConfig{Policy: "forward"}, egresslog.New(10))
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

func mustPort(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u.Port()
}

// TestProxy_BlocksHostnameResolvingToBlockedCIDR is a regression test for the
// SSRF / egress-CIDR bypass: net.ParseIP returns nil for a hostname, so CIDR
// rules were silently skipped for hostname targets. The host "localhost" is NOT
// in block_hosts but resolves into the blocked 127.0.0.0/8 range, so the
// request must be denied (CIDR enforced on the resolved IP).
func TestProxy_BlocksHostnameResolvingToBlockedCIDR(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "should not reach")
	}))
	defer upstream.Close()
	port := mustPort(t, upstream.URL)

	addr := startProxy(t, config.EgressProxyConfig{Policy: "forward", BlockCIDRs: []string{"127.0.0.0/8"}}, egresslog.New(10))
	resp, err := proxyClient(addr).Get("http://localhost:" + port + "/")
	require.NoError(t, err) // the proxy responds (it doesn't crash)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadGateway, resp.StatusCode) // dial denied by CIDR
}

// freePort grabs a momentarily-free 127.0.0.1 port and returns its addr.
// There's an inherent TOCTOU window before Serve re-binds it, which is fine
// for a local test.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

// TestProxy_ServeReturnsNilOnCtxCancel is a regression test for Bug 1: Serve
// must return nil on a normal ctx-cancel shutdown (not a raw
// "use of closed network connection" error).
func TestProxy_ServeReturnsNilOnCtxCancel(t *testing.T) {
	pol, err := NewPolicy(config.EgressProxyConfig{Policy: "forward"})
	require.NoError(t, err)
	addr := freePort(t)
	p := New(addr, pol, egresslog.New(10))

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- p.Serve(ctx) }()

	// Wait until the proxy is actually accepting before cancelling.
	require.Eventually(t, func() bool {
		c, derr := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if derr != nil {
			return false
		}
		_ = c.Close()
		return true
	}, 2*time.Second, 20*time.Millisecond, "proxy never started listening")

	cancel()
	select {
	case err := <-errc:
		assert.NoError(t, err, "Serve must return nil on ctx cancel")
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after ctx cancel")
	}
}

// TestProxy_StripHopByHop asserts that hop-by-hop headers (e.g. Proxy-Authorization)
// are stripped before forwarding, while normal headers (X-Trace) pass through.
func TestProxy_StripHopByHop(t *testing.T) {
	var gotHeaders http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		fmt.Fprint(w, "ok")
	}))
	defer upstream.Close()

	addr := startProxy(t, config.EgressProxyConfig{Policy: "forward"}, egresslog.New(10))
	req, err := http.NewRequest(http.MethodGet, upstream.URL, nil)
	require.NoError(t, err)
	req.Header.Set("Proxy-Authorization", "secret")
	req.Header.Set("X-Trace", "trace-id-123")

	resp, err := proxyClient(addr).Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, "trace-id-123", gotHeaders.Get("X-Trace"), "normal header must pass through")
	assert.Empty(t, gotHeaders.Get("Proxy-Authorization"), "Proxy-Authorization must be stripped")
}

// TestProxy_ConnectHalfCloseDoesNotHang is a regression test for Bug 2: when
// the upstream half-closes while the client stays idle, the tunnel teardown
// must complete (the spawned copy goroutine must not wedge on an idle peer).
// We assert the client-side read returns promptly rather than blocking forever.
func TestProxy_ConnectHalfCloseDoesNotHang(t *testing.T) {
	up, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer up.Close()
	go func() {
		c, aerr := up.Accept()
		if aerr != nil {
			return
		}
		// Immediately close the upstream side: this triggers a half-close on the
		// tunnel while the proxy client stays idle.
		_ = c.Close()
	}()

	addr := startProxy(t, config.EgressProxyConfig{Policy: "forward"}, egresslog.New(10))
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

	// The upstream closed, so the proxy should close our side too. Reading must
	// return (EOF or a read error) promptly; if Bug 2 regresses it hangs here.
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, err = br.ReadByte()
	require.Error(t, err, "tunnel read should return after upstream half-close")
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("tunnel teardown hung: read timed out instead of returning EOF")
	}
}
