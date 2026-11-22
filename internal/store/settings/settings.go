// Package settings persists the stored settings layer (ADR-0066) in the
// setting_groups table of migration 0033_setting_groups. It implements
// [settings.Store]: one row per settings group, inserted on the group's first
// write with version 2 (the API synthesises version 1 until then), changed only
// under optimistic concurrency, and audited in the same transaction.
package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/domain/settings"
	"github.com/aurimasniekis/go-ledger/internal/store"
)

var _ settings.Store = (*Store)(nil)

// TargetType is the audit target type of a settings write; the target ID is
// the group name.
const TargetType = "setting_group"

// Store keeps setting groups on a [store.DB].
type Store struct {
	db    *store.DB
	audit store.AuditWriter
	now   func() time.Time
	newID func() string
}

// New returns a Store. aw records every write and is required; now and newID
// default to time.Now and resource.NewID.
func New(db *store.DB, aw store.AuditWriter, now func() time.Time, newID func() string) *Store {
	if aw == nil {
		panic("settings.New: an audit writer is required")
	}
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = resource.NewID
	}
	return &Store{db: db, audit: aw, now: now, newID: newID}
}

const columns = "id, name, stored, version, created_at, updated_at"

type row struct {
	id string
	settings.Stored
}

func scan(r interface{ Scan(...any) error }) (row, error) {
	var (
		out        row
		stored     string
		created, u store.Timestamp
	)
	if err := r.Scan(&out.id, &out.Name, &stored, &out.Version, &created, &u); err != nil {
		return row{}, err
	}
	if err := json.Unmarshal([]byte(stored), &out.Values); err != nil {
		return row{}, fmt.Errorf("settings: stored document of group %s is not a JSON object", out.Name)
	}
	if out.Values == nil {
		out.Values = map[string]json.RawMessage{}
	}
	out.Exists, out.CreatedAt, out.UpdatedAt = true, created.Time, u.Time
	return out, nil
}

// Groups returns every stored row, ordered by name.
func (s *Store) Groups(ctx context.Context) ([]settings.Stored, error) {
	var out []settings.Stored
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT "+columns+" FROM setting_groups ORDER BY name")
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			r, err := scan(rows)
			if err != nil {
				return err
			}
			out = append(out, r.Stored)
		}
		return rows.Err()
	})
	return out, err
}

func get(ctx context.Context, tx *sql.Tx, name string) (row, error) {
	r, err := scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM setting_groups WHERE name = ?", name))
	if errors.Is(err, sql.ErrNoRows) {
		return row{Stored: settings.Stored{Name: name, Version: 1, Values: map[string]json.RawMessage{}}}, nil
	}
	return r, err
}

// Write implements [settings.Store].
func (s *Store) Write(ctx context.Context, group string, expected int64, decide func(cur settings.Stored) (settings.Change, error)) (settings.Stored, error) {
	if !settings.NamePattern.MatchString(group) {
		return settings.Stored{}, fmt.Errorf("settings: invalid group name")
	}
	var out settings.Stored
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		cur, err := get(ctx, tx, group)
		if err != nil {
			return err
		}
		if cur.Version != expected {
			return &resource.VersionConflictError{Expected: expected, Current: cur.Version}
		}
		ch, err := decide(cur.Stored)
		if err != nil {
			return err
		}
		if ch.Event == nil {
			out = cur.Stored
			return nil
		}
		values := ch.Values
		if values == nil {
			values = map[string]json.RawMessage{}
		}
		doc, err := json.Marshal(values) // sorted keys: the stored text is stable
		if err != nil {
			return err
		}
		now := s.now().UTC()
		next := cur
		next.Values, next.UpdatedAt, next.Exists = values, now, true
		if !cur.Exists {
			// Insert on first write (ADR-0066 point 9): the synthesised
			// representation was version 1, so the row starts at 2. A
			// concurrent first write loses on the unique name.
			next.id, next.CreatedAt, next.Version = s.newID(), now, 2
			_, err = tx.ExecContext(ctx, "INSERT INTO setting_groups ("+columns+") VALUES (?, ?, ?, ?, ?, ?)",
				next.id, group, string(doc), next.Version, store.Timestamp{Time: now}, store.Timestamp{Time: now})
			if err != nil {
				if strings.Contains(err.Error(), "UNIQUE constraint failed") {
					return &resource.VersionConflictError{Expected: expected, Current: 2}
				}
				return err
			}
		} else {
			next.Version, err = store.UpdateVersioned(ctx, tx, "setting_groups", "stored = ?, updated_at = ?", cur.id, cur.Version,
				string(doc), store.Timestamp{Time: now})
			if err != nil {
				return err
			}
		}
		ev := *ch.Event
		ev.TargetType, ev.TargetID, ev.At = TargetType, group, now
		if err := s.audit.WriteAudit(ctx, tx, ev); err != nil {
			return err
		}
		out = next.Stored
		return nil
	})
	if err != nil {
		return settings.Stored{}, err
	}
	return out, nil
}
