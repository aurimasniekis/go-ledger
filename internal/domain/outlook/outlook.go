// Package outlook compares bank balances with the recorded commitments still
// due (task FX3, ADR-0044): per currency first and, only as a secondary
// figure, converted into one reporting currency where an acceptable rate
// exists (decision D5). It is an outlook against recorded commitments: never
// a forecast and never a statement of money that is safe to spend.
//
// What is still due comes from the monthly view ([monthview]), so payment
// state is derived in one place (decision D7, ADR-0031, ADR-0034, ADR-0042);
// this package only adds the months up. Balances come from stored bank facts
// with each account's freshness ([banking.AccountFreshness]). Every figure is
// exact decimal arithmetic in Go; amounts in different currencies are never
// combined except through a conversion whose rate is reported with it.
package outlook

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/fx"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/monthview"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/payments"
)

// Notice is the wording every presentation of the outlook carries.
const Notice = "Outlook against recorded commitments: bank balances compared with the commitments Ledger has recorded as still due. " +
	"It covers recorded commitments only, is not a forecast and does not tell you how much money is safe to spend."

// MaxMonthsBack bounds how far before the current month the outlook
// reads occurrences, so one request cannot read without limit. Anything
// older is reported through [Outlook.Truncated].
const MaxMonthsBack = 240

// pageSize is how many commitments one read of the store fetches.
const pageSize = 200

// Months reads one month of occurrences with their derived payment state;
// [monthview.Service] implements it.
type Months interface {
	Month(ctx context.Context, month calendar.YearMonth, today calendar.Date) (monthview.Summary, error)
}

// Commitments lists commitment definitions; the outlook reads only their
// start dates, to know the first month that can hold an occurrence.
type Commitments interface {
	List(ctx context.Context, f commitments.ListFilter) ([]commitments.Commitment, error)
}

// Banks reads stored bank facts; banking.FactStore (internal/store/bankfacts)
// implements it.
type Banks interface {
	Connections(ctx context.Context) ([]banking.Connection, error)
	Accounts(ctx context.Context, id banking.ConnectionID) ([]banking.StoredAccount, error)
	Balances(ctx context.Context, id banking.AccountID) ([]banking.StoredBalance, error)
	AccountSync(ctx context.Context, id banking.AccountID) (banking.AccountSync, error)
}

// Converter converts money at the latest acceptable rate; *fx.Service
// implements it.
type Converter interface {
	Convert(ctx context.Context, from money.Money, to money.Currency) (fx.Conversion, error)
}

// Service builds outlooks. Banks and Rates may be nil: without Banks no
// balance is known, without Rates only amounts already in the reporting
// currency can be converted (every other currency is excluded and flagged).
type Service struct {
	Months      Months
	Commitments Commitments
	Banks       Banks
	Rates       Converter
	// Today is today in the reporting zone.
	Today func() (calendar.Date, error)
	// Now is the clock for bank freshness; nil means time.Now.
	Now func() time.Time
	// BankStaleAfter is the bank-data staleness threshold; zero means
	// [banking.DefaultStaleAfter].
	BankStaleAfter time.Duration
	// Zone is the reporting zone. A balance with no reference date is dated
	// by the day it was obtained in this zone; zero means UTC.
	Zone calendar.Zone
	// ConvertTimeout bounds the whole conversion phase of one build: the
	// rate lookups (which may call the rate provider) of every currency
	// together. A currency not converted in time is excluded with
	// [ReasonConversionTimedOut] and the outlook is still returned. Zero
	// means no bound beyond the caller's context.
	ConvertTimeout time.Duration
}

// Request says what to build.
type Request struct {
	// Through is the last month whose commitments count; zero, or a month
	// before the current one, means the current month in the reporting zone:
	// everything due up to today is always counted and Through only extends
	// the upcoming part. It may not be beyond the occurrence horizon
	// ([occurrences.HorizonLimit]).
	Through calendar.YearMonth
	// Currency is the reporting currency of the secondary, converted
	// aggregate; zero means none is built.
	Currency money.Currency
	// Balances is whether the caller may see bank balances. When false no
	// balance is read and [Outlook.BalancesShown] is false.
	Balances bool
}

// ErrBeyondHorizon is returned when Through is later than the occurrence
// horizon allows.
var ErrBeyondHorizon = occurrences.ErrBeyondHorizon

// Outlook is the comparison as of one day.
type Outlook struct {
	AsOf calendar.Date
	// From and Through are the months read: Through is always the month used
	// (never zero); From is the month of the earliest commitment start
	// (bounded by [MaxMonthsBack]) and zero when there are no commitments, or
	// none starts by Through.
	From, Through calendar.YearMonth
	// Truncated is true when commitments start before From: occurrences
	// older than [MaxMonthsBack] months are not counted.
	Truncated bool
	// Currencies has one line per currency that has a commitment still due,
	// or a counted balance, sorted by code.
	Currencies []CurrencyLine
	// BalancesShown is false when the caller may not see balances or no
	// bank facts are available; Accounts is then empty and no line has a
	// balance.
	BalancesShown bool
	Accounts      []Account
	// Converted is the secondary aggregate in Request.Currency; nil when
	// none was asked for.
	Converted *Converted
	// OccurrencesChangedAt is the newest change among the occurrences read;
	// nil when none was read.
	OccurrencesChangedAt *time.Time
	// BankStaleAfter is the staleness threshold applied to bank data (the
	// configured one, or [banking.DefaultStaleAfter]); zero when balances are
	// not shown.
	BankStaleAfter time.Duration
	// NotRecordedCount is the number of occurrences in the window that are
	// counted nowhere because they fell due before their commitment was
	// recorded (D38 1a): they owe nothing here, but are listed in the
	// monthly view.
	NotRecordedCount int
}

// CurrencyLine is the comparison for one currency. Nothing in it is
// converted.
//
//   - StillDue is the outstanding amount of every occurrence from From through
//     Through, as the monthly view derives it: paid, cancelled and "not
//     recorded" occurrences owe nothing (D7, D38, D41).
//   - Overdue is the part of StillDue past its original due date, within a
//     documented grace period or not (D7).
//   - Upcoming is StillDue less Overdue.
//   - Balance is the sum of the counted balances of accounts in this
//     currency; nil when no account has one (or balances are not shown).
//   - BalanceLessStillDue is Balance less StillDue; nil when Balance is.
type CurrencyLine struct {
	Currency            money.Currency
	StillDue            money.Money
	Overdue             money.Money
	Upcoming            money.Money
	OverdueCount        int
	Balance             *money.Money
	BalanceLessStillDue *money.Money
	// Accounts counts the accounts whose balance is counted here;
	// StaleAccounts those of them whose data is not fresh.
	Accounts      int
	StaleAccounts int
	// OldestBalanceAt is the earliest time a counted balance was obtained.
	OldestBalanceAt *time.Time
	// UnconvertedEvidence counts occurrences with payment evidence in
	// another currency: such evidence is never converted and counted nowhere.
	UnconvertedEvidence int
	// OverdueMonths says which months the overdue part comes from, oldest
	// first (empty when nothing is overdue): the drill-down to the monthly
	// view.
	OverdueMonths []OverdueMonth
	// BalanceAsOf is the date of the oldest counted balance: its reference
	// date, or the UTC date it was obtained when the bank gave none. Zero when
	// no balance is counted. The balance itself is never adjusted.
	BalanceAsOf calendar.Date
	// PaymentsAfterBalance is the payment evidence in this currency dated
	// after BalanceAsOf: recorded payments the bank balance may not yet
	// reflect. It is a caveat only and is never netted into Balance. Nil when
	// no balance is counted.
	PaymentsAfterBalance *PaymentsAfter
}

// OverdueMonth is the overdue part of one currency that falls in one month.
type OverdueMonth struct {
	Month  calendar.YearMonth
	Count  int
	Amount money.Money
}

// PaymentsAfter counts payment evidence and sums its amounts.
type PaymentsAfter struct {
	Count  int
	Amount money.Money
}

// Balance is the balance of an account that the outlook counts or shows.
type Balance struct {
	banking.Balance
	ObtainedAt time.Time
}

// NotCounted says why an account adds nothing to the totals.
type NotCounted string

const (
	// NotCountedNoBalance: the bank has reported no balance for the account.
	NotCountedNoBalance NotCounted = "no_balance"
	// NotCountedNoLongerReported: the provider stopped returning the account.
	NotCountedNoLongerReported NotCounted = "no_longer_reported"
	// NotCountedOtherType: the only balances reported are of a type whose
	// meaning is unknown ([banking.BalanceOther]).
	NotCountedOtherType NotCounted = "unsupported_balance_type"
)

// Account is one bank account with the balances the outlook counts (one per
// currency: an account in XXX can hold several) and its data freshness.
type Account struct {
	ID           banking.AccountID
	ConnectionID banking.ConnectionID
	Name         string
	Institution  string
	Currency     money.Currency
	Balances     []Balance
	Freshness    banking.Freshness
	// NotCounted is empty when Balances are counted.
	NotCounted NotCounted
}

// BalancePreference is the order in which balance types are chosen when an
// account reports several in one fetch: the booked balance first, as the
// bank's own settled figure, then expected, then available (which can
// include an overdraft facility). Other types are never counted.
var BalancePreference = []banking.BalanceType{banking.BalanceBooked, banking.BalanceExpected, banking.BalanceAvailable}

// Converted is the secondary aggregate in one reporting currency (D5). Only
// currencies whose conversion is acceptable ([fx.Conversion.Acceptable]) are
// in it; the others are in Excluded with the reason, so the total is never
// presented as complete when it is not.
type Converted struct {
	Currency money.Currency
	StillDue money.Money
	Overdue  money.Money
	// Balance and BalanceLessStillDue are nil when balances are not shown or
	// no included currency has a balance.
	Balance             *money.Money
	BalanceLessStillDue *money.Money
	// BalanceAccounts counts the accounts behind Balance (of included
	// currencies only); StaleBalanceAccounts those of them whose data is
	// not fresh. Stale balances are counted, and flagged here.
	BalanceAccounts      int
	StaleBalanceAccounts int
	Included             []RateUse
	Excluded             []Exclusion
}

// Complete reports whether every currency line is in the aggregate.
func (c Converted) Complete() bool { return len(c.Excluded) == 0 }

// RateUse is a currency counted in the aggregate and the rate used for it
// (nil Rate for the reporting currency itself).
type RateUse struct {
	Currency  money.Currency
	Rate      *fx.Rate
	Age       time.Duration
	FromCache bool
	// RefreshFailed is true when a provider refresh was due and failed: the
	// rate came from the cache and is still within the freshness policy.
	RefreshFailed bool
}

// Exclusion is a currency left out of the aggregate.
type Exclusion struct {
	Currency money.Currency
	// Status is stale or missing; Reason says why (an fx reason, or
	// "conversion_failed").
	Status fx.Status
	Reason string
	// Rate is the rate found too old, for a stale conversion.
	Rate *fx.Rate
}

// ReasonConversionFailed marks a conversion the rate service refused with an
// error (for example an invalid pair): the currency is excluded.
const ReasonConversionFailed = "conversion_failed"

// ReasonConversionTimedOut marks a currency left unconverted because the
// conversion phase ran out of time ([Service.ConvertTimeout]); its rate
// may exist but was not obtained in time.
const ReasonConversionTimedOut = "conversion_timed_out"

// Build returns the outlook for r.
func (s Service) Build(ctx context.Context, r Request) (Outlook, error) {
	if s.Months == nil || s.Commitments == nil || s.Today == nil {
		return Outlook{}, errors.New("outlook: service needs Months, Commitments and Today")
	}
	today, err := s.Today()
	if err != nil {
		return Outlook{}, fmt.Errorf("outlook: resolving today: %w", err)
	}
	through := r.Through
	if through == (calendar.YearMonth{}) {
		through = today.YearMonth()
	}
	if through.Compare(occurrences.HorizonLimit(today)) > 0 {
		return Outlook{}, ErrBeyondHorizon
	}
	// Every occurrence due up to today is always counted: a Through before
	// the current month never drops an overdue obligation, it only has
	// nothing to add to the upcoming part.
	if through.Compare(today.YearMonth()) < 0 {
		through = today.YearMonth()
	}
	// Through is always the month used, even when no commitment starts by it:
	// the zero YearMonth does not encode, so the response would be empty.
	out := Outlook{AsOf: today, Through: through, Currencies: []CurrencyLine{}, Accounts: []Account{}}

	lines := map[money.Currency]*CurrencyLine{}
	// Payment evidence of the window, per currency, for the caveat about
	// payments dated after a balance.
	type paid struct {
		date   calendar.Date
		amount money.Money
	}
	evidence := map[money.Currency][]paid{}
	line := func(cur money.Currency, scale int32) *CurrencyLine {
		l := lines[cur]
		if l == nil {
			z, _ := money.Zero(cur, scale)
			l = &CurrencyLine{Currency: cur, StillDue: z, Overdue: z, Upcoming: z}
			lines[cur] = l
		}
		return l
	}

	first, ok, err := s.firstMonth(ctx)
	if err != nil {
		return Outlook{}, err
	}
	if ok && first.Compare(through) <= 0 {
		from := first
		earliest, err := today.YearMonth().AddMonths(-(MaxMonthsBack - 1))
		if err != nil {
			return Outlook{}, fmt.Errorf("outlook: window: %w", err)
		}
		if from.Compare(earliest) < 0 {
			from, out.Truncated = earliest, true
		}
		out.From = from
		var latest time.Time
		for m := from; m.Compare(through) <= 0; {
			sum, err := s.Months.Month(ctx, m, today)
			if err != nil {
				return Outlook{}, fmt.Errorf("outlook: reading %s: %w", m, err)
			}
			if sum.LatestChange != nil && sum.LatestChange.After(latest) {
				latest = *sum.LatestChange
			}
			for _, row := range sum.Rows {
				st := row.State
				if st.Status == payments.StatusCancelled {
					continue
				}
				if st.Status != payments.StatusPaid && st.Timing == payments.StatusNotRecorded {
					out.NotRecordedCount++
				}
				cur := row.Occurrence.Amount.Currency()
				for _, e := range row.Evidence {
					// Evidence in another currency is never counted (D5).
					if e.Amount.Currency() == cur {
						evidence[cur] = append(evidence[cur], paid{e.Date, e.Amount})
					}
				}
			}
			for _, t := range sum.Totals {
				if t.Outstanding.Sign() == 0 && t.Unconverted == 0 {
					continue
				}
				l := line(t.Currency, t.Outstanding.Amount().Scale())
				// Same currency on both sides: Add cannot fail.
				l.StillDue, _ = l.StillDue.Add(t.Outstanding)
				l.Overdue, _ = l.Overdue.Add(t.Overdue)
				l.OverdueCount += t.OverdueCount
				if t.OverdueCount > 0 {
					l.OverdueMonths = append(l.OverdueMonths, OverdueMonth{Month: m, Count: t.OverdueCount, Amount: t.Overdue})
				}
				l.UnconvertedEvidence += t.Unconverted
			}
			if m, err = m.AddMonths(1); err != nil {
				return Outlook{}, fmt.Errorf("outlook: window: %w", err)
			}
		}
		if !latest.IsZero() {
			out.OccurrencesChangedAt = &latest
		}
	}

	if r.Balances && s.Banks != nil {
		accounts, err := s.accounts(ctx)
		if err != nil {
			return Outlook{}, err
		}
		out.BalancesShown, out.Accounts = true, accounts
		out.BankStaleAfter = s.BankStaleAfter
		if out.BankStaleAfter <= 0 {
			out.BankStaleAfter = banking.DefaultStaleAfter
		}
		for _, a := range accounts {
			if a.NotCounted != "" {
				continue
			}
			for _, b := range a.Balances {
				l := line(b.Amount.Currency(), b.Amount.Amount().Scale())
				if l.Balance == nil {
					v := b.Amount
					l.Balance = &v
				} else {
					v, _ := l.Balance.Add(b.Amount)
					l.Balance = &v
				}
				l.Accounts++
				if a.Freshness.Stale {
					l.StaleAccounts++
				}
				asOf := b.ReferenceDate
				if asOf.IsZero() {
					obtained, zerr := s.zone().DateOf(b.ObtainedAt)
					if zerr != nil {
						return Outlook{}, fmt.Errorf("outlook: balance date: %w", zerr)
					}
					asOf = obtained
				}
				if l.BalanceAsOf.IsZero() || asOf.Before(l.BalanceAsOf) {
					l.BalanceAsOf = asOf
				}
				if l.OldestBalanceAt == nil || b.ObtainedAt.Before(*l.OldestBalanceAt) {
					at := b.ObtainedAt
					l.OldestBalanceAt = &at
				}
			}
		}
	}

	codes := make([]money.Currency, 0, len(lines))
	for c := range lines {
		codes = append(codes, c)
	}
	slices.SortFunc(codes, func(a, b money.Currency) int { return strings.Compare(a.Code(), b.Code()) })
	for _, c := range codes {
		l := lines[c]
		l.Upcoming, _ = l.StillDue.Sub(l.Overdue)
		if l.Balance != nil {
			d, _ := l.Balance.Sub(l.StillDue)
			l.BalanceLessStillDue = &d
			z, _ := money.Zero(c, l.Balance.Amount().Scale())
			after := PaymentsAfter{Amount: z}
			for _, p := range evidence[c] {
				if p.date.After(l.BalanceAsOf) {
					after.Count++
					// Same currency: Add cannot fail.
					after.Amount, _ = after.Amount.Add(p.amount)
				}
			}
			l.PaymentsAfterBalance = &after
		}
		out.Currencies = append(out.Currencies, *l)
	}

	if !r.Currency.IsZero() {
		conv, err := s.convert(ctx, r.Currency, out.Currencies, out.BalancesShown)
		if err != nil {
			return Outlook{}, err
		}
		out.Converted = &conv
	}
	return out, nil
}

// zone is the reporting zone, UTC when none was set.
func (s Service) zone() calendar.Zone {
	if s.Zone.IsZero() {
		return calendar.MustLoadZone("UTC")
	}
	return s.Zone
}

// firstMonth is the month of the earliest commitment start.
func (s Service) firstMonth(ctx context.Context) (calendar.YearMonth, bool, error) {
	var first calendar.YearMonth
	found := false
	after := ""
	for {
		page, err := s.Commitments.List(ctx, commitments.ListFilter{After: after, Limit: pageSize})
		if err != nil {
			return calendar.YearMonth{}, false, fmt.Errorf("outlook: listing commitments: %w", err)
		}
		more := len(page) > pageSize
		if more {
			page = page[:pageSize]
		}
		for _, c := range page {
			m := c.StartDate.YearMonth()
			if !found || m.Compare(first) < 0 {
				first, found = m, true
			}
		}
		if !more || len(page) == 0 {
			return first, found, nil
		}
		after = page[len(page)-1].ID
	}
}

// accounts reads every stored account with its counted balances and
// freshness, ordered by connection and account ID.
func (s Service) accounts(ctx context.Context) ([]Account, error) {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	at := now()
	conns, err := s.Banks.Connections(ctx)
	if err != nil {
		return nil, fmt.Errorf("outlook: listing bank connections: %w", err)
	}
	slices.SortFunc(conns, func(a, b banking.Connection) int { return strings.Compare(string(a.ID), string(b.ID)) })
	out := []Account{}
	for _, c := range conns {
		accts, err := s.Banks.Accounts(ctx, c.ID)
		if err != nil {
			return nil, fmt.Errorf("outlook: listing accounts: %w", err)
		}
		slices.SortFunc(accts, func(a, b banking.StoredAccount) int { return strings.Compare(string(a.ID), string(b.ID)) })
		for _, sa := range accts {
			bals, err := s.Banks.Balances(ctx, sa.ID)
			if err != nil {
				return nil, fmt.Errorf("outlook: reading balances: %w", err)
			}
			sync, err := s.Banks.AccountSync(ctx, sa.ID)
			if err != nil {
				return nil, fmt.Errorf("outlook: reading refresh state: %w", err)
			}
			a := Account{ID: sa.ID, ConnectionID: c.ID, Name: sa.Name, Institution: c.Institution.Name, Currency: sa.Currency,
				Balances: Latest(bals), Freshness: banking.AccountFreshness(c.Status, sync, at, s.BankStaleAfter)}
			switch {
			case !sa.NoLongerReportedAt.IsZero():
				a.NotCounted = NotCountedNoLongerReported
			case len(bals) == 0:
				a.NotCounted = NotCountedNoBalance
			case len(a.Balances) == 0:
				a.NotCounted = NotCountedOtherType
			}
			out = append(out, a)
		}
	}
	return out, nil
}

// Latest picks the balances to count from an account's snapshots: per
// currency, the most recently obtained fetch, and in it the first type of
// [BalancePreference]. Balances are append-only snapshots, so re-importing
// the same figures adds snapshots but never a second counted balance.
// Results are sorted by currency code.
func Latest(all []banking.StoredBalance) []Balance {
	newest := map[money.Currency]time.Time{}
	for _, b := range all {
		if !slices.Contains(BalancePreference, b.Type) {
			continue
		}
		cur := b.Amount.Currency()
		if t, ok := newest[cur]; !ok || b.ObtainedAt.After(t) {
			newest[cur] = b.ObtainedAt
		}
	}
	chosen := map[money.Currency]banking.StoredBalance{}
	for _, b := range all {
		cur := b.Amount.Currency()
		rank := slices.Index(BalancePreference, b.Type)
		if rank < 0 || !b.ObtainedAt.Equal(newest[cur]) {
			continue
		}
		if have, ok := chosen[cur]; !ok || rank < slices.Index(BalancePreference, have.Type) ||
			rank == slices.Index(BalancePreference, have.Type) && betterBalance(b, have) {
			chosen[cur] = b
		}
	}
	out := make([]Balance, 0, len(chosen))
	for _, b := range chosen {
		out = append(out, Balance{Balance: b.Balance, ObtainedAt: b.ObtainedAt})
	}
	slices.SortFunc(out, func(a, b Balance) int { return strings.Compare(a.Amount.Currency().Code(), b.Amount.Currency().Code()) })
	return out
}

// providerRank orders provider balance codes of the same type: ITBD
// (interim booked) over CLBD (closing booked), OPBD (opening booked) and
// PRCD (previous closing); unknown codes rank last.
var providerRank = []string{"ITBD", "CLBD", "OPBD", "PRCD"}

func providerRankOf(code string) int {
	if i := slices.Index(providerRank, strings.ToUpper(strings.TrimSpace(code))); i >= 0 {
		return i
	}
	return len(providerRank)
}

// betterBalance reports whether a should be counted over b, both of the same
// preference type and fetch: provider type rank, then the newest reference
// date, then the provider code and the amount text. The stored ID is random
// and never decides, so identical bank data counts the same balance after
// every re-import.
func betterBalance(a, b banking.StoredBalance) bool {
	if ra, rb := providerRankOf(a.ProviderType), providerRankOf(b.ProviderType); ra != rb {
		return ra < rb
	}
	if c := a.ReferenceDate.Compare(b.ReferenceDate); c != 0 {
		return c > 0
	}
	if c := strings.Compare(a.ProviderType, b.ProviderType); c != 0 {
		return c < 0
	}
	return a.Amount.String() > b.Amount.String()
}

// convert builds the secondary aggregate in to. Each currency line is
// converted as a whole (one rounding per figure, half-even, by the rate
// service); a currency without an acceptable rate is excluded entirely, so
// its balance and its commitments are never half counted.
func (s Service) convert(ctx context.Context, to money.Currency, lines []CurrencyLine, balances bool) (Converted, error) {
	scale := int32(0)
	if d, ok := to.MinorUnits(); ok {
		scale = d
	}
	zero, err := money.Zero(to, scale)
	if err != nil {
		return Converted{}, fmt.Errorf("outlook: reporting currency: %w", err)
	}
	out := Converted{Currency: to, StillDue: zero, Overdue: zero, Included: []RateUse{}, Excluded: []Exclusion{}}
	var balance *money.Money
	parent := ctx
	if s.ConvertTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.ConvertTimeout)
		defer cancel()
	}
	for _, l := range lines {
		figures := []money.Money{l.StillDue, l.Overdue}
		if balances && l.Balance != nil {
			figures = append(figures, *l.Balance)
		}
		values := make([]money.Money, len(figures))
		var use RateUse
		var excl *Exclusion
		// One rate per currency, taken once: every figure of the line is
		// converted with it, so the reported provenance is the rate used
		// for all of them even if a refresh lands meanwhile.
		var c fx.Conversion
		var err error
		// The reporting currency itself needs no rate, so it always converts.
		timedOut := l.Currency != to && ctx.Err() != nil && parent.Err() == nil
		if !timedOut {
			c, err = s.conversion(ctx, figures[0], to)
			timedOut = err != nil && ctx.Err() != nil && parent.Err() == nil
		}
		switch {
		case timedOut:
			excl = &Exclusion{Currency: l.Currency, Status: fx.StatusMissing, Reason: ReasonConversionTimedOut}
		case err != nil:
			excl = &Exclusion{Currency: l.Currency, Status: fx.StatusMissing, Reason: ReasonConversionFailed}
		case !c.Acceptable():
			excl = &Exclusion{Currency: l.Currency, Status: c.Status, Reason: string(c.Reason), Rate: c.Rate}
		default:
			values[0] = c.Value
			use = RateUse{Currency: l.Currency, Rate: c.Rate, Age: c.Age, FromCache: c.FromCache, RefreshFailed: c.RefreshError != nil}
			if c.Identity {
				use.Rate, use.Age, use.FromCache = nil, 0, false
			}
			for i := 1; i < len(figures); i++ {
				if values[i], err = applyRate(figures[i], c, to); err != nil {
					excl = &Exclusion{Currency: l.Currency, Status: fx.StatusMissing, Reason: ReasonConversionFailed}
					break
				}
			}
		}
		if excl != nil {
			out.Excluded = append(out.Excluded, *excl)
			continue
		}
		out.Included = append(out.Included, use)
		if len(values) == 3 {
			out.BalanceAccounts += l.Accounts
			out.StaleBalanceAccounts += l.StaleAccounts
		}
		out.StillDue, _ = out.StillDue.Add(values[0])
		out.Overdue, _ = out.Overdue.Add(values[1])
		if len(values) == 3 {
			if balance == nil {
				v := values[2]
				balance = &v
			} else {
				v, _ := balance.Add(values[2])
				balance = &v
			}
		}
	}
	if balance != nil {
		out.Balance = balance
		d, _ := balance.Sub(out.StillDue)
		out.BalanceLessStillDue = &d
	}
	return out, nil
}

// applyRate converts a figure with the rate of c, rounding like the rate
// service does: half-even to the minor units of to.
func applyRate(from money.Money, c fx.Conversion, to money.Currency) (money.Money, error) {
	if c.Identity || c.Rate == nil {
		return from, nil
	}
	v := from.Amount().Mul(c.Rate.Value)
	if digits, ok := to.MinorUnits(); ok {
		v = v.RoundHalfEven(digits)
	}
	return money.New(v, to)
}

// conversion converts one figure; without a rate service only the
// reporting currency itself converts (identity) and every other currency is
// missing with [fx.ReasonNoProvider].
func (s Service) conversion(ctx context.Context, from money.Money, to money.Currency) (fx.Conversion, error) {
	if s.Rates != nil {
		return s.Rates.Convert(ctx, from, to)
	}
	c := fx.Conversion{From: from, To: to}
	if from.Currency() == to {
		c.Status, c.Identity, c.Value, c.Exact = fx.StatusOK, true, from, from
		return c, nil
	}
	c.Status, c.Reason = fx.StatusMissing, fx.ReasonNoProvider
	return c, nil
}
