package logfile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jverhoeks/escrow/internal/logfile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAtomicRewrite_WritesLinesWithNewlines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.jsonl")
	n, err := logfile.AtomicRewrite(path, [][]byte{[]byte(`{"a":1}`), []byte(`{"b":2}`)})
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "{\"a\":1}\n{\"b\":2}\n", string(data))
	assert.Equal(t, int64(len(data)), n)
}

func TestAtomicRewrite_OverwritesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("old\nold\nold\n"), 0o600))
	_, err := logfile.AtomicRewrite(path, [][]byte{[]byte("new")})
	require.NoError(t, err)
	data, _ := os.ReadFile(path)
	assert.Equal(t, "new\n", string(data))
}

func TestAtomicRewrite_Mode0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.jsonl")
	_, err := logfile.AtomicRewrite(path, [][]byte{[]byte("x")})
	require.NoError(t, err)
	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
}

func TestAtomicRewrite_NoTempLeftBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.jsonl")
	_, err := logfile.AtomicRewrite(path, [][]byte{[]byte("x")})
	require.NoError(t, err)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "out.jsonl", entries[0].Name())
}

func TestAtomicRewrite_ErrorWhenDirMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope", "out.jsonl")
	_, err := logfile.AtomicRewrite(path, [][]byte{[]byte("x")})
	require.Error(t, err)
}
