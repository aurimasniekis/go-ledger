// Package commitments persists recurring commitment definitions (task C1,
// ADR-0024) in the table of migration 0009_commitments. It implements
// [commitments.Store]: every write runs in one transaction and records its
// audit event through a [store.AuditWriter].
package commitments

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/store"
	occstore "github.com/aurimasniekis/go-ledger/internal/store/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/store/sqlvalue"
)

// Store implements [commitments.Store] on a [store.DB].
type Store struct {
	db    *store.DB
	audit store.AuditWriter
	now   func() time.Time
	newID func() string
	occ   *occstore.Store
}

var _ commitments.Store = (*Store)(nil)

// New returns a Store. aw records audit events and is required; now and newID
// default to time.Now and resource.NewID. Its occurrences (ADR-0027) use the
// default payment evidence; [Store.Occurrences] returns them.
func New(db *store.DB, aw store.AuditWriter, now func() time.Time, newID func() string) *Store {
	return NewWithOccurrences(db, aw, now, newID, occstore.New(db, occstore.Options{Now: now, NewID: newID}))
}

// NewWithOccurrences is [New] with an explicit occurrence store, which edits
// and deletes keep in step with the definitions in the same transaction.
func NewWithOccurrences(db *store.DB, aw store.AuditWriter, now func() time.Time, newID func() string, occ *occstore.Store) *Store {
	if occ == nil {
		panic("commitments.New: an occurrence store is required")
	}
	if aw == nil {
		panic("commitments.New: an audit writer is required")
	}
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = resource.NewID
	}
	return &Store{db: db, audit: aw, now: now, newID: newID, occ: occ}
}

// Occurrences returns the occurrence store kept in step with this store.
func (s *Store) Occurrences() *occstore.Store { return s.occ }

const table = "commitments"

const columns = "id, name, kind, amount, currency, schedule, start_date, end_date, priority, notes, match_tolerance_amount, match_tolerance_currency, leeway_kind, leeway_days, leeway_until, leeway_conditions, leeway_consequences, leeway_source, version, created_at, updated_at"

type querier interface {
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

func scan(row interface{ Scan(...any) error }) (commitments.Commitment, error) {
	var c commitments.Commitment
	var kind, schedule string
	var amount sqlvalue.Amount
	var currency sqlvalue.Currency
	var start sqlvalue.Date
	var end, priority, notes sql.NullString
	var tolAmount sql.Null[sqlvalue.Amount]
	var tolCurrency sql.Null[sqlvalue.Currency]
	var lwKind, lwUntil, lwConditions, lwConsequences, lwSource sql.NullString
	var lwDays sql.NullInt64
	var created, updated store.Timestamp
	if err := row.Scan(&c.ID, &c.Name, &kind, &amount, &currency, &schedule, &start, &end, &priority, &notes, &tolAmount, &tolCurrency, &lwKind, &lwDays, &lwUntil, &lwConditions, &lwConsequences, &lwSource, &c.Version, &created, &updated); err != nil {
		return commitments.Commitment{}, err
	}
	m, err := sqlvalue.Money(amount, currency)
	if err != nil {
		return commitments.Commitment{}, err
	}
	s, errs := commitments.ParseSchedule([]byte(schedule), "schedule")
	if len(errs) > 0 {
		return commitments.Commitment{}, errors.Join(errors.New("stored schedule"), commitments.Validation(errs))
	}
	c.Kind, c.Amount, c.Schedule, c.StartDate = commitments.Kind(kind), m, s, start.Date
	if end.Valid {
		var d sqlvalue.Date
		if err := d.Scan(end.String); err != nil {
			return commitments.Commitment{}, err
		}
		c.EndDate = &d.Date
	}
	if priority.Valid {
		p := commitments.Priority(priority.String)
		c.Priority = &p
	}
	if notes.Valid {
		c.Notes = &notes.String
	}
	if lwKind.Valid {
		l := commitments.Leeway{Kind: commitments.LeewayKind(lwKind.String)}
		if lwDays.Valid {
			n := int(lwDays.Int64)
			l.Days = &n
		}
		if lwUntil.Valid {
			var d sqlvalue.Date
			if err := d.Scan(lwUntil.String); err != nil {
				return commitments.Commitment{}, err
			}
			l.Until = &d.Date
		}
		for _, m := range []struct {
			from sql.NullString
			to   **string
		}{{lwConditions, &l.Conditions}, {lwConsequences, &l.Consequences}, {lwSource, &l.Source}} {
			if m.from.Valid {
				s := m.from.String
				*m.to = &s
			}
		}
		c.Leeway = &l
	}
	if tolAmount.Valid {
		t, err := sqlvalue.Money(tolAmount.V, tolCurrency.V)
		if err != nil {
			return commitments.Commitment{}, err
		}
		c.MatchTolerance = &t
	}
	c.CreatedAt, c.UpdatedAt = created.Time, updated.Time
	return c, nil
}

func get(ctx context.Context, q querier, id string) (commitments.Commitment, error) {
	c, err := scan(q.QueryRowContext(ctx, "SELECT "+columns+" FROM "+table+" WHERE id = ? AND deleted_at IS NULL", id))
	if errors.Is(err, sql.ErrNoRows) {
		return commitments.Commitment{}, resource.ErrNotFound
	}
	return c, err
}

// args returns the column values of f in the order name, kind, amount,
// currency, schedule, start_date, end_date, priority, notes,
// match_tolerance_amount, match_tolerance_currency, leeway_kind, leeway_days,
// leeway_until, leeway_conditions, leeway_consequences, leeway_source.
func args(f commitments.Fields) ([]any, error) {
	schedule, err := json.Marshal(f.Schedule)
	if err != nil {
		return nil, err
	}
	var end, priority, tolAmount, tolCurrency, lwKind, lwDays, lwUntil, lwConditions, lwConsequences, lwSource any
	if l := f.Leeway; l != nil {
		lwKind, lwConditions, lwConsequences, lwSource = string(l.Kind), l.Conditions, l.Consequences, l.Source
		if l.Days != nil {
			lwDays = int64(*l.Days)
		}
		if l.Until != nil {
			lwUntil = sqlvalue.Date{Date: *l.Until}
		}
	}
	if t := f.MatchTolerance; t != nil {
		tolAmount, tolCurrency = sqlvalue.Amount{Decimal: t.Amount()}, sqlvalue.Currency{Currency: t.Currency()}
	}
	if f.EndDate != nil {
		end = sqlvalue.Date{Date: *f.EndDate}
	}
	if f.Priority != nil {
		priority = string(*f.Priority)
	}
	return []any{f.Name, string(f.Kind), sqlvalue.Amount{Decimal: f.Amount.Amount()}, sqlvalue.Currency{Currency: f.Amount.Currency()},
		string(schedule), sqlvalue.Date{Date: f.StartDate}, end, priority, f.Notes, tolAmount, tolCurrency,
		lwKind, lwDays, lwUntil, lwConditions, lwConsequences, lwSource}, nil
}

// Create validates f, inserts it at version 1 and records commitment.created.
func (s *Store) Create(ctx context.Context, actor audit.Actor, f commitments.Fields) (commitments.Commitment, error) {
	if err := commitments.Validation(f.Validate()); err != nil {
		return commitments.Commitment{}, err
	}
	vals, err := args(f)
	if err != nil {
		return commitments.Commitment{}, err
	}
	now := s.now().UTC()
	c := commitments.Commitment{ID: s.newID(), Fields: f, Version: 1, CreatedAt: now, UpdatedAt: now}
	err = s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		all := append([]any{c.ID}, vals...)
		all = append(all, store.Timestamp{Time: now}, store.Timestamp{Time: now})
		if _, err := tx.ExecContext(ctx, "INSERT INTO "+table+" (id, name, kind, amount, currency, schedule, start_date, end_date, priority, notes,"+
			" match_tolerance_amount, match_tolerance_currency, leeway_kind, leeway_days, leeway_until, leeway_conditions, leeway_consequences, leeway_source,"+
			" created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", all...); err != nil {
			return err
		}
		return s.audit.WriteAudit(ctx, tx, commitments.CreatedEvent(actor, c, now))
	})
	if err != nil {
		return commitments.Commitment{}, err
	}
	return c, nil
}

// Get returns one commitment or resource.ErrNotFound.
func (s *Store) Get(ctx context.Context, id string) (commitments.Commitment, error) {
	var c commitments.Commitment
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		c, err = get(ctx, tx, id)
		return err
	})
	return c, err
}

func in(column string, n int) string {
	return column + " IN (?" + strings.Repeat(", ?", n-1) + ")"
}

// List returns up to f.Limit+1 commitments after f.After in ID order.
func (s *Store) List(ctx context.Context, f commitments.ListFilter) ([]commitments.Commitment, error) {
	where := []string{"id > ?", "deleted_at IS NULL"}
	vals := []any{f.After}
	if len(f.Kinds) > 0 {
		where = append(where, in("kind", len(f.Kinds)))
		for _, k := range f.Kinds {
			vals = append(vals, string(k))
		}
	}
	if len(f.Priorities) > 0 {
		where = append(where, in("priority", len(f.Priorities)))
		for _, p := range f.Priorities {
			vals = append(vals, string(p))
		}
	}
	if len(f.Currencies) > 0 {
		where = append(where, in("currency", len(f.Currencies)))
		for _, c := range f.Currencies {
			vals = append(vals, c.Code())
		}
	}
	if f.IDsSet {
		if len(f.IDs) == 0 {
			return nil, nil
		}
		where = append(where, in("id", len(f.IDs)))
		for _, id := range f.IDs {
			vals = append(vals, id)
		}
	}
	vals = append(vals, f.Limit+1)
	var out []commitments.Commitment
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT "+columns+" FROM "+table+" WHERE "+strings.Join(where, " AND ")+" ORDER BY id LIMIT ?", vals...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			c, err := scan(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// Update replaces the fields when the commitment still has version expected
// and records commitment.updated with the names of the changed fields. When
// nothing changes it writes nothing, keeps the version and records nothing.
// When the terms that decide occurrences change (amount, schedule, start or
// end date), they reach the stored occurrences from effective forward in the
// same transaction (ADR-0027), recorded as occurrences.regenerated. today is
// the current date in the reporting zone: an edit effective earlier than a
// pending future-dated one fails with [occurrences.PendingTermsError].
func (s *Store) Update(ctx context.Context, actor audit.Actor, id string, expected int64, f commitments.Fields, effective, today calendar.Date) (commitments.Commitment, error) {
	if err := commitments.Validation(f.Validate()); err != nil {
		return commitments.Commitment{}, err
	}
	vals, err := args(f)
	if err != nil {
		return commitments.Commitment{}, err
	}
	var out commitments.Commitment
	err = s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		before, err := get(ctx, tx, id)
		if err != nil {
			return err
		}
		if before.Version != expected {
			return &resource.VersionConflictError{Expected: expected, Current: before.Version}
		}
		changed := commitments.ChangedFields(before.Fields, f)
		if len(changed) == 0 {
			out = before
			return nil
		}
		now := s.now().UTC()
		set := "name = ?, kind = ?, amount = ?, currency = ?, schedule = ?, start_date = ?, end_date = ?, priority = ?, notes = ?, match_tolerance_amount = ?, match_tolerance_currency = ?, leeway_kind = ?, leeway_days = ?, leeway_until = ?, leeway_conditions = ?, leeway_consequences = ?, leeway_source = ?, updated_at = ?"
		if _, err := store.UpdateVersioned(ctx, tx, table, set, id, expected, append(vals, store.Timestamp{Time: now})...); err != nil {
			return err
		}
		if err := s.audit.WriteAudit(ctx, tx, commitments.UpdatedEvent(actor, id, changed, now)); err != nil {
			return err
		}
		if old, next := occurrences.TermsOf(before.Fields), occurrences.TermsOf(f); !old.Equal(next) {
			plan, err := s.occ.ApplyTx(ctx, tx, id, old, next, effective, today)
			if err != nil {
				return err
			}
			if err := s.audit.WriteAudit(ctx, tx, occurrences.RegeneratedEvent(actor, id, effective, plan, now)); err != nil {
				return err
			}
		}
		out, err = get(ctx, tx, id)
		return err
	})
	return out, err
}

// Delete removes the commitment when it still has version expected and
// records commitment.deleted. Its unpaid occurrences are derived data and go
// with it; one that only deleted payments refer to is cancelled instead (D31)
// and the commitment row is then kept, marked deleted_at (archived), because
// that history refers to it. An occurrence with live payment evidence blocks
// the delete: that is ErrInUse.
func (s *Store) Delete(ctx context.Context, actor audit.Actor, id string, expected int64) error {
	return s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		before, err := get(ctx, tx, id)
		if err != nil {
			return err
		}
		if before.Version != expected {
			return &resource.VersionConflictError{Expected: expected, Current: before.Version}
		}
		if paid, err := s.occ.PaidTx(ctx, tx, id); err != nil {
			return err
		} else if paid {
			return commitments.ErrInUse
		}
		removed, cancelled, kept, err := s.occ.RemoveUnpaidTx(ctx, tx, id)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		if kept {
			if _, err := store.UpdateVersioned(ctx, tx, table, "deleted_at = ?, updated_at = ?", id, expected, store.Timestamp{Time: now}, store.Timestamp{Time: now}); err != nil {
				return err
			}
			// The kept row is invisible to reads, so what the person filed it
			// under goes now (a hard delete does this by cascade): no
			// bucket, category or label may refer to a commitment no read shows.
			for _, q := range []string{"bucket_members", "category_assignments", "label_assignments"} {
				if _, err := tx.ExecContext(ctx, "DELETE FROM "+q+" WHERE object_type = 'commitment' AND object_id = ?", id); err != nil {
					return err
				}
			}
		} else if err := store.DeleteVersioned(ctx, tx, table, id, expected); err != nil {
			if isForeignKey(err) {
				return commitments.ErrInUse
			}
			return err
		}
		return s.audit.WriteAudit(ctx, tx, commitments.DeletedEvent(actor, before, removed, cancelled, kept, now))
	})
}

func isForeignKey(err error) bool {
	return err != nil && strings.Contains(err.Error(), "FOREIGN KEY constraint failed")
}
