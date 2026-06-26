# Egress-log persistence + size-cap rotation + relative live-feed time — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the egress log persist by default on the `disk` backend (survives restart/upgrade), bound both observability JSONL files with a runtime size-cap, and render relative time ("5m ago") in the Live Feed and Egress dashboard views.

**Architecture:** Mirror the event log's default-path resolution for the egress log in `main.go`. Add one shared, atomically-tested helper (`internal/logfile.AtomicRewrite`) and wire a per-instance size-cap into both `egresslog` and `eventlog` so the file is compacted to the in-memory capped events (oldest-first) once it crosses ~8 MiB. Frontend gets a `fmtRelative` helper plus a 30s refresher.

**Tech Stack:** Go 1.x, `testify`, zerolog (in `main` only), vanilla JS embedded in `internal/dashboard/static/index.html`.

**Design spec:** `docs/superpowers/specs/2026-06-08-live-feed-persistence-and-relative-time-design.md`

**Branch / worktree:** `feat/egress-log-persistence` at `/tmp/escrow-persist` (off `main` @ `a1d6717`).

**Standing constraints (from CLAUDE.md / memory):**
- Commit locally; do NOT push or open a PR until the user asks.
- Do NOT run `escrow-cli config write` against the real `$HOME`.
- Keep GitHub Actions SHA-pinned + least-privilege (no Actions changes here anyway).

---

## File Structure

- **Create** `internal/logfile/compact.go` — `AtomicRewrite(path, lines)` + `DefaultMaxBytes` const. One responsibility: crash-safe whole-file rewrite.
- **Create** `internal/logfile/compact_test.go` — unit tests for the helper.
- **Modify** `internal/egresslog/log.go` — add `path`/`curBytes`/`maxBytes` fields, `MkdirAll`, `0o600`, size-cap compaction.
- **Create** `internal/egresslog/rotation_test.go` — white-box rotation + ordering test (lowers `maxBytes`).
- **Modify** `internal/eventlog/log.go` — same size-cap wiring (already has `MkdirAll`/`0o600`).
- **Create** `internal/eventlog/rotation_test.go` — white-box rotation + ordering test.
- **Modify** `internal/config/config.go` — `egress_log_path` collision warning (`Warnings()`, ~`:462`).
- **Modify** `internal/config/validate_test.go` — test for the new warning.
- **Modify** `cmd/escrow/main.go:189-199` — egress default-path block.
- **Modify** `internal/dashboard/static/index.html` — `fmtRelative`, refresher, Live Feed + Egress rows.

---

## Task 1: `internal/logfile.AtomicRewrite` helper

**Files:**
- Create: `internal/logfile/compact.go`
- Test: `internal/logfile/compact_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// internal/logfile/compact_test.go
package logfile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jverhoeks/escrow/internal/logfile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAtomicRewrite_WritesLinesWithNewlines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.jsonl")
	n, err := logfile.AtomicRewrite(path, [][]byte{[]byte(`{"a":1}`), []byte(`{"b":2}`)})
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "{\"a\":1}\n{\"b\":2}\n", string(data))
	assert.Equal(t, int64(len(data)), n)
}

func TestAtomicRewrite_OverwritesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("old\nold\nold\n"), 0o600))
	_, err := logfile.AtomicRewrite(path, [][]byte{[]byte("new")})
	require.NoError(t, err)
	data, _ := os.ReadFile(path)
	assert.Equal(t, "new\n", string(data))
}

func TestAtomicRewrite_Mode0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.jsonl")
	_, err := logfile.AtomicRewrite(path, [][]byte{[]byte("x")})
	require.NoError(t, err)
	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
}

func TestAtomicRewrite_NoTempLeftBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.jsonl")
	_, err := logfile.AtomicRewrite(path, [][]byte{[]byte("x")})
	require.NoError(t, err)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "out.jsonl", entries[0].Name())
}

func TestAtomicRewrite_ErrorWhenDirMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope", "out.jsonl")
	_, err := logfile.AtomicRewrite(path, [][]byte{[]byte("x")})
	require.Error(t, err)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /tmp/escrow-persist && go test ./internal/logfile/ -v`
Expected: FAIL — `package .../internal/logfile` does not exist / `undefined: logfile.AtomicRewrite`.

- [ ] **Step 3: Write the implementation**

```go
// internal/logfile/compact.go

// Package logfile holds shared helpers for the append-only JSONL observability
// logs (eventlog, egresslog).
package logfile

import (
	"bufio"
	"os"
	"path/filepath"
)

// DefaultMaxBytes is the file size at which a JSONL log is compacted down to its
// in-memory capped events. ~8 MiB ≈ 55k events at ~150 B each, so each
// compaction (to the last few thousand events) is infrequent.
const DefaultMaxBytes int64 = 8 << 20

// AtomicRewrite replaces the contents of path with lines, atomically. Each
// element of lines is one record WITHOUT a trailing newline; AtomicRewrite
// appends '\n' to each. It writes to a temp file in the same directory and
// renames it into place, so a crash mid-write never leaves a partial file at
// path. Returns the size in bytes of the new file. The caller is responsible
// for any locking and for reopening path afterward (the rename detaches an
// already-open fd from the new inode). The file is created with mode 0o600.
func AtomicRewrite(path string, lines [][]byte) (int64, error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return 0, err
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName) // cleanup on any early return
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return 0, err
	}
	w := bufio.NewWriter(tmp)
	var size int64
	for _, ln := range lines {
		n, werr := w.Write(ln)
		if werr != nil {
			_ = tmp.Close()
			return 0, werr
		}
		if werr := w.WriteByte('\n'); werr != nil {
			_ = tmp.Close()
			return 0, werr
		}
		size += int64(n) + 1
	}
	if err := w.Flush(); err != nil {
		_ = tmp.Close()
		return 0, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return 0, err
	}
	tmpName = "" // renamed successfully — don't remove
	return size, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /tmp/escrow-persist && go test ./internal/logfile/ -v`
Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
cd /tmp/escrow-persist && git add internal/logfile/ && \
git commit -m "feat(logfile): add atomic whole-file rewrite helper for JSONL compaction"
```

---

## Task 2: egresslog — default-dir, 0o600, runtime size-cap

**Files:**
- Modify: `internal/egresslog/log.go`
- Test: `internal/egresslog/rotation_test.go` (create)

- [ ] **Step 1: Write the failing white-box test**

```go
// internal/egresslog/rotation_test.go
package egresslog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRotation_CompactsAndPreservesOrder compacts across THREE sessions — the
// middle session reopens an already-compacted file and compacts it AGAIN, which
// is where a double-reverse ordering bug compounds. maxBytes (32 KiB) is set
// comfortably above the compacted-block size (~20 events ≈ 7 KiB) so compaction
// actually brings curBytes back under the cap (no storm).
func TestRotation_CompactsAndPreservesOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "egress.jsonl")
	const capN = 20
	pad := strings.Repeat("x", 256) // each event ≈ 340 B

	writeBatch := func(l *Log, start, n int) {
		for i := start; i < start+n; i++ {
			l.Record(Event{Host: fmt.Sprintf("h%04d", i), Action: "allow", Reason: pad})
		}
	}

	// Session 1: write past the cap → at least one compaction.
	l1, err := NewWithPath(capN, path)
	require.NoError(t, err)
	l1.maxBytes = 32 * 1024
	writeBatch(l1, 0, 300)
	require.NoError(t, l1.Close())

	// Session 2: reopen the compacted file, write more → compact AGAIN.
	l2, err := NewWithPath(capN, path)
	require.NoError(t, err)
	l2.maxBytes = 32 * 1024
	ev2 := l2.Recent(0, "")
	require.Len(t, ev2, capN)
	assert.Equal(t, "h0299", ev2[0].Host, "newest-first after reopen #1")
	writeBatch(l2, 300, 300)
	require.NoError(t, l2.Close())

	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Less(t, fi.Size(), int64(64*1024), "file stays bounded across sessions")

	// Session 3: final reopen → order MUST still be newest-first, and the kept
	// window is exactly the last capN events (h0580..h0599).
	l3, err := NewWithPath(capN, path)
	require.NoError(t, err)
	defer l3.Close()
	ev3 := l3.Recent(0, "")
	require.Len(t, ev3, capN)
	assert.Equal(t, "h0599", ev3[0].Host, "newest-first after reopen #2")
	assert.Equal(t, "h0580", ev3[capN-1].Host, "oldest of the kept window")
}

func TestNewWithPath_CreatesDirAndMode0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "nested", "egress.jsonl")
	l, err := NewWithPath(10, path)
	require.NoError(t, err, "should auto-create parent dirs")
	defer l.Close()
	l.Record(Event{Host: "h", Action: "allow"})
	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /tmp/escrow-persist && go test ./internal/egresslog/ -run 'Rotation|CreatesDir' -v`
Expected: FAIL — `l.maxBytes undefined` (compile error), and `0o600` mismatch (`0o644`), and missing-dir open error.

- [ ] **Step 3: Update imports**

In `internal/egresslog/log.go`, change the import block to add `fmt`, `path/filepath`, and the new `logfile` package:

```go
import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/jverhoeks/escrow/internal/logfile"
)
```

- [ ] **Step 4: Add struct fields + maxBytes**

Replace the `Log` struct (`internal/egresslog/log.go:50-58`) with:

```go
type Log struct {
	mu          sync.RWMutex
	cap         int
	events      []Event // newest-first
	bytes       int64   // aggregate bytes proxied (allow path)
	subscribers map[int]chan Event
	nextID      int
	file        *os.File
	path        string // retained for size-cap compaction
	curBytes    int64  // bytes written to file since last compaction
	maxBytes    int64  // compact when curBytes exceeds this (0 = never)
}
```

- [ ] **Step 5: Update `NewWithPath` (dir create, 0o600, seed counters)**

Replace `NewWithPath` (`internal/egresslog/log.go:67-93`) with:

```go
func NewWithPath(cap int, path string) (*Log, error) {
	l := New(cap)
	l.path = path
	l.maxBytes = logfile.DefaultMaxBytes
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
		if len(loaded) > l.cap {
			loaded = loaded[len(loaded)-l.cap:]
		}
		for i := len(loaded) - 1; i >= 0; i-- {
			l.events = append(l.events, loaded[i])
		}
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create egress log directory: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	l.file = f
	if fi, serr := f.Stat(); serr == nil {
		l.curBytes = fi.Size()
	}
	return l, nil
}
```

- [ ] **Step 6: Track bytes + trigger compaction in `Record`**

In `Record` (`internal/egresslog/log.go:95-120`), replace the file-write block:

```go
	if l.file != nil {
		if b, err := json.Marshal(e); err == nil {
			l.file.Write(append(b, '\n'))
		}
	}
```

with:

```go
	if l.file != nil {
		if b, err := json.Marshal(e); err == nil {
			if n, werr := l.file.Write(append(b, '\n')); werr == nil {
				l.curBytes += int64(n)
				if l.maxBytes > 0 && l.curBytes > l.maxBytes {
					l.compactLocked() // guarded: sets file=nil on failure, no retry storm
				}
			}
		}
	}
```

- [ ] **Step 7: Add `compactLocked`**

Add this method to `internal/egresslog/log.go` (e.g. right after `Record`):

```go
// compactLocked rewrites the file to hold only the in-memory capped events,
// oldest-first, bounding on-disk growth. The caller MUST hold l.mu. The file is
// oldest-first (append order) and NewWithPath reverses it back to newest-first
// on load, so we must emit l.events (newest-first) in REVERSE. On any error this
// disables file persistence (l.file = nil) until restart; the in-memory log is
// unaffected (these leaf packages carry no logger and Record returns no error).
func (l *Log) compactLocked() {
	lines := make([][]byte, 0, len(l.events))
	for i := len(l.events) - 1; i >= 0; i-- {
		if b, err := json.Marshal(l.events[i]); err == nil {
			lines = append(lines, b)
		}
	}
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
	n, err := logfile.AtomicRewrite(l.path, lines)
	if err != nil {
		return
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	l.file = f
	l.curBytes = n
}
```

- [ ] **Step 8: Run tests to verify they pass**

Run: `cd /tmp/escrow-persist && go test ./internal/egresslog/ -v`
Expected: PASS (existing tests + `TestRotation_CompactsAndPreservesOrder` + `TestNewWithPath_CreatesDirAndMode0600`).

- [ ] **Step 9: Commit**

```bash
cd /tmp/escrow-persist && git add internal/egresslog/ && \
git commit -m "feat(egresslog): default-dir create, 0o600, runtime size-cap compaction"
```

---

## Task 3: eventlog — runtime size-cap (parity)

**Files:**
- Modify: `internal/eventlog/log.go`
- Test: `internal/eventlog/rotation_test.go` (create)

`eventlog` already has `MkdirAll` + `0o600`; it only needs the size-cap wiring.

- [ ] **Step 1: Write the failing white-box test**

```go
// internal/eventlog/rotation_test.go
package eventlog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Mirrors the egresslog rotation test: three sessions, the middle one compacts
// an already-compacted file. maxBytes (32 KiB) sits above the compacted-block
// size so compaction brings curBytes back under the cap (no storm).
func TestRotation_CompactsAndPreservesOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	const capN = 20
	pad := strings.Repeat("x", 256)

	writeBatch := func(l *Log, start, n int) {
		for i := start; i < start+n; i++ {
			l.Record(PackageEvent{Package: fmt.Sprintf("p%04d@1", i), Action: "allow", Reason: pad})
		}
	}

	l1, err := NewWithPath(capN, path)
	require.NoError(t, err)
	l1.maxBytes = 32 * 1024
	writeBatch(l1, 0, 300)
	require.NoError(t, l1.Close())

	l2, err := NewWithPath(capN, path)
	require.NoError(t, err)
	l2.maxBytes = 32 * 1024
	ev2 := l2.Events("")
	require.Len(t, ev2, capN)
	assert.Equal(t, "p0299@1", ev2[0].Package, "newest-first after reopen #1")
	writeBatch(l2, 300, 300)
	require.NoError(t, l2.Close())

	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Less(t, fi.Size(), int64(64*1024), "file stays bounded across sessions")

	l3, err := NewWithPath(capN, path)
	require.NoError(t, err)
	defer l3.Close()
	ev3 := l3.Events("")
	require.Len(t, ev3, capN)
	assert.Equal(t, "p0599@1", ev3[0].Package, "newest-first after reopen #2")
	assert.Equal(t, "p0580@1", ev3[capN-1].Package, "oldest of the kept window")
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /tmp/escrow-persist && go test ./internal/eventlog/ -run Rotation -v`
Expected: FAIL — `l.maxBytes undefined`.

- [ ] **Step 3: Add the `logfile` import**

In `internal/eventlog/log.go`, add to the import block:

```go
	"github.com/jverhoeks/escrow/internal/logfile"
```

(Keep the existing imports: `bufio`, `encoding/json`, `fmt`, `os`, `path/filepath`, `strings`, `sync`, `time`, and `.../internal/trust`.)

- [ ] **Step 4: Add struct fields**

Replace the `Log` struct (`internal/eventlog/log.go:59-66`) with:

```go
type Log struct {
	mu          sync.RWMutex
	cap         int
	events      []PackageEvent
	subscribers map[int]chan PackageEvent
	nextID      int
	file        *os.File // append-only JSONL; nil = in-memory only
	path        string   // retained for size-cap compaction
	curBytes    int64    // bytes written to file since last compaction
	maxBytes    int64    // compact when curBytes exceeds this (0 = never)
}
```

- [ ] **Step 5: Seed `path`, `maxBytes`, `curBytes` in `NewWithPath`**

In `NewWithPath` (`internal/eventlog/log.go:76-117`):

(a) Right after `l := &Log{cap: cap, subscribers: make(map[int]chan PackageEvent)}`, add:

```go
	l.path = path
	l.maxBytes = logfile.DefaultMaxBytes
```

(b) After the file is opened and assigned (`l.file = f`), before `return l, nil`, add:

```go
	if fi, serr := f.Stat(); serr == nil {
		l.curBytes = fi.Size()
	}
```

- [ ] **Step 6: Track bytes + trigger compaction in `Record`**

In `Record` (`internal/eventlog/log.go:131-156`), replace the file-write block:

```go
	if l.file != nil {
		if data, err := json.Marshal(e); err == nil {
			l.file.Write(append(data, '\n')) // best-effort
		}
	}
```

with:

```go
	if l.file != nil {
		if data, err := json.Marshal(e); err == nil {
			if n, werr := l.file.Write(append(data, '\n')); werr == nil {
				l.curBytes += int64(n)
				if l.maxBytes > 0 && l.curBytes > l.maxBytes {
					l.compactLocked()
				}
			}
		}
	}
```

- [ ] **Step 7: Add `compactLocked`**

Add to `internal/eventlog/log.go` (e.g. right after `Record`):

```go
// compactLocked rewrites the file to hold only the in-memory capped events,
// oldest-first, bounding on-disk growth. The caller MUST hold l.mu. l.events is
// newest-first and NewWithPath reverses on load, so emit in REVERSE. On error,
// file persistence is disabled until restart; the in-memory log is unaffected.
func (l *Log) compactLocked() {
	lines := make([][]byte, 0, len(l.events))
	for i := len(l.events) - 1; i >= 0; i-- {
		if b, err := json.Marshal(l.events[i]); err == nil {
			lines = append(lines, b)
		}
	}
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
	n, err := logfile.AtomicRewrite(l.path, lines)
	if err != nil {
		return
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	l.file = f
	l.curBytes = n
}
```

- [ ] **Step 8: Run tests to verify they pass**

Run: `cd /tmp/escrow-persist && go test ./internal/eventlog/ -v`
Expected: PASS (existing persistence tests + `TestRotation_CompactsAndPreservesOrder`).

- [ ] **Step 9: Commit**

```bash
cd /tmp/escrow-persist && git add internal/eventlog/ && \
git commit -m "feat(eventlog): runtime size-cap compaction (parity with egresslog)"
```

---

## Task 4: config — `egress_log_path` collision warning

**Files:**
- Modify: `internal/config/config.go` (`Warnings()`, ~`:462`)
- Test: `internal/config/validate_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/config/validate_test.go` (after `TestWarnings_MemoryBackendUnsuitable`):

```go
func TestWarnings_EgressLogPathCollision(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AllowlistPath = "/tmp/escrow/allow.json"
	cfg.EgressLogPath = "/tmp/escrow/allow.json" // same as allowlist
	warnings := cfg.Warnings()
	found := false
	for _, w := range warnings {
		if contains(w, "egress_log_path") && contains(w, "corrupt") {
			found = true
		}
	}
	assert.True(t, found, "should warn when egress_log_path collides with a list file")
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /tmp/escrow-persist && go test ./internal/config/ -run EgressLogPathCollision -v`
Expected: FAIL — `should warn when egress_log_path collides with a list file`.

- [ ] **Step 3: Add the warning**

In `internal/config/config.go`, immediately after the existing `eventlog_path` collision block (~`:462-464`):

```go
	if c.EventLogPath != "" && (c.EventLogPath == c.AllowlistPath || c.EventLogPath == c.BlocklistPath) {
		w = append(w, "eventlog_path is the same as allowlist_path or blocklist_path — JSONL appends will corrupt the list file")
	}
```

add:

```go
	if c.EgressLogPath != "" && (c.EgressLogPath == c.AllowlistPath || c.EgressLogPath == c.BlocklistPath) {
		w = append(w, "egress_log_path is the same as allowlist_path or blocklist_path — JSONL appends will corrupt the list file")
	}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd /tmp/escrow-persist && go test ./internal/config/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /tmp/escrow-persist && git add internal/config/ && \
git commit -m "feat(config): warn when egress_log_path collides with allow/block list path"
```

---

## Task 5: main.go — egress log persists by default on disk

**Files:**
- Modify: `cmd/escrow/main.go:189-199`

No unit test (wiring in `main`); verified by build + the package tests above + a smoke run in Task 7. `filepath` is already imported in `main.go`.

- [ ] **Step 1: Replace the egress-log construction block**

Replace `cmd/escrow/main.go:189-199`:

```go
	var egressLog *egresslog.Log
	if cfg.EgressLogPath != "" {
		var err error
		egressLog, err = egresslog.NewWithPath(5000, config.ExpandPath(cfg.EgressLogPath))
		if err != nil {
			log.Fatal().Err(err).Msg("egress log")
		}
	} else {
		egressLog = egresslog.New(5000)
	}
	defer egressLog.Close()
```

with:

```go
	// Resolve the effective egress-log path so the egress live view survives
	// restarts by default on the disk backend (mirrors the event log below). An
	// explicit egress_log_path always wins; memory/s3 backends stay in-memory.
	var egressLogPath string
	if cfg.EgressLogPath != "" {
		egressLogPath = config.ExpandPath(cfg.EgressLogPath)
	} else if cfg.Storage.Backend == "disk" {
		egressLogPath = filepath.Join(config.ExpandPath(cfg.Storage.Disk.Path), "escrow-egress.jsonl")
	}

	var egressLog *egresslog.Log
	if egressLogPath != "" {
		var err error
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

- [ ] **Step 2: Build + vet**

Run: `cd /tmp/escrow-persist && go build ./... && go vet ./...`
Expected: no output (success).

- [ ] **Step 3: Commit**

```bash
cd /tmp/escrow-persist && git add cmd/escrow/main.go && \
git commit -m "feat(egress): persist egress log by default on disk backend"
```

---

## Task 6: dashboard — relative time in Live Feed + Egress views

**Files:**
- Modify: `internal/dashboard/static/index.html`

No JS test runner exists; verify via `fmtRelative` reasoning + the Task 7 smoke run (open dashboard, confirm "Xs/m ago" + hover tooltip shows absolute).

- [ ] **Step 1: Add `fmtRelative` + a refresher**

In `internal/dashboard/static/index.html`, immediately after `fmtTime` (the function ending at ~`:1437`), add:

```js
function fmtRelative(ts) {
  const then = new Date(ts).getTime();
  if (isNaN(then)) return fmtTime(ts);
  const s = Math.max(0, Math.floor((Date.now() - then) / 1000));
  if (s < 5)   return 'just now';
  if (s < 60)  return s + 's ago';
  const m = Math.floor(s / 60);
  if (m < 60)  return m + 'm ago';
  const h = Math.floor(m / 60);
  if (h < 24)  return h + 'h ago';
  const d = Math.floor(h / 24);
  if (d < 7)   return d + 'd ago';
  return new Date(ts).toLocaleDateString('en-GB', {day: '2-digit', month: 'short'});
}

// refreshRelativeTimes rewrites every .feed-time cell from its stored ISO ts so
// "5m ago" keeps advancing without a reload.
function refreshRelativeTimes() {
  document.querySelectorAll('.feed-time[data-ts]').forEach(el => {
    el.textContent = fmtRelative(el.dataset.ts);
  });
}
setInterval(refreshRelativeTimes, 30000);
```

- [ ] **Step 2: Live Feed rows render relative time**

In `makeRow` (~`:1454-1460`), replace:

```js
  const ts = document.createElement('div');
  ts.className = 'feed-time';
  ts.textContent = fmtTime(e.timestamp);
```

with:

```js
  const ts = document.createElement('div');
  ts.className = 'feed-time';
  ts.textContent = fmtRelative(e.timestamp);
  ts.title = fmtTime(e.timestamp);
  ts.dataset.ts = e.timestamp;
```

- [ ] **Step 3: Egress rows render relative time**

In `egressRow` (~`:2497`), replace:

```js
  const timeCell = cell(e.timestamp ? new Date(e.timestamp).toLocaleTimeString() : '—');
  timeCell.style.color = 'var(--text-tertiary)';
  timeCell.style.fontSize = '12px';
```

with:

```js
  const timeCell = cell(e.timestamp ? fmtRelative(e.timestamp) : '—');
  if (e.timestamp) {
    timeCell.classList.add('feed-time');
    timeCell.title = fmtTime(e.timestamp);
    timeCell.dataset.ts = e.timestamp;
  }
  timeCell.style.color = 'var(--text-tertiary)';
  timeCell.style.fontSize = '12px';
```

(`cell(txt, cls, extra)` in `egressRow` takes a class as its 2nd arg and a style object as its 3rd; here we add the class after construction via `classList.add` to avoid disturbing the existing positional call.)

- [ ] **Step 4: Build (embeds the asset)**

Run: `cd /tmp/escrow-persist && go build ./...`
Expected: success. (The static dir is `go:embed`-ed; build confirms it still compiles.)

- [ ] **Step 5: Commit**

```bash
cd /tmp/escrow-persist && git add internal/dashboard/static/index.html && \
git commit -m "feat(dashboard): relative time in Live Feed and Egress views"
```

---

## Task 7: Full verification gate

- [ ] **Step 1: Build + vet + full test + race on touched packages**

```bash
cd /tmp/escrow-persist && \
go build ./... && \
go vet ./... && \
go test ./... && \
go test -race ./internal/logfile/ ./internal/egresslog/ ./internal/eventlog/ ./internal/config/
```
Expected: all PASS, no vet/build output.

- [ ] **Step 2: Smoke-run persistence (disk backend)**

```bash
cd /tmp/escrow-persist && go build -o /tmp/escrow-bin ./cmd/escrow
TMPDIR_RUN=$(mktemp -d)
cat > "$TMPDIR_RUN/escrow.toml" <<EOF
[server]
  host = "127.0.0.1"
  port = 0
[storage]
  backend = "disk"
  [storage.disk]
    path = "$TMPDIR_RUN/cache"
[ecosystems]
  npm = true
[dashboard]
  enabled = false
EOF
/tmp/escrow-bin --config="$TMPDIR_RUN/escrow.toml" &
ESC_PID=$!
sleep 2
kill -TERM $ESC_PID 2>/dev/null
# Confirm the egress JSONL default path was created under the cache dir:
ls -la "$TMPDIR_RUN/cache/" | grep -E 'escrow-egress.jsonl|escrow-events.jsonl' || echo "NOTE: files appear once egress/events occur; log line 'egress log persistence enabled' is the key signal"
```
Expected: startup log shows `egress log persistence enabled (default path)` with a path under the cache dir. (The `.jsonl` files materialize on first event; the log line proves the default path resolved.)

- [ ] **Step 3: Final review against the spec**

Re-read `docs/superpowers/specs/2026-06-08-live-feed-persistence-and-relative-time-design.md` and confirm every component is implemented. Then STOP — do not push or open a PR until the user asks.

---

## Self-review notes (author)

- **Spec coverage:** C1 → Task 5; C2 helper → Task 1; C2 wiring + fold-in (MkdirAll/0o600 for egress, ordering) → Tasks 2–3; collision warning → Task 4; C3 → Task 6. All covered.
- **Ordering trap:** the `compactLocked` reverse-emit + the double-reopen assertion (Tasks 2 & 3) directly guard it.
- **Type consistency:** field names `path`/`curBytes`/`maxBytes`, method `compactLocked`, and `logfile.AtomicRewrite`/`logfile.DefaultMaxBytes` are used identically across Tasks 1–3.
- **No push:** all tasks commit locally only, per standing constraint.
