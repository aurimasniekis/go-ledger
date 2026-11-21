package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
)

// Proposed two-factor defaults (ADR-0030). None is an operator decision.
const (
	// RecoveryCodeCount is how many recovery codes an enrolment issues.
	RecoveryCodeCount = 10
	// EnrollmentTTL is how long a started TOTP enrolment can be confirmed.
	EnrollmentTTL = 10 * time.Minute
	// ChallengeTTL is how long the second step of a sign-in may take.
	ChallengeTTL = 5 * time.Minute
	// ChallengeMaxFailures is how many wrong codes one sign-in challenge
	// takes before it is discarded (the per-username and per-address limits
	// apply as well).
	ChallengeMaxFailures = 5
	// maxChallenges bounds the challenges kept in memory.
	maxChallenges = 10000
)

// TOTPPurpose is the HKDF info of the key that seals TOTP secrets.
const TOTPPurpose = "ledger totp secret v1"

// TwoFactorState is what the store holds about a user's second factor. The
// secrets are sealed ([Sealer]); recovery codes only as hashes.
type TwoFactorState struct {
	// Secret is the sealed active TOTP secret; empty when TOTP is not enabled.
	Secret    string
	EnabledAt time.Time
	// LastStep is the last TOTP time step accepted; a code of that step or an
	// earlier one is refused (replay protection).
	LastStep int64
	// PendingSecret is the sealed secret of an enrolment that was started and
	// not yet confirmed; PendingAt is when it was started.
	PendingSecret string
	PendingAt     time.Time
	// RecoveryCodes is how many unused recovery codes remain.
	RecoveryCodes int
}

// Enabled reports whether TOTP is enabled.
func (s TwoFactorState) Enabled() bool { return s.Secret != "" }

// TwoFactorStore is the persistence port for second factors; internal/store/auth
// implements it with [Store]. Events are written in the same transaction as
// the change.
type TwoFactorStore interface {
	// TwoFactor returns the user's state; the zero state when there is none.
	TwoFactor(ctx context.Context, userID string) (TwoFactorState, error)
	// SetPendingTOTP records a started enrolment, replacing an earlier one.
	// An enabled secret stays active until the new one is confirmed.
	SetPendingTOTP(ctx context.Context, userID, sealed string, at time.Time) error
	// ConfirmTOTP makes the pending secret (still equal to pending) the active
	// one, with lastStep used, replaces the recovery codes with codeHashes,
	// deletes every session of the user except keep, and writes
	// events(ended). It returns resource.ErrNotFound when the pending secret
	// is no longer pending (confirmed, replaced or disabled meanwhile).
	ConfirmTOTP(ctx context.Context, userID, pending string, lastStep int64, codeHashes []string, keep string, at time.Time, events func(ended int) []audit.Event) (ended int, err error)
	// UseTOTPStep records step as used when it is later than the last used
	// step and TOTP is still enabled with sealed; false otherwise (a replay,
	// or a concurrent use of the same code).
	UseTOTPStep(ctx context.Context, userID, sealed string, step int64) (bool, error)
	// UseRecoveryCode deletes the user's unused recovery code with this hash
	// and returns how many remain; false when there is no such code.
	UseRecoveryCode(ctx context.Context, userID, codeHash string) (remaining int, ok bool, err error)
	// ReplaceRecoveryCodes replaces every recovery code of the user, who must
	// have TOTP enabled (resource.ErrNotFound otherwise).
	ReplaceRecoveryCodes(ctx context.Context, userID string, codeHashes []string, at time.Time, events ...audit.Event) error
	// DisableTwoFactor deletes the user's TOTP secrets (active and pending)
	// and recovery codes; the events are written only when TOTP was enabled.
	// It reports whether it was.
	DisableTwoFactor(ctx context.Context, userID string, events ...audit.Event) (bool, error)
}

// Two-factor errors.
var (
	// ErrInvalidSecondFactor: a TOTP or recovery code did not match, was
	// already used, or was malformed. One answer for every reason.
	ErrInvalidSecondFactor = errors.New("the authentication code is not valid")
	// ErrInvalidChallenge: no such sign-in challenge, or it expired or was
	// used up. The user signs in again with the password.
	ErrInvalidChallenge = errors.New("the sign-in has expired or is not valid; sign in again")
	// ErrNoPendingEnrollment: confirm without a started, unexpired enrolment.
	ErrNoPendingEnrollment = errors.New("no two-factor enrolment is in progress; start one first")
	// ErrTwoFactorNotEnabled: the action needs TOTP enabled.
	ErrTwoFactorNotEnabled = errors.New("two-factor authentication is not enabled")
	// ErrTwoFactorRequired: TOTP cannot be disabled while auth.require_2fa
	// is on (it can be replaced by enrolling again).
	ErrTwoFactorRequired = errors.New("two-factor authentication is required by the configuration and cannot be disabled; enrol a new authenticator to replace it")
	// ErrInvalidCurrentFactor: the current TOTP or recovery code given with
	// a change to 2FA did not match. It is also [ErrInvalidSecondFactor].
	ErrInvalidCurrentFactor = fmt.Errorf("the current authentication code or recovery code is not valid: %w", ErrInvalidSecondFactor)
	// ErrCurrentFactorRequired: TOTP is enabled, so replacing, disabling or
	// regenerating it needs exactly one current TOTP code or recovery code
	// (decision D26-1a).
	ErrCurrentFactorRequired = errors.New("two-factor authentication is enabled: give a current authentication code or a recovery code (one of them)")
	// ErrTwoFactorUnavailable: no key-encryption key, so secrets cannot be
	// stored.
	ErrTwoFactorUnavailable = errors.New("two-factor authentication is not available: no key-encryption key is configured")
)

// Second-factor methods, as audited.
const (
	MethodTOTP         = "totp"
	MethodRecoveryCode = "recovery_code"
)

// totpAAD binds a sealed TOTP secret to its user.
func totpAAD(userID string) []byte { return []byte("totp:" + userID) }

// recoveryAlphabet is Crockford's base32 alphabet (no I, L, O, U).
const recoveryAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// newRecoveryCode returns 16 random base32 characters (80 bits) grouped as
// XXXX-XXXX-XXXX-XXXX. With 80 bits a code cannot be found from its SHA-256
// hash by brute force, so no slow hash is needed.
func newRecoveryCode() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // never fails (Go 1.24+)
	var sb strings.Builder
	for i, c := range b {
		if i > 0 && i%4 == 0 {
			sb.WriteByte('-')
		}
		sb.WriteByte(recoveryAlphabet[c&31])
	}
	return sb.String()
}

// normalizeRecoveryCode upper-cases a submitted code and drops separators and
// spaces; Crockford's look-alikes (O, I, L) read as 0, 1, 1.
func normalizeRecoveryCode(code string) (string, bool) {
	var sb strings.Builder
	for _, r := range strings.ToUpper(code) {
		switch r {
		case '-', ' ':
			continue
		case 'O':
			r = '0'
		case 'I', 'L':
			r = '1'
		}
		if !strings.ContainsRune(recoveryAlphabet, r) {
			return "", false
		}
		sb.WriteRune(r)
		if sb.Len() > 16 {
			return "", false
		}
	}
	return sb.String(), sb.Len() == 16
}

// recoveryCodeHash is the stored form of a recovery code: SHA-256 over the
// user ID and the normalised code, hex encoded.
func recoveryCodeHash(userID, normalized string) string {
	sum := sha256.Sum256([]byte("ledger recovery code v1\x00" + userID + "\x00" + normalized))
	return hex.EncodeToString(sum[:])
}

func newRecoveryCodes(userID string) (codes, hashes []string) {
	for range RecoveryCodeCount {
		c := newRecoveryCode()
		n, _ := normalizeRecoveryCode(c)
		codes = append(codes, c)
		hashes = append(hashes, recoveryCodeHash(userID, n))
	}
	return codes, hashes
}

// TwoFactorStatus is what a user may know about their own second factor.
type TwoFactorStatus struct {
	Enabled   bool
	EnabledAt time.Time
	// RecoveryCodesRemaining is the number of unused recovery codes.
	RecoveryCodesRemaining int
	// Required is auth.require_2fa.
	Required bool
	// PendingUntil is when a started enrolment stops being confirmable; zero
	// when none is pending.
	PendingUntil time.Time
}

// TwoFactorStatus returns the user's second-factor status.
func (s *Service) TwoFactorStatus(ctx context.Context, userID string) (TwoFactorStatus, error) {
	st, err := s.store.TwoFactor(ctx, userID)
	if err != nil {
		return TwoFactorStatus{}, err
	}
	out := TwoFactorStatus{Enabled: st.Enabled(), EnabledAt: st.EnabledAt, RecoveryCodesRemaining: st.RecoveryCodes, Required: s.cfg.Require2FA}
	if st.PendingSecret != "" && s.now().Before(st.PendingAt.Add(EnrollmentTTL)) {
		out.PendingUntil = st.PendingAt.Add(EnrollmentTTL)
	}
	return out, nil
}

// Enrollment is a started TOTP enrolment. Secret and URI are shown to the
// user once, here; the store keeps the secret sealed.
type Enrollment struct {
	// Secret is the base32 secret, for typing into an authenticator app.
	Secret string
	// URI is the otpauth:// URI, for a QR code.
	URI string
	// ExpiresAt is when the enrolment can no longer be confirmed.
	ExpiresAt time.Time
}

// BeginTOTP starts a TOTP enrolment for p's user: a new random secret is
// sealed and stored as pending. An enabled TOTP stays active until the new
// one is confirmed ([Service.ConfirmTOTP]), so a re-enrolment can replace it.
func (s *Service) BeginTOTP(ctx context.Context, p authz.Principal) (Enrollment, error) {
	if s.sealer == nil {
		return Enrollment{}, ErrTwoFactorUnavailable
	}
	cred, err := s.store.CredentialByUser(ctx, p.UserID)
	if err != nil {
		return Enrollment{}, err
	}
	secret := newTOTPSecret()
	sealed, err := s.sealer.Seal(secret, totpAAD(p.UserID))
	if err != nil {
		return Enrollment{}, err
	}
	now := s.now().UTC()
	if err := s.store.SetPendingTOTP(ctx, p.UserID, sealed, now); err != nil {
		return Enrollment{}, err
	}
	return Enrollment{Secret: EncodeTOTPSecret(secret), URI: TOTPURI(cred.Username, secret), ExpiresAt: now.Add(EnrollmentTTL)}, nil
}

// CurrentFactor is the user's current second factor, given with a change to
// it (decision D26-1a): exactly one of a TOTP code from the enabled
// authenticator or an unused recovery code. Address is the client's, for the
// sign-in limits.
type CurrentFactor struct {
	Code         string
	RecoveryCode string
	Address      string
}

// verifyCurrentFactor checks the current second factor of p's user, whose
// TOTP is enabled (st), before a change to it. It is a step-up on top of
// recent re-authentication (password or OIDC), for every session method. The
// attempt is reserved in the same per-username and per-address limits as a
// sign-in (ADR-0030), a wrong code counts as a failure and is audited
// (auth.reauthentication_failed, reason invalid_second_factor), a TOTP step
// is recorded as used and a recovery code is consumed. It returns the method
// used, for the change's audit entry.
func (s *Service) verifyCurrentFactor(ctx context.Context, p authz.Principal, st TwoFactorState, cf CurrentFactor) (string, error) {
	if (cf.Code == "") == (cf.RecoveryCode == "") {
		return "", ErrCurrentFactorRequired
	}
	cred, err := s.store.CredentialByUser(ctx, p.UserID)
	if err != nil {
		return "", err
	}
	method := MethodTOTP
	if cf.RecoveryCode != "" {
		method = MethodRecoveryCode
	}
	now := s.now().UTC()
	failed := func(reason string) audit.Event {
		return audit.Event{Action: audit.ReauthenticationFailed, Actor: audit.ActorOf(p), TargetType: "user", TargetID: p.UserID, At: now,
			Details: map[string]string{"method": method, "reason": reason}}
	}
	at, err := s.begin(accountKey(cred.Username), AddressKey(cf.Address), func() error {
		return s.rec.Record(ctx, failed("locked_out"))
	})
	if err != nil {
		return "", err
	}
	defer at.done()
	ok, _, err := s.checkSecondFactor(ctx, p.UserID, st, SecondFactorInput{Code: cf.Code, RecoveryCode: cf.RecoveryCode})
	if err != nil {
		return "", err
	}
	if !ok {
		at.failed()
		if err := s.rec.Record(ctx, failed("invalid_second_factor")); err != nil {
			return "", err
		}
		return "", ErrInvalidCurrentFactor
	}
	at.succeeded()
	return method, nil
}

// ConfirmTOTP checks code against the pending enrolment of p's user and, when
// it matches, enables TOTP with that secret, issues new recovery codes
// (returned once; any earlier ones stop working) and ends every other session
// of the user. When TOTP is already enabled (a replacement), current must
// also prove the authenticator being replaced or a recovery code
// ([ErrCurrentFactorRequired], [ErrInvalidSecondFactor]); the new code is
// checked first, so a typo in it consumes no recovery code. Audited
// (two_factor.enabled).
func (s *Service) ConfirmTOTP(ctx context.Context, p authz.Principal, code string, current CurrentFactor) (codes []string, ended int, err error) {
	if s.sealer == nil {
		return nil, 0, ErrTwoFactorUnavailable
	}
	st, err := s.store.TwoFactor(ctx, p.UserID)
	if err != nil {
		return nil, 0, err
	}
	now := s.now().UTC()
	if st.PendingSecret == "" || !now.Before(st.PendingAt.Add(EnrollmentTTL)) {
		return nil, 0, ErrNoPendingEnrollment
	}
	secret, err := s.sealer.Open(st.PendingSecret, totpAAD(p.UserID))
	if err != nil {
		return nil, 0, err
	}
	step, ok := matchTOTP(secret, code, now, -1)
	if !ok {
		return nil, 0, ErrInvalidSecondFactor
	}
	details := map[string]string{"method": MethodTOTP, "replaced": strconv.FormatBool(st.Enabled())}
	if st.Enabled() {
		verified, err := s.verifyCurrentFactor(ctx, p, st, current)
		if err != nil {
			return nil, 0, err
		}
		details["verified_with"] = verified
	}
	codes, hashes := newRecoveryCodes(p.UserID)
	ended, err = s.store.ConfirmTOTP(ctx, p.UserID, st.PendingSecret, step, hashes, p.SessionID, now, func(ended int) []audit.Event {
		details["sessions_ended"] = strconv.Itoa(ended)
		return []audit.Event{{Action: audit.TwoFactorEnabled, Actor: audit.ActorOf(p), TargetType: "user", TargetID: p.UserID, At: now, Details: details}}
	})
	if errors.Is(err, resource.ErrNotFound) {
		return nil, 0, ErrNoPendingEnrollment
	}
	if err != nil {
		return nil, 0, err
	}
	return codes, ended, nil
}

// RegenerateRecoveryCodes replaces p's user's recovery codes with new ones,
// returned once. current must prove a TOTP code or an unused recovery code
// (decision D26-1a). Audited (two_factor.recovery_codes_regenerated).
func (s *Service) RegenerateRecoveryCodes(ctx context.Context, p authz.Principal, current CurrentFactor) ([]string, error) {
	st, err := s.store.TwoFactor(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	if !st.Enabled() {
		return nil, ErrTwoFactorNotEnabled
	}
	verified, err := s.verifyCurrentFactor(ctx, p, st, current)
	if err != nil {
		return nil, err
	}
	codes, hashes := newRecoveryCodes(p.UserID)
	now := s.now().UTC()
	err = s.store.ReplaceRecoveryCodes(ctx, p.UserID, hashes, now, audit.Event{Action: audit.RecoveryCodesReset,
		Actor: audit.ActorOf(p), TargetType: "user", TargetID: p.UserID, At: now,
		Details: map[string]string{"count": strconv.Itoa(len(codes)), "verified_with": verified}})
	if errors.Is(err, resource.ErrNotFound) {
		return nil, ErrTwoFactorNotEnabled
	}
	if err != nil {
		return nil, err
	}
	return codes, nil
}

// DisableTwoFactor removes the TOTP secret and recovery codes of user target.
// by is [ByUser] or [ByHost] (who is audited). A user cannot disable it while
// auth.require_2fa is on ([ErrTwoFactorRequired]); the host can, which is the
// recovery path for a lost authenticator without recovery codes: the user is
// then confined to enrolment at the next sign-in. Audited
// (two_factor.disabled) when TOTP was enabled; [ErrTwoFactorNotEnabled]
// otherwise (a pending enrolment is discarded either way). A user (not the
// host) must also prove a current TOTP code or recovery code in current
// (decision D26-1a); the host command needs none.
func (s *Service) DisableTwoFactor(ctx context.Context, actor authz.Principal, system bool, target, by string, current CurrentFactor) error {
	if !system && s.cfg.Require2FA {
		return ErrTwoFactorRequired
	}
	a := audit.System
	details := map[string]string{"method": MethodTOTP, "by": by}
	if !system {
		a = audit.ActorOf(actor)
		st, err := s.store.TwoFactor(ctx, target)
		if err != nil {
			return err
		}
		if st.Enabled() {
			verified, err := s.verifyCurrentFactor(ctx, actor, st, current)
			if err != nil {
				return err
			}
			details["verified_with"] = verified
		}
	}
	now := s.now().UTC()
	was, err := s.store.DisableTwoFactor(ctx, target, audit.Event{Action: audit.TwoFactorDisabled, Actor: a, TargetType: "user",
		TargetID: target, At: now, Details: details})
	if err != nil {
		return err
	}
	if !was {
		return ErrTwoFactorNotEnabled
	}
	return nil
}

// SecondFactorChallenge is the second step of a sign-in for a user with TOTP
// enabled: the password was right, and a TOTP or recovery code must follow.
// Token is a secret, returned once; the server keeps only its hash, in memory.
type SecondFactorChallenge struct {
	Token     string
	ExpiresAt time.Time
	Methods   []string
}

type challenge struct {
	userID, accKey string
	expires        time.Time
	failures       int
	userAgent      string
	// method is the session method the first step used (password or oidc).
	method string
	// authAt is when the first step's authentication happened (see startSession).
	authAt time.Time
	// deviceID is the device cookie the password step came with, when
	// knownDevice; accKey is then that cookie's key (decision D23-1b).
	deviceID    string
	knownDevice bool
}

// challenges holds pending second steps in memory: a restart discards them
// and the user signs in again (ADR-0030).
type challenges struct {
	mu sync.Mutex
	m  map[string]*challenge // by hashSecret(token)
}

func (c *challenges) add(now time.Time, ch *challenge) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]*challenge{}
	}
	if len(c.m) >= maxChallenges {
		for k, x := range c.m {
			if !now.Before(x.expires) {
				delete(c.m, k)
			}
		}
		for len(c.m) >= maxChallenges { // drop the one expiring first
			var victim string
			var at time.Time
			for k, x := range c.m {
				if victim == "" || x.expires.Before(at) {
					victim, at = k, x.expires
				}
			}
			delete(c.m, victim)
		}
	}
	tok := newSecret()
	c.m[hashSecret(tok)] = ch
	return tok
}

// get returns a copy of the live challenge of token.
func (c *challenges) get(now time.Time, token string) (challenge, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := hashSecret(token)
	x, ok := c.m[k]
	if !ok {
		return challenge{}, false
	}
	if !now.Before(x.expires) || x.failures >= ChallengeMaxFailures {
		delete(c.m, k)
		return challenge{}, false
	}
	return *x, true
}

// fail counts a wrong code; the challenge is discarded at the limit.
func (c *challenges) fail(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := hashSecret(token)
	if x, ok := c.m[k]; ok {
		x.failures++
		if x.failures >= ChallengeMaxFailures {
			delete(c.m, k)
		}
	}
}

// take removes the challenge; false when it was already gone (used by a
// concurrent request).
func (c *challenges) take(token string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := hashSecret(token)
	_, ok := c.m[k]
	delete(c.m, k)
	return ok
}

// SecondFactorInput is the second step of a sign-in: the challenge token and
// exactly one of a TOTP code or a recovery code.
type SecondFactorInput struct {
	Challenge    string
	Code         string
	RecoveryCode string
	Address      string
	UserAgent    string
}

// CompleteSignIn checks the second factor of a sign-in challenge and starts
// the session. A wrong, malformed or replayed code is [ErrInvalidSecondFactor]
// and counts towards the same per-username and per-address limits as
// passwords (429 while blocked; the device cookie's limit instead of the
// username's when the password step came from a known browser, and a
// complete password sign-in issues a new device cookie); a challenge takes at most
// [ChallengeMaxFailures] wrong codes. An unknown, expired or used-up
// challenge is [ErrInvalidChallenge]. A recovery code is consumed by its use.
// Success and failure are audited (method totp or recovery_code).
func (s *Service) CompleteSignIn(ctx context.Context, in SecondFactorInput) (Issued, error) {
	if len(in.Challenge) > MaxTokenLength {
		return Issued{}, ErrInvalidChallenge
	}
	now := s.now().UTC()
	ch, ok := s.challenges.get(now, in.Challenge)
	if !ok {
		return Issued{}, ErrInvalidChallenge
	}
	method := MethodTOTP
	if in.RecoveryCode != "" {
		method = MethodRecoveryCode
	}
	actor := audit.Actor{Kind: audit.ActorUser, UserID: ch.userID}
	at, err := s.begin(ch.accKey, AddressKey(in.Address), func() error {
		return s.rec.Record(ctx, signInEvent(actor, false, method, "locked_out", now, ch.knownDevice))
	})
	if err != nil {
		return Issued{}, err
	}
	defer at.done()
	cred, err := s.store.CredentialByUser(ctx, ch.userID)
	if errors.Is(err, resource.ErrNotFound) || (err == nil && cred.Disabled) {
		s.challenges.take(in.Challenge)
		return Issued{}, ErrInvalidChallenge
	}
	if err != nil {
		return Issued{}, err
	}
	st, err := s.store.TwoFactor(ctx, ch.userID)
	if err != nil {
		return Issued{}, err
	}
	if !st.Enabled() { // disabled from the host meanwhile: sign in again
		s.challenges.take(in.Challenge)
		return Issued{}, ErrInvalidChallenge
	}
	ok, remaining, err := s.checkSecondFactor(ctx, ch.userID, st, in)
	if err != nil {
		return Issued{}, err
	}
	if !ok {
		at.failed()
		s.challenges.fail(in.Challenge)
		if err := s.rec.Record(ctx, signInEvent(actor, false, method, "invalid_second_factor", now, ch.knownDevice)); err != nil {
			return Issued{}, err
		}
		return Issued{}, ErrInvalidSecondFactor
	}
	if !s.challenges.take(in.Challenge) {
		at.done()
		return Issued{}, ErrInvalidChallenge
	}
	at.succeeded()
	ev := signInEvent(actor, true, method, "", now, ch.knownDevice)
	if method == MethodRecoveryCode {
		ev.Details["recovery_codes_remaining"] = strconv.Itoa(remaining)
	}
	authAt := ch.authAt
	if authAt.IsZero() { // a password first step: the second factor completes the check
		authAt = now
	}
	iss, err := s.startSession(ctx, ch.userID, ch.method, in.Address, ch.userAgent, now, authAt, ev)
	if err != nil {
		return Issued{}, err
	}
	if ch.method == SessionMethodPassword { // only password sign-ins make a browser known
		s.issueDevice(ctx, &iss, ch.userID, ch.deviceID, now)
	}
	return iss, nil
}

// checkSecondFactor verifies a TOTP code (recording its step as used) or a
// recovery code (consuming it).
func (s *Service) checkSecondFactor(ctx context.Context, userID string, st TwoFactorState, in SecondFactorInput) (ok bool, remaining int, err error) {
	if (in.Code == "") == (in.RecoveryCode == "") {
		return false, 0, nil
	}
	if in.RecoveryCode != "" {
		n, valid := normalizeRecoveryCode(in.RecoveryCode)
		if !valid {
			return false, 0, nil
		}
		remaining, ok, err = s.store.UseRecoveryCode(ctx, userID, recoveryCodeHash(userID, n))
		return ok, remaining, err
	}
	if s.sealer == nil {
		return false, 0, ErrTwoFactorUnavailable
	}
	secret, err := s.sealer.Open(st.Secret, totpAAD(userID))
	if err != nil {
		return false, 0, err
	}
	step, ok := matchTOTP(secret, in.Code, s.now(), st.LastStep)
	if !ok {
		return false, 0, nil
	}
	ok, err = s.store.UseTOTPStep(ctx, userID, st.Secret, step)
	return ok, 0, err
}

// sessionConfined reports whether the session must enrol a second factor
// before anything else: auth.require_2fa is on, TOTP is not enabled, and the
// session was not started through OIDC while Ledger trusts the identity
// provider's own authentication (auth.oidc.require_2fa off, decision D16).
func (s *Service) sessionConfined(ctx context.Context, sess Session) (bool, error) {
	if sess.Method == SessionMethodOIDC && !s.cfg.OIDC.Require2FA {
		return false, nil
	}
	return s.enrollmentRequired(ctx, sess.UserID)
}

// enrollmentRequired reports whether userID must enrol a second factor before
// anything else: auth.require_2fa is on and TOTP is not enabled.
func (s *Service) enrollmentRequired(ctx context.Context, userID string) (bool, error) {
	if !s.cfg.Require2FA {
		return false, nil
	}
	st, err := s.store.TwoFactor(ctx, userID)
	if err != nil {
		return false, err
	}
	return !st.Enabled(), nil
}
