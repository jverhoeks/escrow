package block_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/block"
)

func TestBlockList_Remove_ExactVersion(t *testing.T) {
	l, _ := block.New("")
	require.NoError(t, l.Add(block.Entry{Ecosystem: "npm", Name: "evil", Version: "1.0.0"}))
	require.NoError(t, l.Add(block.Entry{Ecosystem: "npm", Name: "evil", Version: "1.0.1"}))

	require.NoError(t, l.Remove("npm", "evil", "1.0.0"))

	entries := l.Entries()
	require.Len(t, entries, 1)
	assert.Equal(t, "1.0.1", entries[0].Version)
}

func TestBlockList_Remove_AllVersions(t *testing.T) {
	l, _ := block.New("")
	require.NoError(t, l.Add(block.Entry{Ecosystem: "npm", Name: "evil", Version: "1.0.0"}))
	require.NoError(t, l.Add(block.Entry{Ecosystem: "npm", Name: "evil", Version: "1.0.1"}))

	require.NoError(t, l.Remove("npm", "evil", ""))

	assert.Empty(t, l.Entries())
}

func TestBlockList_Remove_PersistsToDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blocklist.json")

	l, err := block.New(path)
	require.NoError(t, err)
	require.NoError(t, l.Add(block.Entry{Ecosystem: "npm", Name: "evil", Version: "1.0.0"}))
	require.NoError(t, l.Add(block.Entry{Ecosystem: "npm", Name: "good", Version: "2.0.0"}))
	require.NoError(t, l.Remove("npm", "evil", ""))

	l2, err := block.New(path)
	require.NoError(t, err)
	entries := l2.Entries()
	require.Len(t, entries, 1)
	assert.Equal(t, "good", entries[0].Name)
}
