package npm_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/cache"
	"github.com/jverhoeks/escrow/internal/config"
	"github.com/jverhoeks/escrow/internal/handler/npm"
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/trust"
)

// TestNPMHandler_SingleflightDeduplicatesConcurrentManifestRequests verifies that N
// concurrent cold-cache requests for the same package result in exactly ONE upstream
// fetch. This is the singleflight guarantee for manifest requests.
func TestNPMHandler_SingleflightDeduplicatesConcurrentManifestRequests(t *testing.T) {
	var upstreamHits int64

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&upstreamHits, 1)
		// Simulate a slow upstream to ensure multiple goroutines are in-flight simultaneously
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"name": "testpkg",
			"versions": map[string]any{
				"1.0.0": map[string]any{"name": "testpkg", "version": "1.0.0"},
			},
			"time": map[string]string{
				"1.0.0": time.Now().Add(-90 * 24 * time.Hour).UTC().Format(time.RFC3339),
			},
			"dist-tags": map[string]string{"latest": "1.0.0"},
		})
	}))
	defer upstream.Close()

	c := cache.NewMemory()
	defer c.Close()
	engine := trust.NewEngine(trust.NewAgeSignal(7, nil))
	pol := policy.New(&config.PolicyConfig{
		Age: &config.AgePolicyConfig{MinDays: 7, Action: "block"},
	})
	h := npm.New(upstream.Client(), upstream.URL, engine, pol, c, nil)

	r := chi.NewRouter()
	h.Mount(r)

	const concurrency = 20
	var wg sync.WaitGroup
	results := make([]int, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/testpkg", nil)
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)
			results[idx] = rr.Code
		}(i)
	}
	wg.Wait()

	for i, code := range results {
		require.Equal(t, http.StatusOK, code, "request %d should succeed", i)
	}

	hits := atomic.LoadInt64(&upstreamHits)
	// Singleflight should collapse N concurrent requests to 1 upstream hit.
	// We allow 2 in case the first request completes before all goroutines start.
	assert.LessOrEqual(t, hits, int64(2),
		fmt.Sprintf("singleflight should deduplicate: %d concurrent requests should produce ≤2 upstream hits, got %d", concurrency, hits))
}
