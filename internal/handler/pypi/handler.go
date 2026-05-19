package pypi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
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
	client         *http.Client
	upstreamURL    string
	engine         *trust.Engine // full engine: age + OSV + publisher (used at download time)
	listingEngine  *trust.Engine // age-only engine: used during index listing to avoid per-version network calls
	policy         *policy.Engine
	cache          cache.Cache
	blockSdist     bool
	webhook        *alerts.Webhook // may be nil
	evlog          *eventlog.Log
	sfJSON         singleflight.Group // dedup concurrent JSON manifest fetches
	sfSimple       singleflight.Group // dedup concurrent simple-index fetches
}

func (h *Handler) WithWebhook(wh *alerts.Webhook) *Handler {
	h.webhook = wh
	return h
}

func (h *Handler) WithListingEngine(e *trust.Engine) *Handler {
	h.listingEngine = e
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
				filename, ok := f["filename"].(string)
				if !ok {
					continue
				}
				// PEP 503: href with sha256 fragment for integrity verification.
				href := "/pypi/packages/" + filename
				if digests, ok := f["digests"].(map[string]any); ok {
					if sha256, ok := digests["sha256"].(string); ok && sha256 != "" {
						href += "#sha256=" + sha256
					}
				}
				fmt.Fprintf(&buf, "<a href=%q", href)

				// PEP 700: upload timestamp for age-aware clients.
				uploadTime, _ := f["upload_time_iso_8601"].(string)
				if uploadTime == "" {
					uploadTime, _ = f["upload_time"].(string)
				}
				if uploadTime != "" {
					fmt.Fprintf(&buf, " data-upload-time=%q", uploadTime)
				}

				// PEP 345: Python version constraint.
				if rp, ok := f["requires_python"].(string); ok && rp != "" {
					fmt.Fprintf(&buf, " data-requires-python=%q", html.EscapeString(rp))
				}

				// PEP 592: yanked releases.
				if yanked, ok := f["yanked"].(bool); ok && yanked {
					reason, _ := f["yanked_reason"].(string)
					fmt.Fprintf(&buf, " data-yanked=%q", html.EscapeString(reason))
				}

				// PEP 658: dist-info metadata file (lets uv fetch 28 KB instead of full wheel).
				// Only wheels are guaranteed to have a .metadata sidecar on the CDN.
				if strings.HasSuffix(filename, ".whl") {
					fmt.Fprintf(&buf, ` data-dist-info-metadata="true" data-core-metadata="true"`)
				}

				fmt.Fprintf(&buf, ">%s</a>\n", filename)
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
	// PEP 658: metadata sidecar — fetch only the METADATA file, not the full wheel.
	if strings.HasSuffix(filename, ".metadata") {
		h.serveFileMetadata(w, r, strings.TrimSuffix(filename, ".metadata"))
		return
	}
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
	// Look up the actual CDN URL that was cached when the package index was fetched.
	// On a cold cache miss, warm it by fetching the package JSON on-demand.
	fileURL := ""
	if b, _ := h.cache.GetMeta(r.Context(), "pypi/fileurl/"+filename); len(b) > 0 {
		fileURL = string(b)
	} else if pkg := pkgFromFilename(filename); pkg != "" {
		h.fetchReleases(r.Context(), pkg)
		if b, _ := h.cache.GetMeta(r.Context(), "pypi/fileurl/"+filename); len(b) > 0 {
			fileURL = string(b)
		}
	}
	if fileURL == "" {
		http.Error(w, "upstream error: file URL not resolved", http.StatusBadGateway)
		return
	}
	resp, err := h.client.Get(fileURL)
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
	// Cache filename → CDN URL so ServeFile can proxy to the correct location.
	for _, files := range meta.Releases {
		for _, f := range files {
			if fn, ok := f["filename"].(string); ok {
				if u, ok := f["url"].(string); ok && u != "" {
					h.cache.SetMeta(ctx, "pypi/fileurl/"+fn, []byte(u), 24*time.Hour)
				}
			}
		}
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

// serveFileMetadata proxies the PEP 658 dist-info metadata sidecar ({file}.metadata).
func (h *Handler) serveFileMetadata(w http.ResponseWriter, r *http.Request, filename string) {
	fileURL := ""
	if b, _ := h.cache.GetMeta(r.Context(), "pypi/fileurl/"+filename); len(b) > 0 {
		fileURL = string(b)
	} else if pkg := pkgFromFilename(filename); pkg != "" {
		h.fetchReleases(r.Context(), pkg)
		if b, _ := h.cache.GetMeta(r.Context(), "pypi/fileurl/"+filename); len(b) > 0 {
			fileURL = string(b)
		}
	}
	if fileURL == "" {
		http.NotFound(w, r)
		return
	}
	resp, err := h.client.Get(fileURL + ".metadata")
	if err != nil || resp.StatusCode != http.StatusOK {
		if err == nil {
			resp.Body.Close()
		}
		http.NotFound(w, r)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.Copy(w, resp.Body)
}

// pkgFromFilename extracts a normalized package name from a wheel or sdist filename.
// Examples: "requests-2.31.0-py3-none-any.whl" → "requests"
//           "Django-4.2.tar.gz" → "django"
func pkgFromFilename(filename string) string {
	base := strings.TrimSuffix(filename, ".whl")
	base = strings.TrimSuffix(base, ".tar.gz")
	base = strings.TrimSuffix(base, ".zip")
	if i := strings.Index(base, "-"); i > 0 {
		return strings.ToLower(strings.ReplaceAll(base[:i], "_", "-"))
	}
	return ""
}
