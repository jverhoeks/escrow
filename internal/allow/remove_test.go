package allow_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/allow"
)

func TestList_Remove_ExactVersion(t *testing.T) {
	l, _ := allow.New("")
	require.NoError(t, l.Add(allow.Entry{Ecosystem: "npm", Name: "lodash", Version: "4.17.20"}))
	require.NoError(t, l.Add(allow.Entry{Ecosystem: "npm", Name: "lodash", Version: "4.17.21"}))

	require.NoError(t, l.Remove("npm", "lodash", "4.17.21"))

	entries := l.Entries()
	require.Len(t, entries, 1)
	assert.Equal(t, "4.17.20", entries[0].Version)
}

func TestList_Remove_AllVersions(t *testing.T) {
	l, _ := allow.New("")
	require.NoError(t, l.Add(allow.Entry{Ecosystem: "npm", Name: "lodash", Version: "4.17.20"}))
	require.NoError(t, l.Add(allow.Entry{Ecosystem: "npm", Name: "lodash", Version: ""}))

	require.NoError(t, l.Remove("npm", "lodash", ""))

	assert.Empty(t, l.Entries())
}

func TestList_Remove_LeavesOtherPackages(t *testing.T) {
	l, _ := allow.New("")
	require.NoError(t, l.Add(allow.Entry{Ecosystem: "npm", Name: "lodash", Version: "4.17.21"}))
	require.NoError(t, l.Add(allow.Entry{Ecosystem: "npm", Name: "once", Version: "1.4.0"}))

	require.NoError(t, l.Remove("npm", "lodash", ""))

	entries := l.Entries()
	require.Len(t, entries, 1)
	assert.Equal(t, "once", entries[0].Name)
}

func TestList_Remove_PersistsToDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "allowlist.json")

	l, err := allow.New(path)
	require.NoError(t, err)
	require.NoError(t, l.Add(allow.Entry{Ecosystem: "npm", Name: "lodash", Version: "4.17.21"}))
	require.NoError(t, l.Add(allow.Entry{Ecosystem: "npm", Name: "once", Version: "1.4.0"}))
	require.NoError(t, l.Remove("npm", "lodash", ""))

	l2, err := allow.New(path)
	require.NoError(t, err)
	entries := l2.Entries()
	require.Len(t, entries, 1)
	assert.Equal(t, "once", entries[0].Name)
}

func TestList_Remove_NonExistent_NoError(t *testing.T) {
	l, _ := allow.New("")
	assert.NoError(t, l.Remove("npm", "nonexistent", ""))
	assert.Empty(t, l.Entries())
}
