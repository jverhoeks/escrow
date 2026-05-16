package metrics

import (
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

	OSVQueryDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "escrow_osv_query_duration_seconds",
		Help:    "OSV API query latency",
		Buckets: prometheus.DefBuckets,
	})
)

var startTime = time.Now()

type HealthResponse struct {
	Status  string `json:"status"`
	Uptime  string `json:"uptime"`
	Backend string `json:"storage_backend"`
}

func HealthHandler(backend string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(HealthResponse{
			Status:  "ok",
			Uptime:  time.Since(startTime).Round(time.Second).String(),
			Backend: backend,
		})
	}
}

func MetricsHandler() http.Handler {
	return promhttp.Handler()
}
