package dashboard

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"
)

// CVEEntry is one (vulnerability, affected package version) pairing.
type CVEEntry struct {
	ID        string    `json:"id"`
	Severity  string    `json:"severity"`
	Ecosystem string    `json:"ecosystem"`
	Package   string    `json:"package"`
	Version   string    `json:"version"`
	Action    string    `json:"action"`
	Reason    string    `json:"reason"`
	LastSeen  time.Time `json:"last_seen"`
}

// handleCVEs returns one entry per (vuln ID × package version), newest-first,
// deduplicated so the most recent sighting of each pairing wins.
func (d *Dashboard) handleCVEs(w http.ResponseWriter, r *http.Request) {
	eco := r.URL.Query().Get("eco")
	type key struct{ id, eco, name, version string }
	seen := map[key]CVEEntry{}

	for _, e := range d.log.Events(eco) { // newest-first
		if len(e.Vulns) == 0 {
			continue
		}
		name, version := splitPackage(e.Package)
		for _, v := range e.Vulns {
			k := key{v.ID, e.Ecosystem, name, version}
			if _, ok := seen[k]; ok {
				continue // newest already recorded
			}
			seen[k] = CVEEntry{
				ID: v.ID, Severity: v.Severity, Ecosystem: e.Ecosystem,
				Package: name, Version: version, Action: e.Action,
				Reason: e.Reason, LastSeen: e.Timestamp,
			}
		}
	}

	out := make([]CVEEntry, 0, len(seen))
	for _, v := range seen {
		out = append(out, v)
	}
	sev := map[string]int{"CRITICAL": 4, "HIGH": 3, "MEDIUM": 2, "LOW": 1}
	sort.Slice(out, func(i, j int) bool {
		if sev[out[i].Severity] != sev[out[j].Severity] {
			return sev[out[i].Severity] > sev[out[j].Severity]
		}
		return out[i].LastSeen.After(out[j].LastSeen)
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}
