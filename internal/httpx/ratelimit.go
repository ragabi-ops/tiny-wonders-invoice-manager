package httpx

import (
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// RateLimiter is a keyed token-bucket limiter kept in memory. A single-instance
// deployment needs nothing more; a shared store would be new infrastructure
// without a demonstrated need (plan.md 22.9).
type RateLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	rate     rate.Limit
	burst    int
	idleFor  time.Duration
	lastGC   time.Time
	nowFunc  func() time.Time
	gcPeriod time.Duration
}

type bucket struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// NewRateLimiter allows `perMinute` requests per key with the given burst.
func NewRateLimiter(perMinute int, burst int) *RateLimiter {
	return &RateLimiter{
		buckets:  make(map[string]*bucket),
		rate:     rate.Limit(float64(perMinute) / 60.0),
		burst:    burst,
		idleFor:  10 * time.Minute,
		gcPeriod: time.Minute,
		nowFunc:  time.Now,
	}
}

// Allow reports whether the key may proceed and consumes a token if so.
func (l *RateLimiter) Allow(key string) bool {
	now := l.nowFunc()

	l.mu.Lock()
	defer l.mu.Unlock()

	l.collectLocked(now)

	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{limiter: rate.NewLimiter(l.rate, l.burst)}
		l.buckets[key] = b
	}
	b.lastSeen = now
	return b.limiter.Allow()
}

// Reset drops a key's bucket, used after a successful login so a legitimate
// user is not throttled by their own earlier typos.
func (l *RateLimiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, key)
}

// collectLocked evicts idle buckets so the map cannot grow without bound.
func (l *RateLimiter) collectLocked(now time.Time) {
	if now.Sub(l.lastGC) < l.gcPeriod {
		return
	}
	l.lastGC = now
	for key, b := range l.buckets {
		if now.Sub(b.lastSeen) > l.idleFor {
			delete(l.buckets, key)
		}
	}
}

// LimitByIP rejects requests from an IP that exceeds the limiter.
func LimitByIP(l *RateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.Allow(ClientIP(r)) {
				WriteError(w, r, ErrRateLimited)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
