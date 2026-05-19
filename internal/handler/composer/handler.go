package composer

import (
	"context"
	"encoding/json"
	"fmt"
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

const manifestTTL = 5 * time.Minute

// Handler is the Composer/Packagist V2 proxy handler.
type Handler struct {
	client        *http.Client
	upstreamURL   string // e.g. "https://repo.packagist.org"
	engine        *trust.Engine // full engine: age + OSV + publisher (download time)
	listingEngine *trust.Engine // age-only engine (package manifest listing)
	policy        *policy.Engine
	cache         cache.Cache
	evlog         *eventlog.Log
	webhook       *alerts.Webhook // may be nil
	sf            singleflight.Group
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

// WithListingEngine sets the age-only engine used during package manifest listing.
func (h *Handler) WithListingEngine(e *trust.Engine) *Handler {
	h.listingEngine = e
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
	if err != nil {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}

	var root map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&root); err != nil {
		http.Error(w, "malformed response", http.StatusBadGateway)
		return
	}

	// Rewrite metadata-url to point to our proxy. Packagist may return either
	// a relative path ("/p2/%package%.json") or a full absolute URL
	// ("https://repo.packagist.org/p2/%package%.json"), so we always replace
	// it with our fixed proxy path.
	if _, ok := root["metadata-url"].(string); ok {
		root["metadata-url"] = "/composer/p2/%package%.json"
	}

	// Same for providers-url.
	if _, ok := root["providers-url"].(string); ok {
		root["providers-url"] = "/composer/p/%package%$%hash%.json"
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

	cacheKey := "composer/meta/" + pkgName
	if cached, _ := h.cache.GetMeta(r.Context(), cacheKey); cached != nil {
		metrics.CacheHitsTotal.WithLabelValues("composer", "manifest").Inc()
		w.Header().Set("Content-Type", "application/json")
		w.Write(cached)
		return
	}

	raw, err, _ := h.sf.Do(pkgName, func() (any, error) {
		t0 := time.Now()
		resp, err := h.client.Get(fmt.Sprintf("%s/p2/%s.json", h.upstreamURL, pkgName))
		metrics.ProxyRequestDuration.WithLabelValues("composer").Observe(time.Since(t0).Seconds())
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("upstream %d", resp.StatusCode)
		}
		defer resp.Body.Close()
		var payload map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			return nil, err
		}
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
					if publishedAt.IsZero() {
						// Unknown publish time on old Packagist entries — treat as ancient so the
						// age gate allows them through. Using time.Now() would block them erroneously.
						publishedAt = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
					}
					if h.versionAllowed(context.Background(), pkgName, version, publishedAt, author) {
						allowed = append(allowed, v)
					}
				}
				packages[pkgName] = allowed
			}
			payload["packages"] = packages
		}
		data, _ := json.Marshal(payload)
		h.cache.SetMeta(context.Background(), cacheKey, data, manifestTTL)
		return data, nil
	})
	if err != nil {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw.([]byte))
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
	eng := h.engine
	if h.listingEngine != nil {
		eng = h.listingEngine
	}
	result, _ := eng.Check(ctx, pkg)
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

// parseComposerTime parses Packagist time fields. Packagist uses RFC3339 in
// modern metadata but older packages use a space-separated format without
// timezone (e.g. "2011-09-13 21:42:26"). Try formats in order.
func parseComposerTime(s string) time.Time {
	for _, layout := range []string{
		time.RFC3339,
		"2006-01-02T15:04:05+00:00",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
