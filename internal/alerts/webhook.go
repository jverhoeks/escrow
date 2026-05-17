package alerts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/trust"
)

type Webhook struct {
	url    string
	client *http.Client
}

func NewWebhook(url string, client *http.Client) *Webhook {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &Webhook{url: url, client: client}
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
	resp, err := w.client.Post(w.url, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webhook post failed: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	return nil
}
