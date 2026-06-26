// Package logfile holds shared helpers for the append-only JSONL observability
// logs (eventlog, egresslog).
package logfile

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path/filepath"
)

// ReadTail returns the trailing bytes of path, at most maxBytes. When the file
// is larger than maxBytes it reads only the last maxBytes and discards the
// first (possibly partial) line, so the result begins at a record boundary.
// This bounds startup memory/time for a log that grew large before compaction
// existed (or between runs), independent of the file's total size. maxBytes <= 0
// reads the whole file.
func ReadTail(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := fi.Size()
	if maxBytes <= 0 || size <= maxBytes {
		return io.ReadAll(f)
	}
	if _, err := f.Seek(size-maxBytes, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		data = data[i+1:] // drop the partial leading line
	}
	return data, nil
}

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
