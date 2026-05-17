package eventlog_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/eventlog"
)

func TestLog_PersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")

	l1, err := eventlog.NewWithPath(10, path)
	require.NoError(t, err)
	l1.Record(eventlog.PackageEvent{Ecosystem: "npm", Package: "lodash@1.0.0", Action: "block"})
	l1.Record(eventlog.PackageEvent{Ecosystem: "pypi", Package: "requests@2.0.0", Action: "allow"})
	require.NoError(t, l1.Close())

	l2, err := eventlog.NewWithPath(10, path)
	require.NoError(t, err)
	defer l2.Close()

	events := l2.Events("")
	require.Len(t, events, 2)
	// Newest first
	assert.Equal(t, "requests@2.0.0", events[0].Package)
	assert.Equal(t, "lodash@1.0.0", events[1].Package)
}

func TestLog_PersistenceCapEnforced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	const cap = 3

	l1, err := eventlog.NewWithPath(cap, path)
	require.NoError(t, err)
	for i := 0; i < cap+5; i++ {
		l1.Record(eventlog.PackageEvent{Package: "pkg", Action: "allow"})
	}
	require.NoError(t, l1.Close())

	l2, err := eventlog.NewWithPath(cap, path)
	require.NoError(t, err)
	defer l2.Close()

	assert.Len(t, l2.Events(""), cap, "loaded events should be capped")
}

func TestLog_PersistenceAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")

	l1, err := eventlog.NewWithPath(10, path)
	require.NoError(t, err)
	l1.Record(eventlog.PackageEvent{Package: "a@1", Action: "block"})
	require.NoError(t, l1.Close())

	l2, err := eventlog.NewWithPath(10, path)
	require.NoError(t, err)
	l2.Record(eventlog.PackageEvent{Package: "b@1", Action: "allow"})
	require.NoError(t, l2.Close())

	l3, err := eventlog.NewWithPath(10, path)
	require.NoError(t, err)
	defer l3.Close()

	events := l3.Events("")
	require.Len(t, events, 2, "both sessions' events should be loaded")
}

func TestLog_PersistenceAutoCreatesDirectory(t *testing.T) {
	// NewWithPath should create parent directories if they don't exist.
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "nested", "events.jsonl")

	l, err := eventlog.NewWithPath(10, path)
	require.NoError(t, err, "NewWithPath should auto-create parent directories")
	defer l.Close()

	l.Record(eventlog.PackageEvent{Package: "x@1", Action: "allow"})
	assert.Len(t, l.Events(""), 1)
}

func TestLog_PersistenceEmptyPath(t *testing.T) {
	l, err := eventlog.NewWithPath(10, "")
	require.NoError(t, err)
	l.Record(eventlog.PackageEvent{Package: "a", Action: "allow"})
	assert.Len(t, l.Events(""), 1)
	assert.NoError(t, l.Close()) // Close on in-memory log should be a no-op
}
