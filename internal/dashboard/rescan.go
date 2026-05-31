package dashboard

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/jverhoeks/escrow/internal/eventlog"
)

// handleRescanStatus reports whether the scanner is configured and its last run.
func (d *Dashboard) handleRescanStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if d.scanner == nil {
		json.NewEncoder(w).Encode(map[string]any{"enabled": false})
		return
	}
	res := d.scanner.LastRun()
	json.NewEncoder(w).Encode(map[string]any{
		"enabled": true, "last_run": res.At,
		"scanned": res.Scanned, "new_findings": res.NewFindings, "blocked": res.Blocked, "errors": res.Errors,
	})
}

// handleRescanTrigger runs a sweep on demand.
func (d *Dashboard) handleRescanTrigger(w http.ResponseWriter, r *http.Request) {
	if !d.originOK(r) {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if d.scanner == nil {
		http.Error(w, `{"error":"rescanner not configured"}`, http.StatusServiceUnavailable)
		return
	}
	res := d.scanner.RunOnce(r.Context())
	json.NewEncoder(w).Encode(map[string]any{
		"scanned": res.Scanned, "new_findings": res.NewFindings, "blocked": res.Blocked, "errors": res.Errors,
	})
}

// NewlyVulnerableEntry is one retroactive finding for the dashboard.
type NewlyVulnerableEntry struct {
	Ecosystem     string    `json:"ecosystem"`
	Package       string    `json:"package"`
	Version       string    `json:"version"`
	Vulns         []string  `json:"vulns"`
	Reason        string    `json:"reason"`
	DetectedAt    time.Time `json:"detected_at"`
	DownloadCount int       `json:"download_count"`
}

// handleNewlyVulnerable lists kind=rescan findings, newest-first, deduped per version.
func (d *Dashboard) handleNewlyVulnerable(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	type key struct{ eco, name, version string }
	seen := map[key]bool{}
	out := []NewlyVulnerableEntry{}
	for _, e := range d.log.Events("") { // newest-first
		if e.Kind != eventlog.KindRescan {
			continue
		}
		name, version := splitPackage(e.Package)
		k := key{e.Ecosystem, name, version}
		if seen[k] {
			continue
		}
		seen[k] = true
		ids := make([]string, 0, len(e.Vulns))
		for _, v := range e.Vulns {
			ids = append(ids, v.ID)
		}
		count, _ := d.dlStat(e.Ecosystem, name, version)
		out = append(out, NewlyVulnerableEntry{
			Ecosystem: e.Ecosystem, Package: name, Version: version, Vulns: ids,
			Reason: e.Reason, DetectedAt: e.Timestamp, DownloadCount: count,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DetectedAt.After(out[j].DetectedAt) })
	json.NewEncoder(w).Encode(out)
}
