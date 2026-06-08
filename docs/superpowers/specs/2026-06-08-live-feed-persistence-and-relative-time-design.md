# Live-feed persistence + runtime size-cap + relative time — design

**Date:** 2026-06-08
**Status:** Approved (design), pending implementation plan
**Branch:** `feat/egress-log-persistence` (off `main` @ `a1d6717`)

## Problem

On a long-running `disk`-backend deployment (the Homebrew/systemd default — `Formula/escrow.rb`
ships `backend = "disk"`, `path = "~/.cache/escrow"`), the dashboard's observability data does
**not** all survive a restart or `brew upgrade`:

- **Event log** + **download stats** already persist by default to `~/.cache/escrow/` and survive
  restart/upgrade (the path is in `$HOME`, not Homebrew's `var/`).
- **Egress log** is **memory-only by default** — `cmd/escrow/main.go:189-199` only persists when
  `egress_log_path` is set explicitly, and unlike the event log it has *no* `disk`-backend default.
  So the egress live view is wiped on every restart.

Two secondary issues, in scope:

1. **Unbounded file growth.** Both `egresslog` and `eventlog` are append-only JSONL with no
   rotation (`Record` appends forever; reload only trims to the last 5000 *in memory*). On a
   long-running service the file grows for the whole uptime.
2. **Live feed shows absolute clock time** (`fmtTime` → `14:32:05`,
   `internal/dashboard/static/index.html:1435`). Users want relative time ("5m ago").

"Restart" and "upgrade" are the same case here: bare-binary/systemd/brew keeps stable paths, so
the fix is "persist to a stable default path + reload on startup" — no migration concerns. The
JSONL schema is additive-only (`json.Unmarshal` tolerates unknown/missing fields), so the format
is upgrade-compatible.

## Scope

In scope (disk backend is the target — confirmed deployment):
- Egress log persists by default on `disk`, mirroring the event log.
- Runtime size-cap rotation for **both** egress and event logs (the event-log file has the same
  unbounded-growth issue today and shares the code path — fix both for consistency).
- Relative time in the live feeds, with absolute time preserved as a tooltip.

Out of scope (YAGNI):
- Decoupling log persistence from the storage backend (would help `s3`/`memory` users; deferred —
  current target is `disk`, and this keeps the existing event-log behavior consistent).
- Any change to chart axis ticks (they stay absolute clock time — relative is meaningless there).

## Component 1 — Egress log persists by default

**File:** `cmd/escrow/main.go` (replace the block at `:189-199`).

Apply the same path-resolution the event log already uses (`main.go` event-log block):

```go
var egressLogPath string
if cfg.EgressLogPath != "" {
    egressLogPath = config.ExpandPath(cfg.EgressLogPath)
} else if cfg.Storage.Backend == "disk" {
    egressLogPath = filepath.Join(config.ExpandPath(cfg.Storage.Disk.Path), "escrow-egress.jsonl")
}

var egressLog *egresslog.Log
if egressLogPath != "" {
    egressLog, err = egresslog.NewWithPath(5000, egressLogPath)
    if err != nil {
        log.Fatal().Err(err).Str("path", egressLogPath).Msg("failed to open egress log file")
    }
    log.Info().Str("path", egressLogPath).Msg("egress log persistence enabled (default path)")
} else {
    egressLog = egresslog.New(5000)
}
defer egressLog.Close()
```

Net effect on a disk install: egress history survives restart/upgrade with zero config. Memory/s3
stay in-memory (unchanged, still opt-in via `egress_log_path`).

## Component 2 — Runtime size-cap rotation

### 2a. Shared atomic-rewrite helper

**New file:** `internal/logfile/compact.go` (+ `compact_test.go`).

```go
// Package logfile holds shared helpers for the append-only JSONL logs
// (eventlog, egresslog).
package logfile

// AtomicRewrite writes lines to path via a temp file + rename, replacing the
// file's contents atomically (crash-safe: a partial temp file is never renamed
// into place). Each element of lines is one JSON record WITHOUT a trailing
// newline; AtomicRewrite appends '\n' to each. Returns the new file size in
// bytes. The caller holds its own lock and must reopen the file for append
// afterward (the rename detaches the caller's existing fd from the new inode).
func AtomicRewrite(path string, lines [][]byte) (int64, error)
```

Implementation: create `path + ".tmp"` in the same dir (same filesystem → rename is atomic),
write each line + `\n`, `Sync`, `Close`, `os.Rename(tmp, path)`. On any error, remove the temp
file and return the error (caller keeps running on the old file). File mode `0o600`.

This is the only risky I/O, written and tested once.

### 2b. Wire rotation into each log

**Files:** `internal/egresslog/log.go`, `internal/eventlog/log.go`.

- Add a constant `maxFileBytes = 8 << 20` (8 MiB) and a field `curBytes int64` to each `Log`.
- In `NewWithPath`, after opening for append, seed `curBytes` from the file's current size
  (`f.Stat().Size()`).
- In `Record`, after a successful append, `l.curBytes += int64(len(line))`. When
  `l.file != nil` and `l.curBytes > maxFileBytes`, compact **under the already-held write lock**
  (the `l.file != nil` guard means a prior failed compaction — which sets `l.file = nil` — is not
  retried on every subsequent `Record`; persistence simply stays disabled until restart):
  1. Build `lines [][]byte` from the in-memory capped events in **oldest-first** order — i.e.
     iterate `l.events` (newest-first) in reverse and `json.Marshal` each. **This ordering is the
     trap:** the file is oldest-first (append order) and `NewWithPath` reverses it back to
     newest-first on load. Writing the in-memory slice as-is would double-reverse and the feed
     would come back backwards.
  2. `l.file.Close()`.
  3. `n, err := logfile.AtomicRewrite(path, lines)`. On error: leave `l.file = nil` — persistence
     is silently disabled until restart; the in-memory log keeps working. (These are leaf packages
     with no logger and `Record` has no error return, so failure is silent — it's an extremely
     unlikely same-dir rename.) On success: reopen `path` `O_APPEND|O_WRONLY|O_CREATE` `0o600`, set
     `l.file`, set `l.curBytes = n`.
- Sizing rationale: 8 MiB ≈ 55k events at ~150 B each; each compaction shrinks to the last 5000
  (~750 KB), so rewrites are infrequent (every ~50k events).

The `path` must be retained on the `Log` struct (add a `path string` field) so `Record` can
compact — currently neither log stores it.

### 2c. Fold-in fixes (from review)

- `egresslog.NewWithPath` (`log.go:87`): add the `MkdirAll(filepath.Dir(path), 0o755)` that
  `eventlog.NewWithPath` already has (`eventlog/log.go:106-110`) — don't rely on the disk cache
  creating the dir first.
- `egresslog.NewWithPath` (`log.go:87`): open `0o600` (was `0o644`) — the egress log records
  destination host/IP, align with `eventlog`'s `0o600`.
- `internal/config/config.go` (near `:462`): add a collision warning for `egress_log_path` ==
  `allowlist_path`/`blocklist_path`, mirroring the existing `eventlog_path` check.

## Component 3 — Relative time in the live feeds

**File:** `internal/dashboard/static/index.html`.

- Add `fmtRelative(ts)`:
  - `< 5s` → `just now`
  - `< 60s` → `Ns ago`
  - `< 60m` → `Nm ago`
  - `< 24h` → `Nh ago`
  - `< 7d`  → `Nd ago`
  - else → existing absolute date format
- Two render sites, each sets `textContent = fmtRelative(ts)`, `title = <absolute>` (precision on
  hover), and `dataset.ts = <ISO timestamp>` (so the refresher can recompute):
  - **Live Feed** rows — `makeRow` (~`:1454`; the `fmtTime(e.timestamp)` at `:1460`). Cell class
    `.feed-time`.
  - **Egress** view rows — `egressRow` (~`:2497`; currently `new Date(e.timestamp).toLocaleTimeString()`).
    Give its time cell a stable class (e.g. add `.feed-time`) so the refresher finds it.
- A single `setInterval(refreshRelativeTimes, 30000)` re-walks visible `.feed-time` cells and
  rewrites `textContent` from `dataset.ts`, so "5m ago" doesn't freeze. A bad/missing `dataset.ts`
  falls back to leaving the existing text.
- **Out of scope (stay absolute, unchanged):** the Access-log table (`renderAccessLogs` ~`:2135`)
  and the Upstream-fetches table (`renderUpstream` ~`:2169`) — both still use `fmtTime`. They are
  not the "live" feed; revisit later if wanted. Chart axis ticks (`:1694`, `:1705`) also stay
  absolute (relative is meaningless on a time axis).

## Error handling

Persistence stays best-effort and off the request path (existing posture):
- Compaction failure → silent (leaf packages have no logger; `Record` has no error return),
  in-memory log keeps serving; file persistence disabled until restart.
- A failed *explicit/default-resolved* path open is `log.Fatal` (same as the event log today) —
  a configured-but-unwritable path is an operator error worth failing loudly on.
- Frontend refresher is purely cosmetic; a parse failure on a bad `dataset.ts` falls back to the
  existing absolute text.

## Testing

- `logfile.AtomicRewrite`: happy path (content + size), temp file cleaned up on write error,
  overwrites existing content, mode is `0o600`.
- `egresslog` + `eventlog` rotation: append > 8 MiB → assert the file shrank below the cap →
  **reopen twice**, asserting `Recent`/`Events` order stays newest-first *both* times (the second
  reopen is what catches the double-reverse ordering bug).
- `egresslog.NewWithPath`: creates a missing parent dir; opens `0o600`.
- `config` validation: `egress_log_path` collision warning fires.
- Frontend: `fmtRelative` boundary cases (just-now / s / m / h / d / older) — small standalone
  check (manual or a tiny harness; the file has no JS test runner today).

## Build sequence

1. `internal/logfile` helper + tests.
2. Rotation + fold-in fixes in `egresslog`, then `eventlog` (TDD: rotation + double-reopen tests).
3. `config.go` collision warning + test.
4. `main.go` egress default-path block.
5. `index.html` relative time + refresher.
6. Full gate: `go build ./...`, `go vet ./...`, `go test ./...`, `-race` on the touched packages.
