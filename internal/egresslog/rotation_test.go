package egresslog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRotation_CompactsAndPreservesOrder compacts across THREE sessions — the
// middle session reopens an already-compacted file and compacts it AGAIN, which
// is where a double-reverse ordering bug compounds. maxBytes (32 KiB) is set
// comfortably above the compacted-block size (~20 events ≈ 7 KiB) so compaction
// actually brings curBytes back under the cap (no storm).
func TestRotation_CompactsAndPreservesOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "egress.jsonl")
	const capN = 20
	pad := strings.Repeat("x", 256) // each event ≈ 340 B

	writeBatch := func(l *Log, start, n int) {
		for i := start; i < start+n; i++ {
			l.Record(Event{Host: fmt.Sprintf("h%04d", i), Action: "allow", Reason: pad})
		}
	}

	// Session 1: write past the cap → at least one compaction.
	l1, err := NewWithPath(capN, path)
	require.NoError(t, err)
	l1.maxBytes = 32 * 1024
	writeBatch(l1, 0, 300)
	require.NoError(t, l1.Close())

	// Session 2: reopen the compacted file, write more → compact AGAIN.
	l2, err := NewWithPath(capN, path)
	require.NoError(t, err)
	l2.maxBytes = 32 * 1024
	ev2 := l2.Recent(0, "")
	require.Len(t, ev2, capN)
	assert.Equal(t, "h0299", ev2[0].Host, "newest-first after reopen #1")
	writeBatch(l2, 300, 300)
	require.NoError(t, l2.Close())

	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Less(t, fi.Size(), int64(64*1024), "file stays bounded across sessions")

	// Session 3: final reopen → order MUST still be newest-first, and the kept
	// window is exactly the last capN events (h0580..h0599).
	l3, err := NewWithPath(capN, path)
	require.NoError(t, err)
	defer l3.Close()
	ev3 := l3.Recent(0, "")
	require.Len(t, ev3, capN)
	assert.Equal(t, "h0599", ev3[0].Host, "newest-first after reopen #2")
	assert.Equal(t, "h0580", ev3[capN-1].Host, "oldest of the kept window")
}

func TestNewWithPath_CreatesDirAndMode0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "nested", "egress.jsonl")
	l, err := NewWithPath(10, path)
	require.NoError(t, err, "should auto-create parent dirs")
	defer l.Close()
	l.Record(Event{Host: "h", Action: "allow"})
	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
}
