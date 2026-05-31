package dashboard

import (
	"encoding/json"
	"net/http"

	"github.com/jverhoeks/escrow/internal/config"
)

// ReloadResult reports which config sections were applied live vs. which
// changed fields still require a restart.
type ReloadResult struct {
	Reloaded        []string `json:"reloaded"`
	RestartRequired []string `json:"restart_required"`
}

// ReloadFunc re-reads the config file, applies the live-reloadable subset, and
// reports the outcome. Constructed in main.go.
type ReloadFunc func() (ReloadResult, error)

// handleReload re-applies the on-disk config to the running process (live subset).
func (d *Dashboard) handleReload(w http.ResponseWriter, r *http.Request) {
	if !d.originOK(r) {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if d.reload == nil {
		http.Error(w, `{"error":"reload not configured"}`, http.StatusServiceUnavailable)
		return
	}
	res, err := d.reload()
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(res)
}

// handleGetSettings returns the on-disk config. The dashboard password is
// returned for the read-only reveal field but is never editable via the API.
func (d *Dashboard) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if d.configPath == "" {
		http.Error(w, `{"error":"settings unavailable (no config path)"}`, http.StatusServiceUnavailable)
		return
	}
	cfg, err := config.Load(d.configPath)
	if err != nil {
		http.Error(w, `{"error":"could not read config"}`, http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{
		"config":            cfg,
		"password_editable": false,
		"config_path":       d.configPath,
	})
}

// handleSaveSettings validates and writes the config (preserving the on-disk
// password regardless of payload), then hot-reloads the live subset.
func (d *Dashboard) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	if !d.originOK(r) {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if d.configPath == "" {
		http.Error(w, `{"error":"settings unavailable (no config path)"}`, http.StatusServiceUnavailable)
		return
	}
	// Start from the on-disk config so omitted keys keep their values.
	cur, err := config.Load(d.configPath)
	if err != nil {
		http.Error(w, `{"error":"could not read current config"}`, http.StatusInternalServerError)
		return
	}
	currentPassword := cur.Dashboard.Password

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	incoming := cur // copy, then overlay the decoded JSON
	if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	// Password is immutable via this API — always restore the on-disk value.
	incoming.Dashboard.Password = currentPassword

	if errs := incoming.Validate(); len(errs) > 0 {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": errs[0].Error()})
		return
	}
	if err := config.Save(d.configPath, incoming); err != nil {
		http.Error(w, `{"error":"failed to write config"}`, http.StatusInternalServerError)
		return
	}
	res := ReloadResult{}
	if d.reload != nil {
		res, _ = d.reload() // best-effort; the file is already saved
	}
	username, _ := d.auth.Username(r)
	d.logger.Info().Str("operator", username).Msg("settings saved via dashboard")
	json.NewEncoder(w).Encode(map[string]any{
		"ok": true, "reloaded": res.Reloaded, "restart_required": res.RestartRequired,
	})
}
