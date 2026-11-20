package banking

import (
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
)

// DefaultStaleAfter is how old the last successful refresh of an account may
// be before its data counts as stale (decision D17: 24 hours).
const DefaultStaleAfter = 24 * time.Hour

// RefreshRunID identifies one refresh attempt of a connection.
type RefreshRunID string

// RefreshOutcome is how a refresh run ended.
type RefreshOutcome string

const (
	// RefreshRunning: the run has not finished.
	RefreshRunning RefreshOutcome = "running"
	// RefreshSucceeded: the account list and every reported account were
	// refreshed.
	RefreshSucceeded RefreshOutcome = "succeeded"
	// RefreshPartial: some accounts were refreshed and some failed.
	RefreshPartial RefreshOutcome = "partial"
	// RefreshFailed: nothing was refreshed (the account list failed, or
	// every account did).
	RefreshFailed RefreshOutcome = "failed"
	// RefreshInterrupted: the run was cancelled, or the process stopped
	// while it was running and a later run found it unfinished. Whatever it
	// committed stays; the next run resumes from it.
	RefreshInterrupted RefreshOutcome = "interrupted"
)

// Valid reports whether o is a known outcome.
func (o RefreshOutcome) Valid() bool {
	switch o {
	case RefreshRunning, RefreshSucceeded, RefreshPartial, RefreshFailed, RefreshInterrupted:
		return true
	}
	return false
}

// RefreshCounts says what a refresh run changed.
type RefreshCounts struct {
	AccountsReported         int
	AccountsSucceeded        int
	AccountsFailed           int
	AccountsNoLongerReported int
	BalancesAdded            int
	// BalancesRejected counts balances the provider reported without a
	// usable amount and currency. They are dropped, never stored as zero.
	BalancesRejected             int
	TransactionsInserted         int
	TransactionsUpdated          int
	TransactionsUnchanged        int
	TransactionsKeptBooked       int
	TransactionsNoLongerReported int
	TransactionsSuperseded       int
}

// Add adds o to c.
func (c *RefreshCounts) Add(o RefreshCounts) {
	c.AccountsReported += o.AccountsReported
	c.AccountsSucceeded += o.AccountsSucceeded
	c.AccountsFailed += o.AccountsFailed
	c.AccountsNoLongerReported += o.AccountsNoLongerReported
	c.BalancesAdded += o.BalancesAdded
	c.BalancesRejected += o.BalancesRejected
	c.TransactionsInserted += o.TransactionsInserted
	c.TransactionsUpdated += o.TransactionsUpdated
	c.TransactionsUnchanged += o.TransactionsUnchanged
	c.TransactionsKeptBooked += o.TransactionsKeptBooked
	c.TransactionsNoLongerReported += o.TransactionsNoLongerReported
	c.TransactionsSuperseded += o.TransactionsSuperseded
}

// RefreshRun is one refresh attempt of a connection.
type RefreshRun struct {
	ID           RefreshRunID
	ConnectionID ConnectionID
	StartedAt    time.Time
	// FinishedAt is zero while the run is running.
	FinishedAt time.Time
	Outcome    RefreshOutcome
	// ErrorKind and ErrorCode classify the failure that ended or degraded
	// the run ("" for a success). They hold no payload.
	ErrorKind ErrorKind
	ErrorCode string
	RefreshCounts
}

// AccountSync is the refresh state of one account.
type AccountSync struct {
	AccountID AccountID
	// LastAttemptAt is when the latest refresh of the account started; zero
	// if it was never refreshed.
	LastAttemptAt time.Time
	// LastSuccessAt is when a refresh of the account last completed without
	// an error; zero if none did.
	LastSuccessAt time.Time
	// LastErrorKind and LastErrorCode describe the latest attempt's failure;
	// a success clears them.
	LastErrorKind ErrorKind
	LastErrorCode string
	// TransactionsThrough is the last day of the newest transaction window
	// imported completely; zero if none was.
	TransactionsThrough calendar.Date
	// OldestOpenPending is the date of the oldest pending transaction that
	// is still reported and not superseded; zero if there is none. A
	// refresh window reaches back to it so the pending item gets resolved.
	OldestOpenPending calendar.Date
}

// TransactionWindow is every transaction a provider returned for one account
// and one inclusive range of whole days (all pages together), keyed with
// [KeyTransactions].
type TransactionWindow struct {
	From, To     calendar.Date
	Transactions []KeyedTransaction
}

// WindowResult is what importing one [TransactionWindow] did.
type WindowResult struct {
	// Upserts has one entry per transaction of the window, in order.
	Upserts []TransactionUpsert
	// NoLongerReported counts stored transactions dated inside the window
	// that the window did not contain; they are marked, never deleted
	// (decision D8).
	NoLongerReported int
	// Superseded counts pending transactions linked to the booked
	// transaction they became.
	Superseded int
}

// FreshnessState is what the UI and API say about an account's data.
type FreshnessState string

const (
	// FreshnessFresh: the last refresh succeeded within the stale threshold.
	FreshnessFresh FreshnessState = "fresh"
	// FreshnessStale: the last success is older than the threshold.
	FreshnessStale FreshnessState = "stale"
	// FreshnessFailed: the latest attempt failed; the data shown is from the
	// last success (if any) and is stale.
	FreshnessFailed FreshnessState = "failed"
	// FreshnessReconnectRequired: the connection needs the user to
	// re-authorise; the data shown is from the last success and is stale.
	FreshnessReconnectRequired FreshnessState = "reconnect_required"
	// FreshnessDisconnected: the connection is not active (disconnected,
	// consent failed or never completed); no refresh will happen.
	FreshnessDisconnected FreshnessState = "disconnected"
	// FreshnessNeverRefreshed: no refresh of the account has succeeded yet.
	FreshnessNeverRefreshed FreshnessState = "never_refreshed"
)

// Freshness is the visible freshness of one account's data.
type Freshness struct {
	State FreshnessState
	// Stale is true for every state but [FreshnessFresh]: the data may not
	// be current and must not be presented as if it were.
	Stale         bool
	LastSuccessAt time.Time
	LastAttemptAt time.Time
	LastErrorKind ErrorKind
	LastErrorCode string
}

// AccountFreshness derives an account's freshness from its connection
// status and refresh state at now. staleAfter <= 0 means
// [DefaultStaleAfter]. Nothing is stored: the state cannot drift from the
// timestamps it is computed from.
func AccountFreshness(conn ConnectionStatus, s AccountSync, now time.Time, staleAfter time.Duration) Freshness {
	if staleAfter <= 0 {
		staleAfter = DefaultStaleAfter
	}
	f := Freshness{
		Stale:         true,
		LastSuccessAt: s.LastSuccessAt, LastAttemptAt: s.LastAttemptAt,
		LastErrorKind: s.LastErrorKind, LastErrorCode: s.LastErrorCode,
	}
	switch {
	case conn == ConnectionReconnectRequired:
		f.State = FreshnessReconnectRequired
	case conn != ConnectionActive:
		f.State = FreshnessDisconnected
	case s.LastErrorKind != "":
		f.State = FreshnessFailed
	case s.LastSuccessAt.IsZero():
		f.State = FreshnessNeverRefreshed
	case now.Sub(s.LastSuccessAt) > staleAfter:
		f.State = FreshnessStale
	default:
		f.State, f.Stale = FreshnessFresh, false
	}
	return f
}
