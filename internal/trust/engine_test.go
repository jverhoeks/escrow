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
