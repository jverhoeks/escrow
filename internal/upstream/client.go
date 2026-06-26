package upstream

import (
	"net/http"
	"time"

	"github.com/jverhoeks/escrow/internal/metrics"
)

// New returns an http.Client tuned for upstream registry requests.
// No total request Timeout is set: large artifacts (Maven JARs, Python wheels,
// Cargo crates) can exceed any reasonable fixed ceiling. Individual phase
// timeouts (TLS handshake, response headers) are set separately.
// The server's WriteTimeout (default 120s) acts as the wall-clock ceiling
// for the full handler — if an upstream stalls, the server closes the connection.
func New() *http.Client {
	return &http.Client{
		Transport: &errorCountingTransport{base: &http.Transport{
			// Global idle-connection cap. Sized above the worst case (7 ecosystems ×
			// MaxIdleConnsPerHost 20 = 140 potential) with headroom so a single busy
			// upstream can't fill the shared idle pool and starve the other ecosystems'
			// idle connections (which the previous cap of 100 allowed).
			MaxIdleConns:          256,
			MaxIdleConnsPerHost:   20,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		}},
	}
}

// errorCountingTransport wraps a RoundTripper to centrally meter failed upstream
// fetches (transport error or 5xx) as escrow_upstream_errors_total, so the RED
// "are upstream fetches failing?" signal needs no per-handler wiring. See #41.
type errorCountingTransport struct{ base http.RoundTripper }

func (t *errorCountingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		metrics.UpstreamErrorsTotal.WithLabelValues(req.URL.Host, "transport").Inc()
		return resp, err
	}
	if resp.StatusCode >= 500 {
		metrics.UpstreamErrorsTotal.WithLabelValues(req.URL.Host, "5xx").Inc()
	}
	return resp, err
}
