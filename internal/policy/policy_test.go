package policy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/cache"
	"github.com/jverhoeks/escrow/internal/config"
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/trust"
)

func makeResult(reports ...trust.SignalReport) trust.TrustResult {
	return trust.TrustResult{Reports: reports}
}

func TestEvaluate_BlockDecisionCarriesVulns(t *testing.T) {
	cfg := &config.PolicyConfig{OSV: &config.OSVPolicyConfig{Action: "block"}}
	e := policy.New(cfg)
	result := trust.TrustResult{
		Package: trust.Package{Ecosystem: trust.EcosystemNPM, Name: "x", Version: "1.0.0"},
		Reports: []trust.SignalReport{{
			Signal: "osv", Result: trust.SignalFail, Reason: "1 vuln",
			Vulns: []trust.Vuln{{ID: "GHSA-aaaa", Severity: "CRITICAL"}},
		}},
	}
	d := e.Evaluate(result)
	require.Equal(t, policy.ActionBlock, d.Action)
	require.Len(t, d.Vulns, 1)
	require.Equal(t, "GHSA-aaaa", d.Vulns[0].ID)
}

func TestPolicy_NoConfig_Allows(t *testing.T) {
	eng := policy.New(nil)
	result := makeResult(trust.SignalReport{Signal: "age", Result: trust.SignalFail, Reason: "too new"})
	d := eng.Evaluate(result)
	assert.Equal(t, policy.ActionAllow, d.Action)
}

func TestPolicy_AgeBlock(t *testing.T) {
	eng := policy.New(&config.PolicyConfig{
		Age: &config.AgePolicyConfig{MinDays: 7, Action: "block"},
	})
	result := makeResult(trust.SignalReport{Signal: "age", Result: trust.SignalFail, Reason: "3 days old"})
	d := eng.Evaluate(result)
	assert.Equal(t, policy.ActionBlock, d.Action)
	assert.Equal(t, "age", d.Signal)
}

func TestPolicy_OSVBlock(t *testing.T) {
	eng := policy.New(&config.PolicyConfig{
		OSV: &config.OSVPolicyConfig{MinSeverity: "MEDIUM", Action: "block"},
	})
	result := makeResult(trust.SignalReport{Signal: "osv", Result: trust.SignalFail, Reason: "CVE-2024-1234"})
	d := eng.Evaluate(result)
	assert.Equal(t, policy.ActionBlock, d.Action)
}

func TestPolicy_PublisherWarn(t *testing.T) {
	eng := policy.New(&config.PolicyConfig{
		Publisher: &config.PublisherPolicyConfig{MaxAccountAgeDays: 30, Action: "warn"},
	})
	result := makeResult(
		trust.SignalReport{Signal: "age", Result: trust.SignalPass},
		trust.SignalReport{Signal: "publisher", Result: trust.SignalWarn, Reason: "new account"},
	)
	d := eng.Evaluate(result)
	assert.Equal(t, policy.ActionWarn, d.Action)
}

func TestPolicy_BlockBeatsWarn(t *testing.T) {
	eng := policy.New(&config.PolicyConfig{
		Age:       &config.AgePolicyConfig{Action: "block"},
		Publisher: &config.PublisherPolicyConfig{Action: "warn"},
	})
	result := makeResult(
		trust.SignalReport{Signal: "age", Result: trust.SignalFail},
		trust.SignalReport{Signal: "publisher", Result: trust.SignalWarn},
	)
	d := eng.Evaluate(result)
	assert.Equal(t, policy.ActionBlock, d.Action, "block takes priority over warn")
}

func TestPolicy_StrictSignals_DefaultFailsOpen(t *testing.T) {
	eng := policy.New(&config.PolicyConfig{})
	result := makeResult(trust.SignalReport{Signal: "osv", Result: trust.SignalError, Reason: "boom"})
	d := eng.Evaluate(result)
	assert.Equal(t, policy.ActionAllow, d.Action, "unset strict_signals must preserve fail-open behavior")
}

func TestPolicy_StrictSignals_BlockFailsClosed(t *testing.T) {
	eng := policy.New(&config.PolicyConfig{StrictSignals: "block"})
	result := makeResult(trust.SignalReport{Signal: "osv", Result: trust.SignalError, Reason: "network down"})
	d := eng.Evaluate(result)
	assert.Equal(t, policy.ActionBlock, d.Action)
	assert.Equal(t, "osv", d.Signal)
}

func TestPolicy_StrictSignals_Warn(t *testing.T) {
	eng := policy.New(&config.PolicyConfig{StrictSignals: "warn"})
	result := makeResult(trust.SignalReport{Signal: "publisher", Result: trust.SignalError, Reason: "5xx"})
	d := eng.Evaluate(result)
	assert.Equal(t, policy.ActionWarn, d.Action)
}

// TestStrictSignals_OSVTransientFailure_EndToEnd is the real regression guard
// for issue #12: it runs the actual OSV signal against an upstream returning 500
// (a transient failure), feeds the resulting report through the policy engine,
// and asserts the fail-closed / fail-open behavior end-to-end.
//
// Before the fix the OSV signal returned SignalSkip on a 500, so strict_signals
// never fired and the BLOCK assertion failed (silently failed open). The default
// (unset/"allow") assertion is a behavior pin — it passes before and after, which
// is the point: default users see no change.
func TestStrictSignals_OSVTransientFailure_EndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	osv := trust.NewOSVSignal("MEDIUM", srv.Client(), cache.NewMemory(), srv.URL)
	rep, err := osv.Check(context.Background(), trust.Package{
		Ecosystem: trust.EcosystemNPM, Name: "x", Version: "1.0.0",
	})
	require.NoError(t, err)
	require.Equal(t, trust.SignalError, rep.Result,
		"OSV transient (500) failure must surface as SignalError")

	result := trust.TrustResult{
		Package: trust.Package{Ecosystem: trust.EcosystemNPM, Name: "x", Version: "1.0.0"},
		Reports: []trust.SignalReport{rep},
	}

	// strict_signals=block → fail closed (this is what was broken).
	blocked := policy.New(&config.PolicyConfig{StrictSignals: "block"}).Evaluate(result)
	assert.Equal(t, policy.ActionBlock, blocked.Action,
		"strict_signals=block must fail closed when a signal errors")
	assert.Equal(t, "osv", blocked.Signal)

	// strict_signals unset (default "allow") → fail open. Behavior pin: default
	// users are unaffected; an OSV outage still allows the install.
	allowed := policy.New(&config.PolicyConfig{}).Evaluate(result)
	assert.Equal(t, policy.ActionAllow, allowed.Action,
		"default strict_signals must preserve fail-open behavior (regression guard)")
}

func TestEngine_SetConfig_AppliesLive(t *testing.T) {
	e := policy.New(&config.PolicyConfig{OSV: &config.OSVPolicyConfig{Action: "warn"}})
	res := trust.TrustResult{
		Package: trust.Package{Ecosystem: trust.EcosystemNPM, Name: "x", Version: "1.0.0"},
		Reports: []trust.SignalReport{{Signal: "osv", Result: trust.SignalFail, Reason: "v"}},
	}
	require.Equal(t, policy.ActionWarn, e.Evaluate(res).Action)

	e.SetConfig(&config.PolicyConfig{OSV: &config.OSVPolicyConfig{Action: "block"}})
	require.Equal(t, policy.ActionBlock, e.Evaluate(res).Action)
}
