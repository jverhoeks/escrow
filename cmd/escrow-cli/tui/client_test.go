package tui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// The real dashboard redirects an expired/missing session to /login (200 HTML)
// rather than returning 401. The client must NOT follow that redirect and must
// report it as unauthorized (not a confusing HTML-decode error).
func TestClient_Redirect302ReportedAsUnauthorized(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/dashboard/api/stats", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dashboard/login", http.StatusFound)
	})
	mux.HandleFunc("/dashboard/login", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><body>login</body></html>"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c, _ := NewClient(srv.URL, "/dashboard", "root", "escrow")
	_, err := c.Stats()
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "unauthor")
}

// Regression: the SSE stream must use a client WITHOUT an http.Client.Timeout.
// http.Client.Timeout also caps response-body reads, so sharing the 10s JSON
// client for the long-lived stream force-closed the live feed every ~10s and
// dropped events during the reconnect gap. One-shot JSON calls keep the 10s.
func TestClient_StreamClientHasNoTimeout(t *testing.T) {
	c, err := NewClient("http://127.0.0.1:7888", "/dashboard", "root", "escrow")
	require.NoError(t, err)
	require.Equal(t, 10*time.Second, c.http.Timeout, "one-shot JSON client keeps its bounded timeout")
	require.Zero(t, c.stream.Timeout, "SSE stream client must have no timeout (ctx controls lifetime)")
	require.NotNil(t, c.stream.Jar)
	require.Same(t, c.http.Jar, c.stream.Jar, "stream shares the cookie jar so the session cookie applies")
}

// Stream() should deliver events end-to-end over the dedicated stream client.
func TestClient_EgressLog(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/dashboard/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "escrow_session", Value: "ok", Path: "/"})
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/dashboard/api/egresslog", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"timestamp":"2026-06-05T10:00:00Z","host":"a.com","ip":"1.2.3.4","verb":"CONNECT","action":"allow","reason":"tunnel"}]`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, err := NewClient(srv.URL, "/dashboard", "root", "escrow")
	require.NoError(t, err)
	require.NoError(t, c.Login())
	rows, err := c.EgressLog(50)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "a.com", rows[0].Host)
	require.Equal(t, "allow", rows[0].Action)
	require.Equal(t, "CONNECT", rows[0].Verb)
}

func TestClient_StreamDeliversEvents(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/dashboard/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "escrow_session", Value: "ok", Path: "/"})
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/dashboard/api/stream", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		require.True(t, ok)
		fmt.Fprint(w, ": connected\n\n")
		fl.Flush()
		fmt.Fprint(w, "data: {\"package\":\"evil@1.0.0\",\"action\":\"block\",\"ecosystem\":\"npm\"}\n\n")
		fl.Flush()
		<-r.Context().Done() // hold the stream open until the client cancels
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c, err := NewClient(srv.URL, "/dashboard", "root", "escrow")
	require.NoError(t, err)
	require.NoError(t, c.Login())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := c.Stream(ctx)
	require.NoError(t, err)

	select {
	case e := <-ch:
		require.Equal(t, "evil@1.0.0", e.Package)
		require.Equal(t, "block", e.Action)
	case <-time.After(3 * time.Second):
		t.Fatal("no event received from SSE stream")
	}
}
