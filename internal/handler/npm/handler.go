package npm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

func extractAuthor(versionData map[string]any) string {
	if npmUser, ok := versionData["_npmUser"].(map[string]any); ok {
		if name, ok := npmUser["name"].(string); ok && name != "" {
			return name
		}
	}
	if maintainers, ok := versionData["maintainers"].([]any); ok && len(maintainers) > 0 {
		if m, ok := maintainers[0].(map[string]any); ok {
			if name, ok := m["name"].(string); ok && name != "" {
				return name
			}
		}
	}
	return ""
}

type Handler struct {
	client         *http.Client
	upstreamURL    string
	engine         *trust.Engine // full engine: age + OSV + publisher (download time)
	listingEngine  *trust.Engine // age-only engine (manifest filtering)
	policy         *policy.Engine
	cache          cache.Cache
	webhook        *alerts.Webhook // may be nil
	evlog          *eventlog.Log
	sf             singleflight.Group
}

func (h *Handler) WithWebhook(wh *alerts.Webhook) *Handler {
	h.webhook = wh
	return h
}

func (h *Handler) WithListingEngine(e *trust.Engine) *Handler {
	h.listingEngine = e
	return h
}

func New(client *http.Client, upstreamURL string, engine *trust.Engine, pol *policy.Engine, c cache.Cache, evLog *eventlog.Log) *Handler {
	return &Handler{client: client, upstreamURL: upstreamURL, engine: engine, policy: pol, cache: c, evlog: evLog}
}

func (h *Handler) Mount(r chi.Router) {
	r.Get("/{package}", func(w http.ResponseWriter, r *http.Request) {
		h.ServeManifest(w, r, chi.URLParam(r, "package"))
	})
	r.Get("/{scope}/{package}", func(w http.ResponseWriter, r *http.Request) {
		h.ServeManifest(w, r, chi.URLParam(r, "scope")+"/"+chi.URLParam(r, "package"))
	})
	r.Get("/{package}/-/{tarball}", func(w http.ResponseWriter, r *http.Request) {
		h.ServeTarball(w, r, chi.URLParam(r, "package"), chi.URLParam(r, "tarball"))
	})
	r.Get("/{scope}/{package}/-/{tarball}", func(w http.ResponseWriter, r *http.Request) {
		h.ServeTarball(w, r,
			chi.URLParam(r, "scope")+"/"+chi.URLParam(r, "package"),
			chi.URLParam(r, "tarball"))
	})
}

func (h *Handler) ServeManifest(w http.ResponseWriter, r *http.Request, name string) {
	cacheKey := "npm/meta/" + name
	if cached, _ := h.cache.GetMeta(r.Context(), cacheKey); cached != nil {
		metrics.CacheHitsTotal.WithLabelValues("npm", "manifest").Inc()
		w.Header().Set("Content-Type", "application/json")
		w.Write(cached)
		return
	}

	// Deduplicate concurrent cold-cache fetches for the same package.
	raw, err, _ := h.sf.Do(name, func() (any, error) {
		t0 := time.Now()
		resp, err := h.client.Get(fmt.Sprintf("%s/%s", h.upstreamURL, name))
		metrics.ProxyRequestDuration.WithLabelValues("npm").Observe(time.Since(t0).Seconds())
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("upstream %d", resp.StatusCode)
		}
		defer resp.Body.Close()
		var manifest map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
			return nil, err
		}
		manifest = h.filterManifest(context.Background(), name, manifest)
		data, _ := json.Marshal(manifest)
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

func (h *Handler) filterManifest(ctx context.Context, name string, manifest map[string]any) map[string]any {
	versions, _ := manifest["versions"].(map[string]any)
	times, _ := manifest["time"].(map[string]any)
	if versions == nil {
		return manifest
	}

	blocked := map[string]bool{}
	// Track one representative decision per signal for webhook dedup.
	type webhookKey struct{ signal string }
	webhookSent := map[webhookKey]bool{}

	for version := range versions {
		versionData, _ := versions[version].(map[string]any)
		publishedStr, _ := times[version].(string)
		publishedAt, _ := time.Parse(time.RFC3339, publishedStr)
		pkg := trust.Package{
			Ecosystem:   trust.EcosystemNPM,
			Name:        name,
			Version:     version,
			PublishedAt: publishedAt,
			Author:      extractAuthor(versionData),
		}
		eng := h.engine
		if h.listingEngine != nil {
			eng = h.listingEngine
		}
		result, _ := eng.Check(ctx, pkg)
		decision := h.policy.Evaluate(result)
		metrics.RequestsTotal.WithLabelValues(string(pkg.Ecosystem), string(decision.Action)).Inc()
		if decision.Action == policy.ActionBlock {
			metrics.BlocksTotal.WithLabelValues(string(pkg.Ecosystem), decision.Signal).Inc()
			blocked[version] = true
			delete(versions, version)
			// Send at most one webhook per unique signal type per manifest filter.
			if h.webhook != nil {
				key := webhookKey{decision.Signal}
				if !webhookSent[key] {
					_ = h.webhook.Send(pkg, decision)
					webhookSent[key] = true
				}
			}
		}
		if h.evlog != nil {
			h.evlog.Record(eventlog.PackageEvent{
				Ecosystem: string(pkg.Ecosystem),
				Package:   pkg.Name + "@" + pkg.Version,
				Action:    string(decision.Action),
				Signal:    decision.Signal,
				Reason:    decision.Reason,
				Vulns:     decision.Vulns,
			})
		}
	}

	// Reassign dist-tags if the tagged version was blocked.
	if distTags, ok := manifest["dist-tags"].(map[string]any); ok {
		for tag, ver := range distTags {
			v, ok := ver.(string)
			if !ok || !blocked[v] {
				continue
			}
			newest := ""
			newestTime := time.Time{}
			for v2 := range versions {
				if ts, ok := times[v2].(string); ok {
					if t2, err := time.Parse(time.RFC3339, ts); err == nil && t2.After(newestTime) {
						newest = v2
						newestTime = t2
					}
				}
			}
			if newest != "" {
				distTags[tag] = newest
			} else {
				delete(distTags, tag)
			}
		}
	}
	manifest["versions"] = versions
	return manifest
}

func (h *Handler) ServeTarball(w http.ResponseWriter, r *http.Request, pkg, tarball string) {
	cacheKey := fmt.Sprintf("npm/%s/-/%s", pkg, tarball)
	if blob, _ := h.cache.GetBlob(r.Context(), cacheKey); blob != nil {
		defer blob.Close()
		metrics.CacheHitsTotal.WithLabelValues("npm", "blob").Inc()
		io.Copy(w, blob)
		return
	}
	resp, err := h.client.Get(fmt.Sprintf("%s/%s/-/%s", h.upstreamURL, pkg, tarball))
	if err != nil {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		http.Error(w, "upstream error", http.StatusBadGateway)
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
