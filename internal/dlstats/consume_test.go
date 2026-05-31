package dlstats_test

import (
	"context"
	"testing"
	"time"

	"github.com/jverhoeks/escrow/internal/dlstats"
	"github.com/jverhoeks/escrow/internal/eventlog"
	"github.com/stretchr/testify/require"
)

func TestConsume_IncrementsOnDownloadedEvents(t *testing.T) {
	log := eventlog.New(50)
	store, _ := dlstats.New("")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go dlstats.Consume(ctx, log, store)
	time.Sleep(20 * time.Millisecond) // let it subscribe

	log.Record(eventlog.PackageEvent{Ecosystem: "npm", Package: "lodash@4.17.21", Action: "allow", Kind: eventlog.KindDownloaded})
	log.Record(eventlog.PackageEvent{Ecosystem: "npm", Package: "lodash@4.17.21", Action: "allow", Kind: eventlog.KindDownloaded})
	log.Record(eventlog.PackageEvent{Ecosystem: "npm", Package: "left-pad@1.3.0", Action: "allow", Kind: eventlog.KindScanned}) // ignored

	require.Eventually(t, func() bool {
		st, ok := store.Get("npm", "lodash", "4.17.21")
		return ok && st.Count == 2
	}, time.Second, 10*time.Millisecond)

	_, ok := store.Get("npm", "left-pad", "1.3.0")
	require.False(t, ok) // scanned events are not counted
}
