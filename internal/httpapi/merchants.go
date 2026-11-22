package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/merchants"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
)

// TransactionsPath is the collection of bank transactions' sub-resources (E1,
// ADR-0051). Bank transactions themselves are not editable and have no
// routes of their own here: only their merchant identity does.
const TransactionsPath = APIPrefix + "/transactions"

var (
	grantMerchantsRead  = authz.G(authz.ResourceMerchants, authz.ActionRead)
	grantMerchantsWrite = authz.G(authz.ResourceMerchants, authz.ActionWrite)
)

// MerchantsPath is the collection prefix of merchant resources that are not
// per transaction.
const MerchantsPath = "/api/v1/merchants"

// CodeNothingToConfirm (409): the transaction has no identified merchant to
// confirm (ADR-0051); correct it instead.
const CodeNothingToConfirm Code = "nothing_to_confirm"

// CodeReviewInherited (409): the decision a DELETE would clear is inherited
// from the pending transaction the booking superseded (ADR-0051). The response
// names that transaction in review.transaction_id; clear the decision there.
const CodeReviewInherited Code = "review_inherited"

// enrichmentJSON is what a lookup source said, as stored: the source's own
// answer, never edited by a decision (ADR-0051).
type enrichmentJSON struct {
	Status merchants.Status `json:"status"`
	Name   *string          `json:"name"`
	// LogoRef is an opaque reference; nothing in Ledger fetches it here.
	LogoRef *string `json:"logo_ref"`
	Source  string  `json:"source"`
	// ConfidencePermille is the source's certainty, 0 to 1000, an exact
	// integer; null unless the merchant was identified.
	ConfidencePermille *int       `json:"confidence_permille"`
	Candidates         []string   `json:"candidates"`
	RetrievedAt        *time.Time `json:"retrieved_at"`
	LastFailedAt       *time.Time `json:"last_failed_at"`
	// MerchantID is the merchant record the identified name resolves to
	// through an alias (ADR-0058), or null.
	MerchantID *string `json:"merchant_id"`
}

type merchantReviewJSON struct {
	// TransactionID is the transaction the decision was made about: this one,
	// or the pending transaction it superseded when review_inherited is true.
	TransactionID string             `json:"transaction_id"`
	Decision      merchants.Decision `json:"decision"`
	Name          *string            `json:"name"`
	// MerchantID is the merchant record the decision attaches to; null for a
	// rejection.
	MerchantID *string     `json:"merchant_id"`
	By         deciderJSON `json:"by"`
	DecidedAt  time.Time   `json:"decided_at"`
	Version    int64       `json:"version"`
}

// merchantJSON is the merchant of a bank transaction: what to show (name,
// logo_ref, only when it may be shown), why (state, identity_source), what is
// left open (needs_review) and the two records it is resolved from, kept
// apart. fallback is true when the client must use its neutral presentation.
// It never carries an amount or a payment status.
type merchantJSON struct {
	TransactionID        string              `json:"transaction_id"`
	State                merchants.State     `json:"state"`
	MerchantID           *string             `json:"merchant_id"`
	Name                 *string             `json:"name"`
	LogoRef              *string             `json:"logo_ref"`
	IdentitySource       *string             `json:"identity_source"`
	Fallback             bool                `json:"fallback"`
	NeedsReview          bool                `json:"needs_review"`
	ConfirmationOutdated bool                `json:"confirmation_outdated"`
	ReviewInherited      bool                `json:"review_inherited"`
	Enrichment           *enrichmentJSON     `json:"enrichment"`
	Review               *merchantReviewJSON `json:"review"`
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func toMerchantJSON(m merchants.Merchant) merchantJSON {
	out := merchantJSON{TransactionID: string(m.TransactionID), State: m.State, MerchantID: strPtr(m.MerchantID), Name: strPtr(m.Name), LogoRef: strPtr(m.LogoRef), IdentitySource: strPtr(m.IdentitySource),
		Fallback: m.Fallback(), NeedsReview: m.NeedsReview, ConfirmationOutdated: m.ConfirmationOutdated, ReviewInherited: m.ReviewInherited}
	if e := m.Enrichment; e != nil {
		ej := &enrichmentJSON{Status: e.Status, Name: strPtr(e.Name), LogoRef: strPtr(e.LogoRef), Source: e.Source, Candidates: slices.Clone(e.Candidates), LastFailedAt: e.LastFailedAt,
			MerchantID: strPtr(e.MerchantID)}
		if ej.Candidates == nil {
			ej.Candidates = []string{}
		}
		if e.Status == merchants.StatusIdentified {
			c := int(e.Confidence)
			ej.ConfidencePermille = &c
		}
		if !e.RetrievedAt.IsZero() {
			t := e.RetrievedAt
			ej.RetrievedAt = &t
		}
		out.Enrichment = ej
	}
	if r := m.Review; r != nil {
		by := deciderJSON{Kind: r.By.Kind, UserID: strPtr(r.By.UserID)}
		if r.Via != "" {
			v := r.Via
			by.Via = &v
		}
		out.Review = &merchantReviewJSON{TransactionID: string(r.TransactionID), Decision: r.Decision, Name: strPtr(r.Name), MerchantID: strPtr(r.MerchantID), By: by, DecidedAt: r.DecidedAt, Version: r.Version}
	}
	return out
}

func merchantError(err error) error {
	var ve *commitments.ValidationError
	switch {
	case errors.As(err, &ve) && len(ve.Errors) > 0:
		return fieldErrors(ve.Errors)
	case errors.Is(err, merchants.ErrNothingToConfirm):
		return &Error{Status: http.StatusConflict, Code: CodeNothingToConfirm, Message: merchants.ErrNothingToConfirm.Error(), Cause: err}
	case errors.Is(err, merchants.ErrReviewInherited):
		return &Error{Status: http.StatusConflict, Code: CodeReviewInherited, Message: merchants.ErrReviewInherited.Error(), Cause: err}
	case errors.Is(err, merchants.ErrNoReview):
		return NotFound(merchants.ErrNoReview.Error())
	case errors.Is(err, merchants.ErrArchived):
		return &Error{Status: http.StatusConflict, Code: CodeMerchantArchived, Message: err.Error(), Cause: err}
	}
	return err
}

// MerchantSourceInfo describes one merchant-identification source for the
// client (task E2, ADR-0054): what it is, whether it is on, and what the
// licence requires to be shown with its answers.
type MerchantSourceInfo struct {
	// Name is the source token stored with an answer (the enrichment's source).
	Name string `json:"name"`
	// Kind is local (offline) or remote.
	Kind    string `json:"kind"`
	Enabled bool   `json:"enabled"`
	// Description says what the source does in a sentence.
	Description string `json:"description"`
	// SendsOffMachine lists what leaves the machine for a lookup; empty for a
	// local source.
	SendsOffMachine []string `json:"sends_off_machine"`
	// Attribution is the text to show with identities from this source, or null.
	Attribution *string `json:"attribution"`
	// StoresLogoRef is whether the source's logo references are kept.
	StoresLogoRef bool `json:"stores_logo_ref"`
	// RetentionHours is how long its answers are kept, or null for until replaced.
	RetentionHours *int `json:"retention_hours"`
}

// registerMerchantSources registers GET /api/v1/merchants/sources.
func registerMerchantSources(s *Server, infos []MerchantSourceInfo) {
	if infos == nil {
		infos = []MerchantSourceInfo{}
	}
	for i := range infos {
		if infos[i].SendsOffMachine == nil {
			infos[i].SendsOffMachine = []string{}
		}
	}
	s.HandleAPI("GET "+MerchantsPath+"/sources", Requires(grantMerchantsRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]any{"sources": infos})
	}))
}

// registerMerchants registers the merchant routes (task E1, ADR-0051): read a
// transaction's merchant, record the person's decision about it and take the
// decision back. The routes never change the transaction, an amount or a
// payment status. Merchant names derive from bank text (decision D13 treats
// counterparty names as sensitive), so the grants are administrator-only like
// banking:read.
func registerMerchants(s *Server, st merchants.Store) {
	item := TransactionsPath + "/{id}/merchant"
	review := item + "/review"

	s.HandleAPI("GET "+item, Requires(grantMerchantsRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m, err := st.Get(r.Context(), banking.TransactionID(r.PathValue("id")))
		if err != nil {
			WriteError(w, r, merchantError(err))
			return
		}
		WriteJSON(w, http.StatusOK, toMerchantJSON(m))
	}))

	s.HandleAPI("PUT "+review, Requires(grantMerchantsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		doc, err := decodeObject(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		var req merchants.Request
		var errs []commitments.FieldError
		for k := range doc {
			if k != "decision" && k != "name" && k != "merchant_id" {
				errs = append(errs, commitments.FieldError{Field: k, Message: "unknown field " + k})
			}
		}
		slices.SortFunc(errs, func(a, b commitments.FieldError) int { return strings.Compare(a.Field, b.Field) })
		var s string
		if raw, ok := doc["decision"]; !ok || isJSONNull(raw) {
			errs = append(errs, commitments.FieldError{Field: "decision", Message: "decision is required"})
		} else if json.Unmarshal(raw, &s) == nil {
			req.Decision = merchants.Decision(s)
		} else {
			errs = append(errs, commitments.FieldError{Field: "decision", Message: "decision must be a string"})
		}
		if raw, ok := doc["name"]; ok && !isJSONNull(raw) {
			if json.Unmarshal(raw, &req.Name) == nil {
				req.Name = merchants.Request{Name: req.Name}.Normalized().Name
			} else {
				errs = append(errs, commitments.FieldError{Field: "name", Message: "name must be a string"})
			}
		}
		if raw, ok := doc["merchant_id"]; ok && !isJSONNull(raw) {
			if json.Unmarshal(raw, &req.MerchantID) != nil || !resource.ValidID(req.MerchantID) {
				errs = append(errs, commitments.FieldError{Field: "merchant_id", Message: "merchant_id must be an identifier"})
			}
		}
		if len(errs) > 0 {
			WriteError(w, r, fieldErrors(errs))
			return
		}
		m, err := st.Review(r.Context(), matchActor(r), banking.TransactionID(r.PathValue("id")), req)
		if err != nil {
			WriteError(w, r, merchantError(err))
			return
		}
		WriteJSON(w, http.StatusOK, toMerchantJSON(m))
	}))

	s.HandleAPI("DELETE "+review, Requires(grantMerchantsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m, err := st.ClearReview(r.Context(), matchActor(r), banking.TransactionID(r.PathValue("id")))
		if err != nil {
			WriteError(w, r, merchantError(err))
			return
		}
		WriteJSON(w, http.StatusOK, toMerchantJSON(m))
	}))
}
