package egresslog

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ev(host, action string) Event {
	return Event{Timestamp: time.Now(), Host: host, Action: action, Verb: "CONNECT", Reason: "tunnel"}
}

func TestLog_RecordRecentFilter(t *testing.T) {
	l := New(3)
	l.Record(ev("a.com", "allow"))
	l.Record(ev("b.com", "block"))
	l.Record(ev("c.com", "allow"))
	l.Record(ev("d.com", "allow")) // evicts a.com (cap 3, newest-first)

	all := l.Recent(10, "")
	require.Len(t, all, 3)
	assert.Equal(t, "d.com", all[0].Host) // newest first
	allowed := l.Recent(10, "allow")
	assert.Len(t, allowed, 2) // c, d
	for _, e := range allowed {
		assert.Equal(t, "allow", e.Action)
	}
}

func TestLog_Subscribe(t *testing.T) {
	l := New(10)
	ch, unsub := l.Subscribe()
	require.NotNil(t, ch)
	defer unsub()
	l.Record(ev("x.com", "block"))
	select {
	case e := <-ch:
		assert.Equal(t, "x.com", e.Host)
	case <-time.After(time.Second):
		t.Fatal("no event delivered to subscriber")
	}
}

func TestLog_StatsAndBytes(t *testing.T) {
	l := New(100)
	l.Record(ev("a.com", "allow"))
	l.Record(ev("a.com", "allow"))
	l.Record(ev("evil.com", "block"))
	l.AddBytes(100)
	l.AddBytes(50)
	s := l.Stats(24*time.Hour, time.Hour)
	assert.Equal(t, 3, s.Total)
	assert.Equal(t, 2, s.Allowed)
	assert.Equal(t, 1, s.Blocked)
	assert.Equal(t, 2, s.DistinctHosts)
	assert.Equal(t, int64(150), s.Bytes)
	require.NotEmpty(t, s.TopAllowed)
	assert.Equal(t, "a.com", s.TopAllowed[0].Host)
	assert.Equal(t, 2, s.TopAllowed[0].Count)
	require.NotEmpty(t, s.TopBlocked)
	assert.Equal(t, "evil.com", s.TopBlocked[0].Host)
}

func TestNewWithPath_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/egress.jsonl"
	l, err := NewWithPath(5, path)
	require.NoError(t, err)
	l.Record(ev("a.com", "allow"))
	l.Record(ev("b.com", "block"))
	// reopen: events reload newest-first
	l2, err := NewWithPath(5, path)
	require.NoError(t, err)
	got := l2.Recent(10, "")
	require.Len(t, got, 2)
	assert.Equal(t, "b.com", got[0].Host)
}
