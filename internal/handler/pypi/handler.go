package pypi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/go-chi/chi/v5"
	"github.com/jverhoeks/escrow/internal/alerts"
	"github.com/jverhoeks/escrow/internal/cache"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/jverhoeks/escrow/internal/gate"
	"github.com/jverhoeks/escrow/internal/metrics"
	"github.com/jverhoeks/escrow/internal/pkgname"
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/staleserve"
	"github.com/jverhoeks/escrow/internal/trust"
	"github.com/jverhoeks/escrow/internal/upstream"
	"golang.org/x/sync/singleflight"
)

const manifestTTL = 5 * time.Minute

type Handler struct {
	client        *http.Client
	metaClient    *http.Client // metadata fetches: shares transport, total timeout (#73)
	upstreamURL   string
	engine        *trust.Engine // full engine: age + OSV + publisher (used at download time)
	listingEngine *trust.Engine // age-only engine: used during index listing to avoid per-version network calls
	policy        *policy.Engine
	cache         cache.Cache
	blockSdist    bool
	webhook       *alerts.Webhook // may be nil
	evlog         *eventlog.Log
	sfJSON        singleflight.Group // dedup concurrent JSON manifest fetches
	sfSimple      singleflight.Group // dedup concurrent simple-index fetches
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
	return &Handler{client: client, metaClient: upstream.MetadataClient(client), upstreamURL: upstreamURL, engine: engine, policy: pol, cache: c, blockSdist: blockSdist, evlog: evLog}
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
	if !pkgname.Safe(name) {
		http.Error(w, "invalid package name", http.StatusBadRequest)
		return
	}
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
		if staleserve.Serve(w, r, h.cache, cacheKey, "text/html", "pypi", "simple") {
			return
		}
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
		resp, err := h.metaClient.Get(fmt.Sprintf("%s/pypi/%s/json", h.upstreamURL, name))
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
		if staleserve.Serve(w, r, h.cache, cacheKey, "application/json", "pypi", "manifest") {
			return
		}
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw.([]byte))
}

func (h *Handler) ServeFile(w http.ResponseWriter, r *http.Request, filename string) {
	if !pkgname.Safe(filename) {
		http.Error(w, "invalid package name", http.StatusBadRequest)
		return
	}
	// PEP 658: metadata sidecar — fetch only the METADATA file, not the full wheel.
	if strings.HasSuffix(filename, ".metadata") {
		h.serveFileMetadata(w, r, strings.TrimSuffix(filename, ".metadata"))
		return
	}
	if h.blockSdist && (strings.HasSuffix(filename, ".tar.gz") || strings.HasSuffix(filename, ".zip")) {
		http.Error(w, `{"blocked":true,"signal":"sdist","reason":"source distributions are blocked by policy"}`, http.StatusForbidden)
		return
	}
	// Name/version are parsed from the wheel/sdist filename; the name is PEP
	// 503-normalized to match the listing events (the normalized simple-index
	// name pip/uv requests).
	name, version := pkgVersionFromFilename(filename)

	// Enforce policy on the artifact path before serving any bytes (blocklist +
	// OSV): a blocked or known-vulnerable version must not be downloadable even
	// via a pinned URL or a warm cache. See internal/gate.
	if name != "" && version != "" {
		if gate.Check(r.Context(), h.engine, h.policy, h.evlog,
			trust.Package{Ecosystem: trust.EcosystemPyPI, Name: name, Version: version}).Action == policy.ActionBlock {
			http.Error(w, "blocked by policy", http.StatusForbidden)
			return
		}
	}

	// Record a successful download event once per served artifact, on both
	// cache-hit and cache-miss serve paths.
	recordDownload := func() {
		if h.evlog == nil || name == "" || version == "" {
			return
		}
		h.evlog.Record(eventlog.PackageEvent{
			Ecosystem: string(trust.EcosystemPyPI),
			Package:   name + "@" + version,
			Action:    "allow",
			Kind:      eventlog.KindDownloaded,
			Reason:    "artifact downloaded",
		})
	}

	cacheKey := "pypi/packages/" + filename
	if blob, _ := h.cache.GetBlob(r.Context(), cacheKey); blob != nil {
		defer blob.Close()
		metrics.CacheHitsTotal.WithLabelValues("pypi", "blob").Inc()
		io.Copy(w, blob)
		recordDownload()
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

	// #46: download to a temp file while hashing, verify against the
	// upstream-declared sha256 (cached from the JSON API), then cache and serve
	// only verified bytes. A mismatch is rejected — never cached, never served.
	// When no digest is available (a pinned/cold fetch, or an old release), serve
	// unverified (fail open) but log it. This trades byte-1 streaming for
	// integrity; acceptable on a cache miss. The temp file keeps memory bounded
	// for large wheels. (The #45 streaming pipe stays for the other handlers.)
	wantDigest := ""
	if b, _ := h.cache.GetMeta(r.Context(), "pypi/digest/"+filename); len(b) > 0 {
		wantDigest = string(b)
	}
	tmp, err := os.CreateTemp("", "escrow-pypi-*")
	if err != nil {
		http.Error(w, "cache temp error", http.StatusInternalServerError)
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	sum := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, sum), resp.Body); err != nil {
		tmp.Close()
		http.Error(w, "upstream read error", http.StatusBadGateway)
		return
	}
	tmp.Close()

	if wantDigest != "" {
		got := hex.EncodeToString(sum.Sum(nil))
		if !strings.EqualFold(got, wantDigest) {
			log.Warn().Str("file", filename).Str("want", wantDigest).Str("got", got).
				Msg("pypi artifact sha256 mismatch — refusing to cache or serve")
			http.Error(w, "artifact integrity check failed", http.StatusBadGateway)
			return
		}
	} else {
		log.Debug().Str("file", filename).Msg("pypi artifact served without sha256 verification (no digest available)")
	}

	// Commit verified bytes to the cache (best-effort), then serve them.
	if f, ferr := os.Open(tmpName); ferr == nil {
		if serr := h.cache.SetBlob(context.Background(), cacheKey, f); serr != nil {
			log.Debug().Err(serr).Str("file", filename).Msg("pypi cache write failed; serving verified bytes anyway")
		}
		f.Close()
	}
	if f, ferr := os.Open(tmpName); ferr == nil {
		io.Copy(w, f) //nolint:errcheck
		f.Close()
	}
	recordDownload()
}

func (h *Handler) fetchReleases(ctx context.Context, name string) map[string][]map[string]any {
	resp, err := h.metaClient.Get(fmt.Sprintf("%s/pypi/%s/json", h.upstreamURL, name))
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
	// Cache filename → CDN URL so ServeFile can proxy to the correct location,
	// and filename → sha256 so ServeFile can verify the downloaded bytes (#46).
	for _, files := range meta.Releases {
		for _, f := range files {
			fn, ok := f["filename"].(string)
			if !ok {
				continue
			}
			if u, ok := f["url"].(string); ok && u != "" {
				h.cache.SetMeta(ctx, "pypi/fileurl/"+fn, []byte(u), 24*time.Hour)
			}
			if digests, ok := f["digests"].(map[string]any); ok {
				if sum, ok := digests["sha256"].(string); ok && sum != "" {
					h.cache.SetMeta(ctx, "pypi/digest/"+fn, []byte(sum), 24*time.Hour)
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
			Kind:      eventlog.KindScanned,
			Vulns:     d.Vulns,
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
	resp, err := h.metaClient.Get(fileURL + ".metadata")
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

// pkgFromFilename extracts a normalized package name from a wheel or sdist
// filename. Examples: "requests-2.31.0-py3-none-any.whl" → "requests";
// "django-allauth-0.50.0.tar.gz" → "django-allauth". Delegates to
// pkgVersionFromFilename so the hyphenated-sdist handling stays in one place
// (see #67).
func pkgFromFilename(filename string) string {
	name, _ := pkgVersionFromFilename(filename)
	return name
}

// normalizePyPI applies PEP 503 name normalization: runs of [-_.] collapse to a
// single '-' and the result is lowercased. This matches the simple-index name
// pip/uv request (and that the listing path records), so download events merge
// onto the same tree node.
func normalizePyPI(name string) string {
	var b strings.Builder
	prevSep := false
	for _, r := range name {
		switch r {
		case '-', '_', '.':
			if !prevSep && b.Len() > 0 {
				b.WriteByte('-')
			}
			prevSep = true
		default:
			if r >= 'A' && r <= 'Z' {
				r += 'a' - 'A'
			}
			b.WriteRune(r)
			prevSep = false
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

// pkgVersionFromFilename parses (normalized name, version) from a wheel or sdist
// filename. Wheels (PEP 427) are reliable: "name-version-pytag-abi-plat.whl",
// so field[1] is the version. Sdists use "name-version.tar.gz"/".zip"; a naive
// first-'-' split misparses hyphenated names ("django-allauth-0.50.0" → name
// "django"), which lets a blocklisted hyphenated sdist slip past gate.Check.
// Instead split at the first '-' that is FOLLOWED BY A DIGIT: a PEP 440 version
// always starts with a digit (or "N!" epoch), and package-name segments don't,
// so "django-allauth-0.50.0" → name "django-allauth", version "0.50.0", while
// modern PEP 625 names (underscores) still parse correctly. Returns "","" if
// unparseable. (Residual: a name segment that itself starts with a digit, e.g.
// "foo-2bar-1.0", can still misparse — rare; the digit-boundary heuristic is a
// best-effort improvement over the first-'-' split.)
func pkgVersionFromFilename(filename string) (name, version string) {
	if strings.HasSuffix(filename, ".whl") {
		base := strings.TrimSuffix(filename, ".whl")
		parts := strings.Split(base, "-")
		if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
			return normalizePyPI(parts[0]), parts[1]
		}
		return "", ""
	}
	base := filename
	switch {
	case strings.HasSuffix(filename, ".tar.gz"):
		base = strings.TrimSuffix(filename, ".tar.gz")
	case strings.HasSuffix(filename, ".zip"):
		base = strings.TrimSuffix(filename, ".zip")
	default:
		return "", "" // not a recognized artifact (e.g. .egg, .metadata)
	}
	for i := 0; i < len(base); i++ {
		if base[i] == '-' && i+1 < len(base) && base[i+1] >= '0' && base[i+1] <= '9' {
			return normalizePyPI(base[:i]), base[i+1:]
		}
	}
	return "", ""
}
