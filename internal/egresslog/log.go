// Package egresslog is a dedicated, newest-first log of egress-proxy decisions
// (separate from the package event log and the client access log). It mirrors
// internal/eventlog: a bounded ring, optional JSONL persistence, and SSE
// subscriber fan-out. Bytes are tracked as an aggregate (AddBytes), because the
// allow event is recorded at connection open, before bytes are known.
package egresslog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/jverhoeks/escrow/internal/logfile"
)

const maxSubscribers = 100

type Event struct {
	Timestamp time.Time `json:"timestamp"`
	Host      string    `json:"host"`
	IP        string    `json:"ip,omitempty"`
	Verb      string    `json:"verb"`   // CONNECT | GET | POST | DIAL
	Action    string    `json:"action"` // allow | block
	Reason    string    `json:"reason"`
}

type HostCount struct {
	Host  string `json:"host"`
	Count int    `json:"count"`
}

type Bucket struct {
	T     time.Time `json:"t"`
	Allow int       `json:"allow"`
	Block int       `json:"block"`
}

type Stats struct {
	Total         int         `json:"total"`
	Allowed       int         `json:"allowed"`
	Blocked       int         `json:"blocked"`
	DistinctHosts int         `json:"distinct_hosts"`
	Bytes         int64       `json:"bytes"`
	TopAllowed    []HostCount `json:"top_allowed"`
	TopBlocked    []HostCount `json:"top_blocked"`
	Series        []Bucket    `json:"series"`
}

type Log struct {
	mu          sync.RWMutex
	cap         int
	events      []Event // newest-first
	bytes       int64   // aggregate bytes proxied (allow path)
	subscribers map[int]chan Event
	nextID      int
	file        *os.File
	path        string // retained for size-cap compaction
	curBytes    int64  // bytes written to file since last compaction
	maxBytes    int64  // compact when curBytes exceeds this (0 = never)
}

func New(cap int) *Log {
	if cap <= 0 {
		cap = 5000
	}
	return &Log{cap: cap, subscribers: map[int]chan Event{}}
}

func NewWithPath(cap int, path string) (*Log, error) {
	l := New(cap)
	l.path = path
	l.maxBytes = logfile.DefaultMaxBytes
	if data, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(data)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		var loaded []Event
		for sc.Scan() {
			var e Event
			if json.Unmarshal(sc.Bytes(), &e) == nil {
				loaded = append(loaded, e)
			}
		}
		data.Close()
		if len(loaded) > l.cap {
			loaded = loaded[len(loaded)-l.cap:]
		}
		for i := len(loaded) - 1; i >= 0; i-- {
			l.events = append(l.events, loaded[i])
		}
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create egress log directory: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	l.file = f
	// A failed Stat leaves curBytes at 0, which only makes compaction trigger
	// LATER (after maxBytes more writes); the file stays consistent — the safe
	// direction.
	if fi, serr := f.Stat(); serr == nil {
		l.curBytes = fi.Size()
	}
	return l, nil
}

func (l *Log) Record(e Event) {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	l.mu.Lock()
	l.events = append([]Event{e}, l.events...)
	if len(l.events) > l.cap {
		l.events = l.events[:l.cap]
	}
	if l.file != nil {
		if b, err := json.Marshal(e); err == nil {
			if n, werr := l.file.Write(append(b, '\n')); werr == nil {
				l.curBytes += int64(n)
				if l.maxBytes > 0 && l.curBytes > l.maxBytes {
					// compactLocked holds the write lock across the fsync+rename
					// inside logfile.AtomicRewrite. Acceptable because it's rare —
					// only every ~maxBytes (DefaultMaxBytes ≈ 8 MiB) of writes. Do
					// NOT silently lower the cap or this lock-hold becomes hot.
					l.compactLocked() // guarded: sets file=nil on failure, no retry storm
				}
			}
		}
	}
	subs := make([]chan Event, 0, len(l.subscribers))
	for _, ch := range l.subscribers {
		subs = append(subs, ch)
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
// oldest-first, bounding on-disk growth. The caller MUST hold l.mu. The file is
// oldest-first (append order) and NewWithPath reverses it back to newest-first
// on load, so we must emit l.events (newest-first) in REVERSE. On any error this
// disables file persistence (l.file = nil) until restart; the in-memory log is
// unaffected (these leaf packages carry no logger and Record returns no error).
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

// AddBytes adds n to the aggregate byte counter (called at connection close).
func (l *Log) AddBytes(n int64) {
	l.mu.Lock()
	l.bytes += n
	l.mu.Unlock()
}

// Close flushes and closes the underlying file (if any). Safe to call multiple
// times. Mirrors eventlog.Log.Close (parity).
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

func (l *Log) Recent(n int, action string) []Event {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Event, 0, len(l.events))
	for _, e := range l.events {
		if action != "" && e.Action != action {
			continue
		}
		out = append(out, e)
		if n > 0 && len(out) >= n {
			break
		}
	}
	return out
}

func (l *Log) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 64)
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
	}
}

func (l *Log) Stats(window, bucket time.Duration) Stats {
	l.mu.RLock()
	events := make([]Event, len(l.events))
	copy(events, l.events)
	bytesTotal := l.bytes
	l.mu.RUnlock()

	if bucket <= 0 {
		bucket = time.Hour
	}
	cutoff := time.Now().Add(-window)
	var s Stats
	s.Bytes = bytesTotal
	hosts := map[string]bool{}
	allowByHost := map[string]int{}
	blockByHost := map[string]int{}
	buckets := map[time.Time]*Bucket{}
	for _, e := range events {
		if e.Timestamp.Before(cutoff) {
			continue
		}
		s.Total++
		hosts[e.Host] = true
		bt := e.Timestamp.Truncate(bucket)
		b := buckets[bt]
		if b == nil {
			b = &Bucket{T: bt}
			buckets[bt] = b
		}
		if e.Action == "block" {
			s.Blocked++
			blockByHost[e.Host]++
			b.Block++
		} else {
			s.Allowed++
			allowByHost[e.Host]++
			b.Allow++
		}
	}
	s.DistinctHosts = len(hosts)
	s.TopAllowed = topHosts(allowByHost, 10)
	s.TopBlocked = topHosts(blockByHost, 10)
	for _, b := range buckets {
		s.Series = append(s.Series, *b)
	}
	sort.Slice(s.Series, func(i, j int) bool { return s.Series[i].T.Before(s.Series[j].T) })
	return s
}

func topHosts(m map[string]int, n int) []HostCount {
	out := make([]HostCount, 0, len(m))
	for h, c := range m {
		out = append(out, HostCount{Host: h, Count: c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Host < out[j].Host
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}
