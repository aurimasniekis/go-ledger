package auth

import (
	"context"
	"errors"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
)

// Device cookies (decision D23-1b, ADR-0026 amendment). A browser that
// completes a password sign-in gets a device cookie: a random token bound to
// the user who signed in, stored only as its SHA-256 hash (OWASP "device
// cookie"). While the cookie is valid for the submitted account, failed
// password checks and second-factor codes from that browser count against the
// cookie ("device:<id>" in the account limiter) instead of the username, so a
// username locked out by someone else's guesses does not lock out the owner's
// known browsers. The per-address limit applies unchanged, and the password
// and the second factor are always checked: the cookie only chooses which
// failure counter applies.
//
// A cookie is replaced (rotated) at every complete password sign-in, expires
// [Config.DeviceCookieTTL] after it was issued, and every cookie of a user is
// deleted when the user's password is set.

// DefaultDeviceCookieTTL is the proposed lifetime of a device cookie (not an
// operator decision; auth.sign_in.device_cookie_ttl). Each sign-in from the
// browser replaces the cookie, so a browser used at least this often stays
// known.
const DefaultDeviceCookieTTL = 90 * 24 * time.Hour

// MaxDeviceCookies is how many device cookies a user keeps; issuing one more
// deletes the oldest.
const MaxDeviceCookies = 20

// DeviceCookie is a known browser of a user. The cookie holds a random
// token; only its hash is stored.
type DeviceCookie struct {
	ID     string
	UserID string
	// TokenHash is the hex SHA-256 hash of the cookie token.
	TokenHash string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// DeviceStore persists device cookies. Store.SetPassword also deletes every
// device cookie of the user.
type DeviceStore interface {
	// DeviceCookieByTokenHash returns resource.ErrNotFound for no such cookie.
	DeviceCookieByTokenHash(ctx context.Context, tokenHash string) (DeviceCookie, error)
	// IssueDeviceCookie inserts d, deletes cookie replace of the same user
	// (the cookie being rotated; may be empty) and the user's expired cookies
	// (expires_at at or before d.CreatedAt), and then the user's oldest cookies
	// beyond keep, in one transaction.
	IssueDeviceCookie(ctx context.Context, d DeviceCookie, replace string, keep int) error
}

func deviceKey(id string) string { return "device:" + id }

// knownDevice returns the device cookie of token when it is valid for
// userID now. A missing, unknown (forged), expired or other user's cookie is
// not valid. The cookie is looked up whenever one is presented, before the
// account is known to exist, so the lookup does not tell whether the
// username exists.
func (s *Service) knownDevice(ctx context.Context, token string, userID string, known bool, now time.Time) (DeviceCookie, bool, error) {
	if token == "" || len(token) > MaxTokenLength {
		return DeviceCookie{}, false, nil
	}
	d, err := s.store.DeviceCookieByTokenHash(ctx, hashSecret(token))
	if errors.Is(err, resource.ErrNotFound) {
		return DeviceCookie{}, false, nil
	}
	if err != nil {
		return DeviceCookie{}, false, err
	}
	if !known || d.UserID != userID || !now.Before(d.ExpiresAt) {
		return DeviceCookie{}, false, nil
	}
	return d, true, nil
}

// issueDevice gives the browser a new device cookie for userID, replacing
// the one it presented (replace, may be empty). Best effort: when it fails,
// the sign-in still succeeds and the presented cookie stays as it was (the
// rotation is one transaction), so the browser is only as known as before.
func (s *Service) issueDevice(ctx context.Context, iss *Issued, userID, replace string, now time.Time) {
	tok := newSecret()
	d := DeviceCookie{ID: s.newID(), UserID: userID, TokenHash: hashSecret(tok), CreatedAt: now, ExpiresAt: now.Add(s.cfg.DeviceCookieTTL)}
	if err := s.store.IssueDeviceCookie(ctx, d, replace, MaxDeviceCookies); err != nil {
		// The token is never logged: it is the credential. The error is the
		// store's, and the browser stays as known as before.
		s.log.WarnContext(ctx, "device cookie not issued; the sign-in still succeeded", "user_id", userID, "error", err.Error())
		return
	}
	iss.DeviceToken, iss.DeviceExpiresAt = tok, d.ExpiresAt
}
