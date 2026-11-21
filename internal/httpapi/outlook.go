package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/fx"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/outlook"
)

// OutlookPath is the outlook against recorded commitments (task FX3,
// ADR-0044).
const OutlookPath = APIPrefix + "/outlook"

// grantOutlookBalances lets a caller of the outlook see bank balances and
// account freshness: the grant that reads bank connections, their accounts
// and balances (task B4). Without it the outlook shows commitments only.
var grantOutlookBalances = authz.G(authz.ResourceConnections, authz.ActionRead)

type outlookRateJSON struct {
	Provider         string         `json:"provider"`
	Source           string         `json:"source"`
	Base             money.Currency `json:"base"`
	Quote            money.Currency `json:"quote"`
	Value            money.Decimal  `json:"value"`
	Date             calendar.Date  `json:"date"`
	RetrievedAt      time.Time      `json:"retrieved_at"`
	ConfirmedThrough calendar.Date  `json:"confirmed_through"`
}

func toOutlookRate(r *fx.Rate) *outlookRateJSON {
	if r == nil {
		return nil
	}
	return &outlookRateJSON{Provider: r.Provider, Source: r.Source, Base: r.Pair.Base, Quote: r.Pair.Quote, Value: r.Value, Date: r.Date,
		RetrievedAt: r.RetrievedAt.UTC(), ConfirmedThrough: r.ConfirmedThrough}
}

type outlookLineJSON struct {
	Currency                money.Currency `json:"currency"`
	StillDue                money.Money    `json:"still_due"`
	Overdue                 money.Money    `json:"overdue"`
	Upcoming                money.Money    `json:"upcoming"`
	OverdueCount            int            `json:"overdue_count"`
	UnconvertedEvidence     int            `json:"unconverted_evidence_count"`
	Balance                 *money.Money   `json:"balance"`
	BalanceLessStillDue     *money.Money   `json:"balance_less_still_due"`
	AccountCount            int            `json:"account_count"`
	StaleAccountCount       int            `json:"stale_account_count"`
	OldestBalanceObtainedAt *time.Time     `json:"oldest_balance_obtained_at"`
}

type outlookBalanceJSON struct {
	Type          banking.BalanceType `json:"type"`
	ProviderType  string              `json:"provider_type"`
	Amount        money.Money         `json:"amount"`
	ReferenceDate *calendar.Date      `json:"reference_date"`
	ObtainedAt    time.Time           `json:"obtained_at"`
}

type outlookFreshnessJSON struct {
	State         banking.FreshnessState `json:"state"`
	Stale         bool                   `json:"stale"`
	LastSuccessAt *time.Time             `json:"last_success_at"`
	LastAttemptAt *time.Time             `json:"last_attempt_at"`
	LastErrorKind *string                `json:"last_error_kind"`
	LastErrorCode *string                `json:"last_error_code"`
	NoticeCode    *string                `json:"notice_code"`
}

type outlookAccountJSON struct {
	ID               banking.AccountID    `json:"id"`
	ConnectionID     banking.ConnectionID `json:"connection_id"`
	Name             string               `json:"name"`
	Institution      string               `json:"institution"`
	Currency         money.Currency       `json:"currency"`
	Balances         []outlookBalanceJSON `json:"balances"`
	Counted          bool                 `json:"counted"`
	NotCountedReason *outlook.NotCounted  `json:"not_counted_reason"`
	Freshness        outlookFreshnessJSON `json:"freshness"`
}

type outlookIncludedJSON struct {
	Currency      money.Currency   `json:"currency"`
	Rate          *outlookRateJSON `json:"rate"`
	AgeSeconds    *int64           `json:"age_seconds"`
	FromCache     bool             `json:"from_cache"`
	RefreshFailed bool             `json:"refresh_failed"`
}

type outlookExcludedJSON struct {
	Currency money.Currency   `json:"currency"`
	Status   fx.Status        `json:"status"`
	Reason   string           `json:"reason"`
	Rate     *outlookRateJSON `json:"rate"`
}

type outlookConvertedJSON struct {
	Currency             money.Currency        `json:"currency"`
	Complete             bool                  `json:"complete"`
	StillDue             money.Money           `json:"still_due"`
	Overdue              money.Money           `json:"overdue"`
	Balance              *money.Money          `json:"balance"`
	BalanceLessStillDue  *money.Money          `json:"balance_less_still_due"`
	BalanceAccounts      int                   `json:"balance_account_count"`
	StaleBalanceAccounts int                   `json:"stale_balance_account_count"`
	Included             []outlookIncludedJSON `json:"included"`
	Excluded             []outlookExcludedJSON `json:"excluded"`
}

type outlookJSON struct {
	AsOf   calendar.Date `json:"as_of"`
	Notice string        `json:"notice"`
	Window struct {
		From      *calendar.YearMonth `json:"from"`
		Through   calendar.YearMonth  `json:"through"`
		Truncated bool                `json:"truncated"`
	} `json:"window"`
	Currencies []outlookLineJSON `json:"currencies"`
	Balances   struct {
		Shown bool   `json:"shown"`
		Note  string `json:"note"`
	} `json:"balances"`
	Accounts       []outlookAccountJSON  `json:"accounts"`
	Converted      *outlookConvertedJSON `json:"converted"`
	PlannedOutflow struct {
		Included bool   `json:"included"`
		Note     string `json:"note"`
	} `json:"planned_outflows"`
	Freshness struct {
		ComputedAt           time.Time  `json:"computed_at"`
		OccurrencesChangedAt *time.Time `json:"occurrences_changed_at"`
	} `json:"freshness"`
}

const (
	outlookBalancesShownNote  = "Balances are the latest each bank reported; check each account's freshness before relying on them."
	outlookBalancesGrantNote  = "Bank balances are not shown: they need the connections:read grant."
	outlookBalancesSourceNote = "Bank balances are not available on this server."
	outlookPlannedNote        = "Planned outflows (deferral plans) are not part of Ledger yet; only recorded commitments are compared."
)

func optTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func toOutlookJSON(o outlook.Outlook, balancesAllowed, balancesAvailable bool, now time.Time) outlookJSON {
	out := outlookJSON{AsOf: o.AsOf, Notice: outlook.Notice, Currencies: make([]outlookLineJSON, len(o.Currencies)), Accounts: make([]outlookAccountJSON, len(o.Accounts))}
	if !o.From.IsZero() {
		from := o.From
		out.Window.From = &from
	}
	out.Window.Through, out.Window.Truncated = o.Through, o.Truncated
	for i, l := range o.Currencies {
		out.Currencies[i] = outlookLineJSON{Currency: l.Currency, StillDue: l.StillDue, Overdue: l.Overdue, Upcoming: l.Upcoming, OverdueCount: l.OverdueCount,
			UnconvertedEvidence: l.UnconvertedEvidence, Balance: l.Balance, BalanceLessStillDue: l.BalanceLessStillDue,
			AccountCount: l.Accounts, StaleAccountCount: l.StaleAccounts, OldestBalanceObtainedAt: l.OldestBalanceAt}
	}
	out.Balances.Shown = o.BalancesShown
	switch {
	case o.BalancesShown:
		out.Balances.Note = outlookBalancesShownNote
	case !balancesAllowed:
		out.Balances.Note = outlookBalancesGrantNote
	case !balancesAvailable:
		out.Balances.Note = outlookBalancesSourceNote
	}
	for i, a := range o.Accounts {
		aj := outlookAccountJSON{ID: a.ID, ConnectionID: a.ConnectionID, Name: a.Name, Institution: a.Institution, Currency: a.Currency,
			Balances: make([]outlookBalanceJSON, len(a.Balances)), Counted: a.NotCounted == "",
			Freshness: outlookFreshnessJSON{State: a.Freshness.State, Stale: a.Freshness.Stale, LastSuccessAt: optTime(a.Freshness.LastSuccessAt),
				LastAttemptAt: optTime(a.Freshness.LastAttemptAt), LastErrorKind: optString(string(a.Freshness.LastErrorKind)),
				LastErrorCode: optString(a.Freshness.LastErrorCode), NoticeCode: optString(a.Freshness.NoticeCode)}}
		if a.NotCounted != "" {
			nc := a.NotCounted
			aj.NotCountedReason = &nc
		}
		for j, b := range a.Balances {
			bj := outlookBalanceJSON{Type: b.Type, ProviderType: b.ProviderType, Amount: b.Amount, ObtainedAt: b.ObtainedAt.UTC()}
			if !b.ReferenceDate.IsZero() {
				ref := b.ReferenceDate
				bj.ReferenceDate = &ref
			}
			aj.Balances[j] = bj
		}
		out.Accounts[i] = aj
	}
	if c := o.Converted; c != nil {
		cj := &outlookConvertedJSON{Currency: c.Currency, Complete: c.Complete(), StillDue: c.StillDue, Overdue: c.Overdue, Balance: c.Balance,
			BalanceLessStillDue: c.BalanceLessStillDue, BalanceAccounts: c.BalanceAccounts, StaleBalanceAccounts: c.StaleBalanceAccounts, Included: make([]outlookIncludedJSON, len(c.Included)), Excluded: make([]outlookExcludedJSON, len(c.Excluded))}
		for i, u := range c.Included {
			ij := outlookIncludedJSON{Currency: u.Currency, Rate: toOutlookRate(u.Rate), FromCache: u.FromCache, RefreshFailed: u.RefreshFailed}
			if u.Rate != nil {
				age := int64(u.Age / time.Second)
				ij.AgeSeconds = &age
			}
			cj.Included[i] = ij
		}
		for i, x := range c.Excluded {
			cj.Excluded[i] = outlookExcludedJSON{Currency: x.Currency, Status: x.Status, Reason: x.Reason, Rate: toOutlookRate(x.Rate)}
		}
		out.Converted = cj
	}
	out.PlannedOutflow.Note = outlookPlannedNote
	out.Freshness.ComputedAt = now.UTC()
	out.Freshness.OccurrencesChangedAt = o.OccurrencesChangedAt
	return out
}

// registerOutlook registers GET /outlook (task FX3, ADR-0044). It needs
// commitments:read like the monthly view it builds on; balances are added
// only for a caller that also holds connections:read. reporting is the
// default currency of the converted aggregate (zero: none unless asked).
func registerOutlook(s *Server, svc outlook.Service, reporting money.Currency, now func() time.Time) {
	if now == nil {
		now = time.Now
	}
	s.HandleAPI("GET "+OutlookPath, Requires(grantCommitmentsRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		for k := range q {
			if k != "through" && k != "currency" {
				WriteError(w, r, Validation(k, "unknown parameter "+k+"; the outlook takes through and currency"))
				return
			}
		}
		req := outlook.Request{Currency: reporting}
		if raw, ok := q["through"]; ok {
			if len(raw) != 1 {
				WriteError(w, r, Validation("through", "through may be given once, such as through=2026-12"))
				return
			}
			m, err := calendar.ParseYearMonth(raw[0])
			if err != nil {
				WriteError(w, r, Validation("through", "through must be a month such as 2026-12"))
				return
			}
			req.Through = m
		}
		if raw, ok := q["currency"]; ok {
			if len(raw) != 1 {
				WriteError(w, r, Validation("currency", "currency may be given once, such as currency=EUR"))
				return
			}
			c, err := money.ParseCurrency(raw[0])
			if err != nil {
				WriteError(w, r, Validation("currency", "currency must be an ISO 4217 code such as EUR"))
				return
			}
			req.Currency = c
		}
		p, _ := authz.FromContext(r.Context())
		allowed := p.Allows(grantOutlookBalances)
		req.Balances = allowed
		o, err := svc.Build(r.Context(), req)
		if err != nil {
			if errors.Is(err, outlook.ErrBeyondHorizon) {
				err = Validation("through", fmt.Sprintf("through must be within %d months after the current month", occurrences.MaxMonthsAhead))
			}
			WriteError(w, r, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		WriteJSON(w, http.StatusOK, toOutlookJSON(o, allowed, svc.Banks != nil, now()))
	}))
}
