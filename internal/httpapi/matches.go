package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/matches"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
)

// grantBankingRead lets a caller see the bank's own text (descriptions,
// counterparty names) in match responses.
var grantBankingRead = authz.G(authz.ResourceBanking, authz.ActionRead)

// PaymentMatchesPath is the payment match collection (task X1, ADR-0035).
const PaymentMatchesPath = APIPrefix + "/payment-matches"

// Error codes of payment matches (ADR-0035). All are 409 conflicts with the
// stored data.
const (
	CodeTransactionNotCountable Code = "transaction_not_countable"  // the transaction is pending, no longer reported or superseded: it can only be suggested
	CodeTransactionNotDebit     Code = "transaction_not_debit"      // an incoming transaction cannot pay an occurrence
	CodeOverAllocated           Code = "transaction_over_allocated" // confirmed allocations would exceed the transaction's amount
	CodeOverpaid                Code = "occurrence_overpaid"        // the occurrence would be paid past its amount without allow_overpayment
	CodeDuplicateMatch          Code = "duplicate_match"            // the pair already has an open match
	CodeMatchState              Code = "invalid_match_state"        // the match's state does not allow the change
)

type deciderJSON struct {
	Kind   audit.ActorKind `json:"kind"`
	UserID *string         `json:"user_id"`
}

func toDeciderJSON(d matches.Decider) deciderJSON {
	out := deciderJSON{Kind: d.Kind}
	if d.UserID != "" {
		id := d.UserID
		out.UserID = &id
	}
	return out
}

// bankTransactionJSON is the bank fact a match refers to, as the provider
// reported it (ADR-0013). source is always "bank".
type bankTransactionJSON struct {
	Source             string                 `json:"source"`
	ID                 banking.TransactionID  `json:"id"`
	AccountID          banking.AccountID      `json:"account_id"`
	Provider           string                 `json:"provider"`
	BookingStatus      banking.BookingStatus  `json:"booking_status"`
	Amount             money.Money            `json:"amount"`
	Date               calendar.Date          `json:"date"`
	BookingDate        *calendar.Date         `json:"booking_date"`
	ValueDate          *calendar.Date         `json:"value_date"`
	Description        *string                `json:"description"`
	Counterparty       *string                `json:"counterparty"`
	NoLongerReportedAt *time.Time             `json:"no_longer_reported_at"`
	SupersededBy       *banking.TransactionID `json:"superseded_by"`
	Countable          bool                   `json:"countable"`
}

type matchEventJSON struct {
	Action matches.Action `json:"action"`
	State  matches.State  `json:"state"`
	Amount money.Money    `json:"amount"`
	By     deciderJSON    `json:"by"`
	Reason *string        `json:"reason"`
	At     time.Time      `json:"at"`
}

// paymentMatchJSON is the API representation of a payment match. It keeps
// apart the bank fact (transaction), who proposed the match (origin), the
// decision (state, decided_by, decided_at) and whether it is payment evidence
// now (counts_as_evidence).
type paymentMatchJSON struct {
	// BankTextRedacted is true when the caller lacks banking:read and the
	// transaction's description and counterparty are null.
	BankTextRedacted bool                `json:"bank_text_redacted"`
	ID               string              `json:"id"`
	OccurrenceID     string              `json:"occurrence_id"`
	TransactionID    string              `json:"transaction_id"`
	Amount           money.Money         `json:"amount"`
	Origin           matches.Origin      `json:"origin"`
	State            matches.State       `json:"state"`
	CountsAsEvidence bool                `json:"counts_as_evidence"`
	NeedsReview      bool                `json:"needs_review"`
	CreatedBy        deciderJSON         `json:"created_by"`
	DecidedBy        *deciderJSON        `json:"decided_by"`
	DecidedAt        *time.Time          `json:"decided_at"`
	Reason           *string             `json:"reason"`
	Transaction      bankTransactionJSON `json:"transaction"`
	History          []matchEventJSON    `json:"history"`
	Version          int64               `json:"version"`
	// OccurrenceVersion is the occurrence's new version, set on create and
	// action responses so a client can send its next If-Match without a refetch.
	OccurrenceVersion *int64    `json:"occurrence_version,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func datePtr(d calendar.Date) *calendar.Date {
	if d.IsZero() {
		return nil
	}
	return &d
}

// toPaymentMatchJSON converts a match. Without bankText (the banking:read
// grant) the bank's description and counterparty are null.
func toPaymentMatchJSON(m matches.Match, bankText bool) paymentMatchJSON {
	t := m.Transaction
	tj := bankTransactionJSON{Source: "bank", ID: t.ID, AccountID: t.AccountID, Provider: t.Provider, BookingStatus: t.Status, Amount: t.Amount,
		Date: t.Date, BookingDate: datePtr(t.BookingDate), ValueDate: datePtr(t.ValueDate), Countable: t.Countable()}
	if bankText {
		tj.Description, tj.Counterparty = &t.Description, &t.Counterparty
	}
	if !t.NoLongerReportedAt.IsZero() {
		at := t.NoLongerReportedAt
		tj.NoLongerReportedAt = &at
	}
	if t.SupersededBy != "" {
		s := t.SupersededBy
		tj.SupersededBy = &s
	}
	out := paymentMatchJSON{BankTextRedacted: !bankText, ID: m.ID, OccurrenceID: m.OccurrenceID, TransactionID: string(m.TransactionID), Amount: m.Amount, Origin: m.Origin, State: m.State,
		CountsAsEvidence: m.CountsAsEvidence(), NeedsReview: m.NeedsReview(),
		CreatedBy: toDeciderJSON(m.CreatedBy), DecidedAt: m.DecidedAt, Reason: m.Reason, Transaction: tj,
		History: make([]matchEventJSON, len(m.History)), Version: m.Version, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt}
	if m.DecidedBy != nil {
		d := toDeciderJSON(*m.DecidedBy)
		out.DecidedBy = &d
	}
	for i, e := range m.History {
		out.History[i] = matchEventJSON{Action: e.Action, State: e.State, Amount: e.Amount, By: toDeciderJSON(e.By), Reason: e.Reason, At: e.At}
	}
	return out
}

// matchError maps the match rules' errors to API errors.
func matchError(err error) error {
	var ve *commitments.ValidationError
	conflict := func(code Code, field string) error {
		return &Error{Status: http.StatusConflict, Code: code, Field: field, Message: err.Error(), Cause: err}
	}
	switch {
	case errors.As(err, &ve) && len(ve.Errors) > 0:
		return fieldErrors(ve.Errors)
	case errors.Is(err, matches.ErrTransactionNotFound):
		return Validation("transaction_id", err.Error())
	case errors.Is(err, matches.ErrOccurrenceNotFound):
		return Validation("occurrence_id", err.Error())
	case errors.Is(err, matches.ErrCurrency):
		return Validation("amount.currency", err.Error())
	case errors.Is(err, matches.ErrScale):
		return Validation("amount", err.Error())
	case errors.Is(err, matches.ErrNotCountable):
		return conflict(CodeTransactionNotCountable, "transaction_id")
	case errors.Is(err, matches.ErrNotDebit):
		return conflict(CodeTransactionNotDebit, "transaction_id")
	case errors.Is(err, matches.ErrOverAllocated):
		return conflict(CodeOverAllocated, "amount")
	case errors.Is(err, matches.ErrOverpaid):
		return conflict(CodeOverpaid, "amount")
	case errors.Is(err, matches.ErrDuplicate):
		return conflict(CodeDuplicateMatch, "")
	case errors.Is(err, matches.ErrTransition):
		return conflict(CodeMatchState, "")
	}
	return err
}

var matchesListSpec = ListSpec{Filters: map[string]FilterSpec{
	"transaction_id": {Ops: []FilterOp{OpEq}, Check: checkID("transaction_id must be a transaction identifier")},
	"occurrence_id":  {Ops: []FilterOp{OpEq}, Check: checkID("occurrence_id must be an occurrence identifier")},
	"state": {Ops: []FilterOp{OpEq}, Check: func(v string) error {
		if !matches.State(v).Valid() {
			return errors.New("state must be suggested, confirmed, rejected or reversed")
		}
		return nil
	}},
	"origin": {Ops: []FilterOp{OpEq}, Check: func(v string) error {
		if !matches.Origin(v).Valid() {
			return errors.New("origin must be auto_rule, model_suggestion or user")
		}
		return nil
	}},
}}

func checkID(msg string) func(string) error {
	return func(v string) error {
		if !resource.ValidID(v) {
			return errors.New(msg)
		}
		return nil
	}
}

// decodeObject reads an optional JSON object body: an empty body is an empty
// object.
func decodeObject(r *http.Request) (map[string]json.RawMessage, error) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, tooLarge(maxErr.Limit)
		}
		return nil, Validation("", "the request body could not be read")
	}
	doc := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(raw)) == 0 {
		return doc, nil
	}
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) || json.Unmarshal(raw, &doc) != nil {
		return nil, Validation("", "the body must be a JSON object")
	}
	return doc, nil
}

// matchBody is a decoded match request body.
type matchBody struct {
	transactionID, occurrenceID string
	amount                      money.Money
	allowOverpayment            bool
	reason                      *string
	version                     *int64
}

// decodeMatchBody decodes the members allowed (and required) for one
// request, reporting every problem.
func decodeMatchBody(doc map[string]json.RawMessage, allowed, required []string) (matchBody, []commitments.FieldError) {
	var b matchBody
	var errs []commitments.FieldError
	fail := func(_, field, msg string) { errs = append(errs, commitments.FieldError{Field: field, Message: msg}) }
	has := func(list []string, k string) bool { return slices.Contains(list, k) }
	for _, k := range slices.Sorted(maps.Keys(doc)) {
		if !has(allowed, k) {
			fail(k, k, "unknown field "+k)
		}
	}
	for _, k := range required {
		if raw, ok := doc[k]; !ok || isJSONNull(raw) {
			fail(k, k, k+" is required")
		}
	}
	str := func(k string) string {
		raw, ok := doc[k]
		if !ok || isJSONNull(raw) {
			return ""
		}
		var s string
		if json.Unmarshal(raw, &s) != nil || !resource.ValidID(s) {
			fail(k, k, k+" must be an identifier")
			return ""
		}
		return s
	}
	if has(allowed, "transaction_id") {
		b.transactionID = str("transaction_id")
	}
	if has(allowed, "occurrence_id") {
		b.occurrenceID = str("occurrence_id")
	}
	if raw, ok := doc["amount"]; ok && !isJSONNull(raw) && has(allowed, "amount") {
		b.amount = decodeAmount(raw, fail)
	}
	if raw, ok := doc["allow_overpayment"]; ok && !isJSONNull(raw) && has(allowed, "allow_overpayment") {
		if json.Unmarshal(raw, &b.allowOverpayment) != nil {
			fail("allow_overpayment", "allow_overpayment", "allow_overpayment must be true or false")
		}
	}
	if raw, ok := doc["reason"]; ok && !isJSONNull(raw) && has(allowed, "reason") {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			fail("reason", "reason", "reason must be a string")
		} else {
			b.reason = &s
		}
	}
	if raw, ok := doc["version"]; ok && !isJSONNull(raw) && has(allowed, "version") {
		var v int64
		if json.Unmarshal(raw, &v) != nil || v < 1 {
			fail("version", "version", "version must be a positive integer")
		} else {
			b.version = &v
		}
	}
	return b, errs
}

// registerMatches registers the payment match routes (task X1, ADR-0035).
// Matches are decisions about commitment occurrences, like manual payments
// (ADR-0031 point 9), so they need the commitments grants.
func registerMatches(s *Server, st matches.Store) {
	item := PaymentMatchesPath + "/{id}"
	// Bank text (descriptions, counterparty names) needs banking:read.
	bankText := func(r *http.Request) bool {
		p, _ := authz.FromContext(r.Context())
		return p.Allows(grantBankingRead)
	}
	writeMatch := func(w http.ResponseWriter, r *http.Request, status int, m matches.Match) {
		SetVersion(w, m.Version)
		WriteJSON(w, status, toPaymentMatchJSON(m, bankText(r)))
	}
	writeChange := func(w http.ResponseWriter, r *http.Request, status int, ch matches.Change) {
		SetVersion(w, ch.Match.Version)
		out := toPaymentMatchJSON(ch.Match, bankText(r))
		v := ch.Result.Occurrence.Version
		out.OccurrenceVersion = &v
		WriteJSON(w, status, out)
	}

	s.HandleAPI("GET "+PaymentMatchesPath, Requires(grantCommitmentsRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, err := ParseList(r, matchesListSpec)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		f := matches.ListFilter{TransactionIDs: q.Filter("transaction_id", OpEq), OccurrenceIDs: q.Filter("occurrence_id", OpEq), After: q.After, Limit: q.Limit + 1}
		for _, v := range q.Filter("state", OpEq) {
			f.States = append(f.States, matches.State(v))
		}
		for _, v := range q.Filter("origin", OpEq) {
			f.Origins = append(f.Origins, matches.Origin(v))
		}
		items, err := st.List(r.Context(), f)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]paymentMatchJSON, len(items))
		for i, m := range items {
			out[i] = toPaymentMatchJSON(m, bankText(r))
		}
		WriteJSON(w, http.StatusOK, NewPage(out, q, func(m paymentMatchJSON) string { return m.ID }))
	}))

	s.HandleAPI("POST "+PaymentMatchesPath, Requires(grantCommitmentsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		doc, err := decodeObject(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		b, errs := decodeMatchBody(doc, []string{"transaction_id", "occurrence_id", "amount", "allow_overpayment", "reason"}, []string{"transaction_id", "occurrence_id", "amount"})
		if len(errs) > 0 {
			WriteError(w, r, fieldErrors(errs))
			return
		}
		ch, err := st.Create(r.Context(), actor(r), matches.New{TransactionID: banking.TransactionID(b.transactionID), OccurrenceID: b.occurrenceID, Amount: b.amount,
			Origin: matches.OriginUser, State: matches.StateConfirmed, AllowOverpayment: b.allowOverpayment, Reason: b.reason})
		if err != nil {
			WriteError(w, r, matchError(err))
			return
		}
		w.Header().Set("Location", PaymentMatchesPath+"/"+ch.Match.ID)
		writeChange(w, r, http.StatusCreated, ch)
	}))

	s.HandleAPI("GET "+item, Requires(grantCommitmentsRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m, err := st.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		writeMatch(w, r, http.StatusOK, m)
	}))

	// action registers POST /payment-matches/{id}/<name>: decode the body,
	// check the version (If-Match or body), run it and answer with the match.
	action := func(name string, allowed, required []string, run func(r *http.Request, id string, expected int64, b matchBody) (matches.Change, error)) {
		s.HandleAPI("POST "+item+"/"+name, Requires(grantCommitmentsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			doc, err := decodeObject(r)
			if err != nil {
				WriteError(w, r, err)
				return
			}
			b, errs := decodeMatchBody(doc, append([]string{"version", "reason"}, allowed...), required)
			if len(errs) > 0 {
				WriteError(w, r, fieldErrors(errs))
				return
			}
			expected, err := ExpectedVersion(r, b.version)
			if err != nil {
				WriteError(w, r, err)
				return
			}
			id := r.PathValue("id")
			ch, err := run(r, id, expected, b)
			if err != nil {
				var vc *resource.VersionConflictError
				if errors.As(err, &vc) {
					if m, gerr := st.Get(r.Context(), id); gerr == nil {
						WriteError(w, r, VersionConflict(m.Version, toPaymentMatchJSON(m, bankText(r))))
						return
					}
				}
				WriteError(w, r, matchError(err))
				return
			}
			writeChange(w, r, http.StatusOK, ch)
		}))
	}
	action("confirm", []string{"allow_overpayment"}, nil, func(r *http.Request, id string, v int64, b matchBody) (matches.Change, error) {
		return st.Confirm(r.Context(), actor(r), id, v, b.allowOverpayment, b.reason)
	})
	action("reject", nil, nil, func(r *http.Request, id string, v int64, b matchBody) (matches.Change, error) {
		return st.Reject(r.Context(), actor(r), id, v, b.reason)
	})
	action("unmatch", nil, nil, func(r *http.Request, id string, v int64, b matchBody) (matches.Change, error) {
		return st.Unmatch(r.Context(), actor(r), id, v, b.reason)
	})
	action("correct", []string{"amount", "allow_overpayment"}, []string{"amount"}, func(r *http.Request, id string, v int64, b matchBody) (matches.Change, error) {
		return st.Correct(r.Context(), actor(r), id, v, b.amount, b.allowOverpayment, b.reason)
	})
	action("reverse", nil, nil, func(r *http.Request, id string, v int64, b matchBody) (matches.Change, error) {
		return st.Reverse(r.Context(), actor(r), id, v, b.reason)
	})
}
