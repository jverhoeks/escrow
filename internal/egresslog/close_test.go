package egresslog

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLog_Close(t *testing.T) {
	path := filepath.Join(t.TempDir(), "egress.jsonl")
	l, err := NewWithPath(10, path)
	require.NoError(t, err)

	l.Record(ev("a.com", "allow"))
	require.NoError(t, l.Close())
	require.NoError(t, l.Close()) // idempotent — second close is a no-op

	// The record was flushed to disk before Close.
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "a.com")

	// Record after Close must not panic (file is nil; ring still works).
	l.Record(ev("b.com", "block"))
	require.Equal(t, "b.com", l.Recent(1, "")[0].Host)
}

func TestLog_Close_NoFile(t *testing.T) {
	// An in-memory log (no path) has no file; Close is a safe no-op.
	require.NoError(t, New(10).Close())
}
