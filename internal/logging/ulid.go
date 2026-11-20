package logging

import (
	"crypto/rand"
	"time"
)

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewULID returns a new ULID: 48 bits of millisecond time then 80 random bits,
// as 26 Crockford base32 characters. It sorts by creation time to the
// millisecond.
func NewULID() string { return ulidAt(time.Now()) }

func ulidAt(t time.Time) string {
	var b [16]byte
	ms := uint64(t.UnixMilli())
	for i := 5; i >= 0; i-- {
		b[i] = byte(ms)
		ms >>= 8
	}
	_, _ = rand.Read(b[6:]) // never fails since Go 1.24
	var out [26]byte
	var acc uint32
	bits, n := 2, 0 // 128 bits are written as 130, so two leading zero bits
	for _, x := range b {
		acc = acc<<8 | uint32(x)
		bits += 8
		for bits >= 5 {
			out[n] = crockford[(acc>>(bits-5))&31]
			n++
			bits -= 5
		}
		acc &= 1<<bits - 1
	}
	return string(out[:])
}
