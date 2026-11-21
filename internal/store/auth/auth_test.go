package auth

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/auth"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/domain/users"
	"github.com/aurimasniekis/go-ledger/internal/store"
	userstore "github.com/aurimasniekis/go-ledger/internal/store/users"
)

const hash1 = "$argon2id$v=19$m=64,t=1,p=1$c2FsdHNhbHRzYWx0c2FsdA$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g"

func setup(t *testing.T) (*Store, *store.DB, *store.AuditLog, string, string) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "ledger.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	al := store.NewAuditLog(db)
	us := userstore.New(db, al, nil, nil)
	a, err := us.Create(ctx, audit.System, users.New{Username: "alice", Role: authz.RoleAdministrator})
	if err != nil {
		t.Fatal(err)
	}
	b, err := us.Create(ctx, audit.System, users.New{Username: "bob", Role: authz.RoleReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	return New(db, al), db, al, a.ID, b.ID
}

func session(id, user, tokenHash string, at time.Time) auth.Session {
	return auth.Session{ID: id, UserID: user, TokenHash: tokenHash, CreatedAt: at, LastSeenAt: at, AuthenticatedAt: at, ExpiresAt: at.Add(time.Hour)}
}

func hexOf(c byte) string {
	b := make([]byte, 64)
	for i := range b {
		b[i] = c
	}
	return string(b)
}

func TestCredentialsAndPasswords(t *testing.T) {
	ctx := context.Background()
	s, _, al, alice, _ := setup(t)
	c, err := s.CredentialByUsername(ctx, "alice")
	if err != nil || c.UserID != alice || c.Hash != "" || c.Disabled {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := s.CredentialByUsername(ctx, "nobody"); !errors.Is(err, resource.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.SetPassword(ctx, "01JD3Y4C8V9M2Q7W5R6T0XZBNZ", hash1, "", time.Now(), nil); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("unknown user: %v", err)
	}
	if _, err := s.SetPassword(ctx, alice, "not-an-argon2id-hash-but-long-enough-text", "", time.Now(), nil); err == nil {
		t.Fatal("the table accepted a value that is not an Argon2id hash")
	}
	now := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	for i, id := range []string{"01JD3Y4C8V9M2Q7W5R6T0XZBN1", "01JD3Y4C8V9M2Q7W5R6T0XZBN2", "01JD3Y4C8V9M2Q7W5R6T0XZBN3"} {
		if err := s.CreateSession(ctx, session(id, alice, hexOf("abc"[i]), now)); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.SetPassword(ctx, alice, hash1, "01JD3Y4C8V9M2Q7W5R6T0XZBN2", now, func(ended int) []audit.Event {
		return []audit.Event{{Action: audit.PasswordChanged, Actor: audit.System, TargetType: "user", TargetID: alice, At: now,
			Details: map[string]string{"by": "host", "sessions_ended": "2"}}}
	})
	if err != nil || n != 2 {
		t.Fatalf("ended %d: %v", n, err)
	}
	list, _ := s.ListSessions(ctx, alice)
	if len(list) != 1 || list[0].ID != "01JD3Y4C8V9M2Q7W5R6T0XZBN2" {
		t.Fatalf("kept %+v", list)
	}
	if c, _ := s.CredentialByUser(ctx, alice); c.Hash != hash1 {
		t.Fatalf("hash %q", c.Hash)
	}
	got, _ := al.List(ctx, audit.Filter{Actions: []audit.Action{audit.PasswordChanged}, Limit: 10})
	if len(got) != 1 {
		t.Fatalf("audit %+v", got)
	}
	// An event the audit log refuses rolls the password change back.
	_, err = s.SetPassword(ctx, alice, hash1+"x", "", now, func(int) []audit.Event {
		return []audit.Event{{Action: "nope.unknown", Actor: audit.System, At: now}}
	})
	if err == nil {
		t.Fatal("invalid audit event accepted")
	}
	if c, _ := s.CredentialByUser(ctx, alice); c.Hash != hash1 {
		t.Fatal("the password changed although its audit entry failed")
	}
	if list, _ := s.ListSessions(ctx, alice); len(list) != 1 {
		t.Fatal("sessions ended although the audit entry failed")
	}
	if err := s.ReplaceHash(ctx, alice, "stale", hash1+"y"); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.CredentialByUser(ctx, alice); c.Hash != hash1 {
		t.Fatal("ReplaceHash replaced a hash that had changed")
	}
}

func TestSessions(t *testing.T) {
	ctx := context.Background()
	s, db, _, alice, bob := setup(t)
	now := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	a := session("01JD3Y4C8V9M2Q7W5R6T0XZBN1", alice, hexOf('a'), now)
	b := session("01JD3Y4C8V9M2Q7W5R6T0XZBN2", bob, hexOf('b'), now.Add(time.Minute))
	for _, x := range []auth.Session{a, b} {
		if err := s.CreateSession(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateSession(ctx, session("01JD3Y4C8V9M2Q7W5R6T0XZBN3", alice, "NOTHEX", now)); err == nil {
		t.Fatal("a token hash that is not hex was stored")
	}
	got, err := s.SessionByTokenHash(ctx, hexOf('a'))
	if err != nil || got.ID != a.ID || !got.ExpiresAt.Equal(a.ExpiresAt) {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := s.SessionByTokenHash(ctx, hexOf('c')); !errors.Is(err, resource.ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.DeleteSession(ctx, alice, b.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("alice deleted bob's session: %v", err)
	}
	if err := s.TouchSession(ctx, a.ID, now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchSession(ctx, "01JD3Y4C8V9M2Q7W5R6T0XZBN9", now); err != nil {
		t.Fatalf("touching a deleted session: %v", err)
	}
	if err := s.MarkAuthenticated(ctx, "01JD3Y4C8V9M2Q7W5R6T0XZBN9", now); !errors.Is(err, resource.ErrNotFound) {
		t.Fatal(err)
	}
	// At now+1h, a has reached its absolute expiry; b (last seen at now+1m)
	// has been idle since before the cut-off of now+2m.
	n, err := s.DeleteExpired(ctx, now.Add(time.Hour), now.Add(2*time.Minute))
	if err != nil || n != 2 {
		t.Fatalf("deleted %d: %v", n, err)
	}
	var left int
	_ = db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT count(*) FROM sessions").Scan(&left)
	})
	if left != 0 {
		t.Fatalf("%d sessions left", left)
	}
}
