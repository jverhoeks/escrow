package maven

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	upstreamPkg "github.com/jverhoeks/escrow/internal/upstream"
)

const (
	metaTTL   = 5 * time.Minute
	searchTTL = 1 * time.Hour
)

type Handler struct {
	client        *http.Client
	upstreamURL   string // e.g. "https://repo1.maven.org/maven2"
	snapshotURL   string // snapshot upstream; falls back to upstreamURL if empty
	searchURL     string // Maven Central Search API base
	engine        *trust.Engine // full engine: age + OSV + publisher (artifact download time)
	listingEngine *trust.Engine // age-only engine (metadata/version listing)
	policy        *policy.Engine
	cache         cache.Cache
	evlog         *eventlog.Log
	webhook       *alerts.Webhook // may be nil
	sf            singleflight.Group
}

func New(client *http.Client, upstreamURL string, engine *trust.Engine, pol *policy.Engine, c cache.Cache, evLog *eventlog.Log) *Handler {
	if upstreamURL == "" {
		upstreamURL = "https://repo1.maven.org/maven2"
	}
	return &Handler{
		client:      client,
		upstreamURL: upstreamURL,
		searchURL:   "https://search.maven.org/solrsearch/select",
		engine:      engine,
		policy:      pol,
		cache:       c,
		evlog:       evLog,
	}
}

func (h *Handler) WithWebhook(wh *alerts.Webhook) *Handler {
	h.webhook = wh
	return h
}

// WithListingEngine sets the age-only engine used during metadata/version listing.
func (h *Handler) WithListingEngine(e *trust.Engine) *Handler {
	h.listingEngine = e
	return h
}

// SetSearchURL overrides the Maven Central Search API URL (used in tests).
func (h *Handler) SetSearchURL(u string) { h.searchURL = u }

// SetSnapshotURL sets a dedicated upstream for -SNAPSHOT artifacts (e.g. a Nexus snapshot repo).
func (h *Handler) SetSnapshotURL(u string) { h.snapshotURL = u }

func (h *Handler) Mount(r chi.Router) {
	r.Route("/maven2", func(r chi.Router) {
		r.Get("/*", h.serve)
		r.Head("/*", h.serveHead)
	})
}

// serveHead handles Maven HEAD probes (artifact existence checks).
// For uncached POMs, fetches and caches the full content so repeated HEAD and
// subsequent GET requests are served from cache — preventing 429 rate-limits
// from Maven Central caused by rapid-fire HEAD probes.
func (h *Handler) serveHead(w http.ResponseWriter, r *http.Request) {
	path := chi.URLParam(r, "*")
	upstream := h.upstreamURL
	if h.snapshotURL != "" && strings.Contains(path, "SNAPSHOT") {
		upstream = h.snapshotURL
	}
	// POMs and metadata XMLs: serve from meta cache or fetch+cache on miss.
	if strings.HasSuffix(path, ".pom") || strings.HasSuffix(path, "maven-metadata.xml") {
		cacheKey := "maven/meta/" + upstream + "/" + path
		if cached, _ := h.cache.GetMeta(r.Context(), cacheKey); cached != nil {
			w.WriteHeader(http.StatusOK)
			return
		}
		// Fetch full content and cache it — eliminates follow-up GET round-trips
		// and prevents Maven Central from rate-limiting repeated HEAD probes.
		resp, err := h.client.Get(upstream + "/" + path)
		if err != nil {
			http.Error(w, "upstream error", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			w.WriteHeader(resp.StatusCode)
			return
		}
		body, _ := upstreamPkg.ReadBody(resp.Body)
		h.cache.SetMeta(r.Context(), cacheKey, body, metaTTL)
		w.WriteHeader(http.StatusOK)
		return
	}
	// Blobs (JARs, checksums): check blob cache, fall back to a HEAD probe.
	if h.cache.HasBlob(r.Context(), "maven/artifacts/"+upstream+"/"+path) {
		w.WriteHeader(http.StatusOK)
		return
	}
	resp, err := h.client.Head(upstream + "/" + path)
	if err != nil {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
}

func (h *Handler) serve(w http.ResponseWriter, r *http.Request) {
	path := chi.URLParam(r, "*")

	// Route snapshot artifacts to the dedicated snapshot upstream if configured.
	if h.snapshotURL != "" && strings.Contains(path, "SNAPSHOT") {
		if strings.HasSuffix(path, "maven-metadata.xml") {
			h.serveMetadataFrom(w, r, path, h.snapshotURL)
			return
		}
		h.serveArtifactFrom(w, r, path, h.snapshotURL)
		return
	}

	if strings.HasSuffix(path, "maven-metadata.xml") {
		h.serveMetadata(w, r, path)
		return
	}
	h.serveArtifact(w, r, path)
}

// serveMetadataFrom proxies maven-metadata.xml from a specific upstream URL.
func (h *Handler) serveMetadataFrom(w http.ResponseWriter, r *http.Request, path, upstream string) {
	cacheKey := "maven/meta/" + upstream + "/" + path
	if cached, _ := h.cache.GetMeta(r.Context(), cacheKey); cached != nil {
		metrics.CacheHitsTotal.WithLabelValues("maven", "metadata").Inc()
		w.Header().Set("Content-Type", "application/xml")
		w.Write(cached)
		return
	}
	groupID, artifactID := ParseMavenMetaPath(path)
	sfKey := "meta:" + upstream + ":" + path
	raw, err, _ := h.sf.Do(sfKey, func() (any, error) {
		t0 := time.Now()
		resp, err := h.client.Get(upstream + "/" + path)
		metrics.ProxyRequestDuration.WithLabelValues("maven").Observe(time.Since(t0).Seconds())
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("upstream %d", resp.StatusCode)
		}
		defer resp.Body.Close()
		body, err := upstreamPkg.ReadBody(resp.Body)
		if err != nil {
			return nil, err
		}
		// Group-level metadata (e.g. org/apache/maven/plugins/maven-metadata.xml) lists
		// plugin prefixes via <plugins> — pass through unfiltered so Maven can resolve
		// short plugin names like "dependency" → maven-dependency-plugin.
		if groupID == "" || artifactID == "" || bytes.Contains(body, []byte("<plugins>")) {
			h.cache.SetMeta(context.Background(), cacheKey, body, metaTTL)
			return body, nil
		}
		timestamps, _ := h.fetchVersionTimestamps(context.Background(), groupID, artifactID)
		eng := h.engine
		if h.listingEngine != nil {
			eng = h.listingEngine
		}
		filtered := filterMetadata(context.Background(), body, groupID, artifactID, timestamps, eng, h.policy, h.evlog, h.webhook)
		h.cache.SetMeta(context.Background(), cacheKey, filtered, metaTTL)
		return filtered, nil
	})
	if err != nil {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.Write(raw.([]byte))
}

// serveArtifactFrom proxies a Maven artifact from a specific upstream URL.
func (h *Handler) serveArtifactFrom(w http.ResponseWriter, r *http.Request, path, upstream string) {
	cacheKey := "maven/artifacts/" + upstream + "/" + path
	if blob, _ := h.cache.GetBlob(r.Context(), cacheKey); blob != nil {
		defer blob.Close()
		metrics.CacheHitsTotal.WithLabelValues("maven", "blob").Inc()
		if ct := mimeByPath(path); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		io.Copy(w, blob)
		return
	}
	resp, err := h.client.Get(upstream + "/" + path)
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
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	} else if ct := mimeByPath(path); ct != "" {
		w.Header().Set("Content-Type", ct)
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

// mimeByPath returns a known Content-Type for common Maven artifact extensions.
func mimeByPath(path string) string {
	switch {
	case strings.HasSuffix(path, ".jar") || strings.HasSuffix(path, ".war") ||
		strings.HasSuffix(path, ".ear") || strings.HasSuffix(path, ".aar"):
		return "application/java-archive"
	case strings.HasSuffix(path, ".pom") || strings.HasSuffix(path, ".xml"):
		return "application/xml"
	case strings.HasSuffix(path, ".sha1"), strings.HasSuffix(path, ".md5"),
		strings.HasSuffix(path, ".sha256"), strings.HasSuffix(path, ".sha512"):
		return "text/plain; charset=utf-8"
	case strings.HasSuffix(path, ".module"):
		return "application/json"
	}
	return ""
}

func (h *Handler) serveMetadata(w http.ResponseWriter, r *http.Request, path string) {
	h.serveMetadataFrom(w, r, path, h.upstreamURL)
}

func (h *Handler) serveArtifact(w http.ResponseWriter, r *http.Request, path string) {
	h.serveArtifactFrom(w, r, path, h.upstreamURL)
}

// fetchVersionTimestamps returns a map of version → publish time using the Maven Central Search API.
func (h *Handler) fetchVersionTimestamps(ctx context.Context, groupID, artifactID string) (map[string]time.Time, error) {
	cacheKey := fmt.Sprintf("maven/search/%s/%s", groupID, artifactID)
	if cached, _ := h.cache.GetMeta(ctx, cacheKey); cached != nil {
		return parseTimestampCache(cached), nil
	}

	q := url.QueryEscape(fmt.Sprintf(`g:"%s" AND a:"%s"`, groupID, artifactID))
	apiURL := fmt.Sprintf("%s?q=%s&core=gav&rows=200&wt=json", h.searchURL, q)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := h.client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return nil, fmt.Errorf("search API unavailable")
	}
	defer resp.Body.Close()

	var result struct {
		Response struct {
			Docs []struct {
				V         string `json:"v"`
				Timestamp int64  `json:"timestamp"` // milliseconds
			} `json:"docs"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	timestamps := make(map[string]time.Time, len(result.Response.Docs))
	for _, doc := range result.Response.Docs {
		timestamps[doc.V] = time.Unix(doc.Timestamp/1000, 0)
	}

	// Cache as JSON map[version]→unix-seconds.
	data, _ := json.Marshal(timestampsToCache(timestamps))
	h.cache.SetMeta(ctx, cacheKey, data, searchTTL)
	return timestamps, nil
}

// mavenMetadata is the parsed structure of maven-metadata.xml.
type mavenMetadata struct {
	XMLName    xml.Name `xml:"metadata"`
	GroupID    string   `xml:"groupId"`
	ArtifactID string   `xml:"artifactId"`
	Versioning struct {
		Latest      string   `xml:"latest"`
		Release     string   `xml:"release"`
		Versions    versions `xml:"versions"`
		LastUpdated string   `xml:"lastUpdated"`
	} `xml:"versioning"`
}

type versions struct {
	Version []string `xml:"version"`
}

// filterMetadata parses maven-metadata.xml, removes too-new versions, and re-serializes.
func filterMetadata(ctx context.Context, data []byte, groupID, artifactID string, timestamps map[string]time.Time, engine *trust.Engine, pol *policy.Engine, evlog *eventlog.Log, wh *alerts.Webhook) []byte {
	var meta mavenMetadata
	if err := xml.Unmarshal(data, &meta); err != nil {
		return data
	}

	allowed := make([]string, 0, len(meta.Versioning.Versions.Version))
	for _, v := range meta.Versioning.Versions.Version {
		publishedAt := timestamps[v]
		pkg := trust.Package{
			Ecosystem:   trust.EcosystemMaven,
			Name:        groupID + ":" + artifactID,
			Version:     v,
			PublishedAt: publishedAt,
		}
		result, _ := engine.Check(ctx, pkg)
		d := pol.Evaluate(result)

		metrics.RequestsTotal.WithLabelValues(string(pkg.Ecosystem), string(d.Action)).Inc()
		if evlog != nil {
			evlog.Record(eventlog.PackageEvent{
				Ecosystem: string(pkg.Ecosystem),
				Package:   pkg.Name + "@" + v,
				Action:    string(d.Action),
				Signal:    d.Signal,
				Reason:    d.Reason,
				Vulns:     d.Vulns,
			})
		}
		if d.Action == policy.ActionBlock {
			metrics.BlocksTotal.WithLabelValues(string(pkg.Ecosystem), d.Signal).Inc()
			if wh != nil {
				_ = wh.Send(pkg, d)
			}
			continue
		}
		allowed = append(allowed, v)
	}

	meta.Versioning.Versions.Version = allowed
	// Update latest/release. When all versions are blocked, clear both fields so Maven/Gradle
	// don't try to resolve a version that's no longer in the <versions> list.
	if len(allowed) == 0 {
		meta.Versioning.Latest = ""
		meta.Versioning.Release = ""
	} else {
		if !contains(allowed, meta.Versioning.Latest) {
			meta.Versioning.Latest = allowed[len(allowed)-1]
		}
		if !contains(allowed, meta.Versioning.Release) {
			meta.Versioning.Release = allowed[len(allowed)-1]
		}
	}

	out, err := xml.MarshalIndent(meta, "", "  ")
	if err != nil {
		return data
	}
	return append([]byte(xml.Header), out...)
}

// ParseMavenMetaPath extracts groupId and artifactId from a maven-metadata.xml path.
// Path: "com/example/mylib/maven-metadata.xml" → ("com.example", "mylib")
func ParseMavenMetaPath(path string) (groupID, artifactID string) {
	// Remove trailing "maven-metadata.xml"
	parts := strings.Split(strings.TrimSuffix(path, "/maven-metadata.xml"), "/")
	if len(parts) < 2 {
		return "", ""
	}
	artifactID = parts[len(parts)-1]
	groupID = strings.Join(parts[:len(parts)-1], ".")
	return groupID, artifactID
}

func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func timestampsToCache(ts map[string]time.Time) map[string]int64 {
	m := make(map[string]int64, len(ts))
	for v, t := range ts {
		m[v] = t.Unix()
	}
	return m
}

func parseTimestampCache(data []byte) map[string]time.Time {
	var m map[string]int64
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	result := make(map[string]time.Time, len(m))
	for v, unix := range m {
		result[v] = time.Unix(unix, 0)
	}
	return result
}
