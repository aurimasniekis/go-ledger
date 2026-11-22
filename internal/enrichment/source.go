package enrichment

import (
	"context"

	"github.com/aurimasniekis/go-ledger/internal/domain/merchants"
)

// Source names the sources the service ships. They are lowercase tokens as the
// store requires.
const (
	SourceCurated = "curated"       // the built-in curated mapping
	SourceLearned = "local_mapping" // a person's confirmed mappings, reused as suggestions
	SourceNone    = "local"         // nothing identified it; no source answered
)

// Confidence levels, in permille, for the local sources. They are policy named
// here, not measurements. The suggestion level is below
// [merchants.DefaultThreshold] on purpose: a reused mapping is never shown
// without the person's confirmation (D15).
const (
	ConfidenceCuratedExact    merchants.Confidence = 950 // the key is exactly the curated pattern
	ConfidenceCuratedContains merchants.Confidence = 850 // the curated pattern is among the key's words
	ConfidenceCuratedWeak     merchants.Confidence = 650 // a generic word (Bolt, Apple...) inside longer text: a suggestion
	ConfidenceSuggestion      merchants.Confidence = 700 // reused from a confirmation: a suggestion
)

// Source answers a lookup. Lookup returns the answer, or an error when it could
// not answer (a failure is not "no match": a no-match is a StatusNone answer).
// Errors must not carry bank text, keys or response bodies.
type Source interface {
	Name() string
	Lookup(ctx context.Context, s Subject) (merchants.Answer, error)
}
