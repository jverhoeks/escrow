package dlstats

import (
	"context"

	"github.com/rs/zerolog/log"

	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/jverhoeks/escrow/internal/pkgref"
)

// Consume subscribes to the event log and increments the store for every
// kind=downloaded event until ctx is cancelled. Run it in its own goroutine.
func Consume(ctx context.Context, evlog *eventlog.Log, store *Store) {
	ch, unsub := evlog.Subscribe()
	if ch == nil {
		// Subscriber cap reached: download stats won't populate and rescan
		// blast-radius counts read zero. Surface it rather than failing silently.
		log.Warn().Msg("dlstats: event-log subscriber cap reached — download statistics will not be recorded")
		return
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
func splitPackage(pkg string) (name, version string) { return pkgref.Split(pkg) }
