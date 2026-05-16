package cache

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type memEntry struct {
	data      []byte
	expiresAt time.Time
}

type Memory struct {
	mu      sync.RWMutex
	meta    map[string]memEntry
	tempDir string
}

func NewMemory() *Memory {
	dir, _ := os.MkdirTemp("", "sentinel-memory-*")
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
	return nil
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
	os.MkdirAll(filepath.Dir(path), 0o755)
	// Read into memory first so the reader isn't consumed
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, bytes.NewReader(data))
	return err
}

func (m *Memory) Close() error {
	return os.RemoveAll(m.tempDir)
}
