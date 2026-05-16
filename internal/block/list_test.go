package block_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/block"
)

func TestBlockList_IsBlocked_ExactVersion(t *testing.T) {
	l, err := block.New("")
	require.NoError(t, err)
	require.NoError(t, l.Add(block.Entry{
		Ecosystem: "npm", Name: "evil-pkg", Version: "1.0.0", Reason: "malicious",
	}))

	ok, e := l.IsBlocked("npm", "evil-pkg", "1.0.0")
	assert.True(t, ok)
	assert.Equal(t, "malicious", e.Reason)

	ok, _ = l.IsBlocked("npm", "evil-pkg", "1.0.1")
	assert.False(t, ok, "different version should not be blocked")
}

func TestBlockList_IsBlocked_Wildcard(t *testing.T) {
	l, _ := block.New("")
	l.Add(block.Entry{Ecosystem: "pypi", Name: "badlib", Version: "", Reason: "all versions blocked"})

	ok, _ := l.IsBlocked("pypi", "badlib", "2.31.0")
	assert.True(t, ok)
	ok, _ = l.IsBlocked("pypi", "badlib", "2.28.0")
	assert.True(t, ok)
}

func TestBlockList_IsBlocked_EcosystemMismatch(t *testing.T) {
	l, _ := block.New("")
	l.Add(block.Entry{Ecosystem: "npm", Name: "evil-pkg", Version: "1.0.0"})

	ok, _ := l.IsBlocked("pypi", "evil-pkg", "1.0.0")
	assert.False(t, ok, "different ecosystem should not match")
}

func TestBlockList_Add_Persists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blocklist.json")

	l1, err := block.New(path)
	require.NoError(t, err)
	require.NoError(t, l1.Add(block.Entry{
		Ecosystem: "npm", Name: "bad-pkg", Version: "2.0.0", Reason: "flagged",
	}))

	// Load from same path in a new list
	l2, err := block.New(path)
	require.NoError(t, err)
	ok, e := l2.IsBlocked("npm", "bad-pkg", "2.0.0")
	assert.True(t, ok)
	assert.Equal(t, "flagged", e.Reason)
	assert.False(t, e.AddedAt.IsZero(), "AddedAt should be set")
}
