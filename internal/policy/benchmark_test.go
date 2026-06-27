package policy_test

import (
	"testing"

	"github.com/jverhoeks/escrow/internal/config"
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/trust"
)

func benchPolicyResult(reports ...trust.SignalReport) trust.TrustResult {
	return trust.TrustResult{
		Package: trust.Package{Ecosystem: trust.EcosystemNPM, Name: "lodash", Version: "4.17.21"},
		Reports: reports,
	}
}

func BenchmarkEvaluate_Allow(b *testing.B) {
	e := policy.New(&config.PolicyConfig{
		Age:       &config.AgePolicyConfig{MinDays: 7, Action: "block"},
		OSV:       &config.OSVPolicyConfig{MinSeverity: "MEDIUM", Action: "block"},
		Publisher: &config.PublisherPolicyConfig{MaxAccountAgeDays: 30, Action: "warn"},
	})
	result := benchPolicyResult(
		trust.SignalReport{Signal: "age", Result: trust.SignalPass},
		trust.SignalReport{Signal: "osv", Result: trust.SignalPass},
		trust.SignalReport{Signal: "publisher", Result: trust.SignalPass},
		trust.SignalReport{Signal: "popularity", Result: trust.SignalSkip},
	)
	b.ResetTimer()
	for range b.N {
		e.Evaluate(result)
	}
}

func BenchmarkEvaluate_BlockByOSV(b *testing.B) {
	e := policy.New(&config.PolicyConfig{
		Age:       &config.AgePolicyConfig{MinDays: 7, Action: "block"},
		OSV:       &config.OSVPolicyConfig{MinSeverity: "MEDIUM", Action: "block"},
		Publisher: &config.PublisherPolicyConfig{MaxAccountAgeDays: 30, Action: "warn"},
		StrictSignals: "block",
	})
	result := benchPolicyResult(
		trust.SignalReport{Signal: "age", Result: trust.SignalPass},
		trust.SignalReport{Signal: "osv", Result: trust.SignalFail, Reason: "CVE-2024-0001"},
	)
	b.ResetTimer()
	for range b.N {
		e.Evaluate(result)
	}
}

func BenchmarkEvaluate_AllowListOverride(b *testing.B) {
	e := policy.New(&config.PolicyConfig{
		OSV: &config.OSVPolicyConfig{MinSeverity: "MEDIUM", Action: "block"},
		StrictSignals: "block",
	})
	result := benchPolicyResult(
		trust.SignalReport{Signal: "osv", Result: trust.SignalFail, Reason: "CVE-2024-0001"},
	)
	b.ResetTimer()
	for range b.N {
		e.Evaluate(result)
	}
}
