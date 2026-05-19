package nuget

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/sync/singleflight"
	"github.com/jverhoeks/escrow/internal/alerts"
	"github.com/jverhoeks/escrow/internal/cache"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/jverhoeks/escrow/internal/metrics"
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/trust"
)

const (
	registrationTTL = 5 * time.Minute
	versionsTTL     = 5 * time.Minute
)

type Handler struct {
	client           *http.Client
	upstreamURL      string // e.g. "https://api.nuget.org/v3"
	flatcontainerURL string // e.g. "https://api.nuget.org/v3-flatcontainer" (derived if empty)
	engine           *trust.Engine // full engine: age + OSV + publisher (download time)
	listingEngine    *trust.Engine // age-only engine (registration/version listing)
	policy           *policy.Engine
	cache            cache.Cache
	evlog            *eventlog.Log
	webhook          *alerts.Webhook // may be nil
	sf               singleflight.Group
}

func New(client *http.Client, upstreamURL string, engine *trust.Engine, pol *policy.Engine, c cache.Cache, evLog *eventlog.Log) *Handler {
	if upstreamURL == "" {
		upstreamURL = "https://api.nuget.org/v3"
	}
	return &Handler{
		client:      client,
		upstreamURL: upstreamURL,
		engine:      engine,
		policy:      pol,
		cache:       c,
		evlog:       evLog,
	}
}

// SetFlatcontainerURL overrides the flatcontainer base URL (for non-standard upstreams such as
// Nexus or Azure Artifacts that don't follow the api.nuget.org /v3 → /v3-flatcontainer convention).
func (h *Handler) SetFlatcontainerURL(u string) { h.flatcontainerURL = u }

func (h *Handler) WithWebhook(wh *alerts.Webhook) *Handler {
	h.webhook = wh
	return h
}

// WithListingEngine sets the age-only engine used during registration/version listing.
func (h *Handler) WithListingEngine(e *trust.Engine) *Handler {
	h.listingEngine = e
	return h
}

func (h *Handler) Mount(r chi.Router) {
	r.Route("/nuget", func(r chi.Router) {
		r.Get("/index.json", h.serveIndex)
		r.Get("/v3/registration5-semver1/{id}/index.json", h.serveRegistration)
		r.Get("/v3-flatcontainer/{id}/index.json", h.serveVersionList)
		r.Get("/v3-flatcontainer/{id}/{version}/{filename}", h.serveDownload)
	})
}

// serveIndex returns the NuGet v3 service index pointing at our proxy endpoints.
func (h *Handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	base := proxyBase(r)
	index := map[string]any{
		"version": "3.0.0",
		"resources": []map[string]any{
			{
				"@id":     base + "/v3/registration5-semver1/",
				"@type":   "RegistrationsBaseUrl/3.6.0",
				"comment": "Package registration with escrow age and vulnerability filtering",
			},
			{
				"@id":     base + "/v3-flatcontainer/",
				"@type":   "PackageBaseAddress/3.0.0",
				"comment": "Package download base",
			},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(index)
}

// regCacheKey returns a host-aware cache key for the NuGet registration.
// The key is host-specific because filterRegistration rewrites packageContent URLs
// to include the proxy's host — cached data for localhost:7888 would be wrong
// if served under escrow.corp.internal.
func regCacheKey(id, host string) string {
	return "nuget/reg/" + host + "/" + id
}

// serveRegistration fetches the NuGet registration index, filters by trust policy, and caches the result.
func (h *Handler) serveRegistration(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(chi.URLParam(r, "id"))
	cacheKey := regCacheKey(id, r.Host)

	if cached, _ := h.cache.GetMeta(r.Context(), cacheKey); cached != nil {
		metrics.CacheHitsTotal.WithLabelValues("nuget", "registration").Inc()
		w.Header().Set("Content-Type", "application/json")
		w.Write(cached)
		return
	}

	base := proxyBase(r)
	raw, err, _ := h.sf.Do("reg:"+r.Host+":"+id, func() (any, error) {
		upURL := fmt.Sprintf("%s/registration5-semver1/%s/index.json", h.upstreamURL, id)
		t0 := time.Now()
		resp, err := h.client.Get(upURL)
		metrics.ProxyRequestDuration.WithLabelValues("nuget").Observe(time.Since(t0).Seconds())
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("upstream %d", resp.StatusCode)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		filtered := h.filterRegistration(context.Background(), id, body, base)
		h.cache.SetMeta(context.Background(), cacheKey, filtered, registrationTTL)
		return filtered, nil
	})
	if err != nil {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw.([]byte))
}

// serveVersionList returns the filtered list of available versions for a package.
// It derives the list from the (cached) filtered registration.
func (h *Handler) serveVersionList(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(chi.URLParam(r, "id"))
	// Version list cache is also host-aware because it's derived from the host-aware registration.
	cacheKey := "nuget/versions/" + r.Host + "/" + id

	if cached, _ := h.cache.GetMeta(r.Context(), cacheKey); cached != nil {
		metrics.CacheHitsTotal.WithLabelValues("nuget", "versions").Inc()
		w.Header().Set("Content-Type", "application/json")
		w.Write(cached)
		return
	}

	// Use the host-aware registration cache to derive the version list.
	rCacheKey := regCacheKey(id, r.Host)
	regData, _ := h.cache.GetMeta(r.Context(), rCacheKey)
	if regData == nil {
		// Registration not cached — fetch it now.
		regURL := fmt.Sprintf("%s/registration5-semver1/%s/index.json", h.upstreamURL, id)
		resp, err := h.client.Get(regURL)
		if err != nil || resp.StatusCode != http.StatusOK {
			if resp != nil {
				resp.Body.Close()
			}
			http.Error(w, "upstream error", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		regData, err = io.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, "upstream read error", http.StatusBadGateway)
			return
		}
		base := proxyBase(r)
		regData = h.filterRegistration(context.Background(), id, regData, base)
		h.cache.SetMeta(context.Background(), rCacheKey, regData, registrationTTL)
	}

	versions := extractVersions(regData)
	result, _ := json.Marshal(map[string][]string{"versions": versions})
	h.cache.SetMeta(context.Background(), cacheKey, result, versionsTTL)
	w.Header().Set("Content-Type", "application/json")
	w.Write(result)
}

// serveDownload proxies a .nupkg file, caching it as a blob.
func (h *Handler) serveDownload(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(chi.URLParam(r, "id"))
	version := strings.ToLower(chi.URLParam(r, "version"))
	filename := chi.URLParam(r, "filename")
	cacheKey := fmt.Sprintf("nuget/pkgs/%s/%s/%s", id, version, filename)

	if blob, _ := h.cache.GetBlob(r.Context(), cacheKey); blob != nil {
		defer blob.Close()
		metrics.CacheHitsTotal.WithLabelValues("nuget", "blob").Inc()
		io.Copy(w, blob)
		return
	}

	fcBase := h.flatcontainerURL
	if fcBase == "" {
		fcBase = flatcontainerBase(h.upstreamURL)
	}
	upURL := fmt.Sprintf("%s/%s/%s/%s", fcBase, id, version, filename)

	resp, err := h.client.Get(upURL)
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

// filterRegistration parses the NuGet registration JSON, runs the trust engine on each
// version, removes blocked entries, and rewrites packageContent URLs to our proxy.
// base is the proxy's nuget prefix (e.g. "http://localhost:7888/nuget").
func (h *Handler) filterRegistration(ctx context.Context, pkgID string, data []byte, base string) []byte {
	// Determine the upstream flatcontainer URL so packageContent URLs can be rewritten to our proxy.
	upstreamFC := h.flatcontainerURL
	if upstreamFC == "" {
		upstreamFC = flatcontainerBase(h.upstreamURL)
	}
	var reg map[string]any
	if err := json.Unmarshal(data, &reg); err != nil {
		return data
	}

	pages, _ := reg["items"].([]any)
	filteredPages := make([]any, 0, len(pages))

	for _, page := range pages {
		p, ok := page.(map[string]any)
		if !ok {
			filteredPages = append(filteredPages, page)
			continue
		}
		items, _ := p["items"].([]any)
		if items == nil {
			// Paged registration: fetch the page from upstream and filter inline.
			if pageID, _ := p["@id"].(string); pageID != "" {
				if pageItems, err := h.fetchRegistrationPage(ctx, pageID); err == nil && len(pageItems) > 0 {
					items = pageItems
				}
			}
			if items == nil {
				// Page fetch failed — omit the page entirely rather than proxying it unfiltered.
				// Proxying unfiltered would expose versions that haven't passed the trust check.
				// Clients will not see those versions; they can retry later.
				continue
			}
		}

		filteredItems := make([]any, 0, len(items))
		for _, item := range items {
			itm, ok := item.(map[string]any)
			if !ok {
				filteredItems = append(filteredItems, item)
				continue
			}
			ce, _ := itm["catalogEntry"].(map[string]any)
			if ce == nil {
				filteredItems = append(filteredItems, item)
				continue
			}
			version, _ := ce["version"].(string)
			publishedStr, _ := ce["published"].(string)
			publishedAt, _ := time.Parse(time.RFC3339, publishedStr)
			if publishedAt.IsZero() {
				publishedAt, _ = time.Parse("2006-01-02T15:04:05Z", publishedStr)
			}
			// Use catalogEntry.id for the canonical package name (original case like "Newtonsoft.Json").
			// OSV package name queries are case-sensitive; using the lowercased pkgID would return no results.
			canonicalID := pkgID
			if id, ok := ce["id"].(string); ok && id != "" {
				canonicalID = id
			}

			pkg := trust.Package{
				Ecosystem:   trust.EcosystemNuGet,
				Name:        canonicalID,
				Version:     version,
				PublishedAt: publishedAt,
			}
			eng := h.engine
			if h.listingEngine != nil {
				eng = h.listingEngine
			}
			result, _ := eng.Check(ctx, pkg)
			d := h.policy.Evaluate(result)

			metrics.RequestsTotal.WithLabelValues(string(pkg.Ecosystem), string(d.Action)).Inc()
			if h.evlog != nil {
				h.evlog.Record(eventlog.PackageEvent{
					Ecosystem: string(pkg.Ecosystem),
					Package:   canonicalID + "@" + version, // canonical case for dashboard display
					Action:    string(d.Action),
					Signal:    d.Signal,
					Reason:    d.Reason,
				})
			}
			if d.Action == policy.ActionBlock {
				metrics.BlocksTotal.WithLabelValues(string(pkg.Ecosystem), d.Signal).Inc()
				if h.webhook != nil {
					_ = h.webhook.Send(pkg, d)
				}
				continue
			}
			// Rewrite packageContent to proxy URL.
			if pc, ok := itm["packageContent"].(string); ok {
				itm["packageContent"] = rewritePackageContent(pc, upstreamFC, base)
			}
			filteredItems = append(filteredItems, itm)
		}

		if len(filteredItems) == 0 {
			continue
		}
		p["items"] = filteredItems
		p["count"] = float64(len(filteredItems))
		filteredPages = append(filteredPages, p)
	}

	reg["items"] = filteredPages
	reg["count"] = float64(len(filteredPages))
	result, _ := json.Marshal(reg)
	return result
}

// extractVersions pulls version strings from a filtered registration JSON blob.
// Pages with nil items (paged pages that could not be fetched) are skipped;
// the version list is intentionally incomplete rather than including unfiltered versions.
func extractVersions(regData []byte) []string {
	var reg map[string]any
	if err := json.Unmarshal(regData, &reg); err != nil {
		return nil
	}
	var versions []string
	pages, _ := reg["items"].([]any)
	for _, page := range pages {
		p, ok := page.(map[string]any)
		if !ok {
			continue
		}
		items, _ := p["items"].([]any)
		if items == nil {
			// Paged page that was proxied without fetching — skip to avoid including
			// unfiltered versions in the flatcontainer version list.
			continue
		}
		for _, item := range items {
			itm, ok := item.(map[string]any)
			if !ok {
				continue
			}
			ce, _ := itm["catalogEntry"].(map[string]any)
			if ce == nil {
				continue
			}
			if v, ok := ce["version"].(string); ok {
				versions = append(versions, strings.ToLower(v))
			}
		}
	}
	return versions
}

// fetchRegistrationPage fetches a single paged registration page from the upstream URL.
func (h *Handler) fetchRegistrationPage(ctx context.Context, pageURL string) ([]any, error) {
	resp, err := h.client.Get(pageURL)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("upstream %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var page struct {
		Items []any `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, err
	}
	return page.Items, nil
}

// rewritePackageContent replaces the upstream flatcontainer base URL with our proxy base.
// upstreamFC is the upstream's flatcontainer base (e.g. "https://api.nuget.org/v3-flatcontainer").
// proxyBase is the escrow proxy's nuget prefix (e.g. "http://localhost:7888/nuget").
func rewritePackageContent(original, upstreamFC, proxyBase string) string {
	// Try the upstream's own flatcontainer URL first (handles custom upstreams).
	if upstreamFC != "" {
		withSlash := upstreamFC + "/"
		if strings.HasPrefix(original, withSlash) {
			return proxyBase + "/v3-flatcontainer/" + original[len(withSlash):]
		}
	}
	// Fallback: rewrite known NuGet.org URLs even if the upstream is configured differently.
	for _, prefix := range []string{
		"https://api.nuget.org/v3-flatcontainer/",
		"http://api.nuget.org/v3-flatcontainer/",
	} {
		if strings.HasPrefix(original, prefix) {
			return proxyBase + "/v3-flatcontainer/" + original[len(prefix):]
		}
	}
	return original
}

// proxyBase returns the scheme+host+/nuget prefix for URL rewriting.
func proxyBase(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := r.Host
	if host == "" {
		host = "localhost:7888" // fallback for HTTP/1.0 requests without Host header
	}
	return scheme + "://" + host + "/nuget"
}

// flatcontainerBase derives the v3-flatcontainer base URL from the v3 upstream URL.
// "https://api.nuget.org/v3" → "https://api.nuget.org/v3-flatcontainer"
func flatcontainerBase(v3URL string) string {
	if strings.HasSuffix(v3URL, "/v3") {
		return v3URL[:len(v3URL)-3] + "/v3-flatcontainer"
	}
	return v3URL + "-flatcontainer"
}
