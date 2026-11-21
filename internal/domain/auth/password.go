package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// MaxPasswordBytes is the longest password accepted, in bytes. It only
// bounds the work a request can cause; it is far above any real password.
const MaxPasswordBytes = 1024

// DefaultMinPasswordBytes is the proposed default minimum password length in
// bytes (ADR-0026; not an operator decision yet).
const DefaultMinPasswordBytes = 12

// PasswordPolicy is the rule a new password must meet: a length in bytes and
// nothing else. There are no composition rules (requirements: "minimum length
// in bytes without composition rules").
type PasswordPolicy struct {
	MinBytes int
}

// ErrPasswordPolicy is wrapped by the error of a password that breaks the
// policy. Its message never repeats the password.
var ErrPasswordPolicy = errors.New("password does not meet the policy")

// PolicyError says why a new password was refused. Message is safe to show.
type PolicyError struct{ Message string }

func (e *PolicyError) Error() string { return e.Message }

// Unwrap makes errors.Is(err, ErrPasswordPolicy) work.
func (e *PolicyError) Unwrap() error { return ErrPasswordPolicy }

// Check returns a *PolicyError when pw is shorter than MinBytes or longer
// than [MaxPasswordBytes] bytes. Length is counted in bytes, as stored, so a
// password of multi-byte characters needs fewer characters.
func (p PasswordPolicy) Check(pw string) error {
	switch {
	case len(pw) < p.MinBytes:
		return &PolicyError{Message: fmt.Sprintf("password must be at least %d bytes long", p.MinBytes)}
	case len(pw) > MaxPasswordBytes:
		return &PolicyError{Message: fmt.Sprintf("password must be at most %d bytes long", MaxPasswordBytes)}
	}
	return nil
}

// Params are Argon2id parameters. They are encoded in every hash (PHC string
// format), so changing them later only affects new hashes; a hash made with
// other parameters is replaced at the next successful sign-in.
type Params struct {
	MemoryKiB uint32
	Time      uint32
	Threads   uint8
	SaltLen   uint32
	KeyLen    uint32
}

// DefaultParams are the parameters of new hashes (ADR-0026): the second
// recommended option of RFC 9106 section 4 (t=3, p=4, m=64 MiB), with a
// 128-bit salt and a 256-bit tag.
var DefaultParams = Params{MemoryKiB: 64 * 1024, Time: 3, Threads: 4, SaltLen: 16, KeyLen: 32}

// Hasher hashes and verifies passwords. [Argon2id] is the implementation;
// tests may count calls through their own.
type Hasher interface {
	// Hash returns the encoded hash of pw.
	Hash(ctx context.Context, pw string) (string, error)
	// Verify reports whether pw matches encoded, and whether encoded should be
	// replaced because it was made with other parameters. An encoded hash
	// that cannot be parsed is an error, never a match.
	Verify(ctx context.Context, encoded, pw string) (ok, rehash bool, err error)
}

// Argon2id is the Argon2id [Hasher]. At most a fixed number of hashes run at
// once, so concurrent sign-ins cannot exhaust memory (each takes MemoryKiB).
type Argon2id struct {
	params Params
	slots  chan struct{}
}

// NewArgon2id returns a hasher with params and at most concurrent hashes at
// once (at least 1).
func NewArgon2id(params Params, concurrent int) *Argon2id {
	if concurrent < 1 {
		concurrent = 1
	}
	return &Argon2id{params: params, slots: make(chan struct{}, concurrent)}
}

func (a *Argon2id) acquire(ctx context.Context) error {
	select {
	case a.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *Argon2id) release() { <-a.slots }

var b64 = base64.RawStdEncoding

// Hash returns "$argon2id$v=19$m=<KiB>,t=<time>,p=<threads>$<salt>$<tag>".
func (a *Argon2id) Hash(ctx context.Context, pw string) (string, error) {
	salt := make([]byte, a.params.SaltLen)
	_, _ = rand.Read(salt) // crypto/rand.Read never fails (Go 1.24+)
	if err := a.acquire(ctx); err != nil {
		return "", err
	}
	defer a.release()
	key := argon2.IDKey([]byte(pw), salt, a.params.Time, a.params.MemoryKiB, a.params.Threads, a.params.KeyLen)
	return encode(a.params, salt, key), nil
}

func encode(p Params, salt, key []byte) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.MemoryKiB, p.Time, p.Threads, b64.EncodeToString(salt), b64.EncodeToString(key))
}

// ErrMalformedHash means a stored hash is not an Argon2id PHC string this
// package can verify.
var ErrMalformedHash = errors.New("auth: malformed password hash")

// Upper bounds on parameters read from a stored hash, so a corrupted or
// planted row cannot make a sign-in allocate unbounded memory or time.
const (
	maxMemoryKiB = 4 * 1024 * 1024
	maxTime      = 64
)

func decode(encoded string) (Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return Params{}, nil, nil, ErrMalformedHash
	}
	var p Params
	var threads uint32
	if n, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.MemoryKiB, &p.Time, &threads); err != nil || n != 3 {
		return Params{}, nil, nil, ErrMalformedHash
	}
	if p.MemoryKiB == 0 || p.MemoryKiB > maxMemoryKiB || p.Time == 0 || p.Time > maxTime || threads == 0 || threads > 255 {
		return Params{}, nil, nil, ErrMalformedHash
	}
	p.Threads = uint8(threads)
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return Params{}, nil, nil, ErrMalformedHash
	}
	key, err := b64.DecodeString(parts[5])
	if err != nil || len(key) < 16 || len(key) > 64 {
		return Params{}, nil, nil, ErrMalformedHash
	}
	p.SaltLen, p.KeyLen = uint32(len(salt)), uint32(len(key))
	if canonical := encode(p, salt, key); canonical != encoded {
		return Params{}, nil, nil, ErrMalformedHash
	}
	return p, salt, key, nil
}

// Verify recomputes the tag with the hash's own parameters and compares in
// constant time.
func (a *Argon2id) Verify(ctx context.Context, encoded, pw string) (bool, bool, error) {
	p, salt, key, err := decode(encoded)
	if err != nil {
		return false, false, err
	}
	if err := a.acquire(ctx); err != nil {
		return false, false, err
	}
	defer a.release()
	got := argon2.IDKey([]byte(pw), salt, p.Time, p.MemoryKiB, p.Threads, p.KeyLen)
	ok := subtle.ConstantTimeCompare(got, key) == 1
	return ok, ok && p != a.params, nil
}

// dummyHash is verified when no real hash exists (unknown account, no
// password set, disabled account checked first), so every failed sign-in
// costs one Argon2id computation with the current parameters.
type dummyHash struct {
	once sync.Once
	hash string
	err  error
}

func (d *dummyHash) get(ctx context.Context, h Hasher) (string, error) {
	d.once.Do(func() {
		// The password is random and thrown away; nothing can match it.
		buf := make([]byte, 32)
		_, _ = rand.Read(buf)
		d.hash, d.err = h.Hash(context.WithoutCancel(ctx), b64.EncodeToString(buf))
	})
	return d.hash, d.err
}
