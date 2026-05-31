package policy

import (
	"sync"

	"github.com/jverhoeks/escrow/internal/allow"
	"github.com/jverhoeks/escrow/internal/block"
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
	Vulns  []trust.Vuln // populated from the triggering signal report (e.g. OSV)
}

type Engine struct {
	mu        sync.RWMutex
	cfg       *config.PolicyConfig
	allowList *allow.List // may be nil
	blockList *block.List // may be nil
}

func New(cfg *config.PolicyConfig) *Engine { return &Engine{cfg: cfg} }

// SetConfig swaps the policy config atomically (live reload).
func (e *Engine) SetConfig(cfg *config.PolicyConfig) {
	e.mu.Lock()
	e.cfg = cfg
	e.mu.Unlock()
}

// WithAllowList sets the allowlist on the engine and returns the engine for chaining.
func (e *Engine) WithAllowList(l *allow.List) *Engine {
	e.allowList = l
	return e
}

// WithBlockList sets the blocklist on the engine and returns the engine for chaining.
func (e *Engine) WithBlockList(l *block.List) *Engine {
	e.blockList = l
	return e
}

func (e *Engine) Evaluate(result trust.TrustResult) Decision {
	e.mu.RLock()
	cfg := e.cfg
	e.mu.RUnlock()
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
	if e.blockList != nil {
		if ok, entry := e.blockList.IsBlocked(
			string(result.Package.Ecosystem),
			result.Package.Name,
			result.Package.Version,
		); ok {
			return Decision{
				Action: ActionBlock,
				Signal: "manual-block",
				Reason: "blocklist: " + entry.Reason,
			}
		}
	}
	if cfg == nil {
		return Decision{Action: ActionAllow}
	}
	var warns []Decision
	for _, r := range result.Reports {
		if r.Result == trust.SignalPass || r.Result == trust.SignalSkip {
			continue
		}
		var a Action
		if r.Result == trust.SignalError {
			a = cfgAction(cfg.StrictSignals)
		} else {
			a = e.actionFor(cfg, r)
		}
		d := Decision{Action: a, Signal: r.Signal, Reason: r.Reason, Vulns: r.Vulns}
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

func (e *Engine) actionFor(cfg *config.PolicyConfig, r trust.SignalReport) Action {
	switch r.Signal {
	case "age":
		if cfg.Age != nil && r.Result == trust.SignalFail {
			return cfgAction(cfg.Age.Action)
		}
	case "osv":
		if cfg.OSV != nil && r.Result == trust.SignalFail {
			return cfgAction(cfg.OSV.Action)
		}
	case "publisher":
		if cfg.Publisher != nil && r.Result == trust.SignalWarn {
			return cfgAction(cfg.Publisher.Action)
		}
	case "popularity":
		if cfg.Popularity != nil && r.Result == trust.SignalWarn {
			return cfgAction(cfg.Popularity.Action)
		}
	}
	return ActionAllow
}
