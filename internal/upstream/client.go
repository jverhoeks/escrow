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

// MetadataTimeout bounds a single metadata (manifest/index/json) fetch so a slow
// upstream that sends headers then trickles the body can't hold the handler —
// and its client + upstream connections — indefinitely (previously until the
// server WriteTimeout). The point is to bound an *indefinite* hang, not to be
// snappy: it's generous (120s) so a legitimately large manifest (npm aws-sdk /
// @types/node full metadata are tens of MB) still completes on a slow CI link,
// while far below the old unbounded behavior. Blob fetches keep NO total timeout
// (large artifacts exceed any fixed ceiling). Overridable in tests. See #73.
var MetadataTimeout = 120 * time.Second

// MetadataClient derives a metadata-fetch client from base: it shares base's
// Transport (and thus the connection pool + error metering) but adds a total
// request Timeout of MetadataTimeout. Use it for manifest/index/json fetches;
// keep the base client for blob downloads.
func MetadataClient(base *http.Client) *http.Client {
	return &http.Client{Transport: base.Transport, Timeout: MetadataTimeout}
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
