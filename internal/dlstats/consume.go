package dlstats

import (
	"context"
	"strings"

	"github.com/jverhoeks/escrow/internal/eventlog"
)

// Consume subscribes to the event log and increments the store for every
// kind=downloaded event until ctx is cancelled. Run it in its own goroutine.
func Consume(ctx context.Context, log *eventlog.Log, store *Store) {
	ch, unsub := log.Subscribe()
	if ch == nil {
		return // subscriber cap reached; stats simply won't populate
	}
	defer unsub()
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-ch:
			if e.Kind != eventlog.KindDownloaded {
				continue
			}
			name, version := splitPackage(e.Package)
			if name == "" {
				continue
			}
			store.Incr(e.Ecosystem, name, version)
		}
	}
}

// splitPackage splits "name@version" on the LAST '@' (scoped npm names start
// with '@'). Mirrors the dashboard's splitPackage.
func splitPackage(pkg string) (name, version string) {
	i := strings.LastIndex(pkg, "@")
	if i <= 0 {
		return pkg, ""
	}
	return pkg[:i], pkg[i+1:]
}
