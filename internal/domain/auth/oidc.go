package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/domain/users"
)

// OIDCPolicy is the single-sign-on policy (task A6, ADR-0033, decision D16).
// The protocol itself (discovery, PKCE, token validation) is internal/oidc;
// this package decides what a validated identity may do.
type OIDCPolicy struct {
	Enabled bool
	// Issuer is the configured issuer. Links are keyed by (issuer, subject),
	// so changing the issuer leaves every existing link unusable.
	Issuer string
	// Provider is the provider's name in audit entries (auth.oidc.name).
	Provider string
	// Require2FA asks a user with TOTP enabled for the second factor after an
	// OIDC sign-in too, and applies auth.require_2fa to OIDC sessions. Off by
	// default: Ledger trusts the provider's authentication (D16).
	Require2FA bool
	// Provisioning creates an account, with ProvisionRole, for an identity
	// that is not linked to any. Off by default.
	Provisioning  bool
	ProvisionRole authz.Role
}

func (p OIDCPolicy) validate() error {
	if !p.Enabled {
		return nil
	}
	switch {
	case p.Issuer == "":
		return errors.New("auth: OIDC needs an issuer")
	case p.Provisioning && !p.ProvisionRole.Valid():
		return errors.New("auth: OIDC provisioning needs a valid role")
	}
	return nil
}

// OIDCIdentity is a validated identity from the provider. Only Issuer and
// Subject identify the user; the email address and names are display data
// (and a provisioned account's suggested username), never a key: an address
// can be reassigned to someone else.
type OIDCIdentity struct {
	Issuer            string
	Subject           string
	Email             string
	PreferredUsername string
	Name              string
	// AuthTime is when the user last authenticated at the provider (zero when
	// the provider did not say): it sets how recent the new session counts.
	AuthTime time.Time
}

// OIDCLink ties one Ledger account to one provider identity. An account has
// at most one link and an identity belongs to at most one account.
type OIDCLink struct {
	ID      string
	UserID  string
	Issuer  string
	Subject string
	// Email is the address the provider last reported, for display only.
	Email      string
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

// RecoverySignIn is a one-time sign-in issued from the host (break-glass,
// decision D16). Only the hash of its token is stored.
type RecoverySignIn struct {
	UserID    string
	TokenHash string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// OIDCStore is the persistence of links and recovery sign-ins;
// internal/store/auth implements it.
type OIDCStore interface {
	// OIDCLinkBySubject and OIDCLinkByUser return resource.ErrNotFound when
	// there is no such link.
	OIDCLinkBySubject(ctx context.Context, issuer, subject string) (OIDCLink, error)
	OIDCLinkByUser(ctx context.Context, userID string) (OIDCLink, error)
	// CreateOIDCLink returns [ErrOIDCAlreadyLinked] when the user already has
	// a link or the identity belongs to another account.
	CreateOIDCLink(ctx context.Context, l OIDCLink, events ...audit.Event) error
	// DeleteOIDCLink removes the user's link (resource.ErrNotFound when none).
	DeleteOIDCLink(ctx context.Context, userID string, events ...audit.Event) error
	// TouchOIDCLink records a sign-in with the link and the email address
	// reported then.
	TouchOIDCLink(ctx context.Context, id string, at time.Time, email string) error
	// ReplaceRecoverySignIn stores r as the user's only recovery sign-in.
	ReplaceRecoverySignIn(ctx context.Context, r RecoverySignIn, events ...audit.Event) error
	// TakeRecoverySignIn deletes and returns the recovery sign-in with this
	// token hash (resource.ErrNotFound when none), so a token works once.
	TakeRecoverySignIn(ctx context.Context, tokenHash string) (RecoverySignIn, error)
}

// UserCreator creates accounts for OIDC provisioning; users.Store (the
// user store) implements it.
type UserCreator interface {
	Create(ctx context.Context, actor audit.Actor, n users.New) (users.User, error)
}

// OIDC errors.
var (
	ErrOIDCNotConfigured = errors.New("single sign-on is not configured")
	// ErrOIDCNotLinked: the identity belongs to no account and provisioning
	// is off. Nothing about which accounts exist is revealed.
	ErrOIDCNotLinked = errors.New("this identity is not linked to a Ledger account")
	// ErrOIDCAlreadyLinked: the account already has a link (unlink it
	// first), or the identity is linked to another account.
	ErrOIDCAlreadyLinked = errors.New("the account already has a single-sign-on link, or this identity is linked to another account")
	// ErrOIDCIdentityMismatch: a re-authentication came back with another
	// identity than the one linked to the account.
	ErrOIDCIdentityMismatch = errors.New("the identity provider signed in a different identity than the one linked to this account")
	// ErrLastSignInMethod: unlinking would leave the account without a way
	// to sign in (no password, or password sign-in disabled).
	ErrLastSignInMethod = errors.New("unlinking would leave the account without a sign-in method; set a password first (password sign-in must be enabled)")
)

// Recovery sign-in limits (ADR-0033, proposed).
const (
	DefaultRecoveryTTL = 15 * time.Minute
	MaxRecoveryTTL     = time.Hour
)

func (s *Service) oidcEvent(a audit.Action, actor audit.Actor, userID, by string, at time.Time) audit.Event {
	return audit.Event{Action: a, Actor: actor, TargetType: "user", TargetID: userID, At: at,
		Details: map[string]string{"provider": s.cfg.OIDC.Provider, "by": by}}
}

// Who linked or unlinked an identity, for the audit entry.
const (
	LinkedBySelf         = "self"
	LinkedByProvisioning = "provisioning"
)

// OIDCSignInInput is a validated identity from a sign-in callback.
type OIDCSignInInput struct {
	Identity  OIDCIdentity
	Address   string
	UserAgent string
}

// SignInOIDC starts a session for the account linked to the identity. An
// identity linked to no account is refused with [ErrOIDCNotLinked] unless
// provisioning is on, which creates the account and its link first. A
// disabled account is [ErrInvalidCredentials]. With auth.oidc.require_2fa on
// and TOTP enabled, a second-factor challenge is returned instead of a
// session, exactly as after a password. Successes and failures are audited
// with method oidc.
func (s *Service) SignInOIDC(ctx context.Context, in OIDCSignInInput) (Issued, error) {
	if !s.cfg.OIDC.Enabled {
		return Issued{}, ErrOIDCNotConfigured
	}
	id := in.Identity
	now := s.now().UTC()
	anon := audit.Actor{Kind: audit.ActorAnonymous}
	if id.Issuer != s.cfg.OIDC.Issuer || id.Subject == "" {
		return Issued{}, errors.New("auth: the identity is not from the configured issuer")
	}
	link, err := s.store.OIDCLinkBySubject(ctx, id.Issuer, id.Subject)
	if errors.Is(err, resource.ErrNotFound) {
		if !s.cfg.OIDC.Provisioning {
			if err := s.rec.Record(ctx, audit.SignIn(anon, false, "oidc", "identity_not_linked", now)); err != nil {
				return Issued{}, err
			}
			return Issued{}, ErrOIDCNotLinked
		}
		link, err = s.provisionOIDC(ctx, id, now)
	}
	if err != nil {
		return Issued{}, err
	}
	// A sign-in is only as recent as the provider's own authentication: a
	// silent single-sign-on round trip must not open the recent-authentication
	// window. No auth_time counts as not recent.
	authAt := now.Add(-s.cfg.ReauthWindow)
	if !id.AuthTime.IsZero() {
		if authAt = id.AuthTime.UTC(); authAt.After(now) {
			authAt = now
		}
	}
	cred, err := s.store.CredentialByUser(ctx, link.UserID)
	if err != nil {
		return Issued{}, err
	}
	actor := audit.Actor{Kind: audit.ActorUser, UserID: cred.UserID}
	if cred.Disabled {
		if err := s.rec.Record(ctx, audit.SignIn(actor, false, "oidc", "account_disabled", now)); err != nil {
			return Issued{}, err
		}
		return Issued{}, ErrInvalidCredentials
	}
	if err := s.store.TouchOIDCLink(ctx, link.ID, now, clean(id.Email, 254)); err != nil {
		return Issued{}, err
	}
	if s.cfg.OIDC.Require2FA {
		st, err := s.store.TwoFactor(ctx, cred.UserID)
		if err != nil {
			return Issued{}, err
		}
		if st.Enabled() {
			exp := now.Add(ChallengeTTL)
			tok := s.challenges.add(now, &challenge{userID: cred.UserID, accKey: accountKey(cred.Username), expires: exp,
				userAgent: in.UserAgent, method: SessionMethodOIDC, authAt: authAt})
			return Issued{SecondFactor: &SecondFactorChallenge{Token: tok, ExpiresAt: exp, Methods: []string{MethodTOTP, MethodRecoveryCode}}}, nil
		}
	}
	return s.startSession(ctx, cred.UserID, SessionMethodOIDC, in.Address, in.UserAgent, now, authAt, audit.SignIn(actor, true, "oidc", "", now))
}

// provisionOIDC creates an account (role auth.oidc.provisioning.role, no
// password) and links the identity to it. Provisioning runs one at a time,
// so two callbacks for the same new identity cannot both create an account.
func (s *Service) provisionOIDC(ctx context.Context, id OIDCIdentity, now time.Time) (OIDCLink, error) {
	s.provision.Lock()
	defer s.provision.Unlock()
	if l, err := s.store.OIDCLinkBySubject(ctx, id.Issuer, id.Subject); err == nil {
		return l, nil // provisioned by a concurrent callback
	} else if !errors.Is(err, resource.ErrNotFound) {
		return OIDCLink{}, err
	}
	var display *string
	if n := clean(id.Name, users.MaxDisplayName*4); users.ValidateDisplayName(&n) == nil {
		display = &n
	}
	var u users.User
	var err error
	for _, name := range usernameCandidates(id) {
		u, err = s.users.Create(ctx, audit.System, users.New{Username: name, DisplayName: display, Role: s.cfg.OIDC.ProvisionRole})
		if !errors.Is(err, users.ErrUsernameTaken) {
			break
		}
	}
	if err != nil {
		return OIDCLink{}, err
	}
	l := OIDCLink{ID: s.newID(), UserID: u.ID, Issuer: id.Issuer, Subject: id.Subject, Email: clean(id.Email, 254), CreatedAt: now}
	if err := s.store.CreateOIDCLink(ctx, l, s.oidcEvent(audit.OIDCLinked, audit.System, u.ID, LinkedByProvisioning, now)); err != nil {
		return OIDCLink{}, err
	}
	return l, nil
}

// usernameCandidates are the usernames a provisioned account tries in order:
// the provider's preferred username, then the local part of the email
// address (both reduced to the username alphabet), each with -2 to -9 when
// taken, and finally a name derived from the subject, which is unique.
func usernameCandidates(id OIDCIdentity) []string {
	var out []string
	seen := map[string]bool{}
	for _, base := range []string{id.PreferredUsername, strings.SplitN(id.Email, "@", 2)[0]} {
		b := usernameBase(base)
		if b == "" || seen[b] {
			continue
		}
		seen[b] = true
		out = append(out, b)
		for i := 2; i <= 9; i++ {
			out = append(out, b+"-"+strconv.Itoa(i))
		}
	}
	sum := sha256.Sum256([]byte(id.Issuer + "\x00" + id.Subject))
	return append(out, "oidc-"+hex.EncodeToString(sum[:8]))
}

// usernameBase reduces s to the username alphabet (lower-case letters,
// digits, '.', '_', '-', starting with a letter or digit), at most 56 bytes so
// a suffix fits; "" when nothing usable is left.
func usernameBase(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case (r == '.' || r == '_' || r == '-') && b.Len() > 0:
			b.WriteRune(r)
		}
		if b.Len() >= users.MaxUsername-8 {
			break
		}
	}
	out := strings.TrimRight(b.String(), "._-")
	if users.ValidateUsername(out) != nil {
		return ""
	}
	return out
}

// sessionOf returns the live session id of userID.
func (s *Service) sessionOf(ctx context.Context, userID, id string) error {
	list, err := s.Sessions(ctx, userID)
	if err != nil {
		return err
	}
	for _, x := range list {
		if x.ID == id {
			return nil
		}
	}
	return ErrInvalidSession
}

// LinkOIDC links the identity to the account of the user who started the
// link from session sessionID (which must still be live). An account has at
// most one link: replacing it means unlinking first. Linking never disables
// password sign-in. Audited as oidc.linked (by self).
func (s *Service) LinkOIDC(ctx context.Context, userID, sessionID string, id OIDCIdentity) (OIDCLink, error) {
	if !s.cfg.OIDC.Enabled {
		return OIDCLink{}, ErrOIDCNotConfigured
	}
	if id.Issuer != s.cfg.OIDC.Issuer || id.Subject == "" {
		return OIDCLink{}, errors.New("auth: the identity is not from the configured issuer")
	}
	if err := s.sessionOf(ctx, userID, sessionID); err != nil {
		return OIDCLink{}, err
	}
	if _, err := s.principals.Principal(ctx, userID); err != nil {
		if errors.Is(err, users.ErrDisabled) || errors.Is(err, resource.ErrNotFound) {
			return OIDCLink{}, ErrInvalidSession
		}
		return OIDCLink{}, err
	}
	now := s.now().UTC()
	l := OIDCLink{ID: s.newID(), UserID: userID, Issuer: id.Issuer, Subject: id.Subject, Email: clean(id.Email, 254), CreatedAt: now}
	actor := audit.Actor{Kind: audit.ActorUser, UserID: userID}
	if err := s.store.CreateOIDCLink(ctx, l, s.oidcEvent(audit.OIDCLinked, actor, userID, LinkedBySelf, now)); err != nil {
		return OIDCLink{}, err
	}
	return l, nil
}

// OIDCLinkOf returns the user's link (resource.ErrNotFound when none).
func (s *Service) OIDCLinkOf(ctx context.Context, userID string) (OIDCLink, error) {
	return s.store.OIDCLinkByUser(ctx, userID)
}

// HasPasswordSignIn reports whether userID can sign in with a password:
// password sign-in is enabled and the account has a password.
func (s *Service) HasPasswordSignIn(ctx context.Context, userID string) (bool, error) {
	if s.cfg.PasswordSignInDisabled {
		return false, nil
	}
	c, err := s.store.CredentialByUser(ctx, userID)
	if err != nil {
		return false, err
	}
	return c.Hash != "", nil
}

// UnlinkOIDC removes the link of p's user (the HTTP route requires recent
// authentication) or, with system set, of target from the host. Unlinking
// by the user is refused with [ErrLastSignInMethod] when no other sign-in
// method would remain; the host may always unlink (break-glass), since it can
// also set a password. Sessions already started stay. Audited as
// oidc.unlinked (by self or host).
func (s *Service) UnlinkOIDC(ctx context.Context, p authz.Principal, system bool, target string) error {
	if !system {
		target = p.UserID
		ok, err := s.HasPasswordSignIn(ctx, target)
		if err != nil {
			return err
		}
		if _, err := s.store.OIDCLinkByUser(ctx, target); err != nil {
			return err
		}
		if !ok {
			return ErrLastSignInMethod
		}
	}
	actor, by := audit.System, ByHost
	if !system {
		actor, by = audit.ActorOf(p), LinkedBySelf
	}
	return s.store.DeleteOIDCLink(ctx, target, s.oidcEvent(audit.OIDCUnlinked, actor, target, by, s.now().UTC()))
}

// ReauthenticateOIDC marks session sessionID of userID as recently
// authenticated after the provider authenticated the user again and returned
// the identity linked to the account. Another identity is
// [ErrOIDCIdentityMismatch]. Audited as auth.reauthenticated (method oidc)
// or auth.reauthentication_failed.
func (s *Service) ReauthenticateOIDC(ctx context.Context, userID, sessionID string, id OIDCIdentity) (time.Time, error) {
	if !s.cfg.OIDC.Enabled {
		return time.Time{}, ErrOIDCNotConfigured
	}
	now := s.now().UTC()
	actor := audit.Actor{Kind: audit.ActorUser, UserID: userID}
	ev := func(a audit.Action, d map[string]string) audit.Event {
		return audit.Event{Action: a, Actor: actor, TargetType: "user", TargetID: userID, At: now, Details: d}
	}
	l, err := s.store.OIDCLinkByUser(ctx, userID)
	if err != nil && !errors.Is(err, resource.ErrNotFound) {
		return time.Time{}, err
	}
	if err != nil || l.Issuer != id.Issuer || l.Subject != id.Subject {
		if err := s.rec.Record(ctx, ev(audit.ReauthenticationFailed, map[string]string{"method": "oidc", "reason": "identity_mismatch"})); err != nil {
			return time.Time{}, err
		}
		return time.Time{}, ErrOIDCIdentityMismatch
	}
	if err := s.sessionOf(ctx, userID, sessionID); err != nil {
		return time.Time{}, err
	}
	err = s.store.MarkAuthenticated(ctx, sessionID, now, ev(audit.Reauthenticated, map[string]string{"method": "oidc"}))
	if errors.Is(err, resource.ErrNotFound) {
		return time.Time{}, ErrInvalidSession
	}
	return now, err
}

// IssueRecoverySignIn creates a one-time sign-in for userID, valid for ttl
// (at most [MaxRecoveryTTL]), and returns its token. It is the host's
// break-glass path (decision D16) when the identity provider is unavailable
// or password sign-in is disabled; it replaces any earlier unused one and is
// audited as auth.recovery_sign_in_issued (by host). The token is returned
// once and never stored.
func (s *Service) IssueRecoverySignIn(ctx context.Context, userID string, ttl time.Duration) (string, time.Time, error) {
	if ttl <= 0 || ttl > MaxRecoveryTTL {
		return "", time.Time{}, errors.New("auth: a recovery sign-in must be valid for more than 0 and at most " + MaxRecoveryTTL.String())
	}
	cred, err := s.store.CredentialByUser(ctx, userID)
	if err != nil {
		return "", time.Time{}, err
	}
	if cred.Disabled {
		return "", time.Time{}, users.ErrDisabled
	}
	now := s.now().UTC()
	tok := newSecret()
	exp := now.Add(ttl)
	ev := audit.Event{Action: audit.RecoverySignInIssued, Actor: audit.System, TargetType: "user", TargetID: userID, At: now,
		Details: map[string]string{"by": ByHost, "expires_at": exp.Format(time.RFC3339)}}
	if err := s.store.ReplaceRecoverySignIn(ctx, RecoverySignIn{UserID: userID, TokenHash: hashSecret(tok), CreatedAt: now, ExpiresAt: exp}, ev); err != nil {
		return "", time.Time{}, err
	}
	return tok, exp, nil
}

// RecoverySignInInput is the use of a recovery sign-in token.
type RecoverySignInInput struct {
	Token     string
	Address   string
	UserAgent string
}

// RecoverySignIn starts a session with a recovery sign-in token. The token is
// consumed whatever the outcome. It skips the second factor (the host that
// issued it can reset that too) and works whether or not password sign-in is
// enabled; auth.require_2fa still confines the session to enrolment. An
// unknown, used or expired token is [ErrInvalidCredentials]. Audited with
// method recovery_sign_in.
func (s *Service) RecoverySignIn(ctx context.Context, in RecoverySignInInput) (Issued, error) {
	now := s.now().UTC()
	fail := func(actor audit.Actor, reason string) (Issued, error) {
		if err := s.rec.Record(ctx, audit.SignIn(actor, false, "recovery_sign_in", reason, now)); err != nil {
			return Issued{}, err
		}
		return Issued{}, ErrInvalidCredentials
	}
	anon := audit.Actor{Kind: audit.ActorAnonymous}
	if in.Token == "" || len(in.Token) > MaxTokenLength {
		return fail(anon, "invalid_credentials")
	}
	r, err := s.store.TakeRecoverySignIn(ctx, hashSecret(in.Token))
	if errors.Is(err, resource.ErrNotFound) {
		return fail(anon, "invalid_credentials")
	}
	if err != nil {
		return Issued{}, err
	}
	actor := audit.Actor{Kind: audit.ActorUser, UserID: r.UserID}
	if !now.Before(r.ExpiresAt) {
		return fail(actor, "invalid_credentials")
	}
	cred, err := s.store.CredentialByUser(ctx, r.UserID)
	if err != nil {
		return Issued{}, err
	}
	if cred.Disabled {
		return fail(actor, "account_disabled")
	}
	return s.startSession(ctx, r.UserID, SessionMethodRecovery, in.Address, in.UserAgent, now, now, audit.SignIn(actor, true, "recovery_sign_in", "", now))
}

// OIDCResponseRejected records a sign-in callback whose provider response
// failed validation (a bad signature, audience, nonce, expiry and so on):
// auth.signin_failed, method oidc, reason invalid_credentials, anonymous
// because no identity could be trusted.
func (s *Service) OIDCResponseRejected(ctx context.Context) error {
	return s.rec.Record(ctx, audit.SignIn(audit.Actor{Kind: audit.ActorAnonymous}, false, "oidc", "invalid_credentials", s.now().UTC()))
}

// SessionConfined reports whether the session is confined to enrolling a
// second factor (see [Service.Authenticate]).
func (s *Service) SessionConfined(ctx context.Context, sess Session) (bool, error) {
	return s.sessionConfined(ctx, sess)
}
