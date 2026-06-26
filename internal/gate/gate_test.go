package gate_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jverhoeks/escrow/internal/block"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/jverhoeks/escrow/internal/gate"
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/trust"
)

func TestCheck_BlockedVersionReturnsBlockAndRecordsDownloadEvent(t *testing.T) {
	bl, err := block.New("")
	require.NoError(t, err)
	require.NoError(t, bl.Add(block.Entry{Ecosystem: "npm", Name: "evil", Version: "1.0.0", Reason: "malware"}))
	pol := policy.New(nil).WithBlockList(bl)
	eng := trust.NewEngine() // no signals — blocklist is what matters here
	ev := eventlog.New(10)

	pkg := trust.Package{Ecosystem: trust.EcosystemNPM, Name: "evil", Version: "1.0.0"}
	d := gate.Check(context.Background(), eng, pol, ev, pkg)

	require.Equal(t, policy.ActionBlock, d.Action)
	events := ev.Events("")
	require.Len(t, events, 1, "exactly one event per serve attempt")
	assert.Equal(t, "block", events[0].Action)
	assert.Equal(t, eventlog.KindDownloaded, events[0].Kind)
	assert.Equal(t, "evil@1.0.0", events[0].Package)
	assert.Equal(t, "npm", events[0].Ecosystem)
}

func TestCheck_CleanVersionAllowsAndRecordsNoEvent(t *testing.T) {
	// The gate records only the block side; the caller records the
	// successful-download event after serving. So an allow leaves the log empty.
	pol := policy.New(nil) // no lists, no signals configured
	eng := trust.NewEngine()
	ev := eventlog.New(10)

	pkg := trust.Package{Ecosystem: trust.EcosystemPyPI, Name: "safe", Version: "2.0.0"}
	d := gate.Check(context.Background(), eng, pol, ev, pkg)

	require.Equal(t, policy.ActionAllow, d.Action)
	assert.Empty(t, ev.Events(""), "gate must not record an event on allow")
}

func TestCheck_NilEventLogDoesNotPanic(t *testing.T) {
	pol := policy.New(nil)
	eng := trust.NewEngine()
	pkg := trust.Package{Ecosystem: trust.EcosystemNPM, Name: "x", Version: "1.0.0"}
	d := gate.Check(context.Background(), eng, pol, nil, pkg)
	require.Equal(t, policy.ActionAllow, d.Action)
}
