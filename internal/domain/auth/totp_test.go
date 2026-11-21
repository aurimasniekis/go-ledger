package auth

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

// RFC 6238 Appendix B, SHA-1 rows: the 8-digit values truncated to the 6
// digits Ledger uses (the same HOTP value modulo 10^6).
func TestTOTPMatchesRFC6238Vectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	for _, c := range []struct {
		unix int64
		want string
	}{{59, "287082"}, {1111111109, "081804"}, {1111111111, "050471"}, {1234567890, "005924"}, {2000000000, "279037"}, {20000000000, "353130"}} {
		if got := TOTPCode(secret, time.Unix(c.unix, 0)); got != c.want {
			t.Errorf("T=%d: %s, want %s", c.unix, got, c.want)
		}
	}
}

func TestMatchTOTPWindowAndReplay(t *testing.T) {
	secret := []byte("12345678901234567890")
	now := time.Unix(1111111111, 0) // step 37037037
	cur := totpStep(now)
	code := func(step int64) string { return hotp(secret, step) }
	for _, c := range []struct {
		name  string
		code  string
		after int64
		step  int64
		ok    bool
	}{
		{"current step", code(cur), 0, cur, true},
		{"previous step", code(cur - 1), 0, cur - 1, true},
		{"next step", code(cur + 1), 0, cur + 1, true},
		{"two steps old", code(cur - 2), 0, 0, false},
		{"two steps ahead", code(cur + 2), 0, 0, false},
		{"already used step", code(cur), cur, 0, false},
		{"earlier than the used step", code(cur - 1), cur, 0, false},
		{"later than the used step", code(cur + 1), cur, cur + 1, true},
		{"with spaces", code(cur)[:3] + " " + code(cur)[3:], 0, cur, true},
		{"too short", code(cur)[:5], 0, 0, false},
		{"letters", "12345a", 0, 0, false},
		{"empty", "", 0, 0, false},
		{"unicode digits", "١٢٣٤٥٦", 0, 0, false},
	} {
		step, ok := matchTOTP(secret, c.code, now, c.after)
		if ok != c.ok || step != c.step {
			t.Errorf("%s: step %d ok %v, want %d %v", c.name, step, ok, c.step, c.ok)
		}
	}
}

func TestTOTPURI(t *testing.T) {
	secret := []byte("12345678901234567890")
	u, err := url.Parse(TOTPURI("alice", secret))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Scheme != "otpauth" || u.Host != "totp" || u.Path != "/Ledger:alice" || q.Get("secret") != "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" ||
		q.Get("issuer") != "Ledger" || q.Get("algorithm") != "SHA1" || q.Get("digits") != "6" || q.Get("period") != "30" {
		t.Errorf("uri %s", u)
	}
	got, err := DecodeTOTPSecret("gezd gnbv gy3t qojq gezd gnbv gy3t qojq")
	if err != nil || string(got) != string(secret) {
		t.Errorf("decode: %q %v", got, err)
	}
	if len(newTOTPSecret()) != 20 {
		t.Error("secret length")
	}
}

func TestSealerRoundTripAndBinding(t *testing.T) {
	s, err := NewKEKSealer("a long random key-encryption key for tests", TOTPPurpose)
	if err != nil {
		t.Fatal(err)
	}
	pt := []byte("12345678901234567890")
	sealed, err := s.Seal(pt, totpAAD("U1"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealed, "v1.") || strings.Contains(sealed, EncodeTOTPSecret(pt)) {
		t.Fatalf("sealed %q", sealed)
	}
	again, _ := s.Seal(pt, totpAAD("U1"))
	if again == sealed {
		t.Error("two seals of the same secret are equal: the nonce is not random")
	}
	if got, err := s.Open(sealed, totpAAD("U1")); err != nil || string(got) != string(pt) {
		t.Fatalf("open: %q %v", got, err)
	}
	other, _ := NewKEKSealer("another key-encryption key entirely", TOTPPurpose)
	otherPurpose, _ := NewKEKSealer("a long random key-encryption key for tests", "something else")
	tampered := sealed[:len(sealed)-2] + map[bool]string{true: "AA", false: "BB"}[!strings.HasSuffix(sealed, "AA")]
	for name, open := range map[string]func() ([]byte, error){
		"other user":    func() ([]byte, error) { return s.Open(sealed, totpAAD("U2")) },
		"other KEK":     func() ([]byte, error) { return other.Open(sealed, totpAAD("U1")) },
		"other purpose": func() ([]byte, error) { return otherPurpose.Open(sealed, totpAAD("U1")) },
		"tampered":      func() ([]byte, error) { return s.Open(tampered, totpAAD("U1")) },
		"no prefix":     func() ([]byte, error) { return s.Open(sealed[3:], totpAAD("U1")) },
		"short":         func() ([]byte, error) { return s.Open("v1.AAAA", totpAAD("U1")) },
	} {
		if _, err := open(); !errors.Is(err, ErrUnsealable) {
			t.Errorf("%s: %v, want ErrUnsealable", name, err)
		}
	}
	if _, err := NewKEKSealer("", TOTPPurpose); err == nil {
		t.Error("empty KEK accepted")
	}
}

func TestRecoveryCodes(t *testing.T) {
	codes, hashes := newRecoveryCodes("01JZ0000000000000000000030")
	if len(codes) != RecoveryCodeCount || len(hashes) != RecoveryCodeCount {
		t.Fatalf("%d codes", len(codes))
	}
	format := regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{4}(-[0-9A-HJKMNP-TV-Z]{4}){3}$`)
	seen := map[string]bool{}
	for i, c := range codes {
		if !format.MatchString(c) {
			t.Errorf("code %q", c)
		}
		if seen[c] {
			t.Errorf("duplicate code")
		}
		seen[c] = true
		n, ok := normalizeRecoveryCode(strings.ToLower(strings.ReplaceAll(c, "-", " ")))
		if !ok || recoveryCodeHash("01JZ0000000000000000000030", n) != hashes[i] {
			t.Errorf("code %d does not normalise to its hash", i)
		}
		if recoveryCodeHash("01JZ0000000000000000000031", n) == hashes[i] {
			t.Error("the hash does not depend on the user")
		}
	}
	for in, want := range map[string]string{"oIl0-ABCD-EFGH-JKMN": "0110ABCDEFGHJKMN", "0110abcdefghjkmn": "0110ABCDEFGHJKMN"} {
		if got, ok := normalizeRecoveryCode(in); !ok || got != want {
			t.Errorf("%q: %q %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", "ABCD-EFGH-JKMN", "ABCD-EFGH-JKMN-PQRSX", "ABCD-EFGH-JKMN-PQR!", "UUUU-UUUU-UUUU-UUUU"} {
		if _, ok := normalizeRecoveryCode(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}
