package decisions

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
)

// Consent is the operator's recorded consent to send the permitted fields to
// one provider for one purpose. It covers exactly that pair.
type Consent struct {
	ID       string
	Provider string
	Purpose  TaskName
	// Terms names the consent text the operator accepted (for example its
	// version); never a secret.
	Terms     string
	GrantedBy audit.Actor
	GrantedAt time.Time
	RevokedBy *audit.Actor
	RevokedAt *time.Time
	Version   int64
}

// Active reports whether the consent is in force.
func (c Consent) Active() bool { return c.RevokedAt == nil }

// Errors of consent records.
var (
	// ErrConsentExists means an active consent for the provider and
	// purpose is already recorded.
	ErrConsentExists = errors.New("consent for this provider and purpose is already recorded")
	// ErrNoConsent means there is no active consent to revoke.
	ErrNoConsent = errors.New("there is no active consent for this provider and purpose")
)

// ValidateConsent checks a consent to record.
func ValidateConsent(provider string, purpose TaskName, terms string) []commitments.FieldError {
	var errs []commitments.FieldError
	if _, ok := KnownProvider(provider); !ok {
		errs = append(errs, commitments.FieldError{Field: "provider", Message: "provider must be openrouter, openai, anthropic or typesafe"})
	}
	if _, ok := LookupTask(purpose); !ok {
		errs = append(errs, commitments.FieldError{Field: "purpose", Message: "purpose must be categorisation, label_suggestion, alerts or payment_matching"})
	}
	if t := strings.TrimSpace(terms); t == "" || utf8.RuneCountInString(t) > 200 || !utf8.ValidString(t) {
		errs = append(errs, commitments.FieldError{Field: "terms", Message: "terms must name the consent text accepted, 1 to 200 characters"})
	}
	return errs
}
