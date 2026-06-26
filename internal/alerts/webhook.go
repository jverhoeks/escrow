package alerts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/trust"
)

type Webhook struct {
	mu     sync.RWMutex
	url    string
	client *http.Client
}

func NewWebhook(url string, client *http.Client) *Webhook {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &Webhook{url: url, client: client}
}

// SetURL swaps the webhook target atomically (live reload).
func (w *Webhook) SetURL(url string) {
	w.mu.Lock()
	w.url = url
	w.mu.Unlock()
}

func (w *Webhook) target() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.url
}

type webhookPayload struct {
	Package   string `json:"package"`
	Version   string `json:"version"`
	Ecosystem string `json:"ecosystem"`
	Action    string `json:"action"`
	Signal    string `json:"signal"`
	Reason    string `json:"reason"`
	Timestamp string `json:"timestamp"`
}

// Send POSTs a JSON alert. It is a no-op for non-block decisions.
func (w *Webhook) Send(pkg trust.Package, d policy.Decision) error {
	if d.Action != policy.ActionBlock {
		return nil
	}
	payload := webhookPayload{
		Package:   pkg.Name,
		Version:   pkg.Version,
		Ecosystem: string(pkg.Ecosystem),
		Action:    string(d.Action),
		Signal:    d.Signal,
		Reason:    d.Reason,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	body, _ := json.Marshal(payload)
	resp, err := w.client.Post(w.target(), "application/json", bytes.NewReader(body))
	if err != nil {
		// Every call site fires this fire-and-forget; log here so a failing
		// alert channel isn't silent (operators rely on block alerts). See #74.
		log.Warn().Err(err).Str("package", pkg.Name+"@"+pkg.Version).Msg("block-alert webhook delivery failed")
		return fmt.Errorf("webhook post failed: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.Warn().Int("status", resp.StatusCode).Str("package", pkg.Name+"@"+pkg.Version).Msg("block-alert webhook returned error status")
		return fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	return nil
}

type rescanPayload struct {
	Type          string   `json:"type"` // always "retroactive-cve"
	Ecosystem     string   `json:"ecosystem"`
	Name          string   `json:"name"`
	Version       string   `json:"version"`
	Vulns         []string `json:"vulns"`
	Severity      string   `json:"severity"`
	Blocked       bool     `json:"blocked"`
	DownloadCount int      `json:"download_count"`
	Timestamp     string   `json:"timestamp"`
}

// SendRescan POSTs an alert about a newly-discovered vulnerability on a package
// version that was previously allowed/used.
func (w *Webhook) SendRescan(eco, name, version string, vulns []string, severity string, blocked bool, downloadCount int) error {
	payload := rescanPayload{
		Type: "retroactive-cve", Ecosystem: eco, Name: name, Version: version,
		Vulns: vulns, Severity: severity, Blocked: blocked, DownloadCount: downloadCount,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	body, _ := json.Marshal(payload)
	resp, err := w.client.Post(w.target(), "application/json", bytes.NewReader(body))
	if err != nil {
		log.Warn().Err(err).Str("package", name+"@"+version).Msg("rescan-alert webhook delivery failed")
		return fmt.Errorf("rescan webhook post failed: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.Warn().Int("status", resp.StatusCode).Str("package", name+"@"+version).Msg("rescan-alert webhook returned error status")
		return fmt.Errorf("rescan webhook returned %d", resp.StatusCode)
	}
	return nil
}
