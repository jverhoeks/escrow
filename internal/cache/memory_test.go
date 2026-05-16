package cache_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/cache"
)

func TestMemory_MetaRoundtrip(t *testing.T) {
	c := cache.NewMemory()
	defer c.Close()
	ctx := context.Background()
	data := []byte(`{"name":"requests"}`)
	require.NoError(t, c.SetMeta(ctx, "pypi/requests/2.31.0", data, time.Hour))
	got, err := c.GetMeta(ctx, "pypi/requests/2.31.0")
	require.NoError(t, err)
	assert.Equal(t, data, got)
}

func TestMemory_MetaTTLExpiry(t *testing.T) {
	c := cache.NewMemory()
	defer c.Close()
	ctx := context.Background()
	require.NoError(t, c.SetMeta(ctx, "pypi/old/1.0.0", []byte("x"), -time.Second))
	got, err := c.GetMeta(ctx, "pypi/old/1.0.0")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestMemory_BlobRoundtrip(t *testing.T) {
	c := cache.NewMemory()
	defer c.Close()
	ctx := context.Background()
	content := []byte("wheel-content")
	require.NoError(t, c.SetBlob(ctx, "pypi/requests/requests-2.31.0-py3-none-any.whl", bytes.NewReader(content)))
	r, err := c.GetBlob(ctx, "pypi/requests/requests-2.31.0-py3-none-any.whl")
	require.NoError(t, err)
	defer r.Close()
	got, _ := io.ReadAll(r)
	assert.Equal(t, content, got)
}

func TestMemory_CloseDeletesTempFiles(t *testing.T) {
	c := cache.NewMemory()
	ctx := context.Background()
	c.SetBlob(ctx, "pypi/test/test-1.0.0.whl", bytes.NewReader([]byte("data")))
	dir := c.TempDir()
	c.Close()
	_, err := os.Stat(dir)
	assert.True(t, os.IsNotExist(err))
}
