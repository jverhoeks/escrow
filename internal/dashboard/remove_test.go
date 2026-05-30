package dashboard_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/allow"
	"github.com/jverhoeks/escrow/internal/block"
	"github.com/jverhoeks/escrow/internal/config"
	"github.com/jverhoeks/escrow/internal/dashboard"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/rs/zerolog"
)

func newDashWithBothLists(t *testing.T) (http.Handler, *allow.List, *block.List, *eventlog.Log) {
	t.Helper()
	al, _ := allow.New("")
	bl, _ := block.New("")
	cfg := config.DashboardConfig{
		Enabled: true, Path: "/dashboard",
		Username: "admin", Password: "pass",
		Secret: "aabbccddeeff00112233445566778899",
	}
	evLog := eventlog.New(50)
	dash := dashboard.New(cfg, evLog, zerolog.Nop(), al, bl, nil, "", 0, nil)
	r := chi.NewRouter()
	dash.Mount(r)
	return r, al, bl, evLog
}

func authReq(t *testing.T, method, path string, body []byte) *http.Request {
	t.Helper()
	auth := dashboard.NewAuth("admin", "pass", "aabbccddeeff00112233445566778899")
	rec := httptest.NewRecorder()
	auth.SetCookie(rec, httptest.NewRequest(http.MethodGet, "/", nil), "admin")
	cookie := rec.Result().Cookies()[0]
	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Header.Set("X-Escrow-Request", "1")
	req.AddCookie(cookie)
	return req
}

func TestHandleAllowRemove_RemovesEntry(t *testing.T) {
	handler, al, _, _ := newDashWithBothLists(t)
	require.NoError(t, al.Add(allow.Entry{Ecosystem: "npm", Name: "lodash", Version: "4.17.21"}))

	payload, _ := json.Marshal(map[string]string{"ecosystem": "npm", "name": "lodash", "version": "4.17.21"})
	req := authReq(t, http.MethodDelete, "/dashboard/api/allow", payload)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Empty(t, al.Entries())
}

func TestHandleAllowRemove_AuditEventLogged(t *testing.T) {
	handler, al, _, evLog := newDashWithBothLists(t)
	require.NoError(t, al.Add(allow.Entry{Ecosystem: "npm", Name: "lodash", Version: "4.17.21"}))

	payload, _ := json.Marshal(map[string]string{"ecosystem": "npm", "name": "lodash", "version": "4.17.21"})
	req := authReq(t, http.MethodDelete, "/dashboard/api/allow", payload)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	events := evLog.Events("")
	require.NotEmpty(t, events)
	assert.Equal(t, eventlog.ActionAllowlistRemove, events[0].Action)
	assert.Equal(t, "admin", events[0].Operator)
}

func TestHandleAllowRemove_NilAllowList(t *testing.T) {
	cfg := config.DashboardConfig{
		Enabled: true, Path: "/dashboard",
		Username: "admin", Password: "pass",
		Secret: "aabbccddeeff00112233445566778899",
	}
	dash := dashboard.New(cfg, eventlog.New(10), zerolog.Nop(), nil, nil, nil, "", 0, nil)
	r := chi.NewRouter()
	dash.Mount(r)

	payload, _ := json.Marshal(map[string]string{"ecosystem": "npm", "name": "x"})
	req := authReq(t, http.MethodDelete, "/dashboard/api/allow", payload)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestHandleBlockRemove_RemovesEntry(t *testing.T) {
	handler, _, bl, _ := newDashWithBothLists(t)
	require.NoError(t, bl.Add(block.Entry{Ecosystem: "npm", Name: "evil", Version: "1.0.0"}))

	payload, _ := json.Marshal(map[string]string{"ecosystem": "npm", "name": "evil", "version": "1.0.0"})
	req := authReq(t, http.MethodDelete, "/dashboard/api/block", payload)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Empty(t, bl.Entries())
}

func TestHandleBlockRemove_AuditEventLogged(t *testing.T) {
	handler, _, bl, evLog := newDashWithBothLists(t)
	require.NoError(t, bl.Add(block.Entry{Ecosystem: "npm", Name: "evil", Version: "1.0.0"}))

	payload, _ := json.Marshal(map[string]string{"ecosystem": "npm", "name": "evil", "version": "1.0.0"})
	req := authReq(t, http.MethodDelete, "/dashboard/api/block", payload)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	events := evLog.Events("")
	require.NotEmpty(t, events)
	assert.Equal(t, eventlog.ActionBlocklistRemove, events[0].Action)
}
