// Package payments persists manual payment records and manual status
// overrides (task C3, ADR-0031) in the tables of migration 0014_payment_state
// and implements [payments.Store]. Payment status itself is never stored: it
// is derived by [payments.Derive] from the facts this package reads.
//
// Every write runs in one transaction that also increases the occurrence's
// version (its ETag covers its payment state) and records the audit event.
package payments

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/payments"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/store"
	occstore "github.com/aurimasniekis/go-ledger/internal/store/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/store/sqlvalue"
)

// EvidenceSources are the queries that list payment evidence, one per
// evidence table. Each yields the columns occurrence_id, kind, id, amount,
// currency and evidence_date, in that order and with those names. Task X1
// adds its confirmed bank-transaction allocations here (one row per
// transaction and occurrence, with the allocated amount, so one transaction
// can pay several occurrences and an occurrence can be paid in parts,
// decision D8 option a), and the matching expression to
// internal/store/occurrences DefaultEvidence; TestEvidenceSourcesMatchDefaultEvidence
// checks the two agree.
var EvidenceSources = []string{
	// Manual payments that are not deleted (D26-3a) and not replaced by a
	// confirmed bank match (D45, ADR-0049): a replaced record is kept, but
	// the match counts instead, so the payment never counts twice.
	"SELECT occurrence_id AS occurrence_id, '" + string(payments.EvidenceManual) + "' AS kind, id AS id, amount AS amount, currency AS currency, paid_on AS evidence_date FROM manual_payments WHERE deleted_at IS NULL" +
		" AND NOT EXISTS (SELECT 1 FROM manual_payment_replacements r WHERE r.manual_payment_id = manual_payments.id AND r.restored_at IS NULL)",
	// Confirmed payment matches (X1, ADR-0035) on a countable transaction:
	// booked, still reported and not superseded (ADR-0020 point 7).
	"SELECT m.occurrence_id AS occurrence_id, '" + string(payments.EvidenceBankTransaction) + "' AS kind, m.id AS id, m.amount AS amount, m.currency AS currency," +
		" coalesce(t.booking_date, t.value_date, t.transaction_date) AS evidence_date FROM payment_matches m JOIN bank_transactions t ON t.id = m.transaction_id" +
		" WHERE m.state = 'confirmed' AND t.booking_status = 'booked' AND t.no_longer_reported_at IS NULL AND t.superseded_by IS NULL",
}

// Store implements [payments.Store] on a [store.DB].
type Store struct {
	db    *store.DB
	audit store.AuditWriter
	now   func() time.Time
	newID func() string
	// zone is the reporting zone in which a commitment's creation instant
	// becomes the date before which unpaid occurrences are "not recorded"
	// (D38 1a, ADR-0042); UTC when unset.
	zone calendar.Zone
}

var _ payments.Store = (*Store)(nil)

// New returns a Store. aw records audit events and is required; now and newID
// default to time.Now and resource.NewID.
func New(db *store.DB, aw store.AuditWriter, now func() time.Time, newID func() string) *Store {
	if aw == nil {
		panic("payments.New: an audit writer is required")
	}
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = resource.NewID
	}
	return &Store{db: db, audit: aw, now: now, newID: newID}
}

// WithZone returns a copy of the store that reads commitment creation dates in
// the reporting zone z (the zero zone means UTC).
func (s *Store) WithZone(z calendar.Zone) *Store {
	if s == nil {
		return nil
	}
	c := *s
	c.zone = z
	return &c
}

// manualColumns are a manual payment's columns, and the match that replaces it
// now (D45, ADR-0049), if any; the queries select FROM manual_payments.
const manualColumns = "id, occurrence_id, amount, currency, paid_on, note, version, created_at, updated_at," +
	" (SELECT r.match_id FROM manual_payment_replacements r WHERE r.manual_payment_id = manual_payments.id AND r.restored_at IS NULL)"

func scanManual(row interface{ Scan(...any) error }) (payments.ManualPayment, error) {
	var p payments.ManualPayment
	var amount sqlvalue.Amount
	var currency sqlvalue.Currency
	var paid sqlvalue.Date
	var note sql.NullString
	var created, updated store.Timestamp
	var replacedBy sql.Null[string]
	if err := row.Scan(&p.ID, &p.OccurrenceID, &amount, &currency, &paid, &note, &p.Version, &created, &updated, &replacedBy); err != nil {
		return payments.ManualPayment{}, err
	}
	m, err := sqlvalue.Money(amount, currency)
	if err != nil {
		return payments.ManualPayment{}, err
	}
	p.Amount, p.PaidOn, p.CreatedAt, p.UpdatedAt, p.ReplacedByMatchID = m, paid.Date, created.Time, updated.Time, replacedBy.V
	if note.Valid {
		p.Note = &note.String
	}
	return p, nil
}

// maxIn bounds the identifiers bound in one IN list.
const maxIn = 500

func placeholders(n int) string { return "?" + strings.Repeat(", ?", n-1) }

// factsTx reads the evidence and active override of each listed occurrence.
func factsTx(ctx context.Context, tx *sql.Tx, zone calendar.Zone, ids []string) (map[string]payments.Facts, error) {
	out := make(map[string]payments.Facts, len(ids))
	for start := 0; start < len(ids); start += maxIn {
		chunk := ids[start:min(start+maxIn, len(ids))]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		if err := evidenceTx(ctx, tx, chunk, args, out); err != nil {
			return nil, err
		}
		if err := overridesTx(ctx, tx, chunk, args, out); err != nil {
			return nil, err
		}
		if err := commitmentTx(ctx, tx, zone, chunk, args, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func evidenceTx(ctx context.Context, tx *sql.Tx, chunk []string, args []any, out map[string]payments.Facts) error {
	rows, err := tx.QueryContext(ctx, "SELECT occurrence_id, kind, id, amount, currency, evidence_date FROM ("+
		strings.Join(EvidenceSources, " UNION ALL ")+") WHERE occurrence_id IN ("+placeholders(len(chunk))+") ORDER BY evidence_date, id", args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var e payments.Evidence
		var kind string
		var amount sqlvalue.Amount
		var currency sqlvalue.Currency
		var date sqlvalue.Date
		if err := rows.Scan(&e.OccurrenceID, &kind, &e.ID, &amount, &currency, &date); err != nil {
			return err
		}
		m, err := sqlvalue.Money(amount, currency)
		if err != nil {
			return err
		}
		e.Kind, e.Amount, e.Date = payments.EvidenceKind(kind), m, date.Date
		f := out[e.OccurrenceID]
		f.Evidence = append(f.Evidence, e)
		out[e.OccurrenceID] = f
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return withinToleranceTx(ctx, tx, chunk, args, out)
}

// withinToleranceTx attaches to each bank evidence the tolerance its match was
// confirmed within, as recorded at confirmation (migration 0021); the
// commitment's current tolerance is never read, so history does not change.
func withinToleranceTx(ctx context.Context, tx *sql.Tx, chunk []string, args []any, out map[string]payments.Facts) error {
	rows, err := tx.QueryContext(ctx, "SELECT id, occurrence_id, tolerance_amount, tolerance_currency FROM payment_matches"+
		" WHERE state = 'confirmed' AND tolerance_amount IS NOT NULL AND occurrence_id IN ("+placeholders(len(chunk))+")", args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, occ string
		var amt sqlvalue.Amount
		var cur sqlvalue.Currency
		if err := rows.Scan(&id, &occ, &amt, &cur); err != nil {
			return err
		}
		m, err := sqlvalue.Money(amt, cur)
		if err != nil {
			return err
		}
		f := out[occ]
		for i := range f.Evidence {
			if f.Evidence[i].Kind == payments.EvidenceBankTransaction && f.Evidence[i].ID == id {
				f.Evidence[i].ConfirmedWithin = &m
			}
		}
	}
	return rows.Err()
}

// commitmentTx adds the commitment's creation date, read in zone (the
// reporting zone, D38 1a, ADR-0042), to the facts of every listed occurrence.
func commitmentTx(ctx context.Context, tx *sql.Tx, zone calendar.Zone, chunk []string, args []any, out map[string]payments.Facts) error {
	rows, err := tx.QueryContext(ctx, "SELECT o.id, c.created_at FROM commitment_occurrences o"+
		" JOIN commitments c ON c.id = o.commitment_id WHERE o.id IN ("+placeholders(len(chunk))+")", args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	if zone.IsZero() {
		zone = calendar.MustLoadZone("UTC")
	}
	for rows.Next() {
		var id string
		var created store.Timestamp
		if err := rows.Scan(&id, &created); err != nil {
			return err
		}
		f := out[id]
		if f.CommitmentCreatedOn, err = zone.DateOf(created.Time); err != nil {
			return err
		}
		out[id] = f
	}
	return rows.Err()
}

func overridesTx(ctx context.Context, tx *sql.Tx, chunk []string, args []any, out map[string]payments.Facts) error {
	rows, err := tx.QueryContext(ctx, "SELECT a.id, a.occurrence_id, a.status, a.reason, a.set_at, p.status FROM occurrence_status_overrides a"+
		" LEFT JOIN occurrence_status_overrides p ON p.id = a.previous_id"+
		" WHERE a.state = 'active' AND a.occurrence_id IN ("+placeholders(len(chunk))+")", args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var o payments.Override
		var status string
		var set store.Timestamp
		var prev sql.NullString
		if err := rows.Scan(&o.ID, &o.OccurrenceID, &status, &o.Reason, &set, &prev); err != nil {
			return err
		}
		o.Status, o.SetAt = payments.OverrideStatus(status), set.Time
		if prev.Valid {
			p := payments.OverrideStatus(prev.String)
			o.Previous = &p
		}
		f := out[o.OccurrenceID]
		f.Override = &o
		out[o.OccurrenceID] = f
	}
	return rows.Err()
}

// FactsTx reads the facts of each listed occurrence inside tx (for other
// stores that write payment evidence, such as payment matches).
func FactsTx(ctx context.Context, tx *sql.Tx, zone calendar.Zone, ids []string) (map[string]payments.Facts, error) {
	return factsTx(ctx, tx, zone, ids)
}

// ChangeTx reads the occurrence and its facts after a payment-state write
// inside tx.
func ChangeTx(ctx context.Context, tx *sql.Tx, zone calendar.Zone, id string) (payments.Change, error) {
	return change(ctx, tx, zone, id)
}

// Facts returns the facts of each listed occurrence.
func (s *Store) Facts(ctx context.Context, ids []string) (map[string]payments.Facts, error) {
	var out map[string]payments.Facts
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		out, err = factsTx(ctx, tx, s.zone, ids)
		return err
	})
	return out, err
}

// Manual lists an occurrence's manual payments by payment date, then ID.
func (s *Store) Manual(ctx context.Context, occurrenceID string) ([]payments.ManualPayment, error) {
	var out []payments.ManualPayment
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := occstore.GetTx(ctx, tx, occurrenceID); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT "+manualColumns+" FROM manual_payments WHERE occurrence_id = ? AND deleted_at IS NULL ORDER BY paid_on, id", occurrenceID)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			p, err := scanManual(rows)
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

func getManual(ctx context.Context, tx *sql.Tx, occurrenceID, id string) (payments.ManualPayment, error) {
	p, err := scanManual(tx.QueryRowContext(ctx, "SELECT "+manualColumns+" FROM manual_payments WHERE id = ? AND occurrence_id = ? AND deleted_at IS NULL", id, occurrenceID))
	if errors.Is(err, sql.ErrNoRows) {
		return payments.ManualPayment{}, resource.ErrNotFound
	}
	return p, err
}

// GetManual returns one manual payment of the occurrence.
func (s *Store) GetManual(ctx context.Context, occurrenceID, id string) (payments.ManualPayment, error) {
	var p payments.ManualPayment
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		p, err = getManual(ctx, tx, occurrenceID, id)
		return err
	})
	return p, err
}

// bump increases the occurrence's version: every payment-state change does,
// so its ETag covers its payment state.
func (s *Store) bump(ctx context.Context, tx *sql.Tx, id string, version int64, now time.Time) error {
	_, err := store.UpdateVersioned(ctx, tx, "commitment_occurrences", "updated_at = ?", id, version, store.Timestamp{Time: now})
	return err
}

// change reads the occurrence and its facts after a write.
func change(ctx context.Context, tx *sql.Tx, zone calendar.Zone, id string) (payments.Change, error) {
	o, err := occstore.GetTx(ctx, tx, id)
	if err != nil {
		return payments.Change{}, err
	}
	f, err := factsTx(ctx, tx, zone, []string{id})
	if err != nil {
		return payments.Change{}, err
	}
	return payments.Change{Occurrence: o, Facts: f[id]}, nil
}

// RecordManual validates in against the occurrence and today's date in the
// reporting zone, stores it and records manual_payment.recorded. Like any
// create it needs no version.
func (s *Store) RecordManual(ctx context.Context, actor audit.Actor, occurrenceID string, in payments.NewManual, today calendar.Date) (payments.ManualPayment, payments.Change, error) {
	var p payments.ManualPayment
	var ch payments.Change
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		o, err := occstore.GetTx(ctx, tx, occurrenceID)
		if err != nil {
			return err
		}
		if o.CancelledAt != nil {
			return occurrences.ErrCancelled
		}
		if err := commitments.Validation(in.Validate(o, today)); err != nil {
			return err
		}
		now := s.now().UTC()
		p = payments.ManualPayment{ID: s.newID(), OccurrenceID: o.ID, Amount: in.Amount, PaidOn: in.PaidOn, Note: in.Note, Version: 1, CreatedAt: now, UpdatedAt: now}
		if _, err := tx.ExecContext(ctx, "INSERT INTO manual_payments (id, occurrence_id, amount, currency, paid_on, note, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
			p.ID, p.OccurrenceID, sqlvalue.Amount{Decimal: p.Amount.Amount()}, sqlvalue.Currency{Currency: p.Amount.Currency()}, sqlvalue.Date{Date: p.PaidOn},
			p.Note, store.Timestamp{Time: now}, store.Timestamp{Time: now}); err != nil {
			return fmt.Errorf("payments: record: %w", err)
		}
		if err := s.bump(ctx, tx, o.ID, o.Version, now); err != nil {
			return err
		}
		if err := s.audit.WriteAudit(ctx, tx, payments.RecordedEvent(actor, p, now)); err != nil {
			return err
		}
		ch, err = change(ctx, tx, s.zone, o.ID)
		return err
	})
	if err != nil {
		return payments.ManualPayment{}, payments.Change{}, err
	}
	return p, ch, nil
}

// deletedBy is the actor as stored in manual_payments.deleted_by.
func deletedBy(a audit.Actor) string {
	switch {
	case a.TokenID != "":
		return "token:" + a.TokenID
	case a.UserID != "":
		return "user:" + a.UserID
	}
	return string(a.Kind)
}

// DeleteManual soft-deletes a manual payment that still has version expected:
// the row is kept, marked deleted, excluded from evidence and lists, and
// manual_payment.deleted is recorded with its amount and currency. The occurrence's status is derived again
// from what evidence remains.
func (s *Store) DeleteManual(ctx context.Context, actor audit.Actor, occurrenceID, id string, expected int64) (payments.Change, error) {
	var ch payments.Change
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		o, err := occstore.GetTx(ctx, tx, occurrenceID)
		if err != nil {
			return err
		}
		p, err := getManual(ctx, tx, occurrenceID, id)
		if err != nil {
			return err
		}
		if p.Version != expected {
			return &resource.VersionConflictError{Expected: expected, Current: p.Version}
		}
		now := s.now().UTC()
		// Soft delete (D26-3a): the row stays, marked, and stops counting.
		if _, err := store.UpdateVersioned(ctx, tx, "manual_payments", "deleted_at = ?, deleted_by = ?, updated_at = ?", id, expected,
			store.Timestamp{Time: now}, deletedBy(actor), store.Timestamp{Time: now}); err != nil {
			return err
		}
		if err := s.bump(ctx, tx, o.ID, o.Version, now); err != nil {
			return err
		}
		if err := s.audit.WriteAudit(ctx, tx, payments.DeletedEvent(actor, p, now)); err != nil {
			return err
		}
		ch, err = change(ctx, tx, s.zone, o.ID)
		return err
	})
	return ch, err
}

// occurrenceAt reads the occurrence and checks its version.
func occurrenceAt(ctx context.Context, tx *sql.Tx, id string, expected int64) (occurrences.Occurrence, error) {
	o, err := occstore.GetTx(ctx, tx, id)
	if err != nil {
		return occurrences.Occurrence{}, err
	}
	if o.Version != expected {
		return occurrences.Occurrence{}, &resource.VersionConflictError{Expected: expected, Current: o.Version}
	}
	return o, nil
}

// SetOverride sets the occurrence's manual status override, superseding the
// active one (which an undo restores), and records
// occurrence.status_overridden. A "paid" override needs payment evidence.
func (s *Store) SetOverride(ctx context.Context, actor audit.Actor, occurrenceID string, expected int64, status payments.OverrideStatus, reason string) (payments.Change, error) {
	if err := commitments.Validation(payments.ValidateOverride(status, reason)); err != nil {
		return payments.Change{}, err
	}
	var ch payments.Change
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		o, err := occurrenceAt(ctx, tx, occurrenceID, expected)
		if err != nil {
			return err
		}
		if o.CancelledAt != nil {
			return occurrences.ErrCancelled
		}
		facts, err := factsTx(ctx, tx, s.zone, []string{o.ID})
		if err != nil {
			return err
		}
		f := facts[o.ID]
		if status == payments.OverridePaid && len(f.Evidence) == 0 {
			return payments.ErrNoEvidence
		}
		now := s.now().UTC()
		var previous any
		var prevStatus *payments.OverrideStatus
		if cur := f.Override; cur != nil {
			if _, err := tx.ExecContext(ctx, "UPDATE occurrence_status_overrides SET state = 'superseded', ended_at = ? WHERE id = ? AND state = 'active'",
				store.Timestamp{Time: now}, cur.ID); err != nil {
				return err
			}
			previous, prevStatus = cur.ID, &cur.Status
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO occurrence_status_overrides (id, occurrence_id, status, reason, previous_id, state, set_at) VALUES (?, ?, ?, ?, ?, 'active', ?)",
			s.newID(), o.ID, string(status), reason, previous, store.Timestamp{Time: now}); err != nil {
			return fmt.Errorf("payments: override: %w", err)
		}
		if err := s.bump(ctx, tx, o.ID, o.Version, now); err != nil {
			return err
		}
		if err := s.audit.WriteAudit(ctx, tx, payments.OverriddenEvent(actor, o.ID, status, prevStatus, now)); err != nil {
			return err
		}
		ch, err = change(ctx, tx, s.zone, o.ID)
		return err
	})
	return ch, err
}

// UndoOverride undoes the active override, restoring the one it superseded
// (or none, so the derived status applies again), and records
// occurrence.status_override_undone.
func (s *Store) UndoOverride(ctx context.Context, actor audit.Actor, occurrenceID string, expected int64) (payments.Change, error) {
	var ch payments.Change
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		o, err := occurrenceAt(ctx, tx, occurrenceID, expected)
		if err != nil {
			return err
		}
		var id, status string
		var prev sql.NullString
		err = tx.QueryRowContext(ctx, "SELECT id, status, previous_id FROM occurrence_status_overrides WHERE occurrence_id = ? AND state = 'active'", o.ID).Scan(&id, &status, &prev)
		if errors.Is(err, sql.ErrNoRows) {
			return payments.ErrNoOverride
		}
		if err != nil {
			return err
		}
		now := s.now().UTC()
		if _, err := tx.ExecContext(ctx, "UPDATE occurrence_status_overrides SET state = 'undone', ended_at = ? WHERE id = ?", store.Timestamp{Time: now}, id); err != nil {
			return err
		}
		var restored *payments.OverrideStatus
		if prev.Valid {
			var ps string
			if err := tx.QueryRowContext(ctx, "UPDATE occurrence_status_overrides SET state = 'active', ended_at = NULL WHERE id = ? AND state = 'superseded' RETURNING status",
				prev.String).Scan(&ps); err != nil {
				return fmt.Errorf("payments: restore the previous override: %w", err)
			}
			r := payments.OverrideStatus(ps)
			restored = &r
		}
		if err := s.bump(ctx, tx, o.ID, o.Version, now); err != nil {
			return err
		}
		if err := s.audit.WriteAudit(ctx, tx, payments.UndoneEvent(actor, o.ID, payments.OverrideStatus(status), restored, now)); err != nil {
			return err
		}
		ch, err = change(ctx, tx, s.zone, o.ID)
		return err
	})
	return ch, err
}
