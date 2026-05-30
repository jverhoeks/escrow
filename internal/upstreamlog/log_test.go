package upstreamlog_test

import (
	"testing"

	"github.com/jverhoeks/escrow/internal/upstreamlog"
	"github.com/stretchr/testify/require"
)

func TestLog_RecordAndEventsNewestFirst(t *testing.T) {
	l := upstreamlog.New(2)
	l.Record(upstreamlog.Event{Ecosystem: "npm", URL: "https://registry.npmjs.org/a", Status: 200})
	l.Record(upstreamlog.Event{Ecosystem: "pypi", URL: "https://pypi.org/b", Status: 404})
	l.Record(upstreamlog.Event{Ecosystem: "cargo", URL: "https://crates.io/c", Status: 200})

	all := l.Events("")
	require.Len(t, all, 2) // capacity 2, newest-first
	require.Equal(t, "cargo", all[0].Ecosystem)
	require.Equal(t, "pypi", all[1].Ecosystem)

	require.Len(t, l.Events("pypi"), 1)
}
