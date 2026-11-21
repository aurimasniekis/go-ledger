package enablebanking

import (
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
)

var unknownCurrency = money.MustParseCurrency("XXX")

// mapAccount maps an AccountResource. ExternalID is the identification hash
// (stable across sessions, R1 section 4), never the per-session uid. The
// AccountResource "name" field is the account holder's name, so the account's
// display name is the PSU or bank description ("details"), else the product.
func mapAccount(a wireAccountResource, fallbackHash string) banking.Account {
	acc := banking.Account{
		ExternalID: a.IdentificationHash,
		Handle:     banking.NewSensitive(a.UID),
		Name:       strings.TrimSpace(a.Details),
		Product:    strings.TrimSpace(a.Product),
		Currency:   unknownCurrency,
	}
	if acc.ExternalID == "" {
		acc.ExternalID = fallbackHash
	}
	if acc.Name == "" {
		acc.Name = acc.Product
	}
	if c, err := money.ParseCurrency(strings.TrimSpace(a.Currency)); err == nil {
		acc.Currency = c
	}
	ids := a.AllAccountIDs
	if a.AccountID != nil {
		if a.AccountID.IBAN != "" {
			ids = append([]wireGenericIdentification{{Identification: a.AccountID.IBAN, SchemeName: "IBAN"}}, ids...)
		}
		if a.AccountID.Other != nil {
			ids = append([]wireGenericIdentification{*a.AccountID.Other}, ids...)
		}
	}
	for _, id := range ids {
		v := strings.TrimSpace(id.Identification)
		if v == "" {
			continue
		}
		switch id.SchemeName {
		case "IBAN":
			if acc.Identifiers.IBAN.IsZero() {
				acc.Identifiers.IBAN = banking.NewSensitive(v)
			}
		case "BBAN":
			if acc.Identifiers.BBAN.IsZero() {
				acc.Identifiers.BBAN = banking.NewSensitive(v)
			}
		case "CPAN":
			if acc.Identifiers.MaskedPAN.IsZero() {
				acc.Identifiers.MaskedPAN = banking.NewSensitive(maskPAN(v))
			}
		default:
			if acc.Identifiers.Other.IsZero() && id.SchemeName != "" {
				acc.Identifiers.Other = banking.NewSensitive(id.SchemeName + ":" + v)
			}
		}
	}
	return acc
}

// maskPAN keeps only the last four characters of a card number. Enable
// Banking documents CPAN as "masked or plain"; Ledger never stores a plain
// card number.
func maskPAN(v string) string {
	v = strings.ReplaceAll(v, " ", "")
	if len(v) <= 4 {
		return "****"
	}
	return "****" + v[len(v)-4:]
}

// balanceTypes maps ISO 20022 balance codes (BalanceStatus enum).
var balanceTypes = map[string]banking.BalanceType{
	"CLBD": banking.BalanceBooked, "OPBD": banking.BalanceBooked, "ITBD": banking.BalanceBooked, "PRCD": banking.BalanceBooked,
	"CLAV": banking.BalanceAvailable, "OPAV": banking.BalanceAvailable, "ITAV": banking.BalanceAvailable, "FWAV": banking.BalanceAvailable,
	"XPCD": banking.BalanceExpected,
	"INFO": banking.BalanceOther, "OTHR": banking.BalanceOther, "VALU": banking.BalanceOther,
}

// mapBalance maps a BalanceResource. An amount or currency that cannot be
// parsed leaves Amount invalid, so the import engine drops and counts the
// balance instead of storing a guess (ADR-0020 rule 11).
func mapBalance(b wireBalance) banking.Balance {
	code := strings.ToUpper(strings.TrimSpace(b.BalanceType))
	t, ok := balanceTypes[code]
	if !ok {
		t = banking.BalanceOther
	}
	out := banking.Balance{Type: t, ProviderType: code, ReferenceDate: parseDate(b.ReferenceDate)}
	if m, err := money.Parse(strings.TrimSpace(b.BalanceAmount.Amount), strings.TrimSpace(b.BalanceAmount.Currency)); err == nil {
		out.Amount = m
	}
	return out
}

// bookingStatuses maps the TransactionStatus enum.
var bookingStatuses = map[string]banking.BookingStatus{
	"BOOK": banking.StatusBooked,
	"PDNG": banking.StatusPending, "HOLD": banking.StatusPending, "SCHD": banking.StatusPending,
	"CNCL": banking.StatusCancelled, "RJCT": banking.StatusCancelled,
	"OTHR": banking.StatusOther,
}

// mapTransaction maps a Transaction. The amount is signed from
// credit_debit_indicator (DBIT is money out, negative). Enable Banking's
// amount pattern also allows a minus sign; a negative amount marked CRDT, or
// a missing or unknown indicator, contradicts itself, so Amount is left
// invalid and the import engine drops and counts the row
// (transactions_rejected) rather than guessing a direction.
func mapTransaction(t wireTransaction) banking.Transaction {
	status := strings.ToUpper(strings.TrimSpace(t.Status))
	bs, ok := bookingStatuses[status]
	if !ok {
		bs = banking.StatusOther
	}
	out := banking.Transaction{
		EntryReference:        strings.TrimSpace(t.EntryReference),
		ProviderTransactionID: strings.TrimSpace(t.TransactionID),
		Status:                bs,
		ProviderStatus:        status,
		BookingDate:           parseDate(t.BookingDate),
		ValueDate:             parseDate(t.ValueDate),
		TransactionDate:       parseDate(t.TransactionDate),
		Description:           strings.Join(t.RemittanceInformation, "\n"),
	}
	cdi := strings.ToUpper(strings.TrimSpace(t.CreditDebitIndicator))
	switch cdi {
	case "DBIT":
		if t.Creditor != nil {
			out.Counterparty = strings.TrimSpace(t.Creditor.Name)
		}
	case "CRDT":
		if t.Debtor != nil {
			out.Counterparty = strings.TrimSpace(t.Debtor.Name)
		}
	}
	m, err := money.Parse(strings.TrimSpace(t.TransactionAmount.Amount), strings.TrimSpace(t.TransactionAmount.Currency))
	if err != nil {
		return out
	}
	signed, ok := applyIndicator(m, cdi)
	if !ok {
		return out
	}
	out.Amount = signed
	if t.ExchangeRate != nil && t.ExchangeRate.InstructedAmount != nil {
		ia := t.ExchangeRate.InstructedAmount
		if im, err := money.Parse(strings.TrimSpace(ia.Amount), strings.TrimSpace(ia.Currency)); err == nil {
			im = im.Abs()
			if signed.Sign() < 0 {
				im = im.Neg()
			}
			out.InstructedAmount = im
		}
	}
	return out
}

func applyIndicator(m money.Money, cdi string) (money.Money, bool) {
	switch cdi {
	case "DBIT":
		return m.Abs().Neg(), true
	case "CRDT":
		if m.Sign() < 0 {
			return money.Money{}, false
		}
		return m, true
	}
	return money.Money{}, false
}

func parseDate(s string) calendar.Date {
	s = strings.TrimSpace(s)
	if len(s) > 10 {
		s = s[:10]
	}
	d, err := calendar.ParseDate(s)
	if err != nil {
		return calendar.Date{}
	}
	return d
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
