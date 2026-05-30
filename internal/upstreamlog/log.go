// Package upstreamlog keeps a bounded, in-memory record of escrow→upstream
// fetches. Every entry represents a real upstream call (a cache miss); cache
// hits never reach the upstream transport and so never appear here.
package upstreamlog

import (
	"sync"
	"time"
)

// Event is a single escrow→upstream fetch.
type Event struct {
	Timestamp time.Time `json:"timestamp"`
	Ecosystem string    `json:"ecosystem"`
	Method    string    `json:"method"`
	URL       string    `json:"url"`
	Status    int       `json:"status"`
	Bytes     int64     `json:"bytes"`
	MS        float64   `json:"ms"`
}

// Log is a fixed-capacity, newest-first ring of upstream fetch events.
type Log struct {
	mu     sync.RWMutex
	cap    int
	events []Event
}

// New returns an upstream log holding at most cap events.
func New(cap int) *Log {
	if cap <= 0 {
		cap = 1
	}
	return &Log{cap: cap}
}

// Record prepends an event, trimming to capacity. Timestamp defaults to now.
func (l *Log) Record(e Event) {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	l.mu.Lock()
	l.events = append([]Event{e}, l.events...)
	if len(l.events) > l.cap {
		l.events = l.events[:l.cap]
	}
	l.mu.Unlock()
}

// Events returns a newest-first copy, optionally filtered by ecosystem.
func (l *Log) Events(eco string) []Event {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Event, 0, len(l.events))
	for _, e := range l.events {
		if eco == "" || e.Ecosystem == eco {
			out = append(out, e)
		}
	}
	return out
}
