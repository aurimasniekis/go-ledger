package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/monthview"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
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

var occurrencesListSpec = ListSpec{Filters: map[string]FilterSpec{
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
}}

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
}

// monthFreshnessJSON says how current the view is. Bank data age is not part
// of it yet: no bank-connection endpoint exists to read it from.
type monthFreshnessJSON struct {
	// ComputedAt is when this response was derived; statuses are derived on
	// every read.
	ComputedAt time.Time `json:"computed_at"`
	// OccurrencesChangedAt is the newest update among the month's
	// occurrences; null for an empty month.
	OccurrencesChangedAt *time.Time `json:"occurrences_changed_at"`
	// Bank is always {"available": false} until bank freshness is exposed.
	Bank struct {
		Available bool   `json:"available"`
		Note      string `json:"note"`
	} `json:"bank"`
}

// monthItemJSON is an occurrence of the monthly view: the occurrence plus the
// name of its commitment, so a row can be read without a second request.
type monthItemJSON struct {
	occurrenceJSON
	CommitmentName string `json:"commitment_name"`
}

type monthSummaryJSON struct {
	Month     calendar.YearMonth `json:"month"`
	AsOf      calendar.Date      `json:"as_of"`
	Items     []monthItemJSON    `json:"items"`
	Totals    []monthTotalsJSON  `json:"totals"`
	Freshness monthFreshnessJSON `json:"freshness"`
}

const bankFreshnessNote = "Bank data age is not available from this endpoint yet; the totals cover recorded commitments and manual payments only."

func toMonthSummaryJSON(s monthview.Summary, now time.Time) monthSummaryJSON {
	out := monthSummaryJSON{Month: s.Month, AsOf: s.AsOf, Items: make([]monthItemJSON, len(s.Rows)), Totals: make([]monthTotalsJSON, len(s.Totals))}
	for i, r := range s.Rows {
		out.Items[i] = monthItemJSON{occurrenceJSON: toOccurrenceJSON(r.Occurrence, r.State), CommitmentName: r.CommitmentName}
	}
	for i, t := range s.Totals {
		out.Totals[i] = monthTotalsJSON{Currency: t.Currency, Due: t.Due, Paid: t.Paid, Outstanding: t.Outstanding, Overdue: t.Overdue, Overpaid: t.Overpaid,
			OccurrenceCount: t.Occurrences, OverdueCount: t.OverdueCount, UnconvertedEvidenceN: t.Unconverted}
	}
	out.Freshness.ComputedAt = now.UTC()
	out.Freshness.OccurrencesChangedAt = s.LatestChange
	out.Freshness.Bank.Note = bankFreshnessNote
	return out
}

// Occurrences are derived from commitment definitions, so reading them needs
// the commitments read grant; there is no separate occurrence grant.
func registerOccurrences(s *Server, st occurrences.Reader, pay payments.Store, cs commitments.Store, todayFn today, grace payments.GraceFunc, now func() time.Time) {
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
		items, err := st.List(r.Context(), occurrences.ListFilter{Month: month, CommitmentIDs: q.Filter("commitment_id", OpEq), After: q.After, Limit: q.Limit,
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
			out[i] = toOccurrenceJSON(o, payments.Derive(o, facts[o.ID], t, grace))
		}
		WriteJSON(w, http.StatusOK, NewPage(out, q, func(o occurrenceJSON) string { return o.ID }))
	}))
	view := monthview.Service{Occurrences: st, Payments: pay, Grace: grace, Commitments: cs, Today: todayFn}
	s.HandleAPI("GET "+OccurrencesSummaryPath, Requires(grantCommitmentsRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k := range r.URL.Query() {
			if k != "month" {
				WriteError(w, r, Validation(k, "unknown parameter "+k+"; the summary takes only month"))
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
		sum, err := view.Current(r.Context(), month)
		if err != nil {
			if errors.Is(err, occurrences.ErrBeyondHorizon) {
				err = Validation("month", fmt.Sprintf("month must be within %d months after the current month", occurrences.MaxMonthsAhead))
			}
			WriteError(w, r, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		WriteJSON(w, http.StatusOK, toMonthSummaryJSON(sum, now()))
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
		WriteJSON(w, http.StatusOK, toOccurrenceJSON(o, payments.Derive(o, facts[o.ID], t, grace)))
	}))
}
