package dashboard

import (
	"testing"
	"time"
)

// The login rate-limiter must evict expired lockouts and stale failure counts so
// a distributed login flood can't grow its maps without bound, while preserving
// still-active entries. See #50.
func TestLoginRateLimiter_SweepEvictsStaleKeepsFresh(t *testing.T) {
	l := newLoginRateLimiter()
	now := time.Now()

	l.lockout["1.1.1.1"] = now.Add(-time.Minute)      // expired lockout → evict
	l.counts["2.2.2.2"] = 3                           // stale count → evict
	l.seen["2.2.2.2"] = now.Add(-2 * lockoutDuration) //
	l.lockout["3.3.3.3"] = now.Add(time.Hour)         // active lockout → keep
	l.counts["4.4.4.4"] = 2                           // fresh count → keep
	l.seen["4.4.4.4"] = now                           //
	l.lastSweep = now.Add(-2 * loginSweepInterval)    // force the throttled sweep

	l.mu.Lock()
	l.sweepLocked(now)
	l.mu.Unlock()

	if _, ok := l.lockout["1.1.1.1"]; ok {
		t.Error("expired lockout was not evicted")
	}
	if _, ok := l.counts["2.2.2.2"]; ok {
		t.Error("stale failure count was not evicted")
	}
	if _, ok := l.lockout["3.3.3.3"]; !ok {
		t.Error("active lockout was wrongly evicted")
	}
	if _, ok := l.counts["4.4.4.4"]; !ok {
		t.Error("fresh failure count was wrongly evicted")
	}
}

// The sweep is throttled: a recent lastSweep means no scan this call.
func TestLoginRateLimiter_SweepThrottled(t *testing.T) {
	l := newLoginRateLimiter()
	now := time.Now()
	l.lockout["1.1.1.1"] = now.Add(-time.Minute) // expired but...
	l.lastSweep = now                            // ...just swept → skip
	l.mu.Lock()
	l.sweepLocked(now.Add(time.Second))
	l.mu.Unlock()
	if _, ok := l.lockout["1.1.1.1"]; !ok {
		t.Error("sweep ran despite throttle window")
	}
}
