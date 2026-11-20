package banking

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned when a connection or account does not exist.
var ErrNotFound = errors.New("banking: not found")

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
	// BeginReconnect records the consent state of a re-authorisation of an
	// existing connection. The status stays as it is until
	// ActivateConnection, so a failed reconnect leaves the connection
	// reconnect-required, with its data visible as stale.
	BeginReconnect(ctx context.Context, id ConnectionID, consentState string, at time.Time) (Connection, error)
	// SetConnectionStatus records a status change and, for a failure, the
	// error kind and provider code of cause (nil clears them).
	SetConnectionStatus(ctx context.Context, id ConnectionID, status ConnectionStatus, cause error, at time.Time) (Connection, error)

	UpsertAccounts(ctx context.Context, id ConnectionID, accounts []Account, seenAt time.Time) ([]AccountUpsert, error)
	Accounts(ctx context.Context, id ConnectionID) ([]StoredAccount, error)

	AddBalances(ctx context.Context, id AccountID, balances []Balance, obtainedAt time.Time) error
	Balances(ctx context.Context, id AccountID) ([]StoredBalance, error)

	UpsertTransactions(ctx context.Context, id AccountID, txs []KeyedTransaction, seenAt time.Time) ([]TransactionUpsert, error)
	Transactions(ctx context.Context, id AccountID) ([]StoredTransaction, error)

	// PurgeConnection deletes a connection and every bank fact under it. It
	// is the only path that deletes bank facts (decision D8: explicit purge)
	// and fails with [ErrPurgeBlocked] while user data refers to them.
	PurgeConnection(ctx context.Context, id ConnectionID) (PurgeResult, error)
}
