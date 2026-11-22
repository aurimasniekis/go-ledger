package enrichment

import (
	"context"
	"sort"
	"sync"

	"github.com/aurimasniekis/go-ledger/internal/domain/merchants"
)

// Confirmation is a mapping the person made: bank text and the name they
// confirmed or gave for it.
type Confirmation struct {
	Subject Subject
	Name    string
}

// Learned reuses the person's confirmed and corrected mappings as suggestions
// for similar transactions (D15): the same matching key. It never confirms
// anything: its confidence is [ConfidenceSuggestion], below the threshold. Two
// different names for one key are ambiguous, never a guess. It holds a snapshot
// loaded by [Learned.Load].
type Learned struct {
	mu    sync.RWMutex
	byKey map[string][]string
}

// NewLearned returns an empty Learned source.
func NewLearned() *Learned { return &Learned{byKey: map[string][]string{}} }

// Load replaces the snapshot with the given confirmations.
func (l *Learned) Load(cs []Confirmation) {
	m := map[string][]string{}
	for _, c := range cs {
		k := c.Subject.Key()
		if k == "" || c.Name == "" {
			continue
		}
		m[k] = merchants.Candidates(append(m[k], c.Name))
	}
	l.mu.Lock()
	l.byKey = m
	l.mu.Unlock()
}

// Name implements [Source].
func (*Learned) Name() string { return SourceLearned }

// Lookup implements [Source].
func (l *Learned) Lookup(_ context.Context, s Subject) (merchants.Answer, error) {
	l.mu.RLock()
	names := l.byKey[s.Key()]
	l.mu.RUnlock()
	switch {
	case s.Key() == "" || len(names) == 0:
		return merchants.Answer{Status: merchants.StatusNone, Source: SourceLearned}, nil
	case len(names) == 1:
		return merchants.Answer{Status: merchants.StatusIdentified, Name: names[0], Source: SourceLearned, Confidence: ConfidenceSuggestion}, nil
	}
	c := append([]string(nil), names...)
	sort.Strings(c)
	if len(c) > merchants.MaxCandidates {
		c = c[:merchants.MaxCandidates]
	}
	return merchants.Answer{Status: merchants.StatusAmbiguous, Source: SourceLearned, Candidates: c}, nil
}
