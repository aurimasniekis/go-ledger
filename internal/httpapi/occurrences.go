package httpapi

import (
	"context"
	"errors"
	"fmt"
	"github.com/aurimasniekis/go-ledger/internal/domain/organisation"
	"net/http"
	"slices"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/deferrals"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/monthview"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/outlook"
	"github.com/aurimasniekis/go-ledger/internal/domain/payments"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
)

// OccurrencesPath is the occurrence collection (task C2, ADR-0027).
const OccurrencesPath = APIPrefix + "/occurrences"

// occurrenceJSON is the API representation of an occurrence with its derived
// payment state (task C3, ADR-0031).
type occurrenceJSON struct {
	ID           string             `json:"id"`
	CommitmentID string             `json:"commitment_id"`
	Month        calendar.YearMonth `json:"month"`
	DueDate      calendar.Date      `json:"due_date"`
	Amount       money.Money        `json:"amount"`
	Version      int64              `json:"version"`
	CreatedAt    time.Time          `json:"created_at"`
	UpdatedAt    time.Time          `json:"updated_at"`
	// CancelledAt and CancelledReason are null unless the occurrence was
	// cancelled (D31).
	CancelledAt     *time.Time       `json:"cancelled_at"`
	CancelledReason *string          `json:"cancelled_reason"`
	Payment         paymentStateJSON `json:"payment"`
}

func toOccurrenceJSON(o occurrences.Occurrence, st payments.State) occurrenceJSON {
	out := occurrenceJSON{ID: o.ID, CommitmentID: o.CommitmentID, Month: o.Month, DueDate: o.DueDate, Amount: o.Amount,
		Version: o.Version, CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt, CancelledAt: o.CancelledAt, Payment: toPaymentStateJSON(st)}
	if o.CancelledAt != nil {
		out.CancelledReason = &o.CancelledReason
	}
	return out
}

var occurrencesListSpec = withOrgFilters(ListSpec{Filters: map[string]FilterSpec{
	"month": {Ops: []FilterOp{OpEq}, Check: func(v string) error {
		if _, err := calendar.ParseYearMonth(v); err != nil {
			return errors.New("month must be a month such as 2026-11")
		}
		return nil
	}},
	"commitment_id": {Ops: []FilterOp{OpEq}, Check: func(v string) error {
		if !resource.ValidID(v) {
			return errors.New("commitment_id must be a commitment identifier")
		}
		return nil
	}},
	"include_cancelled": {Ops: []FilterOp{OpEq}, Check: func(v string) error {
		if v != "true" && v != "false" {
			return errors.New("include_cancelled must be true or false")
		}
		return nil
	}},
}})

// OccurrencesSummaryPath is the monthly view (task C4, ADR-0034).
const OccurrencesSummaryPath = OccurrencesPath + "/summary"

// monthTotalsJSON is the exact totals of one currency. Every figure is a Money
// in that currency; nothing is summed across currencies.
type monthTotalsJSON struct {
	Currency             money.Currency `json:"currency"`
	Due                  money.Money    `json:"due"`
	Paid                 money.Money    `json:"paid"`
	Outstanding          money.Money    `json:"outstanding"`
	Overdue              money.Money    `json:"overdue"`
	Overpaid             money.Money    `json:"overpaid"`
	OccurrenceCount      int            `json:"occurrence_count"`
	OverdueCount         int            `json:"overdue_count"`
	UnconvertedEvidenceN int            `json:"unconverted_evidence_count"`
	// Planned is what is planned to be paid this month (DF1, ADR-0050):
	// outstanding - deferred_out + carried_in + adjustments (fees still
	// planned: neither removed nor marked paid, whether or not the principal is
	// paid). Each outstanding amount is planned in exactly one month.
	Planned     money.Money `json:"planned"`
	DeferredOut money.Money `json:"deferred_out"`
	CarriedIn   money.Money `json:"carried_in"`
	Adjustments money.Money `json:"adjustments"`
}

// monthFreshnessJSON says how current the view is. Bank data age is not part
// of it yet: no bank-connection endpoint exists to read it from.
type monthFreshnessJSON struct {
	// ComputedAt is when this response was derived; statuses are derived on
	// every read.
	ComputedAt time.Time `json:"computed_at"`
	// OccurrencesChangedAt is the newest update among the month's
	// occurrences; null for an empty month.
	OccurrencesChangedAt *time.Time    `json:"occurrences_changed_at"`
	Bank                 monthBankJSON `json:"bank"`
}

// monthBankJSON is what the month view knows about bank data. Available is
// true when at least one bank connection exists (connected or not); State is
// the worst freshness among them, so a disconnected or stale connection is
// never reported as fresh. State is "none" without a connection and "unknown"
// when the connections could not be read.
type monthBankJSON struct {
	Available     bool       `json:"available"`
	Connections   int        `json:"connections"`
	State         string     `json:"state"`
	LastSuccessAt *time.Time `json:"last_success_at"`
	Note          string     `json:"note"`
}

// monthBank derives the bank part of the freshness from the stored facts.
func monthBank(ctx context.Context, banks outlook.Banks, at time.Time, staleAfter time.Duration) monthBankJSON {
	out := monthBankJSON{State: "none", Note: bankNoneNote}
	if banks == nil {
		return out
	}
	unknown := monthBankJSON{State: "unknown", Note: bankUnknownNote}
	conns, err := banks.Connections(ctx)
	if err != nil {
		return unknown
	}
	if len(conns) == 0 {
		return out
	}
	out.Available, out.Connections = true, len(conns)
	worst, rank := banking.FreshnessState(""), len(freshnessRank)
	var last time.Time
	consider := func(st banking.FreshnessState) {
		if r := freshnessRank[st]; r < rank {
			worst, rank = st, r
		}
	}
	for _, c := range conns {
		accts, err := banks.Accounts(ctx, c.ID)
		if err != nil {
			return unknown
		}
		live := 0
		for _, a := range accts {
			if !a.NoLongerReportedAt.IsZero() {
				continue
			}
			live++
			sync, err := banks.AccountSync(ctx, a.ID)
			if err != nil {
				return unknown
			}
			f := banking.AccountFreshness(c.Status, sync, at, staleAfter)
			consider(f.State)
			if f.LastSuccessAt.After(last) {
				last = f.LastSuccessAt
			}
		}
		if live == 0 { // no account yet: the connection's own status decides
			consider(connectionOwnFreshness(c.Status))
		}
	}
	out.State = string(worst)
	out.LastSuccessAt = timePtr(last)
	out.Note = bankNote[worst]
	return out
}

// monthItemJSON is an occurrence of the monthly view: the occurrence plus the
// name of its commitment, so a row can be read without a second request.
type monthItemJSON struct {
	occurrenceJSON
	CommitmentName string `json:"commitment_name"`
	// Deferral is the occurrence's active deferral plan (DF1), null when
	// none. It never changes the payment state beside it.
	Deferral *deferralPlanJSON `json:"deferral"`
}

type monthSummaryJSON struct {
	Month calendar.YearMonth `json:"month"`
	AsOf  calendar.Date      `json:"as_of"`
	Items []monthItemJSON    `json:"items"`
	// CarriedIn are occurrences of earlier months whose active deferral plan
	// falls in this month (DF1). They keep their own month and due date and
	// count only in the planned figures.
	CarriedIn []monthItemJSON    `json:"carried_in"`
	Totals    []monthTotalsJSON  `json:"totals"`
	Freshness monthFreshnessJSON `json:"freshness"`
	// Filter is set when the month was restricted by category or label: the
	// rows and the totals then cover only the matching commitments (task O3).
	Filter *orgFilterJSON `json:"filter"`
}

const (
	bankNoneNote    = "No bank connection exists; the totals cover recorded commitments and manual payments only."
	bankUnknownNote = "Bank connections could not be read just now, so their data age is unknown."
)

// bankNote says what the worst freshness state means for what the view shows.
var bankNote = map[banking.FreshnessState]string{
	banking.FreshnessFresh:             "Bank data is current.",
	banking.FreshnessStale:             "Bank data is stale: the latest successful fetch is older than the staleness limit.",
	banking.FreshnessNeverRefreshed:    "A bank is connected but no data has been fetched yet.",
	banking.FreshnessFailed:            "The latest bank fetch failed; the data may be out of date.",
	banking.FreshnessDisconnected:      "A bank connection is disconnected; its data is not being updated.",
	banking.FreshnessReconnectRequired: "A bank connection needs a new consent; its data is not being updated.",
}

func toMonthSummaryJSON(s monthview.Summary, now time.Time, bank monthBankJSON) monthSummaryJSON {
	out := monthSummaryJSON{Month: s.Month, AsOf: s.AsOf, Items: make([]monthItemJSON, len(s.Rows)), CarriedIn: make([]monthItemJSON, len(s.CarriedIn)), Totals: make([]monthTotalsJSON, len(s.Totals))}
	item := func(r monthview.Row) monthItemJSON {
		it := monthItemJSON{occurrenceJSON: toOccurrenceJSON(r.Occurrence, r.State), CommitmentName: r.CommitmentName}
		if r.Deferral != nil {
			d := toDeferralPlanJSON(*r.Deferral, r.Occurrence.DueDate, r.State, s.AsOf)
			it.Deferral = &d
		}
		return it
	}
	for i, r := range s.Rows {
		out.Items[i] = item(r)
	}
	for i, r := range s.CarriedIn {
		out.CarriedIn[i] = item(r)
	}
	for i, t := range s.Totals {
		out.Totals[i] = monthTotalsJSON{Currency: t.Currency, Due: t.Due, Paid: t.Paid, Outstanding: t.Outstanding, Overdue: t.Overdue, Overpaid: t.Overpaid,
			OccurrenceCount: t.Occurrences, OverdueCount: t.OverdueCount, UnconvertedEvidenceN: t.Unconverted,
			Planned: t.Planned, DeferredOut: t.DeferredOut, CarriedIn: t.CarriedIn, Adjustments: t.Adjustments}
	}
	out.Freshness.ComputedAt = now.UTC()
	out.Freshness.OccurrencesChangedAt = s.LatestChange
	out.Freshness.Bank = bank
	return out
}

// Occurrences are derived from commitment definitions, so reading them needs
// the commitments read grant; there is no separate occurrence grant.
func registerOccurrences(s *Server, org organisation.Store, st occurrences.Reader, pay payments.Store, cs commitments.Store, plans deferrals.Reader, todayFn today, grace payments.GraceFunc, now func() time.Time, banks outlook.Banks, bankStaleAfter time.Duration) {
	if now == nil {
		now = time.Now
	}
	s.HandleAPI("GET "+OccurrencesPath, Requires(grantCommitmentsRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, err := ParseList(r, occurrencesListSpec)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		months := q.Filter("month", OpEq)
		if len(months) != 1 {
			WriteError(w, r, Validation("month", "month is required, once, such as month=2026-11"))
			return
		}
		month, _ := calendar.ParseYearMonth(months[0]) // checked by the spec
		t, err := todayFn()
		if err != nil {
			WriteError(w, r, Internal(err))
			return
		}
		if month.Compare(occurrences.HorizonLimit(t)) > 0 {
			WriteError(w, r, Validation("month", fmt.Sprintf("month must be within %d months after the current month", occurrences.MaxMonthsAhead)))
			return
		}
		inc := q.Filter("include_cancelled", OpEq)
		if len(inc) > 1 {
			WriteError(w, r, Validation("include_cancelled", "include_cancelled cannot be both true and false"))
			return
		}
		commitmentIDs := q.Filter("commitment_id", OpEq)
		// The category and label filters are those of the commitment.
		if ids, active, err := selectOrganised(r, org, organisation.ObjectCommitment, q.Filter("category_id", OpEq), q.Filter("label_id", OpEq)); err != nil {
			WriteError(w, r, err)
			return
		} else if active {
			if len(commitmentIDs) > 0 {
				ids = intersectIDs(commitmentIDs, ids)
			}
			if len(ids) == 0 {
				WriteJSON(w, http.StatusOK, NewPage([]occurrenceJSON{}, q, func(o occurrenceJSON) string { return o.ID }))
				return
			}
			commitmentIDs = ids
		}
		items, err := st.List(r.Context(), occurrences.ListFilter{Month: month, CommitmentIDs: commitmentIDs, After: q.After, Limit: q.Limit,
			IncludeCancelled: slices.Contains(inc, "true")})
		if err != nil {
			if errors.Is(err, occurrences.ErrCursorGone) {
				err = Validation("cursor", "cursor is not valid; start again from the first page")
			}
			WriteError(w, r, err)
			return
		}
		ids := make([]string, len(items))
		for i, o := range items {
			ids[i] = o.ID
		}
		facts, err := pay.Facts(r.Context(), ids)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]occurrenceJSON, len(items))
		for i, o := range items {
			out[i] = toOccurrenceJSON(o, payments.Derive(r.Context(), o, facts[o.ID], t, grace))
		}
		WriteJSON(w, http.StatusOK, NewPage(out, q, func(o occurrenceJSON) string { return o.ID }))
	}))
	view := monthview.Service{Occurrences: st, Payments: pay, Grace: grace, Commitments: cs, Today: todayFn, Deferrals: plans}
	s.HandleAPI("GET "+OccurrencesSummaryPath, Requires(grantCommitmentsRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k := range r.URL.Query() {
			if k != "month" && k != "category_id" && k != "label_id" {
				WriteError(w, r, Validation(k, "unknown parameter "+k+"; the summary takes month, category_id and label_id"))
				return
			}
		}
		categories, labels := r.URL.Query()["category_id"], r.URL.Query()["label_id"]
		for _, c := range categories {
			if !resource.ValidID(c) {
				WriteError(w, r, Validation("category_id", "category_id must be a category identifier"))
				return
			}
		}
		for _, l := range labels {
			if !resource.ValidID(l) {
				WriteError(w, r, Validation("label_id", "label_id must be a label identifier"))
				return
			}
		}
		raw := r.URL.Query()["month"]
		if len(raw) != 1 {
			WriteError(w, r, Validation("month", "month is required, once, such as month=2026-11"))
			return
		}
		month, err := calendar.ParseYearMonth(raw[0])
		if err != nil {
			WriteError(w, r, Validation("month", "month must be a month such as 2026-11"))
			return
		}
		var only map[string]struct{}
		ids, active, err := selectOrganised(r, org, organisation.ObjectCommitment, categories, labels)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		if active {
			only = make(map[string]struct{}, len(ids))
			for _, id := range ids {
				only[id] = struct{}{}
			}
		}
		today, err := todayFn()
		if err != nil {
			WriteError(w, r, Internal(err))
			return
		}
		sum, err := view.MonthScoped(r.Context(), month, today, only)
		if err != nil {
			if errors.Is(err, occurrences.ErrBeyondHorizon) {
				err = Validation("month", fmt.Sprintf("month must be within %d months after the current month", occurrences.MaxMonthsAhead))
			}
			WriteError(w, r, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		res := toMonthSummaryJSON(sum, now(), monthBank(r.Context(), banks, now(), bankStaleAfter))
		res.Filter = orgFilterEcho(categories, labels, "occurrences of commitments")
		WriteJSON(w, http.StatusOK, res)
	}))
	s.HandleAPI("GET "+OccurrencesPath+"/{id}", Requires(grantCommitmentsRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o, err := st.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		t, err := todayFn()
		if err != nil {
			WriteError(w, r, Internal(err))
			return
		}
		facts, err := pay.Facts(r.Context(), []string{o.ID})
		if err != nil {
			WriteError(w, r, err)
			return
		}
		SetVersion(w, o.Version)
		WriteJSON(w, http.StatusOK, toOccurrenceJSON(o, payments.Derive(r.Context(), o, facts[o.ID], t, grace)))
	}))
}
