package metrics

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	RequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "escrow_requests_total",
		Help: "Total requests by ecosystem and action",
	}, []string{"ecosystem", "action"})

	BlocksTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "escrow_blocks_total",
		Help: "Blocked packages by ecosystem and signal",
	}, []string{"ecosystem", "signal"})

	CacheHitsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "escrow_cache_hits_total",
		Help: "Cache hits by ecosystem and type",
	}, []string{"ecosystem", "cache_type"})

	OSVQueryDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "escrow_osv_query_duration_seconds",
		Help:    "OSV API query latency",
		Buckets: prometheus.DefBuckets,
	})

	ProxyRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "escrow_proxy_request_duration_seconds",
		Help:    "End-to-end proxy request latency",
		Buckets: prometheus.DefBuckets,
	}, []string{"ecosystem"})
)

var startTime = time.Now()

type HealthResponse struct {
	Version        string          `json:"version"`
	Status         string          `json:"status"`
	Uptime         string          `json:"uptime"`
	Backend        string          `json:"storage_backend"`
	CacheWritable  bool            `json:"cache_writable"`
	UpstreamStatus map[string]bool `json:"upstream_status,omitempty"`
}

// HealthHandler returns a health check handler that probes each upstream and the cache.
// upstreams maps ecosystem name → base URL (e.g. "npm" → "https://registry.npmjs.org").
// cacheDir is the disk cache root directory; empty means cache is non-disk (memory/S3).
func HealthHandler(version, backend string, upstreams map[string]string, cacheDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		upstreamStatus := make(map[string]bool, len(upstreams))
		for eco, url := range upstreams {
			upstreamStatus[eco] = probeUpstream(r.Context(), url)
		}

		cacheWritable := probeCacheWritable(cacheDir)

		status := "ok"
		if !cacheWritable && cacheDir != "" {
			status = "degraded"
		}
		for _, ok := range upstreamStatus {
			if !ok {
				status = "degraded"
				break
			}
		}

		w.Header().Set("Content-Type", "application/json")
		if status == "degraded" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		json.NewEncoder(w).Encode(HealthResponse{
			Version:        version,
			Status:         status,
			Uptime:         time.Since(startTime).Round(time.Second).String(),
			Backend:        backend,
			CacheWritable:  cacheWritable,
			UpstreamStatus: upstreamStatus,
		})
	}
}

// probeCacheWritable verifies the disk cache directory is writable by creating and removing a probe file.
func probeCacheWritable(dir string) bool {
	if dir == "" {
		return true // non-disk backends always report writable
	}
	f, err := os.CreateTemp(dir, ".health-probe-*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

// probeUpstream does a HEAD request with a 3-second timeout.
func probeUpstream(ctx context.Context, baseURL string) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, baseURL, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}

func MetricsHandler() http.Handler {
	return promhttp.Handler()
}
