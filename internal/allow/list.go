package allow

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

type Entry struct {
	Ecosystem string    `json:"ecosystem"`
	Name      string    `json:"name"`
	Version   string    `json:"version"` // empty = all versions of this package
	Reason    string    `json:"reason"`
	AddedBy   string    `json:"added_by"`
	AddedAt   time.Time `json:"added_at"`
}

type List struct {
	mu      sync.RWMutex
	entries []Entry
	path    string // path to allowlist.json; empty = in-memory only
}

// New creates a List, loading existing entries from path if the file exists.
// Pass empty string for in-memory-only operation.
func New(path string) (*List, error) {
	l := &List{path: path}
	if path == "" {
		return l, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	return l, json.Unmarshal(data, &l.entries)
}

// IsAllowed returns true if ecosystem+name+version is on the allowlist, along with the matching entry.
// A wildcard entry (empty Version) matches any version of the package.
func (l *List) IsAllowed(ecosystem, name, version string) (bool, Entry) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, e := range l.entries {
		if e.Ecosystem == ecosystem && e.Name == name {
			if e.Version == "" || e.Version == version {
				return true, e
			}
		}
	}
	return false, Entry{}
}

// Add appends an entry and persists if a path is set.
func (l *List) Add(e Entry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e.AddedAt = time.Now().UTC()
	l.entries = append(l.entries, e)
	return l.save()
}

// Entries returns a snapshot of all entries.
func (l *List) Entries() []Entry {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Entry, len(l.entries))
	copy(out, l.entries)
	return out
}

// save writes entries to disk. Caller must hold the write lock.
func (l *List) save() error {
	if l.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(l.entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(l.path, data, 0o600)
}
