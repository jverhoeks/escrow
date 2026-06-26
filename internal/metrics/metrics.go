package metrics

import (
	"context"
	"encoding/json"
	"net/http"
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

	CacheStaleServedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "escrow_cache_stale_served_total",
		Help: "Expired metadata served stale on upstream error, by ecosystem and kind",
	}, []string{"ecosystem", "kind"})

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

	EgressRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "escrow_egress_requests_total",
		Help: "Egress proxy decisions by action",
	}, []string{"action"})

	EgressBytesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "escrow_egress_bytes_total",
		Help: "Total bytes proxied through the egress proxy (allow path)",
	})

	// CacheWriteFailuresTotal counts cache write failures, labelled by backend
	// (disk/s3) and op (meta/blob). Incremented centrally in the cache backends
	// so failures are metered even though the proxy handlers ignore the returned
	// error (the cache is a best-effort optimisation, not on the critical path).
	CacheWriteFailuresTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "escrow_cache_write_failures_total",
		Help: "Cache write failures by backend and op",
	}, []string{"backend", "op"})

	// ResponsesTotal counts HTTP responses by status class (2xx/3xx/4xx/5xx),
	// fed by a server middleware on every route. The 5xx/total ratio is the
	// error-rate signal (#19). Saturation is covered by the default Go/process
	// collectors exposed at /metrics (go_goroutines,
	// process_resident_memory_bytes, etc.) — no extra code needed for those.
	ResponsesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "escrow_responses_total",
		Help: "HTTP responses by status class",
	}, []string{"class"})

	// UpstreamConnReused counts upstream connections by ecosystem and whether an
	// idle keep-alive connection was reused (httptrace GotConn.Reused). The
	// reuse ratio per ecosystem is the connection-pool health signal (#17): a
	// falling ratio under load means that ecosystem's idle connections are being
	// evicted from the shared transport pool — the cross-ecosystem starvation the
	// audit warned about. Reuse ≈ 100% under normal load is healthy.
	UpstreamConnReused = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "escrow_upstream_conn_reused_total",
		Help: "Upstream connections by ecosystem and idle-connection reuse (reused=true|false)",
	}, []string{"ecosystem", "reused"})

	// UpstreamConnAcquireSeconds is the httptrace GetConn→GotConn latency by
	// ecosystem: ~0 on idle reuse, dial+TLS time on a new connection. NOTE: with
	// the transport's per-host connection count unbounded (MaxConnsPerHost=0) a
	// request never queues for a slot, so this reflects DIAL latency, not pool
	// saturation. It only becomes a saturation signal if MaxConnsPerHost is set.
	UpstreamConnAcquireSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "escrow_upstream_conn_acquire_seconds",
		Help:    "Upstream connection acquisition latency (GetConn→GotConn) by ecosystem",
		Buckets: []float64{.0001, .0005, .001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5},
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
// cacheHealth probes the configured cache backend (nil error = healthy); disk
// does a probe-write, S3 a HeadBucket, memory always nil. A nil cacheHealth is
// treated as always-healthy so callers that don't wire it can't panic.
func HealthHandler(version, backend string, upstreams map[string]string, cacheHealth func(context.Context) error) http.HandlerFunc {
	if cacheHealth == nil {
		cacheHealth = func(context.Context) error { return nil }
	}
	return func(w http.ResponseWriter, r *http.Request) {
		upstreamStatus := make(map[string]bool, len(upstreams))
		for eco, url := range upstreams {
			upstreamStatus[eco] = probeUpstream(r.Context(), url)
		}

		cacheWritable := cacheHealth(r.Context()) == nil

		status := "ok"
		if !cacheWritable {
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
