package dashboard_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jverhoeks/escrow/internal/dashboard"
	"github.com/stretchr/testify/require"
)

func TestRescanStatus_OKWithoutScanner(t *testing.T) {
	handler, _ := newTestDashboard(t) // scanner is nil
	req := authenticatedRequest(t, http.MethodGet, "/dashboard/api/rescan/status", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"enabled":false`)
}

func TestRescanTrigger_RequiresOrigin(t *testing.T) {
	handler, _ := newTestDashboard(t)
	// No X-Escrow-Request header and no Origin → CSRF guard rejects.
	req := httptest.NewRequest(http.MethodPost, "/dashboard/api/rescan", nil)
	auth := dashboard.NewAuth("admin", "pass", "aabbccddeeff00112233445566778899")
	rec0 := httptest.NewRecorder()
	auth.SetCookie(rec0, httptest.NewRequest(http.MethodGet, "/", nil), "admin")
	req.AddCookie(rec0.Result().Cookies()[0])
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
}
