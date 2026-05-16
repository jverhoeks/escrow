package composer

import (
	"context"
	"encoding/json"
	"fmt"
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
)

// Handler is the Composer/Packagist V2 proxy handler.
type Handler struct {
	client      *http.Client
	upstreamURL string // e.g. "https://repo.packagist.org"
	engine      *trust.Engine
	policy      *policy.Engine
	cache       cache.Cache
	evlog       *eventlog.Log
	webhook     *alerts.Webhook // may be nil
}

// New creates a new Composer handler.
func New(client *http.Client, upstreamURL string, engine *trust.Engine, pol *policy.Engine, c cache.Cache, evLog *eventlog.Log) *Handler {
	return &Handler{
		client:      client,
		upstreamURL: upstreamURL,
		engine:      engine,
		policy:      pol,
		cache:       c,
		evlog:       evLog,
	}
}

// WithWebhook attaches an alert webhook (optional).
func (h *Handler) WithWebhook(wh *alerts.Webhook) *Handler {
	h.webhook = wh
	return h
}

// Mount registers the Composer routes on the provided router.
func (h *Handler) Mount(r chi.Router) {
	r.Route("/composer", func(r chi.Router) {
		r.Get("/packages.json", h.serveRoot)
		r.Get("/p2/*", h.servePackage)
	})
}

// serveRoot proxies /composer/packages.json from Packagist and rewrites
// metadata-url and providers-url to point to our proxy prefix.
func (h *Handler) serveRoot(w http.ResponseWriter, r *http.Request) {
	resp, err := h.client.Get(fmt.Sprintf("%s/packages.json", h.upstreamURL))
	if err != nil || resp.StatusCode != http.StatusOK {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	var root map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&root); err != nil {
		http.Error(w, "malformed response", http.StatusBadGateway)
		return
	}

	// Rewrite metadata-url: "/p2/%package%.json" → "/composer/p2/%package%.json"
	if metaURL, ok := root["metadata-url"].(string); ok {
		if !strings.HasPrefix(metaURL, "/composer") {
			root["metadata-url"] = "/composer" + metaURL
		}
	}

	// Rewrite providers-url if present: "/p/%package%$%hash%.json" → "/composer/p/%package%$%hash%.json"
	if provURL, ok := root["providers-url"].(string); ok {
		if !strings.HasPrefix(provURL, "/composer") {
			root["providers-url"] = "/composer" + provURL
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(root)
}

// servePackage proxies /composer/p2/{vendor}/{package}.json from Packagist,
// filtering out versions that don't pass trust policy.
func (h *Handler) servePackage(w http.ResponseWriter, r *http.Request) {
	// chi wildcard captures everything after /composer/p2/
	wildcard := chi.URLParam(r, "*")
	// Strip .json suffix to get the canonical package name (e.g. "symfony/console")
	pkgName := strings.TrimSuffix(wildcard, ".json")

	resp, err := h.client.Get(fmt.Sprintf("%s/p2/%s.json", h.upstreamURL, pkgName))
	if err != nil || resp.StatusCode != http.StatusOK {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		http.Error(w, "malformed response", http.StatusBadGateway)
		return
	}

	// packages is a map from "vendor/package" → []version-objects
	packages, _ := payload["packages"].(map[string]any)
	if packages != nil {
		versions, _ := packages[pkgName].([]any)
		if versions != nil {
			allowed := make([]any, 0, len(versions))
			for _, v := range versions {
				vObj, ok := v.(map[string]any)
				if !ok {
					continue
				}
				version, _ := vObj["version"].(string)
				timeStr, _ := vObj["time"].(string)
				author := extractAuthor(vObj)

				publishedAt := parseComposerTime(timeStr)

				if h.versionAllowed(r.Context(), pkgName, version, publishedAt, author) {
					allowed = append(allowed, v)
				}
			}
			packages[pkgName] = allowed
		}
		payload["packages"] = packages
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(payload)
}

// versionAllowed runs the trust engine and policy for a single version,
// records the event, fires webhook on block, and returns false if blocked.
func (h *Handler) versionAllowed(ctx context.Context, name, version string, publishedAt time.Time, author string) bool {
	pkg := trust.Package{
		Ecosystem:   trust.EcosystemComposer,
		Name:        name,
		Version:     version,
		PublishedAt: publishedAt,
		Author:      author,
	}
	result, _ := h.engine.Check(ctx, pkg)
	d := h.policy.Evaluate(result)

	metrics.RequestsTotal.WithLabelValues(string(pkg.Ecosystem), string(d.Action)).Inc()
	if d.Action == policy.ActionBlock {
		metrics.BlocksTotal.WithLabelValues(string(pkg.Ecosystem), d.Signal).Inc()
	}

	if h.evlog != nil {
		h.evlog.Record(eventlog.PackageEvent{
			Ecosystem: string(pkg.Ecosystem),
			Package:   pkg.Name + "@" + pkg.Version,
			Action:    string(d.Action),
			Signal:    d.Signal,
			Reason:    d.Reason,
		})
	}

	if d.Action == policy.ActionBlock && h.webhook != nil {
		_ = h.webhook.Send(pkg, d)
	}

	return d.Action != policy.ActionBlock
}

// extractAuthor returns authors[0].name from a Packagist version object.
func extractAuthor(vObj map[string]any) string {
	authors, ok := vObj["authors"].([]any)
	if !ok || len(authors) == 0 {
		return ""
	}
	first, ok := authors[0].(map[string]any)
	if !ok {
		return ""
	}
	name, _ := first["name"].(string)
	return name
}

// parseComposerTime parses Packagist's RFC3339 time field, falling back to
// "2006-01-02T15:04:05+00:00" if the standard parse fails.
func parseComposerTime(s string) time.Time {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	t, _ := time.Parse("2006-01-02T15:04:05+00:00", s)
	return t
}
