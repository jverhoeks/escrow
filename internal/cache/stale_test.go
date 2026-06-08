package cache_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jverhoeks/escrow/internal/cache"
)

// staleCache is the subset of cache.Cache the stale-on-error tests exercise.
type staleCache interface {
	SetMeta(ctx context.Context, key string, data []byte, ttl time.Duration) error
	GetMeta(ctx context.Context, key string) ([]byte, error)
	GetMetaStale(ctx context.Context, key string) ([]byte, time.Time, error)
	SetStaleMaxAge(d time.Duration)
	Close() error
}

// TestGetMetaStale runs the disabled/fresh-within-grace/expired-beyond-grace/absent
// table against both the memory and disk backends.
func TestGetMetaStale(t *testing.T) {
	backends := map[string]func(t *testing.T) staleCache{
		"memory": func(t *testing.T) staleCache { return cache.NewMemory() },
		"disk": func(t *testing.T) staleCache {
			c, err := cache.NewDisk(t.TempDir())
			require.NoError(t, err)
			return c
		},
	}

	for name, mk := range backends {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			data := []byte(`{"name":"lodash"}`)

			t.Run("disabled returns nil", func(t *testing.T) {
				c := mk(t)
				defer c.Close()
				// maxAge stays 0 (default). Even an expired entry must not be served.
				require.NoError(t, c.SetMeta(ctx, "npm/meta/lodash", data, -time.Second))
				got, exp, err := c.GetMetaStale(ctx, "npm/meta/lodash")
				require.NoError(t, err)
				assert.Nil(t, got)
				assert.True(t, exp.IsZero())
			})

			t.Run("fresh within grace after expiry returns data", func(t *testing.T) {
				c := mk(t)
				defer c.Close()
				c.SetStaleMaxAge(10 * time.Minute)
				// Expired 1 minute ago, grace 10 minutes → still serveable.
				require.NoError(t, c.SetMeta(ctx, "npm/meta/lodash", data, -time.Minute))
				// GetMeta must still report a miss for an expired entry.
				live, err := c.GetMeta(ctx, "npm/meta/lodash")
				require.NoError(t, err)
				assert.Nil(t, live)
				// GetMetaStale serves it.
				got, exp, err := c.GetMetaStale(ctx, "npm/meta/lodash")
				require.NoError(t, err)
				assert.Equal(t, data, got)
				assert.False(t, exp.IsZero())
			})

			t.Run("expired beyond grace returns nil", func(t *testing.T) {
				c := mk(t)
				defer c.Close()
				c.SetStaleMaxAge(5 * time.Minute)
				// Expired 10 minutes ago, grace 5 minutes → too old.
				require.NoError(t, c.SetMeta(ctx, "npm/meta/lodash", data, -10*time.Minute))
				got, exp, err := c.GetMetaStale(ctx, "npm/meta/lodash")
				require.NoError(t, err)
				assert.Nil(t, got)
				assert.True(t, exp.IsZero())
			})

			t.Run("absent returns nil", func(t *testing.T) {
				c := mk(t)
				defer c.Close()
				c.SetStaleMaxAge(10 * time.Minute)
				got, exp, err := c.GetMetaStale(ctx, "npm/meta/missing")
				require.NoError(t, err)
				assert.Nil(t, got)
				assert.True(t, exp.IsZero())
			})
		})
	}
}

// TestDisk_StaleRetainsExpiredFile verifies the disk backend does NOT eager-delete
// an expired meta file when stale-on-error is enabled, so GetMetaStale can serve it.
func TestDisk_StaleRetainsExpiredFile(t *testing.T) {
	ctx := context.Background()
	c, err := cache.NewDisk(t.TempDir())
	require.NoError(t, err)
	defer c.Close()
	c.SetStaleMaxAge(10 * time.Minute)

	data := []byte(`{"name":"lodash"}`)
	require.NoError(t, c.SetMeta(ctx, "npm/meta/lodash", data, -time.Minute))

	// Expired GetMeta reports a miss but must NOT delete the file.
	got, err := c.GetMeta(ctx, "npm/meta/lodash")
	require.NoError(t, err)
	assert.Nil(t, got)

	// GetMetaStale proves the file is still on disk.
	stale, _, err := c.GetMetaStale(ctx, "npm/meta/lodash")
	require.NoError(t, err)
	assert.Equal(t, data, stale)
}

// TestDisk_DefaultEagerDeleteRegression verifies the original delete-on-expiry
// behavior is preserved when stale-on-error is disabled (maxAge == 0). It flips
// maxAge on after the expired GetMeta so GetMetaStale's enabled path actually
// probes the file — proving GetMeta deleted it.
func TestDisk_DefaultEagerDeleteRegression(t *testing.T) {
	ctx := context.Background()
	c, err := cache.NewDisk(t.TempDir())
	require.NoError(t, err)
	defer c.Close()
	// maxAge stays 0: default eager-delete posture.

	data := []byte(`{"name":"lodash"}`)
	require.NoError(t, c.SetMeta(ctx, "npm/meta/lodash", data, -time.Minute))

	// Expired GetMeta must delete the file (original fail-closed behavior).
	got, err := c.GetMeta(ctx, "npm/meta/lodash")
	require.NoError(t, err)
	assert.Nil(t, got)

	// Now enable stale serving and probe: the file is gone, so nothing to serve.
	c.SetStaleMaxAge(10 * time.Minute)
	stale, _, err := c.GetMetaStale(ctx, "npm/meta/lodash")
	require.NoError(t, err)
	assert.Nil(t, stale, "expired file should have been eager-deleted by GetMeta when stale-on-error was disabled")
}
