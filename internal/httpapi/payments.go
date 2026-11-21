package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/payments"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
)

// paymentStateJSON is the derived payment state of an occurrence (decision
// D7, ADR-0031). Nothing in it is stored except the override.
type paymentStateJSON struct {
	Status                payments.Status  `json:"status"`
	DerivedStatus         payments.Status  `json:"derived_status"`
	Timing                *payments.Status `json:"timing"`
	PaidAmount            money.Money      `json:"paid_amount"`
	OutstandingAmount     money.Money      `json:"outstanding_amount"`
	OverpaidAmount        money.Money      `json:"overpaid_amount"`
	UnconvertedCurrencies []money.Currency `json:"unconverted_currencies"`
	EvidenceCount         int              `json:"evidence_count"`
	ManualEvidenceCount   int              `json:"manual_evidence_count"`
	BankEvidenceCount     int              `json:"bank_evidence_count"`
	GraceUntil            *calendar.Date   `json:"grace_until"`
	WithinGrace           bool             `json:"within_grace"`
	Override              *overrideJSON    `json:"override"`
}

type overrideJSON struct {
	Status         payments.OverrideStatus  `json:"status"`
	Reason         string                   `json:"reason"`
	SetAt          time.Time                `json:"set_at"`
	Applied        bool                     `json:"applied"`
	PreviousStatus *payments.OverrideStatus `json:"previous_status"`
}

func toPaymentStateJSON(st payments.State) paymentStateJSON {
	out := paymentStateJSON{Status: st.Status, DerivedStatus: st.Derived, PaidAmount: st.Paid, OutstandingAmount: st.Outstanding, OverpaidAmount: st.Overpaid,
		UnconvertedCurrencies: st.Unconverted, EvidenceCount: st.EvidenceCount, ManualEvidenceCount: st.ManualEvidenceCount, BankEvidenceCount: st.BankEvidenceCount, GraceUntil: st.GraceUntil, WithinGrace: st.WithinGrace}
	if out.UnconvertedCurrencies == nil {
		out.UnconvertedCurrencies = []money.Currency{}
	}
	if st.Timing != "" {
		t := st.Timing
		out.Timing = &t
	}
	if o := st.Override; o != nil {
		out.Override = &overrideJSON{Status: o.Status, Reason: o.Reason, SetAt: o.SetAt, Applied: st.OverrideApplied, PreviousStatus: o.Previous}
	}
	return out
}

// manualPaymentJSON is the API representation of a manual payment record.
type manualPaymentJSON struct {
	ID           string        `json:"id"`
	OccurrenceID string        `json:"occurrence_id"`
	Amount       money.Money   `json:"amount"`
	PaidOn       calendar.Date `json:"paid_on"`
	Note         *string       `json:"note"`
	Version      int64         `json:"version"`
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
}

func toManualPaymentJSON(p payments.ManualPayment) manualPaymentJSON {
	return manualPaymentJSON{ID: p.ID, OccurrenceID: p.OccurrenceID, Amount: p.Amount, PaidOn: p.PaidOn, Note: p.Note,
		Version: p.Version, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
}

// paymentError maps the payments package's errors to API errors.
func paymentError(err error) error {
	var ve *commitments.ValidationError
	switch {
	case errors.As(err, &ve) && len(ve.Errors) > 0:
		return fieldErrors(ve.Errors)
	case errors.Is(err, payments.ErrNoEvidence):
		return &Error{Status: http.StatusConflict, Code: CodeEvidenceRequired, Field: "status", Message: payments.ErrNoEvidence.Error(), Cause: err}
	case errors.Is(err, occurrences.ErrCancelled):
		return &Error{Status: http.StatusConflict, Code: CodeOccurrenceCancelled, Message: occurrences.ErrCancelled.Error(), Cause: err}
	case errors.Is(err, payments.ErrNoOverride):
		return NotFound(payments.ErrNoOverride.Error())
	}
	return err
}

var manualMembers = []string{"amount", "paid_on", "note"}

// decodeManual decodes a manual payment body, reporting every problem.
func decodeManual(doc map[string]json.RawMessage) (payments.NewManual, []commitments.FieldError) {
	var n payments.NewManual
	var errs []commitments.FieldError
	fail := func(_, field, msg string) { errs = append(errs, commitments.FieldError{Field: field, Message: msg}) }
	var unknown []string
	for k := range doc {
		if !slices.Contains(manualMembers, k) {
			unknown = append(unknown, k)
		}
	}
	slices.Sort(unknown)
	for _, k := range unknown {
		fail(k, k, "unknown field "+k)
	}
	if raw, ok := doc["amount"]; !ok || isJSONNull(raw) {
		fail("amount", "amount", "amount is required")
	} else {
		n.Amount = decodeAmount(raw, fail)
	}
	if raw, ok := doc["paid_on"]; !ok || isJSONNull(raw) {
		fail("paid_on", "paid_on", "paid_on is required")
	} else {
		var s string
		d, err := calendar.Date{}, json.Unmarshal(raw, &s)
		if err == nil {
			d, err = calendar.ParseDate(s)
		}
		if err != nil {
			fail("paid_on", "paid_on", "paid_on must be a date YYYY-MM-DD")
		}
		n.PaidOn = d
	}
	if raw, ok := doc["note"]; ok && !isJSONNull(raw) {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			fail("note", "note", "note must be a string")
		} else {
			n.Note = &s
		}
	}
	return n, errs
}

// The payment routes (task C3, ADR-0031). Manual payments and overrides are
// user decisions about commitment occurrences, so they need the commitments
// grants: commitments:read to read them, commitments:write to change them.
func registerPayments(s *Server, occ occurrences.Reader, st payments.Store, todayFn today, grace payments.GraceFunc) {
	collection := OccurrencesPath + "/{id}/manual-payments"
	item := collection + "/{payment_id}"
	override := OccurrencesPath + "/{id}/status-override"

	writeOccurrence := func(w http.ResponseWriter, r *http.Request, ch payments.Change) {
		t, err := todayFn()
		if err != nil {
			WriteError(w, r, Internal(err))
			return
		}
		SetVersion(w, ch.Occurrence.Version)
		WriteJSON(w, http.StatusOK, toOccurrenceJSON(ch.Occurrence, payments.Derive(ch.Occurrence, ch.Facts, t, grace)))
	}
	// occurrenceConflict answers a stale occurrence version with the
	// occurrence as it is now.
	occurrenceConflict := func(w http.ResponseWriter, r *http.Request, err error) bool {
		var vc *resource.VersionConflictError
		if !errors.As(err, &vc) {
			return false
		}
		id := r.PathValue("id")
		o, oerr := occ.Get(r.Context(), id)
		facts, ferr := st.Facts(r.Context(), []string{id})
		t, terr := todayFn()
		if oerr != nil || ferr != nil || terr != nil {
			WriteError(w, r, VersionConflict(vc.Current, nil))
			return true
		}
		WriteError(w, r, VersionConflict(o.Version, toOccurrenceJSON(o, payments.Derive(o, facts[id], t, grace))))
		return true
	}

	s.HandleAPI("GET "+collection, Requires(grantCommitmentsRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, err := ParseList(r, ListSpec{})
		if err != nil {
			WriteError(w, r, err)
			return
		}
		all, err := st.Manual(r.Context(), r.PathValue("id"))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		start := 0
		if q.After != "" {
			i := slices.IndexFunc(all, func(p payments.ManualPayment) bool { return p.ID == q.After })
			if i < 0 {
				WriteError(w, r, Validation("cursor", "cursor is not valid; start again from the first page"))
				return
			}
			start = i + 1
		}
		page := all[start:min(len(all), start+q.Limit+1)]
		out := make([]manualPaymentJSON, len(page))
		for i, p := range page {
			out[i] = toManualPaymentJSON(p)
		}
		WriteJSON(w, http.StatusOK, NewPage(out, q, func(p manualPaymentJSON) string { return p.ID }))
	}))
	s.HandleAPI("POST "+collection, Requires(grantCommitmentsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		in, errs := decodeManual(doc)
		if len(errs) > 0 {
			WriteError(w, r, fieldErrors(errs))
			return
		}
		t, err := todayFn()
		if err != nil {
			WriteError(w, r, Internal(err))
			return
		}
		p, _, err := st.RecordManual(r.Context(), actor(r), r.PathValue("id"), in, t)
		if err != nil {
			WriteError(w, r, paymentError(err))
			return
		}
		w.Header().Set("Location", OccurrencesPath+"/"+p.OccurrenceID+"/manual-payments/"+p.ID)
		SetVersion(w, p.Version)
		WriteJSON(w, http.StatusCreated, toManualPaymentJSON(p))
	}))
	s.HandleAPI("GET "+item, Requires(grantCommitmentsRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := st.GetManual(r.Context(), r.PathValue("id"), r.PathValue("payment_id"))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		SetVersion(w, p.Version)
		WriteJSON(w, http.StatusOK, toManualPaymentJSON(p))
	}))
	s.HandleAPI("DELETE "+item, Requires(grantCommitmentsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected, err := ExpectedVersion(r, nil)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		id, pid := r.PathValue("id"), r.PathValue("payment_id")
		if _, err := st.DeleteManual(r.Context(), actor(r), id, pid, expected); err != nil {
			var vc *resource.VersionConflictError
			if errors.As(err, &vc) {
				if p, gerr := st.GetManual(r.Context(), id, pid); gerr == nil {
					WriteError(w, r, VersionConflict(p.Version, toManualPaymentJSON(p)))
					return
				}
			}
			WriteError(w, r, paymentError(err))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	// PUT sets the manual status override, replacing an active one (an undo
	// restores it). The version is the occurrence's.
	s.HandleAPI("PUT "+override, Requires(grantCommitmentsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Status  *payments.OverrideStatus `json:"status"`
			Reason  *string                  `json:"reason"`
			Version *int64                   `json:"version"`
		}
		if err := DecodeJSON(r, &body); err != nil {
			WriteError(w, r, err)
			return
		}
		var errs []commitments.FieldError
		if body.Status == nil {
			errs = append(errs, commitments.FieldError{Field: "status", Message: "status is required: paid or unpaid"})
		}
		if body.Reason == nil {
			errs = append(errs, commitments.FieldError{Field: "reason", Message: "reason is required"})
		}
		if len(errs) > 0 {
			WriteError(w, r, fieldErrors(errs))
			return
		}
		if errs := payments.ValidateOverride(*body.Status, *body.Reason); len(errs) > 0 {
			WriteError(w, r, fieldErrors(errs))
			return
		}
		expected, err := ExpectedVersion(r, body.Version)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		ch, err := st.SetOverride(r.Context(), actor(r), r.PathValue("id"), expected, *body.Status, *body.Reason)
		if err != nil {
			if !occurrenceConflict(w, r, err) {
				WriteError(w, r, paymentError(err))
			}
			return
		}
		writeOccurrence(w, r, ch)
	}))
	// DELETE is the undo of the last override change: it restores the
	// override the active one replaced, or none.
	s.HandleAPI("DELETE "+override, Requires(grantCommitmentsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected, err := ExpectedVersion(r, nil)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		ch, err := st.UndoOverride(r.Context(), actor(r), r.PathValue("id"), expected)
		if err != nil {
			if !occurrenceConflict(w, r, err) {
				WriteError(w, r, paymentError(err))
			}
			return
		}
		writeOccurrence(w, r, ch)
	}))
}
