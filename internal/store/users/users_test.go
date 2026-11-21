package users

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/domain/users"
	"github.com/aurimasniekis/go-ledger/internal/store"
)

type recorder struct {
	events []audit.Event
	fail   error
}

func (r *recorder) WriteAudit(_ context.Context, tx *sql.Tx, e audit.Event) error {
	if tx == nil {
		return errors.New("no transaction")
	}
	if r.fail != nil {
		return r.fail
	}
	r.events = append(r.events, e)
	return nil
}

func (r *recorder) actions() string {
	var out []string
	for _, e := range r.events {
		out = append(out, string(e.Action))
	}
	return strings.Join(out, ",")
}

var t0 = time.Date(2026, 3, 4, 5, 6, 7, 8, time.UTC)

func newStore(t *testing.T) (*Store, *recorder) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "ledger.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	rec := &recorder{}
	return New(db, rec, func() time.Time { return t0 }, nil), rec
}

func ptr[T any](v T) *T { return &v }

var admin = audit.Actor{Kind: audit.ActorUser, UserID: "01JD3Y4C8V9M2Q7W5R6T0XZBNA"}

func TestCreateGetList(t *testing.T) {
	ctx := context.Background()
	st, rec := newStore(t)
	a, err := st.Create(ctx, audit.System, users.New{Username: "alice", Role: authz.RoleAdministrator, DisplayName: ptr("Alice")})
	if err != nil {
		t.Fatal(err)
	}
	if !resource.ValidID(a.ID) || a.Version != 1 || !a.CreatedAt.Equal(t0) || a.Disabled {
		t.Fatalf("created %+v", a)
	}
	got, err := st.Get(ctx, a.ID)
	if err != nil || got.Username != "alice" || *got.DisplayName != "Alice" || got.Role != authz.RoleAdministrator || !got.UpdatedAt.Equal(t0) {
		t.Fatalf("get %+v %v", got, err)
	}
	if rec.actions() != "user.created" || rec.events[0].Actor != audit.System || rec.events[0].Details["role"] != "administrator" {
		t.Fatalf("audit %+v", rec.events)
	}
	if _, err := st.Create(ctx, admin, users.New{Username: "alice", Role: authz.RoleReadOnly}); !errors.Is(err, users.ErrUsernameTaken) {
		t.Fatalf("duplicate: %v", err)
	}
	var fe *users.FieldError
	if _, err := st.Create(ctx, admin, users.New{Username: "Bob", Role: authz.RoleReadOnly}); !errors.As(err, &fe) {
		t.Fatalf("invalid username: %v", err)
	}
	b, err := st.Create(ctx, admin, users.New{Username: "bob", Role: authz.RoleReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get(ctx, "01JD3Y4C8V9M2Q7W5R6T0XZBNB"); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}

	all, err := st.List(ctx, users.ListFilter{Limit: 10})
	if err != nil || len(all) != 2 {
		t.Fatalf("list %v %v", all, err)
	}
	ro, _ := st.List(ctx, users.ListFilter{Limit: 10, Roles: []authz.Role{authz.RoleReadOnly}})
	if len(ro) != 1 || ro[0].ID != b.ID {
		t.Fatalf("role filter %v", ro)
	}
	pre, _ := st.List(ctx, users.ListFilter{Limit: 10, UsernamePrefix: "al"})
	if len(pre) != 1 || pre[0].ID != a.ID {
		t.Fatalf("prefix filter %v", pre)
	}
	en, _ := st.List(ctx, users.ListFilter{Limit: 10, Disabled: ptr(true)})
	if len(en) != 0 {
		t.Fatalf("disabled filter %v", en)
	}
	page, _ := st.List(ctx, users.ListFilter{Limit: 0})
	if len(page) != 1 {
		t.Fatalf("limit+1: %v", page)
	}
}

func TestUpdateAuditsAndProtectsLastAdmin(t *testing.T) {
	ctx := context.Background()
	st, rec := newStore(t)
	a, _ := st.Create(ctx, audit.System, users.New{Username: "alice", Role: authz.RoleAdministrator})
	b, _ := st.Create(ctx, audit.System, users.New{Username: "bob", Role: authz.RoleReadOnly})
	rec.events = nil

	// The only administrator cannot be demoted or disabled.
	if _, err := st.Update(ctx, admin, a.ID, 1, users.Patch{Role: ptr(authz.RoleReadOnly)}); !errors.Is(err, users.ErrLastAdministrator) {
		t.Fatalf("demote last admin: %v", err)
	}
	if _, err := st.Update(ctx, admin, a.ID, 1, users.Patch{Disabled: ptr(true)}); !errors.Is(err, users.ErrLastAdministrator) {
		t.Fatalf("disable last admin: %v", err)
	}
	if len(rec.events) != 0 {
		t.Fatalf("refused change was audited: %v", rec.actions())
	}

	// Promote bob: role change audited with from/to.
	b2, err := st.Update(ctx, admin, b.ID, 1, users.Patch{Role: ptr(authz.RoleAdministrator)})
	if err != nil || b2.Role != authz.RoleAdministrator || b2.Version != 2 {
		t.Fatalf("promote: %+v %v", b2, err)
	}
	if e := rec.events[0]; e.Action != audit.UserRoleChanged || e.Details["from"] != "read-only" || e.Details["to"] != "administrator" || e.TargetID != b.ID || e.Actor != admin {
		t.Fatalf("role change audit %+v", e)
	}
	// Now alice can be disabled; disabled_at is set and kept.
	a2, err := st.Update(ctx, admin, a.ID, 1, users.Patch{Disabled: ptr(true)})
	if err != nil || !a2.Disabled || a2.DisabledAt == nil || !a2.DisabledAt.Equal(t0) {
		t.Fatalf("disable: %+v %v", a2, err)
	}
	a3, err := st.Update(ctx, admin, a.ID, 2, users.Patch{SetDisplayName: true, DisplayName: ptr("Al")})
	if err != nil || a3.DisabledAt == nil || !a3.DisabledAt.Equal(t0) {
		t.Fatalf("rename while disabled: %+v %v", a3, err)
	}
	// bob is the last active admin now.
	if _, err := st.Update(ctx, admin, b.ID, 2, users.Patch{Role: ptr(authz.RoleReadOnly)}); !errors.Is(err, users.ErrLastAdministrator) {
		t.Fatalf("demote last active admin: %v", err)
	}
	a4, err := st.Update(ctx, admin, a.ID, 3, users.Patch{Disabled: ptr(false)})
	if err != nil || a4.Disabled || a4.DisabledAt != nil {
		t.Fatalf("enable: %+v %v", a4, err)
	}
	if got := rec.actions(); got != "user.role_changed,user.disabled,user.updated,user.enabled" {
		t.Fatalf("audit trail %s", got)
	}
	// Stale version, missing user, taken username.
	var vc *resource.VersionConflictError
	if _, err := st.Update(ctx, admin, a.ID, 1, users.Patch{Disabled: ptr(true)}); !errors.As(err, &vc) || vc.Current != 4 {
		t.Fatalf("stale: %v", err)
	}
	if _, err := st.Update(ctx, admin, "01JD3Y4C8V9M2Q7W5R6T0XZBNB", 1, users.Patch{}); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := st.Update(ctx, admin, a.ID, 4, users.Patch{Username: ptr("bob")}); !errors.Is(err, users.ErrUsernameTaken) {
		t.Fatalf("taken: %v", err)
	}
	if _, err := st.CheckVersion(ctx, a.ID, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CheckVersion(ctx, a.ID, 3); !errors.As(err, &vc) {
		t.Fatalf("check version: %v", err)
	}
}

func TestNoOpPatchWritesNothing(t *testing.T) {
	ctx := context.Background()
	st, rec := newStore(t)
	a, _ := st.Create(ctx, audit.System, users.New{Username: "alice", Role: authz.RoleAdministrator})
	rec.events = nil
	for _, p := range []users.Patch{{}, {Role: ptr(authz.RoleAdministrator)}, {Username: ptr("alice")}, {Disabled: ptr(false)}, {SetDisplayName: true}} {
		got, err := st.Update(ctx, admin, a.ID, 1, p)
		if err != nil || got.Version != 1 || !got.UpdatedAt.Equal(a.UpdatedAt) {
			t.Fatalf("%+v: %+v %v", p, got, err)
		}
	}
	if len(rec.events) != 0 {
		t.Fatalf("audited: %v", rec.actions())
	}
	// A real change still bumps the version once, and then the same patch is a no-op.
	b, err := st.Update(ctx, admin, a.ID, 1, users.Patch{SetDisplayName: true, DisplayName: ptr("Al")})
	if err != nil || b.Version != 2 {
		t.Fatalf("change: %+v %v", b, err)
	}
	c, err := st.Update(ctx, admin, a.ID, 2, users.Patch{SetDisplayName: true, DisplayName: ptr("Al")})
	if err != nil || c.Version != 2 {
		t.Fatalf("repeat: %+v %v", c, err)
	}
}

func TestAuditFailureRollsBackTheChange(t *testing.T) {
	ctx := context.Background()
	st, rec := newStore(t)
	a, _ := st.Create(ctx, audit.System, users.New{Username: "alice", Role: authz.RoleAdministrator})
	b, _ := st.Create(ctx, audit.System, users.New{Username: "bob", Role: authz.RoleReadOnly})
	rec.fail = errors.New("audit down")
	if _, err := st.Update(ctx, admin, b.ID, 1, users.Patch{Role: ptr(authz.RoleAdministrator)}); err == nil {
		t.Fatal("update succeeded without audit")
	}
	if _, err := st.Create(ctx, admin, users.New{Username: "carol", Role: authz.RoleReadOnly}); err == nil {
		t.Fatal("create succeeded without audit")
	}
	got, _ := st.Get(ctx, b.ID)
	if got.Role != authz.RoleReadOnly || got.Version != 1 {
		t.Fatalf("change kept without audit: %+v", got)
	}
	all, _ := st.List(ctx, users.ListFilter{Limit: 10})
	if len(all) != 2 {
		t.Fatalf("user created without audit: %d", len(all))
	}
	_ = a
}

func TestPrincipalReflectsCurrentState(t *testing.T) {
	ctx := context.Background()
	st, _ := newStore(t)
	a, _ := st.Create(ctx, audit.System, users.New{Username: "alice", Role: authz.RoleAdministrator})
	b, _ := st.Create(ctx, audit.System, users.New{Username: "bob", Role: authz.RoleAdministrator})
	p, err := st.Principal(ctx, b.ID)
	if err != nil || !p.Allows(authz.G(authz.ResourceUsers, authz.ActionWrite)) || p.Kind != authz.PrincipalUser {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := st.Update(ctx, admin, b.ID, 1, users.Patch{Role: ptr(authz.RoleReadOnly)}); err != nil {
		t.Fatal(err)
	}
	p, _ = st.Principal(ctx, b.ID)
	if p.Allows(authz.G(authz.ResourceUsers, authz.ActionWrite)) || p.Role != authz.RoleReadOnly {
		t.Fatalf("demotion not effective: %+v", p)
	}
	if _, err := st.Update(ctx, admin, b.ID, 2, users.Patch{Disabled: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Principal(ctx, b.ID); !errors.Is(err, users.ErrDisabled) {
		t.Fatalf("disabled user: %v", err)
	}
	if _, err := st.Principal(ctx, "01JD3Y4C8V9M2Q7W5R6T0XZBNB"); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	_ = a
}

func TestSchemaRefusesWhatCodeRefuses(t *testing.T) {
	ctx := context.Background()
	st, _ := newStore(t)
	for _, q := range []string{
		"INSERT INTO users (id, username, role, created_at, updated_at) VALUES ('01JD3Y4C8V9M2Q7W5R6T0XZBNA', 'Alice', 'administrator', '2026-01-01T00:00:00.000000000Z', '2026-01-01T00:00:00.000000000Z')",
		"INSERT INTO users (id, username, role, created_at, updated_at) VALUES ('01JD3Y4C8V9M2Q7W5R6T0XZBNA', 'alice', 'owner', '2026-01-01T00:00:00.000000000Z', '2026-01-01T00:00:00.000000000Z')",
		"INSERT INTO users (id, username, role, disabled, created_at, updated_at) VALUES ('01JD3Y4C8V9M2Q7W5R6T0XZBNA', 'alice', 'administrator', 1, '2026-01-01T00:00:00.000000000Z', '2026-01-01T00:00:00.000000000Z')",
		"INSERT INTO users (id, username, role, created_at, updated_at) VALUES ('01JD3Y4C8V9M2Q7W5R6T0XZBNA', '.alice', 'administrator', '2026-01-01T00:00:00.000000000Z', '2026-01-01T00:00:00.000000000Z')",
	} {
		err := st.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, q)
			return err
		})
		if err == nil {
			t.Errorf("accepted: %s", q)
		}
	}
}
