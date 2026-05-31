// Package rescan periodically re-checks downloaded package versions for newly
// published vulnerabilities and acts on new findings (alert + optional auto-block).
package rescan

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jverhoeks/escrow/internal/block"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/jverhoeks/escrow/internal/trust"
)

// Config controls re-scan behavior.
type Config struct {
	Enabled       bool
	IntervalHours int
	AutoBlock     bool
	MinSeverity   string // CRITICAL|HIGH|MEDIUM|LOW
}

// DownloadStats is the optional dependency that supplies blast-radius context.
type DownloadStats interface {
	Get(eco, name, version string) (count int, lastAt time.Time, ok bool)
}

// Alerter is the optional webhook dependency.
type Alerter interface {
	SendRescan(eco, name, version string, vulns []string, severity string, blocked bool, downloadCount int) error
}

// Deps are the scanner's collaborators. OSV, Log, and BlockList are required;
// Stats and Alerter are optional (may be nil).
type Deps struct {
	Log       *eventlog.Log
	OSV       *trust.OSVSignal
	BlockList *block.List
	Stats     DownloadStats
	Alerter   Alerter
	Logger    interface{ Printf(string, ...any) } // optional; nil = silent
}

// Result summarizes one sweep.
type Result struct {
	Scanned     int
	NewFindings int
	Blocked     int
	Errors      int
	At          time.Time
}

type Scanner struct {
	deps    Deps
	cfg     Config
	mu      sync.Mutex // serializes sweeps
	lastMu  sync.RWMutex
	lastRes Result
}

func New(deps Deps, cfg Config) *Scanner { return &Scanner{deps: deps, cfg: cfg} }

var severityRank = map[string]int{"CRITICAL": 4, "HIGH": 3, "MEDIUM": 2, "LOW": 1}

// RunOnce performs a single sweep. Safe to call concurrently (serialized).
func (s *Scanner) RunOnce(ctx context.Context) Result {
	s.mu.Lock()
	defer s.mu.Unlock()

	inv, baseline := s.inventory()
	res := Result{At: time.Now().UTC()}
	minRank := severityRank[strings.ToUpper(s.cfg.MinSeverity)]

	for key := range inv {
		res.Scanned++
		rep, err := s.deps.OSV.CheckFresh(ctx, trust.Package{
			Ecosystem: trust.Ecosystem(key.eco), Name: key.name, Version: key.version,
		})
		if err != nil || rep.Result == trust.SignalSkip || rep.Result == trust.SignalError {
			res.Errors++
			continue // never treat a failed query as "all clear"
		}
		// New vulns = current vulns at/above threshold not already known.
		known := baseline[key]
		var newVulns []trust.Vuln
		for _, v := range rep.Vulns {
			rank, ok := severityRank[strings.ToUpper(v.Severity)]
			if v.Severity != "" && ok && rank < minRank {
				continue
			}
			if !known[v.ID] {
				newVulns = append(newVulns, v)
			}
		}
		if len(newVulns) == 0 {
			continue
		}
		res.NewFindings++
		s.handleFinding(key, newVulns, &res)
	}

	s.lastMu.Lock()
	s.lastRes = res
	s.lastMu.Unlock()
	return res
}

type verKey struct{ eco, name, version string }

// inventory returns the downloaded version set and, per version, the set of
// already-known vuln IDs (from prior osv/rescan events) used as the baseline.
func (s *Scanner) inventory() (map[verKey]struct{}, map[verKey]map[string]bool) {
	inv := map[verKey]struct{}{}
	baseline := map[verKey]map[string]bool{}
	for _, e := range s.deps.Log.Events("") {
		name, version := splitPackage(e.Package)
		if name == "" {
			continue
		}
		k := verKey{e.Ecosystem, name, version}
		if e.Kind == eventlog.KindDownloaded {
			inv[k] = struct{}{}
		}
		if len(e.Vulns) > 0 {
			if baseline[k] == nil {
				baseline[k] = map[string]bool{}
			}
			for _, v := range e.Vulns {
				baseline[k][v.ID] = true
			}
		}
	}
	return inv, baseline
}

func (s *Scanner) handleFinding(k verKey, vulns []trust.Vuln, res *Result) {
	ids := make([]string, 0, len(vulns))
	topSev := ""
	for _, v := range vulns {
		ids = append(ids, v.ID)
		if severityRank[strings.ToUpper(v.Severity)] > severityRank[strings.ToUpper(topSev)] {
			topSev = v.Severity
		}
	}

	// Record a kind=rescan event so the finding is durable and becomes baseline.
	s.deps.Log.Record(eventlog.PackageEvent{
		Ecosystem: k.eco, Package: k.name + "@" + k.version,
		Action: "block", Signal: "osv", Kind: eventlog.KindRescan,
		Reason: fmt.Sprintf("retroactive CVE: %s", strings.Join(ids, ", ")),
		Vulns:  vulns,
	})

	blocked := false
	if s.cfg.AutoBlock {
		if already, _ := s.deps.BlockList.IsBlocked(k.eco, k.name, k.version); !already {
			_ = s.deps.BlockList.Add(block.Entry{
				Ecosystem: k.eco, Name: k.name, Version: k.version,
				Reason:  fmt.Sprintf("retroactive CVE %s (re-scan %s)", strings.Join(ids, ", "), time.Now().UTC().Format("2006-01-02")),
				AddedBy: "system/rescan",
			})
		}
		blocked = true
		res.Blocked++
	}

	count := 0
	if s.deps.Stats != nil {
		if c, _, ok := s.deps.Stats.Get(k.eco, k.name, k.version); ok {
			count = c
		}
	}
	if s.deps.Alerter != nil {
		_ = s.deps.Alerter.SendRescan(k.eco, k.name, k.version, ids, topSev, blocked, count)
	}
}

// Start runs RunOnce on a ticker until ctx is cancelled. The first sweep runs
// after a short delay so startup isn't blocked.
func (s *Scanner) Start(ctx context.Context) {
	if !s.cfg.Enabled {
		return
	}
	interval := time.Duration(s.cfg.IntervalHours) * time.Hour
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
			s.RunOnce(ctx)
		}
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.RunOnce(ctx)
			}
		}
	}()
}

// LastRun returns the most recent sweep result.
func (s *Scanner) LastRun() Result {
	s.lastMu.RLock()
	defer s.lastMu.RUnlock()
	return s.lastRes
}

func splitPackage(pkg string) (name, version string) {
	i := strings.LastIndex(pkg, "@")
	if i <= 0 {
		return pkg, ""
	}
	return pkg[:i], pkg[i+1:]
}
