package policy

import (
	"github.com/jverhoeks/escrow/internal/allow"
	"github.com/jverhoeks/escrow/internal/config"
	"github.com/jverhoeks/escrow/internal/trust"
)

type Action string

const (
	ActionBlock Action = "block"
	ActionWarn  Action = "warn"
	ActionAllow Action = "allow"
)

type Decision struct {
	Action Action
	Signal string
	Reason string
}

type Engine struct {
	cfg       *config.PolicyConfig
	allowList *allow.List // may be nil
}

func New(cfg *config.PolicyConfig) *Engine { return &Engine{cfg: cfg} }

// WithAllowList sets the allowlist on the engine and returns the engine for chaining.
func (e *Engine) WithAllowList(l *allow.List) *Engine {
	e.allowList = l
	return e
}

func (e *Engine) Evaluate(result trust.TrustResult) Decision {
	if e.allowList != nil {
		if ok, entry := e.allowList.IsAllowed(
			string(result.Package.Ecosystem),
			result.Package.Name,
			result.Package.Version,
		); ok {
			return Decision{
				Action: ActionAllow,
				Signal: "override",
				Reason: "allowlist: " + entry.Reason,
			}
		}
	}
	if e.cfg == nil {
		return Decision{Action: ActionAllow}
	}
	var warns []Decision
	for _, r := range result.Reports {
		if r.Result == trust.SignalPass || r.Result == trust.SignalSkip {
			continue
		}
		a := e.actionFor(r)
		d := Decision{Action: a, Signal: r.Signal, Reason: r.Reason}
		if a == ActionBlock {
			return d
		}
		if a == ActionWarn {
			warns = append(warns, d)
		}
	}
	if len(warns) > 0 {
		return warns[0]
	}
	return Decision{Action: ActionAllow}
}

func cfgAction(s string) Action {
	switch s {
	case "block":
		return ActionBlock
	case "warn":
		return ActionWarn
	default:
		return ActionAllow
	}
}

func (e *Engine) actionFor(r trust.SignalReport) Action {
	switch r.Signal {
	case "age":
		if e.cfg.Age != nil && r.Result == trust.SignalFail {
			return cfgAction(e.cfg.Age.Action)
		}
	case "osv":
		if e.cfg.OSV != nil && r.Result == trust.SignalFail {
			return cfgAction(e.cfg.OSV.Action)
		}
	case "publisher":
		if e.cfg.Publisher != nil && r.Result == trust.SignalWarn {
			return cfgAction(e.cfg.Publisher.Action)
		}
	case "popularity":
		if e.cfg.Popularity != nil && r.Result == trust.SignalWarn {
			return cfgAction(e.cfg.Popularity.Action)
		}
	}
	return ActionAllow
}
