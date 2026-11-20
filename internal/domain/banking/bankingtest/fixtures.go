package bankingtest

import (
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
)

// Synthetic identifiers used by [Standard]. None is a real account.
const (
	InstitutionID       = "Fake Bank"
	InstitutionCountry  = "FI"
	AccountEUR          = "fake-hash-eur-0001"
	AccountGBP          = "fake-hash-gbp-0002"
	AccountMulti        = "fake-hash-xxx-0003"
	SyntheticIBANEUR    = "XX00FAKE0000000000000001"
	SyntheticIBANGBP    = "XX00FAKE0000000000000002"
	RefRent             = "fake-ref-rent-2026-10"
	RefSalary           = "fake-ref-salary-2026-10"
	RefPendingThenBooks = "fake-ref-card-0042"
)

// Standard is a deterministic dataset covering the cases the boundary must
// handle:
//
//   - a linked EUR account with booked transactions that carry entry
//     references, a booked one without, two identical transactions without a
//     reference on one day, a pending item without a reference, and a pending
//     item with a reference (see [BookPending]);
//   - an unlinked GBP account, invisible in [banking.ModeDeveloper];
//   - a linked multi-currency account (XXX) that reports no balances.
//
// All dates are in October 2026.
func Standard() Dataset {
	eur := money.MustParseCurrency("EUR")
	gbp := money.MustParseCurrency("GBP")
	return Dataset{
		Institutions: []banking.Institution{{
			ID: InstitutionID, Name: "Fake Bank", Country: InstitutionCountry,
			MaxConsentValidity: 180 * 24 * time.Hour, MaxHistoryDays: 730,
		}},
		Accounts: []Account{
			{
				Linked: true,
				Account: banking.Account{
					ExternalID:  AccountEUR,
					Identifiers: banking.AccountIdentifiers{IBAN: banking.NewSensitive(SyntheticIBANEUR)},
					Name:        "Everyday", Product: "Current account", Currency: eur,
				},
				Balances: []banking.Balance{
					{Type: banking.BalanceBooked, ProviderType: "CLBD", Amount: money.MustParse("1250.40", "EUR"), ReferenceDate: day(31)},
					{Type: banking.BalanceAvailable, ProviderType: "ITAV", Amount: money.MustParse("1180.40", "EUR")},
				},
				Transactions: []banking.Transaction{
					booked(RefSalary, "2500.00", "EUR", 1, "SALARY OCTOBER", "Example Employer"),
					booked(RefRent, "-800.00", "EUR", 2, "RENT 2026-10", "Example Landlord"),
					booked("", "-12.99", "EUR", 5, "STREAMING SERVICE", ""),
					booked("", "-3.50", "EUR", 7, "COFFEE KIOSK", ""),
					booked("", "-3.50", "EUR", 7, "COFFEE KIOSK", ""),
					{
						EntryReference: RefPendingThenBooks, Status: banking.StatusPending, ProviderStatus: "PDNG",
						Amount: money.MustParse("-45.00", "EUR"), TransactionDate: day(29), Description: "CARD PURCHASE BOOKSHOP",
					},
					{
						Status: banking.StatusPending, ProviderStatus: "PDNG",
						Amount: money.MustParse("-70.00", "EUR"), TransactionDate: day(30), Description: "HOTEL HOLD",
					},
					{
						EntryReference: "fake-ref-abroad-0001", Status: banking.StatusBooked, ProviderStatus: "BOOK",
						Amount: money.MustParse("-23.17", "EUR"), InstructedAmount: money.MustParse("-20.00", "GBP"),
						BookingDate: day(12), ValueDate: day(11), Description: "CARD PURCHASE LONDON",
					},
				},
			},
			{
				Linked: false,
				Account: banking.Account{
					ExternalID:  AccountGBP,
					Identifiers: banking.AccountIdentifiers{IBAN: banking.NewSensitive(SyntheticIBANGBP)},
					Name:        "Savings", Product: "Savings account", Currency: gbp,
				},
				Balances: []banking.Balance{
					{Type: banking.BalanceBooked, ProviderType: "CLBD", Amount: money.MustParse("300.00", "GBP"), ReferenceDate: day(31)},
				},
				Transactions: []banking.Transaction{
					booked("fake-ref-interest-2026-10", "0.25", "GBP", 31, "INTEREST", ""),
				},
			},
			{
				Linked: true,
				Account: banking.Account{
					ExternalID: AccountMulti, Name: "Multi-currency wallet", Currency: money.MustParseCurrency("XXX"),
				},
			},
		},
	}
}

// BookPending books the pending item with entry reference
// [RefPendingThenBooks] in d: same reference and amount, status BOOK, a
// booking date. With the reference it keeps its idempotency key, so the
// stored row becomes booked in place.
func BookPending(d *Dataset) {
	for i := range d.Accounts {
		for j, t := range d.Accounts[i].Transactions {
			if t.EntryReference == RefPendingThenBooks {
				t.Status, t.ProviderStatus, t.BookingDate, t.ValueDate = banking.StatusBooked, "BOOK", day(31), day(31)
				d.Accounts[i].Transactions[j] = t
			}
		}
	}
}

// October2026 is the whole month the [Standard] data falls in.
func October2026() banking.TransactionQuery {
	return banking.TransactionQuery{From: day(1), To: day(31)}
}

func day(d int) calendar.Date { return calendar.MustDate(2026, time.October, d) }

func booked(ref, amount, currency string, d int, description, counterparty string) banking.Transaction {
	return banking.Transaction{
		EntryReference: ref, Status: banking.StatusBooked, ProviderStatus: "BOOK",
		Amount: money.MustParse(amount, currency), BookingDate: day(d), ValueDate: day(d),
		Description: description, Counterparty: counterparty,
	}
}
