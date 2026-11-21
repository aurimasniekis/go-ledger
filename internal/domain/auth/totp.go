package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 TOTP with HMAC-SHA1 is what authenticator apps implement; it is an HMAC, not a collision-resistant hash.
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// TOTP parameters (RFC 6238 with the defaults every authenticator app
// supports): HMAC-SHA1, 6 digits, 30-second steps. A code is accepted in its
// own step and one step either side (TOTPSkew), to allow for clock drift and
// typing time. The skew is a proposed default (ADR-0030), not an operator
// decision.
const (
	TOTPDigits      = 6
	TOTPPeriod      = 30 * time.Second
	TOTPSkew        = 1
	TOTPSecretBytes = 20 // 160 bits, the length RFC 4226 recommends
	// TOTPIssuer labels the account in authenticator apps.
	TOTPIssuer = "Ledger"
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// newTOTPSecret returns a random TOTP secret.
func newTOTPSecret() []byte {
	b := make([]byte, TOTPSecretBytes)
	_, _ = rand.Read(b) // never fails (Go 1.24+)
	return b
}

// EncodeTOTPSecret is the base32 form (no padding) authenticator apps accept.
func EncodeTOTPSecret(secret []byte) string { return b32.EncodeToString(secret) }

// DecodeTOTPSecret reads a base32 secret, ignoring case, spaces and padding.
func DecodeTOTPSecret(s string) ([]byte, error) {
	s = strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "=", "").Replace(s))
	return b32.DecodeString(s)
}

// totpStep is the RFC 6238 time step counter of t.
func totpStep(t time.Time) int64 { return t.Unix() / int64(TOTPPeriod/time.Second) }

// hotp is RFC 4226's HOTP value of counter for secret, as TOTPDigits digits.
func hotp(secret []byte, counter int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(counter))
	m := hmac.New(sha1.New, secret)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	s := strconv.FormatUint(uint64(v%1_000_000), 10)
	return strings.Repeat("0", TOTPDigits-len(s)) + s
}

// TOTPCode is the code for secret at time t. Tests use it to play the
// authenticator app.
func TOTPCode(secret []byte, t time.Time) string { return hotp(secret, totpStep(t)) }

// normalizeCode strips spaces from a submitted code and reports whether what
// remains is TOTPDigits ASCII digits.
func normalizeCode(code string) (string, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != TOTPDigits {
		return "", false
	}
	for i := 0; i < len(code); i++ {
		if code[i] < '0' || code[i] > '9' {
			return "", false
		}
	}
	return code, true
}

// matchTOTP returns the time step whose code equals code, among the steps
// within TOTPSkew of now that are later than after (the last step already
// used: a code is accepted once). Every candidate step is compared, in
// constant time, whatever matches, so timing does not reveal which step
// matched. The earliest matching step wins.
func matchTOTP(secret []byte, code string, now time.Time, after int64) (int64, bool) {
	code, ok := normalizeCode(code)
	if !ok {
		return 0, false
	}
	cur := totpStep(now)
	found := int64(-1)
	for s := cur - TOTPSkew; s <= cur+TOTPSkew; s++ {
		eq := subtle.ConstantTimeCompare([]byte(hotp(secret, s)), []byte(code)) == 1
		if eq && s > after && s >= 0 && found < 0 {
			found = s
		}
	}
	if found < 0 {
		return 0, false
	}
	return found, true
}

// TOTPURI is the otpauth:// URI (Key Uri Format) authenticator apps read
// from a QR code: label "Ledger:<username>", issuer, algorithm, digits and
// period. It contains the secret: show it once, never log it.
func TOTPURI(username string, secret []byte) string {
	q := url.Values{}
	q.Set("secret", EncodeTOTPSecret(secret))
	q.Set("issuer", TOTPIssuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", strconv.Itoa(TOTPDigits))
	q.Set("period", strconv.Itoa(int(TOTPPeriod/time.Second)))
	label := url.PathEscape(TOTPIssuer + ":" + username)
	return "otpauth://totp/" + label + "?" + q.Encode()
}
