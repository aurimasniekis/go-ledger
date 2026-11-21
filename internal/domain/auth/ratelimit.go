package auth

import (
	"net/netip"
	"sync"
	"time"
)

// Limit is a sign-in rate limit: at most Max failed attempts per key within
// Window. Proposed defaults are in ADR-0026.
type Limit struct {
	Max    int
	Window time.Duration
}

// Limiter counts failed attempts per key in a sliding window. It is kept in
// memory: a restart clears it (ADR-0026). It holds at most maxKeys keys; when
// full, expired keys are dropped first, then the key whose newest failure is
// oldest, so memory stays bounded whatever the attacker sends.
type Limiter struct {
	mu      sync.Mutex
	limit   Limit
	now     func() time.Time
	maxKeys int
	keys    map[string]*attempts
}

type attempts struct {
	failures []time.Time // oldest first, at most limit.Max
	reported bool        // the block of this window was already reported
}

// DefaultLimiterKeys is how many keys a limiter tracks at most.
const DefaultLimiterKeys = 10000

// NewLimiter returns a limiter. A Max below 1 disables nothing: it is
// treated as 1.
func NewLimiter(l Limit, maxKeys int, now func() time.Time) *Limiter {
	if l.Max < 1 {
		l.Max = 1
	}
	if maxKeys < 1 {
		maxKeys = DefaultLimiterKeys
	}
	if now == nil {
		now = time.Now
	}
	return &Limiter{limit: l, now: now, maxKeys: maxKeys, keys: map[string]*attempts{}}
}

func (l *Limiter) prune(a *attempts, now time.Time) {
	i := 0
	for i < len(a.failures) && now.Sub(a.failures[i]) >= l.limit.Window {
		i++
	}
	if i > 0 {
		a.failures = append(a.failures[:0], a.failures[i:]...)
		a.reported = false
	}
}

// Blocked reports whether key has used up its failures. retryAfter is when
// the oldest counted failure leaves the window. first is true only for the
// first refusal since the key became blocked, so a block is audited once per
// window rather than once per attempt.
func (l *Limiter) Blocked(key string) (blocked bool, retryAfter time.Duration, first bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.keys[key]
	if !ok {
		return false, 0, false
	}
	now := l.now()
	l.prune(a, now)
	if len(a.failures) < l.limit.Max {
		return false, 0, false
	}
	retryAfter = l.limit.Window - now.Sub(a.failures[0])
	if retryAfter < time.Second {
		retryAfter = time.Second
	}
	first = !a.reported
	a.reported = true
	return true, retryAfter, first
}

// Fail records a failed attempt for key.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	a, ok := l.keys[key]
	if !ok {
		l.makeRoom(now)
		a = &attempts{}
		l.keys[key] = a
	}
	l.prune(a, now)
	a.failures = append(a.failures, now)
	if len(a.failures) > l.limit.Max {
		a.failures = append(a.failures[:0], a.failures[len(a.failures)-l.limit.Max:]...)
	}
}

// Reset forgets key (after a successful sign-in to that account).
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.keys, key)
}

func (l *Limiter) makeRoom(now time.Time) {
	if len(l.keys) < l.maxKeys {
		return
	}
	for k, a := range l.keys {
		l.prune(a, now)
		if len(a.failures) == 0 {
			delete(l.keys, k)
		}
	}
	for len(l.keys) >= l.maxKeys {
		var oldest string
		var at time.Time
		for k, a := range l.keys {
			last := a.failures[len(a.failures)-1]
			if oldest == "" || last.Before(at) {
				oldest, at = k, last
			}
		}
		delete(l.keys, oldest)
	}
}

// AddressKey is the rate-limit key of a client address: the address itself
// for IPv4, its /64 network for IPv6 (one host usually holds a whole /64).
// Anything unparsable is used as given.
func AddressKey(addr string) string {
	a, err := netip.ParseAddr(addr)
	if err != nil {
		return addr
	}
	a = a.Unmap()
	if a.Is4() {
		return a.String()
	}
	p, _ := a.WithZone("").Prefix(64)
	return p.String()
}
