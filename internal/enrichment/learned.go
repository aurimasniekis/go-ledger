package enrichment

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/aurimasniekis/go-ledger/internal/domain/merchants"
)

// Confirmation is a mapping the person made: bank text and the name they
// confirmed or gave for it.
type Confirmation struct {
	Subject Subject
	Name    string
	// Rejected marks a rejection ("this is no merchant"); Name is empty.
	Rejected bool
}

// Learned reuses the person's confirmed and corrected mappings as suggestions
// for similar transactions (D15): the same matching key. It never confirms
// anything: its confidence is [ConfidenceSuggestion], below the threshold. Two
// different names for one key are ambiguous, never a guess. It holds a snapshot
// loaded by [Learned.Load].
type Learned struct {
	mu       sync.RWMutex
	byKey    map[string][]string
	rejected map[string]bool
	print    string
	loaded   bool
}

// NewLearned returns an empty Learned source.
func NewLearned() *Learned {
	return &Learned{byKey: map[string][]string{}, rejected: map[string]bool{}}
}

// Load replaces the snapshot with the given confirmations and reports whether
// it differs from the previous one (the first load always does).
func (l *Learned) Load(cs []Confirmation) bool {
	m := map[string][]string{}
	rej := map[string]bool{}
	for _, c := range cs {
		k := c.Subject.Key()
		if k == "" {
			continue
		}
		switch {
		case c.Rejected:
			rej[k] = true
		case c.Name != "":
			m[k] = merchants.Candidates(append(m[k], c.Name))
		}
	}
	var parts []string
	for k, ns := range m {
		parts = append(parts, "m|"+k+"|"+strings.Join(ns, "|"))
	}
	for k := range rej {
		parts = append(parts, "r|"+k)
	}
	sort.Strings(parts)
	print := strings.Join(parts, "\n")
	l.mu.Lock()
	defer l.mu.Unlock()
	changed := l.print != print || !l.loaded
	l.byKey, l.rejected, l.print, l.loaded = m, rej, print, true
	return changed
}

// Decided reports whether the person has a mapping or a rejection for the
// subject's key. Such a decision beats a curated or remote identity for the
// same key.
func (l *Learned) Decided(s Subject) bool {
	k := s.Key()
	l.mu.RLock()
	defer l.mu.RUnlock()
	return k != "" && (len(l.byKey[k]) > 0 || l.rejected[k])
}

// Rejected reports whether the person rejected the subject's key and gave no
// name for it.
func (l *Learned) Rejected(s Subject) bool {
	k := s.Key()
	l.mu.RLock()
	defer l.mu.RUnlock()
	return k != "" && l.rejected[k] && len(l.byKey[k]) == 0
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
