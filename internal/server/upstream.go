package server

import (
	"net/http"
	"time"

	"github.com/jverhoeks/escrow/internal/upstreamlog"
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

	// Optional upstream-fetch recorder. When rec is non-nil and the request
	// host is present in hostEco, the fetch is recorded with that ecosystem.
	rec     *upstreamlog.Log
	hostEco map[string]string
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

	if t.rec != nil {
		if eco, ok := t.hostEco[req.URL.Hostname()]; ok {
			t.rec.Record(upstreamlog.Event{
				Ecosystem: eco,
				Method:    req.Method,
				URL:       req.URL.String(),
				Status:    resp.StatusCode,
				Bytes:     bytes,
				MS:        ms,
			})
		}
	}

	return resp, nil
}

// NewLoggingClient wraps the provided http.Client so all its requests are
// logged via the given zerolog.Logger. The original transport is preserved;
// if it is nil, http.DefaultTransport is used.
func NewLoggingClient(c *http.Client, log zerolog.Logger) *http.Client {
	return NewLoggingClientWithRecorder(c, log, nil, nil)
}

// NewLoggingClientWithRecorder wraps c so requests are logged, and fetches to
// hosts present in hostEco are recorded into rec with the mapped ecosystem.
func NewLoggingClientWithRecorder(c *http.Client, log zerolog.Logger, rec *upstreamlog.Log, hostEco map[string]string) *http.Client {
	base := c.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	cp := *c
	cp.Transport = &LoggingTransport{Base: log, Next: base, rec: rec, hostEco: hostEco}
	return &cp
}
