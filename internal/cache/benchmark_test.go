package cache_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/jverhoeks/escrow/internal/cache"
)

func BenchmarkDisk_GetMeta_Hit(b *testing.B) {
	c, err := cache.NewDisk(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	key := "npm/lodash/4.17.21"
	data := []byte(`{"name":"lodash","versions":{"4.17.21":{}}}`)
	if err := c.SetMeta(ctx, key, data, time.Hour); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for range b.N {
		c.GetMeta(ctx, key)
	}
}

func BenchmarkDisk_GetMeta_Miss(b *testing.B) {
	c, err := cache.NewDisk(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	key := "npm/doesnotexist/1.0.0"
	b.ResetTimer()
	for range b.N {
		c.GetMeta(ctx, key)
	}
}

func BenchmarkDisk_GetBlob_Hit(b *testing.B) {
	c, err := cache.NewDisk(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	key := "npm/lodash/-/lodash-4.17.21.tgz"
	content := bytes.Repeat([]byte("x"), 1024*100) // 100 KB blob
	if err := c.SetBlob(ctx, key, bytes.NewReader(content)); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for range b.N {
		r, err := c.GetBlob(ctx, key)
		if err != nil {
			b.Fatal(err)
		}
		r.Close()
	}
}

func BenchmarkDisk_SetMeta(b *testing.B) {
	c, err := cache.NewDisk(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	data := []byte(`{"name":"lodash","versions":{"4.17.21":{}}}`)
	b.ResetTimer()
	for range b.N {
		c.SetMeta(ctx, "bench/key", data, time.Hour)
	}
}

func BenchmarkDisk_SetBlob_100KB(b *testing.B) {
	c, err := cache.NewDisk(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	content := bytes.Repeat([]byte("x"), 1024*100)
	b.ResetTimer()
	for range b.N {
		c.SetBlob(ctx, "bench/blob.tgz", bytes.NewReader(content))
	}
}
