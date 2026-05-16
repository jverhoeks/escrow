package trust_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/trust"
)

func TestAgeSignal_TooNew(t *testing.T) {
	now := time.Now()
	sig := trust.NewAgeSignal(7, func() time.Time { return now })
	pkg := trust.Package{
		Ecosystem:   trust.EcosystemNPM,
		Name:        "lodash",
		Version:     "4.17.22",
		PublishedAt: now.Add(-3 * 24 * time.Hour),
	}
	report, err := sig.Check(context.Background(), pkg)
	require.NoError(t, err)
	assert.Equal(t, trust.SignalFail, report.Result)
	assert.Contains(t, report.Reason, "3 day")
	assert.Contains(t, report.Reason, "minimum: 7")
}

func TestAgeSignal_OldEnough(t *testing.T) {
	now := time.Now()
	sig := trust.NewAgeSignal(7, func() time.Time { return now })
	pkg := trust.Package{PublishedAt: now.Add(-10 * 24 * time.Hour)}
	report, err := sig.Check(context.Background(), pkg)
	require.NoError(t, err)
	assert.Equal(t, trust.SignalPass, report.Result)
}

func TestAgeSignal_ExactBoundary(t *testing.T) {
	now := time.Now()
	sig := trust.NewAgeSignal(7, func() time.Time { return now })
	pkg := trust.Package{PublishedAt: now.Add(-7 * 24 * time.Hour)}
	report, err := sig.Check(context.Background(), pkg)
	require.NoError(t, err)
	assert.Equal(t, trust.SignalPass, report.Result)
}
