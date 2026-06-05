# Dashboard Egress Observability + Tabbed Settings — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the egress proxy first-class observability — a dedicated `internal/egresslog` store, Prometheus + dashboard metrics, a live egress log view, plus a tabbed Settings page and TUI parity.

**Architecture:** A new `internal/egresslog` package mirrors `internal/eventlog` (newest-first ring + optional JSONL file + SSE subscriber fan-out). The egress proxy records to it (with byte counts) instead of the shared event log, and increments Prometheus counters. The dashboard gains `/api/egresslog`, `/api/egress/stream` (SSE), and `/api/egress/stats/timeseries`, an Egress view, and a sub-tabbed Settings page; `escrow-cli tui` gains an Egress tab.

**Tech Stack:** Go 1.25 (stdlib `net/http`, `sync`), `prometheus/client_golang` (promauto), vanilla JS + bespoke inline-SVG charts in `internal/dashboard/static/index.html`, Bubble Tea TUI, testify. Module `github.com/jverhoeks/escrow`.

**Working dir:** the worktree at `/tmp/escrow-egress-ui` (branch `feat/dashboard-egress-observability`). `go` is at `/opt/homebrew/bin`; set a project-local `TMPDIR` if a `-race` build needs disk.

---

## File structure

| File | Responsibility |
|---|---|
| `internal/egresslog/log.go` (create) | Dedicated egress log: `Event`, `Stats`, ring + optional file + SSE subscribers |
| `internal/egresslog/log_test.go` (create) | Unit tests for the store |
| `internal/config/config.go` (modify) | Add top-level `EgressLogPath` |
| `internal/metrics/metrics.go` (modify) | Add `EgressRequestsTotal`, `EgressBytesTotal` |
| `internal/egress/proxy.go` (modify) | Record to egresslog (host/ip/verb/action/reason/**bytes**); metrics; keep half-close intact |
| `internal/egress/proxy_test.go` (modify) | Update record assertions → egresslog; byte counts; `-race` |
| `cmd/escrow/main.go` (modify) | Construct egresslog; pass to proxy + dashboard; reload snapshot |
| `internal/dashboard/egress.go` (create) | `handleEgressLog`, `handleEgressStream`, `handleEgressTimeseries` |
| `internal/dashboard/egress_test.go` (create) | Handler tests |
| `internal/dashboard/handlers.go` (modify) | `Dashboard.egressLog` field, `New(...)` param, `Mount` routes |
| `internal/dashboard/static/index.html` (modify) | Egress view (cards, top hosts, chart, live log) + nav tab; tabbed Settings |
| `cmd/escrow-cli/tui/client.go` (modify) | `EgressEntry`, `EgressLog`, `EgressStats` fetchers |
| `cmd/escrow-cli/tui/model.go` (modify) | Egress tab + message + load |
| `cmd/escrow-cli/tui/views.go` (modify) | `bodyEgress` + stats header |
| `docs/dashboard.md` (modify) | Document the Egress view |

---

## Task 1: `internal/egresslog` store

**Files:** Create `internal/egresslog/log.go`; Test `internal/egresslog/log_test.go`

- [ ] **Step 1: Write the failing test** — `internal/egresslog/log_test.go`:

```go
package egresslog

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ev(host, action string, bytes int64) Event {
	return Event{Timestamp: time.Now(), Host: host, Action: action, Verb: "CONNECT", Reason: "tunnel", Bytes: bytes}
}

func TestLog_RecordRecentFilter(t *testing.T) {
	l := New(3)
	l.Record(ev("a.com", "allow", 10))
	l.Record(ev("b.com", "block", 0))
	l.Record(ev("c.com", "allow", 20))
	l.Record(ev("d.com", "allow", 30)) // evicts a.com (cap 3, newest-first)

	all := l.Recent(10, "")
	require.Len(t, all, 3)
	assert.Equal(t, "d.com", all[0].Host) // newest first
	allowed := l.Recent(10, "allow")
	assert.Len(t, allowed, 2) // c, d
	for _, e := range allowed {
		assert.Equal(t, "allow", e.Action)
	}
}

func TestLog_Subscribe(t *testing.T) {
	l := New(10)
	ch, unsub := l.Subscribe()
	require.NotNil(t, ch)
	defer unsub()
	l.Record(ev("x.com", "block", 0))
	select {
	case e := <-ch:
		assert.Equal(t, "x.com", e.Host)
	case <-time.After(time.Second):
		t.Fatal("no event delivered to subscriber")
	}
}

func TestLog_Stats(t *testing.T) {
	l := New(100)
	l.Record(ev("a.com", "allow", 100))
	l.Record(ev("a.com", "allow", 50))
	l.Record(ev("evil.com", "block", 0))
	s := l.Stats(24*time.Hour, time.Hour)
	assert.Equal(t, 3, s.Total)
	assert.Equal(t, 2, s.Allowed)
	assert.Equal(t, 1, s.Blocked)
	assert.Equal(t, 2, s.DistinctHosts)
	assert.Equal(t, int64(150), s.Bytes)
	require.NotEmpty(t, s.TopAllowed)
	assert.Equal(t, "a.com", s.TopAllowed[0].Host)
	assert.Equal(t, 2, s.TopAllowed[0].Count)
	require.NotEmpty(t, s.TopBlocked)
	assert.Equal(t, "evil.com", s.TopBlocked[0].Host)
}
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/egresslog/ -v`
Expected: FAIL — `undefined: New` / `Event` / `Stats`.

- [ ] **Step 3: Implement** — `internal/egresslog/log.go`:

```go
// Package egresslog is a dedicated, newest-first log of egress-proxy decisions
// (separate from the package event log and the client access log). It mirrors
// internal/eventlog: a bounded ring, optional JSONL persistence, and SSE
// subscriber fan-out.
package egresslog

import (
	"bufio"
	"encoding/json"
	"os"
	"sort"
	"sync"
	"time"
)

const maxSubscribers = 100

// Event is one egress-proxy decision.
type Event struct {
	Timestamp time.Time `json:"timestamp"`
	Host      string    `json:"host"`
	IP        string    `json:"ip,omitempty"`
	Verb      string    `json:"verb"`   // CONNECT | GET | POST | …
	Action    string    `json:"action"` // allow | block
	Reason    string    `json:"reason"`
	Bytes     int64     `json:"bytes,omitempty"`
}

// HostCount is a host with its event count (for top-hosts tables).
type HostCount struct {
	Host  string `json:"host"`
	Count int    `json:"count"`
}

// Bucket is one time bucket of the requests-over-time series.
type Bucket struct {
	T     time.Time `json:"t"`
	Allow int       `json:"allow"`
	Block int       `json:"block"`
}

// Stats is the aggregate surfaced by the dashboard metrics view.
type Stats struct {
	Total         int         `json:"total"`
	Allowed       int         `json:"allowed"`
	Blocked       int         `json:"blocked"`
	DistinctHosts int         `json:"distinct_hosts"`
	Bytes         int64       `json:"bytes"`
	TopAllowed    []HostCount `json:"top_allowed"`
	TopBlocked    []HostCount `json:"top_blocked"`
	Series        []Bucket    `json:"series"`
}

// Log is the egress log store.
type Log struct {
	mu          sync.RWMutex
	cap         int
	events      []Event // newest-first
	subscribers map[int]chan Event
	nextID      int
	file        *os.File
}

// New returns an in-memory egress log holding up to cap events.
func New(cap int) *Log {
	if cap <= 0 {
		cap = 5000
	}
	return &Log{cap: cap, subscribers: map[int]chan Event{}}
}

// NewWithPath loads up to cap events from a JSONL file (if present) and opens
// it for append, in addition to the in-memory ring.
func NewWithPath(cap int, path string) (*Log, error) {
	l := New(cap)
	if data, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(data)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		var loaded []Event
		for sc.Scan() {
			var e Event
			if json.Unmarshal(sc.Bytes(), &e) == nil {
				loaded = append(loaded, e)
			}
		}
		data.Close()
		// keep the last cap, newest-first
		if len(loaded) > l.cap {
			loaded = loaded[len(loaded)-l.cap:]
		}
		for i := len(loaded) - 1; i >= 0; i-- {
			l.events = append(l.events, loaded[i])
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	l.file = f
	return l, nil
}

// Record prepends an event, trims to cap, appends to the file, and fans out to
// subscribers without holding the lock (drops to slow subscribers).
func (l *Log) Record(e Event) {
	l.mu.Lock()
	l.events = append([]Event{e}, l.events...)
	if len(l.events) > l.cap {
		l.events = l.events[:l.cap]
	}
	if l.file != nil {
		if b, err := json.Marshal(e); err == nil {
			l.file.Write(append(b, '\n'))
		}
	}
	subs := make([]chan Event, 0, len(l.subscribers))
	for _, ch := range l.subscribers {
		subs = append(subs, ch)
	}
	l.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// Recent returns up to n newest-first events, optionally filtered to a single
// action ("allow"|"block"); action "" means all.
func (l *Log) Recent(n int, action string) []Event {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Event, 0, len(l.events))
	for _, e := range l.events {
		if action != "" && e.Action != action {
			continue
		}
		out = append(out, e)
		if n > 0 && len(out) >= n {
			break
		}
	}
	return out
}

// Subscribe registers a buffered channel for live events. The returned cancel
// func removes it (it does NOT close the channel — a concurrent in-flight send
// from Record's snapshot may still target it).
func (l *Log) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 64)
	l.mu.Lock()
	if len(l.subscribers) >= maxSubscribers {
		l.mu.Unlock()
		return nil, nil
	}
	id := l.nextID
	l.nextID++
	l.subscribers[id] = ch
	l.mu.Unlock()
	return ch, func() {
		l.mu.Lock()
		delete(l.subscribers, id)
		l.mu.Unlock()
	}
}

// Stats aggregates the ring over the given window into counts, top hosts, bytes,
// and an allow/block time series bucketed by bucket.
func (l *Log) Stats(window, bucket time.Duration) Stats {
	l.mu.RLock()
	events := make([]Event, len(l.events))
	copy(events, l.events)
	l.mu.RUnlock()

	if bucket <= 0 {
		bucket = time.Hour
	}
	cutoff := time.Now().Add(-window)
	var s Stats
	hosts := map[string]bool{}
	allowByHost := map[string]int{}
	blockByHost := map[string]int{}
	buckets := map[time.Time]*Bucket{}
	for _, e := range events {
		if e.Timestamp.Before(cutoff) {
			continue
		}
		s.Total++
		hosts[e.Host] = true
		s.Bytes += e.Bytes
		bt := e.Timestamp.Truncate(bucket)
		b := buckets[bt]
		if b == nil {
			b = &Bucket{T: bt}
			buckets[bt] = b
		}
		if e.Action == "block" {
			s.Blocked++
			blockByHost[e.Host]++
			b.Block++
		} else {
			s.Allowed++
			allowByHost[e.Host]++
			b.Allow++
		}
	}
	s.DistinctHosts = len(hosts)
	s.TopAllowed = topHosts(allowByHost, 10)
	s.TopBlocked = topHosts(blockByHost, 10)
	for _, b := range buckets {
		s.Series = append(s.Series, *b)
	}
	sort.Slice(s.Series, func(i, j int) bool { return s.Series[i].T.Before(s.Series[j].T) })
	return s
}

func topHosts(m map[string]int, n int) []HostCount {
	out := make([]HostCount, 0, len(m))
	for h, c := range m {
		out = append(out, HostCount{Host: h, Count: c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Host < out[j].Host
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/egresslog/ -race -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**
```bash
git add internal/egresslog/
git commit -m "feat(egresslog): dedicated egress log store (ring, SSE, stats, optional file)"
```

---

## Task 2: config `EgressLogPath`

**Files:** Modify `internal/config/config.go`; Test `internal/config/config_test.go`

- [ ] **Step 1: Write the failing test** — add to `internal/config/config_test.go`:

```go
func TestLoad_EgressLogPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "escrow.toml")
	require.NoError(t, os.WriteFile(path, []byte(`egress_log_path = "/tmp/egress.jsonl"`), 0o600))
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "/tmp/egress.jsonl", cfg.EgressLogPath)
}
```

- [ ] **Step 2: Run** `go test ./internal/config/ -run TestLoad_EgressLogPath -v` → FAIL (`EgressLogPath` undefined).

- [ ] **Step 3: Implement** — in `internal/config/config.go`, add the field to `Config` right after `EventLogPath`:

```go
	EgressLogPath string `json:"egress_log_path" toml:"egress_log_path"`
```

- [ ] **Step 4: Run** `go test ./internal/config/ -run TestLoad_EgressLogPath -v` → PASS. Also `go build ./...`.

- [ ] **Step 5: Commit**
```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add top-level egress_log_path"
```

---

## Task 3: Egress proxy → egresslog + bytes + metrics

**Files:** Modify `internal/metrics/metrics.go`, `internal/egress/proxy.go`; Test `internal/egress/proxy_test.go`

This is the trickiest task: it restructures both allow paths to record **after** the copy (to capture bytes) while preserving the half-close teardown.

- [ ] **Step 1: Add Prometheus counters** — in `internal/metrics/metrics.go`, add inside the existing `var ( … )` block:

```go
	EgressRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "escrow_egress_requests_total",
		Help: "Egress proxy decisions by action",
	}, []string{"action"})

	EgressBytesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "escrow_egress_bytes_total",
		Help: "Total bytes proxied through the egress proxy (allow path)",
	})
```

- [ ] **Step 2: Write the failing test** — replace the existing event-log assertions in `internal/egress/proxy_test.go`. The proxy now takes an `*egresslog.Log`. Update `startProxy` and the recording tests:

```go
// at top of file, add import "github.com/jverhoeks/escrow/internal/egresslog"

func startProxy(t *testing.T, cfg config.EgressProxyConfig, el *egresslog.Log) string {
	t.Helper()
	pol, err := NewPolicy(cfg)
	require.NoError(t, err)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	p := New(ln.Addr().String(), pol, el)
	go func() { _ = p.serveListener(ln) }()
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String()
}

func TestProxy_ForwardsHTTPAndRecordsEgress(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello")
	}))
	defer upstream.Close()

	el := egresslog.New(10)
	sub, unsub := el.Subscribe()
	defer unsub()

	addr := startProxy(t, config.EgressProxyConfig{Policy: "forward"}, el)
	resp, err := proxyClient(addr).Get(upstream.URL)
	require.NoError(t, err)
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	select {
	case e := <-sub:
		assert.Equal(t, "allow", e.Action)
		assert.Equal(t, mustHost(t, upstream.URL), e.Host)
		assert.Equal(t, "GET", e.Verb)
		assert.Greater(t, e.Bytes, int64(0)) // "hello" body counted
	case <-time.After(2 * time.Second):
		t.Fatal("no egress event recorded")
	}
}
```

Keep the existing `TestProxy_BlocksHTTP`, `TestProxy_ConnectTunnelAllowed`, `TestProxy_ServeReturnsNilOnCtxCancel`, `TestProxy_ConnectHalfCloseDoesNotHang`, `TestProxy_BlocksHostnameResolvingToBlockedCIDR` — change their `startProxy(...)`/`New(...)` calls to pass `egresslog.New(10)` instead of `eventlog.New(10)`, and drop the eventlog import if now unused.

- [ ] **Step 3: Run** `go test ./internal/egress/ -run TestProxy_ForwardsHTTPAndRecordsEgress -v` → FAIL (New signature, no egresslog).

- [ ] **Step 4: Implement** — in `internal/egress/proxy.go`:

(a) Swap the dependency. Change the struct + constructor:
```go
import (
	// …
	"github.com/jverhoeks/escrow/internal/egresslog"
	"github.com/jverhoeks/escrow/internal/metrics"
)

type Proxy struct {
	addr      string
	policy    *Policy
	egress    *egresslog.Log // may be nil
	transport *http.Transport
	srv       *http.Server
}

func New(addr string, policy *Policy, egress *egresslog.Log) *Proxy {
	p := &Proxy{addr: addr, policy: policy, egress: egress}
	p.transport = &http.Transport{Proxy: nil, DialContext: p.dialChecked}
	return p
}
```
Remove the `eventlog` import if no longer used.

(b) Replace `record(host, action, reason string)` with a typed recorder that also bumps metrics:
```go
func (p *Proxy) recordEgress(e egresslog.Event) {
	metrics.EgressRequestsTotal.WithLabelValues(e.Action).Inc()
	if e.Bytes > 0 {
		metrics.EgressBytesTotal.Add(float64(e.Bytes))
	}
	if p.egress != nil {
		p.egress.Record(e)
	}
}
```

(c) `dialChecked` — its resolved-IP block + unresolvable records become:
```go
	// where it currently calls p.record(host, "block", ...):
	p.recordEgress(egresslog.Event{Host: host, IP: ip.String(), Verb: "DIAL", Action: "block", Reason: d.Reason + " (resolved " + ip.String() + ")"})
	// and for unresolvable:
	p.recordEgress(egresslog.Event{Host: host, Verb: "DIAL", Action: "block", Reason: "unresolvable"})
```

(d) `handleConnect` — early host block + record allow AFTER copy with summed bytes + dialed IP:
```go
func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	host := hostOnly(r.Host)
	if d := p.policy.Check(host, net.ParseIP(host)); !d.Allow {
		p.recordEgress(egresslog.Event{Host: host, Verb: "CONNECT", Action: "block", Reason: d.Reason})
		http.Error(w, "blocked by escrow egress policy", http.StatusForbidden)
		return
	}
	upstream, err := p.dialChecked(r.Context(), "tcp", r.Host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer upstream.Close()
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		return
	}
	defer client.Close()
	_, _ = io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n")
	ip := ""
	if ra, ok := upstream.RemoteAddr().(*net.TCPAddr); ok {
		ip = ra.IP.String()
	}
	var up int64
	done := make(chan struct{})
	go func() {
		up, _ = io.Copy(upstream, client)
		_ = upstream.Close()
		close(done)
	}()
	dn, _ := io.Copy(client, upstream)
	_ = client.Close()
	<-done
	p.recordEgress(egresslog.Event{Host: host, IP: ip, Verb: "CONNECT", Action: "allow", Reason: "tunnel", Bytes: up + dn})
}
```
(Note: `up` is written in the goroutine and read after `<-done` — safe, the channel close happens-before the read.)

(e) `handleHTTP` — early host block + record allow AFTER body copy with byte count:
```go
func (p *Proxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	host := r.URL.Hostname()
	if d := p.policy.Check(host, net.ParseIP(host)); !d.Allow {
		p.recordEgress(egresslog.Event{Host: host, Verb: r.Method, Action: "block", Reason: d.Reason})
		http.Error(w, "blocked by escrow egress policy", http.StatusForbidden)
		return
	}
	stripHopByHop(r.Header)
	resp, err := p.transport.RoundTrip(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	n, _ := io.Copy(w, resp.Body)
	p.recordEgress(egresslog.Event{Host: host, Verb: r.Method, Action: "allow", Reason: "forward", Bytes: n})
}
```

- [ ] **Step 5: Run** `go test ./internal/egress/ -race -count=1 -v` and `go build ./...` → all PASS, no races (the half-close tests must still pass).

- [ ] **Step 6: Commit**
```bash
git add internal/metrics/metrics.go internal/egress/proxy.go internal/egress/proxy_test.go
git commit -m "feat(egress): record to egresslog with byte counts + Prometheus egress counters"
```

---

## Task 4: main.go wiring + dashboard endpoints

**Files:** Modify `cmd/escrow/main.go`, `internal/dashboard/handlers.go`; Create `internal/dashboard/egress.go`, `internal/dashboard/egress_test.go`

- [ ] **Step 1: Construct egresslog + thread it (main.go)** — after the `upstreamLog := upstreamlog.New(5000)` line, add:
```go
	var egressLog *egresslog.Log
	if cfg.EgressLogPath != "" {
		egressLog, err = egresslog.NewWithPath(5000, config.ExpandPath(cfg.EgressLogPath))
		if err != nil {
			log.Fatal().Err(err).Msg("egress log")
		}
	} else {
		egressLog = egresslog.New(5000)
	}
```
Change the egress proxy construction to pass it: `eproxy := egress.New(fmt.Sprintf("%s:%d", cfg.Server.Host, port), pol, egressLog)`. Add the `egresslog` import. (Confirm `err` is already declared in scope; otherwise use `:=` on first use.)

- [ ] **Step 2: Dashboard field + New param + Mount routes (handlers.go)** — add `egressLog *egresslog.Log` to the `Dashboard` struct; add `egressLog *egresslog.Log` to `New(...)` after `upstreamLog *upstreamlog.Log` and set `d.egressLog = egressLog`; in `Mount`, alongside `/api/upstreamlog`:
```go
		protected.Get("/api/egresslog", d.handleEgressLog)
		protected.Get("/api/egress/stream", d.handleEgressStream)
		protected.Get("/api/egress/stats/timeseries", d.handleEgressTimeseries)
```
Then update the `dashboard.New(...)` call in `cmd/escrow/main.go` to pass `egressLog` after `upstreamLog`, and add `egressLog`'s fingerprint to the reload `restartSnapshot` paths string (append `c.EgressLogPath`).

- [ ] **Step 3: Write the failing handler test** — `internal/dashboard/egress_test.go`:

```go
package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jverhoeks/escrow/internal/egresslog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleEgressLog(t *testing.T) {
	el := egresslog.New(10)
	el.Record(egresslog.Event{Timestamp: time.Now(), Host: "a.com", Action: "allow", Verb: "CONNECT"})
	el.Record(egresslog.Event{Timestamp: time.Now(), Host: "evil.com", Action: "block", Verb: "CONNECT"})
	d := &Dashboard{egressLog: el}

	req := httptest.NewRequest(http.MethodGet, "/api/egresslog?action=block", nil)
	rr := httptest.NewRecorder()
	d.handleEgressLog(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	var out []egresslog.Event
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&out))
	require.Len(t, out, 1)
	assert.Equal(t, "evil.com", out[0].Host)
}

func TestHandleEgressTimeseries(t *testing.T) {
	el := egresslog.New(10)
	el.Record(egresslog.Event{Timestamp: time.Now(), Host: "a.com", Action: "allow"})
	d := &Dashboard{egressLog: el}
	req := httptest.NewRequest(http.MethodGet, "/api/egress/stats/timeseries", nil)
	rr := httptest.NewRecorder()
	d.handleEgressTimeseries(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	var s egresslog.Stats
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&s))
	assert.Equal(t, 1, s.Total)
}
```

- [ ] **Step 4: Run** `go test ./internal/dashboard/ -run TestHandleEgress -v` → FAIL (handlers undefined).

- [ ] **Step 5: Implement** — `internal/dashboard/egress.go`:

```go
package dashboard

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/jverhoeks/escrow/internal/egresslog"
)

func (d *Dashboard) handleEgressLog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if d.egressLog == nil {
		_ = json.NewEncoder(w).Encode([]egresslog.Event{})
		return
	}
	n := 500
	if s := r.URL.Query().Get("n"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 && v <= 5000 {
			n = v
		}
	}
	_ = json.NewEncoder(w).Encode(d.egressLog.Recent(n, r.URL.Query().Get("action")))
}

func (d *Dashboard) handleEgressTimeseries(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if d.egressLog == nil {
		_ = json.NewEncoder(w).Encode(egresslog.Stats{})
		return
	}
	window := 24 * time.Hour
	if v, err := time.ParseDuration(r.URL.Query().Get("window")); err == nil && v > 0 {
		window = v
	}
	bucket := time.Hour
	if v, err := time.ParseDuration(r.URL.Query().Get("bucket")); err == nil && v > 0 {
		bucket = v
	}
	_ = json.NewEncoder(w).Encode(d.egressLog.Stats(window, bucket))
}

// handleEgressStream is an SSE feed of egress events (mirrors handleStream).
func (d *Dashboard) handleEgressStream(w http.ResponseWriter, r *http.Request) {
	if d.egressLog == nil {
		http.Error(w, "egress log unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})
	ch, unsub := d.egressLog.Subscribe()
	if ch == nil {
		http.Error(w, "too many subscribers", http.StatusServiceUnavailable)
		return
	}
	defer unsub()
	flush := func() { _ = rc.Flush() }
	_, _ = w.Write([]byte(": connected\n\n"))
	flush()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			_, _ = w.Write([]byte(": ping\n\n"))
			flush()
		case e := <-ch:
			b, err := json.Marshal(e)
			if err != nil {
				continue
			}
			_, _ = w.Write([]byte("data: " + string(b) + "\n\n"))
			flush()
		}
	}
}
```

(Cross-check `handleStream` in `handlers.go` for the exact `http.NewResponseController`/flush idiom this repo uses and match it.)

- [ ] **Step 6: Run** `go test ./internal/dashboard/ -run TestHandleEgress -v && go build ./...` → PASS + build OK. Also `go test ./... -count=1` to confirm nothing else broke (esp. any test constructing `dashboard.New` with the old arity — update those call sites).

- [ ] **Step 7: Commit**
```bash
git add cmd/escrow/main.go internal/dashboard/handlers.go internal/dashboard/egress.go internal/dashboard/egress_test.go
git commit -m "feat(dashboard): egresslog wiring + /api/egresslog, /api/egress/stream, /api/egress/stats/timeseries"
```

---

## Task 5: Dashboard Egress view (frontend)

**Files:** Modify `internal/dashboard/static/index.html`. No JS unit-test harness — verify by running the app (Step 5).

- [ ] **Step 1: Add the nav entry + view container.** Add `egress: 'Egress'` to the `MORE_VIEWS` map (near line 2259) and a `<button onclick="pickMore('egress')">Egress</button>` in the `more-dropdown`. Add `'egress'` to the view-visibility array in `setTab()` (line ~2294). Add a `view-egress` panel after `view-upstream`:

```html
<div id="view-egress" class="panel" style="display:none;">
  <div class="cards">
    <div class="card"><div class="card-n" id="eg-total">0</div><div class="card-l">requests</div></div>
    <div class="card"><div class="card-n" id="eg-allow">0</div><div class="card-l">allowed</div></div>
    <div class="card"><div class="card-n" id="eg-block">0</div><div class="card-l">blocked</div></div>
    <div class="card"><div class="card-n" id="eg-hosts">0</div><div class="card-l">distinct hosts</div></div>
    <div class="card"><div class="card-n" id="eg-bytes">0</div><div class="card-l">bytes</div></div>
  </div>
  <div id="chart-egress" class="chart"></div>
  <div class="two-col">
    <div><h3>Top allowed</h3><table id="eg-top-allow"></table></div>
    <div><h3>Top blocked</h3><table id="eg-top-block"></table></div>
  </div>
  <div class="feed-head"><span id="eg-count">0 events</span>
    <select id="eg-filter" onchange="loadEgressLog()"><option value="">all</option><option value="allow">allowed</option><option value="block">blocked</option></select>
  </div>
  <table id="eg-feed"></table>
</div>
```
(Reuse existing `.card`/`.chart`/`.feed-head` classes — match the markup of the Analytics + Live views; adjust class names to those actually defined in the file.)

- [ ] **Step 2: Add the JS.** Add functions mirroring `loadUpstream`/`connect`/`updateCharts`:

```js
let egressEs = null;
function loadEgressLog() {
  const action = document.getElementById('eg-filter').value;
  fetch(BASE + '/api/egresslog?n=500' + (action ? '&action=' + action : ''))
    .then(r => r.json()).then(rows => {
      const t = document.getElementById('eg-feed'); t.innerHTML = '';
      rows.forEach(e => t.appendChild(egressRow(e)));
      document.getElementById('eg-count').textContent = rows.length + ' events';
    });
  fetch(BASE + '/api/egress/stats/timeseries').then(r => r.json()).then(s => {
    document.getElementById('eg-total').textContent = s.total;
    document.getElementById('eg-allow').textContent = s.allowed;
    document.getElementById('eg-block').textContent = s.blocked;
    document.getElementById('eg-hosts').textContent = s.distinct_hosts;
    document.getElementById('eg-bytes').textContent = fmtBytes(s.bytes);
    renderTopHosts('eg-top-allow', s.top_allowed);
    renderTopHosts('eg-top-block', s.top_blocked);
    renderEgressChart(s.series);
  });
  egressConnect();
}
function egressRow(e) {
  const tr = document.createElement('tr');
  const badge = e.action === 'block' ? '✕' : '✓';
  tr.innerHTML = `<td>${new Date(e.timestamp).toLocaleTimeString()}</td><td>${badge} ${e.action}</td><td>${e.verb||''}</td><td>${e.host}</td><td>${e.ip||''}</td><td>${e.reason||''}</td><td>${e.bytes?fmtBytes(e.bytes):''}</td>`;
  return tr;
}
function renderTopHosts(id, rows) {
  const t = document.getElementById(id); t.innerHTML = '';
  (rows||[]).forEach(h => { const tr=document.createElement('tr'); tr.innerHTML=`<td>${h.host}</td><td>${h.count}</td>`; t.appendChild(tr); });
}
function renderEgressChart(series) {
  const buckets = (series||[]).map(b => b.t);
  renderStackedChart('chart-egress', buckets, { allow: (series||[]).map(b=>b.allow), block: (series||[]).map(b=>b.block) });
}
function egressConnect() {
  if (egressEs) { egressEs.close(); egressEs = null; }
  egressEs = new EventSource(BASE + '/api/egress/stream');
  egressEs.onmessage = ev => {
    const e = JSON.parse(ev.data);
    const filter = document.getElementById('eg-filter').value;
    if (filter && e.action !== filter) return;
    const t = document.getElementById('eg-feed');
    t.insertBefore(egressRow(e), t.firstChild);
  };
  egressEs.onerror = () => { egressEs.close(); setTimeout(egressConnect, 3000); };
}
function fmtBytes(n){ if(!n)return '0'; const u=['B','KB','MB','GB']; let i=0; while(n>=1024&&i<u.length-1){n/=1024;i++;} return n.toFixed(i?1:0)+u[i]; }
```
In `setTab()`, add `if (tab === 'egress') loadEgressLog();` and close `egressEs` when leaving the tab (mirror how the Live feed manages `es`). `renderStackedChart` already exists (line ~1466); pass an `{allow, block}` series map — confirm its 3rd-arg shape and add `allow`/`block` colors to the color dict it uses.

- [ ] **Step 3: Build + embed check.** Run `go build ./cmd/escrow` (index.html is embedded; this confirms it still embeds/compiles).

- [ ] **Step 4: Commit**
```bash
git add internal/dashboard/static/index.html
git commit -m "feat(dashboard): Egress view — cards, top hosts, requests chart, live SSE log"
```

- [ ] **Step 5: Verify by running the app.** Start escrow with `[egress_proxy] enabled=true policy="forward"` on a test port (temp data dir), drive a few requests + a blocked host through the egress proxy (`https_proxy=… curl …`), open `/dashboard`, switch to the Egress tab, and confirm: cards populate, top-hosts tables fill, the chart draws, and live rows appear via SSE. Capture a screenshot. (Use Playwright MCP if available.)

---

## Task 6: Tabbed Settings (frontend)

**Files:** Modify `internal/dashboard/static/index.html`. Verify by running (Step 4).

- [ ] **Step 1: Add a settings sub-tab strip + grouping.** In the `view-settings` panel, above the form container, add:
```html
<div id="settings-tabs" class="subtabs">
  <button class="subtab active" data-g="general" onclick="setSettingsTab('general')">General</button>
  <button class="subtab" data-g="policy" onclick="setSettingsTab('policy')">Policy</button>
  <button class="subtab" data-g="egress" onclick="setSettingsTab('egress')">Egress</button>
  <button class="subtab" data-g="advanced" onclick="setSettingsTab('advanced')">Advanced</button>
</div>
```

- [ ] **Step 2: Group sections in `renderSettings()`.** Add a grouping map and tag each rendered section card with `data-group`:
```js
const SETTINGS_GROUPS = {
  server:'general', storage:'general', dashboard:'general',
  policy:'policy', allow:'policy', block:'policy', rescan:'policy',
  egress_proxy:'egress',
  alerts:'advanced', cireport:'advanced', cache:'advanced'
};
let settingsTab = 'general';
function settingsGroupOf(section){ return SETTINGS_GROUPS[section] || 'advanced'; } // unknown → Advanced
function setSettingsTab(g){
  settingsTab = g;
  document.querySelectorAll('#settings-tabs .subtab').forEach(b => b.classList.toggle('active', b.dataset.g === g));
  document.querySelectorAll('#view-settings .settings-section').forEach(card => {
    card.style.display = card.dataset.group === g ? 'block' : 'none';
  });
}
```
In `renderSettings()` where each section card is created, set `card.dataset.group = settingsGroupOf(section);`. After rendering, call `setSettingsTab(settingsTab)` to apply visibility. (Scalar top-level keys that today render in a "general" card stay in `general`.)

- [ ] **Step 3: Minimal CSS** for `.subtabs`/`.subtab`/`.subtab.active` (mirror the existing `.nav-tab` styles).

- [ ] **Step 4: Build + verify.** `go build ./cmd/escrow`; run the app, open Settings, confirm the four sub-tabs switch the visible section groups and Save still works (POST `/api/settings` unchanged). Screenshot.

- [ ] **Step 5: Commit**
```bash
git add internal/dashboard/static/index.html
git commit -m "feat(dashboard): tabbed Settings (General/Policy/Egress/Advanced)"
```

---

## Task 7: TUI Egress tab

**Files:** Modify `cmd/escrow-cli/tui/client.go`, `model.go`, `views.go`; Test `cmd/escrow-cli/tui/client_test.go`

- [ ] **Step 1: Write the failing test** — add to `cmd/escrow-cli/tui/client_test.go`:
```go
func TestClient_EgressLog(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/dashboard/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "escrow_session", Value: "ok", Path: "/"})
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/dashboard/api/egresslog", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"timestamp":"2026-06-05T10:00:00Z","host":"a.com","action":"allow","verb":"CONNECT","reason":"tunnel","bytes":42}]`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, err := NewClient(srv.URL, "/dashboard", "root", "escrow")
	require.NoError(t, err)
	require.NoError(t, c.Login())
	rows, err := c.EgressLog(50)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "a.com", rows[0].Host)
	require.Equal(t, "allow", rows[0].Action)
}
```

- [ ] **Step 2: Run** `go test ./cmd/escrow-cli/tui/ -run TestClient_EgressLog -v` → FAIL (`EgressLog`/`EgressEntry` undefined).

- [ ] **Step 3: Implement client (client.go):**
```go
type EgressEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Host      string    `json:"host"`
	IP        string    `json:"ip"`
	Verb      string    `json:"verb"`
	Action    string    `json:"action"`
	Reason    string    `json:"reason"`
	Bytes     int64     `json:"bytes"`
}

func (c *Client) EgressLog(n int) ([]EgressEntry, error) {
	var e []EgressEntry
	return e, c.getJSON(fmt.Sprintf("/api/egresslog?n=%d", n), &e)
}
```

- [ ] **Step 4: Run** `go test ./cmd/escrow-cli/tui/ -run TestClient_EgressLog -v` → PASS.

- [ ] **Step 5: Add the tab (model.go + views.go).**
  - `model.go`: append `"Egress"` to `tabNames`; add `egress []EgressEntry` to `Model`; add `type egressMsg struct{ rows []EgressEntry }`; in `Update` add `case egressMsg: m.egress = msg.rows`; in `loadTab` add the new index case calling `c.EgressLog(200)`; **change the number-key guard from `<= '6'` to `<= '7'`**.
  - `views.go`: add `bodyEgress(w, maxLines int) string` modeled exactly on `bodyUpstream` — columns `TIME · ACT · VERB · HOST · IP · REASON · BYTES`, coloring the action (✓ allow / ✕ block). Add `case <new index>: return m.bodyEgress(w, maxLines)` in `body()`. Update the footer help `Tab/1-6` → `Tab/1-7`.

- [ ] **Step 6: Run** `go test ./cmd/escrow-cli/... -count=1 && go build ./cmd/escrow-cli` → PASS + build OK (the existing TUI render smoke test exercises all tabs).

- [ ] **Step 7: Commit**
```bash
git add cmd/escrow-cli/tui/
git commit -m "feat(tui): Egress tab (live egress log + client fetcher)"
```

---

## Task 8: Docs

**Files:** Modify `docs/dashboard.md`

- [ ] **Step 1:** Add an "Egress" subsection to `docs/dashboard.md` describing the new view (cards, top hosts, requests-over-time chart, live SSE log, allow/block filter), the new endpoints (`/api/egresslog`, `/api/egress/stream`, `/api/egress/stats/timeseries`), the Prometheus metrics (`escrow_egress_requests_total{action}`, `escrow_egress_bytes_total`), the `egress_log_path` config, and the tabbed Settings layout. Cross-link from `docs/docker.md`.

- [ ] **Step 2: Commit**
```bash
git add docs/dashboard.md
git commit -m "docs: dashboard Egress view + tabbed settings"
```

---

## Self-review

**Spec coverage:** egresslog store → T1 ✓; egress-leaves-eventlog + bytes + Prometheus → T3 ✓; config path → T2 ✓; dashboard endpoints (log/stream/stats) → T4 ✓; dashboard view (cards/top-hosts/chart/live log) → T5 ✓; tabbed settings → T6 ✓; TUI parity → T7 ✓; docs → T8 ✓.

**Placeholder scan:** every Go step has complete code; frontend steps give the actual JS/HTML to add (with a "match existing class names / `renderStackedChart` arg shape" caveat to confirm against the file, since the exact CSS class names + chart signature must be read live). No TBD/TODO.

**Type consistency:** `egresslog.Event` fields (Host/IP/Verb/Action/Reason/Bytes/Timestamp) are identical across T1 (store), T3 (proxy records), T4 (handler tests), T5 (`egressRow` JSON keys: `host/ip/verb/action/reason/bytes/timestamp`), T7 (`EgressEntry`). `Stats` JSON keys (`total/allowed/blocked/distinct_hosts/bytes/top_allowed/top_blocked/series`) match T5's `loadEgressLog` reads. `New(addr, policy, *egresslog.Log)` consistent T3↔main.go T4. `Recent(n, action)` / `Stats(window, bucket)` consistent T1↔T4.

**Open risks (carry from spec):** byte-counting in `handleConnect` must not reintroduce the half-close leak — covered by keeping the existing teardown and the `-race` regression tests (T3 Step 5). Frontend class names + `renderStackedChart` 3rd-arg shape must be confirmed against the live `index.html` (noted in T5/T6).

---

## Execution handoff

**Plan complete and saved to `docs/superpowers/plans/2026-06-05-dashboard-egress-observability.md`. Two execution options:**

**1. Subagent-Driven (recommended)** — fresh subagent per task, two-stage review between tasks.

**2. Inline Execution** — execute here with checkpoints.

**Which approach?**
