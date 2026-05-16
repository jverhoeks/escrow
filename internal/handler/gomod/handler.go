package gomod

import (
	"encoding/json"
	"fmt"
	"io"
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

type Handler struct {
	client      *http.Client
	upstreamURL string
	engine      *trust.Engine
	policy      *policy.Engine
	cache       cache.Cache
	evlog       *eventlog.Log
	webhook     *alerts.Webhook // may be nil
}

func New(client *http.Client, upstreamURL string, engine *trust.Engine, pol *policy.Engine, c cache.Cache, evLog *eventlog.Log) *Handler {
	if upstreamURL == "" {
		upstreamURL = "https://proxy.golang.org"
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

	if !strings.HasSuffix(request, ".info") {
		h.proxyResponse(w, resp)
		return
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "upstream read error", http.StatusBadGateway)
		return
	}
	var info struct {
		Version string    `json:"Version"`
		Time    time.Time `json:"Time"`
	}
	if err := json.Unmarshal(bodyBytes, &info); err != nil {
		http.Error(w, "upstream decode error", http.StatusBadGateway)
		return
	}

	if h.checkTrust(r, w, unescape(escapedModule), info.Version, info.Time) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(bodyBytes)
}

func (h *Handler) serveLatest(w http.ResponseWriter, r *http.Request, escapedModule string) {
	upURL := fmt.Sprintf("%s/%s/@latest", h.upstreamURL, escapedModule)
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

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "upstream read error", http.StatusBadGateway)
		return
	}
	var info struct {
		Version string    `json:"Version"`
		Time    time.Time `json:"Time"`
	}
	if err := json.Unmarshal(bodyBytes, &info); err != nil {
		http.Error(w, "upstream decode error", http.StatusBadGateway)
		return
	}

	if h.checkTrust(r, w, unescape(escapedModule), info.Version, info.Time) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(bodyBytes)
}

// checkTrust runs trust signals and records the event. Returns true if blocked (response already written).
func (h *Handler) checkTrust(r *http.Request, w http.ResponseWriter, modulePath, version string, publishedAt time.Time) bool {
	pkg := trust.Package{
		Ecosystem:   trust.EcosystemGo,
		Name:        modulePath,
		Version:     version,
		PublishedAt: publishedAt,
	}
	result, _ := h.engine.Check(r.Context(), pkg)
	d := h.policy.Evaluate(result)

	metrics.RequestsTotal.WithLabelValues(string(pkg.Ecosystem), string(d.Action)).Inc()
	if d.Action == policy.ActionBlock {
		metrics.BlocksTotal.WithLabelValues(string(pkg.Ecosystem), d.Signal).Inc()
	}
	if h.evlog != nil {
		h.evlog.Record(eventlog.PackageEvent{
			Ecosystem: string(pkg.Ecosystem),
			Package:   modulePath + "@" + version,
			Action:    string(d.Action),
			Signal:    d.Signal,
			Reason:    d.Reason,
		})
	}

	if d.Action == policy.ActionBlock && h.webhook != nil {
		_ = h.webhook.Send(pkg, d)
	}
	if d.Action == policy.ActionBlock {
		http.Error(w, fmt.Sprintf(`{"blocked":true,"signal":%q,"reason":%q}`, d.Signal, d.Reason), http.StatusForbidden)
		return true
	}
	return false
}

func (h *Handler) proxyResponse(w http.ResponseWriter, resp *http.Response) {
	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
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
