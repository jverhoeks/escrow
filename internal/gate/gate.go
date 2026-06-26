// Package gate enforces trust policy on the artifact-download path.
//
// Listing/metadata handlers filter blocked versions out of the manifest with an
// age-only engine. The download endpoints, however, are reachable directly (a
// pinned lockfile URL, a rescan-auto-blocked version, a rewritten PyPI/NuGet
// artifact URL). gate.Check is the single enforcement point those handlers call
// before serving any bytes: it runs the full trust engine + policy (allow/block
// lists + signals), records the real decision as a download event, and reports
// whether the artifact may be served.
package gate

import (
	"context"

	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/jverhoeks/escrow/internal/metrics"
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/trust"
)

// Check evaluates pkg through the full trust engine and policy and returns the
// decision. On a block it records a KindDownloaded block event and bumps the
// block metric, so the caller only has to render 403. On allow/warn it records
// nothing and bumps nothing: the caller serves the artifact and records the
// successful-download event itself (keeping download counts tied to real
// serves). Callers serve the artifact only when the returned Action is not
// policy.ActionBlock. evlog may be nil.
func Check(ctx context.Context, eng *trust.Engine, pol *policy.Engine, evlog *eventlog.Log, pkg trust.Package) policy.Decision {
	result, _ := eng.Check(ctx, pkg)
	decision := pol.Evaluate(result)

	if decision.Action == policy.ActionBlock {
		if evlog != nil {
			evlog.Record(eventlog.PackageEvent{
				Ecosystem: string(pkg.Ecosystem),
				Package:   pkg.Name + "@" + pkg.Version,
				Action:    string(decision.Action),
				Signal:    decision.Signal,
				Reason:    decision.Reason,
				Kind:      eventlog.KindDownloaded,
				Vulns:     decision.Vulns,
			})
		}
		metrics.BlocksTotal.WithLabelValues(string(pkg.Ecosystem), decision.Signal).Inc()
	}
	return decision
}
