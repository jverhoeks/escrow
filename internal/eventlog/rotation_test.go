package eventlog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Mirrors the egresslog rotation test: three sessions, the middle one compacts
// an already-compacted file. maxBytes (32 KiB) sits above the compacted-block
// size so compaction brings curBytes back under the cap (no storm).
func TestRotation_CompactsAndPreservesOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	const capN = 20
	pad := strings.Repeat("x", 256)

	writeBatch := func(l *Log, start, n int) {
		for i := start; i < start+n; i++ {
			l.Record(PackageEvent{Package: fmt.Sprintf("p%04d@1", i), Action: "allow", Reason: pad})
		}
	}

	l1, err := NewWithPath(capN, path)
	require.NoError(t, err)
	l1.maxBytes = 32 * 1024
	writeBatch(l1, 0, 300)
	require.NoError(t, l1.Close())

	l2, err := NewWithPath(capN, path)
	require.NoError(t, err)
	l2.maxBytes = 32 * 1024
	ev2 := l2.Events("")
	require.Len(t, ev2, capN)
	assert.Equal(t, "p0299@1", ev2[0].Package, "newest-first after reopen #1")
	writeBatch(l2, 300, 300)
	require.NoError(t, l2.Close())

	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Less(t, fi.Size(), int64(64*1024), "file stays bounded across sessions")

	l3, err := NewWithPath(capN, path)
	require.NoError(t, err)
	defer l3.Close()
	ev3 := l3.Events("")
	require.Len(t, ev3, capN)
	assert.Equal(t, "p0599@1", ev3[0].Package, "newest-first after reopen #2")
	assert.Equal(t, "p0580@1", ev3[capN-1].Package, "oldest of the kept window")
}
