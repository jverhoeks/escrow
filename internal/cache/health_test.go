package cache

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jverhoeks/escrow/internal/metrics"
)

func TestDiskHealthy_WritableDir(t *testing.T) {
	d, err := NewDisk(t.TempDir())
	require.NoError(t, err)
	defer d.Close()
	assert.NoError(t, d.Healthy(context.Background()), "writable cache dir should be healthy")
}

func TestDiskHealthy_NonWritableDir(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses read-only permission")
	}
	root := t.TempDir()
	d, err := NewDisk(root)
	require.NoError(t, err)
	defer d.Close()

	// Remove write permission on the cache root so the probe write fails.
	require.NoError(t, os.Chmod(root, 0o555))
	defer os.Chmod(root, 0o755) //nolint:errcheck

	assert.Error(t, d.Healthy(context.Background()), "non-writable cache dir should be unhealthy")
}

func TestDiskHealthy_RemovedDir(t *testing.T) {
	root := t.TempDir()
	d, err := NewDisk(root)
	require.NoError(t, err)
	defer d.Close()

	require.NoError(t, os.RemoveAll(root))
	assert.Error(t, d.Healthy(context.Background()), "removed cache dir should be unhealthy")
}

func TestMemoryHealthy(t *testing.T) {
	m := NewMemory()
	defer m.Close()
	assert.NoError(t, m.Healthy(context.Background()), "memory backend is always healthy")
}

func TestDiskSetBlob_WriteFailureMetered(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses read-only permission")
	}
	root := t.TempDir()
	d, err := NewDisk(root)
	require.NoError(t, err)
	defer d.Close()

	// Make the blobs subdir non-writable so SetBlob's temp-file create fails.
	blobs := root + "/blobs"
	require.NoError(t, os.Chmod(blobs, 0o555))
	defer os.Chmod(blobs, 0o755) //nolint:errcheck

	before := testutil.ToFloat64(metrics.CacheWriteFailuresTotal.WithLabelValues("disk", "blob"))
	err = d.SetBlob(context.Background(), "pkg/foo-1.0.0.tgz", bytes.NewReader([]byte("data")))
	require.Error(t, err, "SetBlob to non-writable dir should return an error")
	after := testutil.ToFloat64(metrics.CacheWriteFailuresTotal.WithLabelValues("disk", "blob"))
	assert.Equal(t, before+1, after, "cache write failure should be metered")
}
