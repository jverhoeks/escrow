package egress

import (
	"context"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/jverhoeks/escrow/internal/eventlog"
)

// Proxy is a forward proxy gated by a host/IP Policy. It tunnels CONNECT
// (HTTPS) opaquely and forwards absolute-URI HTTP. No TLS interception.
type Proxy struct {
	addr      string
	policy    *Policy
	evlog     *eventlog.Log // may be nil
	transport *http.Transport
	srv       *http.Server
}

// New builds a Proxy bound to addr (host:port).
func New(addr string, policy *Policy, evlog *eventlog.Log) *Proxy {
	return &Proxy{
		addr:      addr,
		policy:    policy,
		evlog:     evlog,
		transport: &http.Transport{Proxy: nil},
	}
}

// Serve listens on the configured address and serves until ctx is cancelled.
func (p *Proxy) Serve(ctx context.Context) error {
	ln, err := net.Listen("tcp", p.addr)
	if err != nil {
		return err
	}
	// Assign p.srv before starting the shutdown goroutine so the goroutine never
	// races a nil read, and so it can call srv.Close() (which makes Serve return
	// http.ErrServerClosed). Closing only the listener would surface a raw
	// "use of closed network connection" error instead.
	p.srv = &http.Server{Handler: http.HandlerFunc(p.handle)}
	go func() {
		<-ctx.Done()
		_ = p.srv.Close()
	}()
	if err := p.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (p *Proxy) serveListener(ln net.Listener) error {
	if p.srv == nil {
		p.srv = &http.Server{Handler: http.HandlerFunc(p.handle)}
	}
	if err := p.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (p *Proxy) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
		return
	}
	p.handleHTTP(w, r)
}

func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	host := hostOnly(r.Host)
	if d := p.policy.Check(host, net.ParseIP(host)); !d.Allow {
		p.record(host, "block", d.Reason)
		http.Error(w, "blocked by escrow egress policy", http.StatusForbidden)
		return
	}
	upstream, err := net.DialTimeout("tcp", r.Host, 10*time.Second)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	p.record(host, "allow", "tunnel")
	_, _ = io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n")
	// Each direction closes the *other* conn when it finishes so both io.Copy
	// calls unblock on a half-close (otherwise an idle peer would wedge the
	// teardown and leak the goroutine + both connections).
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(upstream, client)
		_ = upstream.Close() // unblocks the io.Copy(client, upstream) below
		close(done)
	}()
	_, _ = io.Copy(client, upstream)
	_ = client.Close() // unblocks the goroutine's io.Copy(upstream, client)
	<-done
}

func (p *Proxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	host := r.URL.Hostname()
	if d := p.policy.Check(host, net.ParseIP(host)); !d.Allow {
		p.record(host, "block", d.Reason)
		http.Error(w, "blocked by escrow egress policy", http.StatusForbidden)
		return
	}
	r.RequestURI = ""
	r.Header.Del("Proxy-Connection")
	resp, err := p.transport.RoundTrip(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	p.record(host, "allow", "forward")
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (p *Proxy) record(host, action, reason string) {
	if p.evlog == nil {
		return
	}
	p.evlog.Record(eventlog.PackageEvent{
		Ecosystem: "egress",
		Package:   host,
		Action:    action,
		Signal:    "egress",
		Reason:    reason,
		Kind:      eventlog.KindEgress,
	})
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}
