package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jverhoeks/escrow/internal/allow"
	"github.com/jverhoeks/escrow/internal/block"
	"github.com/jverhoeks/escrow/internal/cache"
	"github.com/jverhoeks/escrow/internal/config"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/rs/zerolog"
)

type Dashboard struct {
	cfg       config.DashboardConfig
	auth      *Auth
	log       *eventlog.Log
	logger    zerolog.Logger
	allowList *allow.List  // may be nil
	blockList *block.List  // may be nil
	cache     cache.Cache  // may be nil
}

func New(cfg config.DashboardConfig, log *eventlog.Log, logger zerolog.Logger, allowList *allow.List, blockList *block.List, c cache.Cache) *Dashboard {
	return &Dashboard{cfg: cfg, auth: NewAuth(cfg.Username, cfg.Password, cfg.Secret), log: log, logger: logger, allowList: allowList, blockList: blockList, cache: c}
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
	protected.Post("/api/block", d.handleBlock)
	protected.Get("/api/blocklist", d.handleBlockList)
	protected.Get("/api/packages", d.handlePackages)
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

func (d *Dashboard) handlePackages(w http.ResponseWriter, r *http.Request) {
	eco := r.URL.Query().Get("eco")
	all := d.log.Events("") // newest-first

	type pkgEntry struct {
		Ecosystem string    `json:"ecosystem"`
		Name      string    `json:"name"`
		Version   string    `json:"version"`
		Action    string    `json:"action"`
		Signal    string    `json:"signal"`
		Reason    string    `json:"reason"`
		LastSeen  time.Time `json:"last_seen"`
		HitCount  int       `json:"hit_count"`
		Cached    bool      `json:"cached"`
	}

	type key struct{ eco, name, version string }
	seen := map[key]*pkgEntry{}
	for _, e := range all { // newest-first: first occurrence per key = most recent status
		if eco != "" && e.Ecosystem != eco {
			continue
		}
		name, version := splitPackage(e.Package)
		k := key{e.Ecosystem, name, version}
		if existing, ok := seen[k]; ok {
			existing.HitCount++
		} else {
			seen[k] = &pkgEntry{
				Ecosystem: e.Ecosystem,
				Name:      name,
				Version:   version,
				Action:    e.Action,
				Signal:    e.Signal,
				Reason:    e.Reason,
				LastSeen:  e.Timestamp,
				HitCount:  1,
			}
		}
	}

	// Check blob cache for ecosystems with predictable cache keys.
	if d.cache != nil {
		for k, entry := range seen {
			entry.Cached = blobCached(r.Context(), d.cache, k.eco, k.name, k.version)
		}
	}

	result := make([]pkgEntry, 0, len(seen))
	for _, e := range seen {
		result = append(result, *e)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Ecosystem != result[j].Ecosystem {
			return result[i].Ecosystem < result[j].Ecosystem
		}
		if result[i].Name != result[j].Name {
			return result[i].Name < result[j].Name
		}
		return result[i].Version < result[j].Version
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// blobCached returns true if the package binary is present in the blob cache.
// Only npm and cargo have predictable cache key formats.
func blobCached(ctx context.Context, c cache.Cache, ecosystem, name, version string) bool {
	var key string
	switch ecosystem {
	case "npm":
		// scoped: @scope/pkg → cache key uses just the package basename
		basename := name
		if i := strings.LastIndex(name, "/"); i >= 0 {
			basename = name[i+1:]
		}
		key = fmt.Sprintf("npm/%s/-/%s-%s.tgz", name, basename, version)
	case "cargo":
		key = fmt.Sprintf("cargo/crates/%s/%s/download", name, version)
	default:
		return false
	}
	return c.HasBlob(ctx, key)
}

func splitPackage(pkg string) (name, version string) {
	i := strings.LastIndex(pkg, "@")
	if i <= 0 {
		return pkg, ""
	}
	return pkg[:i], pkg[i+1:]
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

func (d *Dashboard) handleBlock(w http.ResponseWriter, r *http.Request) {
	if d.blockList == nil {
		http.Error(w, `{"error":"blocklist not configured"}`, http.StatusServiceUnavailable)
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
	entry := block.Entry{
		Ecosystem: req.Ecosystem,
		Name:      req.Name,
		Version:   req.Version,
		Reason:    req.Reason,
		AddedBy:   username,
	}
	if err := d.blockList.Add(entry); err != nil {
		http.Error(w, `{"error":"failed to save blocklist"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (d *Dashboard) handleBlockList(w http.ResponseWriter, r *http.Request) {
	if d.blockList == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]struct{}{})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(d.blockList.Entries())
}
