package trust

import "context"

// Engine runs each registered Signal against a package and collects the reports.
//
// Signals are expected to handle their own "couldn't determine" cases by
// returning SignalSkip with nil error. The engine-level error branch below is
// for unexpected failures the signal didn't catch (panics recovered to error,
// internal bugs). Those are surfaced as SignalError so the policy layer can
// decide fail-open vs fail-closed via the strict_signals knob.
type Engine struct{ signals []Signal }

func NewEngine(signals ...Signal) *Engine { return &Engine{signals: signals} }

func (e *Engine) Check(ctx context.Context, pkg Package) (TrustResult, error) {
	result := TrustResult{Package: pkg}
	for _, s := range e.signals {
		report, err := s.Check(ctx, pkg)
		if err != nil {
			report = SignalReport{Signal: s.Name(), Result: SignalError, Reason: err.Error()}
		}
		result.Reports = append(result.Reports, report)
	}
	return result, nil
}
