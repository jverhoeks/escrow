package eventlog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Dashboard audit actions (stored in the Action field of PackageEvent).
const (
	ActionAllowlistAdd    = "allowlist-add"
	ActionAllowlistRemove = "allowlist-remove"
	ActionBlocklistAdd    = "blocklist-add"
	ActionBlocklistRemove = "blocklist-remove"
)

type PackageEvent struct {
	Timestamp time.Time `json:"timestamp"`
	Ecosystem string    `json:"ecosystem"`
	Package   string    `json:"package"`
	Action    string    `json:"action"`
	Signal    string    `json:"signal"`
	Reason    string    `json:"reason"`
	Operator  string    `json:"operator,omitempty"` // set for dashboard audit actions
}

type Stats struct {
	Blocked    int        `json:"blocked"`
	Warned     int        `json:"warned"`
	Allowed    int        `json:"allowed"`
	TopBlocked []TopEntry `json:"top_blocked"`
}

type TopEntry struct {
	Package string `json:"package"`
	Count   int    `json:"count"`
}

const maxSubscribers = 100 // cap on concurrent SSE dashboard connections

type Log struct {
	mu          sync.RWMutex
	cap         int
	events      []PackageEvent
	subscribers map[int]chan PackageEvent
	nextID      int
	file        *os.File // append-only JSONL; nil = in-memory only
}

// New creates an in-memory event log with the given capacity.
func New(cap int) *Log {
	return &Log{cap: cap, subscribers: make(map[int]chan PackageEvent)}
}

// NewWithPath creates an event log that persists to a JSONL file.
// Existing events are loaded from the file on startup (up to cap).
// The file is opened for appending; new events are written as they arrive.
func NewWithPath(cap int, path string) (*Log, error) {
	l := &Log{cap: cap, subscribers: make(map[int]chan PackageEvent)}
	if path == "" {
		return l, nil
	}

	// Load existing events (newest last in file → reverse after load).
	if data, err := os.ReadFile(path); err == nil {
		scanner := bufio.NewScanner(strings.NewReader(string(data)))
		var loaded []PackageEvent
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var e PackageEvent
			if json.Unmarshal([]byte(line), &e) == nil {
				loaded = append(loaded, e)
			}
		}
		// Keep last `cap` events; reverse so slice is newest-first.
		if len(loaded) > cap {
			loaded = loaded[len(loaded)-cap:]
		}
		for i, j := 0, len(loaded)-1; i < j; i, j = i+1, j-1 {
			loaded[i], loaded[j] = loaded[j], loaded[i]
		}
		l.events = loaded
	}

	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create event log directory: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	l.file = f
	return l, nil
}

// Close flushes and closes the underlying file (if any). Safe to call multiple times.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil // prevent double-close and stale Write calls in Record
	return err
}

func (l *Log) Record(e PackageEvent) {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	l.mu.Lock()
	l.events = append([]PackageEvent{e}, l.events...)
	if len(l.events) > l.cap {
		l.events = l.events[:l.cap]
	}
	if l.file != nil {
		if data, err := json.Marshal(e); err == nil {
			l.file.Write(append(data, '\n')) // best-effort
		}
	}
	subs := make(map[int]chan PackageEvent, len(l.subscribers))
	for id, ch := range l.subscribers {
		subs[id] = ch
	}
	l.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- e:
		default:
		}
	}
}

func (l *Log) Events(eco string) []PackageEvent {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]PackageEvent, 0, len(l.events))
	for _, e := range l.events {
		if eco == "" || e.Ecosystem == eco {
			out = append(out, e)
		}
	}
	return out
}

// Subscribe registers a channel to receive new events as they are recorded.
// Returns nil, nil if the subscriber cap (maxSubscribers) is reached.
func (l *Log) Subscribe() (<-chan PackageEvent, func()) {
	ch := make(chan PackageEvent, 64)
	l.mu.Lock()
	if len(l.subscribers) >= maxSubscribers {
		l.mu.Unlock()
		return nil, nil
	}
	id := l.nextID
	l.nextID++
	l.subscribers[id] = ch
	l.mu.Unlock()
	return ch, func() {
		l.mu.Lock()
		delete(l.subscribers, id)
		l.mu.Unlock()
		// Do NOT close ch here. Record() snapshots the subscriber map under the
		// write lock but then sends without holding the lock. If unsub() closes ch
		// between the snapshot and the send, Record() panics ("send on closed channel").
		// The subscriber goroutine exits via r.Context().Done() instead.
	}
}

func (l *Log) Stats() Stats {
	l.mu.RLock()
	defer l.mu.RUnlock()
	s := Stats{}
	counts := map[string]int{}
	for _, e := range l.events {
		switch e.Action {
		case "block":
			s.Blocked++
			counts[packageName(e.Package)]++
		case "warn":
			s.Warned++
		case "allow":
			s.Allowed++
		}
	}
	type kv struct {
		k string
		v int
	}
	var sorted []kv
	for k, v := range counts {
		sorted = append(sorted, kv{k, v})
	}
	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].v > sorted[i].v {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	limit := 3
	if len(sorted) < limit {
		limit = len(sorted)
	}
	for _, kv := range sorted[:limit] {
		s.TopBlocked = append(s.TopBlocked, TopEntry{Package: kv.k, Count: kv.v})
	}
	return s
}

func packageName(pkg string) string {
	if i := strings.LastIndex(pkg, "@"); i > 0 {
		return pkg[:i]
	}
	return pkg
}
