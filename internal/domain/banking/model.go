package banking

import (
	"fmt"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
)

// Mode is the access mode of a provider application (decision D10). The
// operator defined the two Enable Banking terms; Ledger uses them for any
// provider whose applications differ the same way:
//
//   - [ModeApp]: an unrestricted production application; any account the
//     user authorises is accessible.
//   - [ModeDeveloper]: a restricted production application; only accounts
//     linked to the application beforehand are accessible, and an unlinked
//     account yields an empty account list rather than an error.
//
// Neither mode is a sandbox; whether an application talks to a sandbox is
// [Capabilities.Environment]. Every connection records its mode so the
// difference stays visible.
type Mode string

const (
	ModeApp       Mode = "app"
	ModeDeveloper Mode = "developer"
)

// Valid reports whether m is a known mode.
func (m Mode) Valid() bool { return m == ModeApp || m == ModeDeveloper }

// AccountAccess is the account-access consequence of a [Mode].
func (m Mode) AccountAccess() AccountAccess {
	if m == ModeDeveloper {
		return AccountAccessLinkedOnly
	}
	return AccountAccessAll
}

// AccountAccess says which accounts a connection can reach.
type AccountAccess string

const (
	// AccountAccessAll: every account the user authorises at the bank.
	AccountAccessAll AccountAccess = "all"
	// AccountAccessLinkedOnly: only accounts linked to the application in
	// the provider's own console beforehand.
	AccountAccessLinkedOnly AccountAccess = "linked_only"
)

// Environment is the provider environment an application belongs to.
type Environment string

const (
	EnvironmentProduction Environment = "production"
	EnvironmentSandbox    Environment = "sandbox"
)

// Valid reports whether e is a known environment.
func (e Environment) Valid() bool { return e == EnvironmentProduction || e == EnvironmentSandbox }

// ConnectionStatus is the lifecycle state of a bank connection.
type ConnectionStatus string

const (
	// ConnectionPending: consent was started and the user has not returned.
	ConnectionPending ConnectionStatus = "pending"
	// ConnectionActive: consent is valid; data can be fetched.
	ConnectionActive ConnectionStatus = "active"
	// ConnectionReconnectRequired: consent expired, was revoked or the
	// provider session is gone. Only the user can fix it by re-authorising.
	ConnectionReconnectRequired ConnectionStatus = "reconnect_required"
	// ConnectionFailed: consent was refused, cancelled or failed.
	ConnectionFailed ConnectionStatus = "failed"
	// ConnectionDisconnected: the user ended the connection. Its bank facts
	// stay until an explicit purge.
	ConnectionDisconnected ConnectionStatus = "disconnected"
)

// Valid reports whether s is a known status.
func (s ConnectionStatus) Valid() bool {
	switch s {
	case ConnectionPending, ConnectionActive, ConnectionReconnectRequired, ConnectionFailed, ConnectionDisconnected:
		return true
	}
	return false
}

// BookingStatus is the normalized status of a transaction. The provider's own
// code is kept next to it ([Transaction.ProviderStatus]).
type BookingStatus string

const (
	// StatusBooked: posted by the bank; the only status that may confirm a
	// payment automatically (decision D8).
	StatusBooked BookingStatus = "booked"
	// StatusPending: not yet booked (authorised, held or scheduled). It may
	// change or disappear; it only ever produces suggestions (D8).
	StatusPending BookingStatus = "pending"
	// StatusCancelled: cancelled or rejected by the bank.
	StatusCancelled BookingStatus = "cancelled"
	// StatusOther: a status the adapter could not map.
	StatusOther BookingStatus = "other"
)

// Valid reports whether s is a known status.
func (s BookingStatus) Valid() bool {
	switch s {
	case StatusBooked, StatusPending, StatusCancelled, StatusOther:
		return true
	}
	return false
}

// BalanceType is the normalized kind of a balance. The provider's own code
// (for Enable Banking an ISO 20022 code such as CLBD) is kept next to it.
type BalanceType string

const (
	// BalanceBooked: booked transactions only (closing/opening/interim booked).
	BalanceBooked BalanceType = "booked"
	// BalanceAvailable: what the bank says is available, usually including
	// pending items and credit lines.
	BalanceAvailable BalanceType = "available"
	// BalanceExpected: booked plus pending items (expected).
	BalanceExpected BalanceType = "expected"
	// BalanceOther: anything else the provider reports.
	BalanceOther BalanceType = "other"
)

// Valid reports whether t is a known balance type.
func (t BalanceType) Valid() bool {
	switch t {
	case BalanceBooked, BalanceAvailable, BalanceExpected, BalanceOther:
		return true
	}
	return false
}

// Institution is a bank a provider can connect to.
type Institution struct {
	// ID is the provider's identifier for the bank (for Enable Banking the
	// ASPSP name, which together with the country identifies it).
	ID      string
	Name    string
	Country string // ISO 3166-1 alpha-2
	// MaxConsentValidity caps the consent the bank grants; zero if unknown.
	MaxConsentValidity time.Duration
	// MaxHistoryDays is how far back the bank serves transactions; zero if
	// unknown.
	MaxHistoryDays int
}

// AccountIdentifiers are the bank's identifiers for an account. They are
// stored, shown to the owner, and never logged.
type AccountIdentifiers struct {
	IBAN      Sensitive
	BBAN      Sensitive
	MaskedPAN Sensitive
	// Other is any other scheme the provider reports, as "scheme:value".
	Other Sensitive
}

// Account is an account as one fetch reported it.
type Account struct {
	// ExternalID identifies the account at the provider across sessions and
	// re-authorisations (for Enable Banking the identification hash, never
	// the per-session uid). Import idempotency keys on it.
	ExternalID string
	// Handle is the per-session reference used to fetch the account's data
	// (for Enable Banking the account uid). It changes on re-authorisation.
	Handle      Sensitive
	Identifiers AccountIdentifiers
	Name        string
	Product     string
	// Currency is the account currency. XXX means the provider reports none
	// or several (multi-currency account).
	Currency money.Currency
}

// Validate checks the fields the store relies on.
func (a Account) Validate() error {
	if strings.TrimSpace(a.ExternalID) == "" {
		return fmt.Errorf("banking: account without an external id")
	}
	if a.Currency.IsZero() {
		return fmt.Errorf("banking: account without a currency (use XXX when unknown)")
	}
	return nil
}

// Balance is one balance figure an account reported.
type Balance struct {
	Type BalanceType
	// ProviderType is the provider's code for the balance, such as CLBD.
	ProviderType string
	// Amount is exact, in the currency the bank reported it in.
	Amount money.Money
	// ReferenceDate is the date the bank says the balance applies to; zero
	// if not reported.
	ReferenceDate calendar.Date
}

// Validate checks the fields the store relies on. A balance whose amount is
// unknown is not a Balance: leave it out rather than reporting zero.
func (b Balance) Validate() error {
	if !b.Type.Valid() {
		return fmt.Errorf("banking: unknown balance type %q", b.Type)
	}
	if strings.TrimSpace(b.ProviderType) == "" {
		return fmt.Errorf("banking: balance without a provider type")
	}
	if !b.Amount.IsValid() {
		return fmt.Errorf("banking: balance without an amount and currency")
	}
	return nil
}

// Transaction is one transaction as one fetch reported it, normalized.
type Transaction struct {
	// EntryReference is the provider's stable identity for the transaction
	// within the account, or "" when the provider gives none the adapter
	// trusts. For Enable Banking this is entry_reference, usually present
	// only once booked. It is not globally unique.
	EntryReference string
	// ProviderTransactionID is a detail-lookup id. It is provenance only,
	// never identity: it may change between listings or be empty.
	ProviderTransactionID string
	Status                BookingStatus
	// ProviderStatus is the provider's own status code (BOOK, PDNG, ...).
	ProviderStatus string
	// Amount is the source amount exactly as the bank reported it, in the
	// bank's currency, signed: negative is money leaving the account.
	Amount money.Money
	// InstructedAmount is the original amount in another currency (a card
	// payment abroad), when reported; the zero Money otherwise.
	InstructedAmount money.Money
	BookingDate      calendar.Date
	ValueDate        calendar.Date
	TransactionDate  calendar.Date
	// Description is the raw remittance text, unchanged. It may hold personal
	// data: store and show it, never log it.
	Description string
	// Counterparty is the other party's name as reported; personal data.
	Counterparty string
}

// Date returns the date that places the transaction: the booking date, else
// the value date, else the transaction date (zero if none).
func (t Transaction) Date() calendar.Date {
	for _, d := range []calendar.Date{t.BookingDate, t.ValueDate, t.TransactionDate} {
		if !d.IsZero() {
			return d
		}
	}
	return calendar.Date{}
}

// Validate checks the fields the store relies on. Errors name the field, not
// its content.
func (t Transaction) Validate() error {
	if !t.Status.Valid() {
		return fmt.Errorf("banking: unknown booking status %q", t.Status)
	}
	if strings.TrimSpace(t.ProviderStatus) == "" {
		return fmt.Errorf("banking: transaction without a provider status")
	}
	if !t.Amount.IsValid() {
		return fmt.Errorf("banking: transaction without an amount and currency")
	}
	if t.Date().IsZero() {
		return fmt.Errorf("banking: transaction without a booking, value or transaction date")
	}
	if !t.InstructedAmount.IsValid() && !t.InstructedAmount.IsZero() {
		return fmt.Errorf("banking: instructed amount without a currency")
	}
	return nil
}
