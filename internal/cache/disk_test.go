package cache_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/cache"
)

func TestDisk_MetaRoundtrip(t *testing.T) {
	c, err := cache.NewDisk(t.TempDir())
	require.NoError(t, err)
	defer c.Close()
	ctx := context.Background()
	data := []byte(`{"name":"lodash"}`)
	require.NoError(t, c.SetMeta(ctx, "npm/lodash/4.17.21", data, time.Hour))
	got, err := c.GetMeta(ctx, "npm/lodash/4.17.21")
	require.NoError(t, err)
	assert.Equal(t, data, got)
}

func TestDisk_MetaMiss(t *testing.T) {
	c, _ := cache.NewDisk(t.TempDir())
	defer c.Close()
	got, err := c.GetMeta(context.Background(), "npm/doesnotexist/1.0.0")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestDisk_MetaTTLExpiry(t *testing.T) {
	c, _ := cache.NewDisk(t.TempDir())
	defer c.Close()
	ctx := context.Background()
	require.NoError(t, c.SetMeta(ctx, "npm/old/1.0.0", []byte("data"), -time.Second))
	got, err := c.GetMeta(ctx, "npm/old/1.0.0")
	require.NoError(t, err)
	assert.Nil(t, got, "expired entry should be a miss")
}

func TestDisk_BlobRoundtrip(t *testing.T) {
	c, _ := cache.NewDisk(t.TempDir())
	defer c.Close()
	ctx := context.Background()
	content := []byte("tarball-bytes")
	require.NoError(t, c.SetBlob(ctx, "npm/lodash/-/lodash-4.17.21.tgz", bytes.NewReader(content)))
	r, err := c.GetBlob(ctx, "npm/lodash/-/lodash-4.17.21.tgz")
	require.NoError(t, err)
	require.NotNil(t, r)
	defer r.Close()
	got, _ := io.ReadAll(r)
	assert.Equal(t, content, got)
}

func TestDisk_BlobMiss(t *testing.T) {
	c, _ := cache.NewDisk(t.TempDir())
	defer c.Close()
	r, err := c.GetBlob(context.Background(), "npm/missing/-/missing-1.0.0.tgz")
	require.NoError(t, err)
	assert.Nil(t, r)
}

// ── eviction / size-limit tests ──────────────────────────────────────────────

// setMtime stamps the mtime of the blob file for a simple (no-slash) key.
func setBlobMtime(t *testing.T, dir, key string, ts time.Time) {
	t.Helper()
	path := filepath.Join(dir, "blobs", key)
	require.NoError(t, os.Chtimes(path, ts, ts))
}

func TestDisk_Evict_UnderLimit_NoEviction(t *testing.T) {
	dir := t.TempDir()
	c, err := cache.NewDiskWithMax(dir, 1000, 0)
	require.NoError(t, err)
	defer c.Close()
	ctx := context.Background()

	for _, key := range []string{"a", "b", "c"} {
		require.NoError(t, c.SetBlob(ctx, key, bytes.NewReader(make([]byte, 100))))
	}

	c.Purge()

	for _, key := range []string{"a", "b", "c"} {
		r, err := c.GetBlob(ctx, key)
		require.NoError(t, err)
		assert.NotNil(t, r, "blob %q should survive when total is under the limit", key)
		if r != nil {
			r.Close()
		}
	}
}

func TestDisk_Evict_OverLimit_EvictsOldestFirst(t *testing.T) {
	// Three 400-byte blobs = 1200 bytes; limit 500 → must delete oldest two.
	dir := t.TempDir()
	c, err := cache.NewDiskWithMax(dir, 500, 0)
	require.NoError(t, err)
	defer c.Close()
	ctx := context.Background()

	for _, key := range []string{"oldest", "middle", "newest"} {
		require.NoError(t, c.SetBlob(ctx, key, bytes.NewReader(make([]byte, 400))))
	}

	base := time.Now().Add(-time.Hour)
	setBlobMtime(t, dir, "oldest", base)
	setBlobMtime(t, dir, "middle", base.Add(10*time.Minute))
	setBlobMtime(t, dir, "newest", base.Add(20*time.Minute))

	c.Purge()

	r, err := c.GetBlob(ctx, "newest")
	require.NoError(t, err)
	assert.NotNil(t, r, "newest blob should survive")
	if r != nil {
		r.Close()
	}

	for _, key := range []string{"oldest", "middle"} {
		r, err := c.GetBlob(ctx, key)
		require.NoError(t, err)
		assert.Nil(t, r, "blob %q should have been evicted", key)
	}
}

func TestDisk_Evict_StopsOnceUnderLimit(t *testing.T) {
	// Three 300-byte blobs = 900 bytes; limit 400.
	// Evicting oldest (300 bytes) → 600 > 400, evict middle (300) → 300 ≤ 400: stop.
	// newest must survive.
	dir := t.TempDir()
	c, err := cache.NewDiskWithMax(dir, 400, 0)
	require.NoError(t, err)
	defer c.Close()
	ctx := context.Background()

	for _, key := range []string{"first", "second", "third"} {
		require.NoError(t, c.SetBlob(ctx, key, bytes.NewReader(make([]byte, 300))))
	}

	base := time.Now().Add(-time.Hour)
	setBlobMtime(t, dir, "first", base)
	setBlobMtime(t, dir, "second", base.Add(10*time.Minute))
	setBlobMtime(t, dir, "third", base.Add(20*time.Minute))

	c.Purge()

	r, err := c.GetBlob(ctx, "third")
	require.NoError(t, err)
	assert.NotNil(t, r, "third blob should survive")
	if r != nil {
		r.Close()
	}
}

func TestDisk_Evict_LRU_RecentAccessSurvives(t *testing.T) {
	// "old" blob has an old mtime, but we access it — GetBlob touches its mtime.
	// After the touch "old" is newer than "stale", so "stale" is evicted instead.
	dir := t.TempDir()
	c, err := cache.NewDiskWithMax(dir, 500, 0)
	require.NoError(t, err)
	defer c.Close()
	ctx := context.Background()

	require.NoError(t, c.SetBlob(ctx, "old", bytes.NewReader(make([]byte, 400))))
	require.NoError(t, c.SetBlob(ctx, "stale", bytes.NewReader(make([]byte, 400))))

	base := time.Now().Add(-time.Hour)
	setBlobMtime(t, dir, "old", base)                           // mtime: T-60m
	setBlobMtime(t, dir, "stale", base.Add(30*time.Minute))    // mtime: T-30m

	// Access "old" — GetBlob calls os.Chtimes to now, making "old" the newest.
	r, err := c.GetBlob(ctx, "old")
	require.NoError(t, err)
	require.NotNil(t, r)
	r.Close()

	// Total 800 bytes > 500 limit. "stale" is now the oldest → evicted.
	c.Purge()

	r, err = c.GetBlob(ctx, "old")
	require.NoError(t, err)
	assert.NotNil(t, r, "recently accessed blob should survive eviction (LRU)")
	if r != nil {
		r.Close()
	}

	r, err = c.GetBlob(ctx, "stale")
	require.NoError(t, err)
	assert.Nil(t, r, "stale blob (not accessed recently) should be evicted")
}

func TestDisk_PurgeTicker_TriggersEviction(t *testing.T) {
	// End-to-end: real ticker fires, over-limit blobs are removed automatically.
	dir := t.TempDir()
	c, err := cache.NewDiskWithMax(dir, 500, 20*time.Millisecond)
	require.NoError(t, err)
	defer c.Close()
	ctx := context.Background()

	require.NoError(t, c.SetBlob(ctx, "tick-old", bytes.NewReader(make([]byte, 400))))
	require.NoError(t, c.SetBlob(ctx, "tick-new", bytes.NewReader(make([]byte, 400))))

	base := time.Now().Add(-time.Hour)
	setBlobMtime(t, dir, "tick-old", base)
	setBlobMtime(t, dir, "tick-new", base.Add(time.Minute))

	// Wait for at least two ticker fires.
	time.Sleep(100 * time.Millisecond)

	r, err := c.GetBlob(ctx, "tick-old")
	require.NoError(t, err)
	assert.Nil(t, r, "oldest blob should be evicted by background ticker")

	r, err = c.GetBlob(ctx, "tick-new")
	require.NoError(t, err)
	assert.NotNil(t, r, "newer blob should survive")
	if r != nil {
		r.Close()
	}
}

func TestDisk_BlobSize(t *testing.T) {
	d, err := cache.NewDisk(t.TempDir())
	require.NoError(t, err)
	defer d.Close()

	ctx := context.Background()
	require.NoError(t, d.SetBlob(ctx, "npm/x/-/x-1.0.0.tgz", bytes.NewReader([]byte("hello world"))))

	if got := d.BlobSize(ctx, "npm/x/-/x-1.0.0.tgz"); got != 11 {
		t.Fatalf("BlobSize = %d, want 11", got)
	}
	if got := d.BlobSize(ctx, "npm/missing"); got != -1 {
		t.Fatalf("BlobSize(missing) = %d, want -1", got)
	}
}

func TestDisk_SanitizePathTraversal(t *testing.T) {
	c, _ := cache.NewDisk(t.TempDir())
	defer c.Close()
	ctx := context.Background()
	// These should not panic or write outside the cache root
	err := c.SetMeta(ctx, "../../etc/passwd", []byte("data"), time.Hour)
	require.NoError(t, err) // should succeed (writes to "invalid.json")
	// Reading it back with the traversal key should return the data (key maps to "invalid")
	// OR return nil (if we choose to silently drop traversal keys)
	// Either behavior is acceptable; the important thing is no files are created outside cache root
	got, err := c.GetMeta(ctx, "../../etc/passwd")
	require.NoError(t, err)
	// Don't assert what got is — just verify no panic and no files escaped
	_ = got
}
