# Docker Build Protection — Phase 1 (Explicit-Mode Egress Firewall) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an opt-in HTTP egress proxy to escrow (rules 2+3: host/IP allow-block + opaque tunnel, **no CA, no transparent intercept**) and an `escrow-cli docker` surface that wires `docker build` / `docker compose` to route registry traffic through escrow's mirror and everything else through the egress proxy.

**Architecture:** A new `internal/egress` package runs a standard forward proxy (`CONNECT` tunnel + absolute-URI HTTP) on its own port, gated by a host/CIDR `Policy` (default forward-everything, optional black/whitelist). It records `kind=egress` events in the existing event log. `cmd/escrow-cli/docker.go` reuses the existing per-ecosystem env derivation to assemble `--add-host` + `--build-arg` for builds and a `docker-compose` override. This is the cross-platform, CA-free first increment of the spec `docs/superpowers/specs/2026-06-03-docker-build-protection-design.md`; transparent interception (`SO_ORIGINAL_DST`) and selective-MITM + CA are deliberately **out of scope** (follow-on plan).

**Tech Stack:** Go 1.25, stdlib `net`/`net/http` (proxy + hijack tunnel), `github.com/BurntSushi/toml` (config), `github.com/rs/zerolog` (logging), `github.com/stretchr/testify` (tests). Module: `github.com/jverhoeks/escrow`.

---

## Out of scope for this plan (tracked, follow-on)

- **Transparent interception** (`SO_ORIGINAL_DST`/`IP6T_SO_ORIGINAL_DST`, REDIRECT/TPROXY) — Linux-only, the *forced* path; needs Linux verification.
- **Phase 2 selective-MITM + CA** (rule 1: decrypt registry hosts, reuse the mirror policy engine, per-ecosystem CA-trust injection).
- **Dedicated dashboard "Egress" view/filter** — egress events already surface in the existing live feed / `escrow-cli live` / TUI because they are `PackageEvent`s; a dedicated filter is polish.
- **DNS interception** — belongs with transparent mode.
- **File-based registry tools in builds** (cargo/nuget/maven/gradle/composer): they get egress coverage but not registry-env injection (no standard env var) — documented gap.

## File structure

| File | Responsibility |
|---|---|
| `internal/config/config.go` (modify) | Add `EgressProxyConfig` + `Config.EgressProxy *EgressProxyConfig` (pointer = disabled when section absent). |
| `internal/egress/policy.go` (create) | `Policy`: host (exact/suffix) + CIDR (v4/v6) allow/block, forward vs whitelist mode. Pure, no I/O. |
| `internal/egress/proxy.go` (create) | `Proxy`: forward-proxy HTTP handler (`CONNECT` tunnel + absolute-URI), policy gate, `kind=egress` events, lifecycle. |
| `internal/eventlog/log.go` (modify) | Add `KindEgress = "egress"` constant. |
| `cmd/escrow/main.go` (modify) | Construct + start the egress proxy in a goroutine when `[egress_proxy]` is enabled; stop on `rootCtx`. |
| `cmd/escrow-cli/docker.go` (create) | `escrow-cli docker <check\|build\|compose>`: derive args, assemble `docker build` argv + compose override. |
| `cmd/escrow-cli/main.go` (modify) | Dispatch `case "docker"`. |
| `docs/docker.md` (create), `docs/routing.md` (modify) | User docs. |

---

### Task 1: Config — `[egress_proxy]` section

**Files:**
- Modify: `internal/config/config.go` (add struct + field near `RescanConfig`)
- Test: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestLoad_EgressProxy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "escrow.toml")
	require.NoError(t, os.WriteFile(path, []byte(`
[egress_proxy]
enabled = true
forward_port = 7889
policy = "whitelist"
allow_hosts = ["registry.npmjs.org", ".pypi.org"]
block_cidrs = ["169.254.0.0/16"]
`), 0o600))

	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.NotNil(t, cfg.EgressProxy)
	require.NotNil(t, cfg.EgressProxy.Enabled)
	assert.True(t, *cfg.EgressProxy.Enabled)
	assert.Equal(t, 7889, cfg.EgressProxy.ForwardPort)
	assert.Equal(t, "whitelist", cfg.EgressProxy.Policy)
	assert.Equal(t, []string{"registry.npmjs.org", ".pypi.org"}, cfg.EgressProxy.AllowHosts)
	assert.Equal(t, []string{"169.254.0.0/16"}, cfg.EgressProxy.BlockCIDRs)
}

func TestLoad_EgressProxyAbsentIsNil(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "escrow.toml")
	require.NoError(t, os.WriteFile(path, []byte("[server]\nport = 7888\n"), 0o600))
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Nil(t, cfg.EgressProxy, "absent section => nil => disabled")
}
```

(If `config_test.go` is `package config_test`, ensure `filepath`, `os`, and the `config`, `require`, `assert` imports are present — match the file's existing import block.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestLoad_EgressProxy -v`
Expected: FAIL — `cfg.EgressProxy undefined (type config.Config has no field or method EgressProxy)`.

- [ ] **Step 3: Add the struct and field**

In `internal/config/config.go`, add the field to the `Config` struct (next to `Rescan`):

```go
	Rescan      *RescanConfig      `json:"rescan" toml:"rescan"`
	EgressProxy *EgressProxyConfig `json:"egress_proxy" toml:"egress_proxy"`
```

And add the type (near `RescanConfig`):

```go
// EgressProxyConfig configures the Docker-build egress proxy. A nil pointer
// (the [egress_proxy] section omitted) means disabled. Forward-proxy / rules 2+3
// only in this phase: host + CIDR allow/block, no TLS interception, no CA.
type EgressProxyConfig struct {
	Enabled     *bool    `json:"enabled" toml:"enabled"`           // nil => disabled
	ForwardPort int      `json:"forward_port" toml:"forward_port"` // 0 => 7889
	Policy      string   `json:"policy" toml:"policy"`             // "forward" (default-allow) | "whitelist" (deny-by-default)
	AllowHosts  []string `json:"allow_hosts" toml:"allow_hosts"`
	BlockHosts  []string `json:"block_hosts" toml:"block_hosts"`
	AllowCIDRs  []string `json:"allow_cidrs" toml:"allow_cidrs"`
	BlockCIDRs  []string `json:"block_cidrs" toml:"block_cidrs"`
}
```

(No change to `DefaultConfig()` — disabled by default, defaults applied at consumption in Task 4.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -run TestLoad_EgressProxy -v`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add [egress_proxy] section (Docker build protection)"
```

---

### Task 2: `internal/egress` — host/CIDR Policy

**Files:**
- Create: `internal/egress/policy.go`
- Test: `internal/egress/policy_test.go`

- [ ] **Step 1: Write the failing test**

```go
package egress

import (
	"net"
	"testing"

	"github.com/jverhoeks/escrow/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPolicy_ForwardMode(t *testing.T) {
	pol, err := NewPolicy(config.EgressProxyConfig{
		Policy:     "forward",
		BlockHosts: []string{"evil.example", ".tracker.net"},
		BlockCIDRs: []string{"169.254.0.0/16"},
	})
	require.NoError(t, err)

	cases := []struct {
		name  string
		host  string
		ip    net.IP
		allow bool
	}{
		{"unknown forwarded", "registry.npmjs.org", nil, true},
		{"exact block", "evil.example", nil, false},
		{"suffix block", "ads.tracker.net", nil, false},
		{"suffix block apex", "tracker.net", nil, false},
		{"blocked v4 cidr", "169.254.169.254", net.ParseIP("169.254.169.254"), false},
		{"allowed ip", "1.1.1.1", net.ParseIP("1.1.1.1"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.allow, pol.Check(tc.host, tc.ip).Allow)
		})
	}
}

func TestPolicy_WhitelistMode(t *testing.T) {
	pol, err := NewPolicy(config.EgressProxyConfig{
		Policy:     "whitelist",
		AllowHosts: []string{"registry.npmjs.org", ".pypi.org"},
		AllowCIDRs: []string{"2001:db8::/32"},
	})
	require.NoError(t, err)
	assert.True(t, pol.Check("registry.npmjs.org", nil).Allow)
	assert.True(t, pol.Check("files.pypi.org", nil).Allow) // suffix
	assert.False(t, pol.Check("random.example", nil).Allow)
	assert.True(t, pol.Check("x", net.ParseIP("2001:db8::1")).Allow) // v6 cidr
}

func TestNewPolicy_BadCIDR(t *testing.T) {
	_, err := NewPolicy(config.EgressProxyConfig{BlockCIDRs: []string{"not-a-cidr"}})
	require.Error(t, err)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/egress/ -run TestPolicy -v`
Expected: FAIL — `undefined: NewPolicy`.

- [ ] **Step 3: Write the implementation**

Create `internal/egress/policy.go`:

```go
// Package egress is escrow's Docker-build egress proxy: a host/IP-policy
// forward proxy (rules 2+3 of the build-protection design). No TLS interception.
package egress

import (
	"fmt"
	"net"
	"strings"

	"github.com/jverhoeks/escrow/internal/config"
)

// Decision is the outcome of a policy check.
type Decision struct {
	Allow  bool
	Reason string
}

// Policy decides whether egress to a host/IP is permitted.
type Policy struct {
	whitelist  bool // true => deny-by-default; false => forward-everything
	allowHosts []string
	blockHosts []string
	allowNets  []*net.IPNet
	blockNets  []*net.IPNet
}

// NewPolicy builds a Policy from config. CIDRs are parsed up front (fail fast).
func NewPolicy(cfg config.EgressProxyConfig) (*Policy, error) {
	p := &Policy{
		whitelist:  strings.EqualFold(cfg.Policy, "whitelist"),
		allowHosts: normHosts(cfg.AllowHosts),
		blockHosts: normHosts(cfg.BlockHosts),
	}
	var err error
	if p.allowNets, err = parseCIDRs(cfg.AllowCIDRs); err != nil {
		return nil, err
	}
	if p.blockNets, err = parseCIDRs(cfg.BlockCIDRs); err != nil {
		return nil, err
	}
	return p, nil
}

// Check decides egress for a host (always present) and an optional dst IP
// (nil in explicit mode unless the target is an IP literal).
func (p *Policy) Check(host string, ip net.IP) Decision {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if hostMatch(p.blockHosts, host) || ipMatch(p.blockNets, ip) {
		return Decision{Allow: false, Reason: "blacklisted"}
	}
	if p.whitelist {
		if hostMatch(p.allowHosts, host) || ipMatch(p.allowNets, ip) {
			return Decision{Allow: true, Reason: "whitelisted"}
		}
		return Decision{Allow: false, Reason: "not in whitelist"}
	}
	return Decision{Allow: true, Reason: "forward"}
}

func normHosts(in []string) []string {
	out := make([]string, len(in))
	for i, h := range in {
		out[i] = strings.ToLower(strings.TrimSuffix(h, "."))
	}
	return out
}

func parseCIDRs(in []string) ([]*net.IPNet, error) {
	nets := make([]*net.IPNet, 0, len(in))
	for _, c := range in {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			return nil, fmt.Errorf("egress: invalid CIDR %q: %w", c, err)
		}
		nets = append(nets, n)
	}
	return nets, nil
}

// hostMatch: exact match, or suffix match when the pattern starts with "."
// (".pypi.org" matches "files.pypi.org" and the apex "pypi.org").
func hostMatch(patterns []string, host string) bool {
	for _, p := range patterns {
		if strings.HasPrefix(p, ".") {
			if host == p[1:] || strings.HasSuffix(host, p) {
				return true
			}
		} else if host == p {
			return true
		}
	}
	return false
}

func ipMatch(nets []*net.IPNet, ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/egress/ -run TestPolicy -v && go test ./internal/egress/ -run TestNewPolicy -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/egress/policy.go internal/egress/policy_test.go
git commit -m "feat(egress): host/CIDR policy (forward + whitelist modes, v4/v6)"
```

---

### Task 3: `internal/egress` — the forward proxy + `kind=egress` events

**Files:**
- Modify: `internal/eventlog/log.go` (add `KindEgress`)
- Create: `internal/egress/proxy.go`
- Test: `internal/egress/proxy_test.go`

- [ ] **Step 1: Write the failing test**

```go
package egress

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/jverhoeks/escrow/internal/config"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func startProxy(t *testing.T, cfg config.EgressProxyConfig, evlog *eventlog.Log) string {
	t.Helper()
	pol, err := NewPolicy(cfg)
	require.NoError(t, err)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	p := New(ln.Addr().String(), pol, evlog)
	go func() { _ = p.serveListener(ln) }()
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String()
}

func proxyClient(addr string) *http.Client {
	u, _ := url.Parse("http://" + addr)
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u)}, Timeout: 5 * time.Second}
}

func TestProxy_ForwardsHTTPAndRecordsEvent(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello")
	}))
	defer upstream.Close()

	evlog := eventlog.New(10)
	events, unsub := evlog.Subscribe()
	defer unsub()

	addr := startProxy(t, config.EgressProxyConfig{Policy: "forward"}, evlog)
	resp, err := proxyClient(addr).Get(upstream.URL)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, "hello", string(body))

	select {
	case e := <-events:
		assert.Equal(t, eventlog.KindEgress, e.Kind)
		assert.Equal(t, "allow", e.Action)
	case <-time.After(2 * time.Second):
		t.Fatal("no egress event recorded")
	}
}

func TestProxy_BlocksHTTP(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "should not reach")
	}))
	defer upstream.Close()
	host := mustHost(t, upstream.URL)

	addr := startProxy(t, config.EgressProxyConfig{Policy: "forward", BlockHosts: []string{host}}, eventlog.New(10))
	resp, err := proxyClient(addr).Get(upstream.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestProxy_ConnectTunnelAllowed(t *testing.T) {
	up, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer up.Close()
	go func() {
		c, err := up.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = io.Copy(c, c) // echo
	}()

	addr := startProxy(t, config.EgressProxyConfig{Policy: "forward"}, eventlog.New(10))
	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer conn.Close()
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", up.Addr(), up.Addr())

	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	require.NoError(t, err)
	assert.Contains(t, status, "200")
	for { // consume header block up to the blank line
		line, err := br.ReadString('\n')
		require.NoError(t, err)
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	fmt.Fprint(conn, "ping")
	got := make([]byte, 4)
	_, err = io.ReadFull(br, got)
	require.NoError(t, err)
	assert.Equal(t, "ping", string(got))
}

func mustHost(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u.Hostname()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/egress/ -run TestProxy -v`
Expected: FAIL — `undefined: New` / `undefined: eventlog.KindEgress`.

- [ ] **Step 3a: Add the event kind**

In `internal/eventlog/log.go`, extend the kind constants:

```go
const (
	KindScanned    = "scanned"
	KindDownloaded = "downloaded"
	KindRescan     = "rescan"
	KindEgress     = "egress"
)
```

- [ ] **Step 3b: Write the proxy**

Create `internal/egress/proxy.go`:

```go
package egress

import (
	"context"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/jverhoeks/escrow/internal/eventlog"
)

// Proxy is a forward proxy gated by a host/IP Policy. It tunnels CONNECT
// (HTTPS) opaquely and forwards absolute-URI HTTP. No TLS interception.
type Proxy struct {
	addr      string
	policy    *Policy
	evlog     *eventlog.Log // may be nil
	transport *http.Transport
	srv       *http.Server
}

// New builds a Proxy bound to addr (host:port).
func New(addr string, policy *Policy, evlog *eventlog.Log) *Proxy {
	return &Proxy{
		addr:      addr,
		policy:    policy,
		evlog:     evlog,
		transport: &http.Transport{Proxy: nil},
	}
}

// Serve listens on the configured address and serves until ctx is cancelled.
func (p *Proxy) Serve(ctx context.Context) error {
	ln, err := net.Listen("tcp", p.addr)
	if err != nil {
		return err
	}
	go func() { <-ctx.Done(); _ = ln.Close() }()
	return p.serveListener(ln)
}

func (p *Proxy) serveListener(ln net.Listener) error {
	p.srv = &http.Server{Handler: http.HandlerFunc(p.handle)}
	err := p.srv.Serve(ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (p *Proxy) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
		return
	}
	p.handleHTTP(w, r)
}

func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	host := hostOnly(r.Host)
	if d := p.policy.Check(host, net.ParseIP(host)); !d.Allow {
		p.record(host, "block", d.Reason)
		http.Error(w, "blocked by escrow egress policy", http.StatusForbidden)
		return
	}
	upstream, err := net.DialTimeout("tcp", r.Host, 10*time.Second)
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
	p.record(host, "allow", "tunnel")
	_, _ = io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n")
	done := make(chan struct{})
	go func() { _, _ = io.Copy(upstream, client); close(done) }()
	_, _ = io.Copy(client, upstream)
	<-done
}

func (p *Proxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	host := r.URL.Hostname()
	if d := p.policy.Check(host, net.ParseIP(host)); !d.Allow {
		p.record(host, "block", d.Reason)
		http.Error(w, "blocked by escrow egress policy", http.StatusForbidden)
		return
	}
	r.RequestURI = ""
	r.Header.Del("Proxy-Connection")
	resp, err := p.transport.RoundTrip(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	p.record(host, "allow", "forward")
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (p *Proxy) record(host, action, reason string) {
	if p.evlog == nil {
		return
	}
	p.evlog.Record(eventlog.PackageEvent{
		Ecosystem: "egress",
		Package:   host,
		Action:    action,
		Signal:    "egress",
		Reason:    reason,
		Kind:      eventlog.KindEgress,
	})
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/egress/ ./internal/eventlog/ -v`
Expected: PASS (proxy + policy + existing eventlog tests).

- [ ] **Step 5: Commit**

```bash
git add internal/egress/proxy.go internal/egress/proxy_test.go internal/eventlog/log.go
git commit -m "feat(egress): forward proxy (CONNECT tunnel + HTTP), kind=egress events"
```

---

### Task 4: Wire the egress proxy into `cmd/escrow/main.go`

**Files:**
- Modify: `cmd/escrow/main.go`

This is integration wiring; verification is a build + a manual smoke test (no unit test — `main` is not unit-tested in this repo).

- [ ] **Step 1: Add the import**

In the import block of `cmd/escrow/main.go`, add:

```go
	"github.com/jverhoeks/escrow/internal/egress"
```

- [ ] **Step 2: Add the wiring**

Insert this block **after** `evLog` and `rootCtx, rootCancel` are defined (both exist by ~line 258) and **before** the signal-handler / `srv.Start()` block (~line 565). A safe spot is right after the re-scanner wiring (~line 361, after `scanner.Start(rootCtx)`):

```go
	// Egress proxy (Docker build protection, Phase 1): optional second listener.
	// nil section => disabled. Forward proxy only; no TLS interception.
	if ep := cfg.EgressProxy; ep != nil && (ep.Enabled == nil || *ep.Enabled) {
		pol, err := egress.NewPolicy(*ep)
		if err != nil {
			log.Fatal().Err(err).Msg("egress proxy: invalid policy")
		}
		port := ep.ForwardPort
		if port == 0 {
			port = 7889
		}
		eproxy := egress.New(fmt.Sprintf("%s:%d", cfg.Server.Host, port), pol, evLog)
		go func() {
			log.Info().Int("port", port).Str("policy", ep.Policy).Msg("egress proxy listening")
			if err := eproxy.Serve(rootCtx); err != nil {
				log.Error().Err(err).Msg("egress proxy stopped")
			}
		}()
	}
```

(`fmt` and the zerolog `log` package are already imported in `main.go`. The proxy stops when `rootCancel()` runs in the existing signal handler — no extra shutdown wiring needed.)

- [ ] **Step 3: Build and smoke-test**

Run:
```bash
go build ./cmd/escrow && go build ./cmd/escrow-cli
```
Expected: both succeed.

Manual smoke (document the result in the commit body if anything deviates):
```bash
cat > /tmp/escrow-egress.toml <<'TOML'
[server]
host = "127.0.0.1"
port = 7888
[egress_proxy]
enabled = true
forward_port = 7889
policy = "forward"
block_hosts = ["blocked.example"]
TOML
./escrow --config=/tmp/escrow-egress.toml &
sleep 1
# forwarded (rule 3):
curl -s -o /dev/null -w "allowed: %{http_code}\n" -x http://127.0.0.1:7889 http://example.com
# blocked (rule 2):
curl -s -o /dev/null -w "blocked: %{http_code}\n" -x http://127.0.0.1:7889 http://blocked.example
# CONNECT (https) forwarded:
curl -s -o /dev/null -w "https: %{http_code}\n" -x http://127.0.0.1:7889 https://example.com
kill %1
```
Expected: `allowed: 200`, `blocked: 403`, `https: 200`. (Do **not** release — local verification only.)

- [ ] **Step 4: Commit**

```bash
git add cmd/escrow/main.go
git commit -m "feat(cmd): start the egress proxy when [egress_proxy] is enabled"
```

---

### Task 5: `escrow-cli docker` dispatch + arg derivation + `check`

**Files:**
- Create: `cmd/escrow-cli/docker.go`
- Modify: `cmd/escrow-cli/main.go` (add `case "docker"`)
- Test: `cmd/escrow-cli/docker_test.go`

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDeriveDockerArgs(t *testing.T) {
	got := deriveDockerArgs([]string{"npm", "pypi", "go"}, "host.docker.internal", 7889)

	assert.Equal(t, "host.docker.internal:host-gateway", got.AddHost)
	assert.Equal(t, "http://host.docker.internal:7889", got.BuildArgs["HTTP_PROXY"])
	assert.Equal(t, "http://host.docker.internal:7889", got.BuildArgs["HTTPS_PROXY"])
	assert.Equal(t, "http://host.docker.internal:7889", got.BuildArgs["http_proxy"])
	assert.Equal(t, "host.docker.internal,localhost,127.0.0.1", got.BuildArgs["NO_PROXY"])
	// registry env reused from buildEnvVars (npm/pypi/go):
	assert.Equal(t, "http://host.docker.internal:7888/", got.BuildArgs["NPM_CONFIG_REGISTRY"])
	assert.Equal(t, "http://host.docker.internal:7888/pypi/simple/", got.BuildArgs["PIP_INDEX_URL"])
	assert.Contains(t, got.BuildArgs["GOPROXY"], "/go,off")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/escrow-cli/ -run TestDeriveDockerArgs -v`
Expected: FAIL — `undefined: deriveDockerArgs`.

- [ ] **Step 3: Write the dispatch + derivation + check**

Create `cmd/escrow-cli/docker.go`:

```go
package main

import (
	"fmt"
	"net/http"
	"os"
	"sort"
	"time"
)

// dockerProxyArgs is the escrow wiring injected into a docker build.
type dockerProxyArgs struct {
	AddHost   string            // host.docker.internal:host-gateway
	BuildArgs map[string]string // proxy env + registry env
}

// deriveDockerArgs assembles the proxy/registry build-args for the given
// ecosystems. proxyHost is the build-reachable address of escrow (default
// host.docker.internal); egressPort is escrow's egress-proxy port.
func deriveDockerArgs(ecosystems []string, proxyHost string, egressPort int) dockerProxyArgs {
	mirrorBase := fmt.Sprintf("http://%s:7888", proxyHost)
	proxyURL := fmt.Sprintf("http://%s:%d", proxyHost, egressPort)
	noProxy := proxyHost + ",localhost,127.0.0.1"

	ba := map[string]string{
		"HTTP_PROXY":  proxyURL,
		"HTTPS_PROXY": proxyURL,
		"http_proxy":  proxyURL,
		"https_proxy": proxyURL,
		"NO_PROXY":    noProxy,
		"no_proxy":    noProxy,
	}
	// Reuse the canonical per-ecosystem env map (npm/pypi/go/yarn) from env.go.
	for _, e := range buildEnvVars(ecosystems, mirrorBase) {
		ba[e.key] = e.value
	}
	return dockerProxyArgs{AddHost: proxyHost + ":host-gateway", BuildArgs: ba}
}

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func runDocker(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: escrow-cli docker <check|build|compose> ...")
		os.Exit(2)
	}
	switch args[0] {
	case "check":
		runDockerCheck(args[1:])
	case "build":
		runDockerBuild(args[1:])
	case "compose":
		runDockerCompose(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown docker subcommand %q\n", args[0])
		os.Exit(2)
	}
}

func runDockerCheck(args []string) {
	ecos, proxyHost, egressPort, _ := parseDockerFlags(args)
	da := deriveDockerArgs(ecos, proxyHost, egressPort)
	fmt.Printf("--add-host %s\n", da.AddHost)
	for _, k := range sortedKeys(da.BuildArgs) {
		fmt.Printf("--build-arg %s=%s\n", k, da.BuildArgs[k])
	}
	// Reachability of escrow's mirror port (host loopback as a proxy for build-reachability).
	client := &http.Client{Timeout: 2 * time.Second}
	if resp, err := client.Get("http://127.0.0.1:7888/healthz"); err == nil {
		_ = resp.Body.Close()
		fmt.Println("escrow mirror: reachable on 127.0.0.1:7888")
	} else {
		fmt.Println("escrow mirror: NOT reachable on 127.0.0.1:7888 — start escrow first")
	}
}
```

Add a flag parser (used by check/build/compose) to the same file:

```go
import "flag"

// parseDockerFlags extracts escrow flags and returns the remaining (user) args.
func parseDockerFlags(args []string) (ecosystems []string, proxyHost string, egressPort int, rest []string) {
	fs := flag.NewFlagSet("docker", flag.ExitOnError)
	ecoStr := fs.String("ecosystems", "npm,pypi,go", "comma-separated ecosystems")
	host := fs.String("proxy-host", "host.docker.internal", "build-reachable escrow host")
	port := fs.Int("egress-port", 7889, "escrow egress-proxy port")
	_ = fs.Parse(args)
	return parseEcosystems(*ecoStr), *host, *port, fs.Args()
}
```

(Move the `import "flag"` into the file's single import block — shown separately only for clarity. `parseEcosystems` already exists in `pf.go`; `buildEnvVars` in `env.go`.)

In `cmd/escrow-cli/main.go`, add to the top-level switch (next to `case "tui":`):

```go
	case "docker":
		runDocker(os.Args[2:])
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/escrow-cli/ -run TestDeriveDockerArgs -v && go build ./cmd/escrow-cli`
Expected: PASS + build OK.

- [ ] **Step 5: Commit**

```bash
git add cmd/escrow-cli/docker.go cmd/escrow-cli/docker_test.go cmd/escrow-cli/main.go
git commit -m "feat(cli): escrow-cli docker dispatch + arg derivation + check"
```

---

### Task 6: `escrow-cli docker build`

**Files:**
- Modify: `cmd/escrow-cli/docker.go` (add `dockerBuildArgv` + `runDockerBuild`)
- Test: `cmd/escrow-cli/docker_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestDockerBuildArgv(t *testing.T) {
	da := deriveDockerArgs([]string{"npm"}, "host.docker.internal", 7889)
	argv := dockerBuildArgv(da, []string{"-t", "myimg", "."})

	assert.Equal(t, "build", argv[0])
	assertHasPair(t, argv, "--add-host", "host.docker.internal:host-gateway")
	assertHasPair(t, argv, "--build-arg", "NPM_CONFIG_REGISTRY=http://host.docker.internal:7888/")
	assertHasPair(t, argv, "--build-arg", "HTTP_PROXY=http://host.docker.internal:7889")
	// user args preserved, in order, at the end:
	assert.Equal(t, []string{"-t", "myimg", "."}, argv[len(argv)-3:])
}

func assertHasPair(t *testing.T, argv []string, flag, val string) {
	t.Helper()
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == val {
			return
		}
	}
	t.Fatalf("expected %s %q in argv: %v", flag, val, argv)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/escrow-cli/ -run TestDockerBuildArgv -v`
Expected: FAIL — `undefined: dockerBuildArgv`.

- [ ] **Step 3: Implement**

Add to `cmd/escrow-cli/docker.go`:

```go
import "os/exec"

// dockerBuildArgv assembles the full `docker build ...` argument vector
// (escrow wiring first, then the user's args). Build-args are emitted in a
// stable (sorted) order for determinism.
func dockerBuildArgv(da dockerProxyArgs, userArgs []string) []string {
	argv := []string{"build", "--add-host", da.AddHost}
	for _, k := range sortedKeys(da.BuildArgs) {
		argv = append(argv, "--build-arg", k+"="+da.BuildArgs[k])
	}
	return append(argv, userArgs...)
}

func runDockerBuild(args []string) {
	ecos, proxyHost, egressPort, userArgs := parseDockerFlags(args)
	da := deriveDockerArgs(ecos, proxyHost, egressPort)
	argv := dockerBuildArgv(da, userArgs)

	cmd := exec.Command("docker", argv...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "docker build failed: %v\n", err)
		os.Exit(1)
	}
}
```

(Move `import "os/exec"` into the file's import block.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/escrow-cli/ -run TestDockerBuildArgv -v && go build ./cmd/escrow-cli`
Expected: PASS + build OK.

- [ ] **Step 5: Commit**

```bash
git add cmd/escrow-cli/docker.go cmd/escrow-cli/docker_test.go
git commit -m "feat(cli): escrow-cli docker build (inject add-host + proxy/registry build-args)"
```

---

### Task 7: `escrow-cli docker compose init` (override generator)

**Files:**
- Modify: `cmd/escrow-cli/docker.go` (add `composeOverride` + `runDockerCompose`)
- Test: `cmd/escrow-cli/docker_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestComposeOverride(t *testing.T) {
	da := deriveDockerArgs([]string{"npm"}, "host.docker.internal", 7889)
	out := composeOverride([]string{"web", "worker"}, da)

	assert.Contains(t, out, "services:\n")
	assert.Contains(t, out, "  web:\n")
	assert.Contains(t, out, "  worker:\n")
	assert.Contains(t, out, `HTTP_PROXY: "http://host.docker.internal:7889"`)
	assert.Contains(t, out, `NPM_CONFIG_REGISTRY: "http://host.docker.internal:7888/"`)
	assert.Contains(t, out, `- "host.docker.internal:host-gateway"`)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/escrow-cli/ -run TestComposeOverride -v`
Expected: FAIL — `undefined: composeOverride`.

- [ ] **Step 3: Implement**

Add to `cmd/escrow-cli/docker.go`:

```go
import "strings"

// composeOverride renders a docker-compose override that routes each named
// service's build through escrow (advisory HTTP_PROXY + registry env + host-gateway).
func composeOverride(services []string, da dockerProxyArgs) string {
	var b strings.Builder
	b.WriteString("# Generated by `escrow-cli docker compose init`. Include with:\n")
	b.WriteString("#   docker compose -f docker-compose.yml -f docker-compose.escrow.yml build\n")
	b.WriteString("services:\n")
	for _, s := range services {
		fmt.Fprintf(&b, "  %s:\n    build:\n      args:\n", s)
		for _, k := range sortedKeys(da.BuildArgs) {
			fmt.Fprintf(&b, "        %s: %q\n", k, da.BuildArgs[k])
		}
		b.WriteString("      extra_hosts:\n")
		fmt.Fprintf(&b, "        - %q\n", da.AddHost)
	}
	return b.String()
}

func runDockerCompose(args []string) {
	if len(args) == 0 || args[0] != "init" {
		fmt.Fprintln(os.Stderr, "usage: escrow-cli docker compose init --service NAME [--service NAME] [flags]")
		os.Exit(2)
	}
	rest := args[1:]
	var services []string
	// pull out repeated --service flags before the shared flag parse
	var passthrough []string
	for i := 0; i < len(rest); i++ {
		if rest[i] == "--service" && i+1 < len(rest) {
			services = append(services, rest[i+1])
			i++
			continue
		}
		passthrough = append(passthrough, rest[i])
	}
	if len(services) == 0 {
		fmt.Fprintln(os.Stderr, "at least one --service NAME is required")
		os.Exit(2)
	}
	ecos, proxyHost, egressPort, _ := parseDockerFlags(passthrough)
	da := deriveDockerArgs(ecos, proxyHost, egressPort)

	const out = "docker-compose.escrow.yml"
	if _, err := os.Stat(out); err == nil {
		if err := os.Rename(out, out+".escrow-backup"); err != nil {
			fmt.Fprintf(os.Stderr, "backup %s: %v\n", out, err)
			os.Exit(1)
		}
	}
	if err := os.WriteFile(out, []byte(composeOverride(services, da)), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", out, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s for services: %s\n", out, strings.Join(services, ", "))
}
```

(Consolidate the `strings` import with the others.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/escrow-cli/ -run TestComposeOverride -v && go build ./cmd/escrow-cli`
Expected: PASS + build OK.

- [ ] **Step 5: Commit**

```bash
git add cmd/escrow-cli/docker.go cmd/escrow-cli/docker_test.go
git commit -m "feat(cli): escrow-cli docker compose init (override generator)"
```

---

### Task 8: Docs

**Files:**
- Create: `docs/docker.md`
- Modify: `docs/routing.md` (add a "Method 5" row + short section)

- [ ] **Step 1: Write `docs/docker.md`**

Create `docs/docker.md` with these sections (real content, not placeholders):

1. **Two lanes** — registries via the path-mirror (full package policy, fail-closed); everything else via the egress proxy (host/IP allow-block, *advisory* unless escrow is the gateway). Link to the spec.
2. **Quick start** —
   ````markdown
   # configure escrow.toml
   [egress_proxy]
   enabled = true
   forward_port = 7889
   policy = "forward"            # or "whitelist" for a hard egress boundary
   block_hosts = ["telemetry.example"]

   # plain build
   escrow-cli docker build --ecosystems npm,pypi,go -- -t myimg .

   # compose
   escrow-cli docker compose init --service web --ecosystems npm
   docker compose -f docker-compose.yml -f docker-compose.escrow.yml build
   ````
3. **What's gated** — a table: npm/pypi/go get full package policy via the mirror; all hosts get name/IP egress control; cargo/nuget/maven/composer get egress but not registry-env (documented gap); HTTPS is name/IP-level (no package policy without the future MITM phase).
4. **Honesty box** — advisory `HTTP_PROXY` inside a plain `RUN` is bypassable; forced only when escrow is the network gateway (transparent mode — future). No CA yet.

- [ ] **Step 2: Add a row to `docs/routing.md`**

In the methods table near the top of `docs/routing.md`, add:

```markdown
| **5** | [Docker / containers](#method-5--docker--containers) | Build & container egress via the escrow egress proxy | No | All |
```

And a short `### Method 5 — Docker / containers` section pointing at `docs/docker.md` and summarizing the two lanes + the advisory-vs-forced caveat.

- [ ] **Step 3: Commit**

```bash
git add docs/docker.md docs/routing.md
git commit -m "docs: Docker build protection (egress proxy) guide + routing Method 5"
```

---

## Self-review

**Spec coverage (against `2026-06-03-docker-build-protection-design.md`):**
- Rule 2 (blacklist reject) + Rule 3 (forward/tunnel, no cache) → Tasks 2–3 ✓
- Host + IP/CIDR, v4 + v6, forward + whitelist modes → Task 2 ✓
- `kind=egress` events → Task 3 ✓
- `[egress_proxy]` config, separate port, off-by-default → Tasks 1, 4 ✓
- Explicit forward-proxy (one port, HTTP + CONNECT) → Task 3 ✓
- `escrow-cli docker build` / `compose` / `check`, reusing config derivation, `--add-host` + build-args → Tasks 5–7 ✓
- Docs incl. honesty framing → Task 8 ✓
- **Deferred (out of scope, noted):** transparent intercept + DNS, Rule 1 selective-MITM + CA, dedicated dashboard egress view. ✓ (explicitly listed)

**Placeholder scan:** No TBD/TODO; every code step has complete code; commands have expected output. ✓

**Type consistency:** `deriveDockerArgs`/`dockerProxyArgs`/`dockerBuildArgv`/`composeOverride`/`sortedKeys`/`parseDockerFlags` consistent across Tasks 5–7; `NewPolicy`/`Policy.Check`/`Decision` consistent across Tasks 2–4; `egress.New`/`Serve`/`serveListener` consistent across Tasks 3–4; `eventlog.KindEgress` + `PackageEvent` fields match the real struct. ✓

---

## Execution handoff

**Plan complete and saved to `docs/superpowers/plans/2026-06-03-docker-build-protection-phase1.md`. Two execution options:**

**1. Subagent-Driven (recommended)** — dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — execute tasks in this session using executing-plans, batch execution with checkpoints.

**Which approach?**
