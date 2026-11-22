package banking

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned when a connection or account does not exist.
var ErrNotFound = errors.New("banking: not found")

// ErrRefreshInProgress is returned by StartRefresh while another refresh of
// the connection is running.
var ErrRefreshInProgress = errors.New("banking: a refresh of this connection is already running")

// ErrPurgeBlocked is returned when bank facts cannot be purged because user
// data (a payment match, a correction) still refers to them.
var ErrPurgeBlocked = errors.New("banking: bank facts are still referenced by user data")

// IDs are ULIDs assigned by the store; they are Ledger's stable identifiers,
// independent of provider identifiers.
type (
	ConnectionID  string
	AccountID     string
	TransactionID string
)

// NewConnection is a connection whose consent has just been started.
type NewConnection struct {
	Capabilities Capabilities
	Institution  Institution
	// ConsentState is the State sent in the [ConsentRequest]; the redirect is
	// matched back to the connection by it. Unique while pending.
	ConsentState string
}

// Connection is a bank link: one institution through one provider
// application. Its mode and account access are recorded when it is created,
// so they stay visible even if the configuration changes later.
type Connection struct {
	ID            ConnectionID
	Provider      string
	Application   string
	Mode          Mode
	Environment   Environment
	AccountAccess AccountAccess
	Institution   Institution
	Status        ConnectionStatus
	// Session is the provider session reference, zero until consent
	// completes.
	Session Sensitive
	// ConsentValidUntil is when the user must re-authorise; zero until
	// consent completes.
	ConsentValidUntil time.Time
	ConsentGrantedAt  time.Time
	// LastErrorKind and LastErrorCode describe the failure that set the
	// current status, if any. They hold no payload.
	LastErrorKind ErrorKind
	LastErrorCode string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	// Version increments on every change (optimistic concurrency, M1).
	Version int64
}

// StoredAccount is an account as stored.
type StoredAccount struct {
	ID           AccountID
	ConnectionID ConnectionID
	Provider     string
	Account
	FirstSeenAt time.Time
	LastSeenAt  time.Time
	// NoLongerReportedAt is set when the provider stopped returning the
	// account (B2); zero otherwise. Accounts are never hard-deleted except by
	// purge.
	NoLongerReportedAt time.Time
}

// StoredBalance is one balance observation. Balances are append-only: every
// fetch adds a snapshot stamped with when it was obtained.
type StoredBalance struct {
	ID        string
	AccountID AccountID
	Balance
	ObtainedAt time.Time
}

// StoredTransaction is a transaction as stored.
type StoredTransaction struct {
	ID        TransactionID
	AccountID AccountID
	Provider  string
	KeyedTransaction
	// ImportedAt is when Ledger first stored the transaction; it never
	// changes. LastSeenAt is the latest fetch that reported it.
	ImportedAt time.Time
	LastSeenAt time.Time
	// NoLongerReportedAt is set (by B2) when a fetch covering the
	// transaction's date no longer returned it (decision D8: never
	// hard-deleted).
	NoLongerReportedAt time.Time
	// SupersededBy points at the booked transaction a pending one became,
	// when B2 can tell; empty otherwise.
	SupersededBy TransactionID
}

// Countable reports whether the transaction may count as money moved:
// booked, still reported by the provider and not superseded. Pending,
// cancelled, superseded and no-longer-reported rows are kept as facts but
// must stay out of totals, balances projections and automatic payment
// confirmation (ADR-0013 limitations, decision D8), so a pending hold that
// later books under another key is never counted twice.
func (t StoredTransaction) Countable() bool {
	return t.Status == StatusBooked && t.NoLongerReportedAt.IsZero() && t.SupersededBy == ""
}

// Outcome is what an upsert did with one item.
type Outcome string

const (
	OutcomeInserted  Outcome = "inserted"
	OutcomeUpdated   Outcome = "updated"
	OutcomeUnchanged Outcome = "unchanged"
	// OutcomeKeptBooked: the provider reported as pending a transaction
	// already stored as booked. The booked fact is kept and only LastSeenAt
	// moves.
	OutcomeKeptBooked Outcome = "kept_booked"
	// OutcomeMoved: an account known through another connection of the same
	// provider is now reported by this one; it was re-pointed, keeping its
	// id and history.
	OutcomeMoved Outcome = "moved"
)

// AccountUpsert is the result for one account.
type AccountUpsert struct {
	ID      AccountID
	Outcome Outcome
}

// TransactionUpsert is the result for one transaction.
type TransactionUpsert struct {
	ID      TransactionID
	Outcome Outcome
}

// PurgeResult counts what a purge removed.
type PurgeResult struct {
	Accounts, Balances, Transactions int
}

// FactStore persists bank facts. internal/store/bankfacts implements it.
// Each method is one write transaction.
//
// Idempotency keys:
//   - account: (provider, external id); an account moves between
//     connections of one provider rather than being duplicated;
//   - transaction: (account, [KeyedTransaction.Key]);
//   - balance: none; every call appends snapshots.
type FactStore interface {
	CreateConnection(ctx context.Context, c NewConnection, at time.Time) (Connection, error)
	Connection(ctx context.Context, id ConnectionID) (Connection, error)
	ConnectionByConsentState(ctx context.Context, state string) (Connection, error)
	Connections(ctx context.Context) ([]Connection, error)
	// ActivateConnection records a completed consent: status active, the
	// session and its validity. It also serves re-authorisation.
	ActivateConnection(ctx context.Context, id ConnectionID, s Session, at time.Time) (Connection, error)
	// SetConnectionStatus records a status change and, for a failure, the
	// error kind and provider code of cause (nil clears them).
	SetConnectionStatus(ctx context.Context, id ConnectionID, status ConnectionStatus, cause error, at time.Time) (Connection, error)
	// SetConnectionStatusForSession is SetConnectionStatus for a run that
	// started with session and grantedAt (the connection's ConsentGrantedAt):
	// inside the same transaction it changes nothing when the connection has
	// since been re-authorised (either differs) or disconnected, so a stale
	// failure cannot undo a reconnect or a disconnect. It never touches the
	// consent state of a reconnect in flight. When nothing is applied, ev (may
	// be nil) is written in that transaction. It returns the stored connection
	// and whether the change was applied.
	SetConnectionStatusForSession(ctx context.Context, id ConnectionID, session Sensitive, grantedAt time.Time, status ConnectionStatus, cause error, at time.Time, ev AuditFunc) (Connection, bool, error)

	UpsertAccounts(ctx context.Context, id ConnectionID, accounts []Account, seenAt time.Time) ([]AccountUpsert, error)
	Accounts(ctx context.Context, id ConnectionID) ([]StoredAccount, error)

	AddBalances(ctx context.Context, id AccountID, balances []Balance, obtainedAt time.Time) error
	Balances(ctx context.Context, id AccountID) ([]StoredBalance, error)

	UpsertTransactions(ctx context.Context, id AccountID, txs []KeyedTransaction, seenAt time.Time) ([]TransactionUpsert, error)
	Transactions(ctx context.Context, id AccountID) ([]StoredTransaction, error)

	// DisconnectConnection is the user-initiated disconnect. It applies only
	// when the connection still has version expected (optimistic
	// concurrency, M1), failing with a *resource.VersionConflictError
	// otherwise. Ending the provider session is the caller's job.
	DisconnectConnection(ctx context.Context, id ConnectionID, expected int64, at time.Time) (Connection, error)

	// StartRefresh records the start of a refresh run of a connection. While
	// a run of the connection is running and started less than lease ago it
	// fails with [ErrRefreshInProgress] (another engine is refreshing). A
	// running run older than lease (its process stopped) is first finished as
	// [RefreshInterrupted]. lease <= 0 means [DefaultRunLease].
	StartRefresh(ctx context.Context, id ConnectionID, at time.Time, lease time.Duration) (RefreshRun, error)
	// FinishRefresh records how run ended: its outcome (not running), error
	// kind and code, counts and FinishedAt.
	FinishRefresh(ctx context.Context, run RefreshRun) (RefreshRun, error)
	// RefreshRuns lists a connection's runs, newest first, at most limit
	// (limit <= 0: all).
	RefreshRuns(ctx context.Context, id ConnectionID, limit int) ([]RefreshRun, error)

	// MarkAccountsNoLongerReported marks the connection's accounts that are
	// not in reported as no longer reported (decision D8: never deleted),
	// and returns how many it newly marked.
	MarkAccountsNoLongerReported(ctx context.Context, id ConnectionID, reported []AccountID, at time.Time) (int, error)

	// BeginAccountRefresh records the start of an account's refresh and
	// returns its refresh state.
	BeginAccountRefresh(ctx context.Context, id AccountID, at time.Time) (AccountSync, error)
	// EndAccountRefresh records the end of an account's refresh: with a nil
	// cause a success at at (clearing the last error), otherwise the
	// cause's kind and provider code. notice is the account's notice code
	// ([NoticeTransactionsRejected]) or empty; it replaces the previous one.
	// Bank facts are not touched, so a failure leaves the last good data in
	// place.
	EndAccountRefresh(ctx context.Context, id AccountID, cause error, notice string, at time.Time) (AccountSync, error)
	// AccountSync returns an account's refresh state; zero timestamps if it
	// was never refreshed.
	AccountSync(ctx context.Context, id AccountID) (AccountSync, error)

	// ImportTransactionWindow stores one complete window in one write
	// transaction: it upserts every transaction like
	// [FactStore.UpsertTransactions] (also clearing a superseded link of a
	// row that is reported again), marks the account's other transactions
	// dated inside the window no longer reported, links a newly unreported
	// pending transaction to the one booked transaction of the window with
	// the same entry reference (also one that stopped being reported in an
	// earlier window or refresh and is still unlinked), and advances the account's
	// TransactionsThrough to w.To. Nothing is written if any step fails.
	ImportTransactionWindow(ctx context.Context, id AccountID, w TransactionWindow, seenAt time.Time) (WindowResult, error)

	// PurgeConnection deletes a connection and every bank fact under it. It
	// is the only path that deletes bank facts (decision D8: explicit purge),
	// applies only at version expected like [FactStore.DisconnectConnection],
	// and fails with [ErrPurgeBlocked] while user data refers to them.
	PurgeConnection(ctx context.Context, id ConnectionID, expected int64) (PurgeResult, error)
}
