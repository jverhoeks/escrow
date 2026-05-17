package dashboard_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/config"
	"github.com/jverhoeks/escrow/internal/dashboard"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/rs/zerolog"
)

// TestHandleStream_ReturnsStatusBeforeSSEHeaders verifies that when the subscriber
// cap is reached, the endpoint returns HTTP 503 with no SSE headers — not a
// garbled SSE stream with a buried error message.
func TestHandleStream_ReturnsStatusBeforeSSEHeaders(t *testing.T) {
	cfg := config.DashboardConfig{
		Enabled: true, Path: "/dashboard",
		Username: "admin", Password: "pass",
		Secret: "aabbccddeeff00112233445566778899",
	}
	evLog := eventlog.New(10)
	dash := dashboard.New(cfg, evLog, zerolog.Nop(), nil, nil, nil)
	r := chi.NewRouter()
	dash.Mount(r)

	auth := dashboard.NewAuth("admin", "pass", "aabbccddeeff00112233445566778899")

	// Fill subscriber cap with dummy connections (cap = 100, but we expose a test hook)
	// Instead, directly call handleStream via HTTP and rely on the cap test infrastructure.
	// We use the exported maxSubscribers value indirectly by filling to the limit via Subscribe().
	var unsubs []func()
	for i := 0; i < 100; i++ {
		ch, unsub := evLog.Subscribe()
		if ch == nil {
			// Already at cap — good
			break
		}
		unsubs = append(unsubs, unsub)
	}
	defer func() {
		for _, u := range unsubs {
			if u != nil {
				u()
			}
		}
	}()

	// Now an attempt to stream should get 503, not start an SSE stream
	rec := httptest.NewRecorder()
	auth.SetCookie(rec, httptest.NewRequest(http.MethodGet, "/", nil), "admin")
	cookie := rec.Result().Cookies()[0]

	req := httptest.NewRequest(http.MethodGet, "/dashboard/api/stream", nil)
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusServiceUnavailable, rr.Code,
		"should return 503 before writing any SSE headers")
	assert.NotEqual(t, "text/event-stream", rr.Header().Get("Content-Type"),
		"should NOT have set SSE Content-Type before checking subscriber cap")
}
