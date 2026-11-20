// Package bankfacts persists bank facts (ADR-0013): connections, accounts,
// balance snapshots and transactions, in the tables of migration
// 0004_bank_facts. It implements [banking.FactStore].
//
// Bank facts are provider-owned. Nothing here writes user data, and the
// only path that deletes bank facts is [Store.PurgeConnection].
//
// Sensitive values (account identifiers, provider handles, session
// references) are written with Reveal and read back into
// [banking.Sensitive]; errors from this package name ids and fields, never
// those values, descriptions or counterparties.
package bankfacts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/store"
	"github.com/aurimasniekis/go-ledger/internal/store/sqlvalue"
	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Store implements [banking.FactStore] on a [store.DB].
type Store struct {
	db    *store.DB
	newID func() string
}

var _ banking.FactStore = (*Store)(nil)

// New returns a Store. newID returns ULIDs; nil means resource.NewID.
func New(db *store.DB, newID func() string) *Store {
	if newID == nil {
		newID = resource.NewID
	}
	return &Store{db: db, newID: newID}
}

func ts(t time.Time) store.Timestamp { return store.Timestamp{Time: t} }

func nullDate(d calendar.Date) any {
	if d.IsZero() {
		return nil
	}
	return sqlvalue.Date{Date: d}
}

func nullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func requireTime(what string, t time.Time) error {
	if t.IsZero() {
		return fmt.Errorf("bankfacts: %s time is zero", what)
	}
	return nil
}

func notFound(what string, id any) error {
	return fmt.Errorf("bankfacts: %s %v: %w", what, id, banking.ErrNotFound)
}

// ---- connections ----

const connectionColumns = "id, provider, application, mode, environment, account_access, institution_id, institution_name, institution_country, status, session_reference, consent_expires_at, consent_granted_at, last_error_kind, last_error_code, created_at, updated_at, version"

type rowScanner interface{ Scan(dest ...any) error }

func scanConnection(r rowScanner) (banking.Connection, error) {
	var c banking.Connection
	var session sql.Null[string]
	var expires, granted sql.Null[store.Timestamp]
	var created, updated store.Timestamp
	err := r.Scan(&c.ID, &c.Provider, &c.Application, &c.Mode, &c.Environment, &c.AccountAccess,
		&c.Institution.ID, &c.Institution.Name, &c.Institution.Country, &c.Status, &session,
		&expires, &granted, &c.LastErrorKind, &c.LastErrorCode, &created, &updated, &c.Version)
	if err != nil {
		return banking.Connection{}, err
	}
	c.Session = banking.NewSensitive(session.V)
	c.ConsentValidUntil = expires.V.Time
	c.ConsentGrantedAt = granted.V.Time
	c.CreatedAt, c.UpdatedAt = created.Time, updated.Time
	return c, nil
}

func connectionTx(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, where string, arg any) (banking.Connection, error) {
	c, err := scanConnection(q.QueryRowContext(ctx, "SELECT "+connectionColumns+" FROM bank_connections WHERE "+where, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return banking.Connection{}, fmt.Errorf("bankfacts: connection: %w", banking.ErrNotFound)
	}
	if err != nil {
		return banking.Connection{}, fmt.Errorf("bankfacts: read connection: %w", err)
	}
	return c, nil
}

func validProviderName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

// CreateConnection implements [banking.FactStore].
func (s *Store) CreateConnection(ctx context.Context, nc banking.NewConnection, at time.Time) (banking.Connection, error) {
	caps := nc.Capabilities
	switch {
	case !validProviderName(caps.Provider):
		return banking.Connection{}, errors.New("bankfacts: provider name must be lower-case letters, digits and underscores")
	case strings.TrimSpace(caps.Application) == "":
		return banking.Connection{}, errors.New("bankfacts: connection without a provider application")
	case !caps.Mode.Valid():
		return banking.Connection{}, fmt.Errorf("bankfacts: unknown mode %q", caps.Mode)
	case !caps.Environment.Valid():
		return banking.Connection{}, fmt.Errorf("bankfacts: unknown environment %q", caps.Environment)
	case strings.TrimSpace(nc.Institution.ID) == "":
		return banking.Connection{}, errors.New("bankfacts: connection without an institution")
	case nc.ConsentState == "":
		return banking.Connection{}, errors.New("bankfacts: connection without a consent state")
	}
	if err := requireTime("creation", at); err != nil {
		return banking.Connection{}, err
	}
	id := banking.ConnectionID(s.newID())
	var out banking.Connection
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO bank_connections (id, provider, application, mode, environment, account_access, institution_id, institution_name, institution_country, status, consent_state, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			id, caps.Provider, caps.Application, caps.Mode, caps.Environment, caps.Mode.AccountAccess(),
			nc.Institution.ID, nc.Institution.Name, nc.Institution.Country, banking.ConnectionPending, nc.ConsentState, ts(at), ts(at))
		if err != nil {
			return fmt.Errorf("bankfacts: create connection: %w", err)
		}
		out, err = connectionTx(ctx, tx, "id = ?", id)
		return err
	})
	return out, err
}

// Connection implements [banking.FactStore].
func (s *Store) Connection(ctx context.Context, id banking.ConnectionID) (banking.Connection, error) {
	var out banking.Connection
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		c, err := connectionTx(ctx, tx, "id = ?", id)
		out = c
		return err
	})
	return out, err
}

// ConnectionByConsentState implements [banking.FactStore]. Only a pending
// connection has a consent state.
func (s *Store) ConnectionByConsentState(ctx context.Context, state string) (banking.Connection, error) {
	if state == "" {
		return banking.Connection{}, fmt.Errorf("bankfacts: connection: %w", banking.ErrNotFound)
	}
	var out banking.Connection
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		c, err := connectionTx(ctx, tx, "consent_state = ?", state)
		out = c
		return err
	})
	return out, err
}

// Connections implements [banking.FactStore], oldest first.
func (s *Store) Connections(ctx context.Context) ([]banking.Connection, error) {
	var out []banking.Connection
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT "+connectionColumns+" FROM bank_connections ORDER BY created_at, rowid")
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			c, err := scanConnection(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("bankfacts: list connections: %w", err)
	}
	return out, nil
}

// updateConnection is for provider-driven transitions: it bumps version
// without comparing it (ADR-0013 §5). User-initiated changes use
// updateConnectionVersioned.
func (s *Store) updateConnection(ctx context.Context, id banking.ConnectionID, set string, args ...any) (banking.Connection, error) {
	var out banking.Connection
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE bank_connections SET "+set+", version = version + 1 WHERE id = ?", append(args, id)...)
		if err != nil {
			return fmt.Errorf("bankfacts: update connection %s: %w", id, err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return fmt.Errorf("bankfacts: connection: %w", banking.ErrNotFound)
		}
		out, err = connectionTx(ctx, tx, "id = ?", id)
		return err
	})
	return out, err
}

func (s *Store) updateConnectionVersioned(ctx context.Context, id banking.ConnectionID, expected int64, set string, args ...any) (banking.Connection, error) {
	var out banking.Connection
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := store.UpdateVersioned(ctx, tx, "bank_connections", set, string(id), expected, args...); err != nil {
			return versionedError(id, err)
		}
		var err error
		out, err = connectionTx(ctx, tx, "id = ?", id)
		return err
	})
	return out, err
}

// versionedError maps a missing row to banking.ErrNotFound and keeps
// version conflicts as they are.
func versionedError(id banking.ConnectionID, err error) error {
	if errors.Is(err, resource.ErrNotFound) {
		return notFound("connection", id)
	}
	return fmt.Errorf("bankfacts: connection %s: %w", id, err)
}

// DisconnectConnection implements [banking.FactStore].
func (s *Store) DisconnectConnection(ctx context.Context, id banking.ConnectionID, expected int64, at time.Time) (banking.Connection, error) {
	if err := requireTime("disconnect", at); err != nil {
		return banking.Connection{}, err
	}
	return s.updateConnectionVersioned(ctx, id, expected,
		"status = 'disconnected', consent_state = NULL, last_error_kind = '', last_error_code = '', updated_at = ?", ts(at))
}

// ActivateConnection implements [banking.FactStore].
func (s *Store) ActivateConnection(ctx context.Context, id banking.ConnectionID, sess banking.Session, at time.Time) (banking.Connection, error) {
	if sess.Reference.IsZero() {
		return banking.Connection{}, errors.New("bankfacts: session without a reference")
	}
	if err := requireTime("consent validity", sess.ValidUntil); err != nil {
		return banking.Connection{}, err
	}
	if err := requireTime("activation", at); err != nil {
		return banking.Connection{}, err
	}
	return s.updateConnection(ctx, id,
		"status = 'active', session_reference = ?, consent_expires_at = ?, consent_granted_at = ?, consent_state = NULL, last_error_kind = '', last_error_code = '', updated_at = ?",
		sess.Reference.Reveal(), ts(sess.ValidUntil), ts(at), ts(at))
}

// BeginReconnect implements [banking.FactStore].
func (s *Store) BeginReconnect(ctx context.Context, id banking.ConnectionID, consentState string, at time.Time) (banking.Connection, error) {
	if consentState == "" {
		return banking.Connection{}, errors.New("bankfacts: reconnect without a consent state")
	}
	if err := requireTime("reconnect", at); err != nil {
		return banking.Connection{}, err
	}
	return s.updateConnection(ctx, id, "consent_state = ?, updated_at = ?", consentState, ts(at))
}

// SetConnectionStatus implements [banking.FactStore]. It cannot set active
// (use ActivateConnection, which records the session).
func (s *Store) SetConnectionStatus(ctx context.Context, id banking.ConnectionID, status banking.ConnectionStatus, cause error, at time.Time) (banking.Connection, error) {
	if !status.Valid() || status == banking.ConnectionActive || status == banking.ConnectionPending {
		return banking.Connection{}, fmt.Errorf("bankfacts: SetConnectionStatus cannot set %q", status)
	}
	if err := requireTime("status change", at); err != nil {
		return banking.Connection{}, err
	}
	kind, code := banking.Classify(cause), ""
	if e, ok := errors.AsType[*banking.Error](cause); ok {
		code = e.Code
	}
	return s.updateConnection(ctx, id,
		"status = ?, consent_state = NULL, last_error_kind = ?, last_error_code = ?, updated_at = ?",
		status, kind, code, ts(at))
}

// ---- accounts ----

const accountColumns = "id, connection_id, provider, external_id, provider_handle, iban, bban, masked_pan, other_identifier, name, product, currency, first_seen_at, last_seen_at, no_longer_reported_at"

func scanAccount(r rowScanner) (banking.StoredAccount, error) {
	var a banking.StoredAccount
	var handle, iban, bban, pan, other sql.Null[string]
	var cur sqlvalue.Currency
	var first, last store.Timestamp
	var gone sql.Null[store.Timestamp]
	err := r.Scan(&a.ID, &a.ConnectionID, &a.Provider, &a.ExternalID, &handle, &iban, &bban, &pan, &other,
		&a.Name, &a.Product, &cur, &first, &last, &gone)
	if err != nil {
		return banking.StoredAccount{}, err
	}
	a.Handle = banking.NewSensitive(handle.V)
	a.Identifiers = banking.AccountIdentifiers{
		IBAN: banking.NewSensitive(iban.V), BBAN: banking.NewSensitive(bban.V),
		MaskedPAN: banking.NewSensitive(pan.V), Other: banking.NewSensitive(other.V),
	}
	a.Currency = cur.Currency
	a.FirstSeenAt, a.LastSeenAt, a.NoLongerReportedAt = first.Time, last.Time, gone.V.Time
	return a, nil
}

func sameAccountFacts(old banking.StoredAccount, a banking.Account) bool {
	return old.Handle.Equal(a.Handle) && old.Identifiers.IBAN.Equal(a.Identifiers.IBAN) &&
		old.Identifiers.BBAN.Equal(a.Identifiers.BBAN) && old.Identifiers.MaskedPAN.Equal(a.Identifiers.MaskedPAN) &&
		old.Identifiers.Other.Equal(a.Identifiers.Other) && old.Name == a.Name && old.Product == a.Product &&
		old.Currency == a.Currency && old.NoLongerReportedAt.IsZero()
}

// UpsertAccounts implements [banking.FactStore]. Accounts are matched by
// (provider, external id); an account last reported by another connection
// of the same provider moves to this one.
func (s *Store) UpsertAccounts(ctx context.Context, id banking.ConnectionID, accounts []banking.Account, seenAt time.Time) ([]banking.AccountUpsert, error) {
	if err := requireTime("seen", seenAt); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for i, a := range accounts {
		if err := a.Validate(); err != nil {
			return nil, fmt.Errorf("bankfacts: account %d: %w", i, err)
		}
		if seen[a.ExternalID] {
			return nil, fmt.Errorf("bankfacts: account %d repeats an external id in one call", i)
		}
		seen[a.ExternalID] = true
	}
	out := make([]banking.AccountUpsert, len(accounts))
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		conn, err := connectionTx(ctx, tx, "id = ?", id)
		if err != nil {
			return err
		}
		for i, a := range accounts {
			old, err := scanAccount(tx.QueryRowContext(ctx, "SELECT "+accountColumns+" FROM bank_accounts WHERE provider = ? AND external_id = ?", conn.Provider, a.ExternalID))
			switch {
			case errors.Is(err, sql.ErrNoRows):
				aid := banking.AccountID(s.newID())
				_, err = tx.ExecContext(ctx, "INSERT INTO bank_accounts (id, connection_id, provider, external_id, provider_handle, iban, bban, masked_pan, other_identifier, name, product, currency, first_seen_at, last_seen_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
					aid, id, conn.Provider, a.ExternalID, nullText(a.Handle.Reveal()), nullText(a.Identifiers.IBAN.Reveal()), nullText(a.Identifiers.BBAN.Reveal()),
					nullText(a.Identifiers.MaskedPAN.Reveal()), nullText(a.Identifiers.Other.Reveal()), a.Name, a.Product, sqlvalue.Currency{Currency: a.Currency}, ts(seenAt), ts(seenAt))
				if err != nil {
					return fmt.Errorf("bankfacts: insert account %d: %w", i, err)
				}
				out[i] = banking.AccountUpsert{ID: aid, Outcome: banking.OutcomeInserted}
				continue
			case err != nil:
				return fmt.Errorf("bankfacts: read account %d: %w", i, err)
			}
			outcome := banking.OutcomeUnchanged
			switch {
			case old.ConnectionID != id:
				outcome = banking.OutcomeMoved
			case !sameAccountFacts(old, a):
				outcome = banking.OutcomeUpdated
			}
			_, err = tx.ExecContext(ctx, "UPDATE bank_accounts SET connection_id = ?, provider_handle = ?, iban = ?, bban = ?, masked_pan = ?, other_identifier = ?, name = ?, product = ?, currency = ?, last_seen_at = ?, no_longer_reported_at = NULL WHERE id = ?",
				id, nullText(a.Handle.Reveal()), nullText(a.Identifiers.IBAN.Reveal()), nullText(a.Identifiers.BBAN.Reveal()),
				nullText(a.Identifiers.MaskedPAN.Reveal()), nullText(a.Identifiers.Other.Reveal()), a.Name, a.Product, sqlvalue.Currency{Currency: a.Currency}, ts(seenAt), old.ID)
			if err != nil {
				return fmt.Errorf("bankfacts: update account %s: %w", old.ID, err)
			}
			out[i] = banking.AccountUpsert{ID: old.ID, Outcome: outcome}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Accounts implements [banking.FactStore], in first-seen order.
func (s *Store) Accounts(ctx context.Context, id banking.ConnectionID) ([]banking.StoredAccount, error) {
	var out []banking.StoredAccount
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := connectionTx(ctx, tx, "id = ?", id); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT "+accountColumns+" FROM bank_accounts WHERE connection_id = ? ORDER BY first_seen_at, rowid", id)
		if err != nil {
			return fmt.Errorf("bankfacts: list accounts: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			a, err := scanAccount(rows)
			if err != nil {
				return fmt.Errorf("bankfacts: read account: %w", err)
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

func accountProvider(ctx context.Context, tx *sql.Tx, id banking.AccountID) (string, error) {
	var provider string
	err := tx.QueryRowContext(ctx, "SELECT provider FROM bank_accounts WHERE id = ?", id).Scan(&provider)
	if errors.Is(err, sql.ErrNoRows) {
		return "", notFound("account", id)
	}
	if err != nil {
		return "", fmt.Errorf("bankfacts: read account %s: %w", id, err)
	}
	return provider, nil
}

// ---- balances ----

// AddBalances implements [banking.FactStore].
func (s *Store) AddBalances(ctx context.Context, id banking.AccountID, balances []banking.Balance, obtainedAt time.Time) error {
	if err := requireTime("obtained", obtainedAt); err != nil {
		return err
	}
	for i, b := range balances {
		if err := b.Validate(); err != nil {
			return fmt.Errorf("bankfacts: balance %d: %w", i, err)
		}
	}
	return s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := accountProvider(ctx, tx, id); err != nil {
			return err
		}
		for i, b := range balances {
			_, err := tx.ExecContext(ctx, "INSERT INTO bank_balance_snapshots (id, account_id, balance_type, provider_type, amount, currency, reference_date, obtained_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
				s.newID(), id, b.Type, b.ProviderType, sqlvalue.Amount{Decimal: b.Amount.Amount()}, sqlvalue.Currency{Currency: b.Amount.Currency()},
				nullDate(b.ReferenceDate), ts(obtainedAt))
			if err != nil {
				return fmt.Errorf("bankfacts: insert balance %d: %w", i, err)
			}
		}
		return nil
	})
}

// Balances implements [banking.FactStore], oldest observation first.
func (s *Store) Balances(ctx context.Context, id banking.AccountID) ([]banking.StoredBalance, error) {
	var out []banking.StoredBalance
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := accountProvider(ctx, tx, id); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT id, account_id, balance_type, provider_type, amount, currency, reference_date, obtained_at FROM bank_balance_snapshots WHERE account_id = ? ORDER BY obtained_at, rowid", id)
		if err != nil {
			return fmt.Errorf("bankfacts: list balances: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var b banking.StoredBalance
			var amt sqlvalue.Amount
			var cur sqlvalue.Currency
			var ref sql.Null[sqlvalue.Date]
			var obtained store.Timestamp
			if err := rows.Scan(&b.ID, &b.AccountID, &b.Type, &b.ProviderType, &amt, &cur, &ref, &obtained); err != nil {
				return fmt.Errorf("bankfacts: read balance: %w", err)
			}
			m, err := sqlvalue.Money(amt, cur)
			if err != nil {
				return err
			}
			b.Amount, b.ReferenceDate, b.ObtainedAt = m, ref.V.Date, obtained.Time
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}

// ---- transactions ----

const transactionColumns = "id, account_id, provider, dedupe_key, key_kind, entry_reference, provider_transaction_id, booking_status, provider_status, source_amount, source_currency, instructed_amount, instructed_currency, booking_date, value_date, transaction_date, raw_description, counterparty_name, imported_at, last_seen_at, no_longer_reported_at, superseded_by"

func scanTransaction(r rowScanner) (banking.StoredTransaction, error) {
	var t banking.StoredTransaction
	var ref, ptid, superseded sql.Null[string]
	var amt sqlvalue.Amount
	var cur sqlvalue.Currency
	var iamt sql.Null[sqlvalue.Amount]
	var icur sql.Null[sqlvalue.Currency]
	var bd, vd, td sql.Null[sqlvalue.Date]
	var imported, last store.Timestamp
	var gone sql.Null[store.Timestamp]
	err := r.Scan(&t.ID, &t.AccountID, &t.Provider, &t.Key, &t.KeyKind, &ref, &ptid, &t.Status, &t.ProviderStatus,
		&amt, &cur, &iamt, &icur, &bd, &vd, &td, &t.Description, &t.Counterparty, &imported, &last, &gone, &superseded)
	if err != nil {
		return banking.StoredTransaction{}, err
	}
	if t.Amount, err = sqlvalue.Money(amt, cur); err != nil {
		return banking.StoredTransaction{}, err
	}
	if iamt.Valid {
		if t.InstructedAmount, err = sqlvalue.Money(iamt.V, icur.V); err != nil {
			return banking.StoredTransaction{}, err
		}
	}
	t.EntryReference, t.ProviderTransactionID, t.SupersededBy = ref.V, ptid.V, banking.TransactionID(superseded.V)
	t.BookingDate, t.ValueDate, t.TransactionDate = bd.V.Date, vd.V.Date, td.V.Date
	t.ImportedAt, t.LastSeenAt, t.NoLongerReportedAt = imported.Time, last.Time, gone.V.Time
	return t, nil
}

// sameMutableFacts compares what a re-import may change.
func sameMutableFacts(old banking.StoredTransaction, t banking.Transaction) bool {
	return old.Status == t.Status && old.ProviderStatus == t.ProviderStatus &&
		old.ProviderTransactionID == t.ProviderTransactionID &&
		old.InstructedAmount.Identical(t.InstructedAmount) &&
		old.BookingDate == t.BookingDate && old.ValueDate == t.ValueDate && old.TransactionDate == t.TransactionDate &&
		old.Description == t.Description && old.Counterparty == t.Counterparty && old.NoLongerReportedAt.IsZero()
}

func checkKey(t banking.KeyedTransaction) error {
	prefix := map[banking.KeyKind]string{banking.KeyEntryReference: "ref:", banking.KeyFallback: "fb:"}[t.KeyKind]
	if prefix == "" || !strings.HasPrefix(t.Key, prefix) || !strings.Contains(t.Key, "#") {
		return errors.New("key not built by banking.KeyTransactions")
	}
	if (t.KeyKind == banking.KeyEntryReference) != (t.EntryReference != "") {
		return errors.New("key kind does not match the entry reference")
	}
	return t.Validate()
}

func instructed(m money.Money) (amount, currency any) {
	if !m.IsValid() {
		return nil, nil
	}
	return sqlvalue.Amount{Decimal: m.Amount()}, sqlvalue.Currency{Currency: m.Currency()}
}

// UpsertTransactions implements [banking.FactStore]. Per item:
//
//   - a new key is inserted (imported_at = last_seen_at = seenAt);
//   - a known key gets the provider's current status, dates, description,
//     counterparty and detail id (pending to booked happens here), and
//     last_seen_at = seenAt, and loses any no-longer-reported mark;
//   - a known booked row reported as anything but booked keeps its booked
//     facts (only last_seen_at moves): [banking.OutcomeKeptBooked].
//
// Identity and source amount never change (a database trigger enforces it).
func (s *Store) UpsertTransactions(ctx context.Context, id banking.AccountID, txs []banking.KeyedTransaction, seenAt time.Time) ([]banking.TransactionUpsert, error) {
	if err := requireTime("seen", seenAt); err != nil {
		return nil, err
	}
	keys := map[string]bool{}
	for i, t := range txs {
		if err := checkKey(t); err != nil {
			return nil, fmt.Errorf("bankfacts: transaction %d: %w", i, err)
		}
		if keys[t.Key] {
			return nil, fmt.Errorf("bankfacts: transaction %d repeats a key in one call", i)
		}
		keys[t.Key] = true
	}
	out := make([]banking.TransactionUpsert, len(txs))
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		provider, err := accountProvider(ctx, tx, id)
		if err != nil {
			return err
		}
		for i, t := range txs {
			iamt, icur := instructed(t.InstructedAmount)
			old, err := scanTransaction(tx.QueryRowContext(ctx, "SELECT "+transactionColumns+" FROM bank_transactions WHERE account_id = ? AND dedupe_key = ?", id, t.Key))
			switch {
			case errors.Is(err, sql.ErrNoRows):
				tid := banking.TransactionID(s.newID())
				_, err = tx.ExecContext(ctx, "INSERT INTO bank_transactions (id, account_id, provider, dedupe_key, key_kind, entry_reference, provider_transaction_id, booking_status, provider_status, source_amount, source_currency, instructed_amount, instructed_currency, booking_date, value_date, transaction_date, raw_description, counterparty_name, imported_at, last_seen_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
					tid, id, provider, t.Key, t.KeyKind, nullText(t.EntryReference), nullText(t.ProviderTransactionID), t.Status, t.ProviderStatus,
					sqlvalue.Amount{Decimal: t.Amount.Amount()}, sqlvalue.Currency{Currency: t.Amount.Currency()}, iamt, icur,
					nullDate(t.BookingDate), nullDate(t.ValueDate), nullDate(t.TransactionDate), t.Description, t.Counterparty, ts(seenAt), ts(seenAt))
				if err != nil {
					return fmt.Errorf("bankfacts: insert transaction %d: %w", i, err)
				}
				out[i] = banking.TransactionUpsert{ID: tid, Outcome: banking.OutcomeInserted}
				continue
			case err != nil:
				return fmt.Errorf("bankfacts: read transaction %d: %w", i, err)
			}
			if old.Status == banking.StatusBooked && t.Status != banking.StatusBooked {
				if _, err := tx.ExecContext(ctx, "UPDATE bank_transactions SET last_seen_at = ?, no_longer_reported_at = NULL WHERE id = ?", ts(seenAt), old.ID); err != nil {
					return fmt.Errorf("bankfacts: update transaction %s: %w", old.ID, err)
				}
				out[i] = banking.TransactionUpsert{ID: old.ID, Outcome: banking.OutcomeKeptBooked}
				continue
			}
			outcome := banking.OutcomeUnchanged
			if !sameMutableFacts(old, t.Transaction) {
				outcome = banking.OutcomeUpdated
			}
			_, err = tx.ExecContext(ctx, "UPDATE bank_transactions SET provider_transaction_id = ?, booking_status = ?, provider_status = ?, instructed_amount = ?, instructed_currency = ?, booking_date = ?, value_date = ?, transaction_date = ?, raw_description = ?, counterparty_name = ?, last_seen_at = ?, no_longer_reported_at = NULL WHERE id = ?",
				nullText(t.ProviderTransactionID), t.Status, t.ProviderStatus, iamt, icur,
				nullDate(t.BookingDate), nullDate(t.ValueDate), nullDate(t.TransactionDate), t.Description, t.Counterparty, ts(seenAt), old.ID)
			if err != nil {
				return fmt.Errorf("bankfacts: update transaction %s: %w", old.ID, err)
			}
			out[i] = banking.TransactionUpsert{ID: old.ID, Outcome: outcome}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Transactions implements [banking.FactStore], by date then import order.
func (s *Store) Transactions(ctx context.Context, id banking.AccountID) ([]banking.StoredTransaction, error) {
	var out []banking.StoredTransaction
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := accountProvider(ctx, tx, id); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT "+transactionColumns+" FROM bank_transactions WHERE account_id = ? ORDER BY coalesce(booking_date, value_date, transaction_date), imported_at, rowid", id)
		if err != nil {
			return fmt.Errorf("bankfacts: list transactions: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			t, err := scanTransaction(rows)
			if err != nil {
				return fmt.Errorf("bankfacts: read transaction: %w", err)
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

// ---- purge ----

// PurgeConnection implements [banking.FactStore]. It deletes the
// connection's transactions, balance snapshots, accounts and the connection
// itself in one transaction, the connection through [store.DeleteVersioned]
// at version expected. Every table that refers to bank facts does so with ON
// DELETE RESTRICT, so while user data (a payment match, a correction) refers
// to any of them the purge fails with [banking.ErrPurgeBlocked] and nothing
// is deleted. superseded_by links between the purged transactions are
// cleared first: SQLite checks that self-reference per row, so the delete
// order would otherwise decide whether the purge fails. Ending the provider
// session is the caller's job and should happen first.
func (s *Store) PurgeConnection(ctx context.Context, id banking.ConnectionID, expected int64) (banking.PurgeResult, error) {
	var res banking.PurgeResult
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		c, err := connectionTx(ctx, tx, "id = ?", id)
		if err != nil {
			return err
		}
		if c.Version != expected {
			return fmt.Errorf("bankfacts: connection %s: %w", id, &resource.VersionConflictError{Expected: expected, Current: c.Version})
		}
		accounts := "SELECT id FROM bank_accounts WHERE connection_id = ?"
		steps := []struct {
			query string
			count *int
		}{
			{"UPDATE bank_transactions SET superseded_by = NULL WHERE superseded_by IS NOT NULL AND account_id IN (" + accounts + ")", nil},
			{"DELETE FROM bank_transactions WHERE account_id IN (" + accounts + ")", &res.Transactions},
			{"DELETE FROM bank_balance_snapshots WHERE account_id IN (" + accounts + ")", &res.Balances},
			{"DELETE FROM bank_accounts WHERE connection_id = ?", &res.Accounts},
		}
		for _, st := range steps {
			r, err := tx.ExecContext(ctx, st.query, id)
			if err != nil {
				return purgeError(id, err)
			}
			if st.count != nil {
				n, err := r.RowsAffected()
				if err != nil {
					return err
				}
				*st.count = int(n)
			}
		}
		if err := store.DeleteVersioned(ctx, tx, "bank_connections", string(id), expected); err != nil {
			return purgeError(id, err)
		}
		return nil
	})
	if err != nil {
		return banking.PurgeResult{}, err
	}
	return res, nil
}

// purgeError reports a foreign-key violation, by SQLite's extended result
// code, as [banking.ErrPurgeBlocked]. SQLite reports a violated ON DELETE
// RESTRICT action with the trigger-class code, an unresolved plain foreign
// key with the foreign-key one; neither can come from anything else in the
// purge, whose statements fire no triggers that raise.
func purgeError(id banking.ConnectionID, err error) error {
	if serr, ok := errors.AsType[*sqlite.Error](err); ok && (serr.Code() == sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY || serr.Code() == sqlite3.SQLITE_CONSTRAINT_TRIGGER) {
		return fmt.Errorf("bankfacts: purge connection %s: %w", id, banking.ErrPurgeBlocked)
	}
	return fmt.Errorf("bankfacts: purge connection %s: %w", id, err)
}
