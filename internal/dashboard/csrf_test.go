package dashboard_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jverhoeks/escrow/internal/dashboard"
	"github.com/stretchr/testify/require"
)

// mutatingRequestWithOrigin builds a POST to a mutating endpoint carrying a
// valid session cookie and the given Origin, but NOT the X-Escrow-Request
// custom header — so the CSRF guard must decide purely on the Origin host.
func mutatingRequestWithOrigin(t *testing.T, origin string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/dashboard/api/rescan", nil)
	auth := dashboard.NewAuth("admin", "pass", "aabbccddeeff00112233445566778899")
	rec := httptest.NewRecorder()
	auth.SetCookie(rec, httptest.NewRequest(http.MethodGet, "/", nil), "admin")
	req.AddCookie(rec.Result().Cookies()[0])
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	return req
}

// A suffix-extended host (escrow.example.com.attacker.com) must NOT satisfy the
// Origin check for r.Host == example.com. The old HasPrefix logic accepted it.
func TestOriginCheck_RejectsHostSuffixAttack(t *testing.T) {
	handler, _ := newTestDashboard(t)
	// httptest's default request host is "example.com".
	req := mutatingRequestWithOrigin(t, "http://example.com.attacker.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code, "suffix-extended origin host must be rejected")
}

// An exactly-matching Origin host passes the CSRF guard (does not 403).
func TestOriginCheck_AcceptsExactHostMatch(t *testing.T) {
	handler, _ := newTestDashboard(t)
	req := mutatingRequestWithOrigin(t, "http://example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.NotEqual(t, http.StatusForbidden, rec.Code, "exact-host origin must pass the CSRF guard")
}
