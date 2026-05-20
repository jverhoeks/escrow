package cache

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Disk struct {
	root     string
	maxBytes int64 // 0 = unlimited
}

type metaEntry struct {
	ExpiresAt time.Time `json:"expires_at"`
	Data      []byte    `json:"data"`
}

func NewDisk(root string) (*Disk, error) {
	return NewDiskWithMax(root, 0)
}

func NewDiskWithMax(root string, maxBytes int64) (*Disk, error) {
	if err := os.MkdirAll(filepath.Join(root, "meta"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(root, "blobs"), 0o755); err != nil {
		return nil, err
	}
	return &Disk{root: root, maxBytes: maxBytes}, nil
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
	rel := strings.ReplaceAll(key, "/", string(os.PathSeparator))
	rel = filepath.Clean(rel)
	if strings.Contains(rel, ".."+string(os.PathSeparator)) || rel == ".." {
		return "invalid"
	}
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
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Write to a temp file in the same directory, then rename atomically.
	// This prevents concurrent writers from corrupting the file and prevents
	// partial reads if a client disconnects mid-download.
	tmp, err := os.CreateTemp(dir, ".blob-tmp-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, err = io.Copy(tmp, r)
	tmp.Close()
	if err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	if d.maxBytes > 0 {
		d.trimBlobs() // best-effort; ignore error
	}
	return nil
}

func (d *Disk) HasBlob(_ context.Context, key string) bool {
	_, err := os.Stat(d.blobPath(key))
	return err == nil
}

func (d *Disk) Flush() error {
	for _, sub := range []string{"meta", "blobs"} {
		dir := filepath.Join(d.root, sub)
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func (d *Disk) Close() error { return nil }

type blobFile struct {
	path    string
	size    int64
	modTime time.Time
}

// trimBlobs evicts the oldest blobs until total blob dir size is under maxBytes.
func (d *Disk) trimBlobs() {
	blobDir := filepath.Join(d.root, "blobs")
	var files []blobFile
	var total int64

	filepath.WalkDir(blobDir, func(p string, e os.DirEntry, err error) error { //nolint:errcheck
		if err != nil || e.IsDir() {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return nil
		}
		files = append(files, blobFile{path: p, size: info.Size(), modTime: info.ModTime()})
		total += info.Size()
		return nil
	})

	if total <= d.maxBytes {
		return
	}

	// Sort oldest first.
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.Before(files[j].modTime)
	})

	for _, f := range files {
		if total <= d.maxBytes {
			break
		}
		if err := os.Remove(f.path); err == nil {
			total -= f.size
		}
	}
}
