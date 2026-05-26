package server

import (
	"net/http"
	"time"

	"github.com/rs/zerolog"
)

// LoggingTransport wraps an http.RoundTripper and logs every upstream request
// at DEBUG level. This makes cache misses (real upstream calls) visible without
// requiring a separate log file — just set log_level = "debug" in escrow.toml.
//
// Log line fields:
//
//	upstream=true  method=GET  url=https://registry.npmjs.org/lodash
//	status=200  bytes=2048  ms=45.3
type LoggingTransport struct {
	Base zerolog.Logger
	Next http.RoundTripper
}

func (t *LoggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := t.Next.RoundTrip(req)
	ms := float64(time.Since(start).Microseconds()) / 1000

	ev := t.Base.Debug().
		Bool("upstream", true).
		Str("method", req.Method).
		Str("url", req.URL.String()).
		Float64("ms", ms)

	if err != nil {
		ev.Err(err).Msg("upstream")
		return resp, err
	}

	bytes := resp.ContentLength
	if bytes < 0 {
		bytes = 0
	}
	ev.Int("status", resp.StatusCode).
		Int64("bytes", bytes).
		Msg("upstream")

	return resp, nil
}

// NewLoggingClient wraps the provided http.Client so all its requests are
// logged via the given zerolog.Logger. The original transport is preserved;
// if it is nil, http.DefaultTransport is used.
func NewLoggingClient(c *http.Client, log zerolog.Logger) *http.Client {
	base := c.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	cp := *c
	cp.Transport = &LoggingTransport{Base: log, Next: base}
	return &cp
}
