// Package alerts persists in-app alerts (task L6, ADR-0065) in the alerts
// table of migration 0032_decision_tasks. An alert is raised idempotently by
// its key, only ever closed afterwards, and audited.
package alerts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/alerts"
	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/store"
)

var _ alerts.Store = (*Store)(nil)

// Store keeps alerts on a [store.DB].
type Store struct {
	db    *store.DB
	audit store.AuditWriter
	now   func() time.Time
	newID func() string
}

// New returns a Store; aw is required, now and newID default to time.Now and
// resource.NewID.
func New(db *store.DB, aw store.AuditWriter, now func() time.Time, newID func() string) *Store {
	if aw == nil {
		panic("alerts.New: an audit writer is required")
	}
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = resource.NewID
	}
	return &Store{db: db, audit: aw, now: now, newID: newID}
}

const columns = "id, kind, object_type, object_id, dedupe_key, facts, message, model_text, model_provider, model_name, state, created_at, closed_at, version"

func scan(row interface{ Scan(...any) error }) (alerts.Alert, error) {
	var a alerts.Alert
	var kind, otype, facts, state string
	var text, provider, model sql.Null[string]
	var created store.Timestamp
	var closed sql.Null[store.Timestamp]
	if err := row.Scan(&a.ID, &kind, &otype, &a.ObjectID, &a.Key, &facts, &a.Message, &text, &provider, &model, &state, &created, &closed, &a.Version); err != nil {
		return alerts.Alert{}, err
	}
	if err := json.Unmarshal([]byte(facts), &a.Facts); err != nil {
		return alerts.Alert{}, err
	}
	a.Kind, a.ObjectType, a.State, a.CreatedAt = alerts.Kind(kind), alerts.ObjectType(otype), alerts.State(state), created.Time
	if text.Valid {
		a.Wording = &alerts.Wording{Text: text.V, Provider: provider.V, Model: model.V}
	}
	if closed.Valid {
		t := closed.V.Time
		a.ClosedAt = &t
	}
	return a, nil
}

func who(a audit.Actor) string {
	return string(a.Channel())
}

// Raise implements [alerts.Store]. An open alert, or one a person dismissed,
// for the same key already exists (created is false and nothing is written):
// a dismissal sticks. Only a resolved alert lets the key be raised again.
func (s *Store) Raise(ctx context.Context, actor audit.Actor, c alerts.Condition) (alerts.Alert, bool, error) {
	facts, err := json.Marshal(c.Facts)
	if err != nil {
		return alerts.Alert{}, false, err
	}
	if len(c.Facts) == 0 {
		facts = []byte("{}")
	}
	now := s.now().UTC()
	var out alerts.Alert
	var created bool
	err = s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		existing, err := scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM alerts WHERE dedupe_key = ? AND state IN ('open', 'dismissed') ORDER BY state = 'open' DESC LIMIT 1", c.Key))
		if err == nil {
			out, created = existing, false
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		id := s.newID()
		if _, err := tx.ExecContext(ctx, "INSERT INTO alerts (id, kind, object_type, object_id, dedupe_key, facts, message, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
			id, string(c.Kind), string(c.ObjectType), c.ObjectID, c.Key, string(facts), c.Message, store.Timestamp{Time: now}); err != nil {
			return err
		}
		out = alerts.Alert{ID: id, Condition: c, State: alerts.StateOpen, CreatedAt: now, Version: 1}
		created = true
		return s.audit.WriteAudit(ctx, tx, audit.Event{Action: audit.AlertRaised, Actor: actor, TargetType: "alert", TargetID: id, At: now,
			Details: map[string]string{"kind": string(c.Kind), "via": who(actor)}})
	})
	return out, created, err
}

// SetWording implements [alerts.Store].
func (s *Store) SetWording(ctx context.Context, id string, w alerts.Wording) error {
	return s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE alerts SET model_text = ?, model_provider = ?, model_name = ?, version = version + 1 WHERE id = ? AND state = 'open' AND model_text IS NULL",
			w.Text, w.Provider, w.Model, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return alerts.ErrNotOpen
		}
		return nil
	})
}

// Close implements [alerts.Store].
func (s *Store) Close(ctx context.Context, actor audit.Actor, id string, to alerts.State) (alerts.Alert, error) {
	if to != alerts.StateDismissed && to != alerts.StateResolved {
		return alerts.Alert{}, errors.New("alerts: an alert is dismissed or resolved")
	}
	now := s.now().UTC()
	var out alerts.Alert
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE alerts SET state = ?, closed_at = ?, version = version + 1 WHERE id = ? AND state = 'open'", string(to), store.Timestamp{Time: now}, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			var one int
			if err := tx.QueryRowContext(ctx, "SELECT 1 FROM alerts WHERE id = ?", id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
				return resource.ErrNotFound
			} else if err != nil {
				return err
			}
			return alerts.ErrNotOpen
		}
		if out, err = scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM alerts WHERE id = ?", id)); err != nil {
			return err
		}
		return s.audit.WriteAudit(ctx, tx, audit.Event{Action: audit.AlertClosed, Actor: actor, TargetType: "alert", TargetID: id, At: now,
			Details: map[string]string{"kind": string(out.Kind), "how": string(to), "via": who(actor)}})
	})
	return out, err
}

// Get implements [alerts.Store].
func (s *Store) Get(ctx context.Context, id string) (alerts.Alert, error) {
	var a alerts.Alert
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		a, err = scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM alerts WHERE id = ?", id))
		if errors.Is(err, sql.ErrNoRows) {
			return resource.ErrNotFound
		}
		return err
	})
	return a, err
}

// Find returns the alerts matching f, oldest first (by identifier).
func (s *Store) Find(ctx context.Context, f alerts.Filter) ([]alerts.Alert, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var where []string
	var args []any
	in := func(col string, vals []string) {
		if len(vals) == 0 {
			return
		}
		where = append(where, col+" IN ("+strings.TrimSuffix(strings.Repeat("?,", len(vals)), ",")+")")
		for _, v := range vals {
			args = append(args, v)
		}
	}
	kinds := make([]string, len(f.Kinds))
	for i, k := range f.Kinds {
		kinds[i] = string(k)
	}
	states := make([]string, len(f.States))
	for i, st := range f.States {
		states[i] = string(st)
	}
	in("kind", kinds)
	in("state", states)
	if f.ObjectType != "" {
		where = append(where, "object_type = ?")
		args = append(args, string(f.ObjectType))
	}
	if f.ObjectID != "" {
		where = append(where, "object_id = ?")
		args = append(args, f.ObjectID)
	}
	if f.After != "" {
		where = append(where, "id > ?")
		args = append(args, f.After)
	}
	q := "SELECT " + columns + " FROM alerts"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY id LIMIT ?"
	args = append(args, limit)
	var out []alerts.Alert
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, q, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			a, err := scan(rows)
			if err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

// List implements [alerts.Store].
func (s *Store) List(ctx context.Context, state alerts.State, limit int) ([]alerts.Alert, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q, args := "SELECT "+columns+" FROM alerts", []any{}
	if state != "" {
		q += " WHERE state = ?"
		args = append(args, string(state))
	}
	q += " ORDER BY created_at DESC, id DESC LIMIT ?"
	args = append(args, limit)
	var out []alerts.Alert
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, q, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			a, err := scan(rows)
			if err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}
