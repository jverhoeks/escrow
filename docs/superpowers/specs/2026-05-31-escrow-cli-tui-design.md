# `escrow-cli tui` — Terminal Dashboard — Design

**Date:** 2026-05-31
**Status:** Approved (pending spec review)

## Problem

The escrow dashboard is web-only. Operators living in a terminal (over SSH, in tmux,
during an incident) have `escrow-cli live` for a raw event tail but no rich, navigable view
of stats, CVEs, the package tree, or the access/upstream logs. This adds an interactive
terminal UI that mirrors the web dashboard's most useful views.

## Goals & non-goals

- `escrow-cli tui` — an interactive TUI with tab navigation across the security-relevant
  views, talking to the running proxy's API, with a no-credentials offline fallback.
- **Non-goals (v1):** Analytics 24h charts (awkward/low-value in a terminal), the Settings
  editor (edit via web or `escrow.toml`), and write actions beyond what already exists
  (allow/block from the TUI is a possible fast-follow, not v1).

## Key decisions (from brainstorming)

| Decision | Choice |
|---|---|
| Data source | **Hybrid, API-first**: authenticated dashboard API for the rich views; event-log file-tail as the offline fallback for the Live feed |
| Auth | **Auto-login from `escrow.toml`** (`dashboard.username`/`password`) against the `runtime.json`-discovered port; `--url`/`--user`/`--password` override for a remote instance |
| Library | **Bubble Tea + Lipgloss + Bubbles** (charmbracelet) — modern styling for CVD-safe badges + a widget set (table/viewport) |
| Views (v1) | Live (+ stats header), CVEs, Newly Vulnerable, Packages (tree), Access log, Upstream |
| Live feed | **SSE** (`/api/stream`) with the session cookie; auto-reconnect |

## Architecture

### Components

- **`cmd/escrow-cli/tui/`** — the TUI package (kept out of `package main` so it's importable
  and testable):
  - `client.go` — `Client`: discovery (`config.ReadRuntime` for the port; config discovery for
    creds), `Login()` (POST `/dashboard/login`, capture the session cookie), and typed
    fetchers: `Stats()`, `Events(n)`, `CVEs()`, `NewlyVulnerable()`, `PackagesTree()`,
    `AccessLog(n)`, `UpstreamLog(n)`, and `Stream(ctx) <-chan Event` (SSE). Reuses the JSON
    shapes the dashboard already returns.
  - `model.go` — the Bubble Tea `Model`: active tab, per-view state, ecosystem + activity
    filters, online/offline mode, error/status line. `Update` handles key messages (tab nav,
    filter cycling, refresh, quit) and data messages (fetch results, SSE events). Pure update
    logic is unit-testable.
  - `views.go` — Lipgloss rendering per tab (stats header, feed rows, tables) with shared
    badge/status styles (✓ allowed / ⚠ warned / ✕ blocked + label; eco badges).
  - `run.go` — `Run(opts Options) error`: build the client, attempt login, start the program;
    on connection/auth failure, enter offline mode (file-tail) with a banner.
- **`cmd/escrow-cli/tui.go`** — thin `runTUI(args)` that parses flags (`--url`, `--user`,
  `--password`, `--path`) and calls `tui.Run`.
- **`cmd/escrow-cli/main.go`** — dispatch `case "tui"`; also accept a top-level `--tui` alias.

### Discovery & auth flow

1. Resolve base URL: `--url` if given, else `http://127.0.0.1:<port>` from `config.ReadRuntime()`.
2. Resolve creds: `--user`/`--password` if given, else load a discovered `escrow.toml`
   (`$ESCROW_CONFIG`, `/opt/homebrew/etc/escrow/escrow.toml`, `/usr/local/etc/escrow/escrow.toml`,
   `./escrow.toml`, `~/.config/escrow/escrow.toml`) and read `dashboard.username`/`password`
   and `dashboard.path`.
3. `Login()` posts the form and stores the session cookie (an `http.CookieJar`). All API
   calls reuse it.
4. On any failure (no runtime file, connection refused, bad creds), `Run` switches to
   **offline mode**: the Live tab tails the event-log JSONL (path from `runtime.json` or
   `--path`); other tabs show "API unavailable — start escrow or pass --url/--user/--password".

### Data flow

- Tab entry and `r` dispatch a `tea.Cmd` that fetches the tab's endpoint; results arrive as a
  message that updates that view's model.
- The Live tab subscribes to SSE on start (`Client.Stream`), pushing each event as a message;
  the model prepends it (bounded ring) and updates the stats header counts.
- Ecosystem and activity filters are applied client-side to the in-memory data for Live, and
  passed as query params where the endpoint supports them (`?eco=`, timeseries `kind` n/a here).

### Interaction

`Tab`/`Shift-Tab` or `1`–`6` switch tabs · `↑/↓`, `PgUp/PgDn`, `g/G` scroll · `e` cycles the
ecosystem filter (all → npm → pypi → …) · `a` cycles activity (all → downloaded → scanned →
blocked) · `r` refresh current tab · `?` toggles a help overlay · `q`/`Ctrl-C` quit.

## Error handling / edge cases

- **Server down / refused:** offline mode, Live-only via file-tail, banner; `r` retries the API.
- **Auth failure:** banner with the reason and a hint; no crash.
- **SSE disconnect:** show "reconnecting…", retry with backoff (mirrors the web client).
- **No TTY (piped/CI):** detect a non-interactive terminal and exit with a message suggesting
  `escrow-cli live` instead (Bubble Tea needs a TTY).
- **Narrow terminals:** Lipgloss truncates columns; tables degrade gracefully.

## Testing

- `client_test.go` — against an `httptest` server: login captures the cookie and authenticated
  requests carry it; each fetcher parses its endpoint's JSON into the typed struct; a 401/redirect
  is surfaced as an auth error.
- `model_test.go` — pure update logic: tab navigation wraps; `e`/`a` cycle the filters through
  their sets; an SSE event message prepends and bumps the right stat; offline-mode selection when
  the client is nil.
- Rendering and live behavior verified by running `escrow-cli tui` against a local escrow.

## Open risks

- **New dependencies** (charmbracelet stack) in a supply-chain tool — accepted: reputable, MIT,
  widely used, and consistent with the existing bundled deps (chi/aws/prometheus).
- **CLI binary size** grows with the TUI deps — acceptable for a developer CLI.
- **API shape coupling** — the TUI consumes the dashboard's JSON; if those shapes change, the
  client structs must track them. Mitigated by typed fetchers in one file.
- **Auth over the wire** — creds are read locally and posted to localhost by default; for a
  remote `--url`, the user is responsible for using TLS.

## Build sequencing (one spec, phased)

1. `tui.Client` — discovery, login, typed fetchers (no UI) + tests.
2. Bubble Tea shell — model/update/nav, the Live tab + stats header, `run.go`, dispatch + `--tui`.
3. The remaining API views — CVEs, Newly Vulnerable, Packages tree, Access, Upstream.
4. SSE live stream + offline file-tail fallback + reconnect/help/no-TTY handling.
