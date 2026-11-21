package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
)

// Grants the commitment routes require.
var (
	grantCommitmentsRead  = authz.G(authz.ResourceCommitments, authz.ActionRead)
	grantCommitmentsWrite = authz.G(authz.ResourceCommitments, authz.ActionWrite)
)

// CommitmentsPath is the commitment collection.
const CommitmentsPath = APIPrefix + "/commitments"

// commitmentJSON is the API representation of a commitment definition.
type commitmentJSON struct {
	ID        string                `json:"id"`
	Name      string                `json:"name"`
	Kind      commitments.Kind      `json:"kind"`
	Amount    money.Money           `json:"amount"`
	Schedule  commitments.Schedule  `json:"schedule"`
	StartDate calendar.Date         `json:"start_date"`
	EndDate   *calendar.Date        `json:"end_date"`
	Priority  *commitments.Priority `json:"priority"`
	Notes     *string               `json:"notes"`
	Version   int64                 `json:"version"`
	CreatedAt time.Time             `json:"created_at"`
	UpdatedAt time.Time             `json:"updated_at"`
}

func toCommitmentJSON(c commitments.Commitment) commitmentJSON {
	return commitmentJSON{ID: c.ID, Name: c.Name, Kind: c.Kind, Amount: c.Amount, Schedule: c.Schedule, StartDate: c.StartDate,
		EndDate: c.EndDate, Priority: c.Priority, Notes: c.Notes, Version: c.Version, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
}

// editableJSON is the part of a commitment a client writes: the body of a
// create, and the document a merge patch applies to.
type editableJSON struct {
	Name      string                `json:"name"`
	Kind      commitments.Kind      `json:"kind"`
	Amount    money.Money           `json:"amount"`
	Schedule  commitments.Schedule  `json:"schedule"`
	StartDate calendar.Date         `json:"start_date"`
	EndDate   *calendar.Date        `json:"end_date"`
	Priority  *commitments.Priority `json:"priority"`
	Notes     *string               `json:"notes"`
}

// commitmentMembers are the writable members, in the order errors are listed.
var commitmentMembers = []string{"name", "kind", "amount", "schedule", "start_date", "end_date", "priority", "notes"}

// requiredCommitmentMembers cannot be absent from a create or null in a patch.
var requiredCommitmentMembers = map[string]bool{"name": true, "kind": true, "amount": true, "schedule": true, "start_date": true}

var commitmentsListSpec = ListSpec{Filters: map[string]FilterSpec{
	"kind": {Ops: []FilterOp{OpEq}, Check: func(v string) error {
		if !commitments.Kind(v).Valid() {
			return errors.New("kind must be one of " + names(commitments.Kinds()))
		}
		return nil
	}},
	"priority": {Ops: []FilterOp{OpEq}, Check: func(v string) error {
		if !commitments.Priority(v).Valid() {
			return errors.New("priority must be one of " + names(commitments.Priorities()))
		}
		return nil
	}},
	"currency": {Ops: []FilterOp{OpEq}, Check: func(v string) error {
		if _, err := money.ParseCurrency(v); err != nil {
			return errors.New("currency must be three upper-case letters")
		}
		return nil
	}},
}}

func names[T ~string](vs []T) string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = string(v)
	}
	return strings.Join(out, ", ")
}

// fieldErrors converts domain field errors for the error envelope.
func fieldErrors(errs []commitments.FieldError) *Error {
	out := make([]FieldDetail, len(errs))
	for i, e := range errs {
		out[i] = FieldDetail{Field: e.Field, Message: e.Message}
	}
	return ValidationFields(out)
}

// commitmentError maps the commitments package's errors to API errors.
func commitmentError(err error) error {
	var ve *commitments.ValidationError
	switch {
	case errors.As(err, &ve) && len(ve.Errors) > 0:
		return fieldErrors(ve.Errors)
	case errors.Is(err, commitments.ErrInUse):
		return Conflict(commitments.ErrInUse.Error())
	}
	return err
}

// decodeCommitment turns the writable members of doc into fields. It reports
// every problem at once: unknown, missing or mistyped members, then the
// domain rules for the members that decoded, ordered by member.
func decodeCommitment(doc map[string]json.RawMessage) (commitments.Fields, []commitments.FieldError) {
	var f commitments.Fields
	var errs []commitments.FieldError
	bad := map[string]bool{} // top-level members with a decoding error
	fail := func(member, field, msg string) {
		bad[member] = true
		errs = append(errs, commitments.FieldError{Field: field, Message: msg})
	}
	var unknown []string
	for k := range doc {
		if !slices.Contains(commitmentMembers, k) {
			unknown = append(unknown, k)
		}
	}
	slices.Sort(unknown)
	for _, k := range unknown {
		fail(k, k, "unknown field "+k)
	}
	present := func(m string) (json.RawMessage, bool) {
		raw, ok := doc[m]
		if ok && !isJSONNull(raw) {
			return raw, true
		}
		if requiredCommitmentMembers[m] {
			fail(m, m, m+" is required")
		}
		return nil, false
	}
	str := func(m string, raw json.RawMessage) (string, bool) {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			fail(m, m, m+" must be a string")
			return "", false
		}
		return s, true
	}
	if raw, ok := present("name"); ok {
		f.Name, _ = str("name", raw)
	}
	if raw, ok := present("kind"); ok {
		s, _ := str("kind", raw)
		f.Kind = commitments.Kind(s)
	}
	if raw, ok := present("amount"); ok {
		f.Amount = decodeAmount(raw, fail)
	}
	if raw, ok := present("schedule"); ok {
		s, serrs := commitments.ParseSchedule(raw, "schedule")
		for _, e := range serrs {
			fail("schedule", e.Field, e.Message)
		}
		f.Schedule = s
	}
	date := func(m string, raw json.RawMessage) *calendar.Date {
		s, ok := str(m, raw)
		if !ok {
			return nil
		}
		d, err := calendar.ParseDate(s)
		if err != nil {
			fail(m, m, m+" must be a date YYYY-MM-DD")
			return nil
		}
		return &d
	}
	if raw, ok := present("start_date"); ok {
		if d := date("start_date", raw); d != nil {
			f.StartDate = *d
		}
	}
	if raw, ok := present("end_date"); ok {
		f.EndDate = date("end_date", raw)
	}
	if raw, ok := present("priority"); ok {
		if s, ok := str("priority", raw); ok {
			p := commitments.Priority(s)
			f.Priority = &p
		}
	}
	if raw, ok := present("notes"); ok {
		if s, ok := str("notes", raw); ok {
			f.Notes = &s
		}
	}
	for _, e := range f.Validate() {
		top, _, _ := strings.Cut(e.Field, ".")
		if !bad[top] {
			errs = append(errs, e)
		}
	}
	rank := func(e commitments.FieldError) int {
		top, _, _ := strings.Cut(e.Field, ".")
		return slices.Index(commitmentMembers, top) // unknown members (-1) first
	}
	slices.SortStableFunc(errs, func(a, b commitments.FieldError) int { return rank(a) - rank(b) })
	return f, errs
}

// decodeAmount decodes {"amount":"12.50","currency":"EUR"}, naming the
// member that is wrong.
func decodeAmount(raw json.RawMessage, fail func(member, field, msg string)) money.Money {
	var m map[string]json.RawMessage
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) || json.Unmarshal(raw, &m) != nil {
		fail("amount", "amount", `amount must be an object such as {"amount":"12.50","currency":"EUR"}`)
		return money.Money{}
	}
	var unknown []string
	for k := range m {
		if k != "amount" && k != "currency" {
			unknown = append(unknown, k)
		}
	}
	slices.Sort(unknown)
	for _, k := range unknown {
		fail("amount", "amount."+k, "unknown field amount."+k)
	}
	var amount money.Decimal
	var currency money.Currency
	ok := true
	if v, has := m["amount"]; !has || isJSONNull(v) {
		fail("amount", "amount.amount", "amount.amount is required")
		ok = false
	} else if !bytes.HasPrefix(bytes.TrimSpace(v), []byte(`"`)) || json.Unmarshal(v, &amount) != nil {
		fail("amount", "amount.amount", `amount.amount must be a decimal string such as "12.50"`)
		ok = false
	}
	if v, has := m["currency"]; !has || isJSONNull(v) {
		fail("amount", "amount.currency", "amount.currency is required")
		ok = false
	} else if json.Unmarshal(v, &currency) != nil {
		fail("amount", "amount.currency", "amount.currency must be an ISO 4217 code of three upper-case letters")
		ok = false
	}
	if !ok || len(unknown) > 0 {
		return money.Money{}
	}
	out, err := money.New(amount, currency)
	if err != nil {
		fail("amount", "amount.currency", "amount.currency is required")
	}
	return out
}

func isJSONNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

// today returns the current date in the reporting time zone (decision D5).
type today func() (calendar.Date, error)

func newToday(zone calendar.Zone, now func() time.Time) today {
	if now == nil {
		now = time.Now
	}
	return func() (calendar.Date, error) {
		if zone.IsZero() {
			return calendar.Date{}, errors.New("httpapi: no reporting time zone configured")
		}
		return zone.Today(now())
	}
}

// effectiveFrom reads the optional effective_from control member of a
// commitment patch: the date from which changed terms reach occurrences
// (decision D6 edits option a). Without it the change applies from today in
// the reporting zone, so overdue unpaid occurrences keep their terms.
func effectiveFrom(patch MergePatch, todayFn today) (calendar.Date, error) {
	t, err := todayFn()
	if err != nil {
		return calendar.Date{}, Internal(err)
	}
	if !patch.Has("effective_from") {
		return t, nil
	}
	var d calendar.Date
	if patch.IsNull("effective_from") || patch.Decode("effective_from", &d) != nil || d.IsZero() {
		return calendar.Date{}, Validation("effective_from", "effective_from must be a date such as 2026-11-01")
	}
	if d.YearMonth().Compare(occurrences.HorizonLimit(t)) > 0 {
		return calendar.Date{}, Validation("effective_from", fmt.Sprintf("effective_from must be within %d months after the current month", occurrences.MaxMonthsAhead))
	}
	return d, nil
}

func registerCommitments(s *Server, st commitments.Store, todayFn today) {
	s.HandleAPI("GET "+CommitmentsPath, Requires(grantCommitmentsRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, err := ParseList(r, commitmentsListSpec)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		f := commitments.ListFilter{After: q.After, Limit: q.Limit}
		for _, v := range q.Filter("kind", OpEq) {
			f.Kinds = append(f.Kinds, commitments.Kind(v))
		}
		for _, v := range q.Filter("priority", OpEq) {
			f.Priorities = append(f.Priorities, commitments.Priority(v))
		}
		for _, v := range q.Filter("currency", OpEq) {
			f.Currencies = append(f.Currencies, money.MustParseCurrency(v))
		}
		items, err := st.List(r.Context(), f)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]commitmentJSON, len(items))
		for i, c := range items {
			out[i] = toCommitmentJSON(c)
		}
		WriteJSON(w, http.StatusOK, NewPage(out, q, func(c commitmentJSON) string { return c.ID }))
	}))
	s.HandleAPI("POST "+CommitmentsPath, Requires(grantCommitmentsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw json.RawMessage
		if err := DecodeJSON(r, &raw); err != nil {
			WriteError(w, r, err)
			return
		}
		var doc map[string]json.RawMessage
		if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) || json.Unmarshal(raw, &doc) != nil {
			WriteError(w, r, Validation("", "the body must be a JSON object"))
			return
		}
		fields, errs := decodeCommitment(doc)
		if len(errs) > 0 {
			WriteError(w, r, fieldErrors(errs))
			return
		}
		c, err := st.Create(r.Context(), actor(r), fields)
		if err != nil {
			WriteError(w, r, commitmentError(err))
			return
		}
		w.Header().Set("Location", CommitmentsPath+"/"+c.ID)
		SetVersion(w, c.Version)
		WriteJSON(w, http.StatusCreated, toCommitmentJSON(c))
	}))
	s.HandleAPI("GET "+CommitmentsPath+"/{id}", Requires(grantCommitmentsRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := st.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		SetVersion(w, c.Version)
		WriteJSON(w, http.StatusOK, toCommitmentJSON(c))
	}))
	// PATCH is a JSON Merge Patch (ADR-0015, ADR-0017) applied to the
	// writable members: amount and schedule merge member by member, null
	// clears end_date, priority or notes. The result is validated as a whole.
	s.HandleAPI("PATCH "+CommitmentsPath+"/{id}", Requires(grantCommitmentsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		patch, err := DecodeMergePatch(r, append([]string{"version", "effective_from"}, commitmentMembers...)...)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		var bodyVersion *int64
		if patch.Has("version") {
			var v int64
			if patch.IsNull("version") || patch.Decode("version", &v) != nil {
				WriteError(w, r, Validation("version", "version must be a positive integer"))
				return
			}
			bodyVersion = &v
			delete(patch, "version")
		}
		effective, err := effectiveFrom(patch, todayFn)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		delete(patch, "effective_from")
		expected, err := ExpectedVersion(r, bodyVersion)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		var nulls []commitments.FieldError
		for _, m := range commitmentMembers {
			if requiredCommitmentMembers[m] && patch.IsNull(m) {
				nulls = append(nulls, commitments.FieldError{Field: m, Message: m + " is required and cannot be cleared"})
			}
		}
		if len(nulls) > 0 {
			WriteError(w, r, fieldErrors(nulls))
			return
		}
		id := r.PathValue("id")
		cur, err := st.Get(r.Context(), id)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		if cur.Version != expected {
			WriteError(w, r, VersionConflict(cur.Version, toCommitmentJSON(cur)))
			return
		}
		if len(patch) == 0 {
			SetVersion(w, cur.Version)
			WriteJSON(w, http.StatusOK, toCommitmentJSON(cur))
			return
		}
		fields, ferrs, err := applyCommitmentPatch(cur, patch)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		if len(ferrs) > 0 {
			WriteError(w, r, fieldErrors(ferrs))
			return
		}
		c, err := st.Update(r.Context(), actor(r), id, expected, fields, effective)
		if err != nil {
			var vc *resource.VersionConflictError
			if errors.As(err, &vc) {
				if now, gerr := st.Get(r.Context(), id); gerr == nil {
					WriteError(w, r, VersionConflict(now.Version, toCommitmentJSON(now)))
					return
				}
			}
			WriteError(w, r, commitmentError(err))
			return
		}
		SetVersion(w, c.Version)
		WriteJSON(w, http.StatusOK, toCommitmentJSON(c))
	}))
	// DELETE removes a definition and its unpaid occurrences and needs
	// If-Match. A commitment with paid occurrences (payment history, task C3)
	// answers 409 conflict instead of losing that history.
	s.HandleAPI("DELETE "+CommitmentsPath+"/{id}", Requires(grantCommitmentsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected, err := ExpectedVersion(r, nil)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		id := r.PathValue("id")
		if err := st.Delete(r.Context(), actor(r), id, expected); err != nil {
			var vc *resource.VersionConflictError
			if errors.As(err, &vc) {
				if cur, gerr := st.Get(r.Context(), id); gerr == nil {
					WriteError(w, r, VersionConflict(cur.Version, toCommitmentJSON(cur)))
					return
				}
			}
			WriteError(w, r, commitmentError(err))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
}

// applyCommitmentPatch merges patch into the writable members of cur (RFC
// 7396) and decodes the result.
func applyCommitmentPatch(cur commitments.Commitment, patch MergePatch) (commitments.Fields, []commitments.FieldError, error) {
	target, err := json.Marshal(editableJSON{Name: cur.Name, Kind: cur.Kind, Amount: cur.Amount, Schedule: cur.Schedule,
		StartDate: cur.StartDate, EndDate: cur.EndDate, Priority: cur.Priority, Notes: cur.Notes})
	if err != nil {
		return commitments.Fields{}, nil, Internal(err)
	}
	p, err := json.Marshal(patch)
	if err != nil {
		return commitments.Fields{}, nil, Internal(err)
	}
	merged, err := ApplyMergePatch(target, p)
	if err != nil {
		return commitments.Fields{}, nil, Validation("", "the patch is not valid JSON")
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(merged, &doc); err != nil {
		return commitments.Fields{}, nil, Internal(err)
	}
	f, errs := decodeCommitment(doc)
	return f, errs, nil
}
