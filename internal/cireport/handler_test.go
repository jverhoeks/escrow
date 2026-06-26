package cireport_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jverhoeks/escrow/internal/cireport"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func serve(t *testing.T, token, reqToken, authHeader string) int {
	t.Helper()
	r := chi.NewRouter()
	cireport.New(eventlog.New(10), token).Mount(r)
	req := httptest.NewRequest(http.MethodGet, "/ci-report", nil)
	if reqToken != "" {
		q := req.URL.Query()
		q.Set("token", reqToken)
		req.URL.RawQuery = q.Encode()
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr.Code
}

// #75: with a token configured, /ci-report requires it; without one, it's open.
func TestCIReport_TokenAuth(t *testing.T) {
	require.Equal(t, http.StatusOK, serve(t, "", "", ""), "no token configured → open")
	assert.Equal(t, http.StatusUnauthorized, serve(t, "secret", "", ""), "missing token → 401")
	assert.Equal(t, http.StatusUnauthorized, serve(t, "secret", "wrong", ""), "wrong token → 401")
	assert.Equal(t, http.StatusOK, serve(t, "secret", "secret", ""), "correct ?token → 200")
	assert.Equal(t, http.StatusOK, serve(t, "secret", "", "Bearer secret"), "correct Bearer → 200")
}
