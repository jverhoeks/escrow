package egress

import (
	"sync"
	"time"
)

// rateLimiter is a self-contained per-IP sliding-window limiter for egress
// proxy traffic. It mirrors the approach of internal/server's ipRateLimiter but
// is kept inside the egress package (that one is unexported package-private), so
// egress has no dependency on internal/server.
type rateLimiter struct {
	mu      sync.Mutex
	windows map[string][]time.Time
	limit   int
	window  time.Duration
	done    chan struct{}
}

func newRateLimiter(limitPerMin int) *rateLimiter {
	rl := &rateLimiter{
		windows: make(map[string][]time.Time),
		limit:   limitPerMin,
		window:  time.Minute,
		done:    make(chan struct{}),
	}
	go rl.cleanup()
	return rl
}

// stop signals the background cleanup goroutine to exit. Safe to call once.
func (rl *rateLimiter) stop() {
	close(rl.done)
}

// allow records a request from ip and reports whether it is within the limit.
func (rl *rateLimiter) allow(ip string) bool {
	now := time.Now()
	cutoff := now.Add(-rl.window)

	rl.mu.Lock()
	defer rl.mu.Unlock()

	times := rl.windows[ip]
	j := 0
	for _, t := range times {
		if t.After(cutoff) {
			times[j] = t
			j++
		}
	}
	times = times[:j]
	if len(times) >= rl.limit {
		rl.windows[ip] = times
		return false
	}
	rl.windows[ip] = append(times, now)
	return true
}

// cleanup removes stale IP entries every minute to bound memory use.
func (rl *rateLimiter) cleanup() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			cutoff := time.Now().Add(-rl.window)
			rl.mu.Lock()
			for ip, times := range rl.windows {
				j := 0
				for _, t := range times {
					if t.After(cutoff) {
						times[j] = t
						j++
					}
				}
				if j == 0 {
					delete(rl.windows, ip)
				} else {
					rl.windows[ip] = times[:j]
				}
			}
			rl.mu.Unlock()
		case <-rl.done:
			return
		}
	}
}
