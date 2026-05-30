package dashboard

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// AccessLogEntry is one parsed Apache-combined line.
type AccessLogEntry struct {
	Host      string    `json:"host"`
	Timestamp time.Time `json:"timestamp"`
	Method    string    `json:"method"`
	Path      string    `json:"path"`
	Proto     string    `json:"proto"`
	Status    int       `json:"status"`
	Bytes     int64     `json:"bytes"`
	UserAgent string    `json:"user_agent"`
}

func (d *Dashboard) handleAccessLog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if d.accessLogPath == "" {
		json.NewEncoder(w).Encode([]AccessLogEntry{})
		return
	}
	n := 200
	if s := r.URL.Query().Get("n"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 && v <= maxEventsPerRequest {
			n = v
		}
	}
	entries := parseAccessLog(d.accessLogPath, d.cfg.Path, n)
	json.NewEncoder(w).Encode(entries)
}

// parseAccessLog reads the file, parses each combined-format line, drops requests
// to dashPath (the dashboard's own traffic), and returns the newest n entries.
func parseAccessLog(path, dashPath string, n int) []AccessLogEntry {
	f, err := os.Open(path)
	if err != nil {
		return []AccessLogEntry{}
	}
	defer f.Close()

	var all []AccessLogEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		e, ok := parseCombinedLine(sc.Text())
		if !ok {
			continue
		}
		if dashPath != "" && strings.HasPrefix(e.Path, dashPath) {
			continue
		}
		all = append(all, e)
	}
	// newest-first
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	if len(all) > n {
		all = all[:n]
	}
	if all == nil {
		all = []AccessLogEntry{}
	}
	return all
}

// parseCombinedLine parses: host - - [ts] "METHOD path proto" status bytes "ref" "ua"
func parseCombinedLine(line string) (AccessLogEntry, bool) {
	var e AccessLogEntry
	host, rest, ok := strings.Cut(line, " ")
	if !ok {
		return e, false
	}
	e.Host = host

	lb := strings.IndexByte(rest, '[')
	rb := strings.IndexByte(rest, ']')
	if lb < 0 || rb < lb {
		return e, false
	}
	ts, err := time.Parse("02/Jan/2006:15:04:05 -0700", rest[lb+1:rb])
	if err != nil {
		return e, false
	}
	e.Timestamp = ts
	after := rest[rb+1:]

	q1 := strings.IndexByte(after, '"')
	if q1 < 0 {
		return e, false
	}
	q2 := strings.IndexByte(after[q1+1:], '"')
	if q2 < 0 {
		return e, false
	}
	reqLine := after[q1+1 : q1+1+q2]
	parts := strings.Split(reqLine, " ")
	if len(parts) >= 3 {
		e.Method, e.Path, e.Proto = parts[0], parts[1], parts[2]
	}
	tail := strings.Fields(after[q1+1+q2+1:])
	if len(tail) >= 1 {
		e.Status, _ = strconv.Atoi(tail[0])
	}
	if len(tail) >= 2 {
		e.Bytes, _ = strconv.ParseInt(tail[1], 10, 64)
	}
	// User-agent = last quoted field.
	if i := strings.LastIndexByte(line, '"'); i > 0 {
		if j := strings.LastIndexByte(line[:i], '"'); j >= 0 {
			e.UserAgent = line[j+1 : i]
		}
	}
	return e, true
}
