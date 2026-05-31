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
	// Filter the dashboard's own traffic across the WHOLE ring first, THEN take
	// the newest n. Limiting before filtering would let frequent dashboard polls
	// crowd the newest-n window and starve the view of real entries over time.
	all := d.accessRing.Recent(0) // newest-first, whole ring
	out := make([]accesslog.Entry, 0, n)
	for _, e := range all {
		if d.cfg.Path != "" && strings.HasPrefix(e.Path, d.cfg.Path) {
			continue
		}
		out = append(out, e)
		if len(out) >= n {
			break
		}
	}
	json.NewEncoder(w).Encode(out)
}
