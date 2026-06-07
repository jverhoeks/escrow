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
	// GetMetaStale returns a recently-expired metadata entry for stale-on-error
	// serving. It returns the stored bytes (even if expired) when the grace
	// window configured via SetStaleMaxAge is non-zero and the entry expired no
	// longer than that window ago. On a disabled (zero) grace window, an absent
	// entry, or one expired beyond the grace window it returns nil, zero-time, nil.
	// Metadata only — blobs are immutable and never served stale.
	GetMetaStale(ctx context.Context, key string) (data []byte, expiresAt time.Time, err error)
	// SetStaleMaxAge configures the stale-on-error grace window. Zero (the
	// default) disables stale serving and preserves the original
	// delete-on-expiry behavior. Set once at startup.
	SetStaleMaxAge(d time.Duration)
	// GetBlob returns nil, nil on a cache miss.
	GetBlob(ctx context.Context, key string) (io.ReadCloser, error)
	SetBlob(ctx context.Context, key string, r io.Reader) error
	// HasBlob returns true if the blob is present in cache (no download needed).
	HasBlob(ctx context.Context, key string) bool
	// BlobSize returns the blob size in bytes, or -1 if the blob is absent
	// or its size cannot be determined.
	BlobSize(ctx context.Context, key string) int64
	// Flush removes all cached entries (metadata and blobs).
	Flush() error
	Close() error
}
