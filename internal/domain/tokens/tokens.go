// Package tokens is Ledger's API tokens (task A4, ADR-0029): their format,
// how they are issued, checked and revoked, and the rules that keep a token
// from carrying more power than its creator.
//
// A token is "ldgr_" followed by 43 random base62 characters (about 256
// bits). It is shown once, when it is created; Ledger stores only its SHA-256
// hash, plus the prefix with the first four random characters and the last
// four characters so a person can recognise it in a list. A token acts for its
// owner with its own grants, limited on every request by the owner's current
// role (authz.TokenPrincipal); a root token holds whatever the owner's role
// holds, including grants added to Ledger later. Token requests skip
// interactive steps (two-factor, recent re-authentication) by design, and the
// HTTP layer audits each state-changing one (token.write_used). Only a root
// token of the root account (the first user, decision D25) may create tokens;
// every other token is refused.
//
// The package is pure: it reaches storage through [Store] and users through
// [Principals].
package tokens

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/domain/users"
)

// Token format (decision D2: the prefix is "ldgr_").
const (
	// Prefix starts every token, so secret scanners can find leaked ones.
	Prefix = "ldgr_"
	// SecretLength is the number of random base62 characters after Prefix.
	SecretLength = 43
	// Length is the length of a whole token.
	Length = len(Prefix) + SecretLength
	// HintChars is how many random characters are kept, after the prefix and
	// at the end, to recognise a token in a list.
	HintChars = 4
	// MaxNameBytes is the longest token name.
	MaxNameBytes = 100
)

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// RootWarning explains what a root token can do. Interfaces show it whenever
// one is created.
const RootWarning = "This is a root token: it can do everything its owner's role allows, " +
	"including managing users, revoking tokens and reading the audit log, and it gains every permission added to Ledger later. " +
	"Requests made with it skip two-factor and re-authentication. Keep it in a secret manager, prefer a token with only the grants " +
	"it needs, set an expiry where you can, and revoke it when it is no longer needed."

// Token is an API token as stored. It never holds the token itself.
type Token struct {
	ID      string
	Name    string
	OwnerID string
	// Root tokens hold every grant their owner's role holds; Grants is empty.
	Root   bool
	Grants authz.Grants
	// TokenHash is the hex SHA-256 of the token.
	TokenHash string
	// DisplayPrefix is Prefix plus the first HintChars random characters;
	// LastChars the last HintChars.
	DisplayPrefix string
	LastChars     string
	CreatedAt     time.Time
	// ParentTokenID is the root token that created this token through the
	// API ("" for a token made by a user or the host). Revoking the parent
	// revokes this token, and ExpiresAt is never later than the parent's.
	ParentTokenID string
	// ExpiresAt is nil for a token that never expires (the default).
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// Hint is how a token is shown in a list: "ldgr_AbCd…wXyZ".
func (t Token) Hint() string { return t.DisplayPrefix + "…" + t.LastChars }

// Status of a token at a time.
const (
	StatusActive  = "active"
	StatusExpired = "expired"
	StatusRevoked = "revoked"
)

// Status returns the token's status at now.
func (t Token) Status(now time.Time) string {
	switch {
	case t.RevokedAt != nil:
		return StatusRevoked
	case t.ExpiresAt != nil && !now.Before(*t.ExpiresAt):
		return StatusExpired
	}
	return StatusActive
}

// New is what a token is created with.
type New struct {
	Name   string
	Root   bool
	Grants authz.Grants
	// ExpiresAt is optional; nil means the token never expires.
	ExpiresAt *time.Time
}

// ListFilter selects tokens for [Store.List]. Zero fields do not filter.
type ListFilter struct {
	OwnerID string
	After   string
	// Limit is the most tokens to return; the store fetches Limit+1.
	Limit int
}

// Store persists tokens. Writes record their audit events in the same
// transaction.
type Store interface {
	Create(ctx context.Context, t Token, events ...audit.Event) error
	// ByHash returns the token with this hash, or resource.ErrNotFound.
	ByHash(ctx context.Context, hash string) (Token, error)
	// Get returns the token, or resource.ErrNotFound.
	Get(ctx context.Context, id string) (Token, error)
	// List returns tokens in ID order.
	List(ctx context.Context, f ListFilter) ([]Token, error)
	// Revoke sets revoked_at on the token if it is not revoked yet, and
	// writes the events in the same transaction. ownerID, when set, limits it
	// to that owner's tokens. It returns resource.ErrNotFound when no such
	// token exists, and revoked=false (writing nothing) when it already was.
	Revoke(ctx context.Context, id, ownerID string, at time.Time, events ...audit.Event) (revoked bool, err error)
	// Touch records that the token was used. It never moves last use backwards.
	Touch(ctx context.Context, id string, at time.Time) error
}

// Principals finds the current principal of an enabled user; it returns
// users.ErrDisabled or resource.ErrNotFound otherwise. RootAccountID names
// the root account (the first user created, decision D25), or returns
// resource.ErrNotFound when there are no users.
type Principals interface {
	Principal(ctx context.Context, id string) (authz.Principal, error)
	RootAccountID(ctx context.Context) (string, error)
}

// Errors.
var (
	// ErrInvalidToken means a token is malformed, unknown, revoked, expired
	// or belongs to a disabled user. It never says which.
	ErrInvalidToken = errors.New("invalid API token")
	// ErrEscalation means a token would hold more than its creator.
	ErrEscalation = errors.New("a token cannot have grants its creator does not hold")
	// ErrNotUser means the creator is neither a user nor a token.
	ErrNotUser = errors.New("tokens are created by a signed-in user or a root token of the root account")
	// ErrTokenCannotCreate means a token tried to create a token without
	// being a root token of the root account (decision D25).
	ErrTokenCannotCreate = errors.New("only a root token of the root account (the first user) may create API tokens")
)

// FieldError is an invalid field of a new token.
type FieldError struct {
	Field   string
	Message string
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Message }

// EscalationError lists the requested grants the creator does not hold.
type EscalationError struct {
	// Grants are the grants not held; "root" for a root token requested by
	// someone who does not hold every grant.
	Grants []string
}

func (e *EscalationError) Error() string {
	return ErrEscalation.Error() + ": " + strings.Join(e.Grants, ", ")
}

func (e *EscalationError) Unwrap() error { return ErrEscalation }

// Service issues, checks and revokes tokens.
type Service struct {
	store      Store
	principals Principals
	now        func() time.Time
	random     io.Reader
	newID      func() string
	log        *slog.Logger
	touchLimit time.Duration
	touching   sync.Map // token ID -> struct{}: last-use writes in flight
}

// Options are the service's replaceable parts, for tests.
type Options struct {
	Now    func() time.Time
	Random io.Reader
	NewID  func() string
	// Logger receives last-use write failures; slog.Default when nil.
	Logger *slog.Logger
	// TouchTimeout bounds one last-use write; 250ms when zero.
	TouchTimeout time.Duration
}

// NewService returns a Service.
func NewService(st Store, principals Principals, o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Random == nil {
		o.Random = rand.Reader
	}
	if o.NewID == nil {
		o.NewID = resource.NewID
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.TouchTimeout <= 0 {
		o.TouchTimeout = 250 * time.Millisecond
	}
	return &Service{store: st, principals: principals, now: o.Now, random: o.Random, newID: o.NewID, log: o.Logger, touchLimit: o.TouchTimeout}
}

// Now is the service's clock.
func (s *Service) Now() time.Time { return s.now().UTC() }

// Issued is a newly created token: the stored record and the token itself,
// which is never available again.
type Issued struct {
	Token  Token
	Secret string
}

// ValidateName checks a token name: 1 to MaxNameBytes bytes of printable
// UTF-8, without surrounding spaces.
func ValidateName(name string) error {
	switch {
	case name == "":
		return &FieldError{Field: "name", Message: "name is required"}
	case len(name) > MaxNameBytes:
		return &FieldError{Field: "name", Message: fmt.Sprintf("name must be at most %d bytes", MaxNameBytes)}
	case !utf8.ValidString(name) || strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return &FieldError{Field: "name", Message: "name must be printable text"}
	case strings.TrimSpace(name) != name:
		return &FieldError{Field: "name", Message: "name must not start or end with spaces"}
	}
	return nil
}

// Create issues a token owned by creator.UserID and bounded by creator's
// grants. creator is a user principal (the signed-in user, or, for a host
// command, the owner's own principal) or a root token of the root account
// (decision D25); any other token is [ErrTokenCannotCreate]. actor is who the
// audit entry names (the user, the token, or audit.System for the host); a
// token-created token's entry also names the parent token (parent_token_id).
//
// A token may only hold grants the creator holds (no escalation), and only a
// creator holding every grant of the vocabulary may create a root token. For
// a creating token that is its effective grants: its owner's current role.
// The owner's current role is applied again on every request.
func (s *Service) Create(ctx context.Context, actor audit.Actor, creator authz.Principal, n New) (Issued, error) {
	var parent Token
	var err error
	switch {
	case creator.UserID == "":
		return Issued{}, ErrNotUser
	case creator.Kind == authz.PrincipalToken:
		parent, err = s.checkMinter(ctx, creator)
		if err != nil {
			return Issued{}, err
		}
	case creator.Kind != authz.PrincipalUser:
		return Issued{}, ErrNotUser
	}
	if err := ValidateName(n.Name); err != nil {
		return Issued{}, err
	}
	now := s.Now()
	grants, err := authz.NewGrants(n.Grants...)
	if err != nil {
		return Issued{}, &FieldError{Field: "grants", Message: "unknown grant; see GET /grants"}
	}
	switch {
	case n.Root && len(grants) > 0:
		return Issued{}, &FieldError{Field: "grants", Message: "a root token holds every grant; leave grants empty"}
	case !n.Root && len(grants) == 0:
		return Issued{}, &FieldError{Field: "grants", Message: "give at least one grant, or ask for a root token explicitly"}
	case n.Root && !creator.Grants.Covers(authz.AllGrants()):
		return Issued{}, &EscalationError{Grants: []string{"root"}}
	}
	var missing []string
	for _, g := range grants {
		if !creator.Grants.Allows(g) {
			missing = append(missing, g.String())
		}
	}
	if len(missing) > 0 {
		return Issued{}, &EscalationError{Grants: missing}
	}
	var expires *time.Time
	if n.ExpiresAt != nil {
		if problem := ExpiryProblem(*n.ExpiresAt, now); problem != "" {
			return Issued{}, &FieldError{Field: "expires_at", Message: "expires_at " + problem}
		}
		e := n.ExpiresAt.UTC()
		expires = &e
	}
	// Containment (D25-1): a token made by a token never outlives it. A parent
	// that never expires leaves the requested expiry as it is.
	if parent.ExpiresAt != nil && (expires == nil || expires.After(*parent.ExpiresAt)) {
		e := parent.ExpiresAt.UTC()
		expires = &e
	}
	secret, err := s.generate()
	if err != nil {
		return Issued{}, err
	}
	t := Token{ID: s.newID(), Name: n.Name, OwnerID: creator.UserID, Root: n.Root, Grants: grants, TokenHash: Hash(secret),
		DisplayPrefix: secret[:len(Prefix)+HintChars], LastChars: secret[len(secret)-HintChars:], CreatedAt: now, ExpiresAt: expires, ParentTokenID: parent.ID}
	ev := CreatedEvent(actor, t)
	if parent.ID != "" {
		ev.Details["parent_token_id"] = parent.ID
	}
	if err := s.store.Create(ctx, t, ev); err != nil {
		return Issued{}, err
	}
	return Issued{Token: t, Secret: secret}, nil
}

// checkMinter returns the creating token after verifying, from current data, that the token principal p may
// create tokens: p.CanCreateTokens is set, and the token still exists, is a
// root token, and belongs to the root account. Anything else is
// [ErrTokenCannotCreate].
func (s *Service) checkMinter(ctx context.Context, p authz.Principal) (Token, error) {
	if !p.CanCreateTokens || p.TokenID == "" {
		return Token{}, ErrTokenCannotCreate
	}
	t, err := s.store.Get(ctx, p.TokenID)
	if errors.Is(err, resource.ErrNotFound) {
		return Token{}, ErrTokenCannotCreate
	}
	if err != nil {
		return Token{}, err
	}
	if !t.Root || t.OwnerID != p.UserID || t.Status(s.Now()) != StatusActive {
		return Token{}, ErrTokenCannotCreate
	}
	root, err := s.isRootAccount(ctx, t.OwnerID)
	if err != nil {
		return Token{}, err
	}
	if !root {
		return Token{}, ErrTokenCannotCreate
	}
	return t, nil
}

// isRootAccount reports whether userID is the root account.
func (s *Service) isRootAccount(ctx context.Context, userID string) (bool, error) {
	id, err := s.principals.RootAccountID(ctx)
	if errors.Is(err, resource.ErrNotFound) {
		return false, nil
	}
	return id == userID, err
}

// ExpiryProblem says why expires cannot be a token's expiry as of now, in words
// that follow the field name, or is "" when it can. Create applies it, and the
// ledger token CLI applies it before it creates anything, so a refused expiry
// never leaves a user behind.
func ExpiryProblem(expires, now time.Time) string {
	e := expires.UTC()
	switch {
	case !e.After(now):
		return "must be in the future"
	case e.Year() > 9999:
		return "must be before the year 10000 (UTC)"
	}
	return ""
}

// CreatedEvent is the audit event of a new token. It names the grants (or
// root) and the expiry, never the token.
func CreatedEvent(actor audit.Actor, t Token) audit.Event {
	expires := "never"
	if t.ExpiresAt != nil {
		expires = t.ExpiresAt.UTC().Format(time.RFC3339)
	}
	root := "false"
	if t.Root {
		root = "true"
	}
	return audit.ForToken(audit.TokenCreated, actor, t.ID,
		map[string]string{"grants": joinGrants(t.Grants), "root": root, "expires_at": expires}, t.CreatedAt)
}

// joinGrants writes grants comma-separated within the audit value limit,
// ending in ",…" when they do not fit.
func joinGrants(gs authz.Grants) string {
	s := strings.Join(gs.Strings(), ",")
	if len(s) <= audit.MaxValueBytes {
		return s
	}
	cut := strings.LastIndexByte(s[:audit.MaxValueBytes-len(",…")], ',')
	return s[:cut] + ",…"
}

// generate returns a new token: Prefix and SecretLength uniformly random
// base62 characters.
func (s *Service) generate() (string, error) {
	out := make([]byte, 0, Length)
	out = append(out, Prefix...)
	buf := make([]byte, 64)
	for len(out) < Length {
		if _, err := io.ReadFull(s.random, buf); err != nil {
			return "", fmt.Errorf("tokens: random source: %w", err)
		}
		for _, b := range buf {
			// 248 = 4*62: rejecting larger bytes keeps every character
			// equally likely.
			if b < 248 && len(out) < Length {
				out = append(out, base62[b%62])
			}
		}
	}
	return string(out), nil
}

// WellFormed reports whether s has the token format. It does not say whether
// the token exists.
func WellFormed(s string) bool {
	if len(s) != Length || !strings.HasPrefix(s, Prefix) {
		return false
	}
	for i := len(Prefix); i < len(s); i++ {
		if strings.IndexByte(base62, s[i]) < 0 {
			return false
		}
	}
	return true
}

// Hash is the hex SHA-256 of a token. Tokens carry 256 random bits, so a fast
// hash is enough: a copy of the database cannot be turned back into tokens.
func Hash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// touchEvery limits how often last-used is written.
const touchEvery = time.Minute

// Authenticate returns the principal of a request made with secret. It reads
// the token and its owner on every call, so revocation, expiry, disabling the
// owner and role changes apply to the next request. Every failure is
// [ErrInvalidToken]; other errors mean the check could not be made.
func (s *Service) Authenticate(ctx context.Context, secret string) (authz.Principal, error) {
	if !WellFormed(secret) {
		return authz.Principal{}, ErrInvalidToken
	}
	t, err := s.store.ByHash(ctx, Hash(secret))
	if errors.Is(err, resource.ErrNotFound) {
		return authz.Principal{}, ErrInvalidToken
	}
	if err != nil {
		return authz.Principal{}, err
	}
	now := s.Now()
	if t.Status(now) != StatusActive {
		return authz.Principal{}, ErrInvalidToken
	}
	owner, err := s.principals.Principal(ctx, t.OwnerID)
	if errors.Is(err, users.ErrDisabled) || errors.Is(err, resource.ErrNotFound) {
		return authz.Principal{}, ErrInvalidToken
	}
	if err != nil {
		return authz.Principal{}, err
	}
	grants := t.Grants
	if t.Root {
		grants = owner.Role.Grants()
	}
	minter := false
	if t.Root {
		if minter, err = s.isRootAccount(ctx, t.OwnerID); err != nil {
			return authz.Principal{}, err
		}
	}
	if t.LastUsedAt == nil || now.Sub(*t.LastUsedAt) >= touchEvery {
		s.touch(ctx, t.ID, now)
	}
	p := authz.TokenPrincipal(t.ID, t.OwnerID, owner.Role, grants)
	p.CanCreateTokens = minter
	return p, nil
}

// touch records the token's use, best effort: it never fails the request. It
// runs under its own short deadline (not the request's), one write per token
// at a time (a concurrent request skips it), and the store only moves the
// value forward. A failure is logged and the request goes on.
func (s *Service) touch(ctx context.Context, id string, at time.Time) {
	if _, busy := s.touching.LoadOrStore(id, struct{}{}); busy {
		return
	}
	defer s.touching.Delete(id)
	tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.touchLimit)
	defer cancel()
	if err := s.store.Touch(tctx, id, at); err != nil {
		s.log.WarnContext(ctx, "API token last-use update skipped", "token_id", id, "error", err.Error())
	}
}

// Get returns a token. ownerID, when set, limits it to that owner's tokens
// (another owner's token is resource.ErrNotFound).
func (s *Service) Get(ctx context.Context, id, ownerID string) (Token, error) {
	t, err := s.store.Get(ctx, id)
	if err != nil {
		return Token{}, err
	}
	if ownerID != "" && t.OwnerID != ownerID {
		return Token{}, resource.ErrNotFound
	}
	return t, nil
}

// List returns tokens in ID order.
func (s *Service) List(ctx context.Context, f ListFilter) ([]Token, error) {
	return s.store.List(ctx, f)
}

// Revoke revokes a token at once: its next request is refused. ownerID,
// when set, limits it to that owner's tokens. Revoking a token that already
// is revoked changes and records nothing.
func (s *Service) Revoke(ctx context.Context, actor audit.Actor, id, ownerID string) error {
	now := s.Now()
	_, err := s.store.Revoke(ctx, id, ownerID, now, audit.ForToken(audit.TokenRevoked, actor, id, nil, now))
	return err
}
