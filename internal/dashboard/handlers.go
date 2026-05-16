package dashboard

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jverhoeks/escrow/internal/allow"
	"github.com/jverhoeks/escrow/internal/config"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/rs/zerolog"
)

type Dashboard struct {
	cfg       config.DashboardConfig
	auth      *Auth
	log       *eventlog.Log
	logger    zerolog.Logger
	allowList *allow.List // may be nil
}

func New(cfg config.DashboardConfig, log *eventlog.Log, logger zerolog.Logger, allowList *allow.List) *Dashboard {
	return &Dashboard{cfg: cfg, auth: NewAuth(cfg.Username, cfg.Password, cfg.Secret), log: log, logger: logger, allowList: allowList}
}

func (d *Dashboard) Mount(r chi.Router) {
	base := d.cfg.Path
	r.Get(base+"/login", d.handleLoginPage)
	r.Post(base+"/login", d.handleLoginSubmit)
	r.Get(base+"/logout", d.handleLogout)

	protected := chi.NewRouter()
	protected.Use(d.auth.Middleware(base + "/login"))
	protected.Get("/", d.handleIndex)
	protected.Get("/api/stream", d.handleStream)
	protected.Get("/api/events", d.handleEvents)
	protected.Get("/api/stats", d.handleStats)
	protected.Post("/api/allow", d.handleAllow)
	protected.Get("/api/allowlist", d.handleAllowList)
	r.Mount(base, protected)
}

func (d *Dashboard) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	raw, err := staticFS.ReadFile("static/login.html")
	if err != nil {
		http.Error(w, "login.html missing", 500)
		return
	}
	tmpl, err := template.New("login").Parse(string(raw))
	if err != nil {
		http.Error(w, "template error", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl.Execute(w, map[string]string{"Error": r.URL.Query().Get("error")})
}

func (d *Dashboard) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	if !d.auth.CheckCredentials(r.FormValue("username"), r.FormValue("password")) {
		http.Redirect(w, r, d.cfg.Path+"/login?error=Invalid+credentials", http.StatusFound)
		return
	}
	d.auth.SetCookie(w, r.FormValue("username"))
	http.Redirect(w, r, d.cfg.Path+"/", http.StatusFound)
}

func (d *Dashboard) handleLogout(w http.ResponseWriter, r *http.Request) {
	d.auth.ClearCookie(w)
	http.Redirect(w, r, d.cfg.Path+"/login", http.StatusFound)
}

func (d *Dashboard) handleIndex(w http.ResponseWriter, r *http.Request) {
	data, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "index.html missing", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func (d *Dashboard) handleStream(w http.ResponseWriter, r *http.Request) {
	eco := r.URL.Query().Get("eco")
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	ch, unsub := d.log.Subscribe()
	defer unsub()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		case e, ok := <-ch:
			if !ok {
				return
			}
			if eco != "" && e.Ecosystem != eco {
				continue
			}
			data, _ := json.Marshal(e)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

func (d *Dashboard) handleEvents(w http.ResponseWriter, r *http.Request) {
	eco := r.URL.Query().Get("eco")
	n := 100
	if s := r.URL.Query().Get("n"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			n = v
		}
	}
	events := d.log.Events(eco)
	if len(events) > n {
		events = events[:n]
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(events)
}

func (d *Dashboard) handleStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(d.log.Stats())
}

func (d *Dashboard) handleAllow(w http.ResponseWriter, r *http.Request) {
	if d.allowList == nil {
		http.Error(w, `{"error":"allowlist not configured"}`, http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Ecosystem string `json:"ecosystem"`
		Name      string `json:"name"`
		Version   string `json:"version"`
		Reason    string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.Ecosystem == "" || req.Name == "" {
		http.Error(w, `{"error":"ecosystem and name are required"}`, http.StatusBadRequest)
		return
	}
	username, _ := d.auth.Username(r)
	entry := allow.Entry{
		Ecosystem: req.Ecosystem,
		Name:      req.Name,
		Version:   req.Version,
		Reason:    req.Reason,
		AddedBy:   username,
	}
	if err := d.allowList.Add(entry); err != nil {
		http.Error(w, `{"error":"failed to save allowlist"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (d *Dashboard) handleAllowList(w http.ResponseWriter, r *http.Request) {
	if d.allowList == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]struct{}{})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(d.allowList.Entries())
}
