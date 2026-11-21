package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
)

// OccurrencesPath is the occurrence collection (task C2, ADR-0027).
const OccurrencesPath = APIPrefix + "/occurrences"

// occurrenceJSON is the API representation of an occurrence. It has no payment
// status yet: status is derived from payment evidence (task C3, D7).
type occurrenceJSON struct {
	ID           string             `json:"id"`
	CommitmentID string             `json:"commitment_id"`
	Month        calendar.YearMonth `json:"month"`
	DueDate      calendar.Date      `json:"due_date"`
	Amount       money.Money        `json:"amount"`
	Version      int64              `json:"version"`
	CreatedAt    time.Time          `json:"created_at"`
	UpdatedAt    time.Time          `json:"updated_at"`
}

func toOccurrenceJSON(o occurrences.Occurrence) occurrenceJSON {
	return occurrenceJSON{ID: o.ID, CommitmentID: o.CommitmentID, Month: o.Month, DueDate: o.DueDate, Amount: o.Amount,
		Version: o.Version, CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt}
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
}}

// Occurrences are derived from commitment definitions, so reading them needs
// the commitments read grant; there is no separate occurrence grant.
func registerOccurrences(s *Server, st occurrences.Reader, todayFn today) {
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
		items, err := st.List(r.Context(), occurrences.ListFilter{Month: month, CommitmentIDs: q.Filter("commitment_id", OpEq), After: q.After, Limit: q.Limit})
		if err != nil {
			if errors.Is(err, occurrences.ErrCursorGone) {
				err = Validation("cursor", "cursor is not valid; start again from the first page")
			}
			WriteError(w, r, err)
			return
		}
		out := make([]occurrenceJSON, len(items))
		for i, o := range items {
			out[i] = toOccurrenceJSON(o)
		}
		WriteJSON(w, http.StatusOK, NewPage(out, q, func(o occurrenceJSON) string { return o.ID }))
	}))
	s.HandleAPI("GET "+OccurrencesPath+"/{id}", Requires(grantCommitmentsRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o, err := st.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		SetVersion(w, o.Version)
		WriteJSON(w, http.StatusOK, toOccurrenceJSON(o))
	}))
}
