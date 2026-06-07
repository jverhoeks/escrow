package trust_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/trust"
)

// TestAgeSignal_ZeroPublishedAt verifies that a package with unknown publish time
// (zero time.Time) surfaces as SignalError rather than silently passing. A zero
// publish time means the signal could not run (we don't know how old the package
// is), so the policy layer's strict_signals knob must decide fail-open vs
// fail-closed — not the age gate. Previously this returned SignalPass (the huge
// computed age cleared minDays), which silently failed open even under
// strict_signals=block.
func TestAgeSignal_ZeroPublishedAt(t *testing.T) {
	sig := trust.NewAgeSignal(7, nil)
	pkg := trust.Package{
		Ecosystem:   trust.EcosystemNPM,
		Name:        "unknown-age-pkg",
		Version:     "1.0.0",
		PublishedAt: time.Time{}, // zero value — publish time unknown
	}
	report, err := sig.Check(context.Background(), pkg)
	require.NoError(t, err)
	assert.Equal(t, trust.SignalError, report.Result,
		"zero publish time should surface as SignalError so strict_signals can decide")
}

// TestAgeSignal_Boundary verifies exact boundary: package published exactly min_days ago passes.
func TestAgeSignal_Boundary(t *testing.T) {
	now := time.Now()
	sig := trust.NewAgeSignal(7, func() time.Time { return now })
	// Exactly 7 days old — should pass
	pkg := trust.Package{
		Ecosystem:   trust.EcosystemNPM,
		Name:        "boundary-pkg",
		Version:     "1.0.0",
		PublishedAt: now.Add(-7 * 24 * time.Hour),
	}
	report, err := sig.Check(context.Background(), pkg)
	require.NoError(t, err)
	assert.Equal(t, trust.SignalPass, report.Result, "package exactly min_days old should pass")
}
