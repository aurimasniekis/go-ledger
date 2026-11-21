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
// memory: a restart clears it (ADR-0026).
//
// An attempt is reserved before the password is checked ([Limiter.Begin])
// and counts against the limit while it is in flight; it then becomes a
// failure ([Limiter.Confirm]) or is dropped ([Limiter.Release]). So requests
// sent at the same time cannot all pass the check before any of them has
// failed: at most Max attempts per key are ever checked within a window.
//
// It holds at most maxKeys keys; when full, keys with nothing counted are
// dropped first, then the key whose newest failure is oldest among the keys
// that are not blocked, and only then a blocked one. Keys with attempts in
// flight are never dropped. Memory stays bounded whatever the attacker sends.
type Limiter struct {
	mu      sync.Mutex
	limit   Limit
	now     func() time.Time
	maxKeys int
	keys    map[string]*attempts
}

type attempts struct {
	failures []time.Time // oldest first, at most limit.Max
	pending  int         // attempts reserved and not yet confirmed or released
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

// Begin reserves an attempt for key, unless the failures in the window and
// the attempts in flight already reach the limit. Then it reserves nothing
// and reports blocked: retryAfter is when the oldest counted failure leaves
// the window (a second when only attempts in flight block it), and first is
// true only for the first refusal since the key's failures reached the limit,
// so a block is audited once per window rather than once per attempt. A
// refusal caused only by attempts in flight is never first.
//
// Every reservation must end with exactly one [Limiter.Confirm] or
// [Limiter.Release].
func (l *Limiter) Begin(key string) (blocked bool, retryAfter time.Duration, first bool) {
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
	if len(a.failures)+a.pending < l.limit.Max {
		a.pending++
		return false, 0, false
	}
	if len(a.failures) < l.limit.Max {
		return true, time.Second, false
	}
	retryAfter = max(l.limit.Window-now.Sub(a.failures[0]), time.Second)
	first = !a.reported
	a.reported = true
	return true, retryAfter, first
}

// Confirm turns a reservation of key into a failed attempt.
func (l *Limiter) Confirm(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	a, ok := l.keys[key]
	if !ok { // dropped by Reset meanwhile: still count the failure
		l.makeRoom(now)
		a = &attempts{}
		l.keys[key] = a
	} else if a.pending > 0 {
		a.pending--
	}
	l.prune(a, now)
	a.failures = append(a.failures, now)
	if len(a.failures) > l.limit.Max {
		a.failures = append(a.failures[:0], a.failures[len(a.failures)-l.limit.Max:]...)
	}
}

// Release drops a reservation of key without counting it (the attempt
// succeeded or was never checked).
func (l *Limiter) Release(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.keys[key]
	if !ok {
		return
	}
	if a.pending > 0 {
		a.pending--
	}
	if a.pending == 0 && len(a.failures) == 0 {
		delete(l.keys, key)
	}
}

// Reset forgets the failures of key (after a successful sign-in to that
// account). Attempts still in flight stay reserved.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.keys[key]
	if !ok {
		return
	}
	if a.pending == 0 {
		delete(l.keys, key)
		return
	}
	a.failures, a.reported = nil, false
}

func (l *Limiter) makeRoom(now time.Time) {
	if len(l.keys) < l.maxKeys {
		return
	}
	for k, a := range l.keys {
		l.prune(a, now)
		if len(a.failures) == 0 && a.pending == 0 {
			delete(l.keys, k)
		}
	}
	for len(l.keys) >= l.maxKeys {
		// The stalest key that is not blocked, else the stalest blocked one.
		// Keys with attempts in flight stay.
		var victim string
		var victimBlocked bool
		var at time.Time
		for k, a := range l.keys {
			if a.pending > 0 || len(a.failures) == 0 {
				continue
			}
			blocked := len(a.failures) >= l.limit.Max
			last := a.failures[len(a.failures)-1]
			if victim == "" || (victimBlocked && !blocked) || (blocked == victimBlocked && last.Before(at)) {
				victim, victimBlocked, at = k, blocked, last
			}
		}
		if victim == "" {
			return // every key has an attempt in flight: allow more keys for now
		}
		delete(l.keys, victim)
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
