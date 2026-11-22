package decisions_test

import (
	"slices"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/decisions"
)

// TestAuditListsMatchTheRegistry: the audit package keeps its own copies of
// the provider and purpose names (it cannot import this package); they must
// agree.
func TestAuditListsMatchTheRegistry(t *testing.T) {
	var providers, purposes []string
	for _, d := range decisions.KnownProviders() {
		providers = append(providers, d.Name)
	}
	for _, n := range decisions.TaskNames() {
		purposes = append(purposes, string(n))
	}
	for _, a := range []audit.Action{audit.DecisionConsentGranted, audit.DecisionConsentRevoked,
		audit.DecisionSuggestionAccepted, audit.DecisionSuggestionRejected, audit.DecisionSuggestionCorrected, audit.DecisionSuggestionAutoApplied,
		audit.DecisionSuggestionReverted, audit.DecisionSuggestionDismissed} {
		for _, f := range audit.Definitions[a] {
			switch f.Name {
			case "provider":
				if !slices.Equal(f.Enum, providers) {
					t.Errorf("%s provider %v, want %v", a, f.Enum, providers)
				}
			case "task", "purpose":
				if !slices.Equal(f.Enum, purposes) {
					t.Errorf("%s purpose %v, want %v", a, f.Enum, purposes)
				}
			}
		}
	}
}
