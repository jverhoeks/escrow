package server

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// ipRateLimiter is a sliding-window per-IP rate limiter.
type ipRateLimiter struct {
	mu      sync.Mutex
	windows map[string][]time.Time
	limit   int
	window  time.Duration
	done    chan struct{}
}

func newIPRateLimiter(limitPerMin int) *ipRateLimiter {
	rl := &ipRateLimiter{
		windows: make(map[string][]time.Time),
		limit:   limitPerMin,
		window:  time.Minute,
		done:    make(chan struct{}),
	}
	go rl.cleanup()
	return rl
}

// stop signals the background cleanup goroutine to exit.
func (rl *ipRateLimiter) stop() {
	close(rl.done)
}

func (rl *ipRateLimiter) allow(r *http.Request) bool {
	ip := remoteIP(r)
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
func (rl *ipRateLimiter) cleanup() {
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

func (rl *ipRateLimiter) middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !rl.allow(r) {
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// remoteIP returns the client IP. It uses r.RemoteAddr by default.
// X-Forwarded-For is not trusted because it can be spoofed by clients —
// only use it if the request arrives from a trusted reverse proxy (not implemented here).
func remoteIP(r *http.Request) string {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}
