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

const maxEventsPerRequest = 1000
const maxBodyBytes = 64 * 1024 // 64 KB for API request bodies

type Dashboard struct {
	cfg          config.DashboardConfig
	auth         *Auth
	loginLimiter *loginRateLimiter
	log          *eventlog.Log
	logger       zerolog.Logger
	allowList    *allow.List // may be nil
	blockList    *block.List // may be nil
	cache        cache.Cache // may be nil
}

func New(cfg config.DashboardConfig, log *eventlog.Log, logger zerolog.Logger, allowList *allow.List, blockList *block.List, c cache.Cache) *Dashboard {
	return &Dashboard{
		cfg:          cfg,
		auth:         NewAuth(cfg.Username, cfg.Password, cfg.Secret),
		loginLimiter: newLoginRateLimiter(),
		log:          log,
		logger:       logger,
		allowList:    allowList,
		blockList:    blockList,
		cache:        c,
	}
}

// originOK returns false when the request comes from a different origin (CSRF guard).
func (d *Dashboard) originOK(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // non-browser API call
	}
	return strings.HasPrefix(origin, "http://"+r.Host) ||
		strings.HasPrefix(origin, "https://"+r.Host)
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
	protected.Delete("/api/allow", d.handleAllowRemove)
	protected.Get("/api/allowlist", d.handleAllowList)
	protected.Post("/api/block", d.handleBlock)
	protected.Delete("/api/block", d.handleBlockRemove)
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
	if d.loginLimiter.isLockedOut(r) {
		http.Redirect(w, r, d.cfg.Path+"/login?error=Too+many+failed+attempts.+Try+again+later.", http.StatusFound)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	r.ParseForm()
	if !d.auth.CheckCredentials(r.FormValue("username"), r.FormValue("password")) {
		d.loginLimiter.recordFailure(r)
		http.Redirect(w, r, d.cfg.Path+"/login?error=Invalid+credentials", http.StatusFound)
		return
	}
	d.loginLimiter.recordSuccess(r)
	d.auth.SetCookie(w, r, r.FormValue("username"))
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

	// Subscribe BEFORE writing any response bytes — once headers are flushed
	// http.Error is a no-op and the client sees garbled SSE instead of a 503.
	ch, unsub := d.log.Subscribe()
	if ch == nil {
		http.Error(w, "too many live streams", http.StatusServiceUnavailable)
		return
	}
	defer unsub()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	// Disable the server WriteTimeout for SSE connections — it would kill the stream
	// after write_timeout_seconds (default 120s), silently disconnecting the dashboard.
	// ReadHeaderTimeout still guards against Slowloris on the initial request.
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		d.logger.Warn().Err(err).Msg("could not disable SSE write deadline; dashboard streams may disconnect after write_timeout_seconds")
	}
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		case e := <-ch:
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
		if v, err := strconv.Atoi(s); err == nil && v > 0 && v <= maxEventsPerRequest {
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
	if !d.originOK(r) {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}
	if d.allowList == nil {
		http.Error(w, `{"error":"allowlist not configured"}`, http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
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
	if d.log != nil {
		d.log.Record(eventlog.PackageEvent{
			Ecosystem: req.Ecosystem,
			Package:   req.Name + "@" + req.Version,
			Action:    eventlog.ActionAllowlistAdd,
			Reason:    req.Reason,
			Operator:  username,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (d *Dashboard) handleAllowRemove(w http.ResponseWriter, r *http.Request) {
	if !d.originOK(r) {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}
	if d.allowList == nil {
		http.Error(w, `{"error":"allowlist not configured"}`, http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var req struct {
		Ecosystem string `json:"ecosystem"`
		Name      string `json:"name"`
		Version   string `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.Ecosystem == "" || req.Name == "" {
		http.Error(w, `{"error":"ecosystem and name are required"}`, http.StatusBadRequest)
		return
	}
	if err := d.allowList.Remove(req.Ecosystem, req.Name, req.Version); err != nil {
		http.Error(w, `{"error":"failed to save allowlist"}`, http.StatusInternalServerError)
		return
	}
	username, _ := d.auth.Username(r)
	if d.log != nil {
		d.log.Record(eventlog.PackageEvent{
			Ecosystem: req.Ecosystem,
			Package:   req.Name + "@" + req.Version,
			Action:    eventlog.ActionAllowlistRemove,
			Operator:  username,
		})
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
// Only ecosystems with fully predictable cache key formats are checked here.
// PyPI, Go, and Maven blob keys include the filename or upstream URL which
// cannot be determined from package name+version alone.
func blobCached(ctx context.Context, c cache.Cache, ecosystem, name, version string) bool {
	var key string
	switch ecosystem {
	case "npm":
		basename := name
		if i := strings.LastIndex(name, "/"); i >= 0 {
			basename = name[i+1:]
		}
		key = fmt.Sprintf("npm/%s/-/%s-%s.tgz", name, basename, version)
	case "cargo":
		key = fmt.Sprintf("cargo/crates/%s/%s/download", name, version)
	case "nuget":
		id := strings.ToLower(name)
		ver := strings.ToLower(version)
		key = fmt.Sprintf("nuget/pkgs/%s/%s/%s.%s.nupkg", id, ver, id, ver)
	}
	if key == "" {
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
	if !d.originOK(r) {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}
	if d.blockList == nil {
		http.Error(w, `{"error":"blocklist not configured"}`, http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
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
	if d.log != nil {
		d.log.Record(eventlog.PackageEvent{
			Ecosystem: req.Ecosystem,
			Package:   req.Name + "@" + req.Version,
			Action:    eventlog.ActionBlocklistAdd,
			Reason:    req.Reason,
			Operator:  username,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (d *Dashboard) handleBlockRemove(w http.ResponseWriter, r *http.Request) {
	if !d.originOK(r) {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}
	if d.blockList == nil {
		http.Error(w, `{"error":"blocklist not configured"}`, http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var req struct {
		Ecosystem string `json:"ecosystem"`
		Name      string `json:"name"`
		Version   string `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.Ecosystem == "" || req.Name == "" {
		http.Error(w, `{"error":"ecosystem and name are required"}`, http.StatusBadRequest)
		return
	}
	if err := d.blockList.Remove(req.Ecosystem, req.Name, req.Version); err != nil {
		http.Error(w, `{"error":"failed to save blocklist"}`, http.StatusInternalServerError)
		return
	}
	username, _ := d.auth.Username(r)
	if d.log != nil {
		d.log.Record(eventlog.PackageEvent{
			Ecosystem: req.Ecosystem,
			Package:   req.Name + "@" + req.Version,
			Action:    eventlog.ActionBlocklistRemove,
			Operator:  username,
		})
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
