// Package alerts persists in-app alerts (task L6, ADR-0063) in the alerts
// table of migration 0032_decision_tasks. An alert is raised idempotently by
// its key, only ever closed afterwards, and audited.
package alerts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

// Raise implements [alerts.Store].
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
		existing, err := scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM alerts WHERE dedupe_key = ? AND state = 'open'", c.Key))
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
