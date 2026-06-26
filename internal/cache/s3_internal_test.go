package cache

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// SetBlob buffers the upload to os.CreateTemp(tempDir, ...) before any S3
// PutObject. A non-existent tempDir must surface as an error here, which proves
// the configured tempDir is honored — and it fails before s.client is touched,
// so a nil client is safe in this test (no real S3 / AWS config needed).
func TestS3SetBlobHonorsTempDir(t *testing.T) {
	s := &S3Cache{tempDir: filepath.Join(t.TempDir(), "does-not-exist")}
	err := s.SetBlob(context.Background(), "some/key", strings.NewReader("payload"))
	require.Error(t, err)
}
