package eventlog

import (
	"strings"
	"sync"
	"time"
)

type PackageEvent struct {
	Timestamp time.Time `json:"timestamp"`
	Ecosystem string    `json:"ecosystem"`
	Package   string    `json:"package"`
	Action    string    `json:"action"`
	Signal    string    `json:"signal"`
	Reason    string    `json:"reason"`
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

type Log struct {
	mu          sync.RWMutex
	cap         int
	events      []PackageEvent
	subscribers map[int]chan PackageEvent
	nextID      int
}

func New(cap int) *Log {
	return &Log{cap: cap, subscribers: make(map[int]chan PackageEvent)}
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

func (l *Log) Subscribe() (<-chan PackageEvent, func()) {
	ch := make(chan PackageEvent, 64)
	l.mu.Lock()
	id := l.nextID
	l.nextID++
	l.subscribers[id] = ch
	l.mu.Unlock()
	return ch, func() {
		l.mu.Lock()
		delete(l.subscribers, id)
		l.mu.Unlock()
		close(ch)
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
