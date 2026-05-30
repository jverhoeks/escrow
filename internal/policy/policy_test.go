package policy_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/jverhoeks/escrow/internal/config"
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/trust"
)

func makeResult(reports ...trust.SignalReport) trust.TrustResult {
	return trust.TrustResult{Reports: reports}
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
