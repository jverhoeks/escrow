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
	"sync/atomic"
	"time"

	"github.com/jellydator/ttlcache/v3"
	"github.com/rs/zerolog/log"

	"github.com/jverhoeks/escrow/internal/metrics"
)

// metaCacheCapacity is the maximum number of metadata entries held in memory.
// At ~50 KB average per manifest, 2000 entries ≈ 100 MB worst-case.
const metaCacheCapacity = 2000

type Disk struct {
	root        string
	maxBytes    int64
	mem         *ttlcache.Cache[string, []byte] // in-memory write-through meta layer
	done        chan struct{}
	staleMaxAge atomic.Int64 // stale-on-error grace window (ns); 0 = disabled. Read by the sweep goroutine, so atomic.
}

type metaEntry struct {
	ExpiresAt time.Time `json:"expires_at"`
	Data      []byte    `json:"data"`
}

func NewDisk(root string) (*Disk, error) {
	return newDisk(root, 0, 0)
}

// NewDiskWithMax creates a disk cache with:
//   - background FIFO purge goroutine that runs every purgeInterval
//   - an in-memory write-through meta cache (capacity metaCacheCapacity)
func NewDiskWithMax(root string, maxBytes int64, purgeInterval time.Duration) (*Disk, error) {
	return newDisk(root, maxBytes, purgeInterval)
}

func newDisk(root string, maxBytes int64, purgeInterval time.Duration) (*Disk, error) {
	for _, sub := range []string{"meta", "blobs"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			return nil, err
		}
	}
	mem := ttlcache.New(
		ttlcache.WithCapacity[string, []byte](metaCacheCapacity),
		ttlcache.WithDisableTouchOnHit[string, []byte](), // TTL counts from Set, not last Get
	)
	go mem.Start()

	d := &Disk{root: root, maxBytes: maxBytes, mem: mem, done: make(chan struct{})}
	if purgeInterval > 0 {
		go d.runPurge(purgeInterval)
	}
	return d, nil
}

func (d *Disk) runPurge(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			d.purge()
		case <-d.done:
			return
		}
	}
}

// purge sweeps expired meta entries from disk and evicts oldest blobs when over the size limit.
func (d *Disk) purge() {
	d.sweepExpiredMeta()
	if d.maxBytes > 0 {
		d.evictOldestBlobs()
	}
}

func (d *Disk) sweepExpiredMeta() {
	// When stale-on-error is enabled, retain expired entries until they fall
	// outside the grace window so GetMetaStale can still serve them. When
	// disabled (grace == 0) this is exactly the original now > ExpiresAt sweep.
	grace := time.Duration(d.staleMaxAge.Load())
	metaDir := filepath.Join(d.root, "meta")
	filepath.WalkDir(metaDir, func(p string, e os.DirEntry, err error) error { //nolint:errcheck
		if err != nil || e.IsDir() || !strings.HasSuffix(p, ".json") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		var entry metaEntry
		if json.Unmarshal(data, &entry) != nil || time.Now().After(entry.ExpiresAt.Add(grace)) {
			os.Remove(p)
		}
		return nil
	})
}

type blobFile struct {
	path    string
	size    int64
	modTime time.Time
}

// evictOldestBlobs enforces maxBytes over the blobs/ subtree only. The meta/
// subtree is intentionally NOT counted: metadata is bounded independently by
// TTL+grace expiry (see purgeExpiredMeta) and the in-memory layer's
// metaCacheCapacity, so folding it into the blob byte budget would conflate two
// separate bounding mechanisms. MaxSizeGB therefore caps cached artifact bytes,
// not the (separately-bounded) metadata footprint. See #50.
func (d *Disk) evictOldestBlobs() {
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

	// Oldest-accessed first (mtime updated on read, so this is LRU).
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

func (d *Disk) metaPath(key string) string {
	return d.safeJoin("meta", sanitize(key)+".json")
}

func (d *Disk) blobPath(key string) string {
	return d.safeJoin("blobs", sanitize(key))
}

// safeJoin joins under d.root and verifies the result stays inside it. If the
// joined path escapes the root (which sanitize() should already prevent) it
// returns a fixed "invalid" path inside the root so writes go to a known safe
// quarantine location rather than overwriting arbitrary files.
func (d *Disk) safeJoin(subdir, rel string) string {
	p := filepath.Join(d.root, subdir, rel)
	clean := filepath.Clean(p)
	rootClean := filepath.Clean(d.root) + string(os.PathSeparator)
	if clean != filepath.Clean(d.root) && !strings.HasPrefix(clean, rootClean) {
		return filepath.Join(d.root, subdir, "invalid")
	}
	return p
}

// sanitize converts a cache key to a safe relative path. It rejects keys
// containing ".." components to prevent path traversal attacks.
func sanitize(key string) string {
	rel := strings.ReplaceAll(key, "/", string(os.PathSeparator))
	rel = filepath.Clean(rel)
	if strings.Contains(rel, ".."+string(os.PathSeparator)) || rel == ".." || strings.HasPrefix(rel, "..") {
		return "invalid"
	}
	return strings.TrimPrefix(rel, string(os.PathSeparator))
}

func (d *Disk) GetMeta(_ context.Context, key string) ([]byte, error) {
	// Fast path: in-memory hit.
	if item := d.mem.Get(key); item != nil {
		return item.Value(), nil
	}

	// Disk read — also populates memory cache with remaining TTL.
	raw, err := os.ReadFile(d.metaPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entry metaEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return nil, nil
	}
	remaining := time.Until(entry.ExpiresAt)
	if remaining <= 0 {
		// Default (stale-on-error disabled): eager-delete the expired file,
		// preserving escrow's fail-closed posture. When enabled, leave the file
		// on disk so GetMetaStale can serve it within the grace window.
		if d.staleMaxAge.Load() == 0 {
			os.Remove(d.metaPath(key))
		}
		return nil, nil
	}
	d.mem.Set(key, entry.Data, remaining)
	return entry.Data, nil
}

// SetStaleMaxAge configures the stale-on-error grace window. Zero disables it.
func (d *Disk) SetStaleMaxAge(dur time.Duration) {
	d.staleMaxAge.Store(int64(dur))
}

// GetMetaStale returns a recently-expired meta entry within the grace window.
// It reads the file directly (the in-memory ttlcache layer has already evicted
// expired entries) and never repopulates the in-memory cache.
func (d *Disk) GetMetaStale(_ context.Context, key string) ([]byte, time.Time, error) {
	grace := time.Duration(d.staleMaxAge.Load())
	if grace == 0 {
		return nil, time.Time{}, nil
	}
	raw, err := os.ReadFile(d.metaPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil, time.Time{}, nil
	}
	if err != nil {
		return nil, time.Time{}, err
	}
	var entry metaEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return nil, time.Time{}, nil
	}
	if time.Since(entry.ExpiresAt) > grace {
		return nil, time.Time{}, nil
	}
	return entry.Data, entry.ExpiresAt, nil
}

func (d *Disk) SetMeta(_ context.Context, key string, data []byte, ttl time.Duration) (err error) {
	// Log + meter any write failure centrally so it's visible even though the
	// proxy handlers ignore the returned error.
	defer func() {
		if err != nil {
			log.Warn().Err(err).Str("op", "setmeta").Msg("cache write failed")
			metrics.CacheWriteFailuresTotal.WithLabelValues("disk", "meta").Inc()
		}
	}()
	entry := metaEntry{ExpiresAt: time.Now().Add(ttl), Data: data}
	encoded, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	path := d.metaPath(key)
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err = os.WriteFile(path, encoded, 0o644); err != nil {
		return err
	}
	if ttl > 0 {
		d.mem.Set(key, data, ttl)
	}
	return nil
}

func (d *Disk) GetBlob(_ context.Context, key string) (io.ReadCloser, error) {
	path := d.blobPath(key)
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Touch mtime on read so the eviction order reflects last access (LRU not FIFO).
	now := time.Now()
	os.Chtimes(path, now, now) //nolint:errcheck
	return f, nil
}

func (d *Disk) SetBlob(_ context.Context, key string, r io.Reader) (err error) {
	defer func() {
		if err != nil {
			log.Warn().Err(err).Str("op", "setblob").Msg("cache write failed")
			metrics.CacheWriteFailuresTotal.WithLabelValues("disk", "blob").Inc()
		}
	}()
	path := d.blobPath(key)
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Write to a temp file in the same directory, then rename atomically.
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
	if err = os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

func (d *Disk) HasBlob(_ context.Context, key string) bool {
	_, err := os.Stat(d.blobPath(key))
	return err == nil
}

func (d *Disk) BlobSize(_ context.Context, key string) int64 {
	info, err := os.Stat(d.blobPath(key))
	if err != nil {
		return -1
	}
	return info.Size()
}

func (d *Disk) Flush() error {
	d.mem.DeleteAll()
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

// InvalidateMeta drops all cached metadata (in-memory layer + on-disk meta/
// files), keeping blobs. See Cache.
func (d *Disk) InvalidateMeta() error {
	d.mem.DeleteAll()
	metaDir := filepath.Join(d.root, "meta")
	if err := os.RemoveAll(metaDir); err != nil {
		return err
	}
	return os.MkdirAll(metaDir, 0o755)
}

// Purge immediately sweeps expired meta and evicts over-limit blobs.
// Exposed for testing; production code relies on the background ticker.
func (d *Disk) Purge() {
	d.purge()
}

// Healthy verifies the disk cache root is writable by creating and removing a
// probe file (the logic formerly in metrics.probeCacheWritable). Returns the
// underlying error if the probe write fails (dir removed, read-only, full, etc.).
func (d *Disk) Healthy(_ context.Context) error {
	f, err := os.CreateTemp(d.root, ".health-probe-*")
	if err != nil {
		return err
	}
	f.Close()
	os.Remove(f.Name())
	return nil
}

func (d *Disk) Close() error {
	d.mem.Stop()
	select {
	case <-d.done:
	default:
		close(d.done)
	}
	return nil
}
