package merchants

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
)

// Limits on stored text. Names are display text, never identifiers.
const (
	MaxName       = 200
	MaxLogoRef    = 500
	MaxSource     = 64
	MaxCandidates = 5
)

// TargetType is the audit target type of merchant reviews; the target ID is
// the bank transaction's.
const TargetType = "transaction"

// Confidence is a source's certainty in permille, 0 to 1000. It is an exact
// integer: nothing here uses binary floating point.
type Confidence int

// MaxConfidence is certainty.
const MaxConfidence Confidence = 1000

// DefaultThreshold is the confidence from which an unreviewed identity counts
// as reliable and is shown: a named default, 800 permille (decision D15, taken
// 2026-11-20, fixes the policy, a local curated mapping plus the person's
// confirmations, but not this number). Callers pass the threshold to [Resolve]
// and the store takes it as an option instead of this constant being read in
// deep code.
const DefaultThreshold Confidence = 800

// Status is the outcome of a lookup.
type Status string

// Lookup outcomes.
const (
	StatusIdentified  Status = "identified"  // one merchant, with a confidence
	StatusAmbiguous   Status = "ambiguous"   // several candidates, none chosen
	StatusNone        Status = "none"        // the source knows nothing about it
	StatusUnavailable Status = "unavailable" // the lookup failed and nothing earlier exists
)

// Enrichment is a lookup source's answer for one transaction.
type Enrichment struct {
	TransactionID banking.TransactionID
	Status        Status
	// Name and LogoRef are set for an identified merchant only. LogoRef is an
	// opaque reference (nothing here fetches it).
	Name    string
	LogoRef string
	// Source names the source, a lowercase token such as "curated".
	Source     string
	Confidence Confidence
	// Candidates are the names an ambiguous answer chose between.
	Candidates []string
	// RetrievedAt is when the source answered; zero when it never did.
	RetrievedAt time.Time
	// LastFailedAt is the latest failed lookup, if any. A failure never
	// replaces an earlier answer.
	LastFailedAt *time.Time
	// MerchantID is the merchant record the identified name resolves to
	// through an alias (task M1, ADR-0058), empty when no alias has the name.
	// The source and confidence above stay the provenance of the answer.
	MerchantID string
	// MerchantName is that merchant's current display name, filled in on read.
	MerchantName string
	// Recheck is set when a person's decision about another transaction of the
	// same merchant may change this answer: the next pass looks it up again.
	Recheck bool
}

// Answer is a lookup source's answer as an adapter hands it over (task E2),
// without the stored bookkeeping.
type Answer struct {
	Status     Status
	Name       string
	LogoRef    string
	Source     string
	Confidence Confidence
	Candidates []string
}

// Validate checks an answer and reports every problem.
func (a Answer) Validate() []commitments.FieldError {
	var errs []commitments.FieldError
	add := func(field, msg string) { errs = append(errs, commitments.FieldError{Field: field, Message: msg}) }
	if a.Source == "" || len(a.Source) > MaxSource || strings.IndexFunc(a.Source, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' && r != '.' && r != '-'
	}) >= 0 {
		add("source", "source must be a lowercase token of at most 64 characters")
	}
	if msg := checkText(a.LogoRef, MaxLogoRef, true); msg != "" {
		add("logo_ref", "logo_ref "+msg)
	}
	switch a.Status {
	case StatusIdentified:
		if msg := checkText(a.Name, MaxName, false); msg != "" {
			add("name", "name "+msg)
		}
		if a.Confidence < 0 || a.Confidence > MaxConfidence {
			add("confidence", "confidence must be between 0 and 1000")
		}
		if len(a.Candidates) > 0 {
			add("candidates", "an identified merchant has no candidates")
		}
	case StatusAmbiguous:
		if a.Name != "" || a.LogoRef != "" {
			add("name", "an ambiguous answer names no merchant")
		}
		if n := len(Candidates(a.Candidates)); n < 2 || n > MaxCandidates {
			add("candidates", fmt.Sprintf("an ambiguous answer needs 2 to %d candidates", MaxCandidates))
		}
		for _, c := range a.Candidates {
			if msg := checkText(c, MaxName, false); msg != "" {
				add("candidates", "a candidate "+msg)
			}
		}
		// The store keeps the de-duplicated list, so that is what the bounds
		// and the table's CHECK constraint apply to: duplicates are a field
		// error here, never a constraint failure there.
		if n := len(Candidates(a.Candidates)); n != len(a.Candidates) {
			add("candidates", "candidates must be distinct names")
		}
	case StatusNone, StatusUnavailable:
		if a.Name != "" || a.LogoRef != "" || len(a.Candidates) > 0 {
			add("status", "a "+string(a.Status)+" answer carries no merchant")
		}
	default:
		add("status", "status must be identified, ambiguous, none or unavailable")
	}
	if a.Status != StatusIdentified && a.Confidence != 0 {
		add("confidence", "only an identified merchant has a confidence")
	}
	return errs
}

// Decision is what the person decided about a transaction's merchant.
type Decision string

// Decisions.
const (
	DecisionConfirmed Decision = "confirmed" // the enrichment's name is right
	DecisionCorrected Decision = "corrected" // the name is the one the person gave
	DecisionRejected  Decision = "rejected"  // the transaction has no (known) merchant
)

// Review is the person's decision about one transaction's merchant. For a
// confirmation Name is the enrichment name that was confirmed; for a
// correction the name the person typed; empty for a rejection.
type Review struct {
	TransactionID banking.TransactionID
	Decision      Decision
	Name          string
	// MerchantID is the merchant record the decision attaches to (task M1,
	// ADR-0058): set for a confirmation or correction, empty for a rejection.
	MerchantID string
	// MerchantName is that merchant's current display name, filled in on read.
	MerchantName string
	By           audit.Actor
	// Via is how the decision came (session, token, mcp, system).
	Via       audit.Via
	DecidedAt time.Time
	Version   int64
}

// Request is a decision as a client sends it.
type Request struct {
	Decision Decision
	// Name is the correction's merchant name. A correction gives a name or a
	// MerchantID, not both.
	Name string
	// MerchantID corrects to an existing merchant record (task M1).
	MerchantID string
}

// Validate checks a request and reports every problem. Names are trimmed by
// [Request.Normalized] first.
func (r Request) Validate() []commitments.FieldError {
	var errs []commitments.FieldError
	switch r.Decision {
	case DecisionCorrected:
		switch {
		case r.MerchantID != "" && r.Name != "":
			errs = append(errs, commitments.FieldError{Field: "merchant_id", Message: "a correction gives a name or a merchant_id, not both"})
		case r.MerchantID != "":
		default:
			if msg := checkText(r.Name, MaxName, false); msg != "" {
				errs = append(errs, commitments.FieldError{Field: "name", Message: "name " + msg})
			}
		}
	case DecisionConfirmed, DecisionRejected:
		if r.Name != "" {
			errs = append(errs, commitments.FieldError{Field: "name", Message: "name is only for a correction"})
		}
		if r.MerchantID != "" {
			errs = append(errs, commitments.FieldError{Field: "merchant_id", Message: "merchant_id is only for a correction"})
		}
	default:
		errs = append(errs, commitments.FieldError{Field: "decision", Message: "decision must be confirmed, corrected or rejected"})
	}
	return errs
}

// Normalized trims the name, collapses runs of white space and composes it
// (NFC).
func (r Request) Normalized() Request {
	r.Name = NormalizeDisplayName(r.Name)
	return r
}

// checkText reports what is wrong with display text, or "".
func checkText(s string, max int, optional bool) string {
	switch {
	case s == "" && optional:
		return ""
	case s == "":
		return "is required"
	case !utf8.ValidString(s):
		return "must be valid UTF-8"
	case utf8.RuneCountInString(s) > max:
		return fmt.Sprintf("must be at most %d characters", max)
	case strings.IndexFunc(s, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r)
	}) >= 0:
		// Cf covers the bidirectional overrides (U+202E) and zero-width
		// characters that make a name read differently from what it is.
		// Zl and Zp are the line and paragraph separators (U+2028, U+2029): a
		// name must stay on one line.
		return "must not contain control, line or paragraph separators or invisible formatting characters"
	case strings.TrimSpace(s) != s:
		return "must not start or end with white space"
	}
	return ""
}

// State is where a transaction's merchant stands, for the client.
type State string

// Merchant states. The first three are the person's decisions and always show
// or hide the identity as decided; the rest describe the enrichment alone.
const (
	StateConfirmed   State = "confirmed"     // the person confirmed the enrichment's name
	StateCorrected   State = "corrected"     // the person gave the name
	StateRejected    State = "rejected"      // the person said there is no merchant
	StateReliable    State = "reliable"      // identified at or above the threshold, not reviewed
	StateUncertain   State = "uncertain"     // identified below the threshold: not shown, needs review
	StateAmbiguous   State = "ambiguous"     // several candidates: not shown, needs review
	StateNone        State = "none"          // the source found nothing
	StateUnavailable State = "unavailable"   // the lookup failed and nothing earlier is known
	StateNotLookedUp State = "not_looked_up" // no lookup has been recorded
)

// Merchant is the resolved view: what to show, and why. Name is empty when no
// identity may be shown; the client then uses its neutral fallback (a generic
// label and icon, or the bank's own text if it may show that). It is never an
// amount or a payment status.
type Merchant struct {
	TransactionID banking.TransactionID
	State         State
	// MerchantID is the merchant record of the identity shown, empty when
	// nothing is shown or the shown name has no record (an unreviewed source
	// name no alias knows). Name is then that record's display name.
	MerchantID string
	// Name and LogoRef are the identity to show, empty when none may be.
	// A correction has no logo: the source's logo belongs to its own name.
	Name    string
	LogoRef string
	// IdentitySource is "user" for a decision's name, else the source's name,
	// or empty when nothing is shown.
	IdentitySource string
	// NeedsReview is true where a person's decision would resolve something
	// the data leaves open: uncertain or ambiguous, or an outdated
	// confirmation.
	NeedsReview bool
	// ConfirmationOutdated is true when the person confirmed a name the
	// enrichment no longer reports. The confirmation is not applied; the
	// merchant is described by the enrichment as if unreviewed.
	ConfirmationOutdated bool
	// ReviewInherited is true when Review is the decision of the pending
	// transaction this one superseded (the booking arrived under a new key),
	// not one made about this transaction itself.
	ReviewInherited bool
	Enrichment      *Enrichment
	Review          *Review
}

// Fallback reports whether the client must use its neutral presentation.
func (m Merchant) Fallback() bool { return m.Name == "" }

// Resolve combines an enrichment and a review into the merchant to show.
// threshold is the confidence from which an unreviewed identity is reliable.
// Either input may be nil. It is pure: no clock, no I/O.
func Resolve(e *Enrichment, r *Review, threshold Confidence) Merchant {
	m := Merchant{Enrichment: e, Review: r}
	switch {
	case e != nil:
		m.TransactionID = e.TransactionID
	case r != nil:
		m.TransactionID = r.TransactionID
	}
	if r != nil {
		switch r.Decision {
		case DecisionCorrected:
			m.State, m.Name, m.IdentitySource = StateCorrected, r.Name, "user"
			m.MerchantID, m.Name = r.MerchantID, shownName(r.Name, r.MerchantName)
			return m
		case DecisionRejected:
			m.State = StateRejected
			return m
		case DecisionConfirmed:
			// A confirmation stands while the source still reports the name
			// that was confirmed, or another spelling of the same merchant
			// (an alias of the record the confirmation attached to).
			if e != nil && e.Status == StatusIdentified && (e.Name == r.Name || r.MerchantID != "" && e.MerchantID == r.MerchantID) {
				m.State, m.LogoRef, m.IdentitySource = StateConfirmed, e.LogoRef, "user"
				m.MerchantID, m.Name = r.MerchantID, shownName(e.Name, r.MerchantName)
				return m
			}
			m.ConfirmationOutdated, m.NeedsReview = true, true
		}
	}
	switch {
	case e == nil:
		m.State = StateNotLookedUp
	case e.Status == StatusIdentified && e.Confidence >= threshold:
		m.State, m.Name, m.LogoRef, m.IdentitySource = StateReliable, shownName(e.Name, e.MerchantName), e.LogoRef, e.Source
		m.MerchantID = e.MerchantID
	case e.Status == StatusIdentified:
		m.State, m.NeedsReview = StateUncertain, true
	case e.Status == StatusAmbiguous:
		m.State, m.NeedsReview = StateAmbiguous, true
	case e.Status == StatusNone:
		m.State = StateNone
	default:
		m.State = StateUnavailable
	}
	return m
}

// shownName is the record's display name when the identity has a record,
// else the name as decided or reported.
func shownName(name, merchantName string) string {
	if merchantName != "" {
		return merchantName
	}
	return name
}

// Errors of the merchant store.
var (
	// ErrNothingToConfirm means the transaction has no identified merchant to
	// confirm: correct it instead.
	ErrNothingToConfirm = errors.New("there is no identified merchant to confirm")
	// ErrNoReview means the transaction has no decision of its own or
	// inherited to clear.
	ErrNoReview = errors.New("the merchant has no decision to clear")
	// ErrReviewInherited means the decision a clear would remove is inherited
	// from the pending transaction this one superseded: it is cleared there.
	ErrReviewInherited = errors.New("the merchant decision is inherited from the pending transaction it replaces; clear it there")
)

// Candidates returns a sorted copy of names without duplicates.
func Candidates(names []string) []string {
	out := slices.Clone(names)
	slices.Sort(out)
	return slices.Compact(out)
}

// Store is the persistence of merchant enrichments and decisions.
type Store interface {
	// Get returns the resolved merchant of a bank transaction
	// (resource.ErrNotFound when there is no such transaction).
	Get(ctx context.Context, id banking.TransactionID) (Merchant, error)
	// RecordAnswer stores a lookup source's answer; an unavailable answer
	// never replaces an earlier one.
	RecordAnswer(ctx context.Context, id banking.TransactionID, a Answer) (Merchant, error)
	// Review records the person's decision; ClearReview takes it back.
	Review(ctx context.Context, actor audit.Actor, id banking.TransactionID, r Request) (Merchant, error)
	ClearReview(ctx context.Context, actor audit.Actor, id banking.TransactionID) (Merchant, error)
	// EntityStore keeps the merchant records decisions attach to (task M1).
	EntityStore
}
