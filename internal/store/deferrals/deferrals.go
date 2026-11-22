// Package deferrals persists deferral plans (task DF1, ADR-0050) in the tables
// of migration 0025_deferral_plans and implements [deferrals.Store].
//
// A plan is stored apart from its occurrence and from payment evidence: no
// write here changes an occurrence, a manual payment or a payment match, and
// the plan tables are not evidence sources (internal/store/payments), so a
// plan never makes an occurrence paid. Bank facts are never read or written,
// so a re-import leaves plans as they are.
package deferrals

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/deferrals"
	"github.com/aurimasniekis/go-ledger/internal/domain/payments"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/store"
	occstore "github.com/aurimasniekis/go-ledger/internal/store/occurrences"
	paystore "github.com/aurimasniekis/go-ledger/internal/store/payments"
	"github.com/aurimasniekis/go-ledger/internal/store/sqlvalue"
)

// Store implements [deferrals.Store] on a [store.DB].
type Store struct {
	db    *store.DB
	audit store.AuditWriter
	now   func() time.Time
	newID func() string
	// zone is the reporting zone payment facts are derived in (D38 1a); UTC
	// when unset.
	zone calendar.Zone
}

var _ deferrals.Store = (*Store)(nil)

// New returns a Store. aw records audit events and is required; now and newID
// default to time.Now and resource.NewID.
func New(db *store.DB, aw store.AuditWriter, now func() time.Time, newID func() string) *Store {
	if aw == nil {
		panic("deferrals.New: an audit writer is required")
	}
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = resource.NewID
	}
	return &Store{db: db, audit: aw, now: now, newID: newID}
}

// WithZone returns a copy of the store that derives payment state in the
// reporting zone z (the zero zone means UTC).
func (s *Store) WithZone(z calendar.Zone) *Store {
	if s == nil {
		return nil
	}
	c := *s
	c.zone = z
	return &c
}

// planColumns are a plan's columns with its occurrence's commitment; queries
// select FROM deferral_plans AS p JOIN commitment_occurrences AS o.
const planColumns = "p.id, p.occurrence_id, o.commitment_id, p.plan_type, p.planned_date, p.reason, p.original_due_date, p.original_amount, p.original_currency," +
	" p.outstanding_amount, p.outstanding_currency, p.version, p.created_at, p.updated_at, p.undone_at"

const planFrom = " FROM deferral_plans AS p JOIN commitment_occurrences AS o ON o.id = p.occurrence_id"

func scanPlan(row interface{ Scan(...any) error }) (deferrals.Plan, error) {
	var p deferrals.Plan
	var typ string
	var planned, due sqlvalue.Date
	var reason sql.Null[string]
	var amt, out sqlvalue.Amount
	var cur, outCur sqlvalue.Currency
	var created, updated store.Timestamp
	var undone sql.Null[store.Timestamp]
	if err := row.Scan(&p.ID, &p.OccurrenceID, &p.CommitmentID, &typ, &planned, &reason, &due, &amt, &cur, &out, &outCur, &p.Version, &created, &updated, &undone); err != nil {
		return deferrals.Plan{}, err
	}
	orig, err := sqlvalue.Money(amt, cur)
	if err != nil {
		return deferrals.Plan{}, err
	}
	owed, err := sqlvalue.Money(out, outCur)
	if err != nil {
		return deferrals.Plan{}, err
	}
	p.Type, p.PlannedDate, p.OriginalDueDate, p.OriginalAmount, p.OutstandingWhenPlanned = deferrals.PlanType(typ), planned.Date, due.Date, orig, owed
	p.CreatedAt, p.UpdatedAt = created.Time, updated.Time
	if reason.Valid {
		r := reason.V
		p.Reason = &r
	}
	if undone.Valid {
		t := undone.V.Time
		p.UndoneAt = &t
	}
	return p, nil
}

const maxIn = 500

func placeholders(n int) string { return "?" + strings.Repeat(", ?", n-1) }

// plans runs a plan query and attaches each plan's adjustments.
func plans(ctx context.Context, tx *sql.Tx, where string, args ...any) ([]deferrals.Plan, error) {
	rows, err := tx.QueryContext(ctx, "SELECT "+planColumns+planFrom+where, args...)
	if err != nil {
		return nil, err
	}
	var out []deferrals.Plan
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, attachAdjustments(ctx, tx, out)
}

func attachAdjustments(ctx context.Context, tx *sql.Tx, ps []deferrals.Plan) error {
	idx := make(map[string]int, len(ps))
	ids := make([]any, 0, len(ps))
	for i, p := range ps {
		idx[p.ID] = i
		ids = append(ids, p.ID)
	}
	for start := 0; start < len(ids); start += maxIn {
		chunk := ids[start:min(start+maxIn, len(ids))]
		rows, err := tx.QueryContext(ctx, "SELECT id, plan_id, kind, amount, currency, note, created_at, removed_at FROM deferral_plan_adjustments"+
			" WHERE plan_id IN ("+placeholders(len(chunk))+") ORDER BY created_at, id", chunk...)
		if err != nil {
			return err
		}
		for rows.Next() {
			a, err := scanAdjustment(rows)
			if err != nil {
				_ = rows.Close()
				return err
			}
			p := &ps[idx[a.PlanID]]
			p.Adjustments = append(p.Adjustments, a)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
	}
	return nil
}

func scanAdjustment(row interface{ Scan(...any) error }) (deferrals.Adjustment, error) {
	var a deferrals.Adjustment
	var kind string
	var amt sqlvalue.Amount
	var cur sqlvalue.Currency
	var note sql.Null[string]
	var created store.Timestamp
	var removed sql.Null[store.Timestamp]
	if err := row.Scan(&a.ID, &a.PlanID, &kind, &amt, &cur, &note, &created, &removed); err != nil {
		return deferrals.Adjustment{}, err
	}
	m, err := sqlvalue.Money(amt, cur)
	if err != nil {
		return deferrals.Adjustment{}, err
	}
	a.Kind, a.Amount, a.CreatedAt = deferrals.AdjustmentKind(kind), m, created.Time
	if note.Valid {
		n := note.V
		a.Note = &n
	}
	if removed.Valid {
		t := removed.V.Time
		a.RemovedAt = &t
	}
	return a, nil
}

func getTx(ctx context.Context, tx *sql.Tx, id string) (deferrals.Plan, error) {
	ps, err := plans(ctx, tx, " WHERE p.id = ?", id)
	if err != nil {
		return deferrals.Plan{}, err
	}
	if len(ps) == 0 {
		return deferrals.Plan{}, resource.ErrNotFound
	}
	return ps[0], nil
}

// Get returns one plan or resource.ErrNotFound.
func (s *Store) Get(ctx context.Context, id string) (deferrals.Plan, error) {
	var p deferrals.Plan
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		p, err = getTx(ctx, tx, id)
		return err
	})
	return p, err
}

// List returns up to f.Limit plans matching f after f.After, ordered by
// identifier.
func (s *Store) List(ctx context.Context, f deferrals.ListFilter) ([]deferrals.Plan, error) {
	var where []string
	var args []any
	in := func(col string, vs []string) {
		if len(vs) == 0 {
			return
		}
		where = append(where, col+" IN ("+placeholders(len(vs))+")")
		for _, v := range vs {
			args = append(args, v)
		}
	}
	in("p.occurrence_id", f.OccurrenceIDs)
	in("o.commitment_id", f.CommitmentIDs)
	if len(f.States) == 1 {
		if f.States[0] == deferrals.StateActive {
			where = append(where, "p.undone_at IS NULL")
		} else {
			where = append(where, "p.undone_at IS NOT NULL")
		}
	}
	if !f.PlannedMonth.IsZero() {
		where = append(where, "p.planned_date BETWEEN ? AND ?")
		args = append(args, f.PlannedMonth.First().String(), f.PlannedMonth.Last().String())
	}
	if f.After != "" {
		where = append(where, "p.id > ?")
		args = append(args, f.After)
	}
	q := ""
	if len(where) > 0 {
		q = " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY p.id"
	if f.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, f.Limit)
	}
	var out []deferrals.Plan
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		out, err = plans(ctx, tx, q, args...)
		return err
	})
	return out, err
}

// Active returns the active plan of each listed occurrence that has one.
func (s *Store) Active(ctx context.Context, occurrenceIDs []string) (map[string]deferrals.Plan, error) {
	out := make(map[string]deferrals.Plan, len(occurrenceIDs))
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for start := 0; start < len(occurrenceIDs); start += maxIn {
			chunk := occurrenceIDs[start:min(start+maxIn, len(occurrenceIDs))]
			args := make([]any, len(chunk))
			for i, id := range chunk {
				args[i] = id
			}
			ps, err := plans(ctx, tx, " WHERE p.undone_at IS NULL AND p.occurrence_id IN ("+placeholders(len(chunk))+")", args...)
			if err != nil {
				return err
			}
			for _, p := range ps {
				out[p.OccurrenceID] = p
			}
		}
		return nil
	})
	return out, err
}

// PlannedIn returns the active plans planned in month, by planned date.
func (s *Store) PlannedIn(ctx context.Context, month calendar.YearMonth) ([]deferrals.Plan, error) {
	var out []deferrals.Plan
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		out, err = plans(ctx, tx, " WHERE p.undone_at IS NULL AND p.planned_date BETWEEN ? AND ? ORDER BY p.planned_date, p.id",
			month.First().String(), month.Last().String())
		return err
	})
	return out, err
}

// History returns a plan's history, oldest first.
func (s *Store) History(ctx context.Context, id string) ([]deferrals.Event, error) {
	var out []deferrals.Event
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM deferral_plans WHERE id = ?", id).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return resource.ErrNotFound
		}
		rows, err := tx.QueryContext(ctx, "SELECT plan_version, action, plan_type, planned_date, reason, adjustment_id, actor_kind, actor_user, actor_via, at"+
			" FROM deferral_plan_events WHERE plan_id = ? ORDER BY plan_version", id)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var e deferrals.Event
			var action, typ, kind string
			var planned sqlvalue.Date
			var reason, adj, user, via sql.Null[string]
			var at store.Timestamp
			if err := rows.Scan(&e.Version, &action, &typ, &planned, &reason, &adj, &kind, &user, &via, &at); err != nil {
				return err
			}
			e.Action, e.Type, e.PlannedDate, e.AdjustmentID, e.At = deferrals.Action(action), deferrals.PlanType(typ), planned.Date, adj.V, at.Time
			e.By = deferrals.By{Kind: audit.ActorKind(kind), UserID: user.V, Via: audit.Via(via.V)}
			if reason.Valid {
				r := reason.V
				e.Reason = &r
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableText(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// event appends one history entry: the plan as it is after the change.
func (s *Store) event(ctx context.Context, tx *sql.Tx, actor audit.Actor, p deferrals.Plan, action deferrals.Action, reason *string, adjustmentID string, at time.Time) error {
	by := deferrals.ByOf(actor)
	_, err := tx.ExecContext(ctx, "INSERT INTO deferral_plan_events (id, plan_id, plan_version, action, plan_type, planned_date, reason, adjustment_id, actor_kind, actor_user, actor_via, at)"+
		" VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		s.newID(), p.ID, p.Version, string(action), string(p.Type), sqlvalue.Date{Date: p.PlannedDate}, nullableText(reason), nullable(adjustmentID),
		string(by.Kind), nullable(by.UserID), nullable(string(by.Via)), store.Timestamp{Time: at})
	return err
}

// stateTx derives the occurrence's payment state today in the caller's
// transaction, from the same facts every other reader uses.
func (s *Store) stateTx(ctx context.Context, tx *sql.Tx, occurrenceID string, today calendar.Date) (payments.State, error) {
	o, err := occstore.GetTx(ctx, tx, occurrenceID)
	if err != nil {
		return payments.State{}, err
	}
	facts, err := paystore.FactsTx(ctx, tx, s.zone, []string{o.ID})
	if err != nil {
		return payments.State{}, err
	}
	return payments.Derive(o, facts[o.ID], today, nil), nil
}

func validation(errs []commitments.FieldError) error {
	if len(errs) == 0 {
		return nil
	}
	return &commitments.ValidationError{Errors: errs}
}

// Create validates and stores a new plan.
func (s *Store) Create(ctx context.Context, actor audit.Actor, in deferrals.NewPlan, today calendar.Date) (deferrals.Plan, error) {
	if err := validation(in.Validate()); err != nil {
		return deferrals.Plan{}, err
	}
	var out deferrals.Plan
	now := s.now()
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		o, err := occstore.GetTx(ctx, tx, in.OccurrenceID)
		if errors.Is(err, resource.ErrNotFound) {
			return deferrals.ErrOccurrenceNotFound
		}
		if err != nil {
			return err
		}
		st, err := s.stateTx(ctx, tx, o.ID, today)
		if err != nil {
			return err
		}
		if err := deferrals.CheckOccurrence(o, st, in.PlannedDate, today); err != nil {
			return err
		}
		var active int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM deferral_plans WHERE occurrence_id = ? AND undone_at IS NULL", o.ID).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			return deferrals.ErrActivePlan
		}
		p := deferrals.Plan{ID: s.newID(), OccurrenceID: o.ID, CommitmentID: o.CommitmentID, Type: in.Type, PlannedDate: in.PlannedDate, Reason: in.Reason,
			OriginalDueDate: o.DueDate, OriginalAmount: o.Amount, OutstandingWhenPlanned: st.Outstanding, Version: 1, CreatedAt: now, UpdatedAt: now}
		ts := store.Timestamp{Time: now}
		if _, err := tx.ExecContext(ctx, "INSERT INTO deferral_plans (id, occurrence_id, plan_type, planned_date, reason, original_due_date, original_amount, original_currency,"+
			" outstanding_amount, outstanding_currency, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			p.ID, p.OccurrenceID, string(p.Type), sqlvalue.Date{Date: p.PlannedDate}, nullableText(p.Reason), sqlvalue.Date{Date: p.OriginalDueDate},
			sqlvalue.Amount{Decimal: p.OriginalAmount.Amount()}, sqlvalue.Currency{Currency: p.OriginalAmount.Currency()},
			sqlvalue.Amount{Decimal: p.OutstandingWhenPlanned.Amount()}, sqlvalue.Currency{Currency: p.OutstandingWhenPlanned.Currency()}, ts, ts); err != nil {
			return err
		}
		if err := s.event(ctx, tx, actor, p, deferrals.ActionCreated, p.Reason, "", now); err != nil {
			return err
		}
		if err := s.audit.WriteAudit(ctx, tx, deferrals.CreatedEvent(actor, p, now)); err != nil {
			return err
		}
		out, err = getTx(ctx, tx, p.ID)
		return err
	})
	return out, err
}

// activeAt loads a plan for a change: it must exist, still have version
// expected and be active.
func activeAt(ctx context.Context, tx *sql.Tx, id string, expected int64) (deferrals.Plan, error) {
	p, err := getTx(ctx, tx, id)
	if err != nil {
		return deferrals.Plan{}, err
	}
	if p.Version != expected {
		return deferrals.Plan{}, &resource.VersionConflictError{Current: p.Version}
	}
	if !p.Active() {
		return deferrals.Plan{}, deferrals.ErrUndone
	}
	return p, nil
}

func sameReason(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// Revise replaces an active plan's fields.
func (s *Store) Revise(ctx context.Context, actor audit.Actor, id string, expected int64, f deferrals.Fields, today calendar.Date) (deferrals.Plan, error) {
	if err := validation(f.Validate()); err != nil {
		return deferrals.Plan{}, err
	}
	var out deferrals.Plan
	now := s.now()
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		p, err := activeAt(ctx, tx, id, expected)
		if err != nil {
			return err
		}
		var fields []string
		if f.Type != p.Type {
			fields = append(fields, "plan_type")
		}
		if f.PlannedDate != p.PlannedDate {
			fields = append(fields, "planned_date")
		}
		if !sameReason(f.Reason, p.Reason) {
			fields = append(fields, "reason")
		}
		if len(fields) == 0 {
			return deferrals.ErrNoChange
		}
		o, err := occstore.GetTx(ctx, tx, p.OccurrenceID)
		if err != nil {
			return err
		}
		st, err := s.stateTx(ctx, tx, o.ID, today)
		if err != nil {
			return err
		}
		if err := deferrals.CheckOccurrence(o, st, f.PlannedDate, today); err != nil {
			return err
		}
		v, err := store.UpdateVersioned(ctx, tx, "deferral_plans", "plan_type = ?, planned_date = ?, reason = ?, updated_at = ?", p.ID, expected,
			string(f.Type), sqlvalue.Date{Date: f.PlannedDate}, nullableText(f.Reason), store.Timestamp{Time: now})
		if err != nil {
			return err
		}
		p.Type, p.PlannedDate, p.Reason, p.Version = f.Type, f.PlannedDate, f.Reason, v
		if err := s.event(ctx, tx, actor, p, deferrals.ActionRevised, p.Reason, "", now); err != nil {
			return err
		}
		if err := s.audit.WriteAudit(ctx, tx, deferrals.RevisedEvent(actor, p, fields, now)); err != nil {
			return err
		}
		out, err = getTx(ctx, tx, p.ID)
		return err
	})
	return out, err
}

// Undo ends an active plan. The occurrence is untouched: its outstanding
// amount is planned in its own month again.
func (s *Store) Undo(ctx context.Context, actor audit.Actor, id string, expected int64, reason *string) (deferrals.Plan, error) {
	if err := validation(deferrals.ValidateReason(reason)); err != nil {
		return deferrals.Plan{}, err
	}
	var out deferrals.Plan
	now := s.now()
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		p, err := activeAt(ctx, tx, id, expected)
		if err != nil {
			return err
		}
		ts := store.Timestamp{Time: now}
		v, err := store.UpdateVersioned(ctx, tx, "deferral_plans", "undone_at = ?, updated_at = ?", p.ID, expected, ts, ts)
		if err != nil {
			return err
		}
		p.Version, p.UndoneAt = v, &now
		if err := s.event(ctx, tx, actor, p, deferrals.ActionUndone, reason, "", now); err != nil {
			return err
		}
		if err := s.audit.WriteAudit(ctx, tx, deferrals.UndoneEvent(actor, p, now)); err != nil {
			return err
		}
		out, err = getTx(ctx, tx, p.ID)
		return err
	})
	return out, err
}

// AddAdjustment records an explicit adjustment on an active plan.
func (s *Store) AddAdjustment(ctx context.Context, actor audit.Actor, id string, expected int64, in deferrals.NewAdjustment) (deferrals.Plan, deferrals.Adjustment, error) {
	var out deferrals.Plan
	var adj deferrals.Adjustment
	now := s.now()
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		p, err := activeAt(ctx, tx, id, expected)
		if err != nil {
			return err
		}
		if err := validation(in.Validate(p.OriginalAmount.Currency())); err != nil {
			return err
		}
		adj = deferrals.Adjustment{ID: s.newID(), PlanID: p.ID, Kind: in.Kind, Amount: in.Amount, Note: in.Note, CreatedAt: now}
		ts := store.Timestamp{Time: now}
		if _, err := tx.ExecContext(ctx, "INSERT INTO deferral_plan_adjustments (id, plan_id, kind, amount, currency, note, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
			adj.ID, p.ID, string(adj.Kind), sqlvalue.Amount{Decimal: adj.Amount.Amount()}, sqlvalue.Currency{Currency: adj.Amount.Currency()}, nullableText(adj.Note), ts); err != nil {
			return err
		}
		v, err := store.UpdateVersioned(ctx, tx, "deferral_plans", "updated_at = ?", p.ID, expected, ts)
		if err != nil {
			return err
		}
		p.Version = v
		if err := s.event(ctx, tx, actor, p, deferrals.ActionAdjustmentAdded, nil, adj.ID, now); err != nil {
			return err
		}
		if err := s.audit.WriteAudit(ctx, tx, deferrals.AdjustmentAddedEvent(actor, p, adj, now)); err != nil {
			return err
		}
		out, err = getTx(ctx, tx, p.ID)
		return err
	})
	return out, adj, err
}

// RemoveAdjustment removes an adjustment of an active plan; it stays as
// history.
func (s *Store) RemoveAdjustment(ctx context.Context, actor audit.Actor, id, adjustmentID string, expected int64) (deferrals.Plan, error) {
	var out deferrals.Plan
	now := s.now()
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		p, err := activeAt(ctx, tx, id, expected)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(p.Adjustments, func(a deferrals.Adjustment) bool { return a.ID == adjustmentID })
		if i < 0 {
			return resource.ErrNotFound
		}
		adj := p.Adjustments[i]
		if adj.RemovedAt != nil {
			return deferrals.ErrAdjustmentRemoved
		}
		ts := store.Timestamp{Time: now}
		if _, err := tx.ExecContext(ctx, "UPDATE deferral_plan_adjustments SET removed_at = ? WHERE id = ?", ts, adj.ID); err != nil {
			return err
		}
		v, err := store.UpdateVersioned(ctx, tx, "deferral_plans", "updated_at = ?", p.ID, expected, ts)
		if err != nil {
			return err
		}
		p.Version = v
		if err := s.event(ctx, tx, actor, p, deferrals.ActionAdjustmentRemoved, nil, adj.ID, now); err != nil {
			return err
		}
		if err := s.audit.WriteAudit(ctx, tx, deferrals.AdjustmentRemovedEvent(actor, p, adj, now)); err != nil {
			return err
		}
		out, err = getTx(ctx, tx, p.ID)
		return err
	})
	return out, err
}
