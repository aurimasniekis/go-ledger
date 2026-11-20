// Package users persists user accounts (task A1, ADR-0021) in the table of
// migration 0005_users. It implements [users.Store]: every write runs in one
// transaction that checks the last-administrator invariant and records its
// audit events through a [store.AuditWriter].
package users

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/domain/users"
	"github.com/aurimasniekis/go-ledger/internal/store"
)

// Store implements [users.Store] on a [store.DB].
type Store struct {
	db    *store.DB
	audit store.AuditWriter
	now   func() time.Time
	newID func() string
}

var _ users.Store = (*Store)(nil)

// New returns a Store. aw records audit events and is required; now and newID
// default to time.Now and resource.NewID.
func New(db *store.DB, aw store.AuditWriter, now func() time.Time, newID func() string) *Store {
	if aw == nil {
		panic("users.New: an audit writer is required")
	}
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = resource.NewID
	}
	return &Store{db: db, audit: aw, now: now, newID: newID}
}

const table = "users"

const columns = "id, username, display_name, role, disabled, disabled_at, version, created_at, updated_at"

type querier interface {
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

func scan(row interface{ Scan(...any) error }) (users.User, error) {
	var u users.User
	var role string
	var disabledAt sql.NullString
	var created, updated store.Timestamp
	if err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &role, &u.Disabled, &disabledAt, &u.Version, &created, &updated); err != nil {
		return users.User{}, err
	}
	u.Role = authz.Role(role)
	u.CreatedAt, u.UpdatedAt = created.Time, updated.Time
	if disabledAt.Valid {
		t, err := store.ParseTime(disabledAt.String)
		if err != nil {
			return users.User{}, err
		}
		u.DisabledAt = &t
	}
	return u, nil
}

func get(ctx context.Context, q querier, id string) (users.User, error) {
	u, err := scan(q.QueryRowContext(ctx, "SELECT "+columns+" FROM "+table+" WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return users.User{}, resource.ErrNotFound
	}
	return u, err
}

func usernameTaken(ctx context.Context, tx *sql.Tx, username, exceptID string) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, "SELECT count(*) FROM "+table+" WHERE username = ? AND id <> ?", username, exceptID).Scan(&n)
	return n > 0, err
}

// Create inserts a user at version 1 and records user.created.
func (s *Store) Create(ctx context.Context, actor audit.Actor, n users.New) (users.User, error) {
	if err := n.Validate(); err != nil {
		return users.User{}, err
	}
	now := s.now().UTC()
	u := users.User{ID: s.newID(), Username: n.Username, DisplayName: n.DisplayName, Role: n.Role, Version: 1, CreatedAt: now, UpdatedAt: now}
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		taken, err := usernameTaken(ctx, tx, u.Username, u.ID)
		if err != nil {
			return err
		}
		if taken {
			return users.ErrUsernameTaken
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO "+table+" (id, username, display_name, role, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
			u.ID, u.Username, u.DisplayName, string(u.Role), store.Timestamp{Time: now}, store.Timestamp{Time: now}); err != nil {
			return err
		}
		return s.audit.WriteAudit(ctx, tx, users.CreatedEvent(actor, u, now))
	})
	if err != nil {
		return users.User{}, err
	}
	return u, nil
}

// Get returns one user or resource.ErrNotFound.
func (s *Store) Get(ctx context.Context, id string) (users.User, error) {
	var u users.User
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		u, err = get(ctx, tx, id)
		return err
	})
	return u, err
}

// List returns up to f.Limit+1 users after f.After in ID order.
func (s *Store) List(ctx context.Context, f users.ListFilter) ([]users.User, error) {
	where := []string{"id > ?"}
	args := []any{f.After}
	if len(f.Roles) > 0 {
		where = append(where, "role IN (?"+strings.Repeat(", ?", len(f.Roles)-1)+")")
		for _, r := range f.Roles {
			args = append(args, string(r))
		}
	}
	if f.Disabled != nil {
		where = append(where, "disabled = ?")
		args = append(args, *f.Disabled)
	}
	if f.UsernamePrefix != "" {
		where = append(where, "substr(username, 1, length(?)) = ?")
		args = append(args, f.UsernamePrefix, f.UsernamePrefix)
	}
	args = append(args, f.Limit+1)
	var out []users.User
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT "+columns+" FROM "+table+" WHERE "+strings.Join(where, " AND ")+" ORDER BY id LIMIT ?", args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			u, err := scan(rows)
			if err != nil {
				return err
			}
			out = append(out, u)
		}
		return rows.Err()
	})
	return out, err
}

// Update applies p when the user still has version expected, keeps at least
// one active administrator and records one audit event per kind of change.
func (s *Store) Update(ctx context.Context, actor audit.Actor, id string, expected int64, p users.Patch) (users.User, error) {
	if err := p.Validate(); err != nil {
		return users.User{}, err
	}
	var out users.User
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		before, err := get(ctx, tx, id)
		if err != nil {
			return err
		}
		if before.Version != expected {
			return &resource.VersionConflictError{Expected: expected, Current: before.Version}
		}
		after := p.Apply(before)
		if before.ActiveAdministrator() && !after.ActiveAdministrator() {
			var others int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM "+table+" WHERE role = ? AND disabled = 0 AND id <> ?",
				string(authz.RoleAdministrator), id).Scan(&others); err != nil {
				return err
			}
			if err := users.CheckLastAdministrator(before, after, others); err != nil {
				return err
			}
		}
		if after.Username != before.Username {
			taken, err := usernameTaken(ctx, tx, after.Username, id)
			if err != nil {
				return err
			}
			if taken {
				return users.ErrUsernameTaken
			}
		}
		now := s.now().UTC()
		var disabledAt any
		switch {
		case after.Disabled && !before.Disabled:
			disabledAt = store.Timestamp{Time: now}
		case after.Disabled:
			disabledAt = before.DisabledAt.UTC().Format(store.TimeLayout)
		}
		set := "username = ?, display_name = ?, role = ?, disabled = ?, disabled_at = ?, updated_at = ?"
		if _, err := store.UpdateVersioned(ctx, tx, table, set, id, expected,
			after.Username, after.DisplayName, string(after.Role), after.Disabled, disabledAt, store.Timestamp{Time: now}); err != nil {
			return err
		}
		for _, e := range users.ChangeEvents(actor, before, after, now) {
			if err := s.audit.WriteAudit(ctx, tx, e); err != nil {
				return err
			}
		}
		out, err = get(ctx, tx, id)
		return err
	})
	return out, err
}

// CheckVersion returns the user when it still has version expected.
func (s *Store) CheckVersion(ctx context.Context, id string, expected int64) (users.User, error) {
	u, err := s.Get(ctx, id)
	if err != nil {
		return users.User{}, err
	}
	if u.Version != expected {
		return users.User{}, &resource.VersionConflictError{Expected: expected, Current: u.Version}
	}
	return u, nil
}

// Principal returns the principal of an enabled user with its current role.
func (s *Store) Principal(ctx context.Context, id string) (authz.Principal, error) {
	u, err := s.Get(ctx, id)
	if err != nil {
		return authz.Principal{}, err
	}
	if u.Disabled {
		return authz.Principal{}, users.ErrDisabled
	}
	return authz.UserPrincipal(u.ID, u.Role), nil
}
