package allow_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/allow"
)

func TestList_AllowExactVersion(t *testing.T) {
	l, err := allow.New("")
	require.NoError(t, err)
	require.NoError(t, l.Add(allow.Entry{
		Ecosystem: "npm", Name: "lodash", Version: "4.17.21", Reason: "audited",
	}))

	ok, e := l.IsAllowed("npm", "lodash", "4.17.21")
	assert.True(t, ok)
	assert.Equal(t, "audited", e.Reason)

	ok, _ = l.IsAllowed("npm", "lodash", "4.17.22")
	assert.False(t, ok, "different version should not be allowed")
}

func TestList_AllowWildcardVersion(t *testing.T) {
	l, _ := allow.New("")
	l.Add(allow.Entry{Ecosystem: "pypi", Name: "requests", Version: "", Reason: "trusted"})

	ok, _ := l.IsAllowed("pypi", "requests", "2.31.0")
	assert.True(t, ok)
	ok, _ = l.IsAllowed("pypi", "requests", "2.28.0")
	assert.True(t, ok)
}

func TestList_EcosystemMismatch(t *testing.T) {
	l, _ := allow.New("")
	l.Add(allow.Entry{Ecosystem: "npm", Name: "lodash", Version: "4.17.21"})

	ok, _ := l.IsAllowed("pypi", "lodash", "4.17.21")
	assert.False(t, ok, "different ecosystem should not match")
}

func TestList_Persistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allowlist.json")

	l1, err := allow.New(path)
	require.NoError(t, err)
	require.NoError(t, l1.Add(allow.Entry{
		Ecosystem: "npm", Name: "once", Version: "1.4.0", Reason: "safe",
	}))

	// Load from same path in a new list
	l2, err := allow.New(path)
	require.NoError(t, err)
	ok, e := l2.IsAllowed("npm", "once", "1.4.0")
	assert.True(t, ok)
	assert.Equal(t, "safe", e.Reason)
	assert.False(t, e.AddedAt.IsZero(), "AddedAt should be set")
}

func TestList_Entries(t *testing.T) {
	l, _ := allow.New("")
	l.Add(allow.Entry{Ecosystem: "npm", Name: "a", Version: "1.0.0"})
	l.Add(allow.Entry{Ecosystem: "pypi", Name: "b", Version: "2.0.0"})
	assert.Len(t, l.Entries(), 2)
}

func TestList_NonexistentFile(t *testing.T) {
	l, err := allow.New("/tmp/no-such-file-escrow-test.json")
	require.NoError(t, err, "missing file should be treated as empty list")
	assert.Empty(t, l.Entries())
}

// Ensure time import is used.
var _ time.Time
