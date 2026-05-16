package eventlog_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/eventlog"
)

func TestLog_RecordAndRetrieve(t *testing.T) {
	l := eventlog.New(10)
	l.Record(eventlog.PackageEvent{Ecosystem: "npm", Package: "lodash@4.17.21", Action: "block", Signal: "osv", Reason: "CVE"})
	l.Record(eventlog.PackageEvent{Ecosystem: "pypi", Package: "requests@2.31.0", Action: "allow"})
	events := l.Events("")
	require.Len(t, events, 2)
	assert.Equal(t, "requests@2.31.0", events[0].Package, "newest first")
}

func TestLog_FilterByEcosystem(t *testing.T) {
	l := eventlog.New(10)
	l.Record(eventlog.PackageEvent{Ecosystem: "npm", Package: "a"})
	l.Record(eventlog.PackageEvent{Ecosystem: "pypi", Package: "b"})
	l.Record(eventlog.PackageEvent{Ecosystem: "npm", Package: "c"})
	assert.Len(t, l.Events("npm"), 2)
	assert.Len(t, l.Events("pypi"), 1)
}

func TestLog_RingBufferCap(t *testing.T) {
	l := eventlog.New(3)
	for i := 0; i < 5; i++ {
		l.Record(eventlog.PackageEvent{Package: fmt.Sprintf("pkg-%d", i)})
	}
	events := l.Events("")
	assert.Len(t, events, 3)
	assert.Equal(t, "pkg-4", events[0].Package, "newest first")
}

func TestLog_Subscribe(t *testing.T) {
	l := eventlog.New(10)
	ch, unsub := l.Subscribe()
	defer unsub()
	go func() {
		time.Sleep(10 * time.Millisecond)
		l.Record(eventlog.PackageEvent{Package: "test@1.0.0", Action: "block"})
	}()
	select {
	case e := <-ch:
		assert.Equal(t, "test@1.0.0", e.Package)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout waiting for event")
	}
}

func TestLog_Stats(t *testing.T) {
	l := eventlog.New(10)
	l.Record(eventlog.PackageEvent{Package: "lodash@1", Action: "block"})
	l.Record(eventlog.PackageEvent{Package: "lodash@2", Action: "block"})
	l.Record(eventlog.PackageEvent{Package: "axios@1", Action: "block"})
	l.Record(eventlog.PackageEvent{Package: "once@1", Action: "allow"})
	l.Record(eventlog.PackageEvent{Package: "ms@1", Action: "warn"})
	s := l.Stats()
	assert.Equal(t, 3, s.Blocked)
	assert.Equal(t, 1, s.Warned)
	assert.Equal(t, 1, s.Allowed)
	require.NotEmpty(t, s.TopBlocked)
	assert.Equal(t, "lodash", s.TopBlocked[0].Package)
	assert.Equal(t, 2, s.TopBlocked[0].Count)
}
