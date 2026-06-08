# 📸 Dashboard & Terminal UI

[← Back to README](../README.md) · Related: [Policy & scanning](policy.md) · [escrow-cli reference](escrow-cli.md)

escrow ships a real-time operator console in **two forms** — a web dashboard and a full
terminal UI — both backed by the same local API. Status is coded with **icons *and* color**
(never color alone), so it stays readable for color-blind operators, and both light and dark
themes follow your OS.

Open the web dashboard at `http://localhost:7888/dashboard`. Credentials are printed on first
boot and stored in `$(brew --prefix)/var/log/escrow.log`. An **Activity** filter
(Downloaded / All) and an **Ecosystem** filter are shared across every view.

---

## The web dashboard

### Live Feed

Every package event as it happens, with running blocked/warned/allowed counts and the
top-blocked packages. Each row shows the classification (`scanned` / `downloaded` / `blocked`)
and a one-click **Approve** / **Block** action.

![Live feed with blocked packages](images/dashboard-live.png)

### CVEs

Every version blocked by a vulnerability, grouped by advisory with severity and a link to the
OSV / GitHub advisory.

![CVE view](images/dashboard-cves.png)

### Package Tree

Ecosystem → namespace/package → version, collapsed by default. Each version shows its status,
`scanned` / `downloaded` marker, hit & download counts, size, and a Block/Approve button.

![Package tree](images/dashboard-tree.png)

### Newly Vulnerable

Packages that gained a CVE *after* you downloaded them, surfaced by the
[continuous re-scanner](policy.md#-continuous-cve-re-scan) with how many times each was pulled —
so you can triage real exposure first.

![Newly Vulnerable](images/dashboard-newly-vulnerable.png)

### Settings

View and edit the whole configuration from the browser (everything except the dashboard
password, which is read-only and never writable via the API), with a **Validate** button and a
live **hot-reload** — see [Settings & hot-reload](policy.md#-settings--hot-reload).

The Settings page is grouped into four sub-tabs:

| Sub-tab | Sections |
|---|---|
| **General** | `server`, `storage`, `dashboard` |
| **Policy** | `policy`, `allow`, `block`, `rescan` |
| **Egress** | `egress_proxy` |
| **Advanced** | `alerts`, `cireport`, `cache`, and any unrecognised sections |

![Settings page](images/dashboard-settings.png)

### Light & dark

Both themes follow your OS preference and use the same color-blind-safe status coding.

| Light | Dark |
|:---:|:---:|
| ![light](images/dashboard-live.png) | ![dark](images/dashboard-live-dark.png) |

### More views

Under **More ▾**: **Analytics** (24-hour stacked-column trends per ecosystem), **Access Logs**
(clients → escrow), **Upstream** (escrow → registries, i.e. cache misses), and **Egress**
(proxy decisions — see below).

| Analytics | Access logs | Upstream fetches |
|:---:|:---:|:---:|
| ![Analytics — 24h trends](images/dashboard-analytics.png) | ![Access logs](images/dashboard-access-logs.png) | ![Upstream fetches](images/dashboard-upstream.png) |

### Egress

The **Egress** view (under **More ▾**) surfaces all decisions made by the egress proxy — the
firewall that gates arbitrary outbound traffic from `docker build` and similar container
workloads. It is a separate log from the package event feed and the access/upstream logs.

**Summary cards** at the top show, for the current window:

| Card | Meaning |
|---|---|
| Total | All decisions (allow + block) in the log window |
| Allowed | Connections permitted |
| Blocked | Connections rejected |
| Distinct hosts | Number of unique destination hostnames |
| Bytes | Aggregate bytes proxied on the allow path (updated at connection close; not per-event) |

**Top hosts** tables list the ten most-seen allowed and blocked destinations by hit count.

**Requests over time** — a stacked allow-vs-block chart, defaulting to the last 24 hours in
hourly buckets.

**Live egress log** — a real-time SSE feed of proxy decisions as they occur. Events are recorded
**at connection open**, so the live view reflects long-lived connections immediately, before the
connection closes and bytes are counted. An **all / allow / block** filter narrows the stream.

> **Note:** the Egress view requires the `[egress_proxy]` section to be present and enabled in
> `escrow.toml` (see [Docker & `docker build` protection](docker.md#configure-the-egress-proxy-escrowtoml)).
> With no egress proxy configured, the view loads but all counts are zero.

#### Endpoints

| Endpoint | Description |
|---|---|
| `GET /api/egresslog?n=<N>&action=<allow\|block>` | Returns the most-recent *N* egress events (max 5000; default 500), optionally filtered by action. |
| `GET /api/egress/stream` | SSE stream — pushes each new egress event as a JSON `data:` line. |
| `GET /api/egress/stats/timeseries?window=<duration>&bucket=<duration>` | Returns the full `Stats` object (summary cards + top-host lists + time-series buckets). Defaults: `window=24h`, `bucket=1h`. |

#### Prometheus metrics

Two egress counters are exported at `/metrics` alongside the existing package-proxy metrics:

| Metric | Labels | Meaning |
|---|---|---|
| `escrow_egress_requests_total` | `action` (`allow` or `block`) | Counter — incremented once per proxy decision, at connection open. |
| `escrow_egress_bytes_total` | — | Counter — bytes proxied on the allow path (incremented at connection close; aggregate, not per-connection). |

#### Configuration

`egress_log_path` is a top-level (not nested) key in `escrow.toml`, mirroring `eventlog_path`:

```toml
egress_log_path = "/var/log/escrow-egress.jsonl"   # optional; empty = in-memory ring only
```

When set, events are appended as JSONL and re-loaded on restart (up to the ring capacity).
When empty, the log is held in memory only. The ring capacity is fixed at **5000** events —
the same default as the package event log. The egress proxy itself (`enabled`, `policy`,
`block_hosts`, `allow_hosts`, …) is configured under `[egress_proxy]`; see
[Docker & `docker build` protection](docker.md#configure-the-egress-proxy-escrowtoml).

### Approving & blocking

**Approve a blocked package:** click **Approve** on any blocked event (or version in the tree).
Added to `escrow-allowlist.json` immediately — no restart. **Block manually:** the **Block**
button, or `POST /dashboard/api/block`. All allow/block actions are recorded in the live feed
with the operator's username. See [Allowlist and Blocklist](policy.md#-allowlist-and-blocklist).

---

## Terminal UI — `escrow-cli tui`

Prefer the terminal? `escrow-cli tui` (alias `escrow-cli --tui`) is an interactive,
keyboard-driven dashboard that mirrors the web views over the local API, with an offline
event-log fallback.

```bash
escrow-cli tui
```

It auto-discovers the running proxy and logs in from your `escrow.toml`; pass
`--url/--user/--password` to point at a remote instance. Keys: **Tab** / **1–7** switch views ·
**↑↓** move · **enter** expand (Packages) · **e** ecosystem · **a** activity · **r** refresh ·
**q** quit. Views: Live (+stats), CVEs, Newly Vulnerable, Packages, Access, Upstream, **Egress**
(key `7` — live egress log, polled from `/api/egresslog`).

| Live feed (allow / block, live) | CVEs (by advisory) | Packages (collapsible tree) |
|:---:|:---:|:---:|
| ![TUI live feed](images/cli-tui-live.png) | ![TUI CVEs](images/cli-tui-cves.png) | ![TUI package tree](images/cli-tui-packages.png) |

`escrow-cli live [--eco npm] [--activity downloaded|scanned|blocked]` tails the event log as
plain colorized lines — handy for piping or a side pane.

For the full command reference, see [escrow-cli.md](escrow-cli.md).
