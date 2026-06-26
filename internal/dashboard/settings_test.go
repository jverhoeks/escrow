package dashboard_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jverhoeks/escrow/internal/allow"
	"github.com/jverhoeks/escrow/internal/config"
	"github.com/jverhoeks/escrow/internal/dashboard"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func settingsDash(t *testing.T, path string, reload dashboard.ReloadFunc) http.Handler {
	t.Helper()
	al, _ := allow.New("")
	cfg := config.DashboardConfig{Enabled: true, Path: "/dashboard", Username: "admin", Password: "pass", Secret: "aabbccddeeff00112233445566778899"}
	dash := dashboard.New(cfg, eventlog.New(10), zerolog.Nop(), al, nil, nil, nil, nil, nil, nil, nil, path, reload)
	r := chi.NewRouter()
	dash.Mount(r)
	return r
}

func TestReload_CallsReloadFunc(t *testing.T) {
	called := false
	reload := func() (dashboard.ReloadResult, error) {
		called = true
		return dashboard.ReloadResult{Reloaded: []string{"policy"}, RestartRequired: []string{"storage"}}, nil
	}
	h := settingsDash(t, "/tmp/escrow.toml", reload)
	req := authenticatedRequest(t, http.MethodPost, "/dashboard/api/reload", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, called)
	var out dashboard.ReloadResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Equal(t, []string{"policy"}, out.Reloaded)
	require.Equal(t, []string{"storage"}, out.RestartRequired)
}

func TestGetSettings_MasksPasswordFlag(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "escrow.toml")
	os.WriteFile(path, []byte("[server]\n  port = 7888\n[dashboard]\n  password = \"sekret\"\n"), 0o600)
	h := settingsDash(t, path, nil)

	req := authenticatedRequest(t, http.MethodGet, "/dashboard/api/settings", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var out struct {
		Config           map[string]any `json:"config"`
		PasswordEditable bool           `json:"password_editable"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.False(t, out.PasswordEditable)
	require.NotEmpty(t, out.Config)
}

func TestPostSettings_PreservesPasswordAndReloads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "escrow.toml")
	os.WriteFile(path, []byte("[server]\n  port = 7888\n[dashboard]\n  password = \"original\"\n  secret = \"aabbccddeeff00112233445566778899\"\n"), 0o600)
	reloaded := false
	reload := func() (dashboard.ReloadResult, error) { reloaded = true; return dashboard.ReloadResult{Reloaded: []string{"policy"}}, nil }
	h := settingsDash(t, path, reload)

	body := []byte(`{"server":{"port":9000},"dashboard":{"password":"hacked"}}`)
	req := authenticatedRequest(t, http.MethodPost, "/dashboard/api/settings", body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, reloaded)

	saved, _ := config.Load(path)
	require.Equal(t, 9000, saved.Server.Port)
	require.Equal(t, "original", saved.Dashboard.Password)
}

func TestPostSettings_RejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "escrow.toml")
	os.WriteFile(path, []byte("[server]\n  port = 7888\n"), 0o600)
	h := settingsDash(t, path, func() (dashboard.ReloadResult, error) { return dashboard.ReloadResult{}, nil })

	body := []byte(`{"server":{"port":70000}}`)
	req := authenticatedRequest(t, http.MethodPost, "/dashboard/api/settings", body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	saved, _ := config.Load(path)
	require.Equal(t, 7888, saved.Server.Port)
}
