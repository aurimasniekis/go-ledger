// Package bankimport is the import engine (task B2, ADR-0019): it refreshes
// one bank connection from its provider into the bank-fact store.
//
// A refresh lists the connection's accounts, then for each account fetches
// its balances and every page of its transaction windows, and stores them
// through [banking.FactStore]:
//
//   - Idempotent: accounts are upserted by (provider, external id) and
//     transactions by their idempotency key ([banking.KeyTransactions]), so
//     repeating or overlapping a window changes nothing that did not change
//     at the bank.
//   - Windows cover whole days and are keyed with all their pages together.
//     A window is stored in one write transaction, or not at all, so an
//     interrupted refresh leaves only complete windows behind; the next one
//     resumes from the last complete window minus an overlap.
//   - Pending to booked: a transaction that keeps its entry reference is
//     booked in place; one that the bank re-reports under a new key leaves
//     its pending row unreported in a later window, which is then marked
//     no longer reported (decision D8: never deleted) and, when exactly one
//     booked transaction of the window has the same entry reference, linked
//     to it with superseded_by.
//   - Failures keep the last good data: balance snapshots are append-only,
//     a failed fetch writes nothing, an unknown balance is dropped (never
//     stored as zero), and every account records its last attempt, last
//     success and last error so stale data is visible
//     ([banking.AccountFreshness]).
//   - Bank facts only: the engine writes no user data, and user corrections
//     live in their own tables referring to bank facts, so a refresh cannot
//     erase them.
//
// Provider calls happen outside write transactions. Retries follow decision
// D17: only for these idempotent reads, at most three, with exponential
// backoff and jitter, honouring Retry-After.
package bankimport

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
)

// JobName is the background-job name in logs and metrics.
const JobName = "bank_refresh"

// Defaults for [Options].
const (
	DefaultOverlapDays        = 7
	DefaultInitialHistoryDays = 90
	DefaultChunkDays          = 31
	DefaultMaxPages           = 1000
	DefaultMaxRetries         = 3
	DefaultBaseBackoff        = time.Second
	DefaultMaxRetryWait       = time.Minute
)

// ErrRefreshInProgress is returned when the connection is already being
// refreshed by this engine.
var ErrRefreshInProgress = errors.New("bankimport: a refresh of this connection is already running")

// ErrNotRefreshable is returned for a connection that is not active.
var ErrNotRefreshable = errors.New("bankimport: connection is not active")

// Meter records job outcomes (internal/metrics implements it).
type Meter interface {
	JobDone(job string, ok bool, took time.Duration)
}

// Options configure an [Engine]. Zero values take the defaults.
type Options struct {
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Sleep waits between retries; nil waits on a timer and stops early
	// when ctx ends.
	Sleep func(ctx context.Context, d time.Duration) error
	// Jitter spreads a backoff delay; nil picks uniformly from [d/2, d].
	Jitter func(d time.Duration) time.Duration
	Log    *slog.Logger
	Meter  Meter

	// OverlapDays is how many days before the last complete window a
	// refresh fetches again, to pick up late bookings and status changes.
	OverlapDays int
	// InitialHistoryDays is how far back the first refresh of an account
	// reaches, capped by the provider's MaxHistoryDays when known. It also
	// bounds how far back a refresh reaches to resolve an old pending item.
	InitialHistoryDays int
	// ChunkDays splits long windows (a first import) into windows of at most
	// this many days, each stored on its own, oldest first, so an
	// interruption keeps what was finished.
	ChunkDays int
	// MaxPages bounds the pages of one window; a provider that never ends a
	// listing fails the window instead of looping.
	MaxPages int
	// MaxRetries bounds retries of one provider call (D17: 3).
	MaxRetries int
	// BaseBackoff is the first retry delay; it doubles per retry.
	BaseBackoff time.Duration
	// MaxRetryWait is the longest delay the engine waits in a run; a
	// provider asking for more (Retry-After) ends the call with its error.
	MaxRetryWait time.Duration
}

// Engine refreshes connections. It is safe for concurrent use; one
// connection is refreshed by at most one call at a time.
type Engine struct {
	facts banking.FactStore
	opts  Options
	log   *slog.Logger

	mu      sync.Mutex
	running map[banking.ConnectionID]bool
}

// New returns an engine storing into facts.
func New(facts banking.FactStore, opts Options) *Engine {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Sleep == nil {
		opts.Sleep = sleep
	}
	if opts.Jitter == nil {
		opts.Jitter = jitter
	}
	if opts.OverlapDays <= 0 {
		opts.OverlapDays = DefaultOverlapDays
	}
	if opts.InitialHistoryDays <= 0 {
		opts.InitialHistoryDays = DefaultInitialHistoryDays
	}
	if opts.ChunkDays <= 0 {
		opts.ChunkDays = DefaultChunkDays
	}
	if opts.MaxPages <= 0 {
		opts.MaxPages = DefaultMaxPages
	}
	if opts.MaxRetries <= 0 {
		opts.MaxRetries = DefaultMaxRetries
	}
	if opts.BaseBackoff <= 0 {
		opts.BaseBackoff = DefaultBaseBackoff
	}
	if opts.MaxRetryWait <= 0 {
		opts.MaxRetryWait = DefaultMaxRetryWait
	}
	log := opts.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Engine{facts: facts, opts: opts, log: log, running: map[banking.ConnectionID]bool{}}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func jitter(d time.Duration) time.Duration {
	if d <= 1 {
		return d
	}
	return d/2 + rand.N(d/2+1)
}

// AccountResult is the outcome for one reported account.
type AccountResult struct {
	ID banking.AccountID
	// Err is the first failure of the account's refresh; nil on success.
	Err    error
	Counts banking.RefreshCounts
}

// Result is the outcome of one refresh.
type Result struct {
	Run      banking.RefreshRun
	Accounts []AccountResult
}

// Refresh refreshes connection id through p, which must be the provider
// application the connection was made with. It returns the finished run.
// The error is nil only when the run succeeded; otherwise it is the first
// failure (classify it with [banking.Classify]), and the run says what was
// stored anyway. A connection that is not active returns
// [ErrNotRefreshable] without a run.
func (e *Engine) Refresh(ctx context.Context, id banking.ConnectionID, p banking.Provider) (Result, error) {
	conn, err := e.facts.Connection(ctx, id)
	if err != nil {
		return Result{}, err
	}
	caps := p.Capabilities()
	if caps.Provider != conn.Provider || caps.Application != conn.Application {
		return Result{}, fmt.Errorf("bankimport: connection %s belongs to another provider application", id)
	}
	if conn.Status != banking.ConnectionActive {
		return Result{}, fmt.Errorf("%w (%s)", ErrNotRefreshable, conn.Status)
	}
	if !e.lock(id) {
		return Result{}, ErrRefreshInProgress
	}
	defer e.unlock(id)

	start := time.Now()
	run, err := e.facts.StartRefresh(ctx, id, e.now())
	if err != nil {
		return Result{}, err
	}
	jl := e.log.With("job_id", string(run.ID), "job", JobName, "connection_id", string(id))
	r := &refresh{e: e, ctx: ctx, conn: conn, caps: caps, p: p, log: jl}
	res, cause := r.run()

	// Bookkeeping outlives a cancelled refresh: the run must not stay
	// "running".
	book := context.WithoutCancel(ctx)
	run.RefreshCounts = res.counts()
	run.Outcome, run.FinishedAt = r.outcome(res, cause), e.now()
	if cause != nil {
		run.ErrorKind = banking.Classify(cause)
		if be, ok := errors.AsType[*banking.Error](cause); ok {
			run.ErrorCode = be.Code
		}
	}
	finished, ferr := e.facts.FinishRefresh(book, run)
	if ferr != nil {
		jl.Error("recording the refresh outcome failed", "error", ferr)
		finished = run
	}
	res.Run = finished
	if e.opts.Meter != nil {
		e.opts.Meter.JobDone(JobName, finished.Outcome == banking.RefreshSucceeded, time.Since(start))
	}
	attrs := []any{"outcome", finished.Outcome, "accounts", finished.AccountsReported, "failed_accounts", finished.AccountsFailed,
		"inserted", finished.TransactionsInserted, "updated", finished.TransactionsUpdated, "no_longer_reported", finished.TransactionsNoLongerReported}
	if cause != nil {
		jl.Warn("bank refresh did not complete", append(attrs, "error_kind", run.ErrorKind, "error", cause)...)
	} else {
		jl.Info("bank refresh finished", attrs...)
	}
	return res, errors.Join(cause, ferr)
}

func (e *Engine) lock(id banking.ConnectionID) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running[id] {
		return false
	}
	e.running[id] = true
	return true
}

func (e *Engine) unlock(id banking.ConnectionID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.running, id)
}

func (e *Engine) now() time.Time { return e.opts.Now().UTC() }

func (r *Result) counts() banking.RefreshCounts {
	var c banking.RefreshCounts
	c.Add(r.Run.RefreshCounts)
	for _, a := range r.Accounts {
		c.Add(a.Counts)
	}
	return c
}

// refresh is one run.
type refresh struct {
	e    *Engine
	ctx  context.Context
	conn banking.Connection
	caps banking.Capabilities
	p    banking.Provider
	log  *slog.Logger
	// listed is false when the account list could not be fetched.
	listed bool
}

// stops reports whether an error must end the whole run: the session is
// gone, the provider asked to back off, or Ledger is shutting down.
func stops(err error) bool {
	switch banking.Classify(err) {
	case banking.KindReconnectRequired, banking.KindRateLimited, banking.KindCanceled:
		return true
	}
	return false
}

func (r *refresh) outcome(res Result, cause error) banking.RefreshOutcome {
	c := res.counts()
	switch {
	case cause == nil:
		return banking.RefreshSucceeded
	case banking.Classify(cause) == banking.KindCanceled:
		return banking.RefreshInterrupted
	case r.listed && c.AccountsSucceeded > 0:
		return banking.RefreshPartial
	default:
		return banking.RefreshFailed
	}
}

func (r *refresh) run() (Result, error) {
	var res Result
	book := context.WithoutCancel(r.ctx)
	now := r.e.now()

	var accounts []banking.Account
	var err error
	if !r.conn.ConsentValidUntil.IsZero() && !now.Before(r.conn.ConsentValidUntil) {
		// Known to be expired: no provider call, which would only spend
		// one of the bank's few daily background fetches.
		err = &banking.Error{Kind: banking.KindReconnectRequired, Reason: banking.ReasonConsentExpired, Provider: r.conn.Provider, Op: "refresh", Code: "consent_expired"}
	} else {
		err = r.e.call(r.ctx, func() error {
			var cerr error
			accounts, cerr = r.p.Accounts(r.ctx, r.conn.Session)
			return cerr
		})
	}
	if err != nil {
		return res, r.connectionFailed(book, err, now)
	}
	r.listed = true
	seenAt := r.e.now()
	ups, err := r.e.facts.UpsertAccounts(r.ctx, r.conn.ID, accounts, seenAt)
	if err != nil {
		return res, r.connectionFailed(book, err, now)
	}
	ids := make([]banking.AccountID, len(ups))
	for i, u := range ups {
		ids[i] = u.ID
	}
	gone, err := r.e.facts.MarkAccountsNoLongerReported(r.ctx, r.conn.ID, ids, seenAt)
	if err != nil {
		return res, r.connectionFailed(book, err, now)
	}
	res.Run.AccountsReported, res.Run.AccountsNoLongerReported = len(accounts), gone

	var first, stop error
	for i, a := range accounts {
		ar := AccountResult{ID: ids[i]}
		if stop != nil {
			// Not attempted: the run had to stop. The account still shows
			// that this refresh did not bring it up to date.
			ar.Err = stop
			if _, err := r.e.facts.BeginAccountRefresh(book, ar.ID, now); err != nil {
				r.log.Error("recording the refresh attempt failed", "account_id", string(ar.ID), "error", err)
			}
			r.recordFailure(book, ar.ID, stop)
		} else {
			ar.Err = r.account(ar.ID, a, &ar.Counts)
			if ar.Err != nil && stops(ar.Err) {
				stop = ar.Err
			}
		}
		if ar.Err != nil {
			ar.Counts.AccountsFailed++
			if first == nil {
				first = ar.Err
			}
		} else {
			ar.Counts.AccountsSucceeded++
		}
		res.Accounts = append(res.Accounts, ar)
	}
	if stop != nil {
		r.updateStatus(book, stop)
	}
	return res, first
}

// connectionFailed records a failure before any account could be refreshed:
// every stored account of the connection gets the failure (its data stays
// and shows as failed), and a reconnect-required failure changes the
// connection status.
func (r *refresh) connectionFailed(book context.Context, cause error, at time.Time) error {
	r.updateStatus(book, cause)
	stored, err := r.e.facts.Accounts(book, r.conn.ID)
	if err != nil {
		return errors.Join(cause, err)
	}
	for _, a := range stored {
		if !a.NoLongerReportedAt.IsZero() {
			continue
		}
		if _, err := r.e.facts.BeginAccountRefresh(book, a.ID, at); err != nil {
			r.log.Error("recording the refresh attempt failed", "account_id", string(a.ID), "error", err)
			continue
		}
		r.recordFailure(book, a.ID, cause)
	}
	return cause
}

func (r *refresh) updateStatus(book context.Context, cause error) {
	if status, ok := banking.StatusAfter(cause); ok {
		if _, err := r.e.facts.SetConnectionStatus(book, r.conn.ID, status, cause, r.e.now()); err != nil {
			r.log.Error("recording the connection status failed", "error", err)
		}
	}
}

func (r *refresh) recordFailure(book context.Context, id banking.AccountID, cause error) {
	if _, err := r.e.facts.EndAccountRefresh(book, id, cause, r.e.now()); err != nil {
		r.log.Error("recording the account refresh failure failed", "account_id", string(id), "error", err)
	}
}

// account refreshes one account: balances, then its transaction windows,
// oldest first. It returns the first failure; a failure of the balances
// does not skip the transactions unless it ends the run.
func (r *refresh) account(id banking.AccountID, a banking.Account, c *banking.RefreshCounts) error {
	book := context.WithoutCancel(r.ctx)
	sync, err := r.e.facts.BeginAccountRefresh(r.ctx, id, r.e.now())
	if err != nil {
		return err
	}
	first := r.balances(id, a, c)
	if first == nil || !stops(first) {
		if err := r.transactions(id, a, sync, c); err != nil && first == nil {
			first = err
		}
	}
	if _, err := r.e.facts.EndAccountRefresh(book, id, first, r.e.now()); err != nil {
		return errors.Join(first, err)
	}
	return first
}

func (r *refresh) balances(id banking.AccountID, a banking.Account, c *banking.RefreshCounts) error {
	var bals []banking.Balance
	err := r.e.call(r.ctx, func() error {
		var cerr error
		bals, cerr = r.p.Balances(r.ctx, r.conn.Session, a.Handle)
		return cerr
	})
	if err != nil {
		return err
	}
	obtainedAt := r.e.now()
	valid := bals[:0:0]
	for _, b := range bals {
		// A balance without a usable amount is unknown: drop it. Storing
		// zero would claim a balance the bank never reported.
		if b.Validate() != nil {
			c.BalancesRejected++
			continue
		}
		valid = append(valid, b)
	}
	if len(valid) == 0 {
		return nil
	}
	if err := r.e.facts.AddBalances(r.ctx, id, valid, obtainedAt); err != nil {
		return err
	}
	c.BalancesAdded += len(valid)
	return nil
}

func (r *refresh) transactions(id banking.AccountID, a banking.Account, sync banking.AccountSync, c *banking.RefreshCounts) error {
	// Bank dates carry no zone; the window ends on today's UTC date, and an
	// item dated later (a bank ahead of UTC) arrives with the next refresh.
	now := r.e.now()
	today, err := calendar.NewDate(now.Year(), now.Month(), now.Day())
	if err != nil {
		return err
	}
	ws, err := r.e.Windows(today, sync, r.caps.MaxHistoryDays)
	if err != nil {
		return err
	}
	for _, w := range ws {
		txs, err := r.fetchWindow(a, w)
		if err != nil {
			return err
		}
		keyed, err := banking.KeyTransactions(txs)
		if err != nil {
			return &banking.Error{Kind: banking.KindPermanent, Provider: r.conn.Provider, Op: "transactions", Code: "invalid_transaction", Err: err}
		}
		wr, err := r.e.facts.ImportTransactionWindow(r.ctx, id, banking.TransactionWindow{From: w.From, To: w.To, Transactions: keyed}, r.e.now())
		if err != nil {
			return err
		}
		for _, u := range wr.Upserts {
			switch u.Outcome {
			case banking.OutcomeInserted:
				c.TransactionsInserted++
			case banking.OutcomeUpdated:
				c.TransactionsUpdated++
			case banking.OutcomeUnchanged:
				c.TransactionsUnchanged++
			case banking.OutcomeKeptBooked:
				c.TransactionsKeptBooked++
			}
		}
		c.TransactionsNoLongerReported += wr.NoLongerReported
		c.TransactionsSuperseded += wr.Superseded
	}
	return nil
}

// fetchWindow fetches every page of one window. Nothing of a window is
// stored until all its pages are in, so the keys' ordinals are computed over
// the whole window.
func (r *refresh) fetchWindow(a banking.Account, w Window) ([]banking.Transaction, error) {
	q := banking.TransactionQuery{From: w.From, To: w.To}
	var all []banking.Transaction
	for pages := 0; ; pages++ {
		if pages >= r.e.opts.MaxPages {
			return nil, &banking.Error{Kind: banking.KindPermanent, Provider: r.conn.Provider, Op: "transactions", Code: "too_many_pages"}
		}
		var page banking.TransactionPage
		err := r.e.call(r.ctx, func() error {
			var cerr error
			page, cerr = r.p.Transactions(r.ctx, r.conn.Session, a.Handle, q)
			return cerr
		})
		if err != nil {
			return nil, err
		}
		all = append(all, page.Transactions...)
		if page.Next == "" {
			return all, nil
		}
		if page.Next == q.Continuation {
			return nil, &banking.Error{Kind: banking.KindPermanent, Provider: r.conn.Provider, Op: "transactions", Code: "continuation_repeated"}
		}
		q.Continuation = page.Next
	}
}

// call runs one idempotent provider read with the D17 retry policy:
// retryable failures, and rate limits that name a Retry-After, are retried
// at most MaxRetries times with exponential backoff and jitter, never
// sooner than Retry-After and never waiting longer than MaxRetryWait.
func (e *Engine) call(ctx context.Context, fn func() error) error {
	for attempt := 0; ; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		kind := banking.Classify(err)
		retryAfter, hasRetryAfter := banking.RetryAfter(err)
		if attempt >= e.opts.MaxRetries || (kind != banking.KindRetryable && (kind != banking.KindRateLimited || !hasRetryAfter)) {
			return err
		}
		wait := e.opts.Jitter(e.opts.BaseBackoff << attempt)
		if retryAfter > wait {
			wait = retryAfter
		}
		if wait > e.opts.MaxRetryWait {
			return err
		}
		if serr := e.opts.Sleep(ctx, wait); serr != nil {
			return serr
		}
	}
}

// Window is an inclusive range of whole days.
type Window struct{ From, To calendar.Date }

// Windows returns the transaction windows to fetch for an account on today,
// oldest first, each at most ChunkDays long:
//
//   - the first refresh reaches back InitialHistoryDays;
//   - later ones resume OverlapDays before TransactionsThrough;
//   - either reaches back further to the oldest open pending transaction,
//     but no further than InitialHistoryDays;
//   - nothing reaches back beyond maxHistoryDays (the provider's limit,
//     0 if unknown).
func (e *Engine) Windows(today calendar.Date, s banking.AccountSync, maxHistoryDays int) ([]Window, error) {
	back := func(days int) (calendar.Date, error) { return today.AddDays(-(days - 1)) }
	initial, err := back(e.opts.InitialHistoryDays)
	if err != nil {
		return nil, err
	}
	from := initial
	if !s.TransactionsThrough.IsZero() {
		if from, err = s.TransactionsThrough.AddDays(-e.opts.OverlapDays); err != nil {
			return nil, err
		}
	}
	if p := s.OldestOpenPending; !p.IsZero() && p.Before(from) {
		from = p
		if from.Before(initial) {
			from = initial
		}
	}
	if maxHistoryDays > 0 {
		floor, err := back(maxHistoryDays)
		if err != nil {
			return nil, err
		}
		if from.Before(floor) {
			from = floor
		}
	}
	if from.After(today) {
		from = today
	}
	var out []Window
	for !from.After(today) {
		to, err := from.AddDays(e.opts.ChunkDays - 1)
		if err != nil {
			return nil, err
		}
		if to.After(today) {
			to = today
		}
		out = append(out, Window{From: from, To: to})
		if from, err = to.AddDays(1); err != nil {
			return nil, err
		}
	}
	return out, nil
}
