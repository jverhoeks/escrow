package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jverhoeks/escrow/internal/egresslog"
)

func (d *Dashboard) handleEgressLog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if d.egressLog == nil {
		_ = json.NewEncoder(w).Encode([]egresslog.Event{})
		return
	}
	n := 500
	if s := r.URL.Query().Get("n"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 && v <= 5000 {
			n = v
		}
	}
	_ = json.NewEncoder(w).Encode(d.egressLog.Recent(n, r.URL.Query().Get("action")))
}

func (d *Dashboard) handleEgressTimeseries(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if d.egressLog == nil {
		_ = json.NewEncoder(w).Encode(egresslog.Stats{})
		return
	}
	window := 24 * time.Hour
	if v, err := time.ParseDuration(r.URL.Query().Get("window")); err == nil && v > 0 {
		window = v
	}
	bucket := time.Hour
	if v, err := time.ParseDuration(r.URL.Query().Get("bucket")); err == nil && v > 0 {
		bucket = v
	}
	_ = json.NewEncoder(w).Encode(d.egressLog.Stats(window, bucket))
}

func (d *Dashboard) handleEgressStream(w http.ResponseWriter, r *http.Request) {
	if d.egressLog == nil {
		http.Error(w, "egress log not configured", http.StatusServiceUnavailable)
		return
	}

	// Subscribe BEFORE writing any response bytes — once headers are flushed
	// http.Error is a no-op and the client sees garbled SSE instead of a 503.
	ch, unsub := d.egressLog.Subscribe()
	if ch == nil {
		http.Error(w, "too many live streams", http.StatusServiceUnavailable)
		return
	}
	defer unsub()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	// Disable the server WriteTimeout for SSE connections — it would kill the stream
	// after write_timeout_seconds (default 120s), silently disconnecting the dashboard.
	// ReadHeaderTimeout still guards against Slowloris on the initial request.
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		d.logger.Warn().Err(err).Msg("could not disable SSE write deadline; egress streams may disconnect after write_timeout_seconds")
	}
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		case e := <-ch:
			data, _ := json.Marshal(e)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}
