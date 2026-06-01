package tui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func testServer(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/dashboard/login", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.FormValue("username") == "root" && r.FormValue("password") == "escrow" {
			http.SetCookie(w, &http.Cookie{Name: "escrow_session", Value: "ok", Path: "/"})
			w.WriteHeader(http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusFound) // login always 302s; auth is enforced on protected routes
	})
	mux.HandleFunc("/dashboard/api/stats", func(w http.ResponseWriter, r *http.Request) {
		if c, _ := r.Cookie("escrow_session"); c == nil {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"blocked":2,"warned":1,"allowed":5,"top_blocked":[{"package":"x","count":2}]}`))
	})
	mux.HandleFunc("/dashboard/api/cves", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":"GHSA-x","severity":"HIGH","ecosystem":"npm","package":"lodash","version":"4.17.11"}]`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestClient_LoginThenStats(t *testing.T) {
	srv := testServer(t)
	c, err := NewClient(srv.URL, "/dashboard", "root", "escrow")
	require.NoError(t, err)
	require.NoError(t, c.Login())

	st, err := c.Stats()
	require.NoError(t, err)
	require.Equal(t, 2, st.Blocked)
	require.Equal(t, 5, st.Allowed)

	cves, err := c.CVEs()
	require.NoError(t, err)
	require.Len(t, cves, 1)
	require.Equal(t, "GHSA-x", cves[0].ID)
}

func TestClient_StatsWithoutLoginUnauthorized(t *testing.T) {
	srv := testServer(t)
	c, _ := NewClient(srv.URL, "/dashboard", "root", "escrow")
	_, err := c.Stats()
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "unauthor"))
}
