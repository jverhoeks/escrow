package rescan

import (
	"context"
	"testing"
	"time"

	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/stretchr/testify/require"
)

// TestSubscribeIndexSeedsHistory is a regression test for A2 / #111: the
// incremental rescan index must be seeded from the full event log when the
// subscription starts. Otherwise the first post-startup event flips
// indexReady() true with an index containing ONLY that event, silently dropping
// all pre-subscription history (e.g. ~5K packages preloaded from the on-disk
// log) from the rescan inventory — so previously-downloaded packages would
// never be re-scanned for retroactive CVEs.
func TestSubscribeIndexSeedsHistory(t *testing.T) {
	log := eventlog.New(100)
	// Historical downloads recorded BEFORE the scanner subscribes (mirrors the
	// on-disk log preloaded at startup; Subscribe does not replay these).
	log.Record(eventlog.PackageEvent{Ecosystem: "npm", Package: "a@1.0.0", Action: "allow", Kind: eventlog.KindDownloaded})
	log.Record(eventlog.PackageEvent{Ecosystem: "npm", Package: "b@1.0.0", Action: "allow", Kind: eventlog.KindDownloaded})

	s := New(Deps{Log: log}, Config{Enabled: true})
	s.idxDone = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.subscribeIndex(ctx)

	// Once the index reports ready it must already reflect the pre-subscription
	// history — the seed runs before the select loop, so readiness implies seeded.
	require.Eventually(t, s.indexReady, time.Second, 5*time.Millisecond, "index never became ready")

	// A new download arriving over the subscription must be added on top of the
	// seeded history, not replace it.
	log.Record(eventlog.PackageEvent{Ecosystem: "npm", Package: "c@1.0.0", Action: "allow", Kind: eventlog.KindDownloaded})
	require.Eventually(t, func() bool {
		inv, _ := s.inventory()
		return len(inv) == 3
	}, time.Second, 5*time.Millisecond, "incremental event not indexed on top of seeded history")

	inv, _ := s.inventory()
	require.Contains(t, inv, verKey{"npm", "a", "1.0.0"}, "pre-subscription history dropped from index")
	require.Contains(t, inv, verKey{"npm", "b", "1.0.0"}, "pre-subscription history dropped from index")
	require.Contains(t, inv, verKey{"npm", "c", "1.0.0"}, "post-subscription event missing from index")
}
