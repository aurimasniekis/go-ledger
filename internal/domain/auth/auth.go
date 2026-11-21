package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/domain/users"
)

// Proposed defaults (ADR-0026). None of them is an operator decision yet;
// each is configurable (auth.* configuration keys).
const (
	DefaultIdleTimeout     = 30 * time.Minute
	DefaultAbsoluteTimeout = 12 * time.Hour
	DefaultReauthWindow    = 10 * time.Minute
)

// Proposed default sign-in limits (ADR-0026).
var (
	DefaultAccountLimit = Limit{Max: 10, Window: 15 * time.Minute}
	DefaultAddressLimit = Limit{Max: 30, Window: 15 * time.Minute}
)

// Config is the sign-in and session policy.
type Config struct {
	Policy PasswordPolicy
	// IdleTimeout ends a session not used for this long.
	IdleTimeout time.Duration
	// AbsoluteTimeout ends a session this long after sign-in, used or not.
	AbsoluteTimeout time.Duration
	// ReauthWindow is how long a password check counts as recent for
	// sensitive actions (credential changes, token creation, user management).
	ReauthWindow time.Duration
	// AccountLimit counts failures per submitted username, AddressLimit per
	// client address ([AddressKey]).
	AccountLimit Limit
	AddressLimit Limit
	// Require2FA confines every signed-in user without TOTP enabled to
	// enrolment (auth.require_2fa, ADR-0030).
	Require2FA bool
	// PasswordSignInDisabled refuses password sign-in and password
	// re-authentication for everyone (auth.password.enabled=false, decision
	// D16); sign-in goes to OIDC. Linking an identity never sets it.
	PasswordSignInDisabled bool
	// OIDC is the single-sign-on policy (ADR-0033).
	OIDC OIDCPolicy
}

// DefaultConfig returns the proposed defaults.
func DefaultConfig() Config {
	return Config{Policy: PasswordPolicy{MinBytes: DefaultMinPasswordBytes}, IdleTimeout: DefaultIdleTimeout,
		AbsoluteTimeout: DefaultAbsoluteTimeout, ReauthWindow: DefaultReauthWindow,
		AccountLimit: DefaultAccountLimit, AddressLimit: DefaultAddressLimit}
}

// Validate checks the policy is usable.
func (c Config) Validate() error {
	switch {
	case c.Policy.MinBytes < 1 || c.Policy.MinBytes > MaxPasswordBytes:
		return errors.New("auth: minimum password length must be 1 to " + strconv.Itoa(MaxPasswordBytes) + " bytes")
	case c.IdleTimeout <= 0 || c.AbsoluteTimeout <= 0 || c.ReauthWindow <= 0:
		return errors.New("auth: session timeouts and the re-authentication window must be positive")
	case c.IdleTimeout > c.AbsoluteTimeout:
		return errors.New("auth: the idle timeout must not exceed the absolute timeout")
	case c.AccountLimit.Max < 1 || c.AddressLimit.Max < 1 || c.AccountLimit.Window <= 0 || c.AddressLimit.Window <= 0:
		return errors.New("auth: sign-in limits need a positive count and window")
	case c.PasswordSignInDisabled && !c.OIDC.Enabled:
		return errors.New("auth: password sign-in can only be disabled when OIDC sign-in is enabled")
	}
	return c.OIDC.validate()
}

// Session is a server-side sign-in session. The cookie holds a random token;
// only its SHA-256 hash is stored. The session's CSRF token is derived from the
// cookie token ([CSRFToken]), so it is never stored either.
type Session struct {
	// ID identifies the session in lists and termination requests. It is not
	// a credential.
	ID     string
	UserID string
	// TokenHash is the hex SHA-256 hash of the cookie token.
	TokenHash string
	CreatedAt time.Time
	// LastSeenAt is updated at most once per touch interval (see
	// [Service.Authenticate]), so the idle timeout is exact to that interval.
	LastSeenAt time.Time
	// AuthenticatedAt is the last password check: the sign-in or a later
	// re-authentication.
	AuthenticatedAt time.Time
	// ExpiresAt is CreatedAt plus the absolute timeout.
	ExpiresAt time.Time
	// UserAgent and Address are what the client sent at sign-in, truncated;
	// shown in the session list to help a user recognise their sessions.
	UserAgent string
	Address   string
	// Method is how the session was started: [SessionMethodPassword] (with
	// a second factor when the user has one), [SessionMethodOIDC] or
	// [SessionMethodRecovery].
	Method string
}

// How a session was started.
const (
	SessionMethodPassword = "password"
	SessionMethodOIDC     = "oidc"
	SessionMethodRecovery = "recovery"
)

// IdleExpiresAt is when the session ends if it is not used again.
func (s Session) IdleExpiresAt(idle time.Duration) time.Time {
	t := s.LastSeenAt.Add(idle)
	if t.After(s.ExpiresAt) {
		return s.ExpiresAt
	}
	return t
}

// Credential is what sign-in needs to know about an account.
type Credential struct {
	UserID   string
	Username string
	Disabled bool
	// Hash is the encoded password hash; empty when no password is set.
	Hash string
}

// Store is the persistence port; internal/store/auth implements it. Events
// passed to a write are recorded in the same transaction as the change.
type Store interface {
	TwoFactorStore
	OIDCStore
	// CredentialByUsername and CredentialByUser return resource.ErrNotFound
	// for an unknown account.
	CredentialByUsername(ctx context.Context, username string) (Credential, error)
	CredentialByUser(ctx context.Context, userID string) (Credential, error)
	// ReplaceHash swaps the hash of userID when it is still old (a rehash with
	// new parameters after a successful sign-in). Not audited: the password
	// itself does not change.
	ReplaceHash(ctx context.Context, userID, old, hash string) error
	// SetPassword stores hash for userID and deletes every session of the user
	// except keep (may be empty), then writes events(ended) in the same
	// transaction. resource.ErrNotFound for an unknown user.
	SetPassword(ctx context.Context, userID, hash, keep string, at time.Time, events func(ended int) []audit.Event) (ended int, err error)
	CreateSession(ctx context.Context, s Session, events ...audit.Event) error
	// SessionByTokenHash returns resource.ErrNotFound for no such session.
	SessionByTokenHash(ctx context.Context, tokenHash string) (Session, error)
	TouchSession(ctx context.Context, id string, at time.Time) error
	MarkAuthenticated(ctx context.Context, id string, at time.Time, events ...audit.Event) error
	// ListSessions returns the user's sessions, newest first.
	ListSessions(ctx context.Context, userID string) ([]Session, error)
	// DeleteSession deletes session id of userID: resource.ErrNotFound when the
	// user has no such session.
	DeleteSession(ctx context.Context, userID, id string, events ...audit.Event) error
	// DeleteExpired deletes sessions past their absolute expiry (at or before
	// now) or idle since idleBefore.
	DeleteExpired(ctx context.Context, now, idleBefore time.Time) (int, error)
}

// Principals resolves the current principal of a user; users.Store implements
// it. It returns users.ErrDisabled or resource.ErrNotFound for a user who may
// not act.
type Principals interface {
	Principal(ctx context.Context, id string) (authz.Principal, error)
}

// Errors.
var (
	// ErrInvalidCredentials is the one answer to every failed password check:
	// unknown account, wrong password, no password set, disabled account.
	ErrInvalidCredentials = errors.New("the username or password is incorrect")
	// ErrInvalidSession: no such session, or it expired or was ended, or its
	// user can no longer act.
	ErrInvalidSession = errors.New("the session is not valid")
	// ErrPasswordSignInDisabled: auth.password.enabled is false, so passwords
	// are not checked at all; sign in with OIDC.
	ErrPasswordSignInDisabled = errors.New("password sign-in is disabled; sign in with single sign-on")
)

// RateLimitedError is returned while a sign-in limit is exhausted.
type RateLimitedError struct{ RetryAfter time.Duration }

func (e *RateLimitedError) Error() string {
	return "too many failed sign-in attempts; try again later"
}

// Service implements sign-in, sessions and re-authentication.
type Service struct {
	cfg        Config
	store      Store
	principals Principals
	hasher     Hasher
	rec        audit.Recorder
	now        func() time.Time
	newID      func() string
	accounts   *Limiter
	addresses  *Limiter
	// sealer seals TOTP secrets; nil disables enrolment (ErrTwoFactorUnavailable).
	sealer     Sealer
	challenges challenges
	// users creates accounts for OIDC provisioning; provision serialises it.
	users     UserCreator
	provision sync.Mutex
	// dummy is verified when there is no real hash to check, so every
	// failed sign-in costs one hash computation. It is made in NewService,
	// so no request pays for it.
	dummy string
}

// Options are the optional parts of a Service.
type Options struct {
	Now   func() time.Time
	NewID func() string
	// Sealer seals TOTP secrets (NewKEKSealer with TOTPPurpose). Without it,
	// TOTP cannot be enrolled or checked; recovery codes still work.
	Sealer Sealer
	// Users creates accounts when OIDC provisioning is enabled; required
	// then.
	Users UserCreator
}

// NewService returns a Service. rec records audit events that are not part
// of a change (failed attempts).
func NewService(cfg Config, st Store, principals Principals, hasher Hasher, rec audit.Recorder, o Options) (*Service, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if st == nil || principals == nil || hasher == nil || rec == nil {
		return nil, errors.New("auth: store, principals, hasher and audit recorder are required")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.NewID == nil {
		o.NewID = resource.NewID
	}
	if cfg.OIDC.Provisioning && o.Users == nil {
		return nil, errors.New("auth: OIDC provisioning needs a user creator")
	}
	dummy, err := newDummyHash(hasher)
	if err != nil {
		return nil, err
	}
	return &Service{cfg: cfg, store: st, principals: principals, hasher: hasher, rec: rec, now: o.Now, newID: o.NewID,
		accounts: NewLimiter(cfg.AccountLimit, DefaultLimiterKeys, o.Now), addresses: NewLimiter(cfg.AddressLimit, DefaultLimiterKeys, o.Now),
		sealer: o.Sealer, users: o.Users, dummy: dummy}, nil
}

// Config returns the policy.
func (s *Service) Config() Config { return s.cfg }

// SignInInput is a password sign-in attempt.
type SignInInput struct {
	Username string
	Password string
	// Address is the client address, UserAgent its User-Agent header.
	Address   string
	UserAgent string
}

// Issued is a new session with its secrets. The secrets exist only here and
// in the response that sets them; the store keeps their hashes. When the
// password was right but the user has TOTP enabled, SecondFactor is set
// instead and there is no session yet ([Service.CompleteSignIn]).
type Issued struct {
	Session      Session
	Token        string
	CSRFToken    string
	SecondFactor *SecondFactorChallenge
}

// normalizeUsername lower-cases a submitted username (usernames are lower
// case). A name longer than any username can be is not valid: it matches no
// account, and its rate-limit key is its first MaxUsername bytes, so the key
// stays bounded.
func normalizeUsername(u string) (name string, valid bool) {
	u = strings.ToLower(strings.TrimSpace(u))
	if len(u) > users.MaxUsername {
		return u[:users.MaxUsername], false
	}
	return u, true
}

func accountKey(username string) string { return "user:" + username }

// attempt is a password check reserved in both limiters. It must end with
// failed or succeeded; done releases it otherwise (an error before the
// outcome was known), and does nothing after either.
type attempt struct {
	s               *Service
	accKey, addrKey string
	ended           bool
}

// begin reserves an attempt for the account and the address before any
// hashing, so concurrent requests count against the limits while in flight
// (ADR-0026). It returns a *RateLimitedError when either is blocked; the
// first refusal of a block is audited (reason locked_out).
func (s *Service) begin(accKey, addrKey string, audited func() error) (*attempt, error) {
	b1, r1, f1 := s.accounts.Begin(accKey)
	b2, r2, f2 := s.addresses.Begin(addrKey)
	if !b1 && !b2 {
		return &attempt{s: s, accKey: accKey, addrKey: addrKey}, nil
	}
	if !b1 {
		s.accounts.Release(accKey)
	}
	if !b2 {
		s.addresses.Release(addrKey)
	}
	if f1 || f2 {
		if err := audited(); err != nil {
			return nil, err
		}
	}
	return nil, &RateLimitedError{RetryAfter: max(r1, r2)}
}

// failed counts the attempt as a failure for the account and the address.
func (a *attempt) failed() {
	a.ended = true
	a.s.accounts.Confirm(a.accKey)
	a.s.addresses.Confirm(a.addrKey)
}

// succeeded clears the account's failures; the address keeps its count.
func (a *attempt) succeeded() {
	a.ended = true
	a.s.accounts.Release(a.accKey)
	a.s.accounts.Reset(a.accKey)
	a.s.addresses.Release(a.addrKey)
}

// passed releases the attempt without clearing the account's failures: the
// password was right but a second factor must follow, and only a complete
// sign-in clears the count. Otherwise a known password would reset the
// limit on second-factor guesses at will.
func (a *attempt) passed() {
	a.ended = true
	a.s.accounts.Release(a.accKey)
	a.s.addresses.Release(a.addrKey)
}

func (a *attempt) done() {
	if !a.ended {
		a.ended = true
		a.s.accounts.Release(a.accKey)
		a.s.addresses.Release(a.addrKey)
	}
}

func actorFor(c Credential, known bool) audit.Actor {
	if !known {
		return audit.Actor{Kind: audit.ActorAnonymous}
	}
	return audit.Actor{Kind: audit.ActorUser, UserID: c.UserID}
}

// verify checks pw against c's hash, or against the dummy hash when there
// is none, so every attempt costs one hash computation whatever the account.
// rehash reports that c's hash should be replaced ([Service.rehash]); the
// caller does that only for an account that may sign in, so a disabled
// account costs no extra work (no timing difference).
func (s *Service) verify(ctx context.Context, c Credential, known bool, pw string) (ok, rehash bool, err error) {
	hash := c.Hash
	if !known || hash == "" {
		hash = s.dummy
	}
	ok, rehash, err = s.hasher.Verify(ctx, hash, pw)
	if err != nil {
		return false, false, err
	}
	ok = ok && known && c.Hash != "" && len(pw) <= MaxPasswordBytes
	return ok, ok && rehash, nil
}

// rehash replaces c's hash, made with other parameters, after a successful
// password check. Best effort: a failed rehash leaves the old, still valid
// hash.
func (s *Service) rehash(ctx context.Context, c Credential, pw string) {
	if h, err := s.hasher.Hash(ctx, pw); err == nil {
		_ = s.store.ReplaceHash(ctx, c.UserID, c.Hash, h)
	}
}

// SignIn checks a username and password and starts a session. Every failure
// returns [ErrInvalidCredentials], whatever went wrong, after the same work
// (one lookup, one hash); a blocked account or address returns a
// *RateLimitedError before any hashing. The attempt is reserved in both
// limits before hashing, so concurrent attempts cannot exceed them.
// Successes and failures are audited (a failure never records the submitted
// username). For a user with TOTP enabled, a right password returns a
// second-factor challenge instead of a session; the password step is then
// not audited as a sign-in (the second step is).
func (s *Service) SignIn(ctx context.Context, in SignInInput) (Issued, error) {
	if s.cfg.PasswordSignInDisabled {
		return Issued{}, ErrPasswordSignInDisabled
	}
	username, valid := normalizeUsername(in.Username)
	accKey, addrKey := accountKey(username), AddressKey(in.Address)
	var cred Credential
	err := resource.ErrNotFound
	if valid {
		cred, err = s.store.CredentialByUsername(ctx, username)
	}
	known := err == nil
	if err != nil && !errors.Is(err, resource.ErrNotFound) {
		return Issued{}, err
	}
	now := s.now().UTC()
	at, err := s.begin(accKey, addrKey, func() error {
		return s.rec.Record(ctx, audit.SignIn(actorFor(cred, known), false, "password", "locked_out", now))
	})
	if err != nil {
		return Issued{}, err
	}
	defer at.done()
	ok, rehash, err := s.verify(ctx, cred, known, in.Password)
	if err != nil {
		return Issued{}, err
	}
	if !ok || cred.Disabled {
		at.failed()
		reason := "invalid_credentials"
		if ok && cred.Disabled {
			reason = "account_disabled"
		}
		if err := s.rec.Record(ctx, audit.SignIn(actorFor(cred, known), false, "password", reason, now)); err != nil {
			return Issued{}, err
		}
		return Issued{}, ErrInvalidCredentials
	}
	st, err := s.store.TwoFactor(ctx, cred.UserID)
	if err != nil {
		return Issued{}, err
	}
	if rehash {
		s.rehash(ctx, cred, in.Password)
	}
	if st.Enabled() {
		at.passed()
		exp := now.Add(ChallengeTTL)
		tok := s.challenges.add(now, &challenge{userID: cred.UserID, accKey: accKey, expires: exp, userAgent: in.UserAgent, method: SessionMethodPassword})
		return Issued{SecondFactor: &SecondFactorChallenge{Token: tok, ExpiresAt: exp, Methods: []string{MethodTOTP, MethodRecoveryCode}}}, nil
	}
	at.succeeded()
	return s.startSession(ctx, cred.UserID, SessionMethodPassword, in.Address, in.UserAgent, now, now, audit.SignIn(actorFor(cred, true), true, "password", "", now))
}

// startSession creates a session for userID, started by method, and records
// ev with it. authAt is when the user last proved who they are (it starts the
// recent-authentication window): now for a password or a recovery link, the
// provider's auth_time for single sign-on.
func (s *Service) startSession(ctx context.Context, userID, method, address, userAgent string, now, authAt time.Time, ev audit.Event) (Issued, error) {
	// Expired sessions of anyone are removed here, so the table stays small
	// without a background job.
	if _, err := s.store.DeleteExpired(ctx, now, now.Add(-s.cfg.IdleTimeout)); err != nil {
		return Issued{}, err
	}
	tok := newSecret()
	iss := Issued{Token: tok, CSRFToken: CSRFToken(tok)}
	iss.Session = Session{ID: s.newID(), UserID: userID, TokenHash: hashSecret(tok),
		CreatedAt: now, LastSeenAt: now, AuthenticatedAt: authAt, ExpiresAt: now.Add(s.cfg.AbsoluteTimeout),
		UserAgent: clean(userAgent, 256), Address: clean(address, 64), Method: method}
	if err := s.store.CreateSession(ctx, iss.Session, ev); err != nil {
		return Issued{}, err
	}
	return iss, nil
}

// touchEvery is how often LastSeenAt is written: a quarter of the idle
// timeout, at most a minute.
func (s *Service) touchEvery() time.Duration { return min(time.Minute, s.cfg.IdleTimeout/4) }

// MaxTokenLength bounds a presented session or CSRF token.
const MaxTokenLength = 128

// Authenticate returns the session of token and its user's current principal
// (with SessionID and AuthTime set, and EnrollmentRequired when
// auth.require_2fa confines the user to enrolling a second factor). It returns [ErrInvalidSession] for an
// unknown, ended, idle or expired session, or a user who can no longer act;
// an expired session is deleted. Termination therefore takes effect on the
// next request: there is no cache.
func (s *Service) Authenticate(ctx context.Context, token string) (Session, authz.Principal, error) {
	if token == "" || len(token) > MaxTokenLength {
		return Session{}, authz.Principal{}, ErrInvalidSession
	}
	sess, err := s.store.SessionByTokenHash(ctx, hashSecret(token))
	if errors.Is(err, resource.ErrNotFound) {
		return Session{}, authz.Principal{}, ErrInvalidSession
	}
	if err != nil {
		return Session{}, authz.Principal{}, err
	}
	now := s.now().UTC()
	if !now.Before(sess.ExpiresAt) || now.Sub(sess.LastSeenAt) >= s.cfg.IdleTimeout {
		if err := s.store.DeleteSession(ctx, sess.UserID, sess.ID); err != nil && !errors.Is(err, resource.ErrNotFound) {
			return Session{}, authz.Principal{}, err
		}
		return Session{}, authz.Principal{}, ErrInvalidSession
	}
	p, err := s.principals.Principal(ctx, sess.UserID)
	if errors.Is(err, users.ErrDisabled) || errors.Is(err, resource.ErrNotFound) {
		return Session{}, authz.Principal{}, ErrInvalidSession
	}
	if err != nil {
		return Session{}, authz.Principal{}, err
	}
	if now.Sub(sess.LastSeenAt) >= s.touchEvery() {
		if err := s.store.TouchSession(ctx, sess.ID, now); err != nil {
			return Session{}, authz.Principal{}, err
		}
		sess.LastSeenAt = now
	}
	if p.EnrollmentRequired, err = s.sessionConfined(ctx, sess); err != nil {
		return Session{}, authz.Principal{}, err
	}
	p.SessionID, p.AuthTime = sess.ID, sess.AuthenticatedAt
	return sess, p, nil
}

// CSRFToken is the CSRF token of the session whose cookie token is
// sessionToken: HMAC-SHA256 keyed by the session token (OWASP's HMAC-based
// token pattern). A page on another origin cannot compute it without the
// HttpOnly cookie, the server needs no storage for it, and it changes with
// every session.
func CSRFToken(sessionToken string) string {
	m := hmac.New(sha256.New, []byte(sessionToken))
	m.Write([]byte("ledger csrf v1"))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// VerifyCSRF reports, in constant time, whether csrf is the CSRF token of the
// session whose cookie token is sessionToken.
func VerifyCSRF(sessionToken, csrf string) bool {
	if sessionToken == "" || csrf == "" || len(csrf) > MaxTokenLength {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(CSRFToken(sessionToken)), []byte(csrf)) == 1
}

// Recent reports whether p proved its password within the re-authentication
// window. Only session principals can; API tokens are never "recent" and the
// HTTP layer exempts them by design (ADR-0026).
func (s *Service) Recent(p authz.Principal) bool {
	if p.Kind != authz.PrincipalUser || p.SessionID == "" || p.AuthTime.IsZero() {
		return false
	}
	age := s.now().Sub(p.AuthTime)
	return age >= -time.Minute && age < s.cfg.ReauthWindow
}

// Reauthenticate checks the password of p's user again and, when it matches,
// marks the session as recently authenticated. Failures count towards the
// same limits as sign-in and return [ErrInvalidCredentials].
func (s *Service) Reauthenticate(ctx context.Context, p authz.Principal, password, address string) (time.Time, error) {
	if s.cfg.PasswordSignInDisabled {
		return time.Time{}, ErrPasswordSignInDisabled
	}
	if p.Kind != authz.PrincipalUser || p.SessionID == "" {
		return time.Time{}, ErrInvalidSession
	}
	cred, err := s.store.CredentialByUser(ctx, p.UserID)
	if errors.Is(err, resource.ErrNotFound) {
		return time.Time{}, ErrInvalidSession
	}
	if err != nil {
		return time.Time{}, err
	}
	actor := audit.Actor{Kind: audit.ActorUser, UserID: p.UserID}
	ev := func(a audit.Action, d map[string]string, at time.Time) audit.Event {
		return audit.Event{Action: a, Actor: actor, TargetType: "user", TargetID: p.UserID, At: at, Details: d}
	}
	now := s.now().UTC()
	accKey, addrKey := accountKey(cred.Username), AddressKey(address)
	at, err := s.begin(accKey, addrKey, func() error {
		return s.rec.Record(ctx, ev(audit.ReauthenticationFailed, map[string]string{"method": "password", "reason": "locked_out"}, now))
	})
	if err != nil {
		return time.Time{}, err
	}
	defer at.done()
	ok, rehash, err := s.verify(ctx, cred, true, password)
	if err != nil {
		return time.Time{}, err
	}
	if !ok || cred.Disabled {
		at.failed()
		if err := s.rec.Record(ctx, ev(audit.ReauthenticationFailed, map[string]string{"method": "password", "reason": "invalid_credentials"}, now)); err != nil {
			return time.Time{}, err
		}
		return time.Time{}, ErrInvalidCredentials
	}
	at.succeeded()
	if rehash {
		s.rehash(ctx, cred, password)
	}
	err = s.store.MarkAuthenticated(ctx, p.SessionID, now, ev(audit.Reauthenticated, map[string]string{"method": "password"}, now))
	if errors.Is(err, resource.ErrNotFound) {
		return time.Time{}, ErrInvalidSession
	}
	return now, err
}

// Who changed a password, for the audit entry.
const (
	ByUser          = "self"
	ByAdministrator = "administrator"
	ByHost          = "host"
)

// SetPassword sets the password of user target after checking the policy,
// and ends every session of that user except the caller's own current
// session (when the caller changes their own password). by is [ByUser],
// [ByAdministrator] or [ByHost]. It returns how many sessions ended. The
// policy error is a *PolicyError; an unknown user is resource.ErrNotFound.
func (s *Service) SetPassword(ctx context.Context, actor authz.Principal, system bool, target, password, by string) (int, error) {
	if err := s.cfg.Policy.Check(password); err != nil {
		return 0, err
	}
	hash, err := s.hasher.Hash(ctx, password)
	if err != nil {
		return 0, err
	}
	keep := ""
	a := audit.System
	if !system {
		a = audit.ActorOf(actor)
		if actor.UserID == target && actor.SessionID != "" {
			keep = actor.SessionID
		}
	}
	now := s.now().UTC()
	return s.store.SetPassword(ctx, target, hash, keep, now, func(ended int) []audit.Event {
		return []audit.Event{{Action: audit.PasswordChanged, Actor: a, TargetType: "user", TargetID: target, At: now,
			Details: map[string]string{"by": by, "sessions_ended": strconv.Itoa(ended)}}}
	})
}

// Sessions lists the active sessions of a user, newest first. Sessions past
// a timeout but not yet deleted are left out.
func (s *Service) Sessions(ctx context.Context, userID string) ([]Session, error) {
	all, err := s.store.ListSessions(ctx, userID)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	out := all[:0]
	for _, x := range all {
		if now.Before(x.ExpiresAt) && now.Sub(x.LastSeenAt) < s.cfg.IdleTimeout {
			out = append(out, x)
		}
	}
	return out, nil
}

// EndSession ends session id of p's user: signing out when it is p's own
// session, terminating it otherwise. It takes effect on that session's next
// request. resource.ErrNotFound when the user has no such session.
func (s *Service) EndSession(ctx context.Context, p authz.Principal, id string) error {
	reason := "terminated"
	if id == p.SessionID {
		reason = "signed_out"
	}
	now := s.now().UTC()
	return s.store.DeleteSession(ctx, p.UserID, id, audit.Event{Action: audit.SessionEnded, Actor: audit.ActorOf(p),
		TargetType: "session", TargetID: id, At: now, Details: map[string]string{"reason": reason}})
}

func newSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // never fails (Go 1.24+)
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashSecret(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// clean keeps at most n bytes of valid, printable UTF-8.
func clean(s string, n int) string {
	var b strings.Builder
	for _, r := range s {
		if r == utf8.RuneError || r < 0x20 || r == 0x7f {
			continue
		}
		if b.Len()+utf8.RuneLen(r) > n {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}
