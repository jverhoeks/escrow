# Settings Page + Config Hot-Reload + CLI Live View — Design

**Date:** 2026-05-31
**Status:** Approved (pending spec review)

## Problem

Tuning escrow today means hand-editing `escrow.toml` and restarting. There is no way to
view/change configuration from the dashboard, no way to apply a config change without a
full restart, and no terminal-native way to watch package activity. This adds:

1. A **settings page** to view/edit the config and write it back to `escrow.toml`.
2. **Config hot-reload** — apply the live-reloadable parts of a changed config without a
   restart, exposed via the dashboard, an HTTP endpoint, `SIGHUP`, and the CLI.
3. A **CLI live view** of package events with ecosystem / activity filters.

## Goals & non-goals

- Edit all config **except `dashboard.password`** (shown read-only; server-enforced immutable).
- Hot-reload the **policy gates, `[rescan]`, and alerts webhook URL**; everything else is
  reported as restart-required (no silent no-op, no self-restart).
- CLI `reload` and `live` subcommands.
- **Non-goals:** RBAC/multi-user (still single dashboard user); live-swapping the listen
  socket, storage backend, mounted ecosystems, or the auth secret/password; Windows `SIGHUP`.

## Key decisions (from brainstorming)

| Decision | Choice |
|---|---|
| Editable scope | All config **except `dashboard.password`** (read-only, masked + reveal; server preserves the on-disk value on save) |
| Write mechanism | Re-encode the full `Config` to `escrow.toml` via the BurntSushi encoder, after backing up to `escrow.toml.bak`. (Re-encoding drops comments — backup mitigates.) |
| Apply model | **Save writes the file and immediately hot-reloads the live subset**; restart-required changes are reported. A standalone **Reload** re-reads the file (e.g. after a CLI/external edit). |
| Hot-reload subset | **Live:** policy gates + `strict_signals`, `[rescan]`, alerts webhook URL. **Restart-required (reported):** server host/port/timeouts/TLS, storage backend/paths, ecosystem enable/disable, log/eventlog/list paths, `dashboard.secret`, `dashboard.password`. |
| Reload surfaces | One in-process reload routine, called by `POST /api/reload`, `SIGHUP`, and `escrow-cli reload` (which signals the proxy). |
| CLI live view | `escrow-cli live` tails the persisted event-log JSONL (no auth, local), filtered by `--eco` and `--activity`. |

## Architecture

### Live-reconfigurable components

- **`policy.Engine`** — guard its `cfg *config.PolicyConfig` with a `sync.RWMutex`; add
  `SetConfig(*config.PolicyConfig)`. `Evaluate` reads under `RLock`. Makes gates/actions live.
- **`rescan.Scanner`** — guard `cfg Config` with a mutex; add `SetConfig(Config)`. `RunOnce`
  reads `enabled`/`auto_block`/`min_severity` under the lock (apply live). `Start`'s loop uses
  a re-computed `time.After(currentInterval)` each cycle instead of a fixed `Ticker`, so an
  `interval_hours` change applies on the next cycle.
- **`alerts.Webhook`** — add `SetURL(string)` (mutex-guarded) so the alert target can change live.

### Reload routine

A `ReloadFunc func() (ReloadResult, error)` constructed in `main.go`, capturing `polEngine`,
`scanner`, the webhook, the config path, a logger, and a **startup snapshot of the
restart-only fields**. On call it:

1. Loads + validates the config file (reject on invalid; no partial apply).
2. Diffs the restart-only fields against the startup snapshot → `RestartRequired []string`.
3. Applies the live subset: `polEngine.SetConfig(cfg.Policy)`, `scanner.SetConfig(...)`,
   `webhook.SetURL(cfg.Alerts...)`.
4. Returns `ReloadResult{ Reloaded []string, RestartRequired []string }` and logs it.

Wiring:
- **`SIGHUP`** — `main.go` adds `SIGHUP` to `signal.Notify`; on receipt it calls `ReloadFunc`
  and logs the result (does not exit).
- **`POST /api/reload`** — dashboard handler (auth + CSRF) calls `ReloadFunc`, returns the JSON result.
- **PID file** — `main.go` writes `<cacheDir or config dir>/escrow.pid` on startup, removes on
  shutdown, so the CLI can find and signal the process.

### Settings (dashboard)

- **`config.Save(path string, cfg Config) error`** — copy `path`→`path.bak`, then write the
  TOML encoding of `cfg` with a generated-by-escrow header comment.
- **`GET /api/settings`** — parse the on-disk config, return JSON; `dashboard.password` is
  returned for the read-only reveal field but flagged `password_editable:false`.
- **`POST /api/settings`** — parse incoming `Config`; **force `dashboard.password` to the
  current on-disk value** (immutable regardless of payload); run config validation (reject,
  no write, on error); `config.Save` (with backup); then call `ReloadFunc`; return
  `{ ok, reloaded:[...], restart_required:[...] }`.
- The dashboard receives the **config file path** (new `New` param) and the `ReloadFunc`.

### Frontend

A **Settings** tab: a sectioned form (Server / Storage / Ecosystems / Policy / Rescan /
Alerts), the password field disabled with a reveal toggle and a "managed in config file"
note, an inline ⚠ on restart-only fields, a **Save** button (writes + live-applies, shows a
banner listing what reloaded vs what needs a restart), and a **Reload from file** button
(applies the on-disk file without saving the form).

### CLI

- **`escrow-cli reload`** — locate the proxy via the PID file and send `SIGHUP`; print the
  outcome. On Windows (no `SIGHUP`): print "use the dashboard Reload button or restart escrow."
- **`escrow-cli live [--eco npm] [--activity downloaded|scanned|all|blocked]`** — resolve the
  event-log path (from the config file, or the disk-backend default), tail it (poll for new
  lines), parse each `PackageEvent`, filter by `--eco` and `--activity` (maps to the event
  `kind`/`action`), and print colorized lines (time · eco · package · status · kind). If no
  persisted event log exists, print a clear "enable event log persistence (disk backend or
  `eventlog_path`)" message and exit non-zero.

## Error handling / edge cases

- Invalid config on save or reload → reject, keep the running config, surface the validation
  error; the `.bak` is only written for a successful save.
- `dashboard.password` immutability is enforced **server-side**, not just the disabled input.
- Reload is mutex-safe against a concurrent scheduled rescan sweep (Scanner already serializes).
- `SIGHUP` reload failure logs the error and keeps the previous in-memory config.
- PID file: best-effort; `escrow-cli reload` errors clearly if it's missing/stale.
- CLI `live` tailing handles file truncation/rotation defensively (re-open on shrink).

## Testing

- `policy.SetConfig` / `rescan.SetConfig` / `webhook.SetURL` apply under concurrency (race test).
- `config.Save` round-trip (save→reload equals input) + backup file created.
- Reload routine: live subset applied, restart-only change reported, invalid config rejected.
- `POST /api/settings`: password preserved despite a different payload value; invalid rejected
  (no write); valid writes + returns reloaded/restart_required. Auth + CSRF enforced.
- `POST /api/reload`: returns the result; auth + CSRF.
- CLI `live`: filters by eco/activity against a fixture JSONL; missing-file message.
- Playwright: edit a policy threshold → Save → banner shows "reloaded: policy"; edit a storage
  path → Save → banner lists it under restart-required.

## Build sequencing (one spec, phased)

1. Live-reconfig (`SetConfig`/`SetURL`) + reload routine + `SIGHUP` + PID file + `POST /api/reload`.
2. `config.Save` + `GET`/`POST /api/settings` (save→reload, password immutability, validation,
   backup) + frontend Settings tab + Reload button.
3. CLI `reload` (PID + SIGHUP) + CLI `live` (tail JSONL, `--eco`/`--activity`).

## Open risks

- **Comment loss** on re-encode — mitigated by `.bak`; documented in the UI.
- **Single-user trust** — the settings page is as privileged as the existing allow/block
  surface; excluding the password/secret-rotation lockout vectors keeps blast radius bounded.
  Full RBAC remains a separate, larger effort.
- **Interval change latency** — an `interval_hours` change applies on the next rescan cycle,
  not instantly; acceptable and documented.
