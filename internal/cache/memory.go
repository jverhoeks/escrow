package cache

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

type memEntry struct {
	data      []byte
	expiresAt time.Time
}

type Memory struct {
	mu          sync.RWMutex
	meta        map[string]memEntry
	tempDir     string
	staleMaxAge atomic.Int64 // stale-on-error grace window (ns); 0 = disabled.
}

func NewMemory() *Memory {
	dir, _ := os.MkdirTemp("", "escrow-memory-*")
	return &Memory{meta: make(map[string]memEntry), tempDir: dir}
}

func (m *Memory) TempDir() string { return m.tempDir }

func (m *Memory) GetMeta(_ context.Context, key string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.meta[key]
	if !ok || time.Now().After(e.expiresAt) {
		return nil, nil
	}
	out := make([]byte, len(e.data))
	copy(out, e.data)
	return out, nil
}

func (m *Memory) SetMeta(_ context.Context, key string, data []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	m.meta[key] = memEntry{data: cp, expiresAt: time.Now().Add(ttl)}
	// Bound the map so a long-running memory-backend instance can't OOM from one
	// retained entry per distinct package key ever requested. Matches the Disk
	// backend's metaCacheCapacity. See #44.
	if len(m.meta) > metaCacheCapacity {
		m.evictLocked()
	}
	return nil
}

// evictLocked reclaims space when the meta map exceeds metaCacheCapacity. It
// first drops entries expired beyond the stale-on-error grace window (truly
// unreclaimable — never served again, even stale), then, if still over
// capacity, evicts the earliest-expiring entries. Caller must hold m.mu.
func (m *Memory) evictLocked() {
	grace := time.Duration(m.staleMaxAge.Load())
	now := time.Now()
	for k, e := range m.meta {
		if now.Sub(e.expiresAt) > grace {
			delete(m.meta, k)
		}
	}
	for len(m.meta) > metaCacheCapacity {
		var oldestKey string
		var oldest time.Time
		first := true
		for k, e := range m.meta {
			if first || e.expiresAt.Before(oldest) {
				oldest, oldestKey, first = e.expiresAt, k, false
			}
		}
		delete(m.meta, oldestKey)
	}
}

// SetStaleMaxAge configures the stale-on-error grace window. Zero disables it.
func (m *Memory) SetStaleMaxAge(d time.Duration) {
	m.staleMaxAge.Store(int64(d))
}

// GetMetaStale returns a recently-expired meta entry within the grace window.
// The memory backend retains expired bytes (no eager delete), so this reads the
// retained entry and enforces the grace window.
func (m *Memory) GetMetaStale(_ context.Context, key string) ([]byte, time.Time, error) {
	grace := time.Duration(m.staleMaxAge.Load())
	if grace == 0 {
		return nil, time.Time{}, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.meta[key]
	if !ok {
		return nil, time.Time{}, nil
	}
	if time.Since(e.expiresAt) > grace {
		return nil, time.Time{}, nil
	}
	out := make([]byte, len(e.data))
	copy(out, e.data)
	return out, e.expiresAt, nil
}

func (m *Memory) GetBlob(_ context.Context, key string) (io.ReadCloser, error) {
	path := filepath.Join(m.tempDir, sanitize(key))
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return f, err
}

func (m *Memory) SetBlob(_ context.Context, key string, r io.Reader) error {
	path := filepath.Join(m.tempDir, sanitize(key))
	dir := filepath.Dir(path)
	os.MkdirAll(dir, 0o755)
	// Buffer first, then write to a temp file and rename atomically.
	// This prevents concurrent readers from seeing partial data and concurrent
	// writers from corrupting the file.
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".blob-tmp-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, err = tmp.Write(data)
	tmp.Close()
	if err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

func (m *Memory) HasBlob(_ context.Context, key string) bool {
	_, err := os.Stat(filepath.Join(m.tempDir, sanitize(key)))
	return err == nil
}

func (m *Memory) BlobSize(_ context.Context, key string) int64 {
	info, err := os.Stat(filepath.Join(m.tempDir, sanitize(key)))
	if err != nil {
		return -1
	}
	return info.Size()
}

// Healthy always returns nil: the in-memory backend cannot become unwritable
// the way disk/S3 can, so it is reported healthy for /healthz.
func (m *Memory) Healthy(_ context.Context) error { return nil }

func (m *Memory) Flush() error {
	m.mu.Lock()
	m.meta = make(map[string]memEntry)
	m.mu.Unlock()
	// Remove and recreate the blob temp dir.
	if err := os.RemoveAll(m.tempDir); err != nil {
		return err
	}
	return os.MkdirAll(m.tempDir, 0o755)
}

func (m *Memory) Close() error {
	return os.RemoveAll(m.tempDir)
}
