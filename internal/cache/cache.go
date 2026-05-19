package cache

import (
	"context"
	"io"
	"time"
)

// Cache stores package metadata (JSON, with TTL) and blobs (tarballs, permanent).
type Cache interface {
	// GetMeta returns nil, nil on a cache miss or expired entry.
	GetMeta(ctx context.Context, key string) ([]byte, error)
	SetMeta(ctx context.Context, key string, data []byte, ttl time.Duration) error
	// GetBlob returns nil, nil on a cache miss.
	GetBlob(ctx context.Context, key string) (io.ReadCloser, error)
	SetBlob(ctx context.Context, key string, r io.Reader) error
	// HasBlob returns true if the blob is present in cache (no download needed).
	HasBlob(ctx context.Context, key string) bool
	// Flush removes all cached entries (metadata and blobs).
	Flush() error
	Close() error
}
