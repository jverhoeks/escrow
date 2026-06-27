package dashboard

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const csrfTokenLen = 32 // bytes of random data per CSRF token, base64-encoded = 44 chars

const cookieName = "escrow_session"
const cookieTTL = 24 * time.Hour

// authCreds is an immutable snapshot of auth credentials. It is stored behind
// an atomic.Pointer so reads are lock-free and rotations are race-free.
type authCreds struct {
	username string
	password string
	secret   []byte
}

// sign computes the HMAC-SHA256 signature of payload using the creds' secret.
func (c *authCreds) sign(payload string) string {
	mac := hmac.New(sha256.New, c.secret)
	mac.Write([]byte(payload))
	return base64.URLEncoding.EncodeToString(mac.Sum(nil))
}

// Auth manages dashboard session authentication. All credential state lives
// inside an atomically-swapped *authCreds so that SetCredentials and concurrent
// request handlers never race.
type Auth struct {
	creds atomic.Pointer[authCreds]
}

// NewAuth constructs an Auth with the given credentials.
func NewAuth(username, password, secret string) *Auth {
	a := &Auth{}
	a.creds.Store(&authCreds{username: username, password: password, secret: []byte(secret)})
	return a
}

// SetCredentials atomically swaps the active credentials (live rotation).
// Rotating the secret invalidates all existing session cookies (they were
// signed with the old secret) — callers re-authenticate. This is intended.
func (a *Auth) SetCredentials(username, password, secret string) {
	a.creds.Store(&authCreds{username: username, password: password, secret: []byte(secret)})
}

func (a *Auth) CheckCredentials(username, password string) bool {
	c := a.creds.Load()
	// Hash both the stored and provided values before comparing so that
	// ConstantTimeCompare always receives equal-length slices. Without this,
	// different-length inputs short-circuit and leak the stored value's length.
	hStored := func(prefix, s string) []byte {
		m := hmac.New(sha256.New, c.secret)
		m.Write([]byte(prefix + s))
		return m.Sum(nil)
	}
	uOK := subtle.ConstantTimeCompare(hStored("u:", c.username), hStored("u:", username)) == 1
	pOK := subtle.ConstantTimeCompare(hStored("p:", c.password), hStored("p:", password)) == 1
	return uOK && pOK
}

// generateCSRFToken returns a random base64-encoded token for CSRF protection.
func generateCSRFToken() string {
	b := make([]byte, csrfTokenLen)
	rand.Read(b)
	return base64.URLEncoding.EncodeToString(b)
}

func (a *Auth) SetCookie(w http.ResponseWriter, r *http.Request, username string) {
	c := a.creds.Load()
	expiry := time.Now().Add(cookieTTL).Unix()
	csrfToken := generateCSRFToken()
	payload := fmt.Sprintf("%s|%d|%s", username, expiry, csrfToken)
	value := base64.URLEncoding.EncodeToString([]byte(payload)) + "." + c.sign(payload)
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(cookieTTL.Seconds()),
	})
}

func (a *Auth) IsValid(r *http.Request) bool {
	u, _, _ := a.parseCookie(r)
	return u != ""
}

// CSRFToken returns the CSRF token from the session cookie, if the session is valid.
func (a *Auth) CSRFToken(r *http.Request) (string, bool) {
	_, _, token := a.parseCookie(r)
	return token, token != ""
}

// parseCookie extracts (username, expiry, csrfToken, ok) from a valid session cookie.
func (a *Auth) parseCookie(r *http.Request) (username string, expiry int64, csrfToken string) {
	c := a.creds.Load()
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return "", 0, ""
	}
	parts := strings.SplitN(cookie.Value, ".", 2)
	if len(parts) != 2 {
		return "", 0, ""
	}
	payloadBytes, err := base64.URLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", 0, ""
	}
	payload := string(payloadBytes)
	if !hmac.Equal([]byte(c.sign(payload)), []byte(parts[1])) {
		return "", 0, ""
	}
	fields := strings.SplitN(payload, "|", 3)
	if len(fields) < 2 {
		return "", 0, ""
	}
	exp, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return "", 0, ""
	}
	if time.Now().Unix() >= exp {
		return "", 0, ""
	}
	username = fields[0]
	expiry = exp
	if len(fields) >= 3 {
		csrfToken = fields[2]
	}
	return username, expiry, csrfToken
}

func (a *Auth) ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1})
}

func (a *Auth) Middleware(loginPath string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !a.IsValid(r) {
				http.Redirect(w, r, loginPath, http.StatusFound)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (a *Auth) Username(r *http.Request) (string, bool) {
	u, _, _ := a.parseCookie(r)
	return u, u != ""
}
