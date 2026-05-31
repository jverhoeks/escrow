// Package accesslog keeps a bounded, in-memory record of HTTP requests handled
// by the escrow server. Every request is recorded here regardless of whether
// the optional Apache-combined file log is configured, so the dashboard's
// Access Logs view always has data to show.
package accesslog

import (
	"sync"
	"time"
)

// Entry is a single HTTP request. The JSON tags match the shape the dashboard
// frontend expects.
type Entry struct {
	Timestamp time.Time `json:"timestamp"`
	Host      string    `json:"host"`
	Method    string    `json:"method"`
	Path      string    `json:"path"`
	Proto     string    `json:"proto"`
	Status    int       `json:"status"`
	Bytes     int64     `json:"bytes"`
	UserAgent string    `json:"user_agent"`
}

// Log is a fixed-capacity, newest-first ring of access log entries.
type Log struct {
	mu      sync.RWMutex
	cap     int
	entries []Entry
}

// New returns an access log holding at most cap entries.
func New(cap int) *Log {
	if cap <= 0 {
		cap = 1
	}
	return &Log{cap: cap}
}

// Record prepends an entry, trimming to capacity. Timestamp defaults to now.
func (l *Log) Record(e Entry) {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	l.mu.Lock()
	l.entries = append([]Entry{e}, l.entries...)
	if len(l.entries) > l.cap {
		l.entries = l.entries[:l.cap]
	}
	l.mu.Unlock()
}

// Recent returns a newest-first copy of up to n entries. If n <= 0 or n exceeds
// the number of stored entries, all entries are returned.
func (l *Log) Recent(n int) []Entry {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if n <= 0 || n > len(l.entries) {
		n = len(l.entries)
	}
	out := make([]Entry, n)
	copy(out, l.entries[:n])
	return out
}
