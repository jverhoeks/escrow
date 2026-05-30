package cargo

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jverhoeks/escrow/internal/alerts"
	"github.com/jverhoeks/escrow/internal/cache"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/jverhoeks/escrow/internal/metrics"
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/trust"
	"github.com/jverhoeks/escrow/internal/upstream"
)

const (
	defaultUpstreamURL = "https://index.crates.io"
	defaultDownloadURL = "https://static.crates.io"
	defaultAPIURL      = "https://crates.io"
	userAgent          = "escrow/0.3 (github.com/jverhoeks/escrow)"
	versionMetaTTL     = 1 * time.Hour
)

// versionMeta holds the metadata fetched from the crates.io API for a single version.
type versionMeta struct {
	CreatedAt   time.Time
	PublishedBy string
}

// Handler proxies the Cargo sparse registry protocol.
type Handler struct {
	client        *http.Client
	upstreamURL   string // "https://index.crates.io"
	downloadURL   string // "https://static.crates.io"
	apiURL        string // "https://crates.io"
	engine        *trust.Engine // full engine: age + OSV + publisher (download time)
	listingEngine *trust.Engine // age-only engine (index listing)
	policy        *policy.Engine
	cache         cache.Cache
	evlog         *eventlog.Log
	webhook       *alerts.Webhook // may be nil
}

// New creates a Cargo handler with the given dependencies.
func New(client *http.Client, engine *trust.Engine, pol *policy.Engine, c cache.Cache, evLog *eventlog.Log) *Handler {
	return &Handler{
		client:      client,
		upstreamURL: defaultUpstreamURL,
		downloadURL: defaultDownloadURL,
		apiURL:      defaultAPIURL,
		engine:      engine,
		policy:      pol,
		cache:       c,
		evlog:       evLog,
	}
}

// WithWebhook sets the alert webhook and returns the handler for chaining.
func (h *Handler) WithWebhook(wh *alerts.Webhook) *Handler {
	h.webhook = wh
	return h
}

// WithListingEngine sets the age-only engine used during index listing.
func (h *Handler) WithListingEngine(e *trust.Engine) *Handler {
	h.listingEngine = e
	return h
}

// SetUpstreamURL overrides the index upstream URL (for testing).
func (h *Handler) SetUpstreamURL(u string) { h.upstreamURL = u }

// SetDownloadURL overrides the download upstream URL (for testing).
func (h *Handler) SetDownloadURL(u string) { h.downloadURL = u }

// SetAPIURL overrides the crates.io API URL (for testing).
func (h *Handler) SetAPIURL(u string) { h.apiURL = u }

// Mount registers Cargo routes on the given chi router.
// Specific routes are registered before the wildcard to avoid conflicts.
func (h *Handler) Mount(r chi.Router) {
	r.Route("/cargo", func(r chi.Router) {
		r.Get("/config.json", h.serveConfig)
		r.Get("/crates/{name}/{version}/download", h.serveDownload)
		r.Get("/*", h.serveIndex)
	})
}

// serveConfig returns the Cargo sparse registry config.json.
// The "dl" field must point to this proxy's host so Cargo fetches .crate files through us.
func (h *Handler) serveConfig(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if host == "" {
		host = "localhost:7888"
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	cfg := map[string]string{
		"dl":  fmt.Sprintf("%s://%s/cargo/crates/{crate}/{version}/download", scheme, host),
		"api": "https://crates.io",
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cfg)
}

// serveIndex proxies the crate index from index.crates.io and filters blocked versions.
// The index is NDJSON (one version per line). Blocked versions are omitted entirely.
func (h *Handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	indexPath := chi.URLParam(r, "*")
	// indexPath looks like "se/rd/serde" or "3/s/syn" etc.
	// Extract the crate name from the last segment.
	parts := strings.Split(strings.Trim(indexPath, "/"), "/")
	name := parts[len(parts)-1]

	upURL := fmt.Sprintf("%s/%s", h.upstreamURL, indexPath)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upURL, nil)
	if err != nil {
		http.Error(w, "upstream request build error", http.StatusInternalServerError)
		return
	}
	req.Header.Set("User-Agent", userAgent)

	t0 := time.Now()
	resp, err := h.client.Do(req)
	metrics.ProxyRequestDuration.WithLabelValues("cargo").Observe(time.Since(t0).Seconds())
	if err != nil {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		return
	}

	// Fetch version metadata once (cached) so we know PublishedAt for each version.
	versionMetaMap := h.fetchVersionMeta(r.Context(), name)

	// Filter the NDJSON index line by line.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	scanner := bufio.NewScanner(resp.Body)
	// Some crate index files can have very long lines (many deps). Use a larger buffer.
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		var entry struct {
			Vers string `json:"vers"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			// Malformed line — pass through unchanged to avoid silently dropping data.
			fmt.Fprintln(w, line)
			continue
		}

		meta := versionMetaMap[entry.Vers]
		blocked := h.checkTrust(r, w, name, entry.Vers, meta.CreatedAt, meta.PublishedBy)
		if !blocked {
			fmt.Fprintln(w, line)
		}
		// If blocked, the line is simply omitted (no HTTP error written — response already started).
	}
}

// serveDownload proxies a .crate file from static.crates.io, caching it locally.
func (h *Handler) serveDownload(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	version := chi.URLParam(r, "version")

	cacheKey := fmt.Sprintf("cargo/crates/%s/%s/download", name, version)
	if blob, _ := h.cache.GetBlob(r.Context(), cacheKey); blob != nil {
		defer blob.Close()
		metrics.CacheHitsTotal.WithLabelValues("cargo", "blob").Inc()
		io.Copy(w, blob)
		return
	}

	upURL := fmt.Sprintf("%s/crates/%s/%s/download", h.downloadURL, name, version)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upURL, nil)
	if err != nil {
		http.Error(w, "upstream request build error", http.StatusInternalServerError)
		return
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := h.client.Do(req)
	if err != nil {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		return
	}

	// Stream to client and cache simultaneously; wait for cache write before returning
	// so the next request always finds the blob (prevents cache-miss race on sequential requests).
	pr, pw := io.Pipe()
	cacheDone := make(chan struct{})
	go func() {
		defer close(cacheDone)
		h.cache.SetBlob(context.Background(), cacheKey, pr)
	}()
	_, copyErr := io.Copy(w, io.TeeReader(resp.Body, pw))
	pw.CloseWithError(copyErr)
	<-cacheDone
}

// fetchVersionMeta fetches crate version metadata from the crates.io API, caching for 1 hour.
// Returns a map of version number → versionMeta. On failure, returns an empty map (safe default).
func (h *Handler) fetchVersionMeta(ctx context.Context, name string) map[string]versionMeta {
	cacheKey := fmt.Sprintf("cargo/versions/%s", name)

	// Try the cache first.
	if data, _ := h.cache.GetMeta(ctx, cacheKey); data != nil {
		return parseVersionMeta(data)
	}

	apiURL := fmt.Sprintf("%s/api/v1/crates/%s/versions", h.apiURL, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return map[string]versionMeta{}
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := h.client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return map[string]versionMeta{}
	}
	defer resp.Body.Close()

	body, err := upstream.ReadBody(resp.Body)
	if err != nil {
		return map[string]versionMeta{}
	}

	// Cache the raw JSON for 1 hour.
	h.cache.SetMeta(ctx, cacheKey, body, versionMetaTTL)

	return parseVersionMeta(body)
}

// parseVersionMeta parses the crates.io versions JSON into a map.
func parseVersionMeta(data []byte) map[string]versionMeta {
	var apiResp struct {
		Versions []struct {
			Num         string    `json:"num"`
			CreatedAt   time.Time `json:"created_at"`
			PublishedBy *struct {
				Login string `json:"login"`
			} `json:"published_by"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(data, &apiResp); err != nil {
		return map[string]versionMeta{}
	}

	result := make(map[string]versionMeta, len(apiResp.Versions))
	for _, v := range apiResp.Versions {
		meta := versionMeta{CreatedAt: v.CreatedAt}
		if v.PublishedBy != nil {
			meta.PublishedBy = v.PublishedBy.Login
		}
		result[v.Num] = meta
	}
	return result
}

// checkTrust runs all trust signals for a crate version and records the event.
// It writes a 403 response if blocked (but only when the response has not yet started).
// Returns true if the version is blocked.
//
// NOTE: serveIndex writes the response header before calling this function (streaming NDJSON).
// For index filtering, the caller must NOT rely on the 403 being written — it will silently
// skip the line instead. checkTrust is also used standalone in tests.
func (h *Handler) checkTrust(r *http.Request, w http.ResponseWriter, name, version string, publishedAt time.Time, author string) bool {
	pkg := trust.Package{
		Ecosystem:   trust.EcosystemCargo,
		Name:        name,
		Version:     version,
		PublishedAt: publishedAt,
		Author:      author,
	}
	eng := h.engine
	if h.listingEngine != nil {
		eng = h.listingEngine
	}
	result, _ := eng.Check(r.Context(), pkg)
	d := h.policy.Evaluate(result)

	metrics.RequestsTotal.WithLabelValues(string(pkg.Ecosystem), string(d.Action)).Inc()
	if d.Action == policy.ActionBlock {
		metrics.BlocksTotal.WithLabelValues(string(pkg.Ecosystem), d.Signal).Inc()
	}
	if h.evlog != nil {
		h.evlog.Record(eventlog.PackageEvent{
			Ecosystem: string(pkg.Ecosystem),
			Package:   name + "@" + version,
			Action:    string(d.Action),
			Signal:    d.Signal,
			Reason:    d.Reason,
		})
	}
	if d.Action == policy.ActionBlock && h.webhook != nil {
		_ = h.webhook.Send(pkg, d)
	}
	return d.Action == policy.ActionBlock
}

