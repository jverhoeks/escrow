package cache_test

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/cache"
)

func TestDisk_SetBlob_AtomicConcurrent(t *testing.T) {
	dir := t.TempDir()
	d, err := cache.NewDisk(dir)
	require.NoError(t, err)

	const key = "test/concurrent"
	const body = "hello concurrent world"
	const goroutines = 20

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.SetBlob(context.Background(), key, bytes.NewReader([]byte(body)))
		}()
	}
	wg.Wait()

	blob, err := d.GetBlob(context.Background(), key)
	require.NoError(t, err)
	require.NotNil(t, blob)
	defer blob.Close()
	got, _ := io.ReadAll(blob)
	assert.Equal(t, body, string(got), "blob should be complete and not corrupted by concurrent writes")
}

func TestDisk_SetBlob_PartialWriteNotCached(t *testing.T) {
	// If the reader returns an error mid-way, the blob must not be readable from cache.
	dir := t.TempDir()
	d, err := cache.NewDisk(dir)
	require.NoError(t, err)

	const key = "test/partial"
	errReader := &errorAfterNReader{data: []byte("partial"), n: 3}
	err = d.SetBlob(context.Background(), key, errReader)
	assert.Error(t, err, "SetBlob should return the reader error")

	blob, err := d.GetBlob(context.Background(), key)
	assert.NoError(t, err)
	assert.Nil(t, blob, "partial blob should not be cached")
}

// errorAfterNReader returns n bytes then an error.
type errorAfterNReader struct {
	data []byte
	n    int
	pos  int
}

func (r *errorAfterNReader) Read(p []byte) (int, error) {
	if r.pos >= r.n {
		return 0, io.ErrUnexpectedEOF
	}
	remaining := r.n - r.pos
	if len(p) > remaining {
		p = p[:remaining]
	}
	copy(p, r.data[r.pos:])
	r.pos += len(p)
	return len(p), nil
}

func TestDisk_SetBlob_FileInSameDir(t *testing.T) {
	// Verify temp file is in the same directory as the target (required for atomic rename).
	dir := t.TempDir()
	d, err := cache.NewDisk(dir)
	require.NoError(t, err)

	require.NoError(t, d.SetBlob(context.Background(), "npm/lodash/-/lodash-4.17.21.tgz", bytes.NewReader([]byte("tarball"))))

	blob, err := d.GetBlob(context.Background(), "npm/lodash/-/lodash-4.17.21.tgz")
	require.NoError(t, err)
	require.NotNil(t, blob)
	defer blob.Close()

	// Verify no temp files left behind
	entries, _ := filepath.Glob(filepath.Join(dir, "blobs", "**", ".blob-tmp-*"))
	assert.Empty(t, entries, "no temp files should remain after successful write")
}
