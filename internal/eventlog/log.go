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

	"github.com/jverhoeks/escrow/internal/logfile"
	"github.com/jverhoeks/escrow/internal/trust"
)

// Dashboard audit actions (stored in the Action field of PackageEvent).
const (
	ActionAllowlistAdd    = "allowlist-add"
	ActionAllowlistRemove = "allowlist-remove"
	ActionBlocklistAdd    = "blocklist-add"
	ActionBlocklistRemove = "blocklist-remove"
)

// Event kinds (stored in the Kind field of PackageEvent). They distinguish the
// noisy per-version policy evaluation done while listing/filtering a package
// index (scanned) from a real artifact fetch served to a client (downloaded).
const (
	KindScanned    = "scanned"
	KindDownloaded = "downloaded"
	KindRescan     = "rescan"
)

type PackageEvent struct {
	Timestamp time.Time    `json:"timestamp"`
	Ecosystem string       `json:"ecosystem"`
	Package   string       `json:"package"`
	Action    string       `json:"action"`
	Signal    string       `json:"signal"`
	Reason    string       `json:"reason"`
	Kind      string       `json:"kind,omitempty"` // "scanned" (index listing) or "downloaded" (artifact fetch)
	Operator  string       `json:"operator,omitempty"` // set for dashboard audit actions
	Vulns     []trust.Vuln `json:"vulns,omitempty"`
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
	path        string   // retained for size-cap compaction
	curBytes    int64    // bytes written to file since last compaction
	// INVARIANT: cap × avg-event-size must stay comfortably below maxBytes.
	// Otherwise post-compaction curBytes (the retained-window size) stays above
	// maxBytes and the next Record re-triggers a full marshal+fsync+rename under
	// the write lock on every event (a stall). At cap=5000 / maxBytes=8 MiB that
	// leaves ~1.6 KB/event; PackageEvent (incl. its Vulns list) stays well under
	// this in practice. Re-check before lowering maxBytes or raising cap.
	maxBytes int64 // compact when curBytes exceeds this (0 = never)
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
	l.path = path
	l.maxBytes = logfile.DefaultMaxBytes
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
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	l.file = f
	if fi, serr := f.Stat(); serr == nil {
		l.curBytes = fi.Size() // Stat failure → 0: compaction triggers later, file stays consistent
	}
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
			if n, werr := l.file.Write(append(data, '\n')); werr == nil {
				l.curBytes += int64(n)
				// compactLocked holds the write lock across fsync+rename; rare
				// (every ~DefaultMaxBytes ≈ 8 MiB) so the stall is acceptable.
				if l.maxBytes > 0 && l.curBytes > l.maxBytes {
					l.compactLocked()
				}
			}
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

// compactLocked rewrites the file to hold only the in-memory capped events,
// oldest-first, bounding on-disk growth. The caller MUST hold l.mu. l.events is
// newest-first and NewWithPath reverses on load, so emit in REVERSE. On error,
// file persistence is disabled until restart; the in-memory log is unaffected.
func (l *Log) compactLocked() {
	lines := make([][]byte, 0, len(l.events))
	for i := len(l.events) - 1; i >= 0; i-- {
		if b, err := json.Marshal(l.events[i]); err == nil {
			lines = append(lines, b)
		}
	}
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
	n, err := logfile.AtomicRewrite(l.path, lines)
	if err != nil {
		return
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	l.file = f
	l.curBytes = n
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

// Stats returns aggregate counts for events within the given window.
// Pass window=0 for all-time totals.
func (l *Log) Stats(window time.Duration) Stats {
	cutoff := time.Time{}
	if window > 0 {
		cutoff = time.Now().UTC().Add(-window)
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	s := Stats{}
	counts := map[string]int{}
	for _, e := range l.events {
		if window > 0 && !e.Timestamp.After(cutoff) {
			continue
		}
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
