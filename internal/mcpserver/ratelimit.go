package mcpserver

import (
	"sync"
	"time"
)

// Limiter is a per-key token bucket held in memory: rate tokens per minute
// refill continuously up to burst. Keys are API token identifiers.
type Limiter struct {
	mu      sync.Mutex
	rate    float64 // tokens per second
	burst   float64
	now     func() time.Time
	buckets map[string]*bucket
	pruned  time.Time
}

type bucket struct {
	tokens float64
	at     time.Time
}

// NewLimiter returns a limiter allowing perMinute requests per minute per key
// with bursts of up to burst. now defaults to time.Now.
func NewLimiter(perMinute, burst int, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{rate: float64(perMinute) / 60, burst: float64(burst), now: now, buckets: map[string]*bucket{}}
}

// Allow takes one token for key. When none is left it returns false and how
// long until one is.
func (l *Limiter) Allow(key string) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.prune(now)
	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: l.burst, at: now}
		l.buckets[key] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.at).Seconds()*l.rate)
	b.at = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
}

// prune drops buckets that have refilled completely, so the map stays as
// small as the set of recently active tokens. It runs at most once a minute.
func (l *Limiter) prune(now time.Time) {
	if now.Sub(l.pruned) < time.Minute {
		return
	}
	l.pruned = now
	for k, b := range l.buckets {
		if b.tokens+now.Sub(b.at).Seconds()*l.rate >= l.burst {
			delete(l.buckets, k)
		}
	}
}
