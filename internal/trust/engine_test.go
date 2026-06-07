package trust_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/trust"
)

type stubSignal struct {
	name   string
	report trust.SignalReport
}

func (s *stubSignal) Name() string { return s.name }
func (s *stubSignal) Check(_ context.Context, _ trust.Package) (trust.SignalReport, error) {
	return s.report, nil
}

func TestEngine_RunsAllSignals(t *testing.T) {
	sig1 := &stubSignal{"age", trust.SignalReport{Signal: "age", Result: trust.SignalPass}}
	sig2 := &stubSignal{"osv", trust.SignalReport{Signal: "osv", Result: trust.SignalFail, Reason: "CVE-2024-1234"}}
	engine := trust.NewEngine(sig1, sig2)
	result, err := engine.Check(context.Background(), trust.Package{Name: "pkg", Version: "1.0.0"})
	require.NoError(t, err)
	assert.Len(t, result.Reports, 2)
	assert.Equal(t, trust.SignalPass, result.Reports[0].Result)
	assert.Equal(t, trust.SignalFail, result.Reports[1].Result)
}

// panicSignal is a fake signal whose Check panics, used to verify the engine
// recovers and surfaces the failure as SignalError instead of crashing.
type panicSignal struct{ name string }

func (s *panicSignal) Name() string { return s.name }
func (s *panicSignal) Check(_ context.Context, _ trust.Package) (trust.SignalReport, error) {
	panic("signal blew up")
}

// TestEngine_RecoversPanicAsError verifies that a panicking signal does not
// crash the engine: the panic is recovered and reported as SignalError, and the
// other signals still run.
func TestEngine_RecoversPanicAsError(t *testing.T) {
	boom := &panicSignal{"boom"}
	ok := &stubSignal{"age", trust.SignalReport{Signal: "age", Result: trust.SignalPass}}
	engine := trust.NewEngine(boom, ok)

	result, err := engine.Check(context.Background(), trust.Package{Name: "pkg", Version: "1.0.0"})
	require.NoError(t, err)
	require.Len(t, result.Reports, 2, "a panicking signal must not abort the remaining signals")
	assert.Equal(t, "boom", result.Reports[0].Signal)
	assert.Equal(t, trust.SignalError, result.Reports[0].Result,
		"a panicking signal should be surfaced as SignalError")
	assert.Contains(t, result.Reports[0].Reason, "panic")
	assert.Equal(t, trust.SignalPass, result.Reports[1].Result,
		"signals after the panic should still run")
}
