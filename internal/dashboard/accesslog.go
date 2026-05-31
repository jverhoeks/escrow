package dashboard

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/jverhoeks/escrow/internal/accesslog"
)

// handleAccessLog returns the newest in-memory access log entries, filtering out
// the dashboard's own traffic. The ring is always populated by the server, so
// this works with no file logging configured.
func (d *Dashboard) handleAccessLog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if d.accessRing == nil {
		json.NewEncoder(w).Encode([]accesslog.Entry{})
		return
	}
	n := 200
	if s := r.URL.Query().Get("n"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 && v <= maxEventsPerRequest {
			n = v
		}
	}
	recent := d.accessRing.Recent(n)
	out := make([]accesslog.Entry, 0, len(recent))
	for _, e := range recent {
		if d.cfg.Path != "" && strings.HasPrefix(e.Path, d.cfg.Path) {
			continue
		}
		out = append(out, e)
	}
	json.NewEncoder(w).Encode(out)
}
