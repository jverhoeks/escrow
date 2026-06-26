package dashboard_test

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/jverhoeks/escrow/internal/dashboard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAuth_SetCredentials_PasswordRotation verifies that after SetCredentials
// the old password is rejected and the new password is accepted.
func TestAuth_SetCredentials_PasswordRotation(t *testing.T) {
	a := dashboard.NewAuth("admin", "oldpass", "secret1")
	assert.True(t, a.CheckCredentials("admin", "oldpass"), "old password should be valid before rotation")
	assert.False(t, a.CheckCredentials("admin", "newpass"), "new password should be invalid before rotation")

	a.SetCredentials("admin", "newpass", "secret1")

	assert.False(t, a.CheckCredentials("admin", "oldpass"), "old password should be invalid after rotation")
	assert.True(t, a.CheckCredentials("admin", "newpass"), "new password should be valid after rotation")
}

// TestAuth_SetCredentials_SecretInvalidatesCookies verifies that rotating the
// HMAC secret invalidates cookies signed with the old secret, and that a cookie
// signed with the new secret is valid.
func TestAuth_SetCredentials_SecretInvalidatesCookies(t *testing.T) {
	a := dashboard.NewAuth("admin", "pass", "secret-old")

	// Mint a cookie with the old secret.
	w := httptest.NewRecorder()
	a.SetCookie(w, httptest.NewRequest(http.MethodGet, "/", nil), "admin")
	cookies := w.Result().Cookies()
	require.Len(t, cookies, 1)

	// Verify the cookie is valid before rotation.
	reqOld := httptest.NewRequest(http.MethodGet, "/", nil)
	reqOld.AddCookie(cookies[0])
	assert.True(t, a.IsValid(reqOld), "cookie should be valid before secret rotation")

	// Rotate the secret.
	a.SetCredentials("admin", "pass", "secret-new")

	// The old cookie must now be rejected.
	reqAfter := httptest.NewRequest(http.MethodGet, "/", nil)
	reqAfter.AddCookie(cookies[0])
	assert.False(t, a.IsValid(reqAfter), "old cookie should be invalid after secret rotation")

	// A fresh cookie minted with the new secret must be accepted.
	w2 := httptest.NewRecorder()
	a.SetCookie(w2, httptest.NewRequest(http.MethodGet, "/", nil), "admin")
	newCookies := w2.Result().Cookies()
	require.Len(t, newCookies, 1)
	reqNew := httptest.NewRequest(http.MethodGet, "/", nil)
	reqNew.AddCookie(newCookies[0])
	assert.True(t, a.IsValid(reqNew), "new cookie should be valid after secret rotation")
}

// TestAuth_SetCredentials_UsernameRotation verifies that username rotation
// works correctly alongside password.
func TestAuth_SetCredentials_UsernameRotation(t *testing.T) {
	a := dashboard.NewAuth("alice", "pass", "mysecret")
	assert.True(t, a.CheckCredentials("alice", "pass"))
	assert.False(t, a.CheckCredentials("bob", "pass"))

	a.SetCredentials("bob", "pass", "mysecret")

	assert.False(t, a.CheckCredentials("alice", "pass"), "old username should be rejected after rotation")
	assert.True(t, a.CheckCredentials("bob", "pass"), "new username should be accepted after rotation")
}

// TestAuth_SetCredentials_Concurrent is a race-detector smoke test: N goroutines
// call IsValid and CheckCredentials while another goroutine rotates credentials
// in a loop. The test asserts no panic and no data race (run with -race).
func TestAuth_SetCredentials_Concurrent(t *testing.T) {
	a := dashboard.NewAuth("admin", "pass", "secret1")

	// Mint a valid cookie for readers to use.
	w := httptest.NewRecorder()
	a.SetCookie(w, httptest.NewRequest(http.MethodGet, "/", nil), "admin")
	cookies := w.Result().Cookies()
	require.Len(t, cookies, 1)

	const readers = 20
	const rotations = 50
	var wg sync.WaitGroup

	// Spawn readers.
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < rotations; j++ {
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				req.AddCookie(cookies[0])
				_ = a.IsValid(req)
				_ = a.CheckCredentials("admin", "pass")
			}
		}()
	}

	// Rotate credentials concurrently.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < rotations; i++ {
			if i%2 == 0 {
				a.SetCredentials("admin", "pass", "secret1")
			} else {
				a.SetCredentials("admin", "pass2", "secret2")
			}
		}
	}()

	wg.Wait()
}
