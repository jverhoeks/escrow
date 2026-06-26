package dashboard

import (
	"net"
	"net/http"
	"sync"
	"time"
)

const maxLoginFailures = 10
const lockoutDuration = 15 * time.Minute

const loginSweepInterval = time.Minute

type loginRateLimiter struct {
	mu        sync.Mutex
	counts    map[string]int
	lockout   map[string]time.Time
	seen      map[string]time.Time // last-failure time per IP, for aging counts
	lastSweep time.Time
}

func newLoginRateLimiter() *loginRateLimiter {
	return &loginRateLimiter{
		counts:  make(map[string]int),
		lockout: make(map[string]time.Time),
		seen:    make(map[string]time.Time),
	}
}

// sweepLocked drops expired lockouts and stale failure counts. It is throttled
// to once per loginSweepInterval so a distributed login flood can't make every
// attempt pay a full-map scan, while still bounding the maps. Caller holds mu.
// See #50.
func (l *loginRateLimiter) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < loginSweepInterval {
		return
	}
	l.lastSweep = now
	for ip, until := range l.lockout {
		if now.After(until) {
			delete(l.lockout, ip)
		}
	}
	for ip, last := range l.seen {
		if now.Sub(last) >= lockoutDuration {
			delete(l.counts, ip)
			delete(l.seen, ip)
		}
	}
}

func (l *loginRateLimiter) isLockedOut(r *http.Request) bool {
	ip := clientIP(r)
	l.mu.Lock()
	defer l.mu.Unlock()
	t, ok := l.lockout[ip]
	return ok && time.Now().Before(t)
}

func (l *loginRateLimiter) recordFailure(r *http.Request) {
	ip := clientIP(r)
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(now)
	l.counts[ip]++
	l.seen[ip] = now
	if l.counts[ip] >= maxLoginFailures {
		l.lockout[ip] = now.Add(lockoutDuration)
		delete(l.counts, ip)
		delete(l.seen, ip)
	}
}

func (l *loginRateLimiter) recordSuccess(r *http.Request) {
	ip := clientIP(r)
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.counts, ip)
	delete(l.lockout, ip)
	delete(l.seen, ip)
}

func clientIP(r *http.Request) string {
	// Use r.RemoteAddr directly; X-Forwarded-For is not trusted
	// because clients can spoof it, defeating the rate limiter.
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}
