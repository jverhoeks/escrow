package egress

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jverhoeks/escrow/internal/egresslog"
	"github.com/jverhoeks/escrow/internal/metrics"
)

// Proxy is a forward proxy gated by a host/IP Policy. It tunnels CONNECT
// (HTTPS) opaquely and forwards absolute-URI HTTP. No TLS interception.
type Proxy struct {
	addr      string
	policy    *Policy
	egress    *egresslog.Log // may be nil
	transport *http.Transport
	srv       *http.Server
}

// New builds a Proxy bound to addr (host:port).
func New(addr string, policy *Policy, egress *egresslog.Log) *Proxy {
	p := &Proxy{addr: addr, policy: policy, egress: egress}
	p.transport = &http.Transport{Proxy: nil, DialContext: p.dialChecked}
	return p
}

// dialChecked resolves addr's host, enforces the egress policy against EVERY
// resolved IP (deny if any is blocked), then dials one of the vetted IPs
// directly — never the hostname — so the connection target is exactly what was
// policy-checked (defeats DNS rebinding). Used for CONNECT and as the HTTP
// transport's DialContext.
func (p *Proxy) dialChecked(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var ips []net.IP
	if lit := net.ParseIP(host); lit != nil {
		ips = []net.IP{lit}
	} else {
		resolved, rerr := net.DefaultResolver.LookupIP(ctx, "ip", host)
		if rerr != nil || len(resolved) == 0 {
			// Ambiguous: cannot vet against CIDR rules. Deny rather than dial blind.
			p.recordEgress(egresslog.Event{Host: host, Verb: "DIAL", Action: "block", Reason: "unresolvable"})
			return nil, fmt.Errorf("egress: cannot resolve %q: %w", host, rerr)
		}
		ips = resolved
	}
	// Deny if ANY resolved IP is blocked by policy.
	for _, ip := range ips {
		if d := p.policy.Check(host, ip); !d.Allow {
			p.recordEgress(egresslog.Event{Host: host, IP: ip.String(), Verb: "DIAL", Action: "block", Reason: d.Reason + " (resolved " + ip.String() + ")"})
			return nil, fmt.Errorf("egress: blocked %s -> %s: %s", host, ip, d.Reason)
		}
	}
	// Dial a vetted IP directly (anti-rebinding). Try each until one connects.
	dialer := net.Dialer{Timeout: 10 * time.Second}
	var lastErr error
	for _, ip := range ips {
		conn, derr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if derr == nil {
			return conn, nil
		}
		lastErr = derr
	}
	return nil, lastErr
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
	p.srv = &http.Server{
		Handler:           http.HandlerFunc(p.handle),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
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
		p.srv = &http.Server{
			Handler:           http.HandlerFunc(p.handle),
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       60 * time.Second,
		}
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
		p.recordEgress(egresslog.Event{Host: host, Verb: "CONNECT", Action: "block", Reason: d.Reason})
		http.Error(w, "blocked by escrow egress policy", http.StatusForbidden)
		return
	}
	upstream, err := p.dialChecked(r.Context(), "tcp", r.Host)
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
	_, _ = io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n")
	ip := ""
	if ra, ok := upstream.RemoteAddr().(*net.TCPAddr); ok {
		ip = ra.IP.String()
	}
	p.recordEgress(egresslog.Event{Host: host, IP: ip, Verb: "CONNECT", Action: "allow", Reason: "tunnel"})
	// Each direction closes the *other* conn when it finishes so both io.Copy
	// calls unblock on a half-close (otherwise an idle peer would wedge the
	// teardown and leak the goroutine + both connections).
	var up int64
	done := make(chan struct{})
	go func() {
		up, _ = io.Copy(upstream, client)
		_ = upstream.Close() // unblocks the io.Copy(client, upstream) below
		close(done)
	}()
	dn, _ := io.Copy(client, upstream)
	_ = client.Close() // unblocks the goroutine's io.Copy(upstream, client)
	<-done
	n := up + dn
	if p.egress != nil {
		p.egress.AddBytes(n)
	}
	metrics.EgressBytesTotal.Add(float64(n))
}

// hopByHop are headers that must not be forwarded by a proxy (RFC 7230 §6.1).
var hopByHop = []string{"Connection", "Proxy-Connection", "Keep-Alive",
	"Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

func stripHopByHop(h http.Header) {
	// Remove headers named in Connection, then the standard hop-by-hop set.
	for _, v := range h["Connection"] {
		for _, name := range strings.Split(v, ",") {
			if n := strings.TrimSpace(name); n != "" {
				h.Del(n)
			}
		}
	}
	for _, k := range hopByHop {
		h.Del(k)
	}
}

func (p *Proxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	host := r.URL.Hostname()
	if d := p.policy.Check(host, net.ParseIP(host)); !d.Allow {
		p.recordEgress(egresslog.Event{Host: host, Verb: r.Method, Action: "block", Reason: d.Reason})
		http.Error(w, "blocked by escrow egress policy", http.StatusForbidden)
		return
	}
	r.RequestURI = ""
	stripHopByHop(r.Header)
	resp, err := p.transport.RoundTrip(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	p.recordEgress(egresslog.Event{Host: host, Verb: r.Method, Action: "allow", Reason: "forward"})
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	n, _ := io.Copy(w, resp.Body)
	if p.egress != nil {
		p.egress.AddBytes(n)
	}
	metrics.EgressBytesTotal.Add(float64(n))
}

func (p *Proxy) recordEgress(e egresslog.Event) {
	metrics.EgressRequestsTotal.WithLabelValues(e.Action).Inc()
	if p.egress != nil {
		p.egress.Record(e)
	}
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}
