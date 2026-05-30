package dashboard

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/jverhoeks/escrow/internal/upstreamlog"
)

func (d *Dashboard) handleUpstreamLog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if d.upstreamLog == nil {
		json.NewEncoder(w).Encode([]upstreamlog.Event{})
		return
	}
	events := d.upstreamLog.Events(r.URL.Query().Get("eco"))
	if s := r.URL.Query().Get("n"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 && v < len(events) {
			events = events[:v]
		}
	}
	json.NewEncoder(w).Encode(events)
}
