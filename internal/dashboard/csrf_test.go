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
// custom header — so the CSRF guard must decide purely on the Origin host
// or the X-CSRF-Token header.
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

// mutatingRequestWithCSRFToken builds a POST carrying a valid session cookie
// and the X-CSRF-Token header set to the session's embedded token, without
// the X-Escrow-Request or Origin headers — the CSRF guard must pass on the
// token alone.
func mutatingRequestWithCSRFToken(t *testing.T) *http.Request {
	t.Helper()
	auth := dashboard.NewAuth("admin", "pass", "aabbccddeeff00112233445566778899")
	rec := httptest.NewRecorder()
	auth.SetCookie(rec, httptest.NewRequest(http.MethodGet, "/", nil), "admin")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/api/rescan", nil)
	req.AddCookie(rec.Result().Cookies()[0])
	// Extract the CSRF token from the auth directly (same instance, same cookie format).
	token, ok := auth.CSRFToken(req)
	require.True(t, ok, "session cookie must contain a CSRF token")
	require.NotEmpty(t, token, "CSRF token must not be empty")
	req.Header.Set("X-CSRF-Token", token)
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

// A request with a valid X-CSRF-Token passes the CSRF guard even without
// an Origin header or X-Escrow-Request (defense-in-depth).
func TestCSRFToken_AcceptsValidToken(t *testing.T) {
	handler, _ := newTestDashboard(t)
	req := mutatingRequestWithCSRFToken(t)
	// Clear Origin and X-Escrow-Request to isolate the token check.
	req.Header.Del("Origin")
	req.Header.Del("X-Escrow-Request")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.NotEqual(t, http.StatusForbidden, rec.Code, "valid CSRF token must pass the CSRF guard")
}

// A request with an invalid X-CSRF-Token is rejected even with a valid
// session cookie (attacker cannot read the cookie to forge the token).
func TestCSRFToken_RejectsWrongToken(t *testing.T) {
	handler, _ := newTestDashboard(t)
	req := mutatingRequestWithCSRFToken(t)
	req.Header.Del("Origin")
	req.Header.Del("X-Escrow-Request")
	req.Header.Set("X-CSRF-Token", "invalid-token-value")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code, "wrong CSRF token must be rejected")
}

// ServeHTTP with nil body, no Origin, no custom header — the CSRF token is
// the only guard. Token must be set. An empty token is rejected.
func TestCSRFToken_RejectsEmptyToken(t *testing.T) {
	handler, _ := newTestDashboard(t)
	auth := dashboard.NewAuth("admin", "pass", "aabbccddeeff00112233445566778899")
	rec := httptest.NewRecorder()
	auth.SetCookie(rec, httptest.NewRequest(http.MethodGet, "/", nil), "admin")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/api/rescan", nil)
	req.AddCookie(rec.Result().Cookies()[0])
	// No Origin, no X-Escrow-Request, no X-CSRF-Token.
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req)
	require.Equal(t, http.StatusForbidden, rec2.Code, "request with no CSRF token must be rejected")
}
