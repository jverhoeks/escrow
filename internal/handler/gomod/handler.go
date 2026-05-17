package gomod

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
	modTTL  = 24 * time.Hour
	listTTL = 5 * time.Minute
)

type Handler struct {
	client      *http.Client
	upstreamURL string
	engine      *trust.Engine
	policy      *policy.Engine
	cache       cache.Cache
	evlog       *eventlog.Log
	webhook     *alerts.Webhook // may be nil
	sfInfo      singleflight.Group
}

func New(client *http.Client, upstreamURL string, engine *trust.Engine, pol *policy.Engine, c cache.Cache, evLog *eventlog.Log) *Handler {
	if upstreamURL == "" {
		upstreamURL = "https://proxy.golang.org"
	}
	return &Handler{client: client, upstreamURL: upstreamURL, engine: engine, policy: pol, cache: c, evlog: evLog}
}

func (h *Handler) WithWebhook(wh *alerts.Webhook) *Handler {
	h.webhook = wh
	return h
}

func (h *Handler) Mount(r chi.Router) {
	r.Route("/go", func(r chi.Router) {
		r.Get("/*", h.Serve)
	})
}

func (h *Handler) Serve(w http.ResponseWriter, r *http.Request) {
	urlPath := chi.URLParam(r, "*")

	if idx := strings.Index(urlPath, "/@v/"); idx >= 0 {
		h.serveVersioned(w, r, urlPath[:idx], urlPath[idx+4:])
		return
	}
	if idx := strings.Index(urlPath, "/@latest"); idx >= 0 {
		h.serveLatest(w, r, urlPath[:idx])
		return
	}
	http.NotFound(w, r)
}

func (h *Handler) serveVersioned(w http.ResponseWriter, r *http.Request, escapedModule, request string) {
	switch {
	case strings.HasSuffix(request, ".info"):
		h.serveInfo(w, r, escapedModule, request)
	case strings.HasSuffix(request, ".mod"):
		h.serveMod(w, r, escapedModule, request)
	case strings.HasSuffix(request, ".zip"):
		h.serveZip(w, r, escapedModule, request)
	default:
		// list and other pass-through with meta cache
		h.servePassthrough(w, r, escapedModule, request, listTTL)
	}
}

func (h *Handler) serveInfo(w http.ResponseWriter, r *http.Request, escapedModule, request string) {
	cacheKey := "go/info/" + escapedModule + "/@v/" + request
	if cached, _ := h.cache.GetMeta(r.Context(), cacheKey); cached != nil {
		metrics.CacheHitsTotal.WithLabelValues("go", "info").Inc()
		w.Header().Set("Content-Type", "application/json")
		w.Write(cached)
		return
	}

	sfKey := escapedModule + "/@v/" + request
	raw, err, _ := h.sfInfo.Do(sfKey, func() (any, error) {
		upURL := fmt.Sprintf("%s/%s/@v/%s", h.upstreamURL, escapedModule, request)
		t0 := time.Now()
		resp, err := h.client.Get(upURL)
		metrics.ProxyRequestDuration.WithLabelValues("go").Observe(time.Since(t0).Seconds())
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return &proxyResult{status: resp.StatusCode}, nil
		}
		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		var info struct {
			Version string    `json:"Version"`
			Time    time.Time `json:"Time"`
		}
		if err := json.Unmarshal(bodyBytes, &info); err != nil {
			return nil, err
		}
		pkg := trust.Package{
			Ecosystem:   trust.EcosystemGo,
			Name:        unescape(escapedModule),
			Version:     info.Version,
			PublishedAt: info.Time,
		}
		result, _ := h.engine.Check(context.Background(), pkg)
		d := h.policy.Evaluate(result)
		metrics.RequestsTotal.WithLabelValues(string(pkg.Ecosystem), string(d.Action)).Inc()
		if d.Action == policy.ActionBlock {
			metrics.BlocksTotal.WithLabelValues(string(pkg.Ecosystem), d.Signal).Inc()
		}
		if h.evlog != nil {
			h.evlog.Record(eventlog.PackageEvent{
				Ecosystem: string(pkg.Ecosystem),
				Package:   pkg.Name + "@" + info.Version,
				Action:    string(d.Action),
				Signal:    d.Signal,
				Reason:    d.Reason,
			})
		}
		if d.Action == policy.ActionBlock && h.webhook != nil {
			_ = h.webhook.Send(pkg, d)
		}
		if d.Action == policy.ActionBlock {
			blocked := fmt.Sprintf(`{"blocked":true,"signal":%q,"reason":%q}`, d.Signal, d.Reason)
			return &proxyResult{status: http.StatusForbidden, body: []byte(blocked)}, nil
		}
		// Cache the allowed .info response so subsequent requests don't hit upstream.
		h.cache.SetMeta(context.Background(), cacheKey, bodyBytes, modTTL)
		return &proxyResult{status: http.StatusOK, body: bodyBytes}, nil
	})
	if err != nil {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	res := raw.(*proxyResult)
	if res.status != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(res.status)
		w.Write(res.body)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(res.body)
}

type proxyResult struct {
	status int
	body   []byte
}

// serveMod proxies .mod files and caches them (they are immutable once published).
func (h *Handler) serveMod(w http.ResponseWriter, r *http.Request, escapedModule, request string) {
	cacheKey := "go/mod/" + escapedModule + "/@v/" + request
	if cached, _ := h.cache.GetMeta(r.Context(), cacheKey); cached != nil {
		metrics.CacheHitsTotal.WithLabelValues("go", "mod").Inc()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write(cached)
		return
	}
	upURL := fmt.Sprintf("%s/%s/@v/%s", h.upstreamURL, escapedModule, request)
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
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "upstream read error", http.StatusBadGateway)
		return
	}
	h.cache.SetMeta(r.Context(), cacheKey, body, modTTL)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(body)
}

// serveZip proxies .zip source archives and caches them as blobs.
func (h *Handler) serveZip(w http.ResponseWriter, r *http.Request, escapedModule, request string) {
	cacheKey := "go/zip/" + escapedModule + "/@v/" + request
	if blob, _ := h.cache.GetBlob(r.Context(), cacheKey); blob != nil {
		defer blob.Close()
		metrics.CacheHitsTotal.WithLabelValues("go", "zip").Inc()
		io.Copy(w, blob)
		return
	}
	upURL := fmt.Sprintf("%s/%s/@v/%s", h.upstreamURL, escapedModule, request)
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

// servePassthrough proxies non-versioned paths (list, etc.) with a short meta cache.
func (h *Handler) servePassthrough(w http.ResponseWriter, r *http.Request, escapedModule, request string, ttl time.Duration) {
	cacheKey := "go/pass/" + escapedModule + "/@v/" + request
	if cached, _ := h.cache.GetMeta(r.Context(), cacheKey); cached != nil {
		metrics.CacheHitsTotal.WithLabelValues("go", "pass").Inc()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write(cached)
		return
	}
	upURL := fmt.Sprintf("%s/%s/@v/%s", h.upstreamURL, escapedModule, request)
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
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "upstream read error", http.StatusBadGateway)
		return
	}
	h.cache.SetMeta(r.Context(), cacheKey, body, ttl)
	h.proxyHeaders(w, resp)
	w.Write(body)
}

func (h *Handler) serveLatest(w http.ResponseWriter, r *http.Request, escapedModule string) {
	sfKey := escapedModule + "/@latest"
	if cached, _ := h.cache.GetMeta(r.Context(), "go/latest/"+sfKey); cached != nil {
		metrics.CacheHitsTotal.WithLabelValues("go", "latest").Inc()
		w.Header().Set("Content-Type", "application/json")
		w.Write(cached)
		return
	}
	raw, err, _ := h.sfInfo.Do(sfKey, func() (any, error) {
		upURL := fmt.Sprintf("%s/%s/@latest", h.upstreamURL, escapedModule)
		resp, err := h.client.Get(upURL)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return &proxyResult{status: resp.StatusCode}, nil
		}
		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		var info struct {
			Version string    `json:"Version"`
			Time    time.Time `json:"Time"`
		}
		if err := json.Unmarshal(bodyBytes, &info); err != nil {
			return nil, err
		}
		pkg := trust.Package{
			Ecosystem:   trust.EcosystemGo,
			Name:        unescape(escapedModule),
			Version:     info.Version,
			PublishedAt: info.Time,
		}
		result, _ := h.engine.Check(context.Background(), pkg)
		d := h.policy.Evaluate(result)
		metrics.RequestsTotal.WithLabelValues(string(pkg.Ecosystem), string(d.Action)).Inc()
		if d.Action == policy.ActionBlock {
			metrics.BlocksTotal.WithLabelValues(string(pkg.Ecosystem), d.Signal).Inc()
			if h.evlog != nil {
				h.evlog.Record(eventlog.PackageEvent{
					Ecosystem: string(pkg.Ecosystem),
					Package:   pkg.Name + "@" + info.Version,
					Action:    string(d.Action),
					Signal:    d.Signal,
					Reason:    d.Reason,
				})
			}
			if h.webhook != nil {
				_ = h.webhook.Send(pkg, d)
			}
			blocked := fmt.Sprintf(`{"blocked":true,"signal":%q,"reason":%q}`, d.Signal, d.Reason)
			return &proxyResult{status: http.StatusForbidden, body: []byte(blocked)}, nil
		}
		if h.evlog != nil {
			h.evlog.Record(eventlog.PackageEvent{
				Ecosystem: string(pkg.Ecosystem),
				Package:   pkg.Name + "@" + info.Version,
				Action:    string(d.Action),
				Signal:    d.Signal,
				Reason:    d.Reason,
			})
		}
		// Cache the allowed @latest response with a short TTL.
		h.cache.SetMeta(context.Background(), "go/latest/"+sfKey, bodyBytes, listTTL)
		return &proxyResult{status: http.StatusOK, body: bodyBytes}, nil
	})
	if err != nil {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	res := raw.(*proxyResult)
	if res.status != http.StatusOK {
		// Use explicit JSON Content-Type for blocked/error JSON bodies, not http.Error's text/plain.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(res.status)
		w.Write(res.body)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(res.body)
}

func (h *Handler) proxyHeaders(w http.ResponseWriter, resp *http.Response) {
	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
}

// unescape converts GOPROXY-escaped module path to real form: !x → X (uppercase).
func unescape(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '!' && i+1 < len(s) && s[i+1] >= 'a' && s[i+1] <= 'z' {
			b.WriteByte(s[i+1] - 32)
			i += 2
		} else {
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}
