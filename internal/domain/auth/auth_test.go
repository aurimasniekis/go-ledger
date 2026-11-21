package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
)

var cheap = Params{MemoryKiB: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}

func TestPasswordPolicyCountsBytesOnly(t *testing.T) {
	p := PasswordPolicy{MinBytes: 12}
	for pw, ok := range map[string]bool{
		"short":                                 false,
		"elevenbytes":                           false,
		"twelve bytes":                          true,
		"            ":                          true, // no composition rules
		"ąčęėįš":                                true, // 6 letters, 12 bytes
		"ąčęėį":                                 false,
		strings.Repeat("a", MaxPasswordBytes):   true,
		strings.Repeat("a", MaxPasswordBytes+1): false,
	} {
		err := p.Check(pw)
		if (err == nil) != ok {
			t.Errorf("%q (%d bytes): %v", pw, len(pw), err)
		}
		if err != nil && (!errors.Is(err, ErrPasswordPolicy) || strings.Contains(err.Error(), pw)) {
			t.Errorf("%q: error %v", pw, err)
		}
	}
}

func TestArgon2idHashAndVerify(t *testing.T) {
	ctx := context.Background()
	h := NewArgon2id(cheap, 2)
	enc, err := h.Hash(ctx, "a long enough password")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Fatalf("encoding %s", enc)
	}
	if enc2, _ := h.Hash(ctx, "a long enough password"); enc2 == enc {
		t.Error("two hashes of one password are equal: no salt")
	}
	if ok, rehash, err := h.Verify(ctx, enc, "a long enough password"); !ok || rehash || err != nil {
		t.Errorf("verify: %v %v %v", ok, rehash, err)
	}
	if ok, _, err := h.Verify(ctx, enc, "a long enough passworD"); ok || err != nil {
		t.Errorf("wrong password: %v %v", ok, err)
	}
	// A hash with other parameters still verifies and asks to be replaced.
	stronger := NewArgon2id(Params{MemoryKiB: 128, Time: 2, Threads: 1, SaltLen: 16, KeyLen: 32}, 1)
	if ok, rehash, err := stronger.Verify(ctx, enc, "a long enough password"); !ok || !rehash || err != nil {
		t.Errorf("old parameters: %v %v %v", ok, rehash, err)
	}
	for _, bad := range []string{
		"", "plain", "$argon2i$v=19$m=64,t=1,p=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=16$m=64,t=1,p=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=99999999,t=1,p=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=64,t=0,p=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=64,t=1,p=1$!!$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=064,t=1,p=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA",
		enc + "$x",
		strings.TrimSuffix(enc, enc[len(enc)-1:]) + "=", // padded base64
	} {
		if ok, _, err := h.Verify(ctx, bad, "x"); ok || !errors.Is(err, ErrMalformedHash) {
			t.Errorf("%q: %v %v", bad, ok, err)
		}
	}
	// The default parameters are RFC 9106's second recommended option.
	if DefaultParams != (Params{MemoryKiB: 65536, Time: 3, Threads: 4, SaltLen: 16, KeyLen: 32}) {
		t.Errorf("default parameters changed without an ADR: %+v", DefaultParams)
	}
}

func TestArgon2idBoundsConcurrency(t *testing.T) {
	h := NewArgon2id(cheap, 1)
	h.slots <- struct{}{} // the only slot is taken
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := h.Hash(ctx, "a long enough password"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hash with no free slot: %v", err)
	}
	<-h.slots
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := h.Hash(context.Background(), "a long enough password"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}

func TestLimiter(t *testing.T) {
	now := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	l := NewLimiter(Limit{Max: 3, Window: 10 * time.Minute}, 100, func() time.Time { return now })
	for i := range 3 {
		if b, _, _ := l.Blocked("k"); b {
			t.Fatalf("blocked after %d failures", i)
		}
		l.Fail("k")
		now = now.Add(time.Minute)
	}
	b, retry, first := l.Blocked("k")
	if !b || !first || retry != 7*time.Minute {
		t.Fatalf("blocked=%v retry=%s first=%v", b, retry, first)
	}
	if b, _, first := l.Blocked("k"); !b || first {
		t.Error("the second refusal is reported as first")
	}
	if b, _, _ := l.Blocked("other"); b {
		t.Error("other key blocked")
	}
	now = now.Add(7 * time.Minute) // the oldest failure leaves the window
	if b, _, _ := l.Blocked("k"); b {
		t.Error("still blocked after the window slid")
	}
	l.Fail("k")
	if b, _, first := l.Blocked("k"); !b || !first {
		t.Error("a new block is reported again")
	}
	l.Reset("k")
	if b, _, _ := l.Blocked("k"); b {
		t.Error("blocked after reset")
	}
}

func TestLimiterMemoryIsBounded(t *testing.T) {
	now := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	l := NewLimiter(Limit{Max: 2, Window: time.Hour}, 10, func() time.Time { return now })
	for i := range 1000 {
		l.Fail(fmt.Sprintf("k%d", i))
		now = now.Add(time.Second)
	}
	if len(l.keys) > 10 {
		t.Fatalf("%d keys kept", len(l.keys))
	}
	if _, ok := l.keys["k999"]; !ok {
		t.Error("the newest key was evicted")
	}
}

func TestAddressKey(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.7":          "203.0.113.7",
		"::ffff:203.0.113.7":   "203.0.113.7",
		"2001:db8:1:2::5":      "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff::1": "2001:db8:1:2::/64",
		"fe80::1%eth0":         "fe80::/64",
		"not an address":       "not an address",
	} {
		if got := AddressKey(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

func TestCSRFToken(t *testing.T) {
	a, b := newSecret(), newSecret()
	if a == b || len(a) != 43 {
		t.Fatalf("secrets %q %q", a, b)
	}
	ta := CSRFToken(a)
	if ta == CSRFToken(b) || ta == a || !VerifyCSRF(a, ta) {
		t.Fatal("CSRF token")
	}
	for _, c := range [][2]string{{a, ""}, {a, CSRFToken(b)}, {"", ta}, {a, ta + "x"}, {a, strings.Repeat("x", MaxTokenLength+1)}} {
		if VerifyCSRF(c[0], c[1]) {
			t.Errorf("accepted %q for %q", c[1], c[0])
		}
	}
	if hashSecret(a) == a || len(hashSecret(a)) != 64 {
		t.Error("hash")
	}
}

func TestRecent(t *testing.T) {
	now := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	s := &Service{cfg: DefaultConfig(), now: func() time.Time { return now }}
	user := authz.UserPrincipal("U", authz.RoleAdministrator)
	user.SessionID = "S"
	for name, c := range map[string]struct {
		p    func() authz.Principal
		want bool
	}{
		"just signed in":      {func() authz.Principal { p := user; p.AuthTime = now; return p }, true},
		"9m59s ago":           {func() authz.Principal { p := user; p.AuthTime = now.Add(-10*time.Minute + time.Second); return p }, true},
		"10m ago":             {func() authz.Principal { p := user; p.AuthTime = now.Add(-10 * time.Minute); return p }, false},
		"far future":          {func() authz.Principal { p := user; p.AuthTime = now.Add(time.Hour); return p }, false},
		"no session":          {func() authz.Principal { p := user; p.SessionID = ""; p.AuthTime = now; return p }, false},
		"token":               {func() authz.Principal { return authz.TokenPrincipal("T", "U", authz.RoleAdministrator, nil) }, false},
		"never authenticated": {func() authz.Principal { return user }, false},
	} {
		if got := s.Recent(c.p()); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

func TestConfigValidate(t *testing.T) {
	if err := DefaultConfig().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*Config){
		"min 0":            func(c *Config) { c.Policy.MinBytes = 0 },
		"idle 0":           func(c *Config) { c.IdleTimeout = 0 },
		"idle > absolute":  func(c *Config) { c.IdleTimeout = c.AbsoluteTimeout + 1 },
		"no account limit": func(c *Config) { c.AccountLimit.Max = 0 },
		"no window":        func(c *Config) { c.AddressLimit.Window = 0 },
	} {
		c := DefaultConfig()
		mod(&c)
		if c.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestClean(t *testing.T) {
	if got := clean("Mozilla/5.0\n\x00(X11) \xff ok", 256); got != "Mozilla/5.0(X11)  ok" {
		t.Errorf("%q", got)
	}
	if got := clean(strings.Repeat("ą", 200), 7); got != "ąąą" {
		t.Errorf("%q", got)
	}
}
