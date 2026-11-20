package banking

import (
	"context"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
)

// Provider is the port a banking-data adapter implements, for one configured
// provider application (decision D10: a deployment may configure several
// applications, each with its own mode, and every connection records the
// application it uses).
//
// Every method returns errors classified by the taxonomy in errors.go (an
// [*Error], or a context error). Adapters keep provider-specific behaviour
// (authentication, request signing, status codes, header rules, retries
// inside one call) to themselves. Ledger retries according to the error kind.
//
// Implementations must be safe for concurrent use and must not log or return
// account identifiers, session references, authorisation codes or raw
// payloads.
type Provider interface {
	// Capabilities describes the application. It is static for the life of
	// the adapter.
	Capabilities() Capabilities

	// Institutions lists the banks available in a country (ISO 3166-1
	// alpha-2).
	Institutions(ctx context.Context, country string) ([]Institution, error)

	// StartConsent begins authorisation at a bank and returns where to send
	// the user.
	StartConsent(ctx context.Context, req ConsentRequest) (ConsentStart, error)

	// CompleteConsent exchanges what the bank redirect returned for a
	// session.
	CompleteConsent(ctx context.Context, req ConsentCompletion) (Session, error)

	// Accounts lists the accounts a session can reach. In
	// [ModeDeveloper] an account not linked to the application is simply
	// absent; an empty list is not an error.
	Accounts(ctx context.Context, session Sensitive) ([]Account, error)

	// Balances returns the balances the bank reports for an account now.
	Balances(ctx context.Context, session Sensitive, account Sensitive) ([]Balance, error)

	// Transactions returns one page of an account's transactions.
	Transactions(ctx context.Context, session Sensitive, account Sensitive, q TransactionQuery) (TransactionPage, error)

	// EndSession closes the session at the provider (and the bank consent,
	// where the provider can). An already closed session is not an error.
	EndSession(ctx context.Context, session Sensitive) error
}

// Capabilities describes what one provider application supports. Zero
// numeric values mean "not known", never "none".
type Capabilities struct {
	// Provider is the adapter's name, such as "enablebanking": lower-case
	// letters, digits and underscores.
	Provider string
	// Application is the operator's name for this configured application,
	// unique per provider.
	Application string
	Mode        Mode
	Environment Environment
	// BalanceTypes lists the normalized balance types the provider can
	// report. Each bank reports only some of them.
	BalanceTypes []BalanceType
	// PendingTransactions is true when pending items may be reported.
	PendingTransactions bool
	// StableEntryReferences is true when [Transaction.EntryReference] is a
	// stable identity for booked transactions.
	StableEntryReferences bool
	// MaxConsentValidity is the longest consent the provider grants; banks
	// may grant less ([Institution.MaxConsentValidity]).
	MaxConsentValidity time.Duration
	// MaxHistoryDays is how far back transactions can usually be fetched.
	MaxHistoryDays int
	// BackgroundFetchesPerDay is the daily limit banks commonly apply to
	// fetches made without the user present (Enable Banking documents about
	// four); zero if unknown.
	BackgroundFetchesPerDay int
}

// AccountAccess is the account-access consequence of the application's mode.
func (c Capabilities) AccountAccess() AccountAccess { return c.Mode.AccountAccess() }

// SupportsBalance reports whether t is among [Capabilities.BalanceTypes].
func (c Capabilities) SupportsBalance(t BalanceType) bool {
	for _, x := range c.BalanceTypes {
		if x == t {
			return true
		}
	}
	return false
}

// PSUType is the kind of bank customer giving consent.
type PSUType string

const (
	PSUPersonal PSUType = "personal"
	PSUBusiness PSUType = "business"
)

// ConsentRequest starts authorisation at a bank.
type ConsentRequest struct {
	Institution Institution
	// ValidUntil is the consent end Ledger asks for; the bank may cap it.
	ValidUntil time.Time
	// RedirectURL is where the bank sends the user back to Ledger.
	RedirectURL string
	// State is Ledger's opaque, unguessable value tying the redirect to this
	// request; the provider returns it unchanged.
	State   string
	PSUType PSUType
}

// ConsentStart says where to send the user.
type ConsentStart struct {
	// AuthorizationURL is the bank or provider page the user opens.
	AuthorizationURL string
	// Reference is the provider's id for the pending authorisation, if any.
	Reference string
}

// ConsentCompletion is what the bank redirect brought back.
type ConsentCompletion struct {
	State string
	// Code is the one-time authorisation code from the redirect.
	Code Sensitive
	// Error is the provider's error code from the redirect, if the user
	// cancelled or the bank refused; "" otherwise.
	Error string
}

// Session is an authorised provider session.
type Session struct {
	// Reference identifies the session at the provider. It changes on every
	// re-authorisation.
	Reference Sensitive
	// ValidUntil is when consent ends; the user must re-authorise after it.
	ValidUntil time.Time
	// Accounts are the accounts the session reaches, as reported at
	// authorisation (may be empty in [ModeDeveloper]).
	Accounts []Account
}

// TransactionQuery selects transactions. From and To are inclusive dates;
// a zero From asks for the longest history the bank allows.
type TransactionQuery struct {
	From calendar.Date
	To   calendar.Date
	// Continuation is the key from the previous page, "" for the first.
	// Keys are valid only within the session and the same From/To.
	Continuation string
}

// TransactionPage is one page. An empty page with a non-empty Next is valid
// and means "keep going". Next is "" on the last page.
type TransactionPage struct {
	Transactions []Transaction
	Next         string
}
