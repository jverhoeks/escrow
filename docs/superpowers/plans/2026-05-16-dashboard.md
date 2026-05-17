# Escrow Dashboard + Metrics Wiring Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire Prometheus counters into npm/PyPI handlers, add an in-memory event log, and serve a password-protected dark-terminal-style dashboard with real-time SSE updates and ecosystem filtering.

**Architecture:** Handlers record every policy decision to a shared `eventlog.Log` ring buffer (cap 500) and increment Prometheus counters. The dashboard reads the log via SSE (`/dashboard/api/stream`) and an initial REST fetch (`/dashboard/api/events`). Auth uses an HMAC-SHA256 session cookie set on POST login; all dashboard routes are protected. The entire frontend is a single embedded `index.html` with vanilla JS using DOM methods (not innerHTML) for user-data fields to avoid XSS.

**Tech Stack:** Go 1.26, `crypto/rand` + `crypto/hmac` (auth), `go:embed` (static assets), `text/event-stream` SSE (no library), vanilla JS (no framework, no innerHTML for dynamic data).

---

## File Map

```
internal/eventlog/log.go          PackageEvent, Log ring buffer, SSE fan-out
internal/eventlog/log_test.go     tests
internal/dashboard/auth.go        HMAC cookie helpers, login/logout handlers, middleware
internal/dashboard/handlers.go    SSE stream, /api/events, /api/stats, page routing
internal/dashboard/embed.go       go:embed static/
internal/dashboard/static/login.html   login form
internal/dashboard/static/index.html   dashboard SPA
internal/handler/npm/handler.go   MODIFY: add evLog, record events, counter .Inc()
internal/handler/pypi/handler.go  MODIFY: add evLog, record events, counter .Inc()
internal/config/config.go         MODIFY: DashboardConfig struct, GenerateIfMissing()
cmd/escrow/main.go                MODIFY: call GenerateIfMissing, wire evLog, mount dashboard
```

---

### Task 1: Metrics wiring in npm and PyPI handlers

**Files:**
- Modify: `internal/handler/npm/handler.go`
- Modify: `internal/handler/pypi/handler.go`

- [ ] **Step 1: Add counter calls to npm `filterManifest`**

In `internal/handler/npm/handler.go`, add `"github.com/jverhoeks/escrow/internal/metrics"` to imports.

In `filterManifest`, replace the block that handles `policy.ActionBlock` with:

```go
decision := h.policy.Evaluate(result)
metrics.RequestsTotal.WithLabelValues(string(pkg.Ecosystem), string(decision.Action)).Inc()
if decision.Action == policy.ActionBlock {
    metrics.BlocksTotal.WithLabelValues(string(pkg.Ecosystem), decision.Signal).Inc()
    blocked[version] = true
    delete(versions, version)
}
```

- [ ] **Step 2: Add counter calls to PyPI `versionAllowed`**

In `internal/handler/pypi/handler.go`, add `"github.com/jverhoeks/escrow/internal/metrics"` to imports.

In `versionAllowed`, replace the final lines with:

```go
d := h.policy.Evaluate(result)
metrics.RequestsTotal.WithLabelValues(string(pkg.Ecosystem), string(d.Action)).Inc()
if d.Action == policy.ActionBlock {
    metrics.BlocksTotal.WithLabelValues(string(pkg.Ecosystem), d.Signal).Inc()
}
return d.Action != policy.ActionBlock
```

- [ ] **Step 3: Build and verify counters appear after a request**

```bash
cd /Users/jjverhoeks/src/tries/2026-05-16-escrow
go build ./...
./escrow-darwin-arm64 /tmp/escrow-test.toml &
sleep 2
curl -sf "http://localhost:8890/once" > /dev/null
sleep 1
curl -sf http://localhost:8890/metrics | grep "escrow_requests_total"
kill %1
```

Expected: `escrow_requests_total{action="allow",ecosystem="npm"} 1`

- [ ] **Step 4: Run tests**

```bash
go test ./...
```

Expected: all 38 tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/handler/npm/handler.go internal/handler/pypi/handler.go
git commit -m "feat: wire escrow_requests_total and escrow_blocks_total into handlers"
```

---

### Task 2: EventLog — ring buffer with SSE fan-out

**Files:**
- Create: `internal/eventlog/log.go`
- Create: `internal/eventlog/log_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/eventlog/log_test.go`:

```go
package eventlog_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/eventlog"
)

func TestLog_RecordAndRetrieve(t *testing.T) {
	l := eventlog.New(10)
	l.Record(eventlog.PackageEvent{Ecosystem: "npm", Package: "lodash@4.17.21", Action: "block", Signal: "osv", Reason: "CVE"})
	l.Record(eventlog.PackageEvent{Ecosystem: "pypi", Package: "requests@2.31.0", Action: "allow"})
	events := l.Events("")
	require.Len(t, events, 2)
	assert.Equal(t, "requests@2.31.0", events[0].Package, "newest first")
}

func TestLog_FilterByEcosystem(t *testing.T) {
	l := eventlog.New(10)
	l.Record(eventlog.PackageEvent{Ecosystem: "npm", Package: "a"})
	l.Record(eventlog.PackageEvent{Ecosystem: "pypi", Package: "b"})
	l.Record(eventlog.PackageEvent{Ecosystem: "npm", Package: "c"})
	assert.Len(t, l.Events("npm"), 2)
	assert.Len(t, l.Events("pypi"), 1)
}

func TestLog_RingBufferCap(t *testing.T) {
	l := eventlog.New(3)
	for i := 0; i < 5; i++ {
		l.Record(eventlog.PackageEvent{Package: fmt.Sprintf("pkg-%d", i)})
	}
	events := l.Events("")
	assert.Len(t, events, 3)
	assert.Equal(t, "pkg-4", events[0].Package, "newest first")
}

func TestLog_Subscribe(t *testing.T) {
	l := eventlog.New(10)
	ch, unsub := l.Subscribe()
	defer unsub()
	go func() {
		time.Sleep(10 * time.Millisecond)
		l.Record(eventlog.PackageEvent{Package: "test@1.0.0", Action: "block"})
	}()
	select {
	case e := <-ch:
		assert.Equal(t, "test@1.0.0", e.Package)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout waiting for event")
	}
}

func TestLog_Stats(t *testing.T) {
	l := eventlog.New(10)
	l.Record(eventlog.PackageEvent{Package: "lodash@1", Action: "block"})
	l.Record(eventlog.PackageEvent{Package: "lodash@2", Action: "block"})
	l.Record(eventlog.PackageEvent{Package: "axios@1", Action: "block"})
	l.Record(eventlog.PackageEvent{Package: "once@1", Action: "allow"})
	l.Record(eventlog.PackageEvent{Package: "ms@1", Action: "warn"})
	s := l.Stats()
	assert.Equal(t, 3, s.Blocked)
	assert.Equal(t, 1, s.Warned)
	assert.Equal(t, 1, s.Allowed)
	require.NotEmpty(t, s.TopBlocked)
	assert.Equal(t, "lodash", s.TopBlocked[0].Package)
	assert.Equal(t, 2, s.TopBlocked[0].Count)
}
```

- [ ] **Step 2: Run — expect compile error**

```bash
go test ./internal/eventlog/... -v
```

- [ ] **Step 3: Create `internal/eventlog/log.go`**

```go
package eventlog

import (
	"strings"
	"sync"
	"time"
)

type PackageEvent struct {
	Timestamp time.Time `json:"timestamp"`
	Ecosystem string    `json:"ecosystem"`
	Package   string    `json:"package"`
	Action    string    `json:"action"`
	Signal    string    `json:"signal"`
	Reason    string    `json:"reason"`
}

type Stats struct {
	Blocked    int        `json:"blocked"`
	Warned     int        `json:"warned"`
	Allowed    int        `json:"allowed"`
	TopBlocked []TopEntry `json:"top_blocked"`
}

type TopEntry struct {
	Package string `json:"package"`
	Count   int    `json:"count"`
}

type Log struct {
	mu          sync.RWMutex
	cap         int
	events      []PackageEvent
	subscribers map[int]chan PackageEvent
	nextID      int
}

func New(cap int) *Log {
	return &Log{cap: cap, subscribers: make(map[int]chan PackageEvent)}
}

func (l *Log) Record(e PackageEvent) {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	l.mu.Lock()
	l.events = append([]PackageEvent{e}, l.events...)
	if len(l.events) > l.cap {
		l.events = l.events[:l.cap]
	}
	subs := make(map[int]chan PackageEvent, len(l.subscribers))
	for id, ch := range l.subscribers {
		subs[id] = ch
	}
	l.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- e:
		default:
		}
	}
}

func (l *Log) Events(eco string) []PackageEvent {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]PackageEvent, 0, len(l.events))
	for _, e := range l.events {
		if eco == "" || e.Ecosystem == eco {
			out = append(out, e)
		}
	}
	return out
}

func (l *Log) Subscribe() (<-chan PackageEvent, func()) {
	ch := make(chan PackageEvent, 64)
	l.mu.Lock()
	id := l.nextID
	l.nextID++
	l.subscribers[id] = ch
	l.mu.Unlock()
	return ch, func() {
		l.mu.Lock()
		delete(l.subscribers, id)
		l.mu.Unlock()
		close(ch)
	}
}

func (l *Log) Stats() Stats {
	l.mu.RLock()
	defer l.mu.RUnlock()
	s := Stats{}
	counts := map[string]int{}
	for _, e := range l.events {
		switch e.Action {
		case "block":
			s.Blocked++
			counts[packageName(e.Package)]++
		case "warn":
			s.Warned++
		case "allow":
			s.Allowed++
		}
	}
	type kv struct{ k string; v int }
	var sorted []kv
	for k, v := range counts {
		sorted = append(sorted, kv{k, v})
	}
	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].v > sorted[i].v {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	limit := 3
	if len(sorted) < limit {
		limit = len(sorted)
	}
	for _, kv := range sorted[:limit] {
		s.TopBlocked = append(s.TopBlocked, TopEntry{Package: kv.k, Count: kv.v})
	}
	return s
}

func packageName(pkg string) string {
	if i := strings.LastIndex(pkg, "@"); i > 0 {
		return pkg[:i]
	}
	return pkg
}
```

- [ ] **Step 4: Run tests — expect PASS**

```bash
go test ./internal/eventlog/... -v
```

Expected: 5 tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/eventlog/
git commit -m "feat: eventlog — ring buffer with SSE fan-out and stats"
```

---

### Task 3: Dashboard config + first-boot generation

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Add DashboardConfig to config.go**

Add after `AlertsConfig`:

```go
type DashboardConfig struct {
	Enabled  bool   `toml:"enabled"`
	Path     string `toml:"path"`
	Username string `toml:"username"`
	Password string `toml:"password"`
	Secret   string `toml:"secret"`
}
```

Add `Dashboard DashboardConfig` to the `Config` struct and update `DefaultConfig()`:

```go
func DefaultConfig() Config {
	return Config{
		Server:     ServerConfig{Host: "0.0.0.0", Port: 7888, LogLevel: "info"},
		Storage:    StorageConfig{Backend: "disk", Disk: DiskConfig{Path: "./escrow-cache"}},
		Ecosystems: EcosystemConfig{NPM: true, PyPI: true},
		Dashboard:  DashboardConfig{Enabled: true, Path: "/dashboard"},
	}
}
```

- [ ] **Step 2: Add GenerateIfMissing and helpers**

Add `"crypto/rand"` to imports, then add after `Load`:

```go
func GenerateIfMissing(path string) (bool, string, error) {
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		return false, "", nil
	}
	secret, err := randomHex(32)
	if err != nil {
		return false, "", fmt.Errorf("generate secret: %w", err)
	}
	password, err := randomAlpha(12)
	if err != nil {
		return false, "", fmt.Errorf("generate password: %w", err)
	}
	cfg := DefaultConfig()
	cfg.Dashboard.Password = password
	cfg.Dashboard.Secret = secret
	content := fmt.Sprintf(`# Generated by escrow on first boot.

[server]
  host      = %q
  port      = %d
  log_level = %q

[storage]
  backend = "disk"
  [storage.disk]
    path = "./escrow-cache"

[ecosystems]
  npm  = true
  pypi = true

[dashboard]
  enabled  = true
  path     = "/dashboard"
  username = "admin"
  password = %q
  secret   = %q

[alerts]
  webhook_url = ""
`,
		cfg.Server.Host, cfg.Server.Port, cfg.Server.LogLevel,
		password, secret,
	)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return false, "", fmt.Errorf("write config: %w", err)
	}
	msg := fmt.Sprintf("Generated %s\n  username: admin\n  password: %s\n  url:      http://localhost:%d%s",
		path, password, cfg.Server.Port, cfg.Dashboard.Path)
	return true, msg, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", b), nil
}

func randomAlpha(n int) (string, error) {
	const chars = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b), nil
}
```

- [ ] **Step 3: Add tests to config_test.go**

```go
func TestGenerateIfMissing_CreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sentinel.toml")
	generated, msg, err := config.GenerateIfMissing(path)
	require.NoError(t, err)
	assert.True(t, generated)
	assert.Contains(t, msg, "username: admin")

	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.True(t, cfg.Dashboard.Enabled)
	assert.Equal(t, "admin", cfg.Dashboard.Username)
	assert.Len(t, cfg.Dashboard.Secret, 64)
}

func TestGenerateIfMissing_SkipsExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sentinel.toml")
	os.WriteFile(path, []byte("[server]\n  port = 9999\n"), 0o644)
	generated, _, err := config.GenerateIfMissing(path)
	require.NoError(t, err)
	assert.False(t, generated)
	cfg, _ := config.Load(path)
	assert.Equal(t, 9999, cfg.Server.Port)
}
```

- [ ] **Step 4: Run tests — expect PASS**

```bash
go test ./internal/config/... -v
```

Expected: 6 tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat: config — DashboardConfig and first-boot generation"
```

---

### Task 4: Dashboard auth

**Files:**
- Create: `internal/dashboard/auth.go`
- Create: `internal/dashboard/auth_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/dashboard/auth_test.go`:

```go
package dashboard_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/dashboard"
)

func TestAuth_SetAndVerify(t *testing.T) {
	a := dashboard.NewAuth("admin", "secret", "aabbccddeeff00112233445566778899")
	w := httptest.NewRecorder()
	a.SetCookie(w, "admin")
	cookies := w.Result().Cookies()
	require.Len(t, cookies, 1)
	assert.Equal(t, "escrow_session", cookies[0].Name)
	assert.True(t, cookies[0].HttpOnly)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookies[0])
	assert.True(t, a.IsValid(req))
}

func TestAuth_TamperedCookie(t *testing.T) {
	a := dashboard.NewAuth("admin", "secret", "aabbccddeeff00112233445566778899")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "escrow_session", Value: "tampered.value"})
	assert.False(t, a.IsValid(req))
}

func TestAuth_Credentials(t *testing.T) {
	a := dashboard.NewAuth("admin", "pass123", "aabbccddeeff00112233445566778899")
	assert.True(t, a.CheckCredentials("admin", "pass123"))
	assert.False(t, a.CheckCredentials("admin", "wrong"))
	assert.False(t, a.CheckCredentials("root", "pass123"))
}
```

- [ ] **Step 2: Create `internal/dashboard/auth.go`**

```go
package dashboard

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const cookieName = "escrow_session"
const cookieTTL = 24 * time.Hour

type Auth struct {
	username string
	password string
	secret   []byte
}

func NewAuth(username, password, secret string) *Auth {
	return &Auth{username: username, password: password, secret: []byte(secret)}
}

func (a *Auth) CheckCredentials(username, password string) bool {
	return username == a.username && password == a.password
}

func (a *Auth) SetCookie(w http.ResponseWriter, username string) {
	expiry := time.Now().Add(cookieTTL).Unix()
	payload := fmt.Sprintf("%s|%d", username, expiry)
	value := base64.URLEncoding.EncodeToString([]byte(payload)) + "." + a.sign(payload)
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(cookieTTL.Seconds()),
	})
}

func (a *Auth) IsValid(r *http.Request) bool {
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	parts := strings.SplitN(cookie.Value, ".", 2)
	if len(parts) != 2 {
		return false
	}
	payloadBytes, err := base64.URLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	payload := string(payloadBytes)
	if a.sign(payload) != parts[1] {
		return false
	}
	fields := strings.SplitN(payload, "|", 2)
	if len(fields) != 2 {
		return false
	}
	expiry, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return false
	}
	return time.Now().Unix() < expiry
}

func (a *Auth) ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1})
}

func (a *Auth) Middleware(loginPath string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !a.IsValid(r) {
				http.Redirect(w, r, loginPath, http.StatusFound)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (a *Auth) sign(payload string) string {
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(payload))
	return base64.URLEncoding.EncodeToString(mac.Sum(nil))
}
```

- [ ] **Step 3: Run tests — expect PASS**

```bash
go test ./internal/dashboard/... -v -run TestAuth
```

Expected: 3 tests pass.

- [ ] **Step 4: Commit**

```bash
git add internal/dashboard/
git commit -m "feat: dashboard auth — HMAC session cookie"
```

---

### Task 5: Login and dashboard HTML

**Files:**
- Create: `internal/dashboard/static/login.html`
- Create: `internal/dashboard/static/index.html`

- [ ] **Step 1: Create `internal/dashboard/static/login.html`**

```html
<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>escrow login</title>
<style>
* { box-sizing: border-box; margin: 0; padding: 0; }
body { background: #0a0f1a; color: #e2e8f0; font-family: 'SF Mono','Fira Code','Courier New',monospace; display: flex; align-items: center; justify-content: center; min-height: 100vh; }
.box { background: #0f1929; border: 1px solid #1e3a5f; border-radius: 6px; padding: 40px; width: 340px; }
.logo { color: #60a5fa; font-size: 18px; font-weight: bold; letter-spacing: 3px; margin-bottom: 6px; }
.sub { color: #475569; font-size: 11px; margin-bottom: 32px; }
label { display: block; color: #64748b; font-size: 10px; letter-spacing: 1px; text-transform: uppercase; margin-bottom: 6px; }
input { width: 100%; background: #0a0f1a; border: 1px solid #1e3a5f; border-radius: 4px; color: #e2e8f0; font-family: inherit; font-size: 13px; padding: 10px 12px; outline: none; margin-bottom: 20px; }
input:focus { border-color: #3b82f6; }
button { width: 100%; background: #1e3a5f; border: 1px solid #3b82f6; border-radius: 4px; color: #93c5fd; font-family: inherit; font-size: 12px; letter-spacing: 1px; padding: 10px; cursor: pointer; }
button:hover { background: #1e4080; }
.err { color: #ef4444; font-size: 11px; margin-bottom: 16px; }
</style>
</head>
<body>
<div class="box">
  <div class="logo">ESCROW</div>
  <div class="sub">package proxy dashboard</div>
  <form method="POST" action="login">
    <label for="u">USERNAME</label>
    <input id="u" name="username" type="text" autocomplete="username" autofocus>
    <label for="p">PASSWORD</label>
    <input id="p" name="password" type="password" autocomplete="current-password">
    {{if .Error}}<div class="err">{{.Error}}</div>{{end}}
    <button type="submit">SIGN IN</button>
  </form>
</div>
</body>
</html>
```

- [ ] **Step 2: Create `internal/dashboard/static/index.html`**

The JavaScript in this file uses only DOM methods (createElement, textContent, appendChild) for all data that comes from the event log — no innerHTML with dynamic values.

```html
<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>escrow dashboard</title>
<style>
* { box-sizing: border-box; margin: 0; padding: 0; }
html, body { height: 100%; }
body { background: #0a0f1a; color: #e2e8f0; font-family: 'SF Mono','Fira Code','Courier New',monospace; font-size: 13px; display: flex; flex-direction: column; }
.topbar { background: #0f1929; border-bottom: 1px solid #1e3a5f; padding: 0 20px; height: 44px; display: flex; align-items: center; justify-content: space-between; flex-shrink: 0; }
.logo { color: #60a5fa; font-weight: bold; letter-spacing: 2px; font-size: 14px; }
.logo span { color: #94a3b8; font-weight: normal; font-size: 11px; margin-left: 8px; }
.topbar-right { display: flex; align-items: center; gap: 16px; }
#live-badge { background: #16a34a22; color: #4ade80; border: 1px solid #16a34a55; padding: 2px 8px; border-radius: 4px; font-size: 10px; }
#live-badge.off { color: #64748b; border-color: #1e3a5f; background: none; }
.user-info { color: #94a3b8; font-size: 11px; }
.logout-btn { color: #64748b; font-size: 11px; text-decoration: none; border: 1px solid #1e3a5f; padding: 3px 10px; border-radius: 4px; }
.nav-bar { background: #0f1929; border-bottom: 1px solid #1e3a5f; padding: 0 20px; display: flex; align-items: center; justify-content: space-between; height: 36px; flex-shrink: 0; }
.nav-tabs { display: flex; height: 100%; }
.nav-tab { padding: 0 16px; color: #64748b; font-size: 11px; cursor: pointer; border-bottom: 2px solid transparent; display: flex; align-items: center; letter-spacing: 0.5px; }
.nav-tab.active { color: #60a5fa; border-bottom-color: #60a5fa; }
.eco-filters { display: flex; align-items: center; gap: 6px; }
.eco-label { color: #334155; font-size: 10px; margin-right: 2px; }
.eco-chip { padding: 2px 10px; border-radius: 3px; font-size: 10px; cursor: pointer; border: 1px solid #1e3a5f; color: #64748b; background: none; font-family: inherit; letter-spacing: 0.5px; text-transform: uppercase; }
.eco-chip.sel-all  { background: #1e3a5f; color: #93c5fd; border-color: #3b82f6; }
.eco-chip.sel-npm  { background: #14532d22; color: #4ade80; border-color: #16a34a55; }
.eco-chip.sel-pypi { background: #1e3a8a22; color: #93c5fd; border-color: #3b82f655; }
.main { display: flex; flex: 1; overflow: hidden; }
.feed-panel { flex: 1; display: flex; flex-direction: column; border-right: 1px solid #1e3a5f; overflow: hidden; }
.panel-header { padding: 8px 16px; border-bottom: 1px solid #1e3a5f; color: #64748b; font-size: 10px; letter-spacing: 1px; text-transform: uppercase; display: flex; justify-content: space-between; align-items: center; flex-shrink: 0; }
.col-headers { display: flex; align-items: baseline; gap: 10px; padding: 5px 16px 4px; border-bottom: 1px solid #0f1929; font-size: 9px; color: #334155; letter-spacing: 0.5px; text-transform: uppercase; flex-shrink: 0; }
#feed { flex: 1; overflow-y: auto; }
.feed-item { padding: 7px 16px; border-bottom: 1px solid #0f1929; display: flex; align-items: baseline; gap: 10px; font-size: 12px; }
.feed-item:hover { background: #0f1929; }
.ts { color: #334155; font-size: 10px; min-width: 56px; }
.eco-tag { font-size: 9px; padding: 1px 5px; border-radius: 2px; min-width: 38px; text-align: center; text-transform: uppercase; letter-spacing: 0.5px; }
.eco-tag.npm  { background: #14532d22; color: #4ade80; border: 1px solid #16a34a33; }
.eco-tag.pypi { background: #1e3a8a22; color: #93c5fd; border: 1px solid #3b82f633; }
.pkg { color: #cbd5e1; flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.sig { color: #475569; font-size: 10px; min-width: 160px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.st { font-weight: bold; min-width: 54px; font-size: 11px; }
.st.block { color: #ef4444; }
.st.warn  { color: #f59e0b; }
.st.allow { color: #22c55e; }
.policy-strip { background: #0f1929; border-top: 1px solid #1e3a5f; padding: 5px 16px; font-size: 10px; color: #475569; display: flex; gap: 16px; flex-shrink: 0; }
.policy-strip span { color: #60a5fa; }
.stats-panel { width: 240px; display: flex; flex-direction: column; flex-shrink: 0; overflow-y: auto; }
.stat-card { padding: 14px 16px; border-bottom: 1px solid #1e3a5f; }
.stat-label { color: #64748b; font-size: 9px; letter-spacing: 1px; text-transform: uppercase; margin-bottom: 4px; }
.stat-value { font-size: 26px; font-weight: bold; line-height: 1; }
.stat-value.blocked { color: #ef4444; }
.stat-value.warned  { color: #f59e0b; }
.stat-value.allowed { color: #22c55e; }
.stat-sub { color: #475569; font-size: 10px; margin-top: 2px; }
.top-section { padding: 12px 16px; flex: 1; }
.top-row { display: flex; justify-content: space-between; padding: 5px 0; font-size: 11px; border-bottom: 1px solid #0f1929; }
.top-name { color: #cbd5e1; }
.top-count { color: #ef4444; }
</style>
</head>
<body>
<div class="topbar">
  <div><span class="logo">ESCROW<span>package proxy</span></span></div>
  <div class="topbar-right">
    <span id="live-badge">● LIVE</span>
    <span class="user-info">admin</span>
    <a class="logout-btn" href="logout">logout</a>
  </div>
</div>
<div class="nav-bar">
  <div class="nav-tabs"><div class="nav-tab active">Live Feed</div></div>
  <div class="eco-filters">
    <span class="eco-label">ECOSYSTEM</span>
    <button class="eco-chip sel-all" id="chip-all"  onclick="setEco('')">All</button>
    <button class="eco-chip"         id="chip-npm"  onclick="setEco('npm')">npm</button>
    <button class="eco-chip"         id="chip-pypi" onclick="setEco('pypi')">PyPI</button>
  </div>
</div>
<div class="main">
  <div class="feed-panel">
    <div class="panel-header">
      <span>PACKAGE EVENTS</span>
      <span id="feed-count" style="color:#334155"></span>
    </div>
    <div class="col-headers">
      <span style="min-width:56px">time</span>
      <span style="min-width:46px">eco</span>
      <span style="flex:1">package</span>
      <span style="min-width:160px">signal / reason</span>
      <span style="min-width:54px">status</span>
    </div>
    <div id="feed"></div>
    <div style="flex:1"></div>
    <div class="policy-strip">
      <span>age <span id="p-age">—</span></span>
      <span>osv <span id="p-osv">—</span></span>
      <span>publisher <span id="p-pub">—</span></span>
      <span>popularity <span id="p-pop">—</span></span>
    </div>
  </div>
  <div class="stats-panel">
    <div class="stat-card">
      <div class="stat-label">Blocked</div>
      <div class="stat-value blocked" id="stat-blocked">—</div>
    </div>
    <div class="stat-card">
      <div class="stat-label">Warned</div>
      <div class="stat-value warned" id="stat-warned">—</div>
    </div>
    <div class="stat-card">
      <div class="stat-label">Allowed</div>
      <div class="stat-value allowed" id="stat-allowed">—</div>
    </div>
    <div class="top-section">
      <div class="panel-header" style="padding:0 0 8px;margin-bottom:8px;border-bottom:1px solid #1e3a5f">TOP BLOCKED</div>
      <div id="top-blocked"></div>
    </div>
  </div>
</div>
<script>
const BASE = window.location.pathname.replace(/\/+$/, '');
let currentEco = '';
let es = null;
let allEvents = [];

function fmtTime(ts) {
  return new Date(ts).toLocaleTimeString('en-GB', {hour:'2-digit',minute:'2-digit',second:'2-digit'});
}

// Build a feed row using DOM methods only (no innerHTML with dynamic data)
function makeRow(e) {
  const row = document.createElement('div');
  row.className = 'feed-item';

  const ts = document.createElement('span');
  ts.className = 'ts';
  ts.textContent = fmtTime(e.timestamp);

  const eco = document.createElement('span');
  eco.className = 'eco-tag ' + (e.ecosystem || '');
  eco.textContent = e.ecosystem || '';

  const pkg = document.createElement('span');
  pkg.className = 'pkg';
  pkg.title = e.package || '';
  pkg.textContent = e.package || '';

  const sigText = e.signal ? (e.signal + (e.reason ? ' · ' + e.reason : '')) : '—';
  const sig = document.createElement('span');
  sig.className = 'sig';
  sig.title = sigText;
  sig.textContent = sigText;

  const stClass = e.action === 'block' ? 'block' : e.action === 'warn' ? 'warn' : 'allow';
  const st = document.createElement('span');
  st.className = 'st ' + stClass;
  st.textContent = (e.action || 'allow').toUpperCase();

  row.append(ts, eco, pkg, sig, st);
  return row;
}

function rebuildFeed() {
  const feed = document.getElementById('feed');
  feed.textContent = '';
  const filtered = currentEco ? allEvents.filter(e => e.ecosystem === currentEco) : allEvents;
  filtered.forEach(e => feed.appendChild(makeRow(e)));
  document.getElementById('feed-count').textContent = filtered.length + ' events';
}

function prependRow(e) {
  allEvents.unshift(e);
  if (allEvents.length > 500) allEvents.pop();
  if (currentEco && e.ecosystem !== currentEco) return;
  const feed = document.getElementById('feed');
  feed.insertBefore(makeRow(e), feed.firstChild);
  document.getElementById('feed-count').textContent = feed.children.length + ' events';
}

function updateStats() {
  fetch(BASE + '/api/stats').then(r => r.json()).then(s => {
    document.getElementById('stat-blocked').textContent = s.blocked;
    document.getElementById('stat-warned').textContent = s.warned;
    document.getElementById('stat-allowed').textContent = s.allowed;
    const top = document.getElementById('top-blocked');
    top.textContent = '';
    (s.top_blocked || []).forEach(t => {
      const row = document.createElement('div');
      row.className = 'top-row';
      const name = document.createElement('span');
      name.className = 'top-name';
      name.textContent = t.package;
      const count = document.createElement('span');
      count.className = 'top-count';
      count.textContent = t.count + '×';
      row.append(name, count);
      top.appendChild(row);
    });
  }).catch(() => {});
}

function connect(eco) {
  if (es) { es.close(); es = null; }
  const url = BASE + '/api/stream' + (eco ? '?eco=' + encodeURIComponent(eco) : '');
  es = new EventSource(url);
  es.onopen = () => {
    const b = document.getElementById('live-badge');
    b.className = '';
    b.textContent = '● LIVE';
  };
  es.onmessage = ev => {
    prependRow(JSON.parse(ev.data));
    updateStats();
  };
  es.onerror = () => {
    const b = document.getElementById('live-badge');
    b.className = 'off';
    b.textContent = '○ RECONNECTING';
    es.close();
    setTimeout(() => connect(eco), 3000);
  };
}

function setEco(eco) {
  currentEco = eco;
  document.getElementById('chip-all').className  = 'eco-chip' + (eco === ''     ? ' sel-all'  : '');
  document.getElementById('chip-npm').className  = 'eco-chip' + (eco === 'npm'  ? ' sel-npm'  : '');
  document.getElementById('chip-pypi').className = 'eco-chip' + (eco === 'pypi' ? ' sel-pypi' : '');
  connect(eco);
  rebuildFeed();
}

fetch(BASE + '/api/events?n=100')
  .then(r => r.json())
  .then(events => { allEvents = events || []; rebuildFeed(); connect(currentEco); updateStats(); })
  .catch(() => connect(currentEco));

setInterval(updateStats, 10000);
</script>
</body>
</html>
```

- [ ] **Step 3: Verify files created**

```bash
ls /Users/jjverhoeks/src/tries/2026-05-16-escrow/internal/dashboard/static/
```

Expected: `index.html  login.html`

- [ ] **Step 4: Commit**

```bash
git add internal/dashboard/static/
git commit -m "feat: dashboard HTML — login form and dark terminal SPA with safe DOM rendering"
```

---

### Task 6: Dashboard embed + handlers

**Files:**
- Create: `internal/dashboard/embed.go`
- Create: `internal/dashboard/handlers.go`

- [ ] **Step 1: Create `internal/dashboard/embed.go`**

```go
package dashboard

import "embed"

//go:embed static
var staticFS embed.FS
```

- [ ] **Step 2: Create `internal/dashboard/handlers.go`**

```go
package dashboard

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jverhoeks/escrow/internal/config"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/rs/zerolog"
)

type Dashboard struct {
	cfg    config.DashboardConfig
	auth   *Auth
	log    *eventlog.Log
	logger zerolog.Logger
}

func New(cfg config.DashboardConfig, log *eventlog.Log, logger zerolog.Logger) *Dashboard {
	return &Dashboard{cfg: cfg, auth: NewAuth(cfg.Username, cfg.Password, cfg.Secret), log: log, logger: logger}
}

func (d *Dashboard) Mount(r chi.Router) {
	base := d.cfg.Path
	r.Get(base+"/login", d.handleLoginPage)
	r.Post(base+"/login", d.handleLoginSubmit)
	r.Get(base+"/logout", d.handleLogout)

	protected := chi.NewRouter()
	protected.Use(d.auth.Middleware(base + "/login"))
	protected.Get("/", d.handleIndex)
	protected.Get("/api/stream", d.handleStream)
	protected.Get("/api/events", d.handleEvents)
	protected.Get("/api/stats", d.handleStats)
	r.Mount(base, protected)
}

func (d *Dashboard) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	raw, err := staticFS.ReadFile("static/login.html")
	if err != nil {
		http.Error(w, "login.html missing", 500)
		return
	}
	tmpl, err := template.New("login").Parse(string(raw))
	if err != nil {
		http.Error(w, "template error", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl.Execute(w, map[string]string{"Error": r.URL.Query().Get("error")})
}

func (d *Dashboard) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	if !d.auth.CheckCredentials(r.FormValue("username"), r.FormValue("password")) {
		http.Redirect(w, r, d.cfg.Path+"/login?error=Invalid+credentials", http.StatusFound)
		return
	}
	d.auth.SetCookie(w, r.FormValue("username"))
	http.Redirect(w, r, d.cfg.Path+"/", http.StatusFound)
}

func (d *Dashboard) handleLogout(w http.ResponseWriter, r *http.Request) {
	d.auth.ClearCookie(w)
	http.Redirect(w, r, d.cfg.Path+"/login", http.StatusFound)
}

func (d *Dashboard) handleIndex(w http.ResponseWriter, r *http.Request) {
	data, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "index.html missing", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func (d *Dashboard) handleStream(w http.ResponseWriter, r *http.Request) {
	eco := r.URL.Query().Get("eco")
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	ch, unsub := d.log.Subscribe()
	defer unsub()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		case e, ok := <-ch:
			if !ok {
				return
			}
			if eco != "" && e.Ecosystem != eco {
				continue
			}
			data, _ := json.Marshal(e)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

func (d *Dashboard) handleEvents(w http.ResponseWriter, r *http.Request) {
	eco := r.URL.Query().Get("eco")
	n := 100
	if s := r.URL.Query().Get("n"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			n = v
		}
	}
	events := d.log.Events(eco)
	if len(events) > n {
		events = events[:n]
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(events)
}

func (d *Dashboard) handleStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(d.log.Stats())
}
```

- [ ] **Step 3: Build**

```bash
go build ./...
```

Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add internal/dashboard/embed.go internal/dashboard/handlers.go
git commit -m "feat: dashboard handlers — SSE, events API, stats, login/logout"
```

---

### Task 7: Wire eventlog into npm and PyPI handlers

**Files:**
- Modify: `internal/handler/npm/handler.go`
- Modify: `internal/handler/pypi/handler.go`

- [ ] **Step 1: Add eventlog to npm Handler struct and New()**

Add field and import in `internal/handler/npm/handler.go`:

```go
import (
    // ... existing ...
    "github.com/jverhoeks/escrow/internal/eventlog"
)

type Handler struct {
    client      *http.Client
    upstreamURL string
    engine      *trust.Engine
    policy      *policy.Engine
    cache       cache.Cache
    webhook     *alerts.Webhook
    evlog       *eventlog.Log
}

func New(client *http.Client, upstreamURL string, engine *trust.Engine, pol *policy.Engine, c cache.Cache, evLog *eventlog.Log) *Handler {
    return &Handler{client: client, upstreamURL: upstreamURL, engine: engine, policy: pol, cache: c, evlog: evLog}
}
```

In `filterManifest`, after the counter increment add:

```go
if h.evlog != nil {
    h.evlog.Record(eventlog.PackageEvent{
        Ecosystem: string(pkg.Ecosystem),
        Package:   pkg.Name + "@" + pkg.Version,
        Action:    string(decision.Action),
        Signal:    decision.Signal,
        Reason:    decision.Reason,
    })
}
```

- [ ] **Step 2: Add eventlog to PyPI Handler**

Same pattern in `internal/handler/pypi/handler.go`:

```go
import (
    // ... existing ...
    "github.com/jverhoeks/escrow/internal/eventlog"
)

type Handler struct {
    // ... existing fields ...
    evlog      *eventlog.Log
}

func New(client *http.Client, upstreamURL string, engine *trust.Engine, pol *policy.Engine, c cache.Cache, blockSdist bool, evLog *eventlog.Log) *Handler {
    return &Handler{client: client, upstreamURL: upstreamURL, engine: engine, policy: pol, cache: c, blockSdist: blockSdist, evlog: evLog}
}
```

In `versionAllowed`, after the counter increment add:

```go
if h.evlog != nil {
    h.evlog.Record(eventlog.PackageEvent{
        Ecosystem: string(pkg.Ecosystem),
        Package:   pkg.Name + "@" + pkg.Version,
        Action:    string(d.Action),
        Signal:    d.Signal,
        Reason:    d.Reason,
    })
}
```

- [ ] **Step 3: Update handler tests to pass nil for evLog**

In `internal/handler/npm/handler_test.go`, update all `npm.New(...)` calls:
```go
h := npm.New(upstream.Client(), upstream.URL, engine, pol, c, nil)
```

In `internal/handler/pypi/handler_test.go`:
```go
h := pypi.New(upstream.Client(), upstream.URL, engine, pol, c, false, nil)
// (and the blockSdist test):
h := pypi.New(upstream.Client(), upstream.URL, trust.NewEngine(), policy.New(nil), c, true, nil)
```

- [ ] **Step 4: Build and test**

```bash
go build ./... && go test ./...
```

Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/handler/npm/handler.go internal/handler/pypi/handler.go
git commit -m "feat: wire eventlog into npm and PyPI handlers"
```

---

### Task 8: Wire everything in main.go + integration test + release

**Files:**
- Modify: `cmd/escrow/main.go`

- [ ] **Step 1: Replace main.go**

```go
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/jverhoeks/escrow/internal/alerts"
	"github.com/jverhoeks/escrow/internal/cache"
	"github.com/jverhoeks/escrow/internal/config"
	"github.com/jverhoeks/escrow/internal/dashboard"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/jverhoeks/escrow/internal/handler/npm"
	"github.com/jverhoeks/escrow/internal/handler/pypi"
	"github.com/jverhoeks/escrow/internal/policy"
	"github.com/jverhoeks/escrow/internal/server"
	"github.com/jverhoeks/escrow/internal/trust"
	"github.com/jverhoeks/escrow/internal/upstream"
)

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	cfgPath := "sentinel.toml"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}

	generated, msg, err := config.GenerateIfMissing(cfgPath)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to generate config")
	}
	if generated {
		fmt.Println(msg)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}
	for _, w := range cfg.Warnings() {
		log.Warn().Msg(w)
	}

	var c cache.Cache
	switch cfg.Storage.Backend {
	case "memory":
		c = cache.NewMemory()
	case "s3":
		c, err = cache.NewS3(cfg.Storage.S3.Bucket, cfg.Storage.S3.Region, cfg.Storage.S3.Endpoint)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to init S3 cache")
		}
	default:
		c, err = cache.NewDisk(cfg.Storage.Disk.Path)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to init disk cache")
		}
	}
	defer c.Close()

	httpClient := upstream.New()
	polEngine := policy.New(cfg.Policy)
	evLog := eventlog.New(500)

	var signals []trust.Signal
	if cfg.Policy != nil {
		if cfg.Policy.Age != nil {
			signals = append(signals, trust.NewAgeSignal(cfg.Policy.Age.MinDays, nil))
		}
		if cfg.Policy.OSV != nil {
			signals = append(signals, trust.NewOSVSignal(cfg.Policy.OSV.MinSeverity, httpClient, c, ""))
		}
		if cfg.Policy.Publisher != nil {
			signals = append(signals, trust.NewPublisherSignal(cfg.Policy.Publisher.MaxAccountAgeDays, httpClient, "", ""))
		}
		if cfg.Policy.Popularity != nil {
			signals = append(signals, trust.NewPopularitySignal(cfg.Policy.Popularity.SpikeFactor, httpClient, c, "", ""))
		}
	}
	trustEngine := trust.NewEngine(signals...)

	var wh *alerts.Webhook
	if cfg.Alerts.WebhookURL != "" {
		wh = alerts.NewWebhook(cfg.Alerts.WebhookURL, nil)
		log.Info().Str("url", cfg.Alerts.WebhookURL).Msg("webhook alerts enabled")
	}

	srv := server.New(cfg.Server.Host, cfg.Server.Port, log.Logger, cfg.Storage.Backend)
	r := srv.Router()

	if cfg.Ecosystems.NPM {
		h := npm.New(httpClient, "https://registry.npmjs.org", trustEngine, polEngine, c, evLog)
		if wh != nil {
			h.WithWebhook(wh)
		}
		h.Mount(r)
	}
	if cfg.Ecosystems.PyPI {
		blockSdist := cfg.Policy != nil && cfg.Policy.PyPI != nil && cfg.Policy.PyPI.BlockSdist
		h := pypi.New(httpClient, "https://pypi.org", trustEngine, polEngine, c, blockSdist, evLog)
		if wh != nil {
			h.WithWebhook(wh)
		}
		h.Mount(r)
	}

	if cfg.Dashboard.Enabled {
		dash := dashboard.New(cfg.Dashboard, evLog, log.Logger)
		dash.Mount(r)
		log.Info().Str("path", cfg.Dashboard.Path).Msg("dashboard enabled")
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-quit
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

	if err := srv.Start(); err != nil && err != http.ErrServerClosed {
		log.Fatal().Err(err).Msg("server stopped unexpectedly")
	}
}
```

- [ ] **Step 2: Build and run all tests**

```bash
go build ./... && go test ./...
```

Expected: build succeeds, all tests pass.

- [ ] **Step 3: Integration smoke test**

```bash
go build -o escrow-darwin-arm64 ./cmd/escrow

cat > /tmp/escrow-dash.toml << 'EOF'
[server]
  port = 8891
  log_level = "info"
[storage]
  backend = "memory"
[ecosystems]
  npm = true
  pypi = true
[dashboard]
  enabled = true
  path = "/dashboard"
  username = "admin"
  password = "test1234"
  secret = "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
[policy]
  [policy.age]
    min_days = 7
    action = "block"
  [policy.osv]
    min_severity = "MEDIUM"
    action = "block"
EOF

./escrow-darwin-arm64 /tmp/escrow-dash.toml &
sleep 2

# Unauthenticated redirect
CODE=$(curl -so /dev/null -w "%{http_code}" http://localhost:8891/dashboard/)
[ "$CODE" = "302" ] && echo "PASS: dashboard redirects to login" || echo "FAIL: got $CODE"

# Login and get cookie
curl -si -X POST http://localhost:8891/dashboard/login \
  -d "username=admin&password=test1234" \
  --cookie-jar /tmp/escrow-cookies.txt -L > /dev/null

# Authenticated page load
BODY=$(curl -sf -b /tmp/escrow-cookies.txt http://localhost:8891/dashboard/)
echo "$BODY" | grep -q "ESCROW" && echo "PASS: dashboard page loads" || echo "FAIL: ESCROW not found"

# Seed events
curl -sf "http://localhost:8891/once" > /dev/null
curl -sf "http://localhost:8891/lodash" > /dev/null
sleep 1

# Events API
curl -sf -b /tmp/escrow-cookies.txt "http://localhost:8891/dashboard/api/events" | \
  python3 -c "import sys,json; e=json.load(sys.stdin); print('PASS: events:', len(e), 'events returned')"

# Stats API
curl -sf -b /tmp/escrow-cookies.txt "http://localhost:8891/dashboard/api/stats" | \
  python3 -c "import sys,json; s=json.load(sys.stdin); print('PASS: stats: blocked=%d warned=%d allowed=%d' % (s['blocked'],s['warned'],s['allowed']))"

# Metrics
curl -sf http://localhost:8891/metrics | grep -q "escrow_requests_total" \
  && echo "PASS: metrics counter present" || echo "FAIL: metric missing"

kill %1
```

Expected output:
```
PASS: dashboard redirects to login
PASS: dashboard page loads
PASS: events: 2 events returned
PASS: stats: blocked=1 warned=0 allowed=1
PASS: metrics counter present
```

- [ ] **Step 4: Test first-boot config generation**

```bash
./escrow-darwin-arm64 /tmp/escrow-firstboot-$(date +%s).toml &
sleep 1
kill %1
```

Expected: startup prints `Generated ... username: admin  password: <random>  url: http://localhost:7888/dashboard`

- [ ] **Step 5: Build release binaries and push v0.2.0**

```bash
GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o escrow-darwin-arm64 ./cmd/escrow
GOOS=linux  GOARCH=amd64 go build -ldflags="-s -w" -o escrow-linux-amd64  ./cmd/escrow
GOOS=linux  GOARCH=arm64 go build -ldflags="-s -w" -o escrow-linux-arm64  ./cmd/escrow

git add .
git commit -m "feat: escrow v0.2.0 — dashboard, metrics wiring, first-boot config"
git tag v0.2.0
git push origin main v0.2.0

gh release create v0.2.0 \
  --title "escrow v0.2.0 — real-time dashboard" \
  --notes "$(cat << 'EOF'
## What's new in v0.2.0

**Real-time dashboard** — dark terminal-style web UI at `/dashboard`:
- Live feed of package events with ecosystem filter (All / npm / PyPI)
- Blocked/warned/allowed counters + top-blocked packages
- Server-Sent Events for real-time updates, auto-reconnect
- Session auth with HMAC-SHA256 cookie, 24h expiry

**Metrics wiring** — `escrow_requests_total` and `escrow_blocks_total` Prometheus counters now populate on every request.

**First-boot config generation** — if `sentinel.toml` is missing, escrow generates it with a random password and secret and prints credentials to stdout.

### Quick start

```bash
./escrow                          # generates sentinel.toml on first run
# or:
./escrow /path/to/sentinel.toml
# then open http://localhost:7888/dashboard
```
EOF
)" \
  escrow-darwin-arm64 escrow-linux-amd64 escrow-linux-arm64 config.example.toml
```

---

## Self-Review

**Spec coverage:**

| Requirement | Task |
|-------------|------|
| escrow_requests_total wired | Task 1 |
| escrow_blocks_total wired | Task 1 |
| EventLog ring buffer cap 500 | Task 2 |
| SSE subscriber fan-out, drop slow | Task 2 |
| Events filtered by ecosystem | Task 2 |
| Stats with top-3 blocked | Task 2 |
| DashboardConfig struct | Task 3 |
| First-boot config generation | Task 3 |
| HMAC session cookie auth | Task 4 |
| Auth middleware redirect | Task 4 |
| Login form dark terminal style | Task 5 |
| Dashboard SPA — dark terminal | Task 5 |
| Ecosystem filter chips (All/npm/PyPI) | Task 5 |
| No innerHTML with dynamic data (XSS safe) | Task 5 |
| go:embed static/ | Task 6 |
| POST /dashboard/login | Task 6 |
| GET /dashboard/logout | Task 6 |
| GET /dashboard/api/stream?eco= | Task 6 |
| GET /dashboard/api/events?n= | Task 6 |
| GET /dashboard/api/stats | Task 6 |
| EventLog wired into npm handler | Task 7 |
| EventLog wired into PyPI handler | Task 7 |
| main.go: GenerateIfMissing | Task 8 |
| main.go: evLog, dashboard.Mount | Task 8 |
| Integration smoke test | Task 8 |
| v0.2.0 release | Task 8 |

**No gaps.**

**Type consistency:**
- `eventlog.PackageEvent` defined Task 2, used identically in Tasks 6, 7
- `eventlog.New(500)` in Task 8 matches `func New(cap int) *Log` in Task 2
- `dashboard.New(cfg.Dashboard, evLog, log.Logger)` in Task 8 matches Task 6 signature
- `npm.New(..., evLog)` in Task 8 matches updated Task 7 signature
- `pypi.New(..., blockSdist, evLog)` in Task 8 matches updated Task 7 signature
