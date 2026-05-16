package dashboard_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/dashboard"
)

func TestAuth_SetAndVerify(t *testing.T) {
	a := dashboard.NewAuth("admin", "secret", "aabbccddeeff00112233445566778899")
	w := httptest.NewRecorder()
	a.SetCookie(w, "admin")
	cookies := w.Result().Cookies()
	require.Len(t, cookies, 1)
	assert.Equal(t, "escrow_session", cookies[0].Name)
	assert.True(t, cookies[0].HttpOnly)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookies[0])
	assert.True(t, a.IsValid(req))
}

func TestAuth_TamperedCookie(t *testing.T) {
	a := dashboard.NewAuth("admin", "secret", "aabbccddeeff00112233445566778899")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "escrow_session", Value: "tampered.value"})
	assert.False(t, a.IsValid(req))
}

func TestAuth_Credentials(t *testing.T) {
	a := dashboard.NewAuth("admin", "pass123", "aabbccddeeff00112233445566778899")
	assert.True(t, a.CheckCredentials("admin", "pass123"))
	assert.False(t, a.CheckCredentials("admin", "wrong"))
	assert.False(t, a.CheckCredentials("root", "pass123"))
}
