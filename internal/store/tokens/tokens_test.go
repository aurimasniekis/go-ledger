package tokens

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/domain/tokens"
	"github.com/aurimasniekis/go-ledger/internal/domain/users"
	"github.com/aurimasniekis/go-ledger/internal/store"
	userstore "github.com/aurimasniekis/go-ledger/internal/store/users"
)

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

var t0 = time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)

func token(id, owner, secretTail string) tokens.Token {
	secret := "ldgr_" + strings.Repeat("A", 43-len(secretTail)) + secretTail
	return tokens.Token{ID: id, OwnerID: owner, Name: "ci " + id[len(id)-2:], Grants: authz.Grants{authz.G(authz.ResourceCommitments, authz.ActionRead), authz.G(authz.ResourceUsers, authz.ActionWrite)},
		TokenHash: tokens.Hash(secret), DisplayPrefix: secret[:9], LastChars: secret[len(secret)-4:], CreatedAt: t0}
}

func TestTokenRoundTripAndList(t *testing.T) {
	s, _, al, alice, bob := setup(t)
	ctx := context.Background()
	exp := t0.Add(48 * time.Hour)
	a := token("01JD3Y4C8V9M2Q7W5R6T0XZC01", alice, "x001")
	a.ExpiresAt = &exp
	b := token("01JD3Y4C8V9M2Q7W5R6T0XZC02", bob, "x002")
	r := token("01JD3Y4C8V9M2Q7W5R6T0XZC03", alice, "x003")
	r.Root, r.Grants = true, nil
	for _, tk := range []tokens.Token{a, b, r} {
		if err := s.Create(ctx, tk, tokens.CreatedEvent(audit.System, tk)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ByHash(ctx, a.TokenHash)
	if err != nil || got.ID != a.ID || got.Name != a.Name || !slices.Equal(got.Grants, a.Grants) || got.ExpiresAt == nil || !got.ExpiresAt.Equal(exp) ||
		got.LastUsedAt != nil || got.RevokedAt != nil || got.DisplayPrefix != a.DisplayPrefix || got.LastChars != "x001" || !got.CreatedAt.Equal(t0) {
		t.Fatalf("by hash: %+v %v", got, err)
	}
	if got, err := s.Get(ctx, r.ID); err != nil || !got.Root || len(got.Grants) != 0 || got.ExpiresAt != nil {
		t.Fatalf("root: %+v %v", got, err)
	}
	if _, err := s.ByHash(ctx, tokens.Hash("nope")); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("unknown hash: %v", err)
	}
	mine, err := s.List(ctx, tokens.ListFilter{OwnerID: alice, Limit: 1})
	if err != nil || len(mine) != 2 || mine[0].ID != a.ID || mine[1].ID != r.ID {
		t.Fatalf("alice's first page (limit+1): %+v %v", mine, err)
	}
	all, err := s.List(ctx, tokens.ListFilter{After: a.ID, Limit: 10})
	if err != nil || len(all) != 2 || all[0].ID != b.ID {
		t.Fatalf("all after a: %+v %v", all, err)
	}
	entries, _ := al.List(ctx, audit.Filter{Actions: []audit.Action{audit.TokenCreated}, Limit: 10})
	if len(entries) != 3 {
		t.Fatalf("created entries: %d", len(entries))
	}
}

func TestRevokeAndTouch(t *testing.T) {
	s, _, al, alice, bob := setup(t)
	ctx := context.Background()
	a := token("01JD3Y4C8V9M2Q7W5R6T0XZC01", alice, "x001")
	if err := s.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	ev := audit.ForToken(audit.TokenRevoked, audit.Actor{Kind: audit.ActorUser, UserID: bob}, a.ID, nil, t0)
	if _, err := s.Revoke(ctx, a.ID, bob, t0, ev); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("another owner revoked: %v", err)
	}
	if _, err := s.Revoke(ctx, "01JD3Y4C8V9M2Q7W5R6T0XZC09", "", t0, ev); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("unknown token: %v", err)
	}
	if err := s.Touch(ctx, a.ID, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	ev.Actor = audit.Actor{Kind: audit.ActorUser, UserID: alice}
	if ok, err := s.Revoke(ctx, a.ID, alice, t0.Add(2*time.Minute), ev); !ok || err != nil {
		t.Fatalf("revoke: %v %v", ok, err)
	}
	if ok, err := s.Revoke(ctx, a.ID, "", t0.Add(3*time.Minute), ev); ok || err != nil {
		t.Fatalf("second revoke: %v %v", ok, err)
	}
	got, _ := s.Get(ctx, a.ID)
	if got.RevokedAt == nil || !got.RevokedAt.Equal(t0.Add(2*time.Minute)) || got.LastUsedAt == nil || !got.LastUsedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("token %+v", got)
	}
	entries, _ := al.List(ctx, audit.Filter{Actions: []audit.Action{audit.TokenRevoked}, Limit: 10})
	if len(entries) != 1 || entries[0].TargetID != a.ID {
		t.Fatalf("revoked entries: %+v", entries)
	}
}

// TestRevokeIsAtomicWithItsAuditEntry: an audit entry that cannot be written
// leaves the token active.
func TestRevokeIsAtomicWithItsAuditEntry(t *testing.T) {
	s, db, _, alice, _ := setup(t)
	ctx := context.Background()
	a := token("01JD3Y4C8V9M2Q7W5R6T0XZC01", alice, "x001")
	if err := s.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	failing := New(db, failWriter{})
	if _, err := failing.Revoke(ctx, a.ID, "", t0, audit.ForToken(audit.TokenRevoked, audit.System, a.ID, nil, t0)); err == nil {
		t.Fatal("revoke succeeded without its audit entry")
	}
	if got, _ := s.Get(ctx, a.ID); got.RevokedAt != nil {
		t.Fatal("the token was revoked without an audit entry")
	}
}

type failWriter struct{}

func (failWriter) WriteAudit(context.Context, *sql.Tx, audit.Event) error {
	return errors.New("audit unavailable")
}

func TestStoredGrantsOutsideTheVocabularyAreDropped(t *testing.T) {
	s, db, _, alice, _ := setup(t)
	ctx := context.Background()
	a := token("01JD3Y4C8V9M2Q7W5R6T0XZC01", alice, "x001")
	if err := s.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE api_tokens SET grants = 'commitments:read,retired:write' WHERE id = ?", a.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, a.ID)
	if err != nil || !slices.Equal(got.Grants.Strings(), []string{"commitments:read"}) {
		t.Fatalf("grants %v %v", got.Grants, err)
	}
}

func TestSchemaRefusesBadRows(t *testing.T) {
	s, _, _, alice, _ := setup(t)
	ctx := context.Background()
	base := token("01JD3Y4C8V9M2Q7W5R6T0XZC01", alice, "x001")
	bad := map[string]tokens.Token{}
	b := base
	b.Root = true // root with grants
	bad["root with grants"] = b
	b = base
	b.Grants = nil // neither
	bad["no grants and not root"] = b
	b = base
	b.TokenHash = "not-a-hash"
	bad["hash"] = b
	b = base
	b.DisplayPrefix = "tok_AAAAA"
	bad["prefix"] = b
	b = base
	b.OwnerID = "01JD3Y4C8V9M2Q7W5R6T0XZC99"
	bad["unknown owner"] = b
	for name, tk := range bad {
		if err := s.Create(ctx, tk); err == nil {
			t.Errorf("%s: stored", name)
		}
	}
}

// TestTouchBehindAHeldWriteTransactionGivesUpAndNeverMovesBackwards: a last-use
// write waits only as long as its context, and an older time never replaces a
// newer one.
func TestTouchBehindAHeldWriteTransactionGivesUpAndNeverMovesBackwards(t *testing.T) {
	s, db, _, alice, _ := setup(t)
	ctx := context.Background()
	a := token("01JD3Y4C8V9M2Q7W5R6T0XZC01", alice, "x001")
	if err := s.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	held, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- db.WriteTx(ctx, func(context.Context, *sql.Tx) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	tctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := s.Touch(tctx, a.ID, t0.Add(time.Minute)); err == nil {
		t.Fatal("touch behind a held writer succeeded")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("touch waited %s", d)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Duration{2 * time.Minute, time.Minute} {
		if err := s.Touch(ctx, a.ID, t0.Add(at)); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.Get(ctx, a.ID)
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("last use %v, want it kept at +2m", got.LastUsedAt)
	}
}
