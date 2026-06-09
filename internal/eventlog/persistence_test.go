package eventlog_test

import (
	"path/filepath"
	"strings"
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

func TestLog_PersistenceKindRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")

	l1, err := eventlog.NewWithPath(10, path)
	require.NoError(t, err)
	l1.Record(eventlog.PackageEvent{Ecosystem: "npm", Package: "lodash@1.0.0", Action: "block", Kind: eventlog.KindScanned})
	l1.Record(eventlog.PackageEvent{Ecosystem: "npm", Package: "lodash@1.0.0", Action: "allow", Kind: eventlog.KindDownloaded})
	require.NoError(t, l1.Close())

	l2, err := eventlog.NewWithPath(10, path)
	require.NoError(t, err)
	defer l2.Close()

	events := l2.Events("")
	require.Len(t, events, 2)
	// Newest first.
	assert.Equal(t, eventlog.KindDownloaded, events[0].Kind)
	assert.Equal(t, eventlog.KindScanned, events[1].Kind)
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

// TestLog_PersistenceLoadsLinesOverDefaultScannerBuffer is a regression test:
// a single event whose JSONL line exceeds bufio.Scanner's default 64 KiB token
// limit (e.g. a large Vulns/Reason payload) must NOT stop the load early and
// silently drop the newer events recorded after it. The loader uses a 1 MiB
// scanner buffer, so both events round-trip.
func TestLog_PersistenceLoadsLinesOverDefaultScannerBuffer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")

	l1, err := eventlog.NewWithPath(10, path)
	require.NoError(t, err)
	bigReason := strings.Repeat("x", 100*1024) // ~100 KiB line, > default 64 KiB
	l1.Record(eventlog.PackageEvent{Ecosystem: "npm", Package: "big@1", Action: "block", Reason: bigReason})
	l1.Record(eventlog.PackageEvent{Ecosystem: "npm", Package: "newer@1", Action: "allow"})
	require.NoError(t, l1.Close())

	l2, err := eventlog.NewWithPath(10, path)
	require.NoError(t, err)
	defer l2.Close()

	events := l2.Events("")
	require.Len(t, events, 2, "both events must load despite the >64 KiB line")
	assert.Equal(t, "newer@1", events[0].Package, "event recorded after the big line must survive (newest-first)")
	assert.Equal(t, "big@1", events[1].Package)
}
