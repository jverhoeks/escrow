package dashboard

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const cookieName = "escrow_session"
const cookieTTL = 24 * time.Hour

type Auth struct {
	username string
	password string
	secret   []byte
}

func NewAuth(username, password, secret string) *Auth {
	return &Auth{username: username, password: password, secret: []byte(secret)}
}

func (a *Auth) CheckCredentials(username, password string) bool {
	// Hash both the stored and provided values before comparing so that
	// ConstantTimeCompare always receives equal-length slices. Without this,
	// different-length inputs short-circuit and leak the stored value's length.
	hStored := func(prefix, s string) []byte {
		m := hmac.New(sha256.New, a.secret)
		m.Write([]byte(prefix + s))
		return m.Sum(nil)
	}
	uOK := subtle.ConstantTimeCompare(hStored("u:", a.username), hStored("u:", username)) == 1
	pOK := subtle.ConstantTimeCompare(hStored("p:", a.password), hStored("p:", password)) == 1
	return uOK && pOK
}

func (a *Auth) SetCookie(w http.ResponseWriter, r *http.Request, username string) {
	expiry := time.Now().Add(cookieTTL).Unix()
	payload := fmt.Sprintf("%s|%d", username, expiry)
	value := base64.URLEncoding.EncodeToString([]byte(payload)) + "." + a.sign(payload)
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
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	parts := strings.SplitN(cookie.Value, ".", 2)
	if len(parts) != 2 {
		return false
	}
	payloadBytes, err := base64.URLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	payload := string(payloadBytes)
	if !hmac.Equal([]byte(a.sign(payload)), []byte(parts[1])) {
		return false
	}
	fields := strings.SplitN(payload, "|", 2)
	if len(fields) != 2 {
		return false
	}
	expiry, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return false
	}
	return time.Now().Unix() < expiry
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
	c, err := r.Cookie(cookieName)
	if err != nil {
		return "", false
	}
	parts := strings.SplitN(c.Value, ".", 2)
	if len(parts) != 2 {
		return "", false
	}
	payloadBytes, err := base64.URLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", false
	}
	payload := string(payloadBytes)
	if !hmac.Equal([]byte(a.sign(payload)), []byte(parts[1])) {
		return "", false
	}
	fields := strings.SplitN(payload, "|", 2)
	if len(fields) != 2 {
		return "", false
	}
	return fields[0], true
}

func (a *Auth) sign(payload string) string {
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(payload))
	return base64.URLEncoding.EncodeToString(mac.Sum(nil))
}
