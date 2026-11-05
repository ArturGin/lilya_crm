package httpapi

import (
	"sync"
	"time"
)

// rateLimiter is a fixed-window in-memory limiter. Good enough for a single
// instance; swap for Redis/Postgres if the app is scaled horizontally.
type rateLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	buckets map[string]*bucket
	lastGC  time.Time
}

type bucket struct {
	count int
	reset time.Time
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, buckets: map[string]*bucket{}, lastGC: time.Now()}
}

// Blocked reports whether any key has exhausted its budget.
func (l *rateLimiter) Blocked(keys ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for _, k := range keys {
		if b, ok := l.buckets[k]; ok && now.Before(b.reset) && b.count >= l.limit {
			return true
		}
	}
	return false
}

// Hit records one attempt against every key.
func (l *rateLimiter) Hit(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if now.Sub(l.lastGC) > l.window {
		for k, b := range l.buckets {
			if now.After(b.reset) {
				delete(l.buckets, k)
			}
		}
		l.lastGC = now
	}
	for _, k := range keys {
		b, found := l.buckets[k]
		if !found || now.After(b.reset) {
			b = &bucket{reset: now.Add(l.window)}
			l.buckets[k] = b
		}
		b.count++
	}
}

// Allow records a hit and reports whether the keys were within budget.
func (l *rateLimiter) Allow(keys ...string) bool {
	if l.Blocked(keys...) {
		return false
	}
	l.Hit(keys...)
	return true
}

// Reset clears a key (e.g. after a successful login).
func (l *rateLimiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, key)
}
