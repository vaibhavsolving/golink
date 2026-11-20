// Package ratelimit implements a per-key token bucket rate limiter.
package ratelimit

import (
	"sync"
	"time"
)

type bucket struct {
	tokens float64
	last   time.Time
}

// Limiter allows `burst` requests at once per key, refilled at `rate` per second.
type Limiter struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	buckets map[string]*bucket
	now     func() time.Time
}

// New returns a limiter. rate is tokens per second, burst is bucket size.
func New(rate float64, burst int) *Limiter {
	return &Limiter{
		rate:    rate,
		burst:   float64(burst),
		buckets: make(map[string]*bucket),
		now:     time.Now,
	}
}

// Allow reports whether one request for key may proceed now.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Cleanup drops buckets idle for longer than maxIdle, so memory stays bounded.
func (l *Limiter) Cleanup(maxIdle time.Duration) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	n := 0
	for k, b := range l.buckets {
		if now.Sub(b.last) > maxIdle {
			delete(l.buckets, k)
			n++
		}
	}
	return n
}
