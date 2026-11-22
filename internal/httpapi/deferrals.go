package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/deferrals"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/payments"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
)

// DeferralPlansPath is the deferral plan collection (task DF1, ADR-0050).
const DeferralPlansPath = APIPrefix + "/deferral-plans"

// Error codes of deferral plans (ADR-0050). All are 409 conflicts with the
// stored data.
const (
	// CodeNothingOutstanding: the occurrence owes nothing now (paid, or not
	// recorded), so there is nothing to defer.
	CodeNothingOutstanding Code = "nothing_outstanding"
	// CodeDeferralPlanExists: the occurrence already has an active plan;
	// revise or undo it instead.
	CodeDeferralPlanExists Code = "deferral_plan_exists"
	// CodeDeferralPlanUndone: the plan was undone and is history.
	CodeDeferralPlanUndone Code = "deferral_plan_undone"
	// CodeAdjustmentRemoved: the adjustment was already removed.
	CodeAdjustmentRemoved Code = "deferral_adjustment_removed"
	// CodeAdjustmentPaid: the adjustment is already marked paid.
	CodeAdjustmentPaid Code = "deferral_adjustment_paid"
	// CodeAdjustmentNotPaid: the adjustment is not marked paid, so there is
	// no marker to clear.
	CodeAdjustmentNotPaid Code = "deferral_adjustment_not_paid"
	// CodeUnpaidAdjustments: the plan still has a fee that is planned; mark
	// it paid or remove it before undoing the plan.
	CodeUnpaidAdjustments Code = "deferral_adjustments_unpaid"
)

type deferralAdjustmentJSON struct {
	ID        string                   `json:"id"`
	Kind      deferrals.AdjustmentKind `json:"kind"`
	Amount    money.Money              `json:"amount"`
	Note      *string                  `json:"note"`
	CreatedAt time.Time                `json:"created_at"`
	// RemovedAt is set once the adjustment was removed: it is kept as
	// history and no longer counts.
	RemovedAt *time.Time `json:"removed_at"`
	// PaidAt and PaidBy are when and by whom the adjustment was marked paid
	// (null while it is still planned). The marker is reversible and is not
	// payment evidence for the occurrence.
	PaidAt *time.Time   `json:"paid_at"`
	PaidBy *deciderJSON `json:"paid_by"`
}

// deferralPlanJSON is the API representation of a deferral plan. It keeps
// apart what the plan says (plan_type, planned_date, reason), what was owed
// when it was made (original_due_date, original_amount,
// outstanding_when_planned: history that never changes) and, when known, the
// occurrence's live state (occurrence_status, outstanding_amount), which
// only payment evidence changes. A plan is never payment.
type deferralPlanJSON struct {
	ID                     string             `json:"id"`
	OccurrenceID           string             `json:"occurrence_id"`
	CommitmentID           string             `json:"commitment_id"`
	PlanType               deferrals.PlanType `json:"plan_type"`
	State                  deferrals.State    `json:"state"`
	PlannedDate            calendar.Date      `json:"planned_date"`
	PlannedMonth           calendar.YearMonth `json:"planned_month"`
	Reason                 *string            `json:"reason"`
	OriginalDueDate        calendar.Date      `json:"original_due_date"`
	OriginalAmount         money.Money        `json:"original_amount"`
	OutstandingWhenPlanned money.Money        `json:"outstanding_when_planned"`
	// OccurrenceStatus and OutstandingAmount are the occurrence's derived
	// payment state today; OverdueSince is the occurrence's current due date
	// while it is overdue (null otherwise): a plan never moves it (decision D7).
	// original_due_date stays what it was when the plan was made (history).
	OccurrenceStatus  payments.Status `json:"occurrence_status"`
	OutstandingAmount money.Money     `json:"outstanding_amount"`
	OverdueSince      *calendar.Date  `json:"overdue_since"`
	// GraceUntil is the planned date of an active documented_grace plan;
	// WithinGrace is true while the occurrence is overdue and today is not
	// after it. The occurrence is overdue either way.
	GraceUntil  *calendar.Date `json:"grace_until"`
	WithinGrace bool           `json:"within_grace"`
	// Missed is true while the plan is active, its planned date has passed
	// and the occurrence still owes something ("plan date passed, still
	// owed"). It is derived on every read, never stored, and is not payment
	// state: it clears when the occurrence is paid, the plan is revised to
	// a later date or undone.
	Missed bool `json:"missed"`
	// AdjustmentTotal sums the adjustments not removed; UnpaidAdjustmentTotal
	// the ones still planned (neither removed nor marked paid), the amount the
	// monthly view counts in the planned month.
	AdjustmentTotal       money.Money              `json:"adjustment_total"`
	UnpaidAdjustmentTotal money.Money              `json:"unpaid_adjustment_total"`
	Adjustments           []deferralAdjustmentJSON `json:"adjustments"`
	Version               int64                    `json:"version"`
	CreatedAt             time.Time                `json:"created_at"`
	UpdatedAt             time.Time                `json:"updated_at"`
	UndoneAt              *time.Time               `json:"undone_at"`
}

// toDeferralPlanJSON converts a plan with its occurrence's payment state as
// of today.
func toDeferralPlanJSON(p deferrals.Plan, due calendar.Date, st payments.State, today calendar.Date) deferralPlanJSON {
	out := deferralPlanJSON{ID: p.ID, OccurrenceID: p.OccurrenceID, CommitmentID: p.CommitmentID, PlanType: p.Type, State: p.State(),
		PlannedDate: p.PlannedDate, PlannedMonth: p.PlannedMonth(), Reason: p.Reason, OriginalDueDate: p.OriginalDueDate, OriginalAmount: p.OriginalAmount,
		OutstandingWhenPlanned: p.OutstandingWhenPlanned, OccurrenceStatus: st.Status, OutstandingAmount: st.Outstanding, AdjustmentTotal: p.AdjustmentTotal(),
		UnpaidAdjustmentTotal: p.UnpaidAdjustmentTotal(), Missed: p.Missed(st, today),
		Adjustments: make([]deferralAdjustmentJSON, len(p.Adjustments)), Version: p.Version, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, UndoneAt: p.UndoneAt}
	overdue := st.Status != payments.StatusPaid && st.Timing == payments.StatusOverdue
	if overdue {
		out.OverdueSince = &due
	}
	if until, ok := p.GraceUntil(); ok {
		out.GraceUntil = &until
		out.WithinGrace = overdue && !today.After(until)
	}
	for i, a := range p.Adjustments {
		out.Adjustments[i] = deferralAdjustmentJSON{ID: a.ID, Kind: a.Kind, Amount: a.Amount, Note: a.Note, CreatedAt: a.CreatedAt, RemovedAt: a.RemovedAt, PaidAt: a.PaidAt}
		if a.PaidBy != nil {
			by := deciderJSON{Kind: a.PaidBy.Kind}
			if a.PaidBy.UserID != "" {
				id := a.PaidBy.UserID
				by.UserID = &id
			}
			if a.PaidBy.Via != "" {
				v := a.PaidBy.Via
				by.Via = &v
			}
			out.Adjustments[i].PaidBy = &by
		}
	}
	return out
}

type deferralEventJSON struct {
	Action       deferrals.Action   `json:"action"`
	PlanVersion  int64              `json:"plan_version"`
	PlanType     deferrals.PlanType `json:"plan_type"`
	PlannedDate  calendar.Date      `json:"planned_date"`
	Reason       *string            `json:"reason"`
	AdjustmentID *string            `json:"adjustment_id"`
	By           deciderJSON        `json:"by"`
	At           time.Time          `json:"at"`
}

func toDeferralEventJSON(e deferrals.Event) deferralEventJSON {
	out := deferralEventJSON{Action: e.Action, PlanVersion: e.Version, PlanType: e.Type, PlannedDate: e.PlannedDate, Reason: e.Reason, By: deciderJSON{Kind: e.By.Kind}, At: e.At}
	if e.AdjustmentID != "" {
		id := e.AdjustmentID
		out.AdjustmentID = &id
	}
	if e.By.UserID != "" {
		id := e.By.UserID
		out.By.UserID = &id
	}
	if e.By.Via != "" {
		v := e.By.Via
		out.By.Via = &v
	}
	return out
}

// deferralError maps the plan rules' errors to API errors.
func deferralError(err error) error {
	var ve *commitments.ValidationError
	conflict := func(code Code, field string) error {
		return &Error{Status: http.StatusConflict, Code: code, Field: field, Message: err.Error(), Cause: err}
	}
	switch {
	case errors.As(err, &ve) && len(ve.Errors) > 0:
		return fieldErrors(ve.Errors)
	case errors.Is(err, deferrals.ErrOccurrenceNotFound):
		return Validation("occurrence_id", err.Error())
	case errors.Is(err, occurrences.ErrCancelled):
		return conflict(CodeOccurrenceCancelled, "occurrence_id")
	case errors.Is(err, deferrals.ErrNothingOutstanding):
		return conflict(CodeNothingOutstanding, "occurrence_id")
	case errors.Is(err, deferrals.ErrActivePlan):
		return conflict(CodeDeferralPlanExists, "occurrence_id")
	case errors.Is(err, deferrals.ErrUndone):
		return conflict(CodeDeferralPlanUndone, "")
	case errors.Is(err, deferrals.ErrAdjustmentRemoved):
		return conflict(CodeAdjustmentRemoved, "")
	case errors.Is(err, deferrals.ErrAdjustmentPaid):
		return conflict(CodeAdjustmentPaid, "")
	case errors.Is(err, deferrals.ErrAdjustmentNotPaid):
		return conflict(CodeAdjustmentNotPaid, "")
	case errors.Is(err, deferrals.ErrUnpaidAdjustments):
		return conflict(CodeUnpaidAdjustments, "")
	case errors.Is(err, deferrals.ErrNoChange):
		return conflict(CodeConflict, "")
	}
	return err
}

var deferralsListSpec = ListSpec{Filters: map[string]FilterSpec{
	"occurrence_id": {Ops: []FilterOp{OpEq}, Check: checkID("occurrence_id must be an occurrence identifier")},
	"commitment_id": {Ops: []FilterOp{OpEq}, Check: checkID("commitment_id must be a commitment identifier")},
	"state": {Ops: []FilterOp{OpEq}, Check: func(v string) error {
		if !deferrals.State(v).Valid() {
			return errors.New("state must be active or undone")
		}
		return nil
	}},
	"planned_month": {Ops: []FilterOp{OpEq}, Check: func(v string) error {
		if _, err := calendar.ParseYearMonth(v); err != nil {
			return errors.New("planned_month must be a month such as 2026-11")
		}
		return nil
	}},
}}

// deferralFieldMembers are the members of a plan the user decides.
var deferralFieldMembers = []string{"plan_type", "planned_date", "reason"}

// decodeDeferralFields decodes plan_type, planned_date and reason from doc
// (all required, reason may be null), reporting every problem.
func decodeDeferralFields(doc map[string]json.RawMessage) (deferrals.Fields, []commitments.FieldError) {
	var f deferrals.Fields
	var errs []commitments.FieldError
	fail := func(field, msg string) { errs = append(errs, commitments.FieldError{Field: field, Message: msg}) }
	if raw, ok := doc["plan_type"]; !ok || isJSONNull(raw) {
		fail("plan_type", "plan_type is required")
	} else if json.Unmarshal(raw, &f.Type) != nil {
		fail("plan_type", "plan_type must be a string")
	}
	if raw, ok := doc["planned_date"]; !ok || isJSONNull(raw) {
		fail("planned_date", "planned_date is required")
	} else {
		var s string
		d, err := calendar.Date{}, json.Unmarshal(raw, &s)
		if err == nil {
			d, err = calendar.ParseDate(s)
		}
		if err != nil {
			fail("planned_date", "planned_date must be a date YYYY-MM-DD")
		}
		f.PlannedDate = d
	}
	if raw, ok := doc["reason"]; ok && !isJSONNull(raw) {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			fail("reason", "reason must be a string or null")
		} else {
			f.Reason = &s
		}
	}
	return f, errs
}

// unknownMembers reports the members of doc that are not allowed.
func unknownMembers(doc map[string]json.RawMessage, allowed ...string) []commitments.FieldError {
	var errs []commitments.FieldError
	for _, k := range slices.Sorted(maps.Keys(doc)) {
		if !slices.Contains(allowed, k) {
			errs = append(errs, commitments.FieldError{Field: k, Message: "unknown field " + k})
		}
	}
	return errs
}

// bodyVersion decodes an optional "version" member.
func bodyVersion(doc map[string]json.RawMessage) (*int64, []commitments.FieldError) {
	raw, ok := doc["version"]
	if !ok || isJSONNull(raw) {
		return nil, nil
	}
	var v int64
	if json.Unmarshal(raw, &v) != nil || v < 1 {
		return nil, []commitments.FieldError{{Field: "version", Message: "version must be a positive integer"}}
	}
	return &v, nil
}

// registerDeferrals registers the deferral plan routes (task DF1, ADR-0050).
// Plans are decisions about commitment occurrences, like manual payments and
// matches, so they need the commitments grants.
func registerDeferrals(s *Server, st deferrals.Store, occ occurrences.Reader, pay payments.Store, todayFn today, grace payments.GraceFunc) {
	item := DeferralPlansPath + "/{id}"
	// render converts plans with their occurrences' payment state today.
	render := func(r *http.Request, ps []deferrals.Plan) ([]deferralPlanJSON, error) {
		t, err := todayFn()
		if err != nil {
			return nil, Internal(err)
		}
		ids := make([]string, len(ps))
		for i, p := range ps {
			ids[i] = p.OccurrenceID
		}
		facts, err := pay.Facts(r.Context(), ids)
		if err != nil {
			return nil, err
		}
		out := make([]deferralPlanJSON, len(ps))
		for i, p := range ps {
			o, err := occ.Get(r.Context(), p.OccurrenceID)
			if err != nil {
				return nil, err
			}
			out[i] = toDeferralPlanJSON(p, o.DueDate, payments.Derive(r.Context(), o, facts[o.ID], t, grace), t)
		}
		return out, nil
	}
	writePlan := func(w http.ResponseWriter, r *http.Request, status int, p deferrals.Plan) {
		out, err := render(r, []deferrals.Plan{p})
		if err != nil {
			WriteError(w, r, err)
			return
		}
		SetVersion(w, p.Version)
		WriteJSON(w, status, out[0])
	}
	// failed answers a write error: a stale version with the plan as it is
	// now, anything else mapped by deferralError.
	failed := func(w http.ResponseWriter, r *http.Request, id string, err error) {
		var vc *resource.VersionConflictError
		if errors.As(err, &vc) {
			if p, gerr := st.Get(r.Context(), id); gerr == nil {
				if out, rerr := render(r, []deferrals.Plan{p}); rerr == nil {
					WriteError(w, r, VersionConflict(p.Version, out[0]))
					return
				}
			}
			WriteError(w, r, VersionConflict(vc.Current, nil))
			return
		}
		WriteError(w, r, deferralError(err))
	}

	s.HandleAPI("GET "+DeferralPlansPath, Requires(grantCommitmentsRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, err := ParseList(r, deferralsListSpec)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		f := deferrals.ListFilter{OccurrenceIDs: q.Filter("occurrence_id", OpEq), CommitmentIDs: q.Filter("commitment_id", OpEq), After: q.After, Limit: q.Limit + 1}
		for _, v := range q.Filter("state", OpEq) {
			f.States = append(f.States, deferrals.State(v))
		}
		if ms := q.Filter("planned_month", OpEq); len(ms) > 1 {
			WriteError(w, r, Validation("planned_month", "planned_month can be given once"))
			return
		} else if len(ms) == 1 {
			f.PlannedMonth, _ = calendar.ParseYearMonth(ms[0]) // checked by the spec
		}
		items, err := st.List(r.Context(), f)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out, err := render(r, items)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, NewPage(out, q, func(p deferralPlanJSON) string { return p.ID }))
	}))

	s.HandleAPI("POST "+DeferralPlansPath, Requires(grantCommitmentsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		doc, err := decodeObject(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		errs := unknownMembers(doc, append([]string{"occurrence_id"}, deferralFieldMembers...)...)
		var occurrenceID string
		if raw, ok := doc["occurrence_id"]; !ok || isJSONNull(raw) {
			errs = append(errs, commitments.FieldError{Field: "occurrence_id", Message: "occurrence_id is required"})
		} else if json.Unmarshal(raw, &occurrenceID) != nil || !resource.ValidID(occurrenceID) {
			errs = append(errs, commitments.FieldError{Field: "occurrence_id", Message: "occurrence_id must be an occurrence identifier"})
		}
		fields, ferrs := decodeDeferralFields(doc)
		if errs = append(errs, ferrs...); len(errs) > 0 {
			WriteError(w, r, fieldErrors(errs))
			return
		}
		t, err := todayFn()
		if err != nil {
			WriteError(w, r, Internal(err))
			return
		}
		p, err := st.Create(r.Context(), matchActor(r), deferrals.NewPlan{OccurrenceID: occurrenceID, Fields: fields}, t)
		if err != nil {
			WriteError(w, r, deferralError(err))
			return
		}
		w.Header().Set("Location", DeferralPlansPath+"/"+p.ID)
		writePlan(w, r, http.StatusCreated, p)
	}))

	s.HandleAPI("GET "+item, Requires(grantCommitmentsRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := st.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		writePlan(w, r, http.StatusOK, p)
	}))

	// PATCH revises an active plan (JSON Merge Patch over plan_type,
	// planned_date and reason). A patch that changes nothing answers the plan
	// as it is.
	s.HandleAPI("PATCH "+item, Requires(grantCommitmentsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		patch, err := DecodeMergePatch(r, append([]string{"version"}, deferralFieldMembers...)...)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		v, verrs := bodyVersion(patch)
		if len(verrs) > 0 {
			WriteError(w, r, fieldErrors(verrs))
			return
		}
		delete(patch, "version")
		expected, err := ExpectedVersion(r, v)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		id := r.PathValue("id")
		cur, err := st.Get(r.Context(), id)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		if cur.Version != expected {
			failed(w, r, id, &resource.VersionConflictError{Current: cur.Version})
			return
		}
		doc := map[string]json.RawMessage{}
		for k, raw := range map[string]any{"plan_type": cur.Type, "planned_date": cur.PlannedDate, "reason": cur.Reason} {
			doc[k], _ = json.Marshal(raw)
		}
		for k, raw := range patch {
			doc[k] = raw
		}
		fields, errs := decodeDeferralFields(doc)
		if len(errs) > 0 {
			WriteError(w, r, fieldErrors(errs))
			return
		}
		if fields.Type == cur.Type && fields.PlannedDate == cur.PlannedDate && sameText(fields.Reason, cur.Reason) {
			if !cur.Active() {
				WriteError(w, r, deferralError(deferrals.ErrUndone))
				return
			}
			writePlan(w, r, http.StatusOK, cur)
			return
		}
		t, err := todayFn()
		if err != nil {
			WriteError(w, r, Internal(err))
			return
		}
		p, err := st.Revise(r.Context(), matchActor(r), id, expected, fields, t)
		if err != nil {
			failed(w, r, id, err)
			return
		}
		writePlan(w, r, http.StatusOK, p)
	}))

	// POST /undo ends an active plan: the occurrence's outstanding amount is
	// planned in its own month again. The plan and its history are kept.
	s.HandleAPI("POST "+item+"/undo", Requires(grantCommitmentsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		doc, err := decodeObject(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		errs := unknownMembers(doc, "version", "reason")
		v, verrs := bodyVersion(doc)
		errs = append(errs, verrs...)
		var reason *string
		if raw, ok := doc["reason"]; ok && !isJSONNull(raw) {
			var s string
			if json.Unmarshal(raw, &s) != nil {
				errs = append(errs, commitments.FieldError{Field: "reason", Message: "reason must be a string or null"})
			} else {
				reason = &s
			}
		}
		if len(errs) > 0 {
			WriteError(w, r, fieldErrors(errs))
			return
		}
		expected, err := ExpectedVersion(r, v)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		id := r.PathValue("id")
		p, err := st.Undo(r.Context(), matchActor(r), id, expected, reason)
		if err != nil {
			failed(w, r, id, err)
			return
		}
		writePlan(w, r, http.StatusOK, p)
	}))

	s.HandleAPI("GET "+item+"/history", Requires(grantCommitmentsRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, err := ParseList(r, ListSpec{})
		if err != nil {
			WriteError(w, r, err)
			return
		}
		all, err := st.History(r.Context(), r.PathValue("id"))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]deferralEventJSON, len(all))
		for i, e := range all {
			out[i] = toDeferralEventJSON(e)
		}
		start := 0
		if q.After != "" {
			i := slices.IndexFunc(out, func(e deferralEventJSON) bool { return eventCursor(e) == q.After })
			if i < 0 {
				WriteError(w, r, Validation("cursor", "cursor is not valid; start again from the first page"))
				return
			}
			start = i + 1
		}
		page := out[start:min(len(out), start+q.Limit+1)]
		WriteJSON(w, http.StatusOK, NewPage(page, q, eventCursor))
	}))

	s.HandleAPI("POST "+item+"/adjustments", Requires(grantCommitmentsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		doc, err := decodeObject(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		errs := unknownMembers(doc, "version", "kind", "amount", "note")
		v, verrs := bodyVersion(doc)
		errs = append(errs, verrs...)
		var in deferrals.NewAdjustment
		fail := func(_, field, msg string) { errs = append(errs, commitments.FieldError{Field: field, Message: msg}) }
		if raw, ok := doc["kind"]; !ok || isJSONNull(raw) {
			fail("kind", "kind", "kind is required")
		} else if json.Unmarshal(raw, &in.Kind) != nil {
			fail("kind", "kind", "kind must be a string")
		}
		if raw, ok := doc["amount"]; !ok || isJSONNull(raw) {
			fail("amount", "amount", "amount is required")
		} else {
			in.Amount = decodeAmount(raw, fail)
		}
		if raw, ok := doc["note"]; ok && !isJSONNull(raw) {
			var s string
			if json.Unmarshal(raw, &s) != nil {
				fail("note", "note", "note must be a string or null")
			} else {
				in.Note = &s
			}
		}
		if len(errs) > 0 {
			WriteError(w, r, fieldErrors(errs))
			return
		}
		expected, err := ExpectedVersion(r, v)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		id := r.PathValue("id")
		p, _, err := st.AddAdjustment(r.Context(), matchActor(r), id, expected, in)
		if err != nil {
			failed(w, r, id, err)
			return
		}
		writePlan(w, r, http.StatusCreated, p)
	}))

	s.HandleAPI("DELETE "+item+"/adjustments/{adjustment_id}", Requires(grantCommitmentsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected, err := ExpectedVersion(r, nil)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		id, adjustmentID := r.PathValue("id"), r.PathValue("adjustment_id")
		if err := checkID("adjustment_id must be an adjustment identifier")(adjustmentID); err != nil {
			WriteError(w, r, Validation("adjustment_id", err.Error()))
			return
		}
		p, err := st.RemoveAdjustment(r.Context(), matchActor(r), id, adjustmentID, expected)
		if err != nil {
			failed(w, r, id, err)
			return
		}
		writePlan(w, r, http.StatusOK, p)
	}))

	// POST …/paid marks an adjustment paid and POST …/unpaid clears the
	// marker again (D-DF 2): the fee is planned until one of them, and
	// neither touches the occurrence.
	paid := func(change func(deferrals.Store) func(context.Context, audit.Actor, string, string, int64) (deferrals.Plan, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			expected, err := ExpectedVersion(r, nil)
			if err != nil {
				WriteError(w, r, err)
				return
			}
			id, adjustmentID := r.PathValue("id"), r.PathValue("adjustment_id")
			if err := checkID("adjustment_id must be an adjustment identifier")(adjustmentID); err != nil {
				WriteError(w, r, Validation("adjustment_id", err.Error()))
				return
			}
			p, err := change(st)(r.Context(), matchActor(r), id, adjustmentID, expected)
			if err != nil {
				failed(w, r, id, err)
				return
			}
			writePlan(w, r, http.StatusOK, p)
		}
	}
	s.HandleAPI("POST "+item+"/adjustments/{adjustment_id}/paid", Requires(grantCommitmentsWrite), paid(func(s deferrals.Store) func(context.Context, audit.Actor, string, string, int64) (deferrals.Plan, error) {
		return s.MarkAdjustmentPaid
	}))
	s.HandleAPI("POST "+item+"/adjustments/{adjustment_id}/unpaid", Requires(grantCommitmentsWrite), paid(func(s deferrals.Store) func(context.Context, audit.Actor, string, string, int64) (deferrals.Plan, error) {
		return s.UnmarkAdjustmentPaid
	}))
}

// eventCursor is a history entry's page cursor: its plan version.
func eventCursor(e deferralEventJSON) string { return strconv.FormatInt(e.PlanVersion, 10) }

func sameText(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
