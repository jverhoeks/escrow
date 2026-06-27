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
	"github.com/jverhoeks/escrow/internal/pkgref"
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
	cfgMu   sync.RWMutex // guards cfg (live reload)
	cfg     Config
	mu      sync.Mutex // serializes sweeps
	lastMu  sync.RWMutex
	lastRes Result

	idxMu   sync.RWMutex
	inv     map[verKey]struct{}
	baseline map[verKey]map[string]bool
	idxDone chan struct{} // closed when the subscription goroutine exits
}

func New(deps Deps, cfg Config) *Scanner { return &Scanner{deps: deps, cfg: cfg} }

// SetConfig swaps the scanner config atomically (live reload). enabled/auto_block/
// min_severity apply on the next sweep; interval_hours on the next scheduled cycle.
func (s *Scanner) SetConfig(cfg Config) {
	s.cfgMu.Lock()
	s.cfg = cfg
	s.cfgMu.Unlock()
}

func (s *Scanner) config() Config {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg
}

// GHSA advisories use "MODERATE" for what CVSS calls "MEDIUM"; treat as equal.
var severityRank = map[string]int{"CRITICAL": 4, "HIGH": 3, "MEDIUM": 2, "MODERATE": 2, "LOW": 1}

// RunOnce performs a single sweep. Safe to call concurrently (serialized).
func (s *Scanner) RunOnce(ctx context.Context) Result {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg := s.config()
	inv, baseline := s.inventory()
	res := Result{At: time.Now().UTC()}
	minRank := severityRank[strings.ToUpper(cfg.MinSeverity)]

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
		s.handleFinding(cfg, key, newVulns, &res)
	}

	s.lastMu.Lock()
	s.lastRes = res
	s.lastMu.Unlock()
	return res
}

type verKey struct{ eco, name, version string }

// inventory returns the downloaded version set and, per version, the set of
// already-known vuln IDs (from prior osv/rescan events) used as the baseline.
// When the incremental subscription index is ready it uses that instead of
// scanning the full event log. This avoids iterating up to 5 000 events on
// every 24-hour rescan cycle (see A2). On the very first call (before the
// subscription has received any events) it falls back to the full scan.
func (s *Scanner) inventory() (map[verKey]struct{}, map[verKey]map[string]bool) {
	if s.indexReady() {
		s.idxMu.RLock()
		inv := make(map[verKey]struct{}, len(s.inv))
		for k := range s.inv {
			inv[k] = struct{}{}
		}
		baseline := make(map[verKey]map[string]bool, len(s.baseline))
		for k, ids := range s.baseline {
			m := make(map[string]bool, len(ids))
			for id := range ids {
				m[id] = true
			}
			baseline[k] = m
		}
		s.idxMu.RUnlock()
		return inv, baseline
	}
	// Fallback: scan the full event log (used once on first sweep before the
	// subscription has delivered any events).
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

func (s *Scanner) handleFinding(cfg Config, k verKey, vulns []trust.Vuln, res *Result) {
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
	if cfg.AutoBlock {
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

// Start runs RunOnce until ctx is cancelled. It also starts a background
// goroutine that subscribes to the event log and maintains an incrementally-
// updated rescan index, avoiding a full event-log scan on every cycle.
// The first sweep runs after a short delay so startup isn't blocked. The
// interval is re-read each cycle and the enabled flag re-checked, so a live
// SetConfig (hot reload) takes effect.
func (s *Scanner) Start(ctx context.Context) {
	if !s.config().Enabled {
		return
	}
	s.idxDone = make(chan struct{})
	go s.subscribeIndex(ctx)
	go func() {
		if !sleepCtx(ctx, 30*time.Second) {
			return
		}
		if s.config().Enabled {
			s.RunOnce(ctx)
		}
		for {
			d := time.Duration(s.config().IntervalHours) * time.Hour
			if d <= 0 {
				d = 24 * time.Hour
			}
			if !sleepCtx(ctx, d) {
				return
			}
			if s.config().Enabled {
				s.RunOnce(ctx)
			}
		}
	}()
}

// subscribeIndex subscribes to the event log and maintains the scanner's
// incrementally-updated rescan index. Exits when ctx is cancelled or when
// the event log's subscribe channel returns nil (cap reached).
func (s *Scanner) subscribeIndex(ctx context.Context) {
	defer close(s.idxDone)
	ch, unsub := s.deps.Log.Subscribe()
	if ch == nil {
		return
	}
	defer unsub()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			name, version := splitPackage(e.Package)
			if name == "" {
				continue
			}
			k := verKey{e.Ecosystem, name, version}
			s.idxMu.Lock()
			if e.Kind == eventlog.KindDownloaded {
				if s.inv == nil {
					s.inv = make(map[verKey]struct{})
				}
				s.inv[k] = struct{}{}
			}
			if len(e.Vulns) > 0 {
				if s.baseline == nil {
					s.baseline = make(map[verKey]map[string]bool)
				}
				if s.baseline[k] == nil {
					s.baseline[k] = make(map[string]bool)
				}
				for _, v := range e.Vulns {
					s.baseline[k][v.ID] = true
				}
			}
			s.idxMu.Unlock()
		}
	}
}

// indexReady returns true when the incremental index has been populated from
// the subscription stream (at least one event received).
func (s *Scanner) indexReady() bool {
	s.idxMu.RLock()
	defer s.idxMu.RUnlock()
	return s.inv != nil || s.baseline != nil
}

// sleepCtx waits for d or until ctx is cancelled. It returns true if the full
// duration elapsed, false if ctx was cancelled first. Unlike time.After, the
// timer is stopped on cancellation, so a long interval (e.g. 24h) doesn't leak
// a timer for the rest of that interval on shutdown. See #69.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// LastRun returns the most recent sweep result.
func (s *Scanner) LastRun() Result {
	s.lastMu.RLock()
	defer s.lastMu.RUnlock()
	return s.lastRes
}

func splitPackage(pkg string) (name, version string) { return pkgref.Split(pkg) }
