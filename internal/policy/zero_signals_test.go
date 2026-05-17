package policy_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/allow"
	"github.com/jverhoeks/escrow/internal/block"
	"github.com/jverhoeks/escrow/internal/config"
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/trust"
)

// TestPolicy_ZeroReports verifies that a non-nil policy config with no signal reports
// returns Allow (fail-open when no signals ran).
func TestPolicy_ZeroReports(t *testing.T) {
	pol := policy.New(&config.PolicyConfig{
		Age: &config.AgePolicyConfig{MinDays: 7, Action: "block"},
	})
	result := trust.TrustResult{
		Package: trust.Package{Name: "test", Version: "1.0.0"},
		Reports: nil,
	}
	d := pol.Evaluate(result)
	assert.Equal(t, policy.ActionAllow, d.Action,
		"zero signal reports should allow the package through")
}

// TestPolicy_AllSignalsSkip verifies fail-open: all APIs unavailable → package allowed.
func TestPolicy_AllSignalsSkip(t *testing.T) {
	pol := policy.New(&config.PolicyConfig{
		Age: &config.AgePolicyConfig{MinDays: 7, Action: "block"},
		OSV: &config.OSVPolicyConfig{MinSeverity: "MEDIUM", Action: "block"},
	})
	result := trust.TrustResult{
		Package: trust.Package{Name: "test", Version: "1.0.0"},
		Reports: []trust.SignalReport{
			{Signal: "age", Result: trust.SignalSkip, Reason: "publish time unknown"},
			{Signal: "osv", Result: trust.SignalSkip, Reason: "OSV API unavailable"},
		},
	}
	d := pol.Evaluate(result)
	assert.Equal(t, policy.ActionAllow, d.Action,
		"all signals skipping should allow the package (fail-open)")
}

// TestPolicy_AllowlistWildcardBeatsBlocklist verifies that a wildcard allowlist entry
// (empty Version) short-circuits the blocklist — allowlist always has priority.
func TestPolicy_AllowlistWildcardBeatsBlocklist(t *testing.T) {
	al, err := allow.New("")
	require.NoError(t, err)
	require.NoError(t, al.Add(allow.Entry{
		Ecosystem: "npm", Name: "lodash", Version: "", Reason: "approved all versions",
	}))

	bl, err := block.New("")
	require.NoError(t, err)
	require.NoError(t, bl.Add(block.Entry{
		Ecosystem: "npm", Name: "lodash", Version: "4.17.21", Reason: "manual block",
	}))

	pol := policy.New(&config.PolicyConfig{
		Age: &config.AgePolicyConfig{MinDays: 7, Action: "block"},
	})
	pol.WithAllowList(al)
	pol.WithBlockList(bl)

	result := trust.TrustResult{
		Package: trust.Package{Ecosystem: trust.EcosystemNPM, Name: "lodash", Version: "4.17.21"},
		Reports: []trust.SignalReport{
			{Signal: "age", Result: trust.SignalFail, Reason: "published 0 days ago"},
		},
	}
	d := pol.Evaluate(result)
	assert.Equal(t, policy.ActionAllow, d.Action,
		"wildcard allowlist entry should bypass both blocklist and trust signals")
	assert.Equal(t, "override", d.Signal)
}
