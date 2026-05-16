package trust

import "context"

type Engine struct{ signals []Signal }

func NewEngine(signals ...Signal) *Engine { return &Engine{signals: signals} }

func (e *Engine) Check(ctx context.Context, pkg Package) (TrustResult, error) {
	result := TrustResult{Package: pkg}
	for _, s := range e.signals {
		report, err := s.Check(ctx, pkg)
		if err != nil {
			report = SignalReport{Signal: s.Name(), Result: SignalSkip, Reason: err.Error()}
		}
		result.Reports = append(result.Reports, report)
	}
	return result, nil
}
