package server

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

const (
	defaultAccessLogMaxDays = 30
	maxLogFieldBytes        = 512 // cap UA/Referer to bound line size
)

// truncateField clamps an attacker-controllable header to a sane length so a
// single hostile request can't produce a multi-megabyte log line.
func truncateField(s string) string {
	if len(s) > maxLogFieldBytes {
		return s[:maxLogFieldBytes]
	}
	return s
}

// AccessLogger writes one line per request in Apache Combined Log Format:
//
//	%h %l %u %t "%r" %>s %b "%{Referer}i" "%{User-agent}i"
//
// Example:
//
//	127.0.0.1 - - [26/May/2026:09:17:46 +0000] "GET /lodash/-/lodash-4.17.21.tgz HTTP/1.1" 200 124567 "-" "npm/11.12.0 node/v22.21.1 darwin arm64"
//
// Daily rotation: at midnight the current log is renamed to
// escrow-access.YYYY-MM-DD.log and a fresh file is opened.
// Files older than maxDays are deleted automatically.
type AccessLogger struct {
	mu      sync.Mutex
	f       *os.File
	path    string
	day     string // YYYY-MM-DD of the currently open file
	maxDays int
	w       io.Writer
}

// NewAccessLogger opens (or creates) the file at path and returns an AccessLogger.
// maxDays is the number of days of rotated logs to keep (0 → defaultAccessLogMaxDays).
func NewAccessLogger(path string, maxDays int) (*AccessLogger, error) {
	if maxDays <= 0 {
		maxDays = defaultAccessLogMaxDays
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}
	al := &AccessLogger{
		f:       f,
		path:    path,
		day:     time.Now().UTC().Format("2006-01-02"),
		maxDays: maxDays,
		w:       f,
	}
	// Clean up old rotated files immediately on start.
	al.cleanup()
	return al, nil
}

// rotate renames the current log file to add a date suffix and opens a new one.
// Must be called with al.mu held. On open failure the writer is set to io.Discard
// and al.day is NOT advanced, so the next request will retry the rotation rather
// than silently dropping log lines forever.
func (al *AccessLogger) rotate(today string) {
	al.f.Close()
	rotated := strings.TrimSuffix(al.path, filepath.Ext(al.path)) + "." + al.day + filepath.Ext(al.path)
	os.Rename(al.path, rotated) //nolint:errcheck
	f, err := os.OpenFile(al.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		// Failed to reopen — discard writes and leave al.day unchanged so the
		// next request retries rotation. al.f set to nil so Close() is safe.
		al.f = nil
		al.w = io.Discard
		return
	}
	al.f = f
	al.w = f
	al.day = today
	al.cleanup()
}

// cleanup deletes rotated log files older than maxDays.
func (al *AccessLogger) cleanup() {
	dir := filepath.Dir(al.path)
	base := strings.TrimSuffix(filepath.Base(al.path), filepath.Ext(al.path))
	cutoff := time.Now().UTC().AddDate(0, 0, -al.maxDays)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, base+".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(dir, name)) //nolint:errcheck
		}
	}
}

// Middleware returns a chi-compatible middleware that writes one access log line
// per request after the handler completes. It also rotates the log file at midnight.
func (al *AccessLogger) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)

			host, _, _ := net.SplitHostPort(r.RemoteAddr)
			if host == "" {
				host = r.RemoteAddr
			}
			referer := r.Referer()
			if referer == "" {
				referer = "-"
			} else {
				referer = truncateField(referer)
			}
			ua := r.UserAgent()
			if ua == "" {
				ua = "-"
			} else {
				ua = truncateField(ua)
			}
			bytes := ww.BytesWritten()
			bytesStr := "-"
			if bytes > 0 {
				bytesStr = fmt.Sprintf("%d", bytes)
			}

			line := fmt.Sprintf("%s - - [%s] %q %d %s %q %q\n",
				host,
				start.UTC().Format("02/Jan/2006:15:04:05 -0700"),
				strings.Join([]string{r.Method, r.RequestURI, r.Proto}, " "),
				ww.Status(),
				bytesStr,
				referer,
				ua,
			)

			al.mu.Lock()
			if today := start.UTC().Format("2006-01-02"); today != al.day {
				al.rotate(today)
			}
			al.w.Write([]byte(line)) //nolint:errcheck
			al.mu.Unlock()
		})
	}
}

// Close flushes and closes the underlying log file.
func (al *AccessLogger) Close() error {
	al.mu.Lock()
	defer al.mu.Unlock()
	if al.f != nil {
		return al.f.Close()
	}
	return nil
}
