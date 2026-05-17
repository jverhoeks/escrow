package dashboard

import (
	"net"
	"net/http"
	"sync"
	"time"
)

const maxLoginFailures = 10
const lockoutDuration = 15 * time.Minute

type loginRateLimiter struct {
	mu      sync.Mutex
	counts  map[string]int
	lockout map[string]time.Time
}

func newLoginRateLimiter() *loginRateLimiter {
	return &loginRateLimiter{
		counts:  make(map[string]int),
		lockout: make(map[string]time.Time),
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
	l.mu.Lock()
	defer l.mu.Unlock()
	l.counts[ip]++
	if l.counts[ip] >= maxLoginFailures {
		l.lockout[ip] = time.Now().Add(lockoutDuration)
		delete(l.counts, ip)
	}
}

func (l *loginRateLimiter) recordSuccess(r *http.Request) {
	ip := clientIP(r)
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.counts, ip)
	delete(l.lockout, ip)
}

func clientIP(r *http.Request) string {
	// Use r.RemoteAddr directly; X-Forwarded-For is not trusted
	// because clients can spoof it, defeating the rate limiter.
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}
