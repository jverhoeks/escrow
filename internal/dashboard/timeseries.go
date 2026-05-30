package dashboard

import (
	"encoding/json"
	"net/http"
	"time"
)

// handleTimeseries returns per-action, per-ecosystem hourly counts over a window.
// Response shape:
//
//	{ "buckets": ["2026-05-30T00:00:00Z", ...],
//	  "series": { "allowed": {"npm":[...]}, "denied": {...}, "warned": {...} } }
//
// "denied" == block events; "allowed" == allow; "warned" == warn.
func (d *Dashboard) handleTimeseries(w http.ResponseWriter, r *http.Request) {
	window := 24 * time.Hour
	if r.URL.Query().Get("window") == "1h" {
		window = time.Hour
	}
	bucket := time.Hour // only hourly buckets are supported

	now := time.Now().UTC().Truncate(bucket)
	start := now.Add(-window).Add(bucket) // inclusive of the current bucket
	n := int(window / bucket)
	if n < 1 {
		n = 1
	}

	buckets := make([]string, n)
	for i := 0; i < n; i++ {
		buckets[i] = start.Add(time.Duration(i) * bucket).Format(time.RFC3339)
	}
	idxFor := func(ts time.Time) int {
		off := ts.UTC().Truncate(bucket).Sub(start) / bucket
		return int(off)
	}

	actionKey := map[string]string{"allow": "allowed", "block": "denied", "warn": "warned"}
	series := map[string]map[string][]int{
		"allowed": {}, "denied": {}, "warned": {},
	}
	ensure := func(action, eco string) []int {
		m := series[action]
		if m[eco] == nil {
			m[eco] = make([]int, n)
		}
		return m[eco]
	}

	for _, e := range d.log.Events("") {
		key, ok := actionKey[e.Action]
		if !ok {
			continue
		}
		i := idxFor(e.Timestamp)
		if i < 0 || i >= n {
			continue
		}
		arr := ensure(key, e.Ecosystem)
		arr[i]++
		series[key][e.Ecosystem] = arr
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"buckets": buckets, "series": series})
}
