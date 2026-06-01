# `escrow-cli tui` — Terminal Dashboard — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An interactive `escrow-cli tui` that mirrors the web dashboard's key views (Live+stats, CVEs, Newly Vulnerable, Packages tree, Access log, Upstream) over the authenticated API, with an offline event-log-tail fallback.

**Architecture:** A `cmd/escrow-cli/tui` package: a `Client` (proxy discovery via `runtime.json`, auto-login from `escrow.toml`, typed JSON fetchers + SSE) and a Bubble Tea `Model` (tabs, filters, online/offline). Read-only endpoints need only the session cookie, so no CSRF header is required. Rendering uses Lipgloss; the `Client` and the model's pure update logic are unit-tested, rendering is verified live.

**Tech Stack:** Go, charmbracelet/bubbletea + lipgloss + bubbles (new), `internal/config` (discovery), testify.

**Spec:** `docs/superpowers/specs/2026-05-31-escrow-cli-tui-design.md`

**Note on commits:** the `pre-commit` tool isn't on PATH and there's no `.pre-commit-config.yaml`, so commit with `git commit --no-verify`. Commits are gpg-signed via `git config gpg.program /opt/homebrew/bin/gpg` (already set).

---

## File Structure

**New files:**
- `cmd/escrow-cli/tui/client.go` — `Client`: discovery, `Login`, typed fetchers, `Stream` (SSE).
- `cmd/escrow-cli/tui/client_test.go`
- `cmd/escrow-cli/tui/model.go` — Bubble Tea `Model` + `Update` (nav/filters/data msgs) + msg types.
- `cmd/escrow-cli/tui/model_test.go`
- `cmd/escrow-cli/tui/views.go` — Lipgloss rendering per tab + shared styles.
- `cmd/escrow-cli/tui/run.go` — `Run(Options) error`: wire client, login, offline fallback, start program.
- `cmd/escrow-cli/tui.go` — `runTUI(args)` flag parsing → `tui.Run`.

**Modified files:**
- `cmd/escrow-cli/main.go` — dispatch `case "tui"` + `--tui` alias + usage line.
- `go.mod` / `go.sum` — add the charmbracelet deps.

---

## Task 1: Add deps + the API client

**Files:**
- Create: `cmd/escrow-cli/tui/client.go`
- Test: `cmd/escrow-cli/tui/client_test.go`
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Add the TUI dependencies**

Run:
```bash
go get github.com/charmbracelet/bubbletea@latest github.com/charmbracelet/lipgloss@latest github.com/charmbracelet/bubbles@latest
```
Expected: `go.mod` gains the three `charmbracelet/*` requires (and transitive deps in `go.sum`).

- [ ] **Step 2: Write the failing test**

Create `cmd/escrow-cli/tui/client_test.go`:

```go
package tui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func testServer(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/dashboard/login", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.FormValue("username") == "root" && r.FormValue("password") == "escrow" {
			http.SetCookie(w, &http.Cookie{Name: "escrow_session", Value: "ok", Path: "/"})
			w.WriteHeader(http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusFound) // login always 302s; auth is enforced on protected routes
	})
	mux.HandleFunc("/dashboard/api/stats", func(w http.ResponseWriter, r *http.Request) {
		if c, _ := r.Cookie("escrow_session"); c == nil {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"blocked":2,"warned":1,"allowed":5,"top_blocked":[{"package":"x","count":2}]}`))
	})
	mux.HandleFunc("/dashboard/api/cves", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":"GHSA-x","severity":"HIGH","ecosystem":"npm","package":"lodash","version":"4.17.11"}]`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestClient_LoginThenStats(t *testing.T) {
	srv := testServer(t)
	c, err := NewClient(srv.URL, "/dashboard", "root", "escrow")
	require.NoError(t, err)
	require.NoError(t, c.Login())

	st, err := c.Stats()
	require.NoError(t, err)
	require.Equal(t, 2, st.Blocked)
	require.Equal(t, 5, st.Allowed)

	cves, err := c.CVEs()
	require.NoError(t, err)
	require.Len(t, cves, 1)
	require.Equal(t, "GHSA-x", cves[0].ID)
}

func TestClient_StatsWithoutLoginUnauthorized(t *testing.T) {
	srv := testServer(t)
	c, _ := NewClient(srv.URL, "/dashboard", "root", "escrow")
	_, err := c.Stats()
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "unauthor"))
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./cmd/escrow-cli/tui/`
Expected: FAIL — `NewClient` undefined.

- [ ] **Step 4: Implement the client**

Create `cmd/escrow-cli/tui/client.go`:

```go
// Package tui implements `escrow-cli tui`, an interactive terminal dashboard
// that reads the running proxy's authenticated API (with an offline event-log
// fallback handled in run.go).
package tui

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

// Client talks to a running escrow dashboard API using a session cookie.
type Client struct {
	base string // e.g. http://127.0.0.1:7888
	path string // dashboard path, e.g. /dashboard
	user string
	pass string
	http *http.Client
}

func NewClient(base, dashPath, user, pass string) (*Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	if dashPath == "" {
		dashPath = "/dashboard"
	}
	return &Client{
		base: strings.TrimRight(base, "/"),
		path: dashPath,
		user: user, pass: pass,
		http: &http.Client{Timeout: 10 * time.Second, Jar: jar},
	}, nil
}

// Login posts the credentials and stores the session cookie in the jar.
func (c *Client) Login() error {
	form := url.Values{"username": {c.user}, "password": {c.pass}}
	// Don't follow the post-login redirect (we only need the Set-Cookie).
	noRedirect := *c.http
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noRedirect.Post(c.base+c.path+"/login", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("login request failed: %w", err)
	}
	defer resp.Body.Close()
	u, _ := url.Parse(c.base)
	for _, ck := range resp.Cookies() {
		if ck.Name == "escrow_session" && ck.Value != "" {
			c.http.Jar.SetCookies(u, []*http.Cookie{ck})
			return nil
		}
	}
	return fmt.Errorf("login failed: no session cookie (check credentials)")
}

func (c *Client) getJSON(path string, out any) error {
	resp, err := c.http.Get(c.base + c.path + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusFound {
		return fmt.Errorf("unauthorized (HTTP %d) — not logged in", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d for %s", resp.StatusCode, path)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ── Typed responses (mirror the dashboard JSON) ───────────────────────────────

type Stats struct {
	Blocked    int `json:"blocked"`
	Warned     int `json:"warned"`
	Allowed    int `json:"allowed"`
	TopBlocked []struct {
		Package string `json:"package"`
		Count   int    `json:"count"`
	} `json:"top_blocked"`
}

type Event struct {
	Timestamp time.Time `json:"timestamp"`
	Ecosystem string    `json:"ecosystem"`
	Package   string    `json:"package"`
	Action    string    `json:"action"`
	Signal    string    `json:"signal"`
	Reason    string    `json:"reason"`
	Kind      string    `json:"kind"`
}

type CVE struct {
	ID        string `json:"id"`
	Severity  string `json:"severity"`
	Ecosystem string `json:"ecosystem"`
	Package   string `json:"package"`
	Version   string `json:"version"`
}

type NewVuln struct {
	Ecosystem     string `json:"ecosystem"`
	Package       string `json:"package"`
	Version       string `json:"version"`
	Vulns         []string `json:"vulns"`
	DownloadCount int    `json:"download_count"`
}

type TreeEco struct {
	Ecosystem string `json:"ecosystem"`
	Packages  []struct {
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
		Versions  []struct {
			Version       string `json:"version"`
			Action        string `json:"action"`
			Downloaded    bool   `json:"downloaded"`
			DownloadCount int    `json:"download_count"`
			CVECount      int    `json:"cve_count"`
		} `json:"versions"`
	} `json:"packages"`
}

type AccessEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Host      string    `json:"host"`
	Method    string    `json:"method"`
	Path      string    `json:"path"`
	Status    int       `json:"status"`
	Bytes     int64     `json:"bytes"`
}

type UpstreamEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Ecosystem string    `json:"ecosystem"`
	Method    string    `json:"method"`
	URL       string    `json:"url"`
	Status    int       `json:"status"`
	MS        float64   `json:"ms"`
}

func (c *Client) Stats() (Stats, error)            { var s Stats; return s, c.getJSON("/api/stats", &s) }
func (c *Client) Events(n int) ([]Event, error)    { var e []Event; return e, c.getJSON(fmt.Sprintf("/api/events?n=%d", n), &e) }
func (c *Client) CVEs() ([]CVE, error)             { var v []CVE; return v, c.getJSON("/api/cves", &v) }
func (c *Client) NewlyVulnerable() ([]NewVuln, error) { var v []NewVuln; return v, c.getJSON("/api/newly-vulnerable", &v) }
func (c *Client) PackagesTree() ([]TreeEco, error) { var t []TreeEco; return t, c.getJSON("/api/packages/tree", &t) }
func (c *Client) AccessLog(n int) ([]AccessEntry, error) { var a []AccessEntry; return a, c.getJSON(fmt.Sprintf("/api/accesslog?n=%d", n), &a) }
func (c *Client) UpstreamLog(n int) ([]UpstreamEntry, error) { var u []UpstreamEntry; return u, c.getJSON(fmt.Sprintf("/api/upstreamlog?n=%d", n), &u) }

// Stream connects to the SSE endpoint and emits each event on the returned
// channel until ctx is cancelled. Lines are "data: {json}" (comments ignored).
func (c *Client) Stream(ctx context.Context) (<-chan Event, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.base+c.path+"/api/stream", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("stream HTTP %d", resp.StatusCode)
	}
	ch := make(chan Event, 64)
	go func() {
		defer resp.Body.Close()
		defer close(ch)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var e Event
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e) == nil {
				select {
				case ch <- e:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return ch, nil
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./cmd/escrow-cli/tui/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum cmd/escrow-cli/tui/client.go cmd/escrow-cli/tui/client_test.go
git commit --no-verify -m "feat(tui): API client with login, typed fetchers, and SSE stream"
```

---

## Task 2: Bubble Tea model + navigation

**Files:**
- Create: `cmd/escrow-cli/tui/model.go`
- Test: `cmd/escrow-cli/tui/model_test.go`

- [ ] **Step 1: Write the failing test**

Create `cmd/escrow-cli/tui/model_test.go`:

```go
package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

func TestModel_TabNavigationWraps(t *testing.T) {
	m := NewModel(nil) // offline (nil client) is fine for nav
	require.Equal(t, 0, m.tab)
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	require.Equal(t, 1, m2.(Model).tab)
	// wrap: from last tab Tab returns to 0
	mm := m2.(Model)
	for i := 0; i < len(tabNames); i++ {
		next, _ := mm.Update(tea.KeyMsg{Type: tea.KeyTab})
		mm = next.(Model)
	}
	require.Equal(t, 1, mm.tab) // len wraps round to where we were +1
}

func TestModel_FilterCycling(t *testing.T) {
	m := NewModel(nil)
	require.Equal(t, "", m.eco)
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	require.Equal(t, ecoCycle[1], m2.(Model).eco)

	m3, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	require.Equal(t, activityCycle[1], m3.(Model).activity)
}

func TestModel_StreamEventPrependsAndCounts(t *testing.T) {
	m := NewModel(nil)
	m2, _ := m.Update(streamMsg{Event{Ecosystem: "npm", Action: "block", Kind: "scanned"}})
	mm := m2.(Model)
	require.Len(t, mm.events, 1)
	require.Equal(t, 1, mm.live.Blocked)
}

func TestModel_QuitKey(t *testing.T) {
	m := NewModel(nil)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	require.NotNil(t, cmd) // tea.Quit
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/escrow-cli/tui/ -run TestModel`
Expected: FAIL — `NewModel`/`Model`/`tabNames`/`ecoCycle`/`activityCycle`/`streamMsg` undefined.

- [ ] **Step 3: Implement the model**

Create `cmd/escrow-cli/tui/model.go`:

```go
package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

var tabNames = []string{"Live", "CVEs", "Newly Vuln", "Packages", "Access", "Upstream"}
var ecoCycle = []string{"", "npm", "pypi", "cargo", "go", "composer", "nuget", "maven"}
var activityCycle = []string{"all", "downloaded", "scanned", "blocked"}

// Messages delivered to Update.
type streamMsg struct{ e Event }
type errMsg struct{ err error }
type statsMsg struct{ s Stats }
type eventsMsg struct{ events []Event }
type cvesMsg struct{ cves []CVE }
type newVulnMsg struct{ rows []NewVuln }
type treeMsg struct{ tree []TreeEco }
type accessMsg struct{ rows []AccessEntry }
type upstreamMsg struct{ rows []UpstreamEntry }

// Model is the Bubble Tea state for the TUI.
type Model struct {
	client   *Client // nil = offline
	offline  bool
	tab      int
	eco      string
	activity string
	width    int
	height   int
	status   string // status/error line

	live     Stats // running counts from the stream
	events   []Event
	cves     []CVE
	newvuln  []NewVuln
	tree     []TreeEco
	access   []AccessEntry
	upstream []UpstreamEntry
	scroll   int // per-tab scroll offset (reset on tab switch)
}

func NewModel(c *Client) Model {
	return Model{client: c, offline: c == nil, eco: "", activity: "all", status: ""}
}

func (m Model) Init() tea.Cmd { return nil }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "tab", "right":
			m.tab = (m.tab + 1) % len(tabNames)
			m.scroll = 0
			return m, m.loadTab()
		case "shift+tab", "left":
			m.tab = (m.tab - 1 + len(tabNames)) % len(tabNames)
			m.scroll = 0
			return m, m.loadTab()
		case "e":
			m.eco = next(ecoCycle, m.eco)
			return m, m.loadTab()
		case "a":
			m.activity = next(activityCycle, m.activity)
			return m, m.loadTab()
		case "r":
			return m, m.loadTab()
		case "down", "j":
			m.scroll++
		case "up", "k":
			if m.scroll > 0 {
				m.scroll--
			}
		}
		// number keys 1-6 jump to a tab
		if s := msg.String(); len(s) == 1 && s[0] >= '1' && s[0] <= '6' {
			m.tab = int(s[0] - '1')
			m.scroll = 0
			return m, m.loadTab()
		}
	case streamMsg:
		m.events = append([]Event{msg.e}, m.events...)
		if len(m.events) > 500 {
			m.events = m.events[:500]
		}
		switch msg.e.Action {
		case "block":
			m.live.Blocked++
		case "warn":
			m.live.Warned++
		case "allow":
			m.live.Allowed++
		}
	case statsMsg:
		m.live = msg.s
	case eventsMsg:
		m.events = msg.events
	case cvesMsg:
		m.cves = msg.cves
	case newVulnMsg:
		m.newvuln = msg.rows
	case treeMsg:
		m.tree = msg.tree
	case accessMsg:
		m.access = msg.rows
	case upstreamMsg:
		m.upstream = msg.rows
	case errMsg:
		m.status = "error: " + msg.err.Error()
	}
	return m, nil
}

func next(cycle []string, cur string) string {
	for i, v := range cycle {
		if v == cur {
			return cycle[(i+1)%len(cycle)]
		}
	}
	return cycle[0]
}

// loadTab returns a command that fetches the active tab's data (no-op offline).
func (m Model) loadTab() tea.Cmd {
	if m.client == nil {
		return nil
	}
	c := m.client
	switch m.tab {
	case 0:
		return func() tea.Msg { s, err := c.Stats(); if err != nil { return errMsg{err} }; return statsMsg{s} }
	case 1:
		return func() tea.Msg { v, err := c.CVEs(); if err != nil { return errMsg{err} }; return cvesMsg{v} }
	case 2:
		return func() tea.Msg { v, err := c.NewlyVulnerable(); if err != nil { return errMsg{err} }; return newVulnMsg{v} }
	case 3:
		return func() tea.Msg { t, err := c.PackagesTree(); if err != nil { return errMsg{err} }; return treeMsg{t} }
	case 4:
		return func() tea.Msg { a, err := c.AccessLog(200); if err != nil { return errMsg{err} }; return accessMsg{a} }
	case 5:
		return func() tea.Msg { u, err := c.UpstreamLog(200); if err != nil { return errMsg{err} }; return upstreamMsg{u} }
	}
	return nil
}
```

Note: the test uses `m.Update(streamMsg{...})` with a bare `Event` — adjust `streamMsg` to wrap the event as `streamMsg{e Event}` and the test to `streamMsg{e: Event{...}}`, OR make `streamMsg` a named type `type streamMsg struct{ e Event }` and the test construct `streamMsg{Event{...}}` positionally. Keep the test and type in sync (positional `streamMsg{Event{...}}` works with the single-field struct).

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/escrow-cli/tui/ -run TestModel`
Expected: PASS. (Fix the `streamMsg` field access in the test/impl to match — single field `e`.)

- [ ] **Step 5: Commit**

```bash
git add cmd/escrow-cli/tui/model.go cmd/escrow-cli/tui/model_test.go
git commit --no-verify -m "feat(tui): Bubble Tea model — tabs, filters, data messages"
```

---

## Task 3: Views (Lipgloss rendering) + run wiring + dispatch

**Files:**
- Create: `cmd/escrow-cli/tui/views.go`, `cmd/escrow-cli/tui/run.go`, `cmd/escrow-cli/tui.go`
- Modify: `cmd/escrow-cli/main.go`

This task is verified by running `escrow-cli tui` against a live escrow (rendering isn't unit-tested). Implement `View()` on `Model` and the run wiring.

- [ ] **Step 1: Implement `views.go`**

Add `func (m Model) View() string` plus Lipgloss styles. Layout: a top bar (`ESCROW tui` + online/offline + `eco=<>` `activity=<>`), a tab strip highlighting `tabNames[m.tab]`, the active view body, and a footer help line (`Tab views · e eco · a activity · r refresh · q quit`). Per-tab body:
- **Live (0):** a stats line (`✓ allowed N  ⚠ warned N  ✕ blocked N` using the live counts) then the most recent events (filtered by `m.eco`/`m.activity`) as rows `time · eco · package · status · kind`, sliced by `m.scroll` to the available height.
- **CVEs (1):** table — Severity · Advisory · Ecosystem · Package · Version.
- **Newly Vuln (2):** table — Advisory(s) · Ecosystem · Package · Version · Downloads.
- **Packages (3):** the tree — ecosystem headers, then `namespace/name` and indented versions with status + downloaded marker (apply the activity filter: when not `all`, show downloaded/blocked per the same rule as the web tree).
- **Access (4):** table — Time · Host · Method · Path · Status.
- **Upstream (5):** table — Time · Eco · Method · URL · Status · ms.

Shared style helpers: `statusBadge(action)` → colored `✓ Allowed`/`⚠ Warned`/`✕ Blocked` (Lipgloss `Foreground` with green/amber/red, CVD-safe), `ecoBadge(eco)`, `sevBadge(sev)`. Truncate cells to `m.width`. Apply the `m.eco`/`m.activity` filters in the Live and Packages renderers (helper `eventMatches(e, eco, activity)` mirroring `escrow-cli live`'s `liveMatch`).

- [ ] **Step 2: Implement `run.go`**

```go
package tui

import (
	"context"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-isatty" // already transitive via bubbletea; otherwise use term.IsTerminal
)

// Options configures the TUI.
type Options struct {
	URL      string // explicit base URL ("" → discover via runtime.json)
	User     string
	Password string
	Path     string // event-log path for offline tail ("" → discover)
}

// Run builds the client, attempts login, and starts the program. On failure it
// falls back to offline mode (event-log tail for the Live view).
func Run(opts Options) error {
	if !isatty.IsTerminal(os.Stdout.Fd()) {
		return fmt.Errorf("escrow-cli tui needs an interactive terminal; use `escrow-cli live` for piped output")
	}
	client, mode := buildClient(opts) // returns (*Client or nil, statusString)
	m := NewModel(client)
	m.status = mode
	p := tea.NewProgram(m, tea.WithAltScreen())
	// Live stream (online) or file tail (offline) feeds events into the program.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startFeed(ctx, client, opts, p) // SSE when online; tailEvents when offline
	_, err := p.Run()
	return err
}
```

Implement `buildClient(opts)` (discover base via `config.ReadRuntime` when `opts.URL==""`; discover creds via the same config search the CLI `live`/`reload` use; `NewClient` + `Login()`; on any error return `nil` + an offline status string), `startFeed` (online: `client.Stream` → `p.Send(streamMsg{e})` in a goroutine with reconnect; offline: tail the JSONL like `live.go` → `p.Send(streamMsg{e})`), and have `Model.Init()` return `m.loadTab()` so the first tab loads on start.

- [ ] **Step 3: Implement `tui.go` + dispatch**

Create `cmd/escrow-cli/tui.go`:

```go
package main

import (
	"flag"

	"github.com/jverhoeks/escrow/cmd/escrow-cli/tui"
)

func runTUI(args []string) {
	fs := flag.NewFlagSet("tui", flag.ExitOnError)
	url := fs.String("url", "", "dashboard base URL (default: discover the local proxy)")
	user := fs.String("user", "", "dashboard username (default: from escrow.toml)")
	pass := fs.String("password", "", "dashboard password (default: from escrow.toml)")
	path := fs.String("path", "", "event-log path for offline mode (default: discover)")
	fs.Parse(args) //nolint:errcheck
	if err := tui.Run(tui.Options{URL: *url, User: *user, Password: *pass, Path: *path}); err != nil {
		fmt.Fprintln(os.Stderr, "escrow-cli tui:", err)
		os.Exit(1)
	}
}
```

(Add `fmt`/`os` imports.) In `cmd/escrow-cli/main.go`, add `case "tui": runTUI(os.Args[2:])`, accept a top-level `--tui` (if `os.Args[1] == "--tui"` → `runTUI(os.Args[2:])`), and add a usage line: `  escrow-cli tui                     interactive terminal dashboard`.

- [ ] **Step 4: Build + run-verify**

Run: `go build ./... && go vet ./...` (clean). Then against a local escrow: `go run ./cmd/escrow-cli tui` — confirm login, tab navigation (Tab/1-6), `e`/`a` filters, `r` refresh, live events streaming on the Live tab, and `q` quits cleanly. Try with escrow stopped → offline banner + file-tail Live.

- [ ] **Step 5: Commit**

```bash
git add cmd/escrow-cli/tui/views.go cmd/escrow-cli/tui/run.go cmd/escrow-cli/tui.go cmd/escrow-cli/main.go
git commit --no-verify -m "feat(tui): views, run wiring, live/offline feed, and CLI dispatch"
```

---

## Task 4: SSE reconnect + offline tail robustness

**Files:**
- Modify: `cmd/escrow-cli/tui/run.go`

- [ ] **Step 1: Reconnect + status**

In `startFeed` (online), wrap the stream in a loop: on channel close / error, send a status message ("reconnecting…"), wait a backoff (e.g. 3s), and re-`Stream` until ctx is cancelled. On a successful (re)connect, clear the status. The offline tailer re-opens the file if it shrinks (rotation), mirroring `live.go`.

- [ ] **Step 2: Build + run-verify**

Run: `go build ./...`. Start the TUI online, stop escrow → confirm "reconnecting…", restart escrow → events resume without restarting the TUI.

- [ ] **Step 3: Commit**

```bash
git add cmd/escrow-cli/tui/run.go
git commit --no-verify -m "feat(tui): SSE reconnect with backoff and rotation-safe offline tail"
```

---

## Self-Review (completed during planning)

- **Spec coverage:** Client w/ discovery/login/fetchers/SSE (T1); model tabs+filters+messages (T2); the six views + run wiring + dispatch + `--tui` + offline fallback + no-TTY (T3); SSE reconnect + rotation-safe tail (T4). Auto-login from config + `runtime.json` discovery (T3 `buildClient`). Read-only ⇒ cookie only, no CSRF (T1).
- **Placeholder scan:** Client (T1) and model (T2) have full code; T3/T4 are run-verified UI with precise per-view/field specs and the run.go skeleton + concrete responsibilities for each helper. No "TBD"/"handle errors" hand-waves — each behavior is named.
- **Type consistency:** `Event`/`Stats`/`CVE`/`NewVuln`/`TreeEco`/`AccessEntry`/`UpstreamEntry` defined in T1 and consumed by the model messages (T2) and views (T3); `tabNames` (len 6) consistent with the `1`–`6` keys and `loadTab` switch; `streamMsg` single-field `e Event` used in T2 test + Update + T3 feed.
- **Dependency note:** `go-isatty` is pulled transitively by bubbletea; if `go vet` flags it as not a direct dep, either `go get github.com/mattn/go-isatty` or switch to `golang.org/x/term`'s `IsTerminal`. Pick whichever `go mod tidy` keeps clean.

## Phase notes
Each task builds working software: T1 a usable API client (tested), T2 a navigable model (tested), T3 the running TUI, T4 resilience. Run `go build/vet/test ./...` after each.
