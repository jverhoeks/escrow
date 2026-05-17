package pypi

import (
	"bytes"
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

const manifestTTL = 5 * time.Minute

type Handler struct {
	client      *http.Client
	upstreamURL string
	engine      *trust.Engine
	policy      *policy.Engine
	cache       cache.Cache
	blockSdist  bool
	webhook     *alerts.Webhook // may be nil
	evlog       *eventlog.Log
	sfJSON      singleflight.Group // dedup concurrent JSON manifest fetches
	sfSimple    singleflight.Group // dedup concurrent simple-index fetches
}

func (h *Handler) WithWebhook(wh *alerts.Webhook) *Handler {
	h.webhook = wh
	return h
}

func New(client *http.Client, upstreamURL string, engine *trust.Engine, pol *policy.Engine, c cache.Cache, blockSdist bool, evLog *eventlog.Log) *Handler {
	return &Handler{client: client, upstreamURL: upstreamURL, engine: engine, policy: pol, cache: c, blockSdist: blockSdist, evlog: evLog}
}

func (h *Handler) Mount(r chi.Router) {
	r.Get("/pypi/simple/{package}/", func(w http.ResponseWriter, r *http.Request) {
		h.ServeSimpleIndex(w, r, chi.URLParam(r, "package"))
	})
	r.Get("/pypi/{package}/json", func(w http.ResponseWriter, r *http.Request) {
		h.ServeJSON(w, r, chi.URLParam(r, "package"))
	})
	r.Get("/pypi/packages/{filename}", func(w http.ResponseWriter, r *http.Request) {
		h.ServeFile(w, r, chi.URLParam(r, "filename"))
	})
}

func (h *Handler) ServeSimpleIndex(w http.ResponseWriter, r *http.Request, name string) {
	cacheKey := "pypi/meta/simple/" + name
	if cached, _ := h.cache.GetMeta(r.Context(), cacheKey); cached != nil {
		metrics.CacheHitsTotal.WithLabelValues("pypi", "simple").Inc()
		w.Header().Set("Content-Type", "text/html")
		w.Write(cached)
		return
	}

	raw, err, _ := h.sfSimple.Do(name, func() (any, error) {
		releases := h.fetchReleases(context.Background(), name)
		if releases == nil {
			return nil, fmt.Errorf("upstream error")
		}
		var buf bytes.Buffer
		buf.WriteString("<!DOCTYPE html><html><body>\n")
		for version, files := range releases {
			if !h.versionAllowed(context.Background(), name, version, files) {
				continue
			}
			for _, f := range files {
				if filename, ok := f["filename"].(string); ok {
					fmt.Fprintf(&buf, `<a href="/pypi/packages/%s">%s</a>`+"\n", filename, filename)
				}
			}
		}
		buf.WriteString("</body></html>\n")
		data := buf.Bytes()
		h.cache.SetMeta(context.Background(), cacheKey, data, manifestTTL)
		return data, nil
	})
	if err != nil {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	w.Write(raw.([]byte))
}

func (h *Handler) ServeJSON(w http.ResponseWriter, r *http.Request, name string) {
	cacheKey := "pypi/meta/json/" + name
	if cached, _ := h.cache.GetMeta(r.Context(), cacheKey); cached != nil {
		metrics.CacheHitsTotal.WithLabelValues("pypi", "manifest").Inc()
		w.Header().Set("Content-Type", "application/json")
		w.Write(cached)
		return
	}

	raw, err, _ := h.sfJSON.Do(name, func() (any, error) {
		t0 := time.Now()
		resp, err := h.client.Get(fmt.Sprintf("%s/pypi/%s/json", h.upstreamURL, name))
		metrics.ProxyRequestDuration.WithLabelValues("pypi").Observe(time.Since(t0).Seconds())
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("upstream %d", resp.StatusCode)
		}
		defer resp.Body.Close()
		var meta map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
			return nil, err
		}
		releases, _ := meta["releases"].(map[string]any)
		filtered := make(map[string]any)
		for version, files := range releases {
			var fList []map[string]any
			if arr, ok := files.([]any); ok {
				for _, f := range arr {
					if m, ok := f.(map[string]any); ok {
						fList = append(fList, m)
					}
				}
			}
			if h.versionAllowed(context.Background(), name, version, fList) {
				filtered[version] = files
			}
		}
		meta["releases"] = filtered
		data, _ := json.Marshal(meta)
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

func (h *Handler) ServeFile(w http.ResponseWriter, r *http.Request, filename string) {
	if h.blockSdist && (strings.HasSuffix(filename, ".tar.gz") || strings.HasSuffix(filename, ".zip")) {
		http.Error(w, `{"blocked":true,"signal":"sdist","reason":"source distributions are blocked by policy"}`, http.StatusForbidden)
		return
	}
	cacheKey := "pypi/packages/" + filename
	if blob, _ := h.cache.GetBlob(r.Context(), cacheKey); blob != nil {
		defer blob.Close()
		metrics.CacheHitsTotal.WithLabelValues("pypi", "blob").Inc()
		io.Copy(w, blob)
		return
	}
	resp, err := h.client.Get(fmt.Sprintf("%s/pypi/packages/%s", h.upstreamURL, filename))
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

func (h *Handler) fetchReleases(ctx context.Context, name string) map[string][]map[string]any {
	resp, err := h.client.Get(fmt.Sprintf("%s/pypi/%s/json", h.upstreamURL, name))
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var meta struct {
		Releases map[string][]map[string]any `json:"releases"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return nil
	}
	return meta.Releases
}

func (h *Handler) versionAllowed(ctx context.Context, name, version string, files []map[string]any) bool {
	uploadTime := ""
	for _, f := range files {
		if t, ok := f["upload_time"].(string); ok {
			uploadTime = t
			break
		}
	}
	publishedAt, err := time.Parse(time.RFC3339, uploadTime)
	if err != nil {
		publishedAt, _ = time.Parse("2006-01-02T15:04:05", uploadTime)
	}
	pkg := trust.Package{
		Ecosystem:   trust.EcosystemPyPI,
		Name:        name,
		Version:     version,
		PublishedAt: publishedAt,
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
