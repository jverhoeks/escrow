package trust

import (
	"context"
	"fmt"
)

// Engine runs each registered Signal against a package and collects the reports.
//
// Signals return SignalSkip only for not-applicable cases (e.g. an ecosystem
// they don't cover, or "first seen, no baseline yet") — those never block. A
// signal that genuinely couldn't run (network/timeout/parse failure) returns
// SignalError directly. The engine also maps any non-nil error, and any panic,
// to SignalError. SignalError lets the policy layer decide fail-open vs
// fail-closed via the strict_signals knob.
type Engine struct{ signals []Signal }

func NewEngine(signals ...Signal) *Engine { return &Engine{signals: signals} }

func (e *Engine) Check(ctx context.Context, pkg Package) (TrustResult, error) {
	result := TrustResult{Package: pkg}
	for _, s := range e.signals {
		// Run each signal in a closure so a panic in one signal is recovered and
		// surfaced as SignalError rather than aborting the whole evaluation.
		report := func() (rep SignalReport) {
			defer func() {
				if r := recover(); r != nil {
					rep = SignalReport{Signal: s.Name(), Result: SignalError, Reason: fmt.Sprintf("panic: %v", r)}
				}
			}()
			r, err := s.Check(ctx, pkg)
			if err != nil {
				return SignalReport{Signal: s.Name(), Result: SignalError, Reason: err.Error()}
			}
			return r
		}()
		result.Reports = append(result.Reports, report)
	}
	return result, nil
}
