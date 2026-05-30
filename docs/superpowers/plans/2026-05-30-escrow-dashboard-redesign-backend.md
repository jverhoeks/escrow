# Escrow Dashboard Redesign — Phase 1 (Backend) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the backend data capture and API endpoints the redesigned dashboard needs — structured CVE data, best-effort package size, an upstream-fetch log, and four new/extended JSON endpoints — all behind the existing dashboard auth.

**Architecture:** Capture structured vulnerabilities at trust-decision time onto each `PackageEvent` (durable, survives the OSV cache TTL). Read package size best-effort from cache blobs via a new `cache.BlobSize`. Record escrow→upstream fetches into a new in-memory `upstreamlog` ring buffer from the existing `LoggingTransport`, filtered to known registry hosts. Expose everything through new handlers in the `dashboard` package, tested with the existing `handlers_test.go` style. No frontend changes in this phase.

**Tech Stack:** Go 1.x, chi router, zerolog, testify, `go:embed`. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-05-30-escrow-dashboard-redesign-design.md`

---

## File Structure

**New files:**
- `internal/upstreamlog/log.go` — in-memory ring buffer of upstream fetch events (mirrors `eventlog`).
- `internal/upstreamlog/log_test.go` — tests for the ring buffer.
- `internal/dashboard/timeseries.go` — `/api/stats/timeseries` handler + bucketing.
- `internal/dashboard/timeseries_test.go`
- `internal/dashboard/accesslog.go` — `/api/accesslog` handler + Apache-combined parser.
- `internal/dashboard/accesslog_test.go`
- `internal/dashboard/upstream.go` — `/api/upstreamlog` handler.
- `internal/dashboard/upstream_test.go`
- `internal/dashboard/cves.go` — `/api/cves` handler + grouping.
- `internal/dashboard/cves_test.go`
- `internal/dashboard/tree.go` — namespace-tree shaping + size for `/api/packages`.
- `internal/dashboard/tree_test.go`

**Modified files:**
- `internal/cache/cache.go` — add `BlobSize` to the interface.
- `internal/cache/memory.go`, `internal/cache/disk.go`, `internal/cache/s3.go` — implement `BlobSize`.
- `internal/cache/*_test.go` — `BlobSize` tests (the disk/memory tests live in `internal/cache`).
- `internal/trust/types.go` — add `Vuln` type and `SignalReport.Vulns`.
- `internal/trust/osv.go` — populate `Vulns` in `toReport`.
- `internal/policy/policy.go` — add `Decision.Vulns`, copy from the triggering report.
- `internal/eventlog/log.go` — add `PackageEvent.Vulns`.
- `internal/handler/{npm,pypi,gomod,cargo,composer,nuget,maven}/handler.go` — set `Vulns` on recorded events.
- `internal/server/upstream.go` — record into `upstreamlog` with host→ecosystem filter.
- `internal/dashboard/handlers.go` — new routes; extend `Dashboard` struct + `New`.
- `internal/dashboard/handlers_test.go` — update `newTestDashboard` for the new `New` signature.
- `cmd/escrow/main.go` — build the host→ecosystem map, create the buffer, wire both.

---

## Task 1: `cache.BlobSize` across all backends

**Files:**
- Modify: `internal/cache/cache.go:10-22`
- Modify: `internal/cache/memory.go:89-92`
- Modify: `internal/cache/disk.go:263-266`
- Modify: `internal/cache/s3.go:132-136`
- Test: `internal/cache/disk_test.go` (existing file in package `cache`)

- [ ] **Step 1: Write the failing test**

Add to `internal/cache/disk_test.go`:

```go
func TestDisk_BlobSize(t *testing.T) {
	d, err := NewDisk(t.TempDir(), 0, 0)
	require.NoError(t, err)
	defer d.Close()

	ctx := context.Background()
	require.NoError(t, d.SetBlob(ctx, "npm/x/-/x-1.0.0.tgz", bytes.NewReader([]byte("hello world"))))

	if got := d.BlobSize(ctx, "npm/x/-/x-1.0.0.tgz"); got != 11 {
		t.Fatalf("BlobSize = %d, want 11", got)
	}
	if got := d.BlobSize(ctx, "npm/missing"); got != -1 {
		t.Fatalf("BlobSize(missing) = %d, want -1", got)
	}
}
```

Check the existing `NewDisk` signature at the top of `internal/cache/disk.go` and match it (arguments for capacity/TTL). If the test helpers differ, mirror an existing `disk_test.go` constructor call.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cache/ -run TestDisk_BlobSize`
Expected: FAIL — `d.BlobSize undefined`.

- [ ] **Step 3: Add `BlobSize` to the interface**

In `internal/cache/cache.go`, add inside the `Cache` interface after `HasBlob`:

```go
	// BlobSize returns the blob size in bytes, or -1 if the blob is absent
	// or its size cannot be determined.
	BlobSize(ctx context.Context, key string) int64
```

- [ ] **Step 4: Implement for memory**

In `internal/cache/memory.go`, after `HasBlob`:

```go
func (m *Memory) BlobSize(_ context.Context, key string) int64 {
	info, err := os.Stat(filepath.Join(m.tempDir, sanitize(key)))
	if err != nil {
		return -1
	}
	return info.Size()
}
```

- [ ] **Step 5: Implement for disk**

In `internal/cache/disk.go`, after `HasBlob`:

```go
func (d *Disk) BlobSize(_ context.Context, key string) int64 {
	info, err := os.Stat(d.blobPath(key))
	if err != nil {
		return -1
	}
	return info.Size()
}
```

- [ ] **Step 6: Implement for s3**

In `internal/cache/s3.go`, after `HasBlob`:

```go
func (s *S3Cache) BlobSize(ctx context.Context, key string) int64 {
	k := s.blobKey(key)
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.bucket, Key: &k})
	if err != nil || out.ContentLength == nil {
		return -1
	}
	return *out.ContentLength
}
```

- [ ] **Step 7: Run tests**

Run: `go test ./internal/cache/`
Expected: PASS (all cache tests, including the new one).

- [ ] **Step 8: Commit**

```bash
git add internal/cache/
git commit -m "feat(cache): add BlobSize to Cache interface and all backends"
```

---

## Task 2: Structured vulnerabilities on `SignalReport`

**Files:**
- Modify: `internal/trust/types.go:39-49`
- Modify: `internal/trust/osv.go:114-141`
- Test: `internal/trust/osv_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/trust/osv_test.go`:

```go
func TestOSV_ReportCarriesStructuredVulns(t *testing.T) {
	body := `{"vulns":[
		{"id":"GHSA-aaaa","database_specific":{"severity":"CRITICAL"}},
		{"id":"GHSA-bbbb","database_specific":{"severity":"HIGH"}}
	]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()

	s := NewOSVSignal("HIGH", srv.Client(), newTestCache(t), srv.URL)
	rep, err := s.Check(context.Background(), Package{Ecosystem: EcosystemNPM, Name: "x", Version: "1.0.0"})
	require.NoError(t, err)
	require.Equal(t, SignalFail, rep.Result)
	require.Len(t, rep.Vulns, 2)
	require.Equal(t, "GHSA-aaaa", rep.Vulns[0].ID)
	require.Equal(t, "CRITICAL", rep.Vulns[0].Severity)
	require.Equal(t, "HIGH", rep.Vulns[1].Severity)
}
```

If `osv_test.go` already has a cache constructor helper, reuse it instead of `newTestCache`; check the top of `osv_test.go` for the existing pattern and match it.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/trust/ -run TestOSV_ReportCarriesStructuredVulns`
Expected: FAIL — `rep.Vulns undefined`.

- [ ] **Step 3: Add the `Vuln` type and field**

In `internal/trust/types.go`, add above `SignalReport`:

```go
// Vuln is a single vulnerability advisory matched against a package version.
type Vuln struct {
	ID       string `json:"id"`
	Severity string `json:"severity"` // "CRITICAL"|"HIGH"|"MEDIUM"|"LOW"|"" (unknown)
}
```

Then add a field to `SignalReport`:

```go
type SignalReport struct {
	Signal string
	Result SignalResult
	Reason string
	Vulns  []Vuln // populated by the OSV signal when Result == SignalFail
}
```

- [ ] **Step 4: Populate `Vulns` in `toReport`**

Replace the body of `toReport` in `internal/trust/osv.go` so it collects structured vulns alongside the IDs:

```go
func (s *OSVSignal) toReport(resp osvResponse) SignalReport {
	minRank := severityRank[s.minSeverity]
	var matching []Vuln
	for _, v := range resp.Vulns {
		sev := ""
		if v.DatabaseSpecific != nil && v.DatabaseSpecific.Severity != "" {
			sev = strings.ToUpper(v.DatabaseSpecific.Severity)
		}
		rank, known := severityRank[sev]
		if !known || rank >= minRank {
			matching = append(matching, Vuln{ID: v.ID, Severity: sev})
		}
	}
	if len(matching) == 0 {
		return SignalReport{Signal: s.Name(), Result: SignalPass, Reason: "no known vulnerabilities at or above " + s.minSeverity}
	}
	ids := make([]string, 0, len(matching))
	for _, v := range matching {
		ids = append(ids, v.ID)
	}
	limit := 3
	if len(ids) < limit {
		limit = len(ids)
	}
	return SignalReport{
		Signal: s.Name(),
		Result: SignalFail,
		Reason: fmt.Sprintf("%d vulnerability/vulnerabilities at or above %s: %s",
			len(ids), s.minSeverity, strings.Join(ids[:limit], ", ")),
		Vulns: matching,
	}
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/trust/`
Expected: PASS (new test + existing OSV tests unchanged).

- [ ] **Step 6: Commit**

```bash
git add internal/trust/
git commit -m "feat(trust): OSV signal reports structured vulnerabilities"
```

---

## Task 3: Carry vulns through the policy decision

**Files:**
- Modify: `internal/policy/policy.go:18-22,74-92`
- Test: `internal/policy/policy_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/policy/policy_test.go`:

```go
func TestEvaluate_BlockDecisionCarriesVulns(t *testing.T) {
	cfg := &config.PolicyConfig{OSV: &config.OSVPolicyConfig{Action: "block"}}
	e := policy.New(cfg)
	result := trust.TrustResult{
		Package: trust.Package{Ecosystem: trust.EcosystemNPM, Name: "x", Version: "1.0.0"},
		Reports: []trust.SignalReport{{
			Signal: "osv", Result: trust.SignalFail, Reason: "1 vuln",
			Vulns: []trust.Vuln{{ID: "GHSA-aaaa", Severity: "CRITICAL"}},
		}},
	}
	d := e.Evaluate(result)
	require.Equal(t, policy.ActionBlock, d.Action)
	require.Len(t, d.Vulns, 1)
	require.Equal(t, "GHSA-aaaa", d.Vulns[0].ID)
}
```

Match the existing `policy_test.go` imports/config construction; if `OSVPolicyConfig` field names differ, copy them from an existing test in that file.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/policy/ -run TestEvaluate_BlockDecisionCarriesVulns`
Expected: FAIL — `d.Vulns undefined`.

- [ ] **Step 3: Add `Vulns` to `Decision`**

In `internal/policy/policy.go`:

```go
type Decision struct {
	Action Action
	Signal string
	Reason string
	Vulns  []trust.Vuln // populated from the triggering signal report (e.g. OSV)
}
```

- [ ] **Step 4: Copy vulns when building the decision**

In `Evaluate`, change the per-report decision construction (currently `d := Decision{Action: a, Signal: r.Signal, Reason: r.Reason}`) to:

```go
		d := Decision{Action: a, Signal: r.Signal, Reason: r.Reason, Vulns: r.Vulns}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/policy/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/policy/
git commit -m "feat(policy): decision carries structured vulns from triggering signal"
```

---

## Task 4: Persist vulns on `PackageEvent` and record them in handlers

**Files:**
- Modify: `internal/eventlog/log.go:22-30`
- Modify: `internal/handler/npm/handler.go:164-172`
- Modify: `internal/handler/pypi/handler.go:305`, `gomod/handler.go:141,319,334`, `cargo/handler.go:319`, `maven/handler.go:353`, `composer/handler.go:199`, `nuget/handler.go:318`
- Test: `internal/eventlog/log_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/eventlog/log_test.go`:

```go
func TestPackageEvent_VulnsRoundTrip(t *testing.T) {
	l := eventlog.New(10)
	l.Record(eventlog.PackageEvent{
		Ecosystem: "npm", Package: "x@1.0.0", Action: "block", Signal: "osv",
		Vulns: []trust.Vuln{{ID: "GHSA-aaaa", Severity: "CRITICAL"}},
	})
	evs := l.Events("")
	require.Len(t, evs, 1)
	require.Len(t, evs[0].Vulns, 1)
	require.Equal(t, "GHSA-aaaa", evs[0].Vulns[0].ID)
}
```

Add `"github.com/jverhoeks/escrow/internal/trust"` to the test imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/eventlog/ -run TestPackageEvent_VulnsRoundTrip`
Expected: FAIL — `Vulns` unknown field.

- [ ] **Step 3: Add the field**

In `internal/eventlog/log.go`, add the import `"github.com/jverhoeks/escrow/internal/trust"` and extend `PackageEvent`:

```go
type PackageEvent struct {
	Timestamp time.Time    `json:"timestamp"`
	Ecosystem string       `json:"ecosystem"`
	Package   string       `json:"package"`
	Action    string       `json:"action"`
	Signal    string       `json:"signal"`
	Reason    string       `json:"reason"`
	Operator  string       `json:"operator,omitempty"`
	Vulns     []trust.Vuln `json:"vulns,omitempty"`
}
```

Verify no import cycle: `trust` must not import `eventlog`. Run `go build ./internal/eventlog/` — if it reports a cycle, stop and report; the fallback is to define a local `eventlog.Vuln{ID,Severity string}` and convert in handlers.

- [ ] **Step 4: Set `Vulns` in every handler's Record call**

In each handler listed above, the `h.evlog.Record(eventlog.PackageEvent{...})` (or `evlog.Record(...)` in maven) is built from a `decision`. Add the field:

```go
				Vulns:     decision.Vulns,
```

For npm (`internal/handler/npm/handler.go:165-172`) the block becomes:

```go
			h.evlog.Record(eventlog.PackageEvent{
				Ecosystem: string(pkg.Ecosystem),
				Package:   pkg.Name + "@" + pkg.Version,
				Action:    string(decision.Action),
				Signal:    decision.Signal,
				Reason:    decision.Reason,
				Vulns:     decision.Vulns,
			})
```

Apply the same one-line `Vulns: decision.Vulns,` addition at each Record site. The local decision variable name is `decision` in every handler — confirm by reading two lines above each Record call; if a site uses a different name (e.g. `d`), use that name.

- [ ] **Step 5: Build and run the affected packages**

Run: `go build ./... && go test ./internal/eventlog/ ./internal/handler/...`
Expected: PASS / build clean.

- [ ] **Step 6: Commit**

```bash
git add internal/eventlog/ internal/handler/
git commit -m "feat(eventlog): persist structured vulns on package events"
```

---

## Task 5: `internal/upstreamlog` ring buffer

**Files:**
- Create: `internal/upstreamlog/log.go`
- Test: `internal/upstreamlog/log_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/upstreamlog/log_test.go`:

```go
package upstreamlog_test

import (
	"testing"

	"github.com/jverhoeks/escrow/internal/upstreamlog"
	"github.com/stretchr/testify/require"
)

func TestLog_RecordAndEventsNewestFirst(t *testing.T) {
	l := upstreamlog.New(2)
	l.Record(upstreamlog.Event{Ecosystem: "npm", URL: "https://registry.npmjs.org/a", Status: 200})
	l.Record(upstreamlog.Event{Ecosystem: "pypi", URL: "https://pypi.org/b", Status: 404})
	l.Record(upstreamlog.Event{Ecosystem: "cargo", URL: "https://crates.io/c", Status: 200})

	all := l.Events("")
	require.Len(t, all, 2) // capacity 2, newest-first
	require.Equal(t, "cargo", all[0].Ecosystem)
	require.Equal(t, "pypi", all[1].Ecosystem)

	require.Len(t, l.Events("pypi"), 1)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/upstreamlog/`
Expected: FAIL — package does not compile / `New` undefined.

- [ ] **Step 3: Implement the ring buffer**

Create `internal/upstreamlog/log.go`:

```go
// Package upstreamlog keeps a bounded, in-memory record of escrow→upstream
// fetches. Every entry represents a real upstream call (a cache miss); cache
// hits never reach the upstream transport and so never appear here.
package upstreamlog

import (
	"sync"
	"time"
)

// Event is a single escrow→upstream fetch.
type Event struct {
	Timestamp time.Time `json:"timestamp"`
	Ecosystem string    `json:"ecosystem"`
	Method    string    `json:"method"`
	URL       string    `json:"url"`
	Status    int       `json:"status"`
	Bytes     int64     `json:"bytes"`
	MS        float64   `json:"ms"`
}

// Log is a fixed-capacity, newest-first ring of upstream fetch events.
type Log struct {
	mu     sync.RWMutex
	cap    int
	events []Event
}

// New returns an upstream log holding at most cap events.
func New(cap int) *Log {
	if cap <= 0 {
		cap = 1
	}
	return &Log{cap: cap}
}

// Record prepends an event, trimming to capacity. Timestamp defaults to now.
func (l *Log) Record(e Event) {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	l.mu.Lock()
	l.events = append([]Event{e}, l.events...)
	if len(l.events) > l.cap {
		l.events = l.events[:l.cap]
	}
	l.mu.Unlock()
}

// Events returns a newest-first copy, optionally filtered by ecosystem.
func (l *Log) Events(eco string) []Event {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Event, 0, len(l.events))
	for _, e := range l.events {
		if eco == "" || e.Ecosystem == eco {
			out = append(out, e)
		}
	}
	return out
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/upstreamlog/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/upstreamlog/
git commit -m "feat(upstreamlog): in-memory ring buffer of upstream fetches"
```

---

## Task 6: Record upstream fetches from `LoggingTransport`

**Files:**
- Modify: `internal/server/upstream.go`
- Test: `internal/server/upstream_test.go` (create if absent)

- [ ] **Step 1: Write the failing test**

Create or append `internal/server/upstream_test.go`:

```go
package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jverhoeks/escrow/internal/upstreamlog"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestLoggingTransport_RecordsKnownHostsOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	ul := upstreamlog.New(10)
	hosts := map[string]string{"registry.npmjs.org": "npm"}
	c := NewLoggingClientWithRecorder(srv.Client(), zerolog.Nop(), ul, hosts)

	// Unknown host (the test server) → not recorded.
	resp, err := c.Get(srv.URL)
	require.NoError(t, err)
	resp.Body.Close()
	require.Len(t, ul.Events(""), 0)

	// Known host → recorded. Force the Host header so the transport classifies it.
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Host = "registry.npmjs.org"
	resp2, err := c.Do(req)
	require.NoError(t, err)
	resp2.Body.Close()

	evs := ul.Events("")
	require.Len(t, evs, 1)
	require.Equal(t, "npm", evs[0].Ecosystem)
	require.Equal(t, 200, evs[0].Status)
}
```

Note: the transport must classify on `req.URL.Host` (the dialed host), so in the test set `req.Host`/`req.URL.Host` — confirm which one the existing code reads (`req.URL.String()` today). Classify on `req.URL.Hostname()`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestLoggingTransport_RecordsKnownHostsOnly`
Expected: FAIL — `NewLoggingClientWithRecorder` undefined.

- [ ] **Step 3: Extend the transport**

In `internal/server/upstream.go`, add a recorder + host map to `LoggingTransport` and a new constructor. Keep the existing `NewLoggingClient` working (delegate to the new one with nil recorder):

```go
type LoggingTransport struct {
	Base zerolog.Logger
	Next http.RoundTripper

	// Optional upstream-fetch recorder. When rec is non-nil and the request
	// host is present in hostEco, the fetch is recorded with that ecosystem.
	rec     *upstreamlog.Log
	hostEco map[string]string
}

func (t *LoggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := t.Next.RoundTrip(req)
	ms := float64(time.Since(start).Microseconds()) / 1000

	ev := t.Base.Debug().
		Bool("upstream", true).
		Str("method", req.Method).
		Str("url", req.URL.String()).
		Float64("ms", ms)

	if err != nil {
		ev.Err(err).Msg("upstream")
		return resp, err
	}

	bytes := resp.ContentLength
	if bytes < 0 {
		bytes = 0
	}
	ev.Int("status", resp.StatusCode).
		Int64("bytes", bytes).
		Msg("upstream")

	if t.rec != nil {
		if eco, ok := t.hostEco[req.URL.Hostname()]; ok {
			t.rec.Record(upstreamlog.Event{
				Ecosystem: eco,
				Method:    req.Method,
				URL:       req.URL.String(),
				Status:    resp.StatusCode,
				Bytes:     bytes,
				MS:        ms,
			})
		}
	}

	return resp, nil
}

// NewLoggingClient wraps c so all requests are logged via log (no upstream-log recording).
func NewLoggingClient(c *http.Client, log zerolog.Logger) *http.Client {
	return NewLoggingClientWithRecorder(c, log, nil, nil)
}

// NewLoggingClientWithRecorder wraps c so requests are logged, and fetches to
// hosts present in hostEco are recorded into rec with the mapped ecosystem.
func NewLoggingClientWithRecorder(c *http.Client, log zerolog.Logger, rec *upstreamlog.Log, hostEco map[string]string) *http.Client {
	base := c.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	cp := *c
	cp.Transport = &LoggingTransport{Base: log, Next: base, rec: rec, hostEco: hostEco}
	return &cp
}
```

Add the import `"github.com/jverhoeks/escrow/internal/upstreamlog"`.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/server/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/server/upstream.go internal/server/upstream_test.go
git commit -m "feat(server): record upstream fetches for known registry hosts"
```

---

## Task 7: Wire the upstream log and host map in main.go

**Files:**
- Modify: `cmd/escrow/main.go` (the `upstreamURLs` block ~220-237, the `httpClient` line 131, the `dashboard.New` call 334)

- [ ] **Step 1: Move `upstreamURLs` construction above `httpClient`**

The `upstreamURLs` map (currently built ~lines 215-237 from `cfg.Ecosystems`) depends only on `cfg`. Move that entire block to just **above** line 131 (`httpClient := ...`). Confirm it references only `cfg.Ecosystems.*` — if it references a later variable, leave that reference where it is and only move the map population that doesn't.

- [ ] **Step 2: Build the host→ecosystem map and the buffer, then wire the client**

Immediately after the moved `upstreamURLs` block and before `httpClient :=`, add:

```go
	// Map known registry hostnames → ecosystem for the upstream fetch log.
	// Derived from configured upstreams, plus well-known defaults so artifact
	// CDNs (which differ from the metadata host) are also classified.
	upstreamLog := upstreamlog.New(5000)
	hostEco := map[string]string{
		"registry.npmjs.org":      "npm",
		"pypi.org":                "pypi",
		"files.pythonhosted.org":  "pypi",
		"crates.io":               "cargo",
		"static.crates.io":        "cargo",
		"proxy.golang.org":        "go",
		"repo1.maven.org":         "maven",
		"repo.maven.apache.org":   "maven",
		"repo.packagist.org":      "composer",
		"packagist.org":           "composer",
		"api.nuget.org":           "nuget",
	}
	for eco, raw := range upstreamURLs {
		if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
			hostEco[u.Hostname()] = eco
		}
	}
```

Add `"net/url"` to imports and `"github.com/jverhoeks/escrow/internal/upstreamlog"`.

- [ ] **Step 3: Use the recorder-aware client**

Change line 131 from:

```go
	httpClient := server.NewLoggingClient(upstream.New(), log.Logger)
```

to:

```go
	httpClient := server.NewLoggingClientWithRecorder(upstream.New(), log.Logger, upstreamLog, hostEco)
```

- [ ] **Step 4: Build**

Run: `go build ./...`
Expected: clean (the `dashboard.New` call still compiles — `upstreamLog` is passed in Task 9).

- [ ] **Step 5: Commit**

```bash
git add cmd/escrow/main.go
git commit -m "feat(cmd): wire upstream fetch log and host classification"
```

---

## Task 8: `/api/stats/timeseries` endpoint

**Files:**
- Create: `internal/dashboard/timeseries.go`
- Modify: `internal/dashboard/handlers.go:90` (route registration)
- Test: `internal/dashboard/timeseries_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/dashboard/timeseries_test.go`:

```go
package dashboard_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTimeseries_BucketsByHourAndEcosystem(t *testing.T) {
	handler, _ := newTestDashboardWithEvents(t)
	req := authenticatedRequest(t, http.MethodGet, "/dashboard/api/stats/timeseries?window=24h&bucket=1h", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var out struct {
		Buckets []string                      `json:"buckets"`
		Series  map[string]map[string][]int   `json:"series"` // action -> ecosystem -> per-bucket counts
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.NotEmpty(t, out.Buckets)
	require.Contains(t, out.Series, "allowed")
	require.Contains(t, out.Series, "denied")
}
```

Add a shared helper at the bottom of `timeseries_test.go` (used by later tasks too):

```go
func newTestDashboardWithEvents(t *testing.T) (http.Handler, *eventlog.Log) {
	t.Helper()
	al, err := allow.New("")
	require.NoError(t, err)
	cfg := config.DashboardConfig{Enabled: true, Path: "/dashboard", Username: "admin", Password: "pass", Secret: "aabbccddeeff00112233445566778899"}
	evLog := eventlog.New(100)
	evLog.Record(eventlog.PackageEvent{Ecosystem: "npm", Package: "a@1.0.0", Action: "allow"})
	evLog.Record(eventlog.PackageEvent{Ecosystem: "npm", Package: "b@1.0.0", Action: "block", Signal: "osv",
		Vulns: []trust.Vuln{{ID: "GHSA-x", Severity: "HIGH"}}})
	evLog.Record(eventlog.PackageEvent{Ecosystem: "pypi", Package: "c@2.0.0", Action: "warn"})
	dash := dashboard.New(cfg, evLog, zerolog.Nop(), al, nil, nil, "", 0, nil)
	r := chi.NewRouter()
	dash.Mount(r)
	return r, evLog
}
```

This calls the **new** `dashboard.New` signature finalized in Task 13 (`..., accessLogPath string, accessLogMaxDays int, upstreamLog *upstreamlog.Log`). Until Task 13 lands, this test will not compile — implement Tasks 8–12's handler code first, then do the signature change in Task 13, then run the dashboard test suite. (Subagent-driven execution: treat Tasks 8–12 as "write handler + test", and Task 13 as the compile/green gate for the whole `dashboard` package.)

Add imports to `timeseries_test.go`: `eventlog`, `trust`, `allow`, `config`, `dashboard`, `chi`, `zerolog`.

- [ ] **Step 2: Implement the handler**

Create `internal/dashboard/timeseries.go`:

```go
package dashboard

import (
	"encoding/json"
	"net/http"
	"time"
)

// handleTimeseries returns per-action, per-ecosystem hourly counts over a window.
// Response shape:
//   { "buckets": ["2026-05-30T00:00:00Z", ...],
//     "series": { "allowed": {"npm":[...]}, "denied": {...}, "warned": {...} } }
// "denied" == block events; "allowed" == allow; "warned" == warn.
func (d *Dashboard) handleTimeseries(w http.ResponseWriter, r *http.Request) {
	window := 24 * time.Hour
	if r.URL.Query().Get("window") == "1h" {
		window = time.Hour
	}
	bucket := time.Hour // only hourly buckets are supported

	now := time.Now().UTC().Truncate(bucket)
	start := now.Add(-window).Add(bucket) // inclusive of the current bucket
	n := int(window / bucket)
	if n < 1 {
		n = 1
	}

	buckets := make([]string, n)
	for i := 0; i < n; i++ {
		buckets[i] = start.Add(time.Duration(i) * bucket).Format(time.RFC3339)
	}
	idxFor := func(ts time.Time) int {
		off := ts.UTC().Truncate(bucket).Sub(start) / bucket
		return int(off)
	}

	actionKey := map[string]string{"allow": "allowed", "block": "denied", "warn": "warned"}
	series := map[string]map[string][]int{
		"allowed": {}, "denied": {}, "warned": {},
	}
	ensure := func(action, eco string) []int {
		m := series[action]
		if m[eco] == nil {
			m[eco] = make([]int, n)
		}
		return m[eco]
	}

	for _, e := range d.log.Events("") {
		key, ok := actionKey[e.Action]
		if !ok {
			continue
		}
		i := idxFor(e.Timestamp)
		if i < 0 || i >= n {
			continue
		}
		arr := ensure(key, e.Ecosystem)
		arr[i]++
		series[key][e.Ecosystem] = arr
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"buckets": buckets, "series": series})
}
```

- [ ] **Step 3: Register the route**

In `internal/dashboard/handlers.go` `Mount`, after the `protected.Get("/api/stats", d.handleStats)` line add:

```go
	protected.Get("/api/stats/timeseries", d.handleTimeseries)
```

- [ ] **Step 4: Defer running until Task 13**

Build the package: `go build ./internal/dashboard/` — expect a compile error only in the **test** file (new `New` signature), not in `timeseries.go`. If `timeseries.go` itself fails to build, fix it now.

- [ ] **Step 5: Commit**

```bash
git add internal/dashboard/timeseries.go internal/dashboard/timeseries_test.go internal/dashboard/handlers.go
git commit -m "feat(dashboard): 24h per-ecosystem timeseries endpoint"
```

---

## Task 9: `/api/upstreamlog` endpoint + thread the buffer into the dashboard

**Files:**
- Create: `internal/dashboard/upstream.go`
- Modify: `internal/dashboard/handlers.go` (struct + route; `New` updated in Task 13)
- Test: `internal/dashboard/upstream_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/dashboard/upstream_test.go`:

```go
package dashboard_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jverhoeks/escrow/internal/allow"
	"github.com/jverhoeks/escrow/internal/config"
	"github.com/jverhoeks/escrow/internal/dashboard"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/jverhoeks/escrow/internal/upstreamlog"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestUpstreamLog_ReturnsEvents(t *testing.T) {
	al, _ := allow.New("")
	cfg := config.DashboardConfig{Enabled: true, Path: "/dashboard", Username: "admin", Password: "pass", Secret: "aabbccddeeff00112233445566778899"}
	ul := upstreamlog.New(10)
	ul.Record(upstreamlog.Event{Ecosystem: "npm", URL: "https://registry.npmjs.org/a", Status: 200, Bytes: 123})
	dash := dashboard.New(cfg, eventlog.New(10), zerolog.Nop(), al, nil, nil, "", 0, ul)
	r := chi.NewRouter()
	dash.Mount(r)

	req := authenticatedRequest(t, http.MethodGet, "/dashboard/api/upstreamlog?n=10", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var out []upstreamlog.Event
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out, 1)
	require.Equal(t, "npm", out[0].Ecosystem)
}
```

- [ ] **Step 2: Add the field, route, and handler**

In `internal/dashboard/handlers.go`, add the import `"github.com/jverhoeks/escrow/internal/upstreamlog"`, add a field to `Dashboard`:

```go
	upstreamLog *upstreamlog.Log // may be nil
```

Add the route in `Mount` after the timeseries route:

```go
	protected.Get("/api/upstreamlog", d.handleUpstreamLog)
```

Create `internal/dashboard/upstream.go`:

```go
package dashboard

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/jverhoeks/escrow/internal/upstreamlog"
)

func (d *Dashboard) handleUpstreamLog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if d.upstreamLog == nil {
		json.NewEncoder(w).Encode([]upstreamlog.Event{})
		return
	}
	events := d.upstreamLog.Events(r.URL.Query().Get("eco"))
	if s := r.URL.Query().Get("n"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 && v < len(events) {
			events = events[:v]
		}
	}
	json.NewEncoder(w).Encode(events)
}
```

- [ ] **Step 3: Defer compile to Task 13; commit**

```bash
git add internal/dashboard/upstream.go internal/dashboard/upstream_test.go internal/dashboard/handlers.go
git commit -m "feat(dashboard): upstream fetch log endpoint"
```

---

## Task 10: `/api/accesslog` endpoint + parser

**Files:**
- Create: `internal/dashboard/accesslog.go`
- Modify: `internal/dashboard/handlers.go` (struct fields + route)
- Test: `internal/dashboard/accesslog_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/dashboard/accesslog_test.go`:

```go
package dashboard_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jverhoeks/escrow/internal/allow"
	"github.com/jverhoeks/escrow/internal/config"
	"github.com/jverhoeks/escrow/internal/dashboard"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestAccessLog_ParsesAndFiltersDashboardPath(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "access.log")
	lines := `127.0.0.1 - - [30/May/2026:09:17:46 +0000] "GET /npm/lodash HTTP/1.1" 200 1234 "-" "npm/11"
127.0.0.1 - - [30/May/2026:09:17:47 +0000] "GET /dashboard/api/stream HTTP/1.1" 200 0 "-" "Mozilla"
`
	require.NoError(t, os.WriteFile(logPath, []byte(lines), 0o644))

	al, _ := allow.New("")
	cfg := config.DashboardConfig{Enabled: true, Path: "/dashboard", Username: "admin", Password: "pass", Secret: "aabbccddeeff00112233445566778899"}
	dash := dashboard.New(cfg, eventlog.New(10), zerolog.Nop(), al, nil, nil, logPath, 0, nil)
	r := chi.NewRouter()
	dash.Mount(r)

	req := authenticatedRequest(t, http.MethodGet, "/dashboard/api/accesslog?n=100", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var out []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out, 1) // dashboard's own /dashboard/... request filtered out
	require.Equal(t, "/npm/lodash", out[0]["path"])
	require.Equal(t, float64(200), out[0]["status"])
}
```

- [ ] **Step 2: Implement the parser + handler**

Create `internal/dashboard/accesslog.go`:

```go
package dashboard

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// AccessLogEntry is one parsed Apache-combined line.
type AccessLogEntry struct {
	Host      string    `json:"host"`
	Timestamp time.Time `json:"timestamp"`
	Method    string    `json:"method"`
	Path      string    `json:"path"`
	Proto     string    `json:"proto"`
	Status    int       `json:"status"`
	Bytes     int64     `json:"bytes"`
	UserAgent string    `json:"user_agent"`
}

func (d *Dashboard) handleAccessLog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if d.accessLogPath == "" {
		json.NewEncoder(w).Encode([]AccessLogEntry{})
		return
	}
	n := 200
	if s := r.URL.Query().Get("n"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 && v <= maxEventsPerRequest {
			n = v
		}
	}
	entries := parseAccessLog(d.accessLogPath, d.cfg.Path, n)
	json.NewEncoder(w).Encode(entries)
}

// parseAccessLog reads the file, parses each combined-format line, drops requests
// to dashPath (the dashboard's own traffic), and returns the newest n entries.
func parseAccessLog(path, dashPath string, n int) []AccessLogEntry {
	f, err := os.Open(path)
	if err != nil {
		return []AccessLogEntry{}
	}
	defer f.Close()

	var all []AccessLogEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		e, ok := parseCombinedLine(sc.Text())
		if !ok {
			continue
		}
		if dashPath != "" && strings.HasPrefix(e.Path, dashPath) {
			continue
		}
		all = append(all, e)
	}
	// newest-first
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	if len(all) > n {
		all = all[:n]
	}
	if all == nil {
		all = []AccessLogEntry{}
	}
	return all
}

// parseCombinedLine parses: host - - [ts] "METHOD path proto" status bytes "ref" "ua"
func parseCombinedLine(line string) (AccessLogEntry, bool) {
	var e AccessLogEntry
	host, rest, ok := strings.Cut(line, " ")
	if !ok {
		return e, false
	}
	e.Host = host

	lb := strings.IndexByte(rest, '[')
	rb := strings.IndexByte(rest, ']')
	if lb < 0 || rb < lb {
		return e, false
	}
	ts, err := time.Parse("02/Jan/2006:15:04:05 -0700", rest[lb+1:rb])
	if err != nil {
		return e, false
	}
	e.Timestamp = ts
	after := rest[rb+1:]

	q1 := strings.IndexByte(after, '"')
	if q1 < 0 {
		return e, false
	}
	q2 := strings.IndexByte(after[q1+1:], '"')
	if q2 < 0 {
		return e, false
	}
	reqLine := after[q1+1 : q1+1+q2]
	parts := strings.Split(reqLine, " ")
	if len(parts) >= 3 {
		e.Method, e.Path, e.Proto = parts[0], parts[1], parts[2]
	}
	tail := strings.Fields(after[q1+1+q2+1:])
	if len(tail) >= 1 {
		e.Status, _ = strconv.Atoi(tail[0])
	}
	if len(tail) >= 2 {
		e.Bytes, _ = strconv.ParseInt(tail[1], 10, 64)
	}
	// User-agent = last quoted field.
	if i := strings.LastIndexByte(line, '"'); i > 0 {
		if j := strings.LastIndexByte(line[:i], '"'); j >= 0 {
			e.UserAgent = line[j+1 : i]
		}
	}
	return e, true
}
```

- [ ] **Step 3: Add struct fields + route**

In `internal/dashboard/handlers.go`, add fields to `Dashboard`:

```go
	accessLogPath    string // may be empty
	accessLogMaxDays int
```

Add route in `Mount` after the upstreamlog route:

```go
	protected.Get("/api/accesslog", d.handleAccessLog)
```

- [ ] **Step 4: Defer compile to Task 13; commit**

```bash
git add internal/dashboard/accesslog.go internal/dashboard/accesslog_test.go internal/dashboard/handlers.go
git commit -m "feat(dashboard): access log viewer endpoint with self-path filter"
```

---

## Task 11: `/api/cves` endpoint

**Files:**
- Create: `internal/dashboard/cves.go`
- Modify: `internal/dashboard/handlers.go` (route)
- Test: `internal/dashboard/cves_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/dashboard/cves_test.go`:

```go
package dashboard_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCVEs_ListsBlockedByVuln(t *testing.T) {
	handler, _ := newTestDashboardWithEvents(t) // includes one osv-blocked event with GHSA-x
	req := authenticatedRequest(t, http.MethodGet, "/dashboard/api/cves", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var out []struct {
		ID        string `json:"id"`
		Severity  string `json:"severity"`
		Ecosystem string `json:"ecosystem"`
		Package   string `json:"package"`
		Version   string `json:"version"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out, 1)
	require.Equal(t, "GHSA-x", out[0].ID)
	require.Equal(t, "HIGH", out[0].Severity)
	require.Equal(t, "b", out[0].Package)
}
```

- [ ] **Step 2: Implement the handler**

Create `internal/dashboard/cves.go`:

```go
package dashboard

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"
)

// CVEEntry is one (vulnerability, affected package version) pairing.
type CVEEntry struct {
	ID        string    `json:"id"`
	Severity  string    `json:"severity"`
	Ecosystem string    `json:"ecosystem"`
	Package   string    `json:"package"`
	Version   string    `json:"version"`
	Action    string    `json:"action"`
	Reason    string    `json:"reason"`
	LastSeen  time.Time `json:"last_seen"`
}

// handleCVEs returns one entry per (vuln ID × package version), newest-first,
// deduplicated so the most recent sighting of each pairing wins.
func (d *Dashboard) handleCVEs(w http.ResponseWriter, r *http.Request) {
	eco := r.URL.Query().Get("eco")
	type key struct{ id, eco, name, version string }
	seen := map[key]CVEEntry{}

	for _, e := range d.log.Events(eco) { // newest-first
		if len(e.Vulns) == 0 {
			continue
		}
		name, version := splitPackage(e.Package)
		for _, v := range e.Vulns {
			k := key{v.ID, e.Ecosystem, name, version}
			if _, ok := seen[k]; ok {
				continue // newest already recorded
			}
			seen[k] = CVEEntry{
				ID: v.ID, Severity: v.Severity, Ecosystem: e.Ecosystem,
				Package: name, Version: version, Action: e.Action,
				Reason: e.Reason, LastSeen: e.Timestamp,
			}
		}
	}

	out := make([]CVEEntry, 0, len(seen))
	for _, v := range seen {
		out = append(out, v)
	}
	sev := map[string]int{"CRITICAL": 4, "HIGH": 3, "MEDIUM": 2, "LOW": 1}
	sort.Slice(out, func(i, j int) bool {
		if sev[out[i].Severity] != sev[out[j].Severity] {
			return sev[out[i].Severity] > sev[out[j].Severity]
		}
		return out[i].LastSeen.After(out[j].LastSeen)
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}
```

- [ ] **Step 3: Register the route**

In `Mount`, after the accesslog route:

```go
	protected.Get("/api/cves", d.handleCVEs)
```

- [ ] **Step 4: Defer compile to Task 13; commit**

```bash
git add internal/dashboard/cves.go internal/dashboard/cves_test.go internal/dashboard/handlers.go
git commit -m "feat(dashboard): CVE listing endpoint from event vulns"
```

---

## Task 12: Namespace tree + size on `/api/packages`

**Files:**
- Create: `internal/dashboard/tree.go`
- Modify: `internal/dashboard/handlers.go` (route; reuse existing `handlePackages` for the flat list, add a new tree route)
- Test: `internal/dashboard/tree_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/dashboard/tree_test.go`:

```go
package dashboard

import "testing"

func TestNamespaceFor(t *testing.T) {
	cases := []struct{ eco, name, wantNS, wantLeaf string }{
		{"npm", "@scope/pkg", "@scope", "pkg"},
		{"npm", "lodash", "", "lodash"},
		{"maven", "com.google.guava:guava", "com.google.guava", "guava"},
		{"go", "golang.org/x/net", "golang.org/x", "net"},
		{"nuget", "Microsoft.AspNetCore.Mvc", "Microsoft.AspNetCore", "Mvc"},
		{"pypi", "requests", "", "requests"},
		{"cargo", "serde", "", "serde"},
	}
	for _, c := range cases {
		ns, leaf := namespaceFor(c.eco, c.name)
		if ns != c.wantNS || leaf != c.wantLeaf {
			t.Errorf("namespaceFor(%q,%q) = (%q,%q), want (%q,%q)", c.eco, c.name, ns, leaf, c.wantNS, c.wantLeaf)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/dashboard/ -run TestNamespaceFor`
Expected: FAIL — `namespaceFor` undefined. (This test is in-package `dashboard`, so it compiles independently of the `_test` package's `New` signature.)

- [ ] **Step 3: Implement namespace derivation + the tree handler**

Create `internal/dashboard/tree.go`:

```go
package dashboard

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"
)

// namespaceFor splits a package name into (namespace, leaf) per ecosystem.
// Flat ecosystems (pypi, cargo) and unscoped names return ("", name).
func namespaceFor(eco, name string) (ns, leaf string) {
	switch eco {
	case "npm":
		if strings.HasPrefix(name, "@") {
			if i := strings.IndexByte(name, '/'); i > 0 {
				return name[:i], name[i+1:]
			}
		}
	case "maven":
		if i := strings.IndexByte(name, ':'); i > 0 {
			return name[:i], name[i+1:]
		}
	case "go":
		if i := strings.LastIndexByte(name, '/'); i > 0 {
			return name[:i], name[i+1:]
		}
	case "nuget":
		if i := strings.LastIndexByte(name, '.'); i > 0 {
			return name[:i], name[i+1:]
		}
	}
	return "", name
}

// TreeVersion is a leaf: a specific package version with its status & metadata.
type TreeVersion struct {
	Version  string    `json:"version"`
	Action   string    `json:"action"`
	Signal   string    `json:"signal"`
	Reason   string    `json:"reason"`
	Size     int64     `json:"size"`   // -1 when unknown
	Cached   bool      `json:"cached"`
	CVECount int       `json:"cve_count"`
	LastSeen time.Time `json:"last_seen"`
	HitCount int       `json:"hit_count"`
}

type TreePackage struct {
	Namespace string        `json:"namespace"`
	Name      string        `json:"name"`
	Versions  []TreeVersion `json:"versions"`
}

type TreeEcosystem struct {
	Ecosystem string        `json:"ecosystem"`
	Packages  []TreePackage `json:"packages"`
}

// handlePackagesTree returns the ecosystem→namespace/package→version hierarchy.
func (d *Dashboard) handlePackagesTree(w http.ResponseWriter, r *http.Request) {
	eco := r.URL.Query().Get("eco")
	all := d.log.Events("") // newest-first

	type vkey struct{ eco, name, version string }
	type pkey struct{ eco, ns, name string }
	verSeen := map[vkey]*TreeVersion{}
	pkgVers := map[pkey][]*TreeVersion{}
	pkgOrder := []pkey{}

	for _, e := range all {
		if eco != "" && e.Ecosystem != eco {
			continue
		}
		name, version := splitPackage(e.Package)
		vk := vkey{e.Ecosystem, name, version}
		if v, ok := verSeen[vk]; ok {
			v.HitCount++
			continue
		}
		ns, leaf := namespaceFor(e.Ecosystem, name)
		tv := &TreeVersion{
			Version: version, Action: e.Action, Signal: e.Signal, Reason: e.Reason,
			Size: -1, CVECount: len(e.Vulns), LastSeen: e.Timestamp, HitCount: 1,
		}
		if d.cache != nil {
			tv.Cached = blobCached(r.Context(), d.cache, e.Ecosystem, name, version)
			if tv.Cached {
				if sz := blobSize(r.Context(), d.cache, e.Ecosystem, name, version); sz >= 0 {
					tv.Size = sz
				}
			}
		}
		verSeen[vk] = tv
		pk := pkey{e.Ecosystem, ns, leaf}
		if _, ok := pkgVers[pk]; !ok {
			pkgOrder = append(pkgOrder, pk)
		}
		pkgVers[pk] = append(pkgVers[pk], tv)
	}

	// Group packages by ecosystem.
	ecoIdx := map[string]int{}
	var out []TreeEcosystem
	for _, pk := range pkgOrder {
		i, ok := ecoIdx[pk.eco]
		if !ok {
			i = len(out)
			ecoIdx[pk.eco] = i
			out = append(out, TreeEcosystem{Ecosystem: pk.eco})
		}
		vers := make([]TreeVersion, 0, len(pkgVers[pk]))
		for _, v := range pkgVers[pk] {
			vers = append(vers, *v)
		}
		sort.Slice(vers, func(a, b int) bool { return vers[a].Version > vers[b].Version })
		out[i].Packages = append(out[i].Packages, TreePackage{Namespace: pk.ns, Name: pk.name, Versions: vers})
	}
	for i := range out {
		sort.Slice(out[i].Packages, func(a, b int) bool {
			pa, pb := out[i].Packages[a], out[i].Packages[b]
			if pa.Namespace != pb.Namespace {
				return pa.Namespace < pb.Namespace
			}
			return pa.Name < pb.Name
		})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Ecosystem < out[b].Ecosystem })

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}
```

Add a `blobSize` helper next to `blobCached` in `internal/dashboard/handlers.go` (mirrors `blobCached`'s key logic):

```go
// blobSize returns the cached blob size for ecosystems with predictable keys,
// or -1 when unknown. Mirrors blobCached.
func blobSize(ctx context.Context, c cache.Cache, ecosystem, name, version string) int64 {
	var key string
	switch ecosystem {
	case "npm":
		basename := name
		if i := strings.LastIndex(name, "/"); i >= 0 {
			basename = name[i+1:]
		}
		key = fmt.Sprintf("npm/%s/-/%s-%s.tgz", name, basename, version)
	case "cargo":
		key = fmt.Sprintf("cargo/crates/%s/%s/download", name, version)
	case "nuget":
		id := strings.ToLower(name)
		ver := strings.ToLower(version)
		key = fmt.Sprintf("nuget/pkgs/%s/%s/%s.%s.nupkg", id, ver, id, ver)
	}
	if key == "" {
		return -1
	}
	return c.BlobSize(ctx, key)
}
```

- [ ] **Step 4: Run the in-package test**

Run: `go test ./internal/dashboard/ -run TestNamespaceFor`
Expected: PASS.

- [ ] **Step 5: Register the route**

In `Mount`, after the existing `protected.Get("/api/packages", d.handlePackages)`:

```go
	protected.Get("/api/packages/tree", d.handlePackagesTree)
```

- [ ] **Step 6: Commit**

```bash
git add internal/dashboard/tree.go internal/dashboard/tree_test.go internal/dashboard/handlers.go
git commit -m "feat(dashboard): namespace package tree with best-effort size"
```

---

## Task 13: Update `dashboard.New` signature and wire everything (compile/green gate)

**Files:**
- Modify: `internal/dashboard/handlers.go:37-48` (`New`)
- Modify: `internal/dashboard/handlers_test.go:24-43` (`newTestDashboard`)
- Modify: `cmd/escrow/main.go:334`

- [ ] **Step 1: Update `New`**

Replace the `New` constructor in `internal/dashboard/handlers.go`:

```go
func New(cfg config.DashboardConfig, log *eventlog.Log, logger zerolog.Logger, allowList *allow.List, blockList *block.List, c cache.Cache, accessLogPath string, accessLogMaxDays int, upstreamLog *upstreamlog.Log) *Dashboard {
	return &Dashboard{
		cfg:              cfg,
		auth:             NewAuth(cfg.Username, cfg.Password, cfg.Secret),
		loginLimiter:     newLoginRateLimiter(),
		log:              log,
		logger:           logger,
		allowList:        allowList,
		blockList:        blockList,
		cache:            c,
		accessLogPath:    accessLogPath,
		accessLogMaxDays: accessLogMaxDays,
		upstreamLog:      upstreamLog,
	}
}
```

- [ ] **Step 2: Update the existing `newTestDashboard` helper**

In `internal/dashboard/handlers_test.go`, change the `dashboard.New(...)` call to the new signature:

```go
	dash := dashboard.New(cfg, evLog, logger, al, nil, nil, "", 0, nil)
```

- [ ] **Step 3: Update the main.go call site**

In `cmd/escrow/main.go:334`:

```go
		dash := dashboard.New(cfg.Dashboard, evLog, log.Logger, allowList, blockList, c,
			config.ExpandPath(cfg.Server.AccessLogPath), cfg.Server.AccessLogMaxDays, upstreamLog)
```

- [ ] **Step 4: Build and run the full suite**

Run: `go build ./... && go test ./...`
Expected: PASS across the repo (cache, trust, policy, eventlog, server, upstreamlog, dashboard, handlers).

- [ ] **Step 5: Commit**

```bash
git add internal/dashboard/handlers.go internal/dashboard/handlers_test.go cmd/escrow/main.go
git commit -m "feat(dashboard): wire access log, upstream log, and new endpoints"
```

---

## Self-Review (completed during planning)

- **Spec coverage:** structured CVE capture (Tasks 2–4, 11); best-effort size (Tasks 1, 12); upstream fetch log (Tasks 5–7, 9); access-log viewer w/ self-path filter (Task 10); 24h timeseries (Task 8); namespace tree (Task 12). Frontend (sidebar, theme, charts, views) is **Phase 2**, authored separately.
- **Placeholder scan:** no TBD/TODO; every code step contains full code.
- **Type consistency:** `trust.Vuln{ID,Severity}` flows `SignalReport.Vulns` → `Decision.Vulns` → `PackageEvent.Vulns`; `upstreamlog.Event` fields match between producer (Task 6) and consumer (Task 9); `dashboard.New` signature in Task 13 matches every call site (Tasks 8–12 tests + main.go).
- **Known execution ordering note:** Tasks 8–12 add handler code + tests but the `dashboard_test` package will not compile until Task 13 changes `New`. Implement handler files first; Task 13 is the green gate for the whole `dashboard` package. In-package tests that don't touch `New` (Task 12 `TestNamespaceFor`) can run earlier.

## Phase 2 (Frontend) — separate plan to follow

After this backend lands and `go test ./...` is green, author `docs/superpowers/plans/2026-05-30-escrow-dashboard-redesign-frontend.md` against the now-concrete endpoint shapes: sidebar layout, `prefers-color-scheme` theme system + persistent toggle (applied to `login.html` too), CVD-tuned status coding, multi-select ecosystem filter, the six views, and hand-rolled SVG stacked-column charts. Verification is via Playwright screenshots of light/dark + the CVD palette, not TDD.
