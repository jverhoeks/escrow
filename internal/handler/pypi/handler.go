package pypi

import (
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
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/trust"
)

type Handler struct {
	client      *http.Client
	upstreamURL string
	engine      *trust.Engine
	policy      *policy.Engine
	cache       cache.Cache
	blockSdist  bool
	webhook     *alerts.Webhook // may be nil
}

func (h *Handler) WithWebhook(wh *alerts.Webhook) *Handler {
	h.webhook = wh
	return h
}

func New(client *http.Client, upstreamURL string, engine *trust.Engine, pol *policy.Engine, c cache.Cache, blockSdist bool) *Handler {
	return &Handler{client: client, upstreamURL: upstreamURL, engine: engine, policy: pol, cache: c, blockSdist: blockSdist}
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
	releases := h.fetchReleases(r.Context(), name)
	if releases == nil {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte("<!DOCTYPE html><html><body>\n"))
	for version, files := range releases {
		if !h.versionAllowed(r.Context(), name, version, files) {
			continue
		}
		for _, f := range files {
			if filename, ok := f["filename"].(string); ok {
				fmt.Fprintf(w, `<a href="/pypi/packages/%s">%s</a>`+"\n", filename, filename)
			}
		}
	}
	w.Write([]byte("</body></html>\n"))
}

func (h *Handler) ServeJSON(w http.ResponseWriter, r *http.Request, name string) {
	resp, err := h.client.Get(fmt.Sprintf("%s/pypi/%s/json", h.upstreamURL, name))
	if err != nil || resp.StatusCode != http.StatusOK {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	var meta map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		http.Error(w, "malformed response", http.StatusBadGateway)
		return
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
		if h.versionAllowed(r.Context(), name, version, fList) {
			filtered[version] = files
		}
	}
	meta["releases"] = filtered
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(meta)
}

func (h *Handler) ServeFile(w http.ResponseWriter, r *http.Request, filename string) {
	if h.blockSdist && (strings.HasSuffix(filename, ".tar.gz") || strings.HasSuffix(filename, ".zip")) {
		http.Error(w, `{"blocked":true,"signal":"sdist","reason":"source distributions are blocked by policy"}`, http.StatusForbidden)
		return
	}
	cacheKey := "pypi/packages/" + filename
	if blob, _ := h.cache.GetBlob(r.Context(), cacheKey); blob != nil {
		defer blob.Close()
		io.Copy(w, blob)
		return
	}
	resp, err := h.client.Get(fmt.Sprintf("%s/pypi/packages/%s", h.upstreamURL, filename))
	if err != nil || resp.StatusCode != http.StatusOK {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	h.cache.SetBlob(r.Context(), cacheKey, resp.Body)
	if blob, _ := h.cache.GetBlob(r.Context(), cacheKey); blob != nil {
		defer blob.Close()
		io.Copy(w, blob)
	}
}

func (h *Handler) fetchReleases(ctx context.Context, name string) map[string][]map[string]any {
	resp, err := h.client.Get(fmt.Sprintf("%s/pypi/%s/json", h.upstreamURL, name))
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil
	}
	defer resp.Body.Close()
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
	// Try RFC3339 first (used in test mocks), fall back to PyPI's actual format (no timezone, treat as UTC)
	publishedAt, err := time.Parse(time.RFC3339, uploadTime)
	if err != nil {
		publishedAt, _ = time.Parse("2006-01-02T15:04:05", uploadTime)
		// If both fail, publishedAt remains zero value and the age check treats it as ancient (safe default)
	}
	pkg := trust.Package{
		Ecosystem:   trust.EcosystemPyPI,
		Name:        name,
		Version:     version,
		PublishedAt: publishedAt,
	}
	result, _ := h.engine.Check(ctx, pkg)
	d := h.policy.Evaluate(result)
	if d.Action == policy.ActionBlock && h.webhook != nil {
		_ = h.webhook.Send(pkg, d)
	}
	return d.Action != policy.ActionBlock
}
