package rescan

import (
	"context"
	"testing"
	"time"
)

// #69: sleepCtx returns false immediately when the context is already
// cancelled (and stops its timer rather than leaking it).
func TestSleepCtx(t *testing.T) {
	if !sleepCtx(context.Background(), time.Millisecond) {
		t.Error("expected true when the duration elapses")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if sleepCtx(ctx, time.Hour) {
		t.Error("expected false when ctx is cancelled")
	}
	if time.Since(start) > time.Second {
		t.Error("sleepCtx did not return promptly on cancel")
	}
}
