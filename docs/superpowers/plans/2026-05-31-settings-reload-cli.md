# Settings Page + Config Hot-Reload + CLI Live View — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** View/edit `escrow.toml` from the dashboard, apply the live-reloadable parts of a config change without a restart (dashboard button, `POST /api/reload`, `SIGHUP`, `escrow-cli reload`), and watch package events from the terminal (`escrow-cli live`).

**Architecture:** Make `policy.Engine`, `rescan.Scanner`, and `alerts.Webhook` live-reconfigurable behind mutexes. A reload routine (built in `main.go`) re-reads + validates the file, applies the live subset (policy/rescan/alerts), and reports restart-required changes; it's invoked by an HTTP endpoint, `SIGHUP`, and the CLI (via a PID file). The settings page reads/writes the config file (password server-enforced immutable) and triggers reload on save.

**Tech Stack:** Go, chi, BurntSushi/toml, zerolog, testify. No new deps.

**Spec:** `docs/superpowers/specs/2026-05-31-settings-reload-cli-design.md`

---

## File Structure

**New files:**
- `internal/config/save.go` — `Save(path, Config)` (+ backup) and `Validate()`.
- `internal/config/save_test.go`
- `internal/dashboard/settings.go` — `GET`/`POST /api/settings`, `POST /api/reload`, `ReloadResult`/`ReloadFunc`.
- `internal/dashboard/settings_test.go`
- `cmd/escrow-cli/reload.go` — `escrow-cli reload` (PID + SIGHUP).
- `cmd/escrow-cli/live.go` — `escrow-cli live` (tail event-log JSONL).
- `cmd/escrow-cli/live_test.go`

**Modified files:**
- `internal/policy/policy.go` — `RWMutex` + `SetConfig`.
- `internal/rescan/scanner.go` — `SetConfig` + interval-aware `Start` loop.
- `internal/alerts/webhook.go` — `SetURL`.
- `internal/dashboard/handlers.go` — `Dashboard` gains `configPath string` + `reload ReloadFunc`; `New` signature; routes.
- `cmd/escrow/main.go` — reload closure, `SIGHUP`, PID file, pass `configPath`/`reload` to `dashboard.New`.
- `cmd/escrow-cli/main.go` — dispatch `reload` and `live`.

---

## PHASE 1 — Live reconfig + reload

### Task 1: `policy.Engine` live `SetConfig`

**Files:**
- Modify: `internal/policy/policy.go`
- Test: `internal/policy/policy_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/policy/policy_test.go`:

```go
func TestEngine_SetConfig_AppliesLive(t *testing.T) {
	e := policy.New(&config.PolicyConfig{OSV: &config.OSVPolicyConfig{Action: "warn"}})
	res := trust.TrustResult{
		Package: trust.Package{Ecosystem: trust.EcosystemNPM, Name: "x", Version: "1.0.0"},
		Reports: []trust.SignalReport{{Signal: "osv", Result: trust.SignalFail, Reason: "v"}},
	}
	require.Equal(t, policy.ActionWarn, e.Evaluate(res).Action)

	e.SetConfig(&config.PolicyConfig{OSV: &config.OSVPolicyConfig{Action: "block"}})
	require.Equal(t, policy.ActionBlock, e.Evaluate(res).Action)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/policy/ -run TestEngine_SetConfig_AppliesLive`
Expected: FAIL — `e.SetConfig` undefined.

- [ ] **Step 3: Add the mutex + SetConfig; read cfg under RLock**

In `internal/policy/policy.go`, add `"sync"` to imports and change `Engine`:

```go
type Engine struct {
	mu        sync.RWMutex
	cfg       *config.PolicyConfig
	allowList *allow.List // may be nil
	blockList *block.List // may be nil
}

// SetConfig swaps the policy config atomically (live reload).
func (e *Engine) SetConfig(cfg *config.PolicyConfig) {
	e.mu.Lock()
	e.cfg = cfg
	e.mu.Unlock()
}
```

At the top of `Evaluate`, snapshot the cfg pointer under the read lock and use the local everywhere `e.cfg` was used:

```go
func (e *Engine) Evaluate(result trust.TrustResult) Decision {
	e.mu.RLock()
	cfg := e.cfg
	e.mu.RUnlock()
	// ... below, replace every `e.cfg` with `cfg`; keep `e.allowList`/`e.blockList` as-is.
```

Replace the `e.cfg` references in `Evaluate` and in `actionFor` (pass `cfg` in, or snapshot there too). Concretely: change `actionFor(r trust.SignalReport)` to `actionFor(cfg *config.PolicyConfig, r trust.SignalReport)` and update its body to use the passed `cfg`, and its call site in `Evaluate` to `e.actionFor(cfg, r)`. The `if e.cfg == nil` check becomes `if cfg == nil`.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/policy/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/policy/policy.go internal/policy/policy_test.go
git commit -m "feat(policy): live SetConfig for hot-reload"
```

---

### Task 2: `rescan.Scanner` live `SetConfig` + interval-aware loop

**Files:**
- Modify: `internal/rescan/scanner.go`
- Test: `internal/rescan/scanner_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/rescan/scanner_test.go`:

```go
func TestScanner_SetConfig_AppliesLive(t *testing.T) {
	log := eventlog.New(100)
	log.Record(eventlog.PackageEvent{Ecosystem: "npm", Package: "lodash@4.17.21", Action: "allow", Kind: eventlog.KindDownloaded})
	bl, _ := block.New("")
	osv := newOSV(t, `{"vulns":[{"id":"GHSA-new","database_specific":{"severity":"HIGH"}}]}`)
	s := rescan.New(rescan.Deps{Log: log, OSV: osv, BlockList: bl}, rescan.Config{MinSeverity: "HIGH", AutoBlock: false})

	// auto_block off → finding but no block
	require.Equal(t, 1, s.RunOnce(context.Background()).NewFindings)
	blocked, _ := bl.IsBlocked("npm", "lodash", "4.17.21")
	require.False(t, blocked)

	// turn auto_block on live; a fresh downloaded version gets blocked
	log.Record(eventlog.PackageEvent{Ecosystem: "npm", Package: "left-pad@1.0.0", Action: "allow", Kind: eventlog.KindDownloaded})
	s.SetConfig(rescan.Config{MinSeverity: "HIGH", AutoBlock: true})
	s.RunOnce(context.Background())
	blocked2, _ := bl.IsBlocked("npm", "left-pad", "1.0.0")
	require.True(t, blocked2)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/rescan/ -run TestScanner_SetConfig_AppliesLive`
Expected: FAIL — `s.SetConfig` undefined.

- [ ] **Step 3: Add a cfg mutex, SetConfig, and read cfg under lock**

In `internal/rescan/scanner.go`, add a `cfgMu sync.RWMutex` to `Scanner` and a getter/setter; have `RunOnce` snapshot the config at the top:

```go
// SetConfig swaps the scanner config atomically (live reload). interval_hours
// takes effect on the next scheduled cycle; the rest applies on the next sweep.
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
```

In `RunOnce`, replace reads of `s.cfg` with a local `cfg := s.config()` snapshot taken after acquiring the existing sweep mutex `s.mu`. In `Start`, replace the fixed `time.NewTicker(interval)` loop with a self-rescheduling loop that re-reads the interval each cycle:

```go
func (s *Scanner) Start(ctx context.Context) {
	if !s.config().Enabled {
		return
	}
	go func() {
		// initial delay
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
			if s.config().Enabled {
				s.RunOnce(ctx)
			}
		}
		for {
			d := time.Duration(s.config().IntervalHours) * time.Hour
			if d <= 0 {
				d = 24 * time.Hour
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(d):
				if s.config().Enabled {
					s.RunOnce(ctx)
				}
			}
		}
	}()
}
```

(Keep the `Enabled` field usage consistent: `Start` is still only called when initially enabled in main.go, but re-checking `Enabled` each cycle lets a live disable take effect.)

- [ ] **Step 4: Run tests**

Run: `go test ./internal/rescan/`
Expected: PASS (new test + the three existing scanner tests).

- [ ] **Step 5: Commit**

```bash
git add internal/rescan/scanner.go internal/rescan/scanner_test.go
git commit -m "feat(rescan): live SetConfig and interval-aware scheduler"
```

---

### Task 3: `alerts.Webhook` live `SetURL`

**Files:**
- Modify: `internal/alerts/webhook.go`
- Test: `internal/alerts/webhook_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/alerts/webhook_test.go`:

```go
func TestWebhook_SetURL(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.WriteHeader(200) }))
	defer srv.Close()
	wh := alerts.NewWebhook("http://127.0.0.1:1/none", srv.Client())
	wh.SetURL(srv.URL) // redirect live
	require.NoError(t, wh.SendRescan("npm", "x", "1.0.0", []string{"GHSA-x"}, "HIGH", false, 0))
	require.Equal(t, 1, hits)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/alerts/ -run TestWebhook_SetURL`
Expected: FAIL — `wh.SetURL` undefined.

- [ ] **Step 3: Guard `url` with a mutex + add SetURL**

In `internal/alerts/webhook.go`, add `"sync"`, change `Webhook` to hold a mutex, add `SetURL`, and read the url under lock in both `Send` and `SendRescan`:

```go
type Webhook struct {
	mu     sync.RWMutex
	url    string
	client *http.Client
}

func (w *Webhook) SetURL(url string) {
	w.mu.Lock()
	w.url = url
	w.mu.Unlock()
}

func (w *Webhook) target() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.url
}
```

In `Send` and `SendRescan`, replace `w.url` in the `w.client.Post(...)` call with `w.target()`.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/alerts/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/alerts/webhook.go internal/alerts/webhook_test.go
git commit -m "feat(alerts): live SetURL for hot-reload"
```

---

### Task 4: `config.Validate` + `config.Save`

**Files:**
- Create: `internal/config/save.go`
- Test: `internal/config/save_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/config/save_test.go`:

```go
package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jverhoeks/escrow/internal/config"
	"github.com/stretchr/testify/require"
)

func TestValidate(t *testing.T) {
	c := config.DefaultConfig()
	c.Server.Port = 7888
	require.NoError(t, c.Validate())

	bad := config.DefaultConfig()
	bad.Server.Port = 70000
	require.Error(t, bad.Validate())

	badAction := config.DefaultConfig()
	badAction.Policy = &config.PolicyConfig{OSV: &config.OSVPolicyConfig{Action: "nuke"}}
	require.Error(t, badAction.Validate())
}

func TestSave_BackupAndRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "escrow.toml")
	require.NoError(t, os.WriteFile(path, []byte("[server]\n  port = 1\n"), 0o600))

	c := config.DefaultConfig()
	c.Server.Port = 9999
	require.NoError(t, config.Save(path, c))

	// backup preserves the prior content
	bak, err := os.ReadFile(path + ".bak")
	require.NoError(t, err)
	require.Contains(t, string(bak), "port = 1")

	// reloading the saved file yields the saved value
	reloaded, err := config.Load(path)
	require.NoError(t, err)
	require.Equal(t, 9999, reloaded.Server.Port)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run 'TestValidate|TestSave_BackupAndRoundTrip'`
Expected: FAIL — `c.Validate` / `config.Save` undefined.

- [ ] **Step 3: Implement Validate + Save**

Create `internal/config/save.go`:

```go
package config

import (
	"bytes"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

var validActions = map[string]bool{"": true, "allow": true, "warn": true, "block": true}
var validSeverities = map[string]bool{"": true, "CRITICAL": true, "HIGH": true, "MEDIUM": true, "LOW": true}

// Validate checks hard constraints that must hold before a config is written or
// applied. It is intentionally conservative — type-level correctness is already
// guaranteed by decoding into Config.
func (c Config) Validate() error {
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port %d out of range 1-65535", c.Server.Port)
	}
	if p := c.Policy; p != nil {
		actions := map[string]string{}
		if p.Age != nil {
			actions["age"] = p.Age.Action
		}
		if p.OSV != nil {
			actions["osv"] = p.OSV.Action
			if !validSeverities[p.OSV.MinSeverity] {
				return fmt.Errorf("policy.osv.min_severity %q invalid", p.OSV.MinSeverity)
			}
		}
		if p.Publisher != nil {
			actions["publisher"] = p.Publisher.Action
		}
		if p.Popularity != nil {
			actions["popularity"] = p.Popularity.Action
		}
		for sig, a := range actions {
			if !validActions[a] {
				return fmt.Errorf("policy.%s.action %q invalid (allow|warn|block)", sig, a)
			}
		}
		if !validActions[p.StrictSignals] {
			return fmt.Errorf("policy.strict_signals %q invalid (allow|warn|block)", p.StrictSignals)
		}
	}
	if c.Rescan != nil && !validSeverities[c.Rescan.MinSeverity] {
		return fmt.Errorf("rescan.min_severity %q invalid", c.Rescan.MinSeverity)
	}
	return nil
}

// Save backs up path to path+".bak" (best-effort) and writes the TOML encoding
// of cfg with a generated header. Re-encoding does not preserve comments; the
// backup is the recovery path.
func Save(path string, cfg Config) error {
	if existing, err := os.ReadFile(path); err == nil {
		_ = os.WriteFile(path+".bak", existing, 0o600)
	}
	var buf bytes.Buffer
	buf.WriteString("# Written by the escrow dashboard. Comments are not preserved on save;\n")
	buf.WriteString("# the previous file is kept at this path + \".bak\".\n\n")
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	return os.WriteFile(path, buf.Bytes(), 0o600)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/config/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/save.go internal/config/save_test.go
git commit -m "feat(config): Validate and Save (with backup) for the settings page"
```

---

### Task 5: `POST /api/reload` + ReloadFunc plumbing

**Files:**
- Create: `internal/dashboard/settings.go` (the reload pieces; settings GET/POST come in Phase 2)
- Modify: `internal/dashboard/handlers.go` (`Dashboard` fields, `New`, route)
- Test: `internal/dashboard/settings_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/dashboard/settings_test.go`:

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
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestReload_CallsReloadFunc(t *testing.T) {
	al, _ := allow.New("")
	cfg := config.DashboardConfig{Enabled: true, Path: "/dashboard", Username: "admin", Password: "pass", Secret: "aabbccddeeff00112233445566778899"}
	called := false
	reload := func() (dashboard.ReloadResult, error) {
		called = true
		return dashboard.ReloadResult{Reloaded: []string{"policy"}, RestartRequired: []string{"storage"}}, nil
	}
	dash := dashboard.New(cfg, eventlog.New(10), zerolog.Nop(), al, nil, nil, nil, nil, nil, nil, "/tmp/escrow.toml", reload)
	r := chi.NewRouter()
	dash.Mount(r)

	req := authenticatedRequest(t, http.MethodPost, "/dashboard/api/reload", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, called)
	var out dashboard.ReloadResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Equal(t, []string{"policy"}, out.Reloaded)
	require.Equal(t, []string{"storage"}, out.RestartRequired)
}
```

This calls the **final** `dashboard.New` signature: the existing 10 args (through `scanner`) **plus** `configPath string` and `reload ReloadFunc`. Update the helper/other call sites in Step 3.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/dashboard/ -run TestReload_CallsReloadFunc`
Expected: FAIL — too few args to `dashboard.New` / `ReloadResult` undefined.

- [ ] **Step 3: Add types, fields, route, handler; update call sites**

Create `internal/dashboard/settings.go`:

```go
package dashboard

import (
	"encoding/json"
	"net/http"
)

// ReloadResult reports which config sections were applied live vs. which
// changed fields still require a restart.
type ReloadResult struct {
	Reloaded        []string `json:"reloaded"`
	RestartRequired []string `json:"restart_required"`
}

// ReloadFunc re-reads the config file, applies the live-reloadable subset, and
// reports the outcome. Constructed in main.go.
type ReloadFunc func() (ReloadResult, error)

func (d *Dashboard) handleReload(w http.ResponseWriter, r *http.Request) {
	if !d.originOK(r) {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if d.reload == nil {
		http.Error(w, `{"error":"reload not configured"}`, http.StatusServiceUnavailable)
		return
	}
	res, err := d.reload()
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(res)
}
```

In `internal/dashboard/handlers.go`, add fields to `Dashboard`:

```go
	configPath string     // path to escrow.toml; empty disables settings/reload writes
	reload     ReloadFunc // may be nil
```

Append the two params to `New` (after `scanner *rescan.Scanner`): `configPath string, reload ReloadFunc`, and assign them. Add the route in `Mount` after `/api/newly-vulnerable`:

```go
	protected.Post("/api/reload", d.handleReload)
```

Update **all** `dashboard.New(` call sites to append `, "", nil` (configPath, reload) — except `main.go` which passes the real path + closure (Task 6). Grep: handlers_test.go, remove_test.go, stream_cap_test.go, upstream_test.go, timeseries_test.go, accesslog_test.go, tree_test.go, rescan_test.go.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/dashboard/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/dashboard/settings.go internal/dashboard/handlers.go internal/dashboard/*_test.go
git commit -m "feat(dashboard): POST /api/reload + ReloadFunc plumbing"
```

---

### Task 6: main.go — reload closure, SIGHUP, PID file

**Files:**
- Modify: `cmd/escrow/main.go`

- [ ] **Step 1: Capture a restart-only snapshot + build the reload closure**

After `cfg` is loaded and `polEngine`, `scanner`, `wh` exist (and before `dashboard.New`), add:

```go
	// Snapshot of restart-only fields, used to report what a reload can't apply live.
	restartSnapshot := func(c config.Config) map[string]string {
		return map[string]string{
			"server":     fmt.Sprintf("%s:%d:%s:%s", c.Server.Host, c.Server.Port, c.Server.TLSCertFile, c.Server.TLSKeyFile),
			"storage":    fmt.Sprintf("%s:%s", c.Storage.Backend, c.Storage.Disk.Path),
			"ecosystems": fmt.Sprintf("%v", c.Ecosystems),
			"secret":     c.Dashboard.Secret,
			"paths":      fmt.Sprintf("%s:%s:%s:%s", c.AllowlistPath, c.BlocklistPath, c.EventLogPath, c.Server.AccessLogPath),
		}
	}
	startupSnapshot := restartSnapshot(cfg)

	reloadFn := func() (dashboard.ReloadResult, error) {
		newCfg, err := config.Load(*cfgPath)
		if err != nil {
			return dashboard.ReloadResult{}, err
		}
		if err := newCfg.Validate(); err != nil {
			return dashboard.ReloadResult{}, err
		}
		var restart []string
		now := restartSnapshot(newCfg)
		for k, v := range startupSnapshot {
			if now[k] != v {
				restart = append(restart, k)
			}
		}
		// Apply the live-reloadable subset.
		polEngine.SetConfig(newCfg.Policy)
		// rescan
		minSev := "HIGH"
		if newCfg.Policy != nil && newCfg.Policy.OSV != nil && newCfg.Policy.OSV.MinSeverity != "" {
			minSev = newCfg.Policy.OSV.MinSeverity
		}
		enabled, autoBlock, interval := true, true, 24
		if rc := newCfg.Rescan; rc != nil {
			if rc.Enabled != nil {
				enabled = *rc.Enabled
			}
			if rc.AutoBlock != nil {
				autoBlock = *rc.AutoBlock
			}
			if rc.MinSeverity != "" {
				minSev = rc.MinSeverity
			}
			if rc.IntervalHours > 0 {
				interval = rc.IntervalHours
			}
		}
		if scanner != nil {
			scanner.SetConfig(rescan.Config{Enabled: enabled, IntervalHours: interval, AutoBlock: autoBlock, MinSeverity: minSev})
		}
		if wh != nil {
			wh.SetURL(newCfg.Alerts.WebhookURL)
		}
		reloaded := []string{"policy", "rescan", "alerts"}
		log.Info().Strs("reloaded", reloaded).Strs("restart_required", restart).Msg("config reloaded")
		return dashboard.ReloadResult{Reloaded: reloaded, RestartRequired: restart}, nil
	}
```

(`scanner` is declared with `var scanner *rescan.Scanner` earlier; ensure `reloadFn` is defined after that block. `fmt` is already imported.)

- [ ] **Step 2: Pass configPath + reloadFn to the dashboard**

Update the `dashboard.New(...)` call to append `*cfgPath, reloadFn`:

```go
		dash := dashboard.New(cfg.Dashboard, evLog, log.Logger, allowList, blockList, c,
			srv.AccessRing(), upstreamLog, dlStore, scanner, *cfgPath, reloadFn)
```

- [ ] **Step 3: Write a PID file and handle SIGHUP**

Before the `quit` signal block, add the PID file (under the cache dir on disk, else the config dir):

```go
	pidPath := filepath.Join(filepath.Dir(*cfgPath), "escrow.pid")
	if cfg.Storage.Backend == "disk" {
		pidPath = filepath.Join(config.ExpandPath(cfg.Storage.Disk.Path), "escrow.pid")
	}
	_ = os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o644)
	defer os.Remove(pidPath)

	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			if _, err := reloadFn(); err != nil {
				log.Error().Err(err).Msg("SIGHUP reload failed; keeping previous config")
			}
		}
	}()
```

Add imports `"strconv"` (and confirm `"os"`, `"syscall"`, `"os/signal"`, `"path/filepath"` are present).

- [ ] **Step 4: Build**

Run: `go build ./... && go vet ./...`
Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add cmd/escrow/main.go
git commit -m "feat(cmd): config reload closure, SIGHUP handler, and PID file"
```

---

## PHASE 2 — Settings page

### Task 7: `GET /api/settings`

**Files:**
- Modify: `internal/dashboard/settings.go`
- Modify: `internal/dashboard/handlers.go` (route)
- Test: `internal/dashboard/settings_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/dashboard/settings_test.go`:

```go
func TestGetSettings_MasksPasswordFlag(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "escrow.toml")
	os.WriteFile(path, []byte("[server]\n  port = 7888\n[dashboard]\n  password = \"sekret\"\n"), 0o600)
	al, _ := allow.New("")
	cfg := config.DashboardConfig{Enabled: true, Path: "/dashboard", Username: "admin", Password: "pass", Secret: "aabbccddeeff00112233445566778899"}
	dash := dashboard.New(cfg, eventlog.New(10), zerolog.Nop(), al, nil, nil, nil, nil, nil, nil, path, nil)
	r := chi.NewRouter()
	dash.Mount(r)

	req := authenticatedRequest(t, http.MethodGet, "/dashboard/api/settings", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var out struct {
		Config          map[string]any `json:"config"`
		PasswordEditable bool          `json:"password_editable"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.False(t, out.PasswordEditable)
	require.NotEmpty(t, out.Config)
}
```

Add `os`, `path/filepath` imports if missing.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/dashboard/ -run TestGetSettings`
Expected: FAIL — route 404.

- [ ] **Step 3: Implement GET + route**

In `internal/dashboard/settings.go` add:

```go
func (d *Dashboard) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if d.configPath == "" {
		http.Error(w, `{"error":"settings unavailable (no config path)"}`, http.StatusServiceUnavailable)
		return
	}
	cfg, err := config.Load(d.configPath)
	if err != nil {
		http.Error(w, `{"error":"could not read config"}`, http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{
		"config":            cfg,
		"password_editable": false,
		"config_path":       d.configPath,
	})
}
```

Add `"github.com/jverhoeks/escrow/internal/config"` to the settings.go imports. Register the route in `handlers.go` `Mount` (after `/api/reload`):

```go
	protected.Get("/api/settings", d.handleGetSettings)
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/dashboard/ -run TestGetSettings`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/dashboard/settings.go internal/dashboard/handlers.go internal/dashboard/settings_test.go
git commit -m "feat(dashboard): GET /api/settings (password read-only)"
```

---

### Task 8: `POST /api/settings` (password-immutable, validate, save, reload)

**Files:**
- Modify: `internal/dashboard/settings.go`
- Modify: `internal/dashboard/handlers.go` (route)
- Test: `internal/dashboard/settings_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/dashboard/settings_test.go`:

```go
func TestPostSettings_PreservesPasswordAndReloads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "escrow.toml")
	os.WriteFile(path, []byte("[server]\n  port = 7888\n[dashboard]\n  password = \"original\"\n"), 0o600)
	al, _ := allow.New("")
	cfg := config.DashboardConfig{Enabled: true, Path: "/dashboard", Username: "admin", Password: "pass", Secret: "aabbccddeeff00112233445566778899"}
	reloaded := false
	reload := func() (dashboard.ReloadResult, error) { reloaded = true; return dashboard.ReloadResult{Reloaded: []string{"policy"}}, nil }
	dash := dashboard.New(cfg, eventlog.New(10), zerolog.Nop(), al, nil, nil, nil, nil, nil, nil, path, reload)
	r := chi.NewRouter()
	dash.Mount(r)

	// Client attempts to change the password AND the port.
	body := []byte(`{"server":{"port":9000},"dashboard":{"password":"hacked"}}`)
	req := authenticatedRequest(t, http.MethodPost, "/dashboard/api/settings", body)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, reloaded)

	saved, _ := config.Load(path)
	require.Equal(t, 9000, saved.Server.Port)           // editable field applied
	require.Equal(t, "original", saved.Dashboard.Password) // password NOT changed
}

func TestPostSettings_RejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "escrow.toml")
	os.WriteFile(path, []byte("[server]\n  port = 7888\n"), 0o600)
	al, _ := allow.New("")
	cfg := config.DashboardConfig{Enabled: true, Path: "/dashboard", Username: "admin", Password: "pass", Secret: "aabbccddeeff00112233445566778899"}
	dash := dashboard.New(cfg, eventlog.New(10), zerolog.Nop(), al, nil, nil, nil, nil, nil, nil, path, func() (dashboard.ReloadResult, error) { return dashboard.ReloadResult{}, nil })
	r := chi.NewRouter()
	dash.Mount(r)

	body := []byte(`{"server":{"port":70000}}`) // out of range
	req := authenticatedRequest(t, http.MethodPost, "/dashboard/api/settings", body)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	saved, _ := config.Load(path)
	require.Equal(t, 7888, saved.Server.Port) // unchanged
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/dashboard/ -run TestPostSettings`
Expected: FAIL — route 404.

- [ ] **Step 3: Implement POST + route**

In `internal/dashboard/settings.go` add (`io` import for the body limit; `maxBodyBytes` already exists in the package):

```go
func (d *Dashboard) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	if !d.originOK(r) {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if d.configPath == "" {
		http.Error(w, `{"error":"settings unavailable (no config path)"}`, http.StatusServiceUnavailable)
		return
	}
	// Start from the on-disk config so omitted keys keep their values.
	cur, err := config.Load(d.configPath)
	if err != nil {
		http.Error(w, `{"error":"could not read current config"}`, http.StatusInternalServerError)
		return
	}
	currentPassword := cur.Dashboard.Password

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	incoming := cur // copy, then overlay decoded JSON
	if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	// Password is immutable via this API — always restore the on-disk value.
	incoming.Dashboard.Password = currentPassword

	if err := incoming.Validate(); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	if err := config.Save(d.configPath, incoming); err != nil {
		http.Error(w, `{"error":"failed to write config"}`, http.StatusInternalServerError)
		return
	}
	res := ReloadResult{}
	if d.reload != nil {
		res, _ = d.reload() // best-effort; file is already saved
	}
	username, _ := d.auth.Username(r)
	d.logger.Info().Str("operator", username).Msg("settings saved via dashboard")
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "reloaded": res.Reloaded, "restart_required": res.RestartRequired})
}
```

Register the route in `handlers.go` `Mount`:

```go
	protected.Post("/api/settings", d.handleSaveSettings)
```

Note on JSON-overlay: decoding JSON into a copy of the current `Config` means only keys present in the payload override; nested structs (e.g. `server`) replace only the fields the client sends *within* that object that JSON includes — to keep this predictable, the frontend (Task 9) sends the full config object it received from GET (minus password), so the overlay is a full replacement. The password restore guarantees immutability regardless.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/dashboard/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/dashboard/settings.go internal/dashboard/handlers.go internal/dashboard/settings_test.go
git commit -m "feat(dashboard): POST /api/settings — immutable password, validate, save, reload"
```

---

### Task 9: Frontend — Settings tab + Reload button

**Files:**
- Modify: `internal/dashboard/static/index.html`

Verified by running the app + Playwright. JSON contracts: `GET /api/settings` → `{config:{...full Config...}, password_editable:false, config_path}`; `POST /api/settings` (body = the full config object from GET, password field ignored server-side) → `{ok, reloaded:[...], restart_required:[...]}`; `POST /api/reload` → `{reloaded:[...], restart_required:[...]}`.

- [ ] **Step 1: Add the Settings tab + view**

Add a nav tab `<div class="nav-tab" id="tab-settings" onclick="setTab('settings')">Settings</div>` (last), a `<div id="view-settings" class="panel" ...>` containing a `#settings-form` container, a Save button, a "Reload from file" button, and a `#settings-banner`. Handle `'settings'` in `setTab` (display + `loadSettings()`) and add it to the view-display array.

- [ ] **Step 2: Implement loadSettings/renderSettings**

`loadSettings()` fetches `/api/settings`, stores the returned `config` object in a module var `settingsCfg`, and renders a sectioned form. Build inputs generically from the config object grouped by top-level key (server/storage/ecosystems/policy/rescan/alerts); render scalars as text/number/checkbox inputs bound by path. Render `dashboard.password` as a **disabled** input with a reveal (type toggle) and the note "managed in config file". Mark server/storage/secret/paths inputs with a ⚠ "restart required" hint.

- [ ] **Step 3: Implement saveSettings + reloadConfig**

```js
async function saveSettings() {
  // Re-read inputs into settingsCfg by their data-path, then POST the whole object.
  collectSettingsForm();
  const banner = document.getElementById('settings-banner');
  try {
    const resp = await fetch(BASE + '/api/settings', {method:'POST', headers:{'Content-Type':'application/json','X-Escrow-Request':'1'}, body: JSON.stringify(settingsCfg)});
    const res = await resp.json();
    if (!resp.ok) { banner.textContent = 'Error: ' + (res.error || resp.status); banner.className='settings-banner err'; return; }
    banner.className = 'settings-banner ok';
    banner.textContent = 'Saved. Reloaded: ' + ((res.reloaded||[]).join(', ') || 'none')
      + ((res.restart_required||[]).length ? ' · Restart required for: ' + res.restart_required.join(', ') : '');
  } catch(e) { banner.textContent = 'Error: ' + e; banner.className='settings-banner err'; }
}
async function reloadConfig() {
  const banner = document.getElementById('settings-banner');
  try {
    const resp = await fetch(BASE + '/api/reload', {method:'POST', headers:{'X-Escrow-Request':'1'}});
    const res = await resp.json();
    banner.className = resp.ok ? 'settings-banner ok' : 'settings-banner err';
    banner.textContent = resp.ok
      ? 'Reloaded: ' + ((res.reloaded||[]).join(', ')||'none') + ((res.restart_required||[]).length ? ' · Restart required for: ' + res.restart_required.join(', ') : '')
      : 'Error: ' + (res.error||resp.status);
  } catch(e) { banner.textContent = 'Error: ' + e; banner.className='settings-banner err'; }
}
```

`collectSettingsForm()` walks the rendered inputs and writes each value back into `settingsCfg` by its `data-path` (e.g. `policy.osv.min_severity`), coercing number/checkbox types. Never write the password input.

- [ ] **Step 4: CSS**

Add `.settings-banner { padding:10px 16px; border-radius:6px; font-size:13px; margin:12px; }`, `.settings-banner.ok { background: color-mix(in srgb, var(--status-allowed) 15%, transparent); color: var(--status-allowed); }`, `.settings-banner.err { background: color-mix(in srgb, var(--status-blocked) 15%, transparent); color: var(--status-blocked); }`, plus simple form-row styling reusing existing vars.

- [ ] **Step 5: Verify in the browser**

Run a binary with a disk config, log in, open Settings: confirm the password field is disabled/revealable; change `policy.osv.min_severity` and Save → banner shows "Reloaded: policy, rescan, alerts"; change `storage.disk.path` and Save → banner lists `storage` under restart-required; confirm `escrow.toml` changed and `escrow.toml.bak` exists. Screenshot light + dark.

- [ ] **Step 6: Commit**

```bash
git add internal/dashboard/static/index.html
git commit -m "feat(dashboard): settings page with save + live reload"
```

---

## PHASE 3 — CLI

### Task 10: `escrow-cli reload`

**Files:**
- Create: `cmd/escrow-cli/reload.go`
- Modify: `cmd/escrow-cli/main.go` (dispatch + usage)

- [ ] **Step 1: Implement `runReload`**

Create `cmd/escrow-cli/reload.go`:

```go
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// runReload signals the running escrow proxy to re-read its config (SIGHUP).
func runReload(args []string) {
	fs := flag.NewFlagSet("reload", flag.ExitOnError)
	pidPath := fs.String("pid", "", "path to escrow.pid (default: search common locations)")
	fs.Parse(args) //nolint:errcheck

	path := *pidPath
	if path == "" {
		for _, c := range []string{"./escrow-cache/escrow.pid", "escrow.pid"} {
			if _, err := os.Stat(c); err == nil {
				path = c
				break
			}
		}
	}
	if path == "" {
		fmt.Fprintln(os.Stderr, "escrow-cli reload: could not find escrow.pid; pass --pid /path/to/escrow.pid")
		os.Exit(1)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "escrow-cli reload: read %s: %v\n", path, err)
		os.Exit(1)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "escrow-cli reload: invalid pid in %s\n", path)
		os.Exit(1)
	}
	if err := syscall.Kill(pid, syscall.SIGHUP); err != nil {
		fmt.Fprintf(os.Stderr, "escrow-cli reload: signal pid %d: %v\n", pid, err)
		os.Exit(1)
	}
	fmt.Printf("sent SIGHUP to escrow (pid %d) at %s — config reloaded; check the proxy log for restart-required fields\n", pid, filepath.Clean(path))
}
```

Note: `syscall.Kill` is unix-only. If a Windows build is needed, add a `reload_windows.go` stub that prints "reload via SIGHUP is unsupported on Windows; use the dashboard Reload button or restart escrow" — but escrow's CLI is macOS/Linux-focused, so a single unix file is acceptable for v1.

- [ ] **Step 2: Wire dispatch**

In `cmd/escrow-cli/main.go`, add a case to the top-level `switch os.Args[1]`:

```go
	case "reload":
		runReload(os.Args[2:])
```

and add a usage line near the others: `  escrow-cli reload                  signal the running proxy to reload its config (SIGHUP)`.

- [ ] **Step 3: Build**

Run: `go build ./cmd/escrow-cli/`
Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add cmd/escrow-cli/reload.go cmd/escrow-cli/main.go
git commit -m "feat(cli): escrow-cli reload signals the proxy via SIGHUP"
```

---

### Task 11: `escrow-cli live`

**Files:**
- Create: `cmd/escrow-cli/live.go`
- Test: `cmd/escrow-cli/live_test.go`

- [ ] **Step 1: Write the failing test (filter logic)**

Create `cmd/escrow-cli/live_test.go`:

```go
package main

import "testing"

func TestLiveMatch(t *testing.T) {
	e := liveEvent{Ecosystem: "npm", Action: "allow", Kind: "downloaded"}
	if !liveMatch(e, "", "all") {
		t.Error("all should match")
	}
	if !liveMatch(e, "npm", "downloaded") {
		t.Error("npm/downloaded should match")
	}
	if liveMatch(e, "pypi", "all") {
		t.Error("pypi filter should exclude npm")
	}
	if liveMatch(e, "", "scanned") {
		t.Error("scanned filter should exclude a downloaded event")
	}
	if !liveMatch(liveEvent{Action: "block"}, "", "blocked") {
		t.Error("blocked filter should match a block event")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/escrow-cli/ -run TestLiveMatch`
Expected: FAIL — `liveEvent`/`liveMatch` undefined.

- [ ] **Step 3: Implement `runLive` + `liveMatch`**

Create `cmd/escrow-cli/live.go`:

```go
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type liveEvent struct {
	Timestamp time.Time `json:"timestamp"`
	Ecosystem string    `json:"ecosystem"`
	Package   string    `json:"package"`
	Action    string    `json:"action"`
	Kind      string    `json:"kind"`
	Reason    string    `json:"reason"`
}

// liveMatch reports whether e passes the --eco and --activity filters.
func liveMatch(e liveEvent, eco, activity string) bool {
	if eco != "" && e.Ecosystem != eco {
		return false
	}
	switch activity {
	case "", "all":
		return true
	case "downloaded":
		return e.Kind == "downloaded"
	case "scanned":
		return e.Kind != "downloaded" && e.Action != "block"
	case "blocked":
		return e.Action == "block"
	default:
		return true
	}
}

func runLive(args []string) {
	fs := flag.NewFlagSet("live", flag.ExitOnError)
	eco := fs.String("eco", "", "filter by ecosystem (npm, pypi, cargo, go, ...)")
	activity := fs.String("activity", "all", "all|downloaded|scanned|blocked")
	path := fs.String("path", "", "event-log JSONL path (default: ./escrow-cache/escrow-events.jsonl)")
	fs.Parse(args) //nolint:errcheck

	p := *path
	if p == "" {
		p = filepath.Join("escrow-cache", "escrow-events.jsonl")
	}
	f, err := os.Open(p)
	if err != nil {
		fmt.Fprintf(os.Stderr, "escrow-cli live: cannot open event log %s: %v\n", p, err)
		fmt.Fprintln(os.Stderr, "enable event-log persistence (disk backend, or set eventlog_path) or pass --path")
		os.Exit(1)
	}
	defer f.Close()

	// Start tailing from the end so we show new activity.
	f.Seek(0, io.SeekEnd)
	reader := bufio.NewReader(f)
	fmt.Printf("watching %s  (eco=%s activity=%s) — Ctrl-C to stop\n", p, orAll(*eco), *activity)
	for {
		line, err := reader.ReadString('\n')
		if err == io.EOF {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if err != nil {
			return
		}
		var e liveEvent
		if json.Unmarshal([]byte(strings.TrimSpace(line)), &e) != nil {
			continue
		}
		if !liveMatch(e, *eco, *activity) {
			continue
		}
		fmt.Println(formatLive(e))
	}
}

func orAll(s string) string {
	if s == "" {
		return "all"
	}
	return s
}

func formatLive(e liveEvent) string {
	st := "✓ allow"
	switch {
	case e.Action == "block":
		st = "✕ block"
	case e.Action == "warn":
		st = "⚠ warn"
	}
	kind := e.Kind
	if kind == "" {
		kind = "scanned"
	}
	return fmt.Sprintf("%s  %-8s  %-9s  %-40s  %s",
		e.Timestamp.Local().Format("15:04:05"), e.Ecosystem, st, e.Package, kind)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/escrow-cli/ -run TestLiveMatch`
Expected: PASS.

- [ ] **Step 5: Wire dispatch**

In `cmd/escrow-cli/main.go`, add:

```go
	case "live":
		runLive(os.Args[2:])
```

and a usage line: `  escrow-cli live [--eco npm] [--activity downloaded|scanned|blocked|all]   tail package events`.

- [ ] **Step 6: Build + test + commit**

```bash
go build ./cmd/escrow-cli/ && go test ./cmd/escrow-cli/
git add cmd/escrow-cli/live.go cmd/escrow-cli/live_test.go cmd/escrow-cli/main.go
git commit -m "feat(cli): escrow-cli live tails package events with eco/activity filters"
```

---

## Self-Review (completed during planning)

- **Spec coverage:** live reconfig (T1–T3); Validate+Save w/ backup (T4); reload routine + SIGHUP + PID + `POST /api/reload` (T5–T6); GET settings w/ read-only password (T7); POST settings w/ server-enforced password immutability + validate + save + reload (T8); frontend settings + reload button (T9); CLI reload (T10); CLI live w/ `--eco`/`--activity` (T11). Save→reload model (T8). Hot-reload subset + restart-required reporting (T6 closure).
- **Placeholder scan:** none; backend steps have full code. T9 is frontend-by-verification with concrete JS + JSON contracts.
- **Type consistency:** `dashboard.New` final signature adds exactly `configPath string, reload ReloadFunc` after `scanner` (T5), matched in main.go (T6) and every test call site; `ReloadResult{Reloaded, RestartRequired []string}` used identically in T5/T6/T8/T9; `liveEvent`/`liveMatch` defined and used in T11; `config.Validate`/`config.Save` defined in T4 and used in T6/T8.
- **Ordering:** T5 introduces the final `dashboard.New` signature; do T5 before T7/T8 (same package). T1–T4 are independent and can land first. T6 depends on T1–T5. CLI (T10–T11) is independent of the dashboard tasks.
