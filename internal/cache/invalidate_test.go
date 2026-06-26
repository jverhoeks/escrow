package cache_test

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/jverhoeks/escrow/internal/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// InvalidateMeta must drop cached metadata (so a policy/block change takes
// effect on the next listing) while keeping immutable blobs. See #38.
func TestInvalidateMeta_DropsMetaKeepsBlobs(t *testing.T) {
	disk, err := cache.NewDiskWithMax(filepath.Join(t.TempDir(), "c"), 0, 0)
	require.NoError(t, err)
	defer disk.Close()

	backends := map[string]cache.Cache{
		"memory": cache.NewMemory(),
		"disk":   disk,
	}
	ctx := context.Background()
	for name, c := range backends {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, c.SetMeta(ctx, "npm/meta/lodash", []byte(`{"v":1}`), time.Hour))
			require.NoError(t, c.SetBlob(ctx, "npm/lodash/-/lodash-1.0.0.tgz", bytes.NewReader([]byte("BLOB"))))

			require.NoError(t, c.InvalidateMeta())

			meta, err := c.GetMeta(ctx, "npm/meta/lodash")
			require.NoError(t, err)
			assert.Nil(t, meta, "metadata must be dropped by InvalidateMeta")

			blob, err := c.GetBlob(ctx, "npm/lodash/-/lodash-1.0.0.tgz")
			require.NoError(t, err)
			require.NotNil(t, blob, "blob must survive InvalidateMeta")
			b, _ := io.ReadAll(blob)
			blob.Close()
			assert.Equal(t, "BLOB", string(b))
		})
	}
}
