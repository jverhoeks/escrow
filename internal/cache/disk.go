package cache

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Disk struct{ root string }

type metaEntry struct {
	ExpiresAt time.Time `json:"expires_at"`
	Data      []byte    `json:"data"`
}

func NewDisk(root string) (*Disk, error) {
	if err := os.MkdirAll(filepath.Join(root, "meta"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(root, "blobs"), 0o755); err != nil {
		return nil, err
	}
	return &Disk{root: root}, nil
}

func (d *Disk) metaPath(key string) string {
	return filepath.Join(d.root, "meta", sanitize(key)+".json")
}

func (d *Disk) blobPath(key string) string {
	return filepath.Join(d.root, "blobs", sanitize(key))
}

// sanitize converts a cache key to a safe relative path. It rejects keys
// containing ".." components to prevent path traversal attacks.
func sanitize(key string) string {
	// Replace forward slashes with OS separator
	rel := strings.ReplaceAll(key, "/", string(os.PathSeparator))
	// Clean the path (resolves .., removes duplicate separators)
	rel = filepath.Clean(rel)
	// Reject any key that still contains ".." after cleaning
	// (this catches cases like "../../etc/passwd")
	if strings.Contains(rel, ".."+string(os.PathSeparator)) || rel == ".." {
		return "invalid"
	}
	// Remove any leading separator
	return strings.TrimPrefix(rel, string(os.PathSeparator))
}

func (d *Disk) GetMeta(_ context.Context, key string) ([]byte, error) {
	data, err := os.ReadFile(d.metaPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entry metaEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, nil // treat corrupt entry as miss
	}
	if time.Now().After(entry.ExpiresAt) {
		os.Remove(d.metaPath(key))
		return nil, nil
	}
	return entry.Data, nil
}

func (d *Disk) SetMeta(_ context.Context, key string, data []byte, ttl time.Duration) error {
	entry := metaEntry{ExpiresAt: time.Now().Add(ttl), Data: data}
	encoded, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	path := d.metaPath(key)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, encoded, 0o644)
}

func (d *Disk) GetBlob(_ context.Context, key string) (io.ReadCloser, error) {
	f, err := os.Open(d.blobPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return f, err
}

func (d *Disk) SetBlob(_ context.Context, key string, r io.Reader) error {
	path := d.blobPath(key)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, r)
	return err
}

func (d *Disk) HasBlob(_ context.Context, key string) bool {
	_, err := os.Stat(d.blobPath(key))
	return err == nil
}

func (d *Disk) Close() error { return nil }
