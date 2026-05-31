package accesslog_test

import (
	"testing"

	"github.com/jverhoeks/escrow/internal/accesslog"
	"github.com/stretchr/testify/require"
)

func TestLog_RecordCapAndRecentNewestFirst(t *testing.T) {
	l := accesslog.New(2)
	l.Record(accesslog.Entry{Path: "/npm/a", Status: 200})
	l.Record(accesslog.Entry{Path: "/npm/b", Status: 404})
	l.Record(accesslog.Entry{Path: "/npm/c", Status: 200})

	all := l.Recent(0)
	require.Len(t, all, 2) // capacity 2, newest-first
	require.Equal(t, "/npm/c", all[0].Path)
	require.Equal(t, "/npm/b", all[1].Path)

	// n larger than stored returns all.
	require.Len(t, l.Recent(10), 2)
	// n smaller than stored truncates to the newest n.
	one := l.Recent(1)
	require.Len(t, one, 1)
	require.Equal(t, "/npm/c", one[0].Path)
}

func TestLog_RecordDefaultsTimestamp(t *testing.T) {
	l := accesslog.New(1)
	l.Record(accesslog.Entry{Path: "/npm/a"})
	got := l.Recent(1)
	require.Len(t, got, 1)
	require.False(t, got[0].Timestamp.IsZero())
}
