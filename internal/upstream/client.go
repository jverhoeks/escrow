package upstream

import (
	"net/http"
	"time"
)

// New returns an http.Client tuned for upstream registry requests.
// No total request Timeout is set: large artifacts (Maven JARs, Python wheels,
// Cargo crates) can exceed any reasonable fixed ceiling. Individual phase
// timeouts (TLS handshake, response headers) are set separately.
// The server's WriteTimeout (default 120s) acts as the wall-clock ceiling
// for the full handler — if an upstream stalls, the server closes the connection.
func New() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			// Global idle-connection cap. Sized above the worst case (7 ecosystems ×
			// MaxIdleConnsPerHost 20 = 140 potential) with headroom so a single busy
			// upstream can't fill the shared idle pool and starve the other ecosystems'
			// idle connections (which the previous cap of 100 allowed).
			MaxIdleConns:          256,
			MaxIdleConnsPerHost:   20,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		},
	}
}
