// Package matches persists payment matches (task X1, ADR-0035) in the tables
// of migration 0017_payment_matches and implements [matches.Store].
//
// Every write runs in one transaction that re-checks the match rules against
// the stored data (the single writer connection serialises writers, so the
// checks cannot race), writes the match and its history entry, increases the
// occurrence's version and records the audit event. The database backs the
// rules with constraints and triggers: one open match per transaction and
// occurrence, the same currency on all three, confirmation only on a booked
// and reported transaction, allowed state changes only, and no deletion of
// confirmed or reversed matches. Allocation sums are exact decimals and are
// never computed in SQL.
package matches

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/matches"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/payments"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/store"
	occstore "github.com/aurimasniekis/go-ledger/internal/store/occurrences"
	paymentstore "github.com/aurimasniekis/go-ledger/internal/store/payments"
	"github.com/aurimasniekis/go-ledger/internal/store/sqlvalue"
)

// Store implements [matches.Store] on a [store.DB].
type Store struct {
	db    *store.DB
	audit store.AuditWriter
	now   func() time.Time
	newID func() string
	// zone is the reporting zone payment facts are derived in (see
	// paymentstore.Store.WithZone); UTC when unset.
	zone calendar.Zone
}

// Zone returns the reporting zone the store derives payment facts in (the zero
// zone means UTC).
func (s *Store) Zone() calendar.Zone { return s.zone }

// WithZone returns a copy of the store that derives payment facts in the
// reporting zone z (the zero zone means UTC).
func (s *Store) WithZone(z calendar.Zone) *Store {
	if s == nil {
		return nil
	}
	c := *s
	c.zone = z
	return &c
}

var _ matches.Store = (*Store)(nil)

// New returns a Store. aw records audit events and is required; now and newID
// default to time.Now and resource.NewID.
func New(db *store.DB, aw store.AuditWriter, now func() time.Time, newID func() string) *Store {
	if aw == nil {
		panic("matches.New: an audit writer is required")
	}
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = resource.NewID
	}
	return &Store{db: db, audit: aw, now: now, newID: newID}
}

const selectMatches = "SELECT m.id, m.transaction_id, m.occurrence_id, m.amount, m.currency, m.origin, m.state, m.created_by_kind, m.created_by_user," +
	" m.decided_by_kind, m.decided_by_user, m.decided_at, m.reason, m.version, m.created_at, m.updated_at, m.created_by_via, m.decided_by_via, " + transactionColumns +
	" FROM payment_matches m JOIN bank_transactions t ON t.id = m.transaction_id"

const transactionColumns = "t.id, t.account_id, t.provider, t.booking_status, t.source_amount, t.source_currency, t.booking_date, t.value_date, t.transaction_date," +
	" t.raw_description, t.counterparty_name, t.no_longer_reported_at, t.superseded_by"

type rowScanner interface{ Scan(dest ...any) error }

func scanTransactionInto(dest *matches.Transaction) (args []any, finish func() error) {
	var amt sqlvalue.Amount
	var cur sqlvalue.Currency
	var bd, vd, td sql.Null[sqlvalue.Date]
	var gone sql.Null[store.Timestamp]
	var superseded sql.Null[string]
	args = []any{&dest.ID, &dest.AccountID, &dest.Provider, &dest.Status, &amt, &cur, &bd, &vd, &td, &dest.Description, &dest.Counterparty, &gone, &superseded}
	return args, func() error {
		m, err := sqlvalue.Money(amt, cur)
		if err != nil {
			return err
		}
		dest.Amount = m
		dest.BookingDate, dest.ValueDate = bd.V.Date, vd.V.Date
		switch {
		case bd.Valid:
			dest.Date = bd.V.Date
		case vd.Valid:
			dest.Date = vd.V.Date
		default:
			dest.Date = td.V.Date
		}
		dest.NoLongerReportedAt, dest.SupersededBy = gone.V.Time, banking.TransactionID(superseded.V)
		return nil
	}
}

func scanMatch(r rowScanner) (matches.Match, error) {
	var m matches.Match
	var amount sqlvalue.Amount
	var currency sqlvalue.Currency
	var createdUser, decidedKind, decidedUser, reason, createdVia, decidedVia sql.Null[string]
	var decidedAt sql.Null[store.Timestamp]
	var created, updated store.Timestamp
	targs, finish := scanTransactionInto(&m.Transaction)
	args := append([]any{&m.ID, &m.TransactionID, &m.OccurrenceID, &amount, &currency, &m.Origin, &m.State, &m.CreatedBy.Kind, &createdUser,
		&decidedKind, &decidedUser, &decidedAt, &reason, &m.Version, &created, &updated, &createdVia, &decidedVia}, targs...)
	if err := r.Scan(args...); err != nil {
		return matches.Match{}, err
	}
	if err := finish(); err != nil {
		return matches.Match{}, err
	}
	a, err := sqlvalue.Money(amount, currency)
	if err != nil {
		return matches.Match{}, err
	}
	m.Amount, m.CreatedBy.UserID, m.CreatedBy.Via, m.CreatedAt, m.UpdatedAt = a, createdUser.V, audit.Via(createdVia.V), created.Time, updated.Time
	if decidedKind.Valid {
		m.DecidedBy = &matches.Decider{Kind: audit.ActorKind(decidedKind.V), UserID: decidedUser.V, Via: audit.Via(decidedVia.V)}
		at := decidedAt.V.Time
		m.DecidedAt = &at
	}
	if reason.Valid {
		r := reason.V
		m.Reason = &r
	}
	return m, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

const maxIn = 500

func placeholders(n int) string { return "?" + strings.Repeat(", ?", n-1) }

// historyTx attaches each match's history, oldest first.
func historyTx(ctx context.Context, tx *sql.Tx, ms []matches.Match) error {
	index := make(map[string]int, len(ms))
	for i, m := range ms {
		index[m.ID] = i
	}
	for start := 0; start < len(ms); start += maxIn {
		chunk := ms[start:min(start+maxIn, len(ms))]
		args := make([]any, len(chunk))
		for i, m := range chunk {
			args[i] = m.ID
		}
		// rowid breaks ties of the same instant in the order the entries
		// were written.
		rows, err := tx.QueryContext(ctx, "SELECT e.match_id, e.id, e.action, e.state, e.amount, e.currency, e.actor_kind, e.actor_user, e.reason, e.at, e.actor_via"+
			" FROM payment_match_events e WHERE e.match_id IN ("+placeholders(len(chunk))+") ORDER BY e.at, e.rowid", args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var matchID string
			var e matches.Event
			var amount sqlvalue.Amount
			var currency sqlvalue.Currency
			var user, reason, via sql.Null[string]
			var at store.Timestamp
			if err := rows.Scan(&matchID, &e.ID, &e.Action, &e.State, &amount, &currency, &e.By.Kind, &user, &reason, &at, &via); err != nil {
				_ = rows.Close()
				return err
			}
			if e.Amount, err = sqlvalue.Money(amount, currency); err != nil {
				_ = rows.Close()
				return err
			}
			e.By.UserID, e.By.Via, e.At = user.V, audit.Via(via.V), at.Time
			if reason.Valid {
				r := reason.V
				e.Reason = &r
			}
			i := index[matchID]
			ms[i].History = append(ms[i].History, e)
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

func getTx(ctx context.Context, tx *sql.Tx, id string) (matches.Match, error) {
	m, err := scanMatch(tx.QueryRowContext(ctx, selectMatches+" WHERE m.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return matches.Match{}, resource.ErrNotFound
	}
	if err != nil {
		return matches.Match{}, err
	}
	ms := []matches.Match{m}
	if err := historyTx(ctx, tx, ms); err != nil {
		return matches.Match{}, err
	}
	return ms[0], nil
}

// Get returns one match with its transaction and history.
func (s *Store) Get(ctx context.Context, id string) (matches.Match, error) {
	var m matches.Match
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		m, err = getTx(ctx, tx, id)
		return err
	})
	return m, err
}

func inList[T ~string](col string, vals []T, where *[]string, args *[]any) {
	if len(vals) == 0 {
		return
	}
	*where = append(*where, col+" IN ("+placeholders(len(vals))+")")
	for _, v := range vals {
		*args = append(*args, string(v))
	}
}

// List returns matches by ID with their transactions and histories.
func (s *Store) List(ctx context.Context, f matches.ListFilter) ([]matches.Match, error) {
	var where []string
	var args []any
	inList("m.transaction_id", f.TransactionIDs, &where, &args)
	inList("m.occurrence_id", f.OccurrenceIDs, &where, &args)
	inList("m.state", f.States, &where, &args)
	inList("m.origin", f.Origins, &where, &args)
	if f.After != "" {
		where = append(where, "m.id > ?")
		args = append(args, f.After)
	}
	q := selectMatches
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY m.id"
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", f.Limit)
	}
	var out []matches.Match
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, q, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			m, err := scanMatch(rows)
			if err != nil {
				_ = rows.Close()
				return err
			}
			out = append(out, m)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return historyTx(ctx, tx, out)
	})
	return out, err
}

func transactionTx(ctx context.Context, tx *sql.Tx, id banking.TransactionID) (matches.Transaction, error) {
	var t matches.Transaction
	args, finish := scanTransactionInto(&t)
	err := tx.QueryRowContext(ctx, "SELECT "+transactionColumns+" FROM bank_transactions t WHERE t.id = ?", string(id)).Scan(args...)
	if errors.Is(err, sql.ErrNoRows) {
		return matches.Transaction{}, matches.ErrTransactionNotFound
	}
	if err != nil {
		return matches.Transaction{}, err
	}
	return t, finish()
}

// confirmedAllocations lists the amounts confirmed from a transaction,
// leaving out the match except (when correcting it).
func confirmedAllocations(ctx context.Context, tx *sql.Tx, transactionID banking.TransactionID, except string) ([]money.Money, error) {
	rows, err := tx.QueryContext(ctx, "SELECT amount, currency FROM payment_matches WHERE transaction_id = ? AND state = 'confirmed' AND id <> ?", string(transactionID), except)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []money.Money
	for rows.Next() {
		var a sqlvalue.Amount
		var c sqlvalue.Currency
		if err := rows.Scan(&a, &c); err != nil {
			return nil, err
		}
		m, err := sqlvalue.Money(a, c)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// checkConfirm runs matches.CheckConfirm against the stored data, leaving the
// match except out of the allocations and the occurrence's evidence.
func (s *Store) checkConfirm(ctx context.Context, tx *sql.Tx, t matches.Transaction, o occurrences.Occurrence, amount money.Money, except string, allow bool) error {
	others, err := confirmedAllocations(ctx, tx, t.ID, except)
	if err != nil {
		return err
	}
	facts, err := paymentstore.FactsTx(ctx, tx, s.zone, []string{o.ID})
	if err != nil {
		return err
	}
	f := facts[o.ID]
	kept := f.Evidence[:0:0]
	for _, e := range f.Evidence {
		if e.Kind != payments.EvidenceBankTransaction || e.ID != except {
			kept = append(kept, e)
		}
	}
	// Every other confirmed match of the occurrence counts toward the
	// overpayment check, countable or not: a transaction the provider
	// reports again would count again, and must not double-count.
	rows, err := tx.QueryContext(ctx, "SELECT id, amount, currency FROM payment_matches WHERE occurrence_id = ? AND state = 'confirmed' AND id <> ?", o.ID, except)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		var a sqlvalue.Amount
		var c sqlvalue.Currency
		if err := rows.Scan(&id, &a, &c); err != nil {
			return err
		}
		if slices.ContainsFunc(kept, func(e payments.Evidence) bool { return e.Kind == payments.EvidenceBankTransaction && e.ID == id }) {
			continue
		}
		m, err := sqlvalue.Money(a, c)
		if err != nil {
			return err
		}
		kept = append(kept, payments.Evidence{Kind: payments.EvidenceBankTransaction, ID: id, OccurrenceID: o.ID, Amount: m})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	f.Evidence = kept
	return matches.CheckConfirm(t, others, amount, o, f, allow)
}

// entry is a history entry to record: the change, the state and amount after
// it and the reason given.
type entry struct {
	action matches.Action
	state  matches.State
	amount money.Money
	reason *string
}

// event appends a history entry with how its actor came (migration 0023) and
// the occurrence's version after the change.
func (s *Store) event(ctx context.Context, tx *sql.Tx, matchID string, e entry, actor audit.Actor, occurrenceVersion int64, at time.Time) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO payment_match_events (id, match_id, action, state, amount, currency, actor_kind, actor_user, reason, at, actor_via, occurrence_version)"+
		" VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		s.newID(), matchID, string(e.action), string(e.state), sqlvalue.Amount{Decimal: e.amount.Amount()}, sqlvalue.Currency{Currency: e.amount.Currency()},
		string(storedKind(actor.Kind)), nullable(actor.UserID), e.reason, store.Timestamp{Time: at}, string(storedVia(actor)), occurrenceVersion)
	if err != nil {
		return fmt.Errorf("matches: history: %w", err)
	}
	return nil
}

// storedKind keeps the actor kinds the tables accept; anything else is
// recorded as the system.
func storedKind(k audit.ActorKind) audit.ActorKind {
	switch k {
	case audit.ActorUser, audit.ActorToken:
		return k
	}
	return audit.ActorSystem
}

// storedVia is how the actor came, kept consistent with storedKind: an actor
// recorded as the system came as the system.
func storedVia(actor audit.Actor) audit.Via {
	if storedKind(actor.Kind) == audit.ActorSystem {
		return audit.ViaSystem
	}
	return actor.Channel()
}

// finish bumps the occurrence's version, appends the history entry e (with
// that new version), records the audit event and reads the result.
func (s *Store) finish(ctx context.Context, tx *sql.Tx, e entry, actor audit.Actor, id, occurrenceID string, from matches.State, at time.Time) (matches.Change, error) {
	o, err := occstore.GetTx(ctx, tx, occurrenceID)
	if err != nil {
		return matches.Change{}, err
	}
	v, err := store.UpdateVersioned(ctx, tx, "commitment_occurrences", "updated_at = ?", o.ID, o.Version, store.Timestamp{Time: at})
	if err != nil {
		return matches.Change{}, err
	}
	if err := s.event(ctx, tx, id, e, actor, v, at); err != nil {
		return matches.Change{}, err
	}
	m, err := getTx(ctx, tx, id)
	if err != nil {
		return matches.Change{}, err
	}
	ev := matches.AuditEvent(e.action, actor, m, from, at)
	ev.Details["via"] = string(storedVia(actor)) // the same provenance the history entry stores
	if err := s.audit.WriteAudit(ctx, tx, ev); err != nil {
		return matches.Change{}, err
	}
	ch, err := paymentstore.ChangeTx(ctx, tx, s.zone, o.ID)
	if err != nil {
		return matches.Change{}, err
	}
	return matches.Change{Match: m, Result: ch}, nil
}

// Create stores a new match after checking it against the transaction, the
// occurrence and, for a confirmation, the allocations and evidence already
// recorded.
func (s *Store) Create(ctx context.Context, actor audit.Actor, n matches.New) (matches.Change, error) {
	if err := commitments.Validation(n.Validate()); err != nil {
		return matches.Change{}, err
	}
	var ch matches.Change
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		t, err := transactionTx(ctx, tx, n.TransactionID)
		if err != nil {
			return err
		}
		o, err := occstore.GetTx(ctx, tx, n.OccurrenceID)
		if errors.Is(err, resource.ErrNotFound) {
			return matches.ErrOccurrenceNotFound
		}
		if err != nil {
			return err
		}
		if o.CancelledAt != nil {
			return occurrences.ErrCancelled
		}
		if err := matches.CheckTarget(t, o, n.Amount); err != nil {
			return err
		}
		var open int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM payment_matches WHERE transaction_id = ? AND occurrence_id = ? AND state IN ('suggested', 'confirmed')",
			string(t.ID), o.ID).Scan(&open); err != nil {
			return err
		}
		if open > 0 {
			return matches.ErrDuplicate
		}
		automated := automation(actor)
		if automated && n.Origin == matches.OriginUser {
			return matches.ErrActorOrigin
		}
		if automated && n.AllowOverpayment {
			return matches.ErrActorOverpayment
		}
		action := matches.ActionSuggested
		// "|| automated": an automation actor is held to the automatic rules
		// whatever origin it claims, as in Confirm and Correct.
		if n.State == matches.StateConfirmed && (n.Origin == matches.OriginAutoRule || automated) {
			var others int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM payment_matches WHERE transaction_id = ? AND state IN ('suggested', 'confirmed')", string(t.ID)).Scan(&others); err != nil {
				return err
			}
			tol, err := commitmentTolerance(ctx, tx, o.ID)
			if err != nil {
				return err
			}
			if err := matches.CheckAuto(t, others, n.Amount, o, tol); err != nil {
				return err
			}
		}
		if n.State == matches.StateConfirmed {
			if err := s.checkConfirm(ctx, tx, t, o, n.Amount, "", n.AllowOverpayment); err != nil {
				return err
			}
			action = matches.ActionConfirmed
		}
		now := s.now().UTC()
		id := s.newID()
		var decidedKind, decidedUser, decidedVia, decidedAt, tolAmt, tolCur any
		if n.State == matches.StateConfirmed {
			tol, err := s.withinTolerance(ctx, tx, o, n.Amount, "")
			if err != nil {
				return err
			}
			if tol != nil {
				tolAmt, tolCur = sqlvalue.Amount{Decimal: tol.Amount()}, sqlvalue.Currency{Currency: tol.Currency()}
			}
			decidedKind, decidedUser, decidedVia, decidedAt = string(storedKind(actor.Kind)), nullable(actor.UserID), string(storedVia(actor)), store.Timestamp{Time: now}
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO payment_matches (id, transaction_id, occurrence_id, amount, currency, origin, state, created_by_kind, created_by_user,"+
			" decided_by_kind, decided_by_user, decided_at, reason, created_at, updated_at, tolerance_amount, tolerance_currency, created_by_via, decided_by_via)"+
			" VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			id, string(t.ID), o.ID, sqlvalue.Amount{Decimal: n.Amount.Amount()}, sqlvalue.Currency{Currency: n.Amount.Currency()}, string(n.Origin), string(n.State),
			string(storedKind(actor.Kind)), nullable(actor.UserID), decidedKind, decidedUser, decidedAt, n.Reason, store.Timestamp{Time: now}, store.Timestamp{Time: now}, tolAmt, tolCur,
			string(storedVia(actor)), decidedVia); err != nil {
			if isUniqueViolation(err) {
				return matches.ErrDuplicate
			}
			return fmt.Errorf("matches: create: %w", err)
		}
		ch, err = s.finish(ctx, tx, entry{action, n.State, n.Amount, n.Reason}, actor, id, o.ID, "", now)
		return err
	})
	return ch, err
}

// automation says whether the actor is neither a user nor a token: such a
// change is held to the automatic-matching rules (decision D8) whatever origin
// it claims.
func automation(actor audit.Actor) bool {
	return actor.Kind != audit.ActorUser && actor.Kind != audit.ActorToken
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// at reads a match and checks its version.
func at(ctx context.Context, tx *sql.Tx, id string, expected int64) (matches.Match, error) {
	m, err := getTx(ctx, tx, id)
	if err != nil {
		return matches.Match{}, err
	}
	if m.Version != expected {
		return matches.Match{}, &resource.VersionConflictError{Expected: expected, Current: m.Version}
	}
	return m, nil
}

// decide moves a match from one of the states in from to the state to.
func (s *Store) decide(ctx context.Context, actor audit.Actor, id string, expected int64, reason *string, a matches.Action, to matches.State,
	check func(ctx context.Context, tx *sql.Tx, m matches.Match) error, from ...matches.State) (matches.Change, error) {
	if err := commitments.Validation(matches.ValidateReason(reason)); err != nil {
		return matches.Change{}, err
	}
	var ch matches.Change
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		m, err := at(ctx, tx, id, expected)
		if err != nil {
			return err
		}
		allowed := false
		for _, f := range from {
			allowed = allowed || m.State == f
		}
		if !allowed {
			return matches.ErrTransition
		}
		if automation(actor) {
			if err := refuseAutomation(ctx, tx, a, m); err != nil {
				return err
			}
		}
		if check != nil {
			if err := check(ctx, tx, m); err != nil {
				return err
			}
		}
		now := s.now().UTC()
		if _, err := store.UpdateVersioned(ctx, tx, "payment_matches", "state = ?, decided_by_kind = ?, decided_by_user = ?, decided_by_via = ?, decided_at = ?, reason = ?, updated_at = ?",
			m.ID, expected, string(to), string(storedKind(actor.Kind)), nullable(actor.UserID), string(storedVia(actor)), store.Timestamp{Time: now}, reason, store.Timestamp{Time: now}); err != nil {
			return err
		}
		ch, err = s.finish(ctx, tx, entry{a, to, m.Amount, reason}, actor, m.ID, m.OccurrenceID, m.State, now)
		return err
	})
	return ch, err
}

// refuseAutomation applies decision D33 to a decision by automation. Unmatch
// and reverse are refused outright, its own confirmations included: a
// provider reversal signal is a warning for the user, not an automatic
// undo. Reject is allowed only for a suggestion with no person in its history.
func refuseAutomation(ctx context.Context, tx *sql.Tx, a matches.Action, m matches.Match) error {
	switch a {
	case matches.ActionUnmatched, matches.ActionReversed:
		return matches.ErrActorUndo
	case matches.ActionRejected:
		return refuseUndo(ctx, tx, m)
	}
	return nil
}

// refuseUndo refuses a reject of a suggestion a person touched. A person
// did when any history event (suggestion, confirmation, correction) came from
// a user or token; a correction does not change the latest decision, so the
// history is the rule. The DecidedBy check below is only a fast path to the
// same answer.
func refuseUndo(ctx context.Context, tx *sql.Tx, m matches.Match) error {
	if m.DecidedBy != nil && (m.DecidedBy.Kind == audit.ActorUser || m.DecidedBy.Kind == audit.ActorToken) {
		return matches.ErrActorUndo
	}
	var n int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM payment_match_events WHERE match_id = ? AND actor_kind IN ('user', 'token')", m.ID).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return matches.ErrActorUndo
	}
	return nil
}

// Confirm confirms a suggestion: the transaction must be countable, the
// allocation must fit the transaction and, unless allowed, the occurrence's
// amount.
func (s *Store) Confirm(ctx context.Context, actor audit.Actor, id string, expected int64, allow bool, reason *string) (matches.Change, error) {
	return s.confirm(ctx, actor, id, expected, allow, reason)
}

// ConfirmAuto confirms one of the matching engine's suggestions as the
// system (task X2, ADR-0039). The automatic rules apply as for any automation
// actor; the tolerance is the commitment's match tolerance as the write
// transaction reads it (none means an exact amount), never a caller's value; a
// model suggestion is never confirmed this way.
func (s *Store) ConfirmAuto(ctx context.Context, id string, expected int64, reason *string) (matches.Change, error) {
	return s.confirm(ctx, audit.System, id, expected, false, reason)
}

// commitmentTolerance reads the match tolerance of the commitment an
// occurrence belongs to; the zero value means none.
func commitmentTolerance(ctx context.Context, tx *sql.Tx, occurrenceID string) (money.Money, error) {
	var amt sql.Null[sqlvalue.Amount]
	var cur sql.Null[sqlvalue.Currency]
	err := tx.QueryRowContext(ctx, "SELECT c.match_tolerance_amount, c.match_tolerance_currency FROM commitment_occurrences o JOIN commitments c ON c.id = o.commitment_id WHERE o.id = ?",
		occurrenceID).Scan(&amt, &cur)
	if err != nil {
		return money.Money{}, err
	}
	if !amt.Valid {
		return money.Money{}, nil
	}
	return sqlvalue.Money(amt.V, cur.V)
}

// withinTolerance returns the commitment's match tolerance when a confirmed
// match of amount to o is confirmed within it: the occurrence's counted
// evidence, with this match, is confirmed bank matches alone (no manual
// payment), in the occurrence's currency, short of its amount by no more than
// the tolerance (D39 5a, ADR-0042). Nil otherwise. except is the match being
// confirmed or corrected, left out of the existing evidence.
func (s *Store) withinTolerance(ctx context.Context, tx *sql.Tx, o occurrences.Occurrence, amount money.Money, except string) (*money.Money, error) {
	tol, err := commitmentTolerance(ctx, tx, o.ID)
	if err != nil {
		return nil, err
	}
	cur := o.Amount.Currency()
	if !tol.IsValid() || tol.Currency() != cur || tol.Sign() <= 0 || amount.Currency() != cur {
		return nil, nil
	}
	facts, err := paymentstore.FactsTx(ctx, tx, s.zone, []string{o.ID})
	if err != nil {
		return nil, err
	}
	total := amount
	for _, e := range facts[o.ID].Evidence {
		switch {
		case e.Kind == payments.EvidenceBankTransaction && e.ID == except:
		case e.Kind != payments.EvidenceBankTransaction:
			return nil, nil // manual evidence: the user's own payment is never rounded up
		case e.Amount.Currency() == cur:
			if total, err = total.Add(e.Amount); err != nil {
				return nil, err
			}
		}
	}
	if c, _ := total.Cmp(o.Amount); c >= 0 {
		return nil, nil
	}
	gap, _ := o.Amount.Sub(total)
	if c, _ := gap.Cmp(tol); c > 0 {
		return nil, nil
	}
	return &tol, nil
}

// recordTolerance stores on the match the tolerance it is confirmed within,
// or clears it (migration 0021).
func (s *Store) recordTolerance(ctx context.Context, tx *sql.Tx, id string, o occurrences.Occurrence, amount money.Money) error {
	tol, err := s.withinTolerance(ctx, tx, o, amount, id)
	if err != nil {
		return err
	}
	var amt, cur any
	if tol != nil {
		amt, cur = sqlvalue.Amount{Decimal: tol.Amount()}, sqlvalue.Currency{Currency: tol.Currency()}
	}
	_, err = tx.ExecContext(ctx, "UPDATE payment_matches SET tolerance_amount = ?, tolerance_currency = ? WHERE id = ?", amt, cur, id)
	return err
}

func (s *Store) confirm(ctx context.Context, actor audit.Actor, id string, expected int64, allow bool, reason *string) (matches.Change, error) {
	return s.decide(ctx, actor, id, expected, reason, matches.ActionConfirmed, matches.StateConfirmed, func(ctx context.Context, tx *sql.Tx, m matches.Match) error {
		o, err := occstore.GetTx(ctx, tx, m.OccurrenceID)
		if err != nil {
			return err
		}
		if err := matches.CheckTarget(m.Transaction, o, m.Amount); err != nil {
			return err
		}
		if automation(actor) {
			if allow {
				return matches.ErrActorOverpayment
			}
			if m.Origin == matches.OriginModel {
				return matches.ErrAutoModel
			}
			var others int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM payment_matches WHERE transaction_id = ? AND id <> ? AND state IN ('suggested', 'confirmed')",
				string(m.Transaction.ID), m.ID).Scan(&others); err != nil {
				return err
			}
			tolerance, err := commitmentTolerance(ctx, tx, o.ID)
			if err != nil {
				return err
			}
			if err := matches.CheckAuto(m.Transaction, others, m.Amount, o, tolerance); err != nil {
				return err
			}
		}
		if err := s.checkConfirm(ctx, tx, m.Transaction, o, m.Amount, m.ID, allow); err != nil {
			return err
		}
		// Record, inside this transaction, whether it is confirmed within
		// the tolerance, so a later tolerance edit never changes it.
		return s.recordTolerance(ctx, tx, m.ID, o, m.Amount)
	}, matches.StateSuggested)
}

// Reject rejects a suggestion.
func (s *Store) Reject(ctx context.Context, actor audit.Actor, id string, expected int64, reason *string) (matches.Change, error) {
	return s.decide(ctx, actor, id, expected, reason, matches.ActionRejected, matches.StateRejected, nil, matches.StateSuggested)
}

// Unmatch undoes a confirmation: the transaction does not pay the
// occurrence. The occurrence's status is derived again without it.
func (s *Store) Unmatch(ctx context.Context, actor audit.Actor, id string, expected int64, reason *string) (matches.Change, error) {
	return s.decide(ctx, actor, id, expected, reason, matches.ActionUnmatched, matches.StateRejected, nil, matches.StateConfirmed)
}

// Reverse marks a confirmed match reversed: the payment was reversed, and the
// occurrence reopens.
func (s *Store) Reverse(ctx context.Context, actor audit.Actor, id string, expected int64, reason *string) (matches.Change, error) {
	return s.decide(ctx, actor, id, expected, reason, matches.ActionReversed, matches.StateReversed, nil, matches.StateConfirmed)
}

// Unconfirm takes back a confirmation (decision D42 1a, ADR-0045): the match
// is a suggestion again, exactly as it was before it was confirmed. Its
// decision and the tolerance snapshot of the confirmation (D41 3a) are
// cleared and its reason is the suggestion's again; the reason given here is
// kept in the history entry only. The occurrence's payment state is derived
// again without it.
//
// Only the person who confirmed it may (a token acts as its owner, D42 3a);
// automation never may (D33). And only inside the undo window, which the
// store checks inside the write transaction, all three at once:
//
//  1. the confirmation is the match's latest change (no correction since);
//  2. it is the person's latest match decision (no match event by them on any
//     match since, compared by time and then insertion order, which a reused
//     rowid cannot defeat): the undo notice is replaced by their next change (D37);
//  3. nothing else changed the occurrence since: its version is still the one
//     the confirmation left (another match, a manual payment or its deletion,
//     a status override, a terms change, a cancellation).
//
// A match created confirmed was never a suggestion and is refused
// (ErrNotUnconfirmable): unmatch undoes it.
func (s *Store) Unconfirm(ctx context.Context, actor audit.Actor, id string, expected int64, reason *string) (matches.Change, error) {
	if err := commitments.Validation(matches.ValidateReason(reason)); err != nil {
		return matches.Change{}, err
	}
	if automation(actor) {
		return matches.Change{}, matches.ErrActorUndo
	}
	var ch matches.Change
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		m, err := at(ctx, tx, id, expected)
		if err != nil {
			return err
		}
		if m.State != matches.StateConfirmed {
			return matches.ErrTransition
		}
		// The latest entry, in the order entries were written.
		var lastRow int64
		var lastAt string
		var lastAction string
		var lastOccurrenceVersion sql.Null[int64]
		if err := tx.QueryRowContext(ctx, "SELECT rowid, at, action, occurrence_version FROM payment_match_events WHERE match_id = ? ORDER BY rowid DESC LIMIT 1", m.ID).
			Scan(&lastRow, &lastAt, &lastAction, &lastOccurrenceVersion); err != nil {
			return err
		}
		// The entry before it: the suggestion as it was (nil when the match
		// was created confirmed).
		var before *entryState
		var beforeState string
		switch err := tx.QueryRowContext(ctx, "SELECT state FROM payment_match_events WHERE match_id = ? AND rowid < ? ORDER BY rowid DESC LIMIT 1", m.ID, lastRow).
			Scan(&beforeState); {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return err
		default:
			before = &entryState{state: matches.State(beforeState)}
			// The suggestion's reason is the newest one not written by a
			// decision: confirmations and earlier undos carry the person's
			// text, not the engine's.
			var beforeReason sql.Null[string]
			if err := tx.QueryRowContext(ctx, "SELECT reason FROM payment_match_events WHERE match_id = ? AND rowid < ? AND action NOT IN (?, ?) ORDER BY rowid DESC LIMIT 1",
				m.ID, lastRow, string(matches.ActionConfirmed), string(matches.ActionUnconfirmed)).Scan(&beforeReason); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if beforeReason.Valid {
				r := beforeReason.V
				before.reason = &r
			}
		}
		if m.Origin == matches.OriginUser || (matches.Action(lastAction) == matches.ActionConfirmed && (before == nil || before.state != matches.StateSuggested)) {
			return matches.ErrNotUnconfirmable
		}
		// The decider: the same person, whether they came by session, token
		// or MCP; never a confirmation automation made.
		d := m.DecidedBy
		if d == nil || (d.Kind != audit.ActorUser && d.Kind != audit.ActorToken) || d.UserID == "" || d.UserID != actor.UserID {
			return matches.ErrUndoNotDecider
		}
		// 1. The confirmation is the match's latest change.
		if matches.Action(lastAction) != matches.ActionConfirmed {
			return matches.ErrUndoWindow
		}
		// 2. It is the person's latest match decision.
		var later int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM payment_match_events WHERE actor_user = ? AND (at > ? OR (at = ? AND rowid > ?))", actor.UserID, lastAt, lastAt, lastRow).Scan(&later); err != nil {
			return err
		}
		if later > 0 {
			return matches.ErrUndoWindow
		}
		// 3. Nothing else changed the occurrence since. A confirmation
		// recorded before migration 0023 has no version: its window is closed.
		o, err := occstore.GetTx(ctx, tx, m.OccurrenceID)
		if err != nil {
			return err
		}
		if !lastOccurrenceVersion.Valid || lastOccurrenceVersion.V != o.Version {
			return matches.ErrUndoWindow
		}
		now := s.now().UTC()
		if _, err := store.UpdateVersioned(ctx, tx, "payment_matches", "state = ?, decided_by_kind = NULL, decided_by_user = NULL, decided_by_via = NULL, decided_at = NULL,"+
			" reason = ?, tolerance_amount = NULL, tolerance_currency = NULL, updated_at = ?",
			m.ID, expected, string(matches.StateSuggested), before.reason, store.Timestamp{Time: now}); err != nil {
			return err
		}
		ch, err = s.finish(ctx, tx, entry{matches.ActionUnconfirmed, matches.StateSuggested, m.Amount, reason}, actor, m.ID, o.ID, m.State, now)
		return err
	})
	return ch, err
}

// entryState is the state and reason a history entry left.
type entryState struct {
	state  matches.State
	reason *string
}

// Correct changes the allocated amount of a suggested or confirmed match. A
// confirmed match is checked again with its new amount.
func (s *Store) Correct(ctx context.Context, actor audit.Actor, id string, expected int64, amount money.Money, allow bool, reason *string) (matches.Change, error) {
	if err := commitments.Validation(append(matches.ValidateAmount(amount), matches.ValidateReason(reason)...)); err != nil {
		return matches.Change{}, err
	}
	if automation(actor) {
		return matches.Change{}, matches.ErrActorCorrect
	}
	var ch matches.Change
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		m, err := at(ctx, tx, id, expected)
		if err != nil {
			return err
		}
		if !m.State.Open() {
			return matches.ErrTransition
		}
		o, err := occstore.GetTx(ctx, tx, m.OccurrenceID)
		if err != nil {
			return err
		}
		if err := matches.CheckTarget(m.Transaction, o, amount); err != nil {
			return err
		}
		if m.State == matches.StateConfirmed {
			if err := s.checkConfirm(ctx, tx, m.Transaction, o, amount, m.ID, allow); err != nil {
				return err
			}
		}
		now := s.now().UTC()
		if _, err := store.UpdateVersioned(ctx, tx, "payment_matches", "amount = ?, reason = ?, updated_at = ?",
			m.ID, expected, sqlvalue.Amount{Decimal: amount.Amount()}, reason, store.Timestamp{Time: now}); err != nil {
			return err
		}
		if m.State == matches.StateConfirmed {
			if err := s.recordTolerance(ctx, tx, m.ID, o, amount); err != nil {
				return err
			}
		}
		ch, err = s.finish(ctx, tx, entry{matches.ActionCorrected, m.State, amount, reason}, actor, m.ID, o.ID, m.State, now)
		return err
	})
	return ch, err
}
