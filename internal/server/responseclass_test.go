package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"

	"github.com/jverhoeks/escrow/internal/metrics"
)

func TestResponseClassMiddleware_Counts5xx(t *testing.T) {
	r := chi.NewRouter()
	r.Use(responseClassMiddleware)
	r.Get("/boom", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	before := testutil.ToFloat64(metrics.ResponsesTotal.WithLabelValues("5xx"))
	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)

	after := testutil.ToFloat64(metrics.ResponsesTotal.WithLabelValues("5xx"))
	assert.Equal(t, before+1, after, "a 500 response should increment escrow_responses_total{class=5xx}")
}

func TestResponseClassMiddleware_DefaultsTo2xx(t *testing.T) {
	r := chi.NewRouter()
	r.Use(responseClassMiddleware)
	// Handler writes a body without an explicit WriteHeader → implicit 200.
	r.Get("/ok", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	before := testutil.ToFloat64(metrics.ResponsesTotal.WithLabelValues("2xx"))
	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)

	after := testutil.ToFloat64(metrics.ResponsesTotal.WithLabelValues("2xx"))
	assert.Equal(t, before+1, after, "an implicit-200 response should increment class=2xx")
}
