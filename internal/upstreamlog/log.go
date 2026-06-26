// Package upstreamlog keeps a bounded, in-memory record of escrow→upstream
// fetches. Every entry represents a real upstream call (a cache miss); cache
// hits never reach the upstream transport and so never appear here.
package upstreamlog

import (
	"sync"
	"time"

	"github.com/jverhoeks/escrow/internal/ringbuf"
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
	events *ringbuf.Buf[Event]
}

// New returns an upstream log holding at most cap events.
func New(cap int) *Log {
	return &Log{events: ringbuf.New[Event](cap)}
}

// Record appends an event (O(1)), evicting the oldest at capacity. Timestamp
// defaults to now.
func (l *Log) Record(e Event) {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	l.mu.Lock()
	l.events.Push(e)
	l.mu.Unlock()
}

// Events returns a newest-first copy, optionally filtered by ecosystem.
func (l *Log) Events(eco string) []Event {
	l.mu.RLock()
	defer l.mu.RUnlock()
	all := l.events.Newest()
	if eco == "" {
		return all
	}
	out := make([]Event, 0, len(all))
	for _, e := range all {
		if e.Ecosystem == eco {
			out = append(out, e)
		}
	}
	return out
}
