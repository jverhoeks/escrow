// Package dlstats keeps a persistent, per-version count of how many times each
// package artifact has been downloaded through escrow, plus first/last timestamps.
// It is intentionally independent of the bounded event-log ring so counts survive
// event eviction.
package dlstats

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// Stat is the download record for one (ecosystem, name, version).
type Stat struct {
	Count   int       `json:"count"`
	FirstAt time.Time `json:"first_at"`
	LastAt  time.Time `json:"last_at"`
}

// Store is a mutex-guarded map keyed by "eco\x00name\x00version", optionally
// persisted to a JSON file. Writes are batched: Incr only marks the store dirty;
// Flush writes to disk.
type Store struct {
	mu    sync.Mutex
	m     map[string]Stat
	path  string // "" = in-memory only
	dirty bool
}

func key(eco, name, version string) string { return eco + "\x00" + name + "\x00" + version }

// New loads the store from path (if it exists); path "" means in-memory only.
func New(path string) (*Store, error) {
	s := &Store{m: map[string]Stat{}, path: path}
	if path == "" {
		return s, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &s.m); err != nil {
		return nil, err
	}
	return s, nil
}

// Incr records one download of eco/name/version (now).
func (s *Store) Incr(eco, name, version string) {
	now := time.Now().UTC()
	k := key(eco, name, version)
	s.mu.Lock()
	st := s.m[k]
	st.Count++
	if st.FirstAt.IsZero() {
		st.FirstAt = now
	}
	st.LastAt = now
	s.m[k] = st
	s.dirty = true
	s.mu.Unlock()
}

// Get returns the stat for a version, or false if it was never downloaded.
func (s *Store) Get(eco, name, version string) (Stat, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.m[key(eco, name, version)]
	return st, ok
}

// Flush writes the store to disk if it has unsaved changes.
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" || !s.dirty {
		return nil
	}
	data, err := json.Marshal(s.m)
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.path, data, 0o600); err != nil {
		return err
	}
	s.dirty = false
	return nil
}

// Close performs a final flush.
func (s *Store) Close() error { return s.Flush() }
