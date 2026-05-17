package eventlog_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/jverhoeks/escrow/internal/eventlog"
)

func TestLog_SubscriberCap(t *testing.T) {
	l := eventlog.New(10)

	var unsubs []func()
	// Fill up to the cap
	for i := 0; i < 100; i++ {
		ch, unsub := l.Subscribe()
		require.NotNil(t, ch, "subscriber %d should be accepted", i)
		require.NotNil(t, unsub)
		unsubs = append(unsubs, unsub)
	}

	// The next subscription should be rejected
	ch, unsub := l.Subscribe()
	assert.Nil(t, ch, "should reject subscription when at cap")
	assert.Nil(t, unsub, "unsub should be nil when rejected")

	// After one unsubscribes, a new one should be accepted
	unsubs[0]()
	unsubs = unsubs[1:]

	ch2, unsub2 := l.Subscribe()
	assert.NotNil(t, ch2, "should accept new subscriber after one unsubscribed")
	assert.NotNil(t, unsub2)

	// Clean up
	unsub2()
	for _, u := range unsubs {
		u()
	}
}

func TestLog_SubscribeAfterUnsubscribeAccepted(t *testing.T) {
	l := eventlog.New(10)
	ch, unsub := l.Subscribe()
	require.NotNil(t, ch)
	// Unsubscribe releases the slot
	unsub()
	// Now a new subscriber should be accepted (slot freed)
	ch2, unsub2 := l.Subscribe()
	assert.NotNil(t, ch2, "slot should be available after unsubscribe")
	if unsub2 != nil {
		unsub2()
	}
}

func TestLog_SubscriberCapDoesNotBlockRecord(t *testing.T) {
	l := eventlog.New(10)

	// Fill subscribers to the cap
	var unsubs []func()
	for i := 0; i < 100; i++ {
		_, unsub := l.Subscribe()
		unsubs = append(unsubs, unsub)
	}
	defer func() {
		for _, u := range unsubs {
			u()
		}
	}()

	// Record should still work even at cap
	l.Record(eventlog.PackageEvent{Package: "test@1.0", Action: "block"})
	assert.Len(t, l.Events(""), 1, "Record should work even when subscriber cap is reached")
}
