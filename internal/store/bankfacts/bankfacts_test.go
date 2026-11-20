package bankfacts_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking/bankingtest"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/store"
	"github.com/aurimasniekis/go-ledger/internal/store/bankfacts"
)

var t0 = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

type env struct {
	t     *testing.T
	ctx   context.Context
	db    *store.DB
	facts *bankfacts.Store
	clock *clock
	p     *bankingtest.Provider
}

func newEnv(t *testing.T, mode banking.Mode, opts ...func(*bankingtest.Options)) *env {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "ledger.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	c := &clock{t0}
	o := bankingtest.Options{Mode: mode, Now: c.Now, PageSize: 3}
	for _, f := range opts {
		f(&o)
	}
	return &env{t: t, ctx: ctx, db: db, facts: bankfacts.New(db, nil), clock: c, p: bankingtest.New(o, bankingtest.Standard())}
}

// connect runs the consent flow against the fake and records it.
func (e *env) connect(state string) (banking.Connection, banking.Session) {
	e.t.Helper()
	inst := bankingtest.Standard().Institutions[0]
	if _, err := e.p.StartConsent(e.ctx, banking.ConsentRequest{Institution: inst, RedirectURL: "https://ledger.example/cb", State: state, PSUType: banking.PSUPersonal}); err != nil {
		e.t.Fatal(err)
	}
	conn, err := e.facts.CreateConnection(e.ctx, banking.NewConnection{Capabilities: e.p.Capabilities(), Institution: inst, ConsentState: state}, e.clock.now)
	if err != nil {
		e.t.Fatal(err)
	}
	return e.complete(conn, state)
}

func (e *env) complete(conn banking.Connection, state string) (banking.Connection, banking.Session) {
	e.t.Helper()
	found, err := e.facts.ConnectionByConsentState(e.ctx, state)
	if err != nil || found.ID != conn.ID {
		e.t.Fatalf("ConnectionByConsentState: %v", err)
	}
	sess, err := e.p.CompleteConsent(e.ctx, banking.ConsentCompletion{State: state, Code: bankingtest.AuthorizationCode(state)})
	if err != nil {
		e.t.Fatal(err)
	}
	conn, err = e.facts.ActivateConnection(e.ctx, conn.ID, sess, e.clock.now)
	if err != nil {
		e.t.Fatal(err)
	}
	return conn, sess
}

type counts struct{ inserted, updated, unchanged, kept int }

func (c *counts) add(o banking.Outcome) {
	switch o {
	case banking.OutcomeInserted:
		c.inserted++
	case banking.OutcomeUpdated:
		c.updated++
	case banking.OutcomeUnchanged:
		c.unchanged++
	case banking.OutcomeKeptBooked:
		c.kept++
	}
}

// importAll is the shape of what the import engine (B2) will do: accounts,
// balances, then every page of the window, keyed together.
func (e *env) importAll(conn banking.Connection, sess banking.Session, q banking.TransactionQuery) (accounts, txs counts) {
	e.t.Helper()
	provAccounts, err := e.p.Accounts(e.ctx, sess.Reference)
	if err != nil {
		e.t.Fatal(err)
	}
	ups, err := e.facts.UpsertAccounts(e.ctx, conn.ID, provAccounts, e.clock.now)
	if err != nil {
		e.t.Fatal(err)
	}
	for i, a := range provAccounts {
		accounts.add(ups[i].Outcome)
		bals, err := e.p.Balances(e.ctx, sess.Reference, a.Handle)
		if err != nil {
			e.t.Fatal(err)
		}
		if err := e.facts.AddBalances(e.ctx, ups[i].ID, bals, e.clock.now); err != nil {
			e.t.Fatal(err)
		}
		var all []banking.Transaction
		page := q
		for {
			res, err := e.p.Transactions(e.ctx, sess.Reference, a.Handle, page)
			if err != nil {
				e.t.Fatal(err)
			}
			all = append(all, res.Transactions...)
			if res.Next == "" {
				break
			}
			page.Continuation = res.Next
		}
		keyed, err := banking.KeyTransactions(all)
		if err != nil {
			e.t.Fatal(err)
		}
		res, err := e.facts.UpsertTransactions(e.ctx, ups[i].ID, keyed, e.clock.now)
		if err != nil {
			e.t.Fatal(err)
		}
		for _, r := range res {
			txs.add(r.Outcome)
		}
	}
	return accounts, txs
}

func (e *env) count(table string) int {
	e.t.Helper()
	var n int
	err := e.db.ReadTx(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n)
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) eurAccount(conn banking.Connection) banking.StoredAccount {
	e.t.Helper()
	accs, err := e.facts.Accounts(e.ctx, conn.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, a := range accs {
		if a.ExternalID == bankingtest.AccountEUR {
			return a
		}
	}
	e.t.Fatal("EUR account not stored")
	return banking.StoredAccount{}
}

func TestImportIsIdempotentInBothModes(t *testing.T) {
	for _, tc := range []struct {
		mode               banking.Mode
		access             banking.AccountAccess
		accounts, txsTotal int
	}{
		{banking.ModeApp, banking.AccountAccessAll, 3, 9},
		{banking.ModeDeveloper, banking.AccountAccessLinkedOnly, 2, 8},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			e := newEnv(t, tc.mode)
			conn, sess := e.connect("state-1")
			if conn.Status != banking.ConnectionActive || conn.Mode != tc.mode || conn.AccountAccess != tc.access || conn.Version != 2 {
				t.Errorf("connection = status %s, mode %s, access %s, version %d", conn.Status, conn.Mode, conn.AccountAccess, conn.Version)
			}
			if !conn.ConsentValidUntil.Equal(sess.ValidUntil) || conn.Session.Reveal() != sess.Reference.Reveal() {
				t.Error("session or validity not recorded")
			}
			if _, err := e.facts.ConnectionByConsentState(e.ctx, "state-1"); !errors.Is(err, banking.ErrNotFound) {
				t.Errorf("consent state survived activation: %v", err)
			}

			accs, txs := e.importAll(conn, sess, bankingtest.October2026())
			if accs.inserted != tc.accounts || txs.inserted != tc.txsTotal {
				t.Fatalf("first import: %+v accounts, %+v transactions", accs, txs)
			}
			// The same import again: nothing new.
			e.clock.now = t0.Add(time.Hour)
			accs, txs = e.importAll(conn, sess, bankingtest.October2026())
			if accs.unchanged != tc.accounts || txs.unchanged != tc.txsTotal || txs.inserted+txs.updated != 0 {
				t.Errorf("re-import: %+v accounts, %+v transactions", accs, txs)
			}
			if n := e.count("bank_transactions"); n != tc.txsTotal {
				t.Errorf("%d transactions stored after re-import, want %d", n, tc.txsTotal)
			}
			// Overlapping windows: nothing new either.
			oct := bankingtest.October2026()
			_, a := e.importAll(conn, sess, banking.TransactionQuery{From: oct.From, To: mustAdd(t, oct.From, 14)})
			_, b := e.importAll(conn, sess, banking.TransactionQuery{From: mustAdd(t, oct.From, 9), To: oct.To})
			if a.inserted+b.inserted != 0 || e.count("bank_transactions") != tc.txsTotal {
				t.Errorf("overlapping windows added rows: %+v, %+v", a, b)
			}
			// Balances are observations: each fetch appends.
			eur := e.eurAccount(conn)
			bals, err := e.facts.Balances(e.ctx, eur.ID)
			if err != nil || len(bals) != 8 { // 2 balances x 4 imports
				t.Fatalf("balances = %d, %v", len(bals), err)
			}
			if !bals[0].Amount.Identical(money.MustParse("1250.40", "EUR")) || bals[0].ProviderType != "CLBD" || bals[0].ReferenceDate.IsZero() {
				t.Errorf("first balance = %+v", bals[0])
			}
			if !bals[len(bals)-1].ObtainedAt.After(bals[0].ObtainedAt) {
				t.Error("obtained_at not recorded per fetch")
			}
			// Transactions keep their first import time and record the latest sighting.
			stored, err := e.facts.Transactions(e.ctx, eur.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range stored {
				if !s.ImportedAt.Equal(t0) || !s.LastSeenAt.Equal(t0.Add(time.Hour)) {
					t.Errorf("imported_at %v, last_seen_at %v", s.ImportedAt, s.LastSeenAt)
				}
				if s.Provider != bankingtest.ProviderName {
					t.Errorf("provider = %q", s.Provider)
				}
			}
			if eur.Identifiers.IBAN.Reveal() != bankingtest.SyntheticIBANEUR || eur.Handle.IsZero() {
				t.Error("identifiers or handle not round-tripped")
			}
		})
	}
}

func mustAdd(t *testing.T, d calendar.Date, n int) calendar.Date {
	t.Helper()
	out, err := d.AddDays(n)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (e *env) eurTransactions(conn banking.Connection) []banking.StoredTransaction {
	e.t.Helper()
	txs, err := e.facts.Transactions(e.ctx, e.eurAccount(conn).ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return txs
}

func byRef(txs []banking.StoredTransaction, ref string) (banking.StoredTransaction, bool) {
	for _, t := range txs {
		if t.EntryReference == ref {
			return t, true
		}
	}
	return banking.StoredTransaction{}, false
}

func TestPendingBecomesBookedInPlaceAndBookedIsFinal(t *testing.T) {
	e := newEnv(t, banking.ModeApp)
	conn, sess := e.connect("s")
	e.importAll(conn, sess, bankingtest.October2026())
	before, _ := byRef(e.eurTransactions(conn), bankingtest.RefPendingThenBooks)
	if before.Status != banking.StatusPending {
		t.Fatalf("status = %s", before.Status)
	}

	e.p.Mutate(bankingtest.BookPending)
	e.clock.now = t0.Add(24 * time.Hour)
	_, txs := e.importAll(conn, sess, bankingtest.October2026())
	if txs.updated != 1 || txs.inserted != 0 {
		t.Errorf("booking import: %+v", txs)
	}
	after, _ := byRef(e.eurTransactions(conn), bankingtest.RefPendingThenBooks)
	if after.ID != before.ID || after.Status != banking.StatusBooked || after.ProviderStatus != "BOOK" || after.BookingDate.IsZero() {
		t.Errorf("not booked in place: %+v", after.Status)
	}
	if !after.ImportedAt.Equal(before.ImportedAt) || !after.LastSeenAt.Equal(e.clock.now) {
		t.Error("imported_at changed or last_seen_at not moved")
	}

	// A later pending report of the same key cannot undo the booking.
	e.p.Mutate(func(d *bankingtest.Dataset) {
		for i, tx := range d.Accounts[0].Transactions {
			if tx.EntryReference == bankingtest.RefPendingThenBooks {
				tx.Status, tx.ProviderStatus = banking.StatusPending, "PDNG"
				d.Accounts[0].Transactions[i] = tx
			}
		}
	})
	_, txs = e.importAll(conn, sess, bankingtest.October2026())
	if txs.kept != 1 {
		t.Errorf("regressed report: %+v", txs)
	}
	kept, _ := byRef(e.eurTransactions(conn), bankingtest.RefPendingThenBooks)
	if kept.Status != banking.StatusBooked {
		t.Error("booked transaction went back to pending")
	}
}

func TestPendingWithoutReferenceThatChangesBecomesANewRow(t *testing.T) {
	// Documented boundary behaviour: the import engine (B2) marks the old
	// pending row no longer reported and may link it with superseded_by.
	e := newEnv(t, banking.ModeApp)
	conn, sess := e.connect("s")
	e.importAll(conn, sess, bankingtest.October2026())
	n := e.count("bank_transactions")
	e.p.Mutate(func(d *bankingtest.Dataset) {
		txs := d.Accounts[0].Transactions
		for i, tx := range txs {
			if tx.Description == "HOTEL HOLD" {
				tx.Status, tx.ProviderStatus, tx.Amount = banking.StatusBooked, "BOOK", money.MustParse("-68.20", "EUR")
				tx.BookingDate = tx.TransactionDate
				txs[i] = tx
			}
		}
	})
	_, txs := e.importAll(conn, sess, bankingtest.October2026())
	if txs.inserted != 1 || e.count("bank_transactions") != n+1 {
		t.Errorf("changed pending item: %+v", txs)
	}
}

func TestIdenticalTransactionsAreBothKept(t *testing.T) {
	e := newEnv(t, banking.ModeApp)
	conn, sess := e.connect("s")
	e.importAll(conn, sess, bankingtest.October2026())
	coffees := 0
	for _, tx := range e.eurTransactions(conn) {
		if tx.Description == "COFFEE KIOSK" {
			coffees++
			if tx.KeyKind != banking.KeyFallback {
				t.Errorf("key kind = %s", tx.KeyKind)
			}
		}
	}
	if coffees != 2 {
		t.Errorf("%d coffees stored, want 2", coffees)
	}
	abroad, ok := byRef(e.eurTransactions(conn), "fake-ref-abroad-0001")
	if !ok || !abroad.InstructedAmount.Identical(money.MustParse("-20.00", "GBP")) || !abroad.Amount.Identical(money.MustParse("-23.17", "EUR")) {
		t.Errorf("source and instructed amounts not kept exactly: %v / %v", abroad.Amount, abroad.InstructedAmount)
	}
}

func TestReconnectKeepsAccountsAndMovesBetweenConnections(t *testing.T) {
	e := newEnv(t, banking.ModeApp)
	conn, sess := e.connect("s1")
	e.importAll(conn, sess, bankingtest.October2026())
	first := e.eurAccount(conn)

	// Consent expires: the provider classifies it, the connection records it.
	e.clock.now = sess.ValidUntil
	_, err := e.p.Accounts(e.ctx, sess.Reference)
	status, ok := banking.StatusAfter(err)
	if !ok {
		t.Fatalf("expired session not reconnect-required: %v", err)
	}
	conn, err = e.facts.SetConnectionStatus(e.ctx, conn.ID, status, err, e.clock.now)
	if err != nil {
		t.Fatal(err)
	}
	if conn.Status != banking.ConnectionReconnectRequired || conn.LastErrorKind != banking.KindReconnectRequired || conn.LastErrorCode != "session_expired" {
		t.Errorf("connection after expiry: %s %s %s", conn.Status, conn.LastErrorKind, conn.LastErrorCode)
	}

	// Re-authorise the same connection: new session, new handles, same rows.
	if _, err := e.p.StartConsent(e.ctx, banking.ConsentRequest{Institution: bankingtest.Standard().Institutions[0], RedirectURL: "https://ledger.example/cb", State: "s2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.facts.BeginReconnect(e.ctx, conn.ID, "s2", e.clock.now); err != nil {
		t.Fatal(err)
	}
	if _, err := e.facts.BeginReconnect(e.ctx, conn.ID, "", e.clock.now); err == nil {
		t.Error("BeginReconnect accepted an empty state")
	}
	conn, sess = e.complete(conn, "s2")
	if conn.Status != banking.ConnectionActive || conn.LastErrorKind != "" {
		t.Errorf("after reconnect: %s %s", conn.Status, conn.LastErrorKind)
	}
	accs, txs := e.importAll(conn, sess, bankingtest.October2026())
	if accs.inserted != 0 || accs.updated != 3 || txs.inserted != 0 {
		t.Errorf("reconnect import: %+v accounts, %+v transactions", accs, txs)
	}
	again := e.eurAccount(conn)
	if again.ID != first.ID || again.Handle.Equal(first.Handle) {
		t.Error("want the same account row with a new handle")
	}

	// The same account reported by another connection of the provider moves.
	other, sess2 := e.connect("s3")
	provAccs, err := e.p.Accounts(e.ctx, sess2.Reference)
	if err != nil {
		t.Fatal(err)
	}
	ups, err := e.facts.UpsertAccounts(e.ctx, other.ID, provAccs, e.clock.now)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range ups {
		if u.Outcome != banking.OutcomeMoved {
			t.Errorf("outcome = %s, want moved", u.Outcome)
		}
	}
	if e.count("bank_accounts") != 3 {
		t.Error("accounts duplicated across connections")
	}
}

func TestPurgeIsExplicitAndBlockedByUserData(t *testing.T) {
	e := newEnv(t, banking.ModeApp)
	conn, sess := e.connect("s")
	e.importAll(conn, sess, bankingtest.October2026())
	tx := e.eurTransactions(conn)[0]

	// A stand-in for a user-data table (a payment match, X1), referring to a
	// bank fact the way ADR-0013 requires.
	err := e.db.WriteTx(e.ctx, func(ctx context.Context, sqlTx *sql.Tx) error {
		if _, err := sqlTx.ExecContext(ctx, "CREATE TABLE test_user_matches (id INTEGER PRIMARY KEY, transaction_id TEXT NOT NULL REFERENCES bank_transactions (id) ON DELETE RESTRICT) STRICT"); err != nil {
			return err
		}
		_, err := sqlTx.ExecContext(ctx, "INSERT INTO test_user_matches (transaction_id) VALUES (?)", tx.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	before := e.count("bank_transactions")
	if _, err := e.facts.PurgeConnection(e.ctx, conn.ID, e.version(conn)); !errors.Is(err, banking.ErrPurgeBlocked) {
		t.Fatalf("purge with user data: %v, want ErrPurgeBlocked", err)
	}
	if e.count("bank_transactions") != before || e.count("bank_connections") != 1 {
		t.Fatal("a blocked purge deleted something")
	}

	if err := e.db.WriteTx(e.ctx, func(ctx context.Context, sqlTx *sql.Tx) error {
		_, err := sqlTx.ExecContext(ctx, "DELETE FROM test_user_matches")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	res, err := e.facts.PurgeConnection(e.ctx, conn.ID, e.version(conn))
	if err != nil {
		t.Fatal(err)
	}
	if res.Accounts != 3 || res.Transactions != before || res.Balances != 3 {
		t.Errorf("purge result = %+v", res)
	}
	if _, err := e.facts.Connection(e.ctx, conn.ID); !errors.Is(err, banking.ErrNotFound) {
		t.Errorf("connection after purge: %v", err)
	}
	for _, table := range []string{"bank_transactions", "bank_balance_snapshots", "bank_accounts", "bank_connections"} {
		if n := e.count(table); n != 0 {
			t.Errorf("%s has %d rows after purge", table, n)
		}
	}
}

// version is the connection's current version.
func (e *env) version(conn banking.Connection) int64 {
	e.t.Helper()
	c, err := e.facts.Connection(e.ctx, conn.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return c.Version
}

func (e *env) exec(query string, args ...any) error {
	e.t.Helper()
	return e.db.WriteTx(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, query, args...)
		return err
	})
}

// A superseded pair whose referenced (booked) row was inserted first must not
// make the purge fail on the self-referencing foreign key.
func TestPurgeWithSupersededPairInsertedBookedFirst(t *testing.T) {
	e := newEnv(t, banking.ModeApp)
	conn, sess := e.connect("s")
	e.importAll(conn, sess, bankingtest.October2026())
	txs := e.eurTransactions(conn)
	if len(txs) < 2 {
		t.Fatalf("need two transactions, have %d", len(txs))
	}
	booked, pending := txs[0], txs[1] // import order: booked has the lower rowid
	if err := e.exec("UPDATE bank_transactions SET superseded_by = ? WHERE id = ?", booked.ID, pending.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.facts.PurgeConnection(e.ctx, conn.ID, e.version(conn)); err != nil {
		t.Fatalf("purge with a superseded pair: %v", err)
	}
	for _, table := range []string{"bank_transactions", "bank_balance_snapshots", "bank_accounts", "bank_connections"} {
		if n := e.count(table); n != 0 {
			t.Errorf("%s has %d rows after purge", table, n)
		}
	}
}

func TestDisconnectAndPurgeUseExpectedVersion(t *testing.T) {
	e := newEnv(t, banking.ModeApp)
	conn, _ := e.connect("s")
	v := e.version(conn)

	var vc *resource.VersionConflictError
	if _, err := e.facts.DisconnectConnection(e.ctx, conn.ID, v-1, e.clock.now); !errors.As(err, &vc) {
		t.Fatalf("disconnect at a stale version: %v, want a version conflict", err)
	}
	if _, err := e.facts.PurgeConnection(e.ctx, conn.ID, v+1); !errors.As(err, &vc) {
		t.Fatalf("purge at a wrong version: %v, want a version conflict", err)
	}
	if e.count("bank_connections") != 1 {
		t.Fatal("a conflicting purge deleted the connection")
	}
	got, err := e.facts.DisconnectConnection(e.ctx, conn.ID, v, e.clock.now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != banking.ConnectionDisconnected || got.Version != v+1 {
		t.Errorf("after disconnect: status %q version %d, want disconnected and %d", got.Status, got.Version, v+1)
	}
	if _, err := e.facts.DisconnectConnection(e.ctx, "01JZ0000000000000000000009", 1, e.clock.now); !errors.Is(err, banking.ErrNotFound) {
		t.Errorf("disconnect of an unknown connection: %v", err)
	}
	if _, err := e.facts.PurgeConnection(e.ctx, conn.ID, got.Version); err != nil {
		t.Fatal(err)
	}
}

func TestSupersededAndProviderMustMatchTheAccount(t *testing.T) {
	e := newEnv(t, banking.ModeApp)
	conn, sess := e.connect("s")
	e.importAll(conn, sess, bankingtest.October2026())
	accts, err := e.facts.Accounts(e.ctx, conn.ID)
	if err != nil || len(accts) < 2 {
		t.Fatalf("accounts: %v (%d)", err, len(accts))
	}
	var one, two banking.StoredTransaction
	for _, a := range accts {
		txs, err := e.facts.Transactions(e.ctx, a.ID)
		if err != nil || len(txs) == 0 {
			continue
		}
		if one.ID == "" {
			one = txs[0]
		} else if two.ID == "" {
			two = txs[0]
		}
	}
	if one.ID == "" || two.ID == "" || one.AccountID == two.AccountID {
		t.Skip("dataset has transactions in fewer than two accounts")
	}
	if err := e.exec("UPDATE bank_transactions SET superseded_by = ? WHERE id = ?", one.ID, two.ID); err == nil {
		t.Error("superseded_by across accounts accepted")
	}
	if err := e.exec("UPDATE bank_accounts SET provider = 'other' WHERE id = ?", one.AccountID); err == nil {
		t.Error("account provider changed")
	}
	if err := e.exec("INSERT INTO bank_transactions (id, account_id, provider, dedupe_key, key_kind, entry_reference, booking_status, provider_status, source_amount, source_currency, booking_date, imported_at, last_seen_at) SELECT ?, account_id, 'other', 'ref:zz#1', 'entry_reference', 'zz', 'booked', 'BOOK', '1', 'EUR', '2026-10-01', imported_at, last_seen_at FROM bank_transactions WHERE id = ?", resource.NewID(), one.ID); err == nil {
		t.Error("transaction with a provider different from its account accepted")
	}
}

func TestBankFactsAreProtectedByTheSchema(t *testing.T) {
	e := newEnv(t, banking.ModeApp)
	conn, sess := e.connect("s")
	e.importAll(conn, sess, bankingtest.October2026())
	booked, _ := byRef(e.eurTransactions(conn), bankingtest.RefRent)
	for name, q := range map[string]string{
		"source amount":       "UPDATE bank_transactions SET source_amount = '-1.00' WHERE id = ?",
		"imported_at":         "UPDATE bank_transactions SET imported_at = '2020-01-01T00:00:00.000000000Z' WHERE id = ?",
		"dedupe key":          "UPDATE bank_transactions SET dedupe_key = 'fb:x#1' WHERE id = ?",
		"booked to pending":   "UPDATE bank_transactions SET booking_status = 'pending' WHERE id = ?",
		"balance snapshot":    "UPDATE bank_balance_snapshots SET amount = '0' WHERE account_id = (SELECT account_id FROM bank_transactions WHERE id = ?)",
		"cascade-free delete": "DELETE FROM bank_accounts WHERE id = (SELECT account_id FROM bank_transactions WHERE id = ?)",
		"float amount":        "UPDATE bank_transactions SET instructed_amount = '1e3', instructed_currency = 'EUR' WHERE id = ?",
	} {
		err := e.db.WriteTx(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, q, booked.ID)
			return err
		})
		if err == nil {
			t.Errorf("%s: the schema allowed it", name)
		}
	}
}

func TestValidationAndErrorsLeakNothing(t *testing.T) {
	e := newEnv(t, banking.ModeApp)
	conn, _ := e.connect("s")
	secret := banking.NewSensitive(bankingtest.SyntheticIBANEUR)
	_, err := e.facts.UpsertAccounts(e.ctx, conn.ID, []banking.Account{{ExternalID: "x", Identifiers: banking.AccountIdentifiers{IBAN: secret}}}, e.clock.now)
	if err == nil || strings.Contains(err.Error(), bankingtest.SyntheticIBANEUR) {
		t.Errorf("account without currency: %v", err)
	}
	if _, err := e.facts.UpsertAccounts(e.ctx, "01ARZ3NDEKTSV4RRFFQ69G5FAV", nil, e.clock.now); !errors.Is(err, banking.ErrNotFound) {
		t.Errorf("unknown connection: %v", err)
	}
	ups, err := e.facts.UpsertAccounts(e.ctx, conn.ID, []banking.Account{{ExternalID: "x", Currency: money.MustParseCurrency("EUR"), Identifiers: banking.AccountIdentifiers{IBAN: secret}}}, e.clock.now)
	if err != nil {
		t.Fatal(err)
	}
	unkeyed := banking.KeyedTransaction{Transaction: banking.Transaction{
		Status: banking.StatusBooked, ProviderStatus: "BOOK", Amount: money.MustParse("-1.00", "EUR"),
		BookingDate: calendar.MustDate(2026, time.October, 1), Description: "PRIVATE TEXT",
	}, Key: "made-up", KeyKind: banking.KeyFallback}
	_, err = e.facts.UpsertTransactions(e.ctx, ups[0].ID, []banking.KeyedTransaction{unkeyed}, e.clock.now)
	if err == nil || strings.Contains(err.Error(), "PRIVATE") {
		t.Errorf("hand-made key: %v", err)
	}
	keyed, _ := banking.KeyTransactions([]banking.Transaction{unkeyed.Transaction})
	_, err = e.facts.UpsertTransactions(e.ctx, ups[0].ID, []banking.KeyedTransaction{keyed[0], keyed[0]}, e.clock.now)
	if err == nil {
		t.Error("a key repeated in one call was accepted")
	}
	if _, err := e.facts.SetConnectionStatus(e.ctx, conn.ID, banking.ConnectionActive, nil, e.clock.now); err == nil {
		t.Error("SetConnectionStatus set active without a session")
	}
	if _, err := e.facts.CreateConnection(e.ctx, banking.NewConnection{Capabilities: banking.Capabilities{Provider: "Bad-Name", Application: "a", Mode: banking.ModeApp, Environment: banking.EnvironmentSandbox}, Institution: bankingtest.Standard().Institutions[0], ConsentState: "z"}, e.clock.now); err == nil {
		t.Error("invalid provider name accepted")
	}
}
