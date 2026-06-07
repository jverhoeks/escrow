// Package staleserve implements escrow's opt-in stale-on-error metadata
// fallback. When an upstream metadata fetch fails and a recently-expired entry
// is still cached within the configured grace window, escrow serves that stale
// copy instead of returning 502. The grace window is enforced inside the cache
// (Cache.GetMetaStale); this package is the thin handler-side glue.
//
// The fallback is default-OFF: with storage.stale_on_error_max_age_m == 0,
// GetMetaStale returns no data and Serve always reports false, so callers 502
// exactly as before.
package staleserve

import (
	"net/http"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/jverhoeks/escrow/internal/cache"
	"github.com/jverhoeks/escrow/internal/metrics"
)

// Serve attempts to serve recently-expired cached metadata when upstream is down.
// Returns true if it wrote a stale response; false means the caller should 502.
//
// Metadata only — callers must never invoke this for blobs/tarballs, which are
// immutable and whose staleness is meaningless and resurrection-dangerous.
func Serve(w http.ResponseWriter, r *http.Request, c cache.Cache, key, contentType, ecosystem, kind string) bool {
	data, expiresAt, err := c.GetMetaStale(r.Context(), key)
	if err != nil || data == nil {
		return false
	}

	metrics.CacheStaleServedTotal.WithLabelValues(ecosystem, kind).Inc()
	log.Warn().
		Str("ecosystem", ecosystem).
		Str("kind", kind).
		Str("host", r.Host).
		Str("key", key).
		Dur("age", time.Since(expiresAt)).
		Msg("serving stale metadata; upstream unreachable")

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Escrow-Stale", "true")
	w.Header().Set("Warning", `110 - "Response is Stale"`)
	w.Write(data) //nolint:errcheck
	return true
}
