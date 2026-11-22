package bankfacts_test

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/bankimport"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking/bankingtest"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
)

// Import engine (B2) tests: bankimport.Engine against the real store and the
// fake provider. They live here because domain packages may not import the
// store, even in tests.

// hooked wraps the fake to fail or act on a given call.
type hooked struct {
	*bankingtest.Provider
	txCalls      int
	onTx         func(ctx context.Context, n int) error
	onAccounts   func()
	balanceCalls int
}

func (h *hooked) Transactions(ctx context.Context, ref, handle banking.Sensitive, q banking.TransactionQuery) (banking.TransactionPage, error) {
	h.txCalls++
	if h.onTx != nil {
		if err := h.onTx(ctx, h.txCalls); err != nil {
			return banking.TransactionPage{}, err
		}
	}
	return h.Provider.Transactions(ctx, ref, handle, q)
}

func (h *hooked) Accounts(ctx context.Context, ref banking.Sensitive) ([]banking.Account, error) {
	if h.onAccounts != nil {
		h.onAccounts()
	}
	return h.Provider.Accounts(ctx, ref)
}

func (h *hooked) Balances(ctx context.Context, ref, handle banking.Sensitive) ([]banking.Balance, error) {
	h.balanceCalls++
	return h.Provider.Balances(ctx, ref, handle)
}

type engineEnv struct {
	*env
	engine *bankimport.Engine
	h      *hooked
	conn   banking.Connection
	sleeps []time.Duration
}

func newEngineEnv(t *testing.T, mode banking.Mode, opts ...func(*bankingtest.Options)) *engineEnv {
	t.Helper()
	e := &engineEnv{env: newEnv(t, mode, opts...)}
	e.h = &hooked{Provider: e.p}
	e.engine = bankimport.New(e.facts, bankimport.Options{
		Now: e.clock.Now,
		Sleep: func(_ context.Context, d time.Duration) error {
			e.sleeps = append(e.sleeps, d)
			return nil
		},
		Jitter: func(d time.Duration) time.Duration { return d },
	})
	e.conn, _ = e.connect("s")
	return e
}

// A refresh that started with the old session and fails with
// reconnect-required after the user has reconnected must not undo the
// reconnect: the failure stays on the run, the new consent stays active.
func TestStaleRunFailureDoesNotUndoReconnect(t *testing.T) {
	e := newEngineEnv(t, banking.ModeApp)
	e.mustRefresh()
	oldSession := e.conn.Session
	e.h.onAccounts = func() {
		e.h.onAccounts = nil
		if _, err := e.p.StartConsent(e.ctx, banking.ConsentRequest{Institution: bankingtest.Standard().Institutions[0], RedirectURL: "https://ledger.example/cb", State: "s2"}); err != nil {
			t.Error(err)
		}
		if _, err := e.facts.BeginReconnectBy(e.ctx, e.conn.ID, "s2", "user", e.clock.now, nil); err != nil {
			t.Error(err)
		}
		e.clock.now = e.clock.now.Add(time.Minute)
		e.complete(e.conn, "s2")
		e.p.FailNext(bankingtest.OpAccounts, bankingtest.Failure(bankingtest.OpAccounts, banking.KindReconnectRequired, banking.ReasonNone, "session_expired", 0))
	}
	res, err := e.refresh()
	if !errors.Is(err, banking.ErrReconnectRequired) || res.Run.Outcome != banking.RefreshFailed || res.Run.ErrorKind != banking.KindReconnectRequired {
		t.Fatalf("stale run: %v, %s, %s; want a recorded reconnect-required failure", err, res.Run.Outcome, res.Run.ErrorKind)
	}
	c, err := e.facts.Connection(e.ctx, e.conn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != banking.ConnectionActive || c.Session.Equal(oldSession) || c.LastErrorKind != "" {
		t.Errorf("reconnect undone by a stale run: status %s, error kind %q", c.Status, c.LastErrorKind)
	}
}

type recordedAudit struct{ events []audit.Event }

func (r *recordedAudit) WriteAudit(_ context.Context, _ *sql.Tx, ev audit.Event) error {
	r.events = append(r.events, ev)
	return nil
}

func staleStatus(e *engineEnv, sess banking.Sensitive, granted time.Time, ev banking.AuditFunc) (banking.Connection, bool) {
	e.t.Helper()
	c, applied, err := e.facts.SetConnectionStatusForSession(e.ctx, e.conn.ID, sess, granted, banking.ConnectionReconnectRequired,
		&banking.Error{Kind: banking.KindReconnectRequired, Code: "session_expired"}, e.clock.now, ev)
	if err != nil {
		e.t.Fatal(err)
	}
	return c, applied
}

// A stale failure of the current session while a reconnect is still in flight
// must not clear the reconnect's consent state: its callback still claims it
// and activates the connection.
func TestStaleFailureKeepsReconnectInFlight(t *testing.T) {
	e := newEngineEnv(t, banking.ModeApp)
	conn, err := e.facts.Connection(e.ctx, e.conn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.p.StartConsent(e.ctx, banking.ConsentRequest{Institution: bankingtest.Standard().Institutions[0], RedirectURL: "https://ledger.example/cb", State: "s2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.facts.BeginReconnectBy(e.ctx, e.conn.ID, "s2", "user", e.clock.now, nil); err != nil {
		t.Fatal(err)
	}
	if _, applied := staleStatus(e, conn.Session, conn.ConsentGrantedAt, nil); !applied {
		t.Fatal("a failure of the current session was not recorded")
	}
	claim, err := e.facts.ClaimConsent(e.ctx, "s2", "user", e.clock.now, time.Hour)
	if err != nil {
		t.Fatalf("claim after a stale failure: %v", err)
	}
	sess, err := e.p.CompleteConsent(e.ctx, banking.ConsentCompletion{State: "s2", Code: bankingtest.AuthorizationCode("s2")})
	if err != nil {
		t.Fatal(err)
	}
	c, err := e.facts.ActivateClaimed(e.ctx, claim, sess, e.clock.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != banking.ConnectionActive || c.LastErrorKind != "" {
		t.Errorf("after activation: status %s, error kind %q", c.Status, c.LastErrorKind)
	}
}

// A refresh failing after a disconnect must not bring the connection back as
// reconnect-required, and a different grant time with the same session
// reference is a different authorisation. Each skip is recorded.
func TestStaleFailureSkips(t *testing.T) {
	e := newEngineEnv(t, banking.ModeApp)
	rec := &recordedAudit{}
	e.facts.WithAudit(rec)
	conn, err := e.facts.Connection(e.ctx, e.conn.ID)
	if err != nil {
		t.Fatal(err)
	}
	ev := func(c banking.Connection) audit.Event {
		return audit.Event{Action: audit.BankStatusUpdateSkipped, Actor: audit.System, TargetType: "bank_connection", TargetID: string(c.ID), At: e.clock.now,
			Details: map[string]string{"provider": c.Provider, "application": c.Application, "kind": "reconnect_required", "status": "reconnect_required"}}
	}

	// Same reference, re-granted later.
	e.clock.now = e.clock.now.Add(time.Minute)
	if _, err := e.facts.ActivateConnection(e.ctx, e.conn.ID, banking.Session{Reference: conn.Session, ValidUntil: conn.ConsentValidUntil}, e.clock.now); err != nil {
		t.Fatal(err)
	}
	if _, applied := staleStatus(e, conn.Session, conn.ConsentGrantedAt, ev); applied {
		t.Error("a failure of an earlier grant with a reused session reference was applied")
	}
	if len(rec.events) != 1 {
		t.Fatalf("audit events %d, want 1", len(rec.events))
	}

	// Disconnected.
	cur, err := e.facts.Connection(e.ctx, e.conn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.facts.DisconnectConnection(e.ctx, e.conn.ID, cur.Version, e.clock.now); err != nil {
		t.Fatal(err)
	}
	c, applied := staleStatus(e, cur.Session, cur.ConsentGrantedAt, ev)
	if applied || c.Status != banking.ConnectionDisconnected {
		t.Errorf("after a disconnect: applied %v, status %s", applied, c.Status)
	}
	if len(rec.events) != 2 {
		t.Errorf("audit events %d, want 2", len(rec.events))
	}
}

func (e *engineEnv) refresh() (bankimport.Result, error) {
	e.t.Helper()
	return e.engine.Refresh(e.ctx, e.conn.ID, e.h)
}

func (e *engineEnv) mustRefresh() bankimport.Result {
	e.t.Helper()
	res, err := e.refresh()
	if err != nil {
		e.t.Fatalf("refresh: %v", err)
	}
	if res.Run.Outcome != banking.RefreshSucceeded {
		e.t.Fatalf("refresh outcome %s", res.Run.Outcome)
	}
	return res
}

func (e *engineEnv) sync(id banking.AccountID) banking.AccountSync {
	e.t.Helper()
	s, err := e.facts.AccountSync(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

func (e *engineEnv) freshness(id banking.AccountID) banking.Freshness {
	e.t.Helper()
	c, err := e.facts.Connection(e.ctx, e.conn.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return banking.AccountFreshness(c.Status, e.sync(id), e.clock.now, 0)
}

// snapshot is every stored transaction of the EUR account by id.
func (e *engineEnv) snapshot() map[banking.TransactionID]banking.StoredTransaction {
	e.t.Helper()
	out := map[banking.TransactionID]banking.StoredTransaction{}
	for _, tx := range e.eurTransactions(e.conn) {
		out[tx.ID] = tx
	}
	return out
}

func mutateEUR(e *engineEnv, fn func(*banking.Transaction) bool) {
	e.p.Mutate(func(d *bankingtest.Dataset) {
		txs := d.Accounts[0].Transactions
		for i := range txs {
			if fn(&txs[i]) {
				return
			}
		}
	})
}

func TestRefreshIsIdempotentInBothModes(t *testing.T) {
	for _, tc := range []struct {
		mode          banking.Mode
		accounts, txs int
	}{
		{banking.ModeApp, 3, 9},
		{banking.ModeDeveloper, 2, 8},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			e := newEngineEnv(t, tc.mode)
			first := e.mustRefresh()
			if first.Run.AccountsReported != tc.accounts || first.Run.TransactionsInserted != tc.txs || first.Run.AccountsSucceeded != tc.accounts {
				t.Fatalf("first refresh counts: %+v", first.Run.RefreshCounts)
			}
			before := e.snapshot()
			e.clock.now = e.clock.now.Add(time.Hour)
			second := e.mustRefresh()
			if second.Run.TransactionsInserted != 0 || second.Run.TransactionsUpdated != 0 || second.Run.TransactionsNoLongerReported != 0 {
				t.Errorf("repeated refresh changed transactions: %+v", second.Run.RefreshCounts)
			}
			if e.count("bank_transactions") != tc.txs || e.count("bank_accounts") != tc.accounts {
				t.Errorf("rows after repeat: %d transactions, %d accounts", e.count("bank_transactions"), e.count("bank_accounts"))
			}
			after := e.snapshot()
			for id, tx := range before {
				got, ok := after[id]
				if !ok || !got.ImportedAt.Equal(tx.ImportedAt) || got.Key != tx.Key {
					t.Errorf("transaction %s changed identity on re-import", id)
				}
			}
			runs, err := e.facts.RefreshRuns(e.ctx, e.conn.ID, 0)
			if err != nil || len(runs) != 2 || runs[0].ID != second.Run.ID || runs[1].Outcome != banking.RefreshSucceeded {
				t.Errorf("runs = %d (%v)", len(runs), err)
			}
			acc := e.eurAccount(e.conn)
			s := e.sync(acc.ID)
			if !s.LastAttemptAt.Equal(e.clock.now) || !s.LastSuccessAt.Equal(e.clock.now) || s.LastErrorKind != "" {
				t.Errorf("sync state = %+v", s)
			}
			if s.TransactionsThrough != calendar.MustDate(2026, time.November, 1) {
				t.Errorf("transactions through %s", s.TransactionsThrough)
			}
		})
	}
}

func TestOverlappingRefreshesAddOnlyWhatIsNew(t *testing.T) {
	e := newEngineEnv(t, banking.ModeApp)
	e.mustRefresh()
	n := e.count("bank_transactions")

	// Two days later a new item and a late booking dated inside the overlap.
	e.clock.now = t0.Add(48 * time.Hour)
	e.p.Mutate(func(d *bankingtest.Dataset) {
		d.Accounts[0].Transactions = append(d.Accounts[0].Transactions,
			banking.Transaction{EntryReference: "fake-ref-late-0001", Status: banking.StatusBooked, ProviderStatus: "BOOK",
				Amount: money.MustParse("-9.99", "EUR"), BookingDate: calendar.MustDate(2026, time.October, 28), Description: "LATE BOOKING"},
			banking.Transaction{EntryReference: "fake-ref-new-0001", Status: banking.StatusBooked, ProviderStatus: "BOOK",
				Amount: money.MustParse("-15.00", "EUR"), BookingDate: calendar.MustDate(2026, time.November, 2), Description: "NEW"})
	})
	res := e.mustRefresh()
	if res.Run.TransactionsInserted != 2 || e.count("bank_transactions") != n+2 {
		t.Errorf("overlapping refresh: %+v, %d rows", res.Run.RefreshCounts, e.count("bank_transactions"))
	}
	// The overlap re-reported the October items it covers: unchanged, not new.
	if res.Run.TransactionsUnchanged == 0 {
		t.Error("the overlap fetched nothing already stored")
	}
	if s := e.sync(e.eurAccount(e.conn).ID); s.TransactionsThrough != calendar.MustDate(2026, time.November, 3) {
		t.Errorf("through = %s", s.TransactionsThrough)
	}
}

func TestSmallOverlappingChunksKeepIdenticalItemsApart(t *testing.T) {
	e := newEngineEnv(t, banking.ModeApp)
	e.engine = bankimport.New(e.facts, bankimport.Options{Now: e.clock.Now, ChunkDays: 2, OverlapDays: 3, InitialHistoryDays: 40})
	e.mustRefresh()
	n := e.count("bank_transactions")
	for range 3 {
		e.clock.now = e.clock.now.Add(24 * time.Hour)
		e.mustRefresh()
	}
	coffees := 0
	for _, tx := range e.eurTransactions(e.conn) {
		if tx.Description == "COFFEE KIOSK" {
			coffees++
		}
	}
	if coffees != 2 || e.count("bank_transactions") != n {
		t.Errorf("%d coffees, %d rows (want 2, %d)", coffees, e.count("bank_transactions"), n)
	}
}

func TestPaginatedRefreshStoresEveryPage(t *testing.T) {
	e := newEngineEnv(t, banking.ModeApp, func(o *bankingtest.Options) { o.PageSize, o.EmptyFirstPage = 1, true })
	res := e.mustRefresh()
	if res.Run.TransactionsInserted != 9 {
		t.Errorf("inserted %d, want 9", res.Run.TransactionsInserted)
	}
	// One empty first page per window plus one page per transaction.
	if e.h.txCalls < 9+3 {
		t.Errorf("only %d transaction pages fetched", e.h.txCalls)
	}
}

func TestEndlessPaginationFailsTheWindow(t *testing.T) {
	e := newEngineEnv(t, banking.ModeApp)
	e.engine = bankimport.New(e.facts, bankimport.Options{Now: e.clock.Now, MaxPages: 1})
	res, err := e.refresh()
	if err == nil || res.Run.Outcome == banking.RefreshSucceeded || res.Run.ErrorCode != "too_many_pages" {
		t.Fatalf("refresh with a page cap of 1: %v, %+v", err, res.Run)
	}
}

func TestInterruptedRefreshResumesWithoutDuplicates(t *testing.T) {
	clean := newEngineEnv(t, banking.ModeApp)
	clean.mustRefresh()
	want := clean.count("bank_transactions")

	e := newEngineEnv(t, banking.ModeApp)
	// EUR windows (Aug 4-Sep 3, Sep 4-Oct 4, Oct 5-Nov 1) take four calls at
	// three per page; the fourth is the second page of the last window.
	e.h.onTx = func(_ context.Context, n int) error {
		if n == 4 {
			return bankingtest.Failure(bankingtest.OpTransactions, banking.KindPermanent, "", "boom", 0)
		}
		return nil
	}
	res, err := e.refresh()
	if banking.Classify(err) != banking.KindPermanent || res.Run.Outcome != banking.RefreshPartial || res.Run.AccountsFailed != 1 {
		t.Fatalf("interrupted refresh: %v, %+v", err, res.Run)
	}
	eur := e.eurAccount(e.conn)
	if got := len(e.eurTransactions(e.conn)); got != 2 {
		t.Errorf("%d EUR transactions after the interruption, want the 2 of the complete windows", got)
	}
	if s := e.sync(eur.ID); s.TransactionsThrough != calendar.MustDate(2026, time.October, 4) || s.LastErrorCode != "boom" {
		t.Errorf("sync after interruption: %+v", s)
	}

	e.h.onTx = nil
	e.clock.now = e.clock.now.Add(time.Hour)
	e.mustRefresh()
	if got := e.count("bank_transactions"); got != want {
		t.Errorf("%d rows after resuming, want %d", got, want)
	}
	if f := e.freshness(eur.ID); f.State != banking.FreshnessFresh {
		t.Errorf("freshness after resume = %s", f.State)
	}
}

func TestCancelledRefreshIsInterruptedAndResumes(t *testing.T) {
	e := newEngineEnv(t, banking.ModeApp)
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	e.h.onTx = func(_ context.Context, n int) error {
		if n == 3 {
			cancel()
			return context.Canceled
		}
		return nil
	}
	res, err := e.engine.Refresh(ctx, e.conn.ID, e.h)
	if !errors.Is(err, context.Canceled) || res.Run.Outcome != banking.RefreshInterrupted || res.Run.FinishedAt.IsZero() {
		t.Fatalf("cancelled refresh: %v, %+v", err, res.Run)
	}
	e.h.onTx = nil
	e.clock.now = e.clock.now.Add(time.Hour)
	e.mustRefresh()
	if got := e.count("bank_transactions"); got != 9 {
		t.Errorf("%d rows after resuming, want 9", got)
	}
}

func TestRunLeftRunningIsMarkedInterrupted(t *testing.T) {
	e := newEngineEnv(t, banking.ModeApp)
	stale, err := e.facts.StartRefresh(e.ctx, e.conn.ID, e.clock.now, 0) // the process "stopped" here
	if err != nil {
		t.Fatal(err)
	}
	// Within the lease the run may still be alive in another engine.
	e.clock.now = e.clock.now.Add(time.Minute)
	if _, err := e.refresh(); !errors.Is(err, bankimport.ErrRefreshInProgress) {
		t.Fatalf("refresh within the lease: %v", err)
	}
	e.clock.now = e.clock.now.Add(banking.DefaultRunLease)
	e.mustRefresh()
	runs, err := e.facts.RefreshRuns(e.ctx, e.conn.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(runs, func(r banking.RefreshRun) bool { return r.ID == stale.ID })
	if i < 0 || runs[i].Outcome != banking.RefreshInterrupted || runs[i].FinishedAt.IsZero() {
		t.Errorf("unfinished run = %+v", runs)
	}
	// A finished run is history.
	if err := e.exec("UPDATE bank_refresh_runs SET outcome = 'failed' WHERE id = ?", stale.ID); err == nil {
		t.Error("a finished run was rewritten")
	}
}

func TestPendingToBookedTransitions(t *testing.T) {
	e := newEngineEnv(t, banking.ModeApp)
	e.mustRefresh()
	before := e.eurTransactions(e.conn)
	withRef, _ := byRef(before, bankingtest.RefPendingThenBooks)
	var hold banking.StoredTransaction
	for _, tx := range before {
		if tx.Description == "HOTEL HOLD" {
			hold = tx
		}
	}
	if withRef.Status != banking.StatusPending || hold.Status != banking.StatusPending || withRef.Countable() || hold.Countable() {
		t.Fatal("pending items not stored as pending")
	}

	t.Run("same reference and amount: booked in place", func(t *testing.T) {
		e.p.Mutate(bankingtest.BookPending)
		e.clock.now = t0.Add(24 * time.Hour)
		res := e.mustRefresh()
		after, _ := byRef(e.eurTransactions(e.conn), bankingtest.RefPendingThenBooks)
		if after.ID != withRef.ID || after.Status != banking.StatusBooked || !after.Countable() || res.Run.TransactionsInserted != 0 {
			t.Errorf("not booked in place: %s, %+v", after.Status, res.Run.RefreshCounts)
		}
	})

	t.Run("same reference, amount changed on booking: superseded", func(t *testing.T) {
		e.p.Mutate(func(d *bankingtest.Dataset) {
			d.Accounts[0].Transactions = append(d.Accounts[0].Transactions, banking.Transaction{
				EntryReference: "fake-ref-hold-0002", Status: banking.StatusPending, ProviderStatus: "PDNG",
				Amount: money.MustParse("-40.00", "EUR"), TransactionDate: calendar.MustDate(2026, time.October, 29), Description: "FUEL HOLD",
			})
		})
		e.clock.now = t0.Add(25 * time.Hour)
		e.mustRefresh()
		pendingHold, _ := byRef(e.eurTransactions(e.conn), "fake-ref-hold-0002")
		mutateEUR(e, func(tx *banking.Transaction) bool {
			if tx.EntryReference != "fake-ref-hold-0002" {
				return false
			}
			tx.Status, tx.ProviderStatus, tx.Amount, tx.BookingDate = banking.StatusBooked, "BOOK", money.MustParse("-47.50", "EUR"), calendar.MustDate(2026, time.October, 31)
			return true
		})
		e.clock.now = t0.Add(26 * time.Hour)
		res := e.mustRefresh()
		var old, booked banking.StoredTransaction
		for _, tx := range e.eurTransactions(e.conn) {
			if tx.EntryReference == "fake-ref-hold-0002" {
				if tx.ID == pendingHold.ID {
					old = tx
				} else {
					booked = tx
				}
			}
		}
		if res.Run.TransactionsInserted != 1 || res.Run.TransactionsSuperseded != 1 {
			t.Errorf("counts = %+v", res.Run.RefreshCounts)
		}
		if old.NoLongerReportedAt.IsZero() || old.SupersededBy != booked.ID || old.Countable() || !booked.Countable() {
			t.Errorf("old hold: gone %v, superseded by %q (booked %q)", old.NoLongerReportedAt, old.SupersededBy, booked.ID)
		}
		if !old.Amount.Identical(money.MustParse("-40.00", "EUR")) || !booked.Amount.Identical(money.MustParse("-47.50", "EUR")) {
			t.Error("source amounts not kept as reported")
		}
	})

	t.Run("no reference, changed on booking: unreported, not linked", func(t *testing.T) {
		mutateEUR(e, func(tx *banking.Transaction) bool {
			if tx.Description != "HOTEL HOLD" {
				return false
			}
			tx.Status, tx.ProviderStatus, tx.Amount, tx.BookingDate = banking.StatusBooked, "BOOK", money.MustParse("-68.20", "EUR"), tx.TransactionDate
			return true
		})
		e.clock.now = t0.Add(27 * time.Hour)
		res := e.mustRefresh()
		var old banking.StoredTransaction
		newRows := 0
		for _, tx := range e.eurTransactions(e.conn) {
			switch {
			case tx.ID == hold.ID:
				old = tx
			case tx.Description == "HOTEL HOLD":
				newRows++
			}
		}
		if newRows != 1 || res.Run.TransactionsNoLongerReported != 1 || res.Run.TransactionsSuperseded != 0 {
			t.Errorf("counts = %+v, new rows %d", res.Run.RefreshCounts, newRows)
		}
		// Linking by amount or text would be a guess (matching safeguards are
		// decision D9's): the pending row is only marked.
		if old.NoLongerReportedAt.IsZero() || old.SupersededBy != "" || old.Countable() {
			t.Errorf("old hold = gone %v, superseded %q", old.NoLongerReportedAt, old.SupersededBy)
		}
	})

	t.Run("an unreported row reported again is restored", func(t *testing.T) {
		e.p.Mutate(func(d *bankingtest.Dataset) {
			d.Accounts[0].Transactions = append(d.Accounts[0].Transactions, banking.Transaction{
				EntryReference: "fake-ref-hold-0002", Status: banking.StatusPending, ProviderStatus: "PDNG",
				Amount: money.MustParse("-40.00", "EUR"), TransactionDate: calendar.MustDate(2026, time.October, 29), Description: "FUEL HOLD",
			})
		})
		e.clock.now = t0.Add(28 * time.Hour)
		e.mustRefresh()
		for _, tx := range e.eurTransactions(e.conn) {
			if tx.EntryReference == "fake-ref-hold-0002" && tx.Status == banking.StatusPending && (!tx.NoLongerReportedAt.IsZero() || tx.SupersededBy != "") {
				t.Errorf("re-reported pending row still marked: %v %q", tx.NoLongerReportedAt, tx.SupersededBy)
			}
		}
	})
}

func TestCorrectionsSurviveReimport(t *testing.T) {
	e := newEngineEnv(t, banking.ModeApp)
	e.mustRefresh()
	// A stand-in for a user-correction table (merchant correction, X1/O1):
	// user data in its own table, referring to bank facts with RESTRICT.
	if err := e.exec("CREATE TABLE test_user_corrections (transaction_id TEXT PRIMARY KEY REFERENCES bank_transactions (id) ON DELETE RESTRICT, merchant TEXT NOT NULL) STRICT"); err != nil {
		t.Fatal(err)
	}
	txs := e.eurTransactions(e.conn)
	rent, _ := byRef(txs, bankingtest.RefRent)
	pending, _ := byRef(txs, bankingtest.RefPendingThenBooks)
	for _, id := range []banking.TransactionID{rent.ID, pending.ID} {
		if err := e.exec("INSERT INTO test_user_corrections (transaction_id, merchant) VALUES (?, 'Corrected merchant')", id); err != nil {
			t.Fatal(err)
		}
	}

	// The pending item books, the bank rewrites the rent's text, and later
	// stops reporting the rent altogether. Each refresh resumes from a cursor
	// set back so that its window reaches the rent's date.
	resumeFromOctober := func() {
		if err := e.exec("UPDATE bank_account_sync SET transactions_through = '2026-10-05'"); err != nil {
			t.Fatal(err)
		}
	}
	e.p.Mutate(bankingtest.BookPending)
	mutateEUR(e, func(tx *banking.Transaction) bool {
		if tx.EntryReference != bankingtest.RefRent {
			return false
		}
		tx.Description = "RENT OCTOBER (REVISED)"
		return true
	})
	e.clock.now = t0.Add(24 * time.Hour)
	resumeFromOctober()
	e.mustRefresh()
	e.p.Mutate(func(d *bankingtest.Dataset) {
		d.Accounts[0].Transactions = slices.DeleteFunc(d.Accounts[0].Transactions, func(tx banking.Transaction) bool { return tx.EntryReference == bankingtest.RefRent })
	})
	e.clock.now = t0.Add(48 * time.Hour)
	resumeFromOctober()
	e.mustRefresh()

	if n := e.count("test_user_corrections"); n != 2 {
		t.Fatalf("%d corrections left, want 2", n)
	}
	after := e.snapshot()
	r, ok := after[rent.ID]
	if !ok || r.NoLongerReportedAt.IsZero() || r.Description != "RENT OCTOBER (REVISED)" || r.Countable() {
		t.Errorf("rent row: present %v, gone %v", ok, r.NoLongerReportedAt)
	}
	if p, ok := after[pending.ID]; !ok || p.Status != banking.StatusBooked {
		t.Error("corrected pending row was not booked in place")
	}
	var merchant string
	err := e.db.ReadTx(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT merchant FROM test_user_corrections WHERE transaction_id = ?", rent.ID).Scan(&merchant)
	})
	if err != nil || merchant != "Corrected merchant" {
		t.Errorf("correction = %q (%v)", merchant, err)
	}
}

func TestFailedRefreshKeepsDataAndMarksStale(t *testing.T) {
	e := newEngineEnv(t, banking.ModeApp)
	e.mustRefresh()
	eur := e.eurAccount(e.conn)
	balances, err := e.facts.Balances(e.ctx, eur.ID)
	if err != nil {
		t.Fatal(err)
	}
	txs := e.snapshot()
	if f := e.freshness(eur.ID); f.State != banking.FreshnessFresh || f.Stale {
		t.Fatalf("after success: %+v", f)
	}
	succeeded := e.clock.now

	check := func(t *testing.T, state banking.FreshnessState) {
		t.Helper()
		got, err := e.facts.Balances(e.ctx, eur.ID)
		if err != nil || len(got) != len(balances) {
			t.Errorf("balances after failure: %d (%v), want %d", len(got), err, len(balances))
		}
		for i := range got {
			if i < len(balances) && !got[i].Amount.Identical(balances[i].Amount) {
				t.Errorf("balance %d changed to %v", i, got[i].Amount)
			}
		}
		if len(e.snapshot()) != len(txs) {
			t.Error("transactions changed on a failed refresh")
		}
		f := e.freshness(eur.ID)
		if f.State != state || !f.Stale || !f.LastSuccessAt.Equal(succeeded) || !f.LastAttemptAt.Equal(e.clock.now) {
			t.Errorf("freshness = %+v, want %s", f, state)
		}
	}

	t.Run("outage on the account list", func(t *testing.T) {
		e.clock.now = succeeded.Add(time.Hour)
		outage := bankingtest.Failure(bankingtest.OpAccounts, banking.KindRetryable, "", "unavailable", 0)
		e.p.FailNext(bankingtest.OpAccounts, outage, outage, outage, outage)
		res, err := e.refresh()
		if !errors.Is(err, banking.ErrRetryable) || res.Run.Outcome != banking.RefreshFailed || res.Run.ErrorKind != banking.KindRetryable {
			t.Fatalf("refresh = %v, %+v", err, res.Run)
		}
		if len(e.sleeps) != 3 {
			t.Errorf("%d retries, want 3 (D17)", len(e.sleeps))
		}
		check(t, banking.FreshnessFailed)
	})

	t.Run("balances fail for one account", func(t *testing.T) {
		e.clock.now = succeeded.Add(2 * time.Hour)
		e.p.FailNext(bankingtest.OpBalances, bankingtest.Failure(bankingtest.OpBalances, banking.KindPermanent, "", "bad_account", 0))
		res, err := e.refresh()
		if err == nil || res.Run.Outcome != banking.RefreshPartial || res.Run.AccountsFailed != 1 || res.Run.AccountsSucceeded != 2 {
			t.Fatalf("refresh = %v, %+v", err, res.Run)
		}
		check(t, banking.FreshnessFailed)
	})

	t.Run("rate limit far in the future is not waited for", func(t *testing.T) {
		e.clock.now = succeeded.Add(3 * time.Hour)
		e.sleeps = nil
		e.p.FailNext(bankingtest.OpBalances, bankingtest.Failure(bankingtest.OpBalances, banking.KindRateLimited, "", "ASPSP_RATE_LIMIT_EXCEEDED", 6*time.Hour))
		calls := e.h.balanceCalls
		res, err := e.refresh()
		if !errors.Is(err, banking.ErrRateLimited) || res.Run.Outcome != banking.RefreshFailed || len(e.sleeps) != 0 {
			t.Fatalf("refresh = %v, %+v, sleeps %v", err, res.Run, e.sleeps)
		}
		// The run stops at the rate limit: no other account is fetched.
		if e.h.balanceCalls != calls+1 {
			t.Errorf("%d balance calls after a rate limit", e.h.balanceCalls-calls)
		}
		check(t, banking.FreshnessFailed)
	})

	t.Run("consent revoked", func(t *testing.T) {
		e.clock.now = succeeded.Add(4 * time.Hour)
		e.p.Revoke()
		res, err := e.refresh()
		if !errors.Is(err, banking.ErrReconnectRequired) || res.Run.Outcome != banking.RefreshFailed {
			t.Fatalf("refresh = %v, %+v", err, res.Run)
		}
		check(t, banking.FreshnessReconnectRequired)
		if _, err := e.refresh(); !errors.Is(err, bankimport.ErrNotRefreshable) {
			t.Errorf("refresh of a reconnect-required connection: %v", err)
		}
	})
}

func TestExpiredConsentIsNotFetched(t *testing.T) {
	e := newEngineEnv(t, banking.ModeApp)
	e.mustRefresh()
	e.clock.now = e.conn.ConsentValidUntil.Add(time.Minute)
	calls := len(e.p.Calls())
	res, err := e.refresh()
	if !errors.Is(err, banking.ErrConsentExpired) || res.Run.ErrorCode != "consent_expired" {
		t.Fatalf("refresh = %v, %+v", err, res.Run)
	}
	if len(e.p.Calls()) != calls {
		t.Error("an expired consent still cost a provider call")
	}
	c, _ := e.facts.Connection(e.ctx, e.conn.ID)
	if c.Status != banking.ConnectionReconnectRequired {
		t.Errorf("status = %s", c.Status)
	}
}

func TestRetryableFailuresAreRetriedHonouringRetryAfter(t *testing.T) {
	e := newEngineEnv(t, banking.ModeApp)
	e.p.FailNext(bankingtest.OpBalances,
		bankingtest.Failure(bankingtest.OpBalances, banking.KindRetryable, "", "timeout", 0),
		bankingtest.Failure(bankingtest.OpBalances, banking.KindRateLimited, "", "ASPSP_RATE_LIMIT_EXCEEDED", 30*time.Second))
	e.mustRefresh()
	if !slices.Equal(e.sleeps, []time.Duration{time.Second, 30 * time.Second}) {
		t.Errorf("sleeps = %v", e.sleeps)
	}
	// A rate limit without Retry-After is not retried: the bank's daily
	// budget is small.
	e.sleeps = nil
	e.p.FailNext(bankingtest.OpBalances, bankingtest.Failure(bankingtest.OpBalances, banking.KindRateLimited, "", "ASPSP_RATE_LIMIT_EXCEEDED", 0))
	if _, err := e.refresh(); !errors.Is(err, banking.ErrRateLimited) || len(e.sleeps) != 0 {
		t.Errorf("rate limit without Retry-After: %v, sleeps %v", err, e.sleeps)
	}
}

func TestUnknownBalanceIsNeverZero(t *testing.T) {
	e := newEngineEnv(t, banking.ModeApp)
	e.p.Mutate(func(d *bankingtest.Dataset) {
		d.Accounts[0].Balances = append(d.Accounts[0].Balances, banking.Balance{Type: banking.BalanceExpected, ProviderType: "XPCD"})
	})
	res := e.mustRefresh()
	if res.Run.BalancesRejected != 1 || res.Run.BalancesAdded != 3 {
		t.Errorf("balances: %+v", res.Run.RefreshCounts)
	}
	accs, err := e.facts.Accounts(e.ctx, e.conn.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range accs {
		bals, err := e.facts.Balances(e.ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range bals {
			if b.Type == banking.BalanceExpected || b.Amount.Amount().IsZero() {
				t.Errorf("stored a balance the bank did not report: %s %v", b.Type, b.Amount)
			}
		}
		if a.ExternalID == bankingtest.AccountMulti && len(bals) != 0 {
			t.Errorf("multi-currency account without balances got %d", len(bals))
		}
	}
	// Balances that stop being reported leave the last snapshot in place.
	e.p.Mutate(func(d *bankingtest.Dataset) { d.Accounts[0].Balances = nil })
	e.clock.now = e.clock.now.Add(time.Hour)
	e.mustRefresh()
	bals, _ := e.facts.Balances(e.ctx, e.eurAccount(e.conn).ID)
	if len(bals) != 2 {
		t.Errorf("%d EUR balance snapshots, want the 2 earlier ones", len(bals))
	}
}

func TestAccountsNoLongerReportedAreMarkedAndRestored(t *testing.T) {
	e := newEngineEnv(t, banking.ModeApp)
	e.mustRefresh()
	var removed bankingtest.Account
	e.p.Mutate(func(d *bankingtest.Dataset) {
		removed = d.Accounts[1]
		d.Accounts = slices.Delete(d.Accounts, 1, 2)
	})
	e.clock.now = e.clock.now.Add(time.Hour)
	res := e.mustRefresh()
	if res.Run.AccountsNoLongerReported != 1 || e.count("bank_accounts") != 3 {
		t.Errorf("counts %+v, %d accounts", res.Run.RefreshCounts, e.count("bank_accounts"))
	}
	e.p.Mutate(func(d *bankingtest.Dataset) { d.Accounts = append(d.Accounts, removed) })
	e.clock.now = e.clock.now.Add(time.Hour)
	e.mustRefresh()
	accs, _ := e.facts.Accounts(e.ctx, e.conn.ID)
	for _, a := range accs {
		if !a.NoLongerReportedAt.IsZero() {
			t.Errorf("account %s still marked after it came back", a.ID)
		}
	}
}

func TestFreshnessGoesStaleAfterTheThreshold(t *testing.T) {
	e := newEngineEnv(t, banking.ModeApp)
	e.mustRefresh()
	eur := e.eurAccount(e.conn)
	e.clock.now = e.clock.now.Add(banking.DefaultStaleAfter + time.Minute)
	if f := e.freshness(eur.ID); f.State != banking.FreshnessStale || !f.Stale {
		t.Errorf("after 24h: %+v", f)
	}
}

func TestOneRefreshPerConnection(t *testing.T) {
	e := newEngineEnv(t, banking.ModeApp)
	var inner error
	e.h.onAccounts = func() {
		e.h.onAccounts = nil
		_, inner = e.engine.Refresh(e.ctx, e.conn.ID, e.h)
	}
	e.mustRefresh()
	if !errors.Is(inner, bankimport.ErrRefreshInProgress) {
		t.Errorf("concurrent refresh: %v", inner)
	}
}

func TestSecondEngineCannotRefreshWhileARunIsLive(t *testing.T) {
	e := newEngineEnv(t, banking.ModeApp)
	other := bankimport.New(e.facts, bankimport.Options{Now: e.clock.Now})
	var inner error
	e.h.onAccounts = func() {
		e.h.onAccounts = nil
		_, inner = other.Refresh(e.ctx, e.conn.ID, e.h)
	}
	res := e.mustRefresh()
	if !errors.Is(inner, bankimport.ErrRefreshInProgress) {
		t.Fatalf("second engine: %v", inner)
	}
	runs, err := e.facts.RefreshRuns(e.ctx, e.conn.ID, 0)
	if err != nil || len(runs) != 1 || runs[0].ID != res.Run.ID || runs[0].Outcome != banking.RefreshSucceeded {
		t.Errorf("runs = %+v, %v", runs, err)
	}
	// Once the run finished the other engine may refresh.
	if _, err := other.Refresh(e.ctx, e.conn.ID, e.h); err != nil {
		t.Errorf("second engine after the first finished: %v", err)
	}
}

func TestInvalidTransactionIsDroppedNotFatal(t *testing.T) {
	e := newEngineEnv(t, banking.ModeApp)
	e.mustRefresh()
	eur := e.eurAccount(e.conn)
	// The bank now sends a row without a provider status, dated inside the
	// overlap, plus a valid new transaction; and a stored pending row turns
	// invalid too.
	e.p.Mutate(func(d *bankingtest.Dataset) {
		a := &d.Accounts[0]
		a.Transactions = append(a.Transactions, banking.Transaction{
			EntryReference: "bad-row", Status: banking.StatusBooked, Amount: money.MustParse("-1.00", "EUR"),
			BookingDate: calendar.MustDate(2026, time.October, 30), Description: "BROKEN",
		})
		for i := range a.Transactions {
			if a.Transactions[i].EntryReference == bankingtest.RefPendingThenBooks {
				a.Transactions[i].ProviderStatus = ""
			}
		}
	})
	for i := range 3 {
		e.p.Mutate(func(d *bankingtest.Dataset) {
			d.Accounts[0].Transactions = append(d.Accounts[0].Transactions, banking.Transaction{
				EntryReference: "new-" + string(rune('a'+i)), Status: banking.StatusBooked, ProviderStatus: "BOOK",
				Amount: money.MustParse("-2.00", "EUR"), BookingDate: calendar.MustDate(2026, time.October, 31), Description: "NEW",
			})
		})
		e.clock.now = e.clock.now.Add(time.Hour)
		res, err := e.refresh()
		if err != nil || res.Run.Outcome != banking.RefreshSucceeded {
			t.Fatalf("refresh %d: %v, %+v", i, err, res.Run)
		}
		wantRejected := 2
		if res.Run.TransactionsRejected != wantRejected || res.Run.TransactionsInserted != 1 {
			t.Fatalf("refresh %d counts = %+v", i, res.Run.RefreshCounts)
		}
		s := e.sync(eur.ID)
		if s.LastNoticeCode != banking.NoticeTransactionsRejected || s.LastErrorKind != "" {
			t.Errorf("refresh %d sync = %+v", i, s)
		}
		if f := e.freshness(eur.ID); f.NoticeCode != banking.NoticeTransactionsRejected {
			t.Errorf("freshness notice = %q", f.NoticeCode)
		}
	}
	if _, ok := byRef(e.eurTransactions(e.conn), "bad-row"); ok {
		t.Error("an invalid row was stored")
	}
	pending, _ := byRef(e.eurTransactions(e.conn), bankingtest.RefPendingThenBooks)
	if !pending.NoLongerReportedAt.IsZero() {
		t.Error("a row whose new version failed validation was marked no longer reported")
	}
	// The bank corrects itself: a clean refresh clears the notice.
	e.p.Mutate(func(d *bankingtest.Dataset) {
		a := &d.Accounts[0]
		a.Transactions = slices.DeleteFunc(a.Transactions, func(t banking.Transaction) bool { return t.EntryReference == "bad-row" })
		for i := range a.Transactions {
			if a.Transactions[i].EntryReference == bankingtest.RefPendingThenBooks {
				a.Transactions[i].ProviderStatus = "PDNG"
			}
		}
	})
	e.clock.now = e.clock.now.Add(time.Hour)
	e.mustRefresh()
	if s := e.sync(eur.ID); s.LastNoticeCode != "" {
		t.Errorf("notice after a clean refresh = %q", s.LastNoticeCode)
	}
}

func TestBookedRowLinksAnAlreadyUnreportedHold(t *testing.T) {
	const ref = "fake-ref-hold-0003"
	hold := banking.Transaction{
		EntryReference: ref, Status: banking.StatusPending, ProviderStatus: "PDNG",
		Amount: money.MustParse("-40.00", "EUR"), TransactionDate: calendar.MustDate(2026, time.October, 29), Description: "FUEL HOLD",
	}
	booked := banking.Transaction{
		EntryReference: ref, Status: banking.StatusBooked, ProviderStatus: "BOOK",
		Amount: money.MustParse("-47.50", "EUR"), BookingDate: calendar.MustDate(2026, time.October, 31), Description: "FUEL",
	}
	find := func(e *engineEnv) (old, now banking.StoredTransaction) {
		for _, tx := range e.eurTransactions(e.conn) {
			switch {
			case tx.EntryReference == ref && tx.Status == banking.StatusPending:
				old = tx
			case tx.EntryReference == ref:
				now = tx
			}
		}
		return old, now
	}

	t.Run("hold disappears before the booking appears", func(t *testing.T) {
		e := newEngineEnv(t, banking.ModeApp)
		e.p.Mutate(func(d *bankingtest.Dataset) { d.Accounts[0].Transactions = append(d.Accounts[0].Transactions, hold) })
		e.mustRefresh()
		e.p.Mutate(func(d *bankingtest.Dataset) {
			d.Accounts[0].Transactions = slices.DeleteFunc(d.Accounts[0].Transactions, func(t banking.Transaction) bool { return t.EntryReference == ref })
		})
		e.clock.now = e.clock.now.Add(time.Hour)
		e.mustRefresh()
		if old, _ := find(e); old.NoLongerReportedAt.IsZero() || old.SupersededBy != "" {
			t.Fatalf("hold = gone %v, superseded %q", old.NoLongerReportedAt, old.SupersededBy)
		}
		e.p.Mutate(func(d *bankingtest.Dataset) { d.Accounts[0].Transactions = append(d.Accounts[0].Transactions, booked) })
		e.clock.now = e.clock.now.Add(time.Hour)
		res := e.mustRefresh()
		old, now := find(e)
		if now.ID == "" || old.SupersededBy != now.ID || res.Run.TransactionsSuperseded != 1 || old.Countable() || !now.Countable() {
			t.Errorf("old superseded by %q, booked %q, counts %+v", old.SupersededBy, now.ID, res.Run.RefreshCounts)
		}
		// A further refresh changes nothing.
		e.clock.now = e.clock.now.Add(time.Hour)
		if res := e.mustRefresh(); res.Run.TransactionsSuperseded != 0 {
			t.Errorf("relinked: %+v", res.Run.RefreshCounts)
		}
	})

	t.Run("hold and booking are in different windows", func(t *testing.T) {
		e := newEngineEnv(t, banking.ModeApp)
		e.mustRefresh()
		eur := e.eurAccount(e.conn)
		window := func(from, to int, txs ...banking.Transaction) banking.WindowResult {
			t.Helper()
			keyed, err := banking.KeyTransactions(txs)
			if err != nil {
				t.Fatal(err)
			}
			res, err := e.facts.ImportTransactionWindow(e.ctx, eur.ID, banking.TransactionWindow{
				From: calendar.MustDate(2026, time.October, from), To: calendar.MustDate(2026, time.October, to), Transactions: keyed,
			}, e.clock.now)
			if err != nil {
				t.Fatal(err)
			}
			return res
		}
		window(29, 29, hold)
		window(29, 29) // the hold stops being reported
		res := window(30, 31, booked)
		old, now := find(e)
		if res.Superseded != 1 || old.SupersededBy != now.ID || now.ID == "" {
			t.Errorf("superseded %d, old linked to %q, booked %q", res.Superseded, old.SupersededBy, now.ID)
		}
	})
}

func TestLateLinkIsBoundedByDate(t *testing.T) {
	const ref = "fake-ref-bound-0001"
	pend := func(day int, month time.Month) banking.Transaction {
		return banking.Transaction{EntryReference: ref, Status: banking.StatusPending, ProviderStatus: "PDNG",
			Amount: money.MustParse("-40.00", "EUR"), TransactionDate: calendar.MustDate(2026, month, day), Description: "HOLD"}
	}
	book := func(day int, month time.Month) banking.Transaction {
		return banking.Transaction{EntryReference: ref, Status: banking.StatusBooked, ProviderStatus: "BOOK",
			Amount: money.MustParse("-47.50", "EUR"), BookingDate: calendar.MustDate(2026, month, day), Description: "PAID"}
	}
	for _, tc := range []struct {
		name         string
		hold, booked banking.Transaction
		want         int
	}{
		{"booking within the bound", pend(1, time.October), book(20, time.October), 1},
		{"hold older than the bound", pend(1, time.March), book(30, time.October), 0},
		{"booking dated before its hold", pend(25, time.October), book(6, time.October), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEngineEnv(t, banking.ModeApp)
			e.mustRefresh()
			eur := e.eurAccount(e.conn)
			window := func(txs ...banking.Transaction) banking.WindowResult {
				t.Helper()
				keyed, err := banking.KeyTransactions(txs)
				if err != nil {
					t.Fatal(err)
				}
				res, err := e.facts.ImportTransactionWindow(e.ctx, eur.ID, banking.TransactionWindow{
					From: tc.hold.TransactionDate, To: tc.hold.TransactionDate, Transactions: keyed,
				}, e.clock.now)
				if err != nil {
					t.Fatal(err)
				}
				return res
			}
			window(tc.hold)
			window() // the hold stops being reported
			keyed, err := banking.KeyTransactions([]banking.Transaction{tc.booked})
			if err != nil {
				t.Fatal(err)
			}
			res, err := e.facts.ImportTransactionWindow(e.ctx, eur.ID, banking.TransactionWindow{
				From: calendar.MustDate(2026, time.October, 6), To: calendar.MustDate(2026, time.October, 31), Transactions: keyed,
			}, e.clock.now)
			if err != nil {
				t.Fatal(err)
			}
			if res.Superseded != tc.want {
				t.Errorf("superseded %d, want %d", res.Superseded, tc.want)
			}
		})
	}
}

func TestLateLinkNeedsExactlyOneHold(t *testing.T) {
	const ref = "fake-ref-two-holds"
	e := newEngineEnv(t, banking.ModeApp)
	e.mustRefresh()
	eur := e.eurAccount(e.conn)
	day := calendar.MustDate(2026, time.October, 10)
	hold := func(amount string) banking.Transaction {
		return banking.Transaction{EntryReference: ref, Status: banking.StatusPending, ProviderStatus: "PDNG",
			Amount: money.MustParse(amount, "EUR"), TransactionDate: day, Description: "HOLD"}
	}
	window := func(txs ...banking.Transaction) banking.WindowResult {
		keyed, err := banking.KeyTransactions(txs)
		if err != nil {
			t.Fatal(err)
		}
		res, err := e.facts.ImportTransactionWindow(e.ctx, eur.ID, banking.TransactionWindow{From: day, To: day, Transactions: keyed}, e.clock.now)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	window(hold("-10.00"), hold("-20.00"))
	window()
	res := window(banking.Transaction{EntryReference: ref, Status: banking.StatusBooked, ProviderStatus: "BOOK",
		Amount: money.MustParse("-30.00", "EUR"), BookingDate: day, Description: "PAID"})
	if res.Superseded != 0 {
		t.Errorf("two holds were linked to one booking: %d", res.Superseded)
	}
}

func TestRefreshNeedsTheConnectionsProvider(t *testing.T) {
	e := newEngineEnv(t, banking.ModeApp)
	other := bankingtest.New(bankingtest.Options{Mode: banking.ModeDeveloper, Now: e.clock.Now}, bankingtest.Standard())
	if _, err := e.engine.Refresh(e.ctx, e.conn.ID, other); err == nil {
		t.Error("refreshed through another provider application")
	}
}

func TestPurgeRemovesRefreshBookkeeping(t *testing.T) {
	e := newEngineEnv(t, banking.ModeApp)
	e.mustRefresh()
	if _, err := e.facts.PurgeConnection(e.ctx, e.conn.ID, e.version(e.conn)); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"bank_refresh_runs", "bank_account_sync", "bank_transactions", "bank_accounts", "bank_connections"} {
		if n := e.count(table); n != 0 {
			t.Errorf("%s has %d rows after purge", table, n)
		}
	}
}
