package upstream

import (
	"fmt"
	"io"
)

// MaxManifestBytes caps the size of a registry metadata response read into
// memory. 32 MB is enough for any real-world package manifest (the largest
// npm package metadata documents observed are < 20 MB) while preventing a
// malicious or misbehaving upstream from exhausting process memory.
const MaxManifestBytes = 32 << 20

// ReadBody reads up to MaxManifestBytes from r and returns an error if the
// stream would exceed that limit. The returned slice always reflects the
// actual bytes read; never reads beyond the cap.
//
// Use this instead of io.ReadAll for any upstream response whose contents
// are buffered fully in memory (manifests, JSON metadata, XML index files).
// For artifact downloads streamed to clients/cache, continue to use io.Copy.
func ReadBody(r io.Reader) ([]byte, error) {
	return ReadBodyLimit(r, MaxManifestBytes)
}

// ReadBodyLimit reads up to limit+1 bytes; if the stream would exceed limit
// it returns an error rather than buffering the excess.
func ReadBodyLimit(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("upstream response exceeds %d-byte limit", limit)
	}
	return data, nil
}
