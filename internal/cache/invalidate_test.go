package cache_test

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/jverhoeks/escrow/internal/block"
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

// #68: adding a block invalidates cached metadata (the #38 OnChange→InvalidateMeta
// wiring used in main.go), so a blocked version cannot be re-exposed via a stale
// manifest during an upstream outage — GetMetaStale returns nil after the block.
func TestInvalidateMeta_BlockDropsStaleServableCopy(t *testing.T) {
	c := cache.NewMemory()
	defer c.Close()
	c.SetStaleMaxAge(time.Hour) // enable stale-on-error
	bl, err := block.New("")
	require.NoError(t, err)
	bl.SetOnChange(func() { _ = c.InvalidateMeta() }) // mirrors main.go wiring
	ctx := context.Background()

	// An already-expired manifest is stale-servable within the grace window.
	require.NoError(t, c.SetMeta(ctx, "npm/meta/evil", []byte(`{"versions":{"1.0.0":{}}}`), -time.Minute))
	d, _, _ := c.GetMetaStale(ctx, "npm/meta/evil")
	require.NotNil(t, d, "precondition: expired entry is stale-servable")

	// Blocking a version fires OnChange → InvalidateMeta.
	require.NoError(t, bl.Add(block.Entry{Ecosystem: "npm", Name: "evil", Version: "1.0.0"}))

	d, _, _ = c.GetMetaStale(ctx, "npm/meta/evil")
	assert.Nil(t, d, "#68: a block must drop the stale-servable manifest")
}
