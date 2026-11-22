package enrichment

import (
	"context"
	"sort"
	"strings"

	"github.com/aurimasniekis/go-ledger/internal/domain/merchants"
)

// Entry is one curated merchant: the display name and the patterns (as [Key]
// words) that identify it. Domain documents where a logo would come from; no
// logo file ships, so the interface uses its neutral icon (a logo needs a
// licence, see ADR-0054).
type Entry struct {
	Name     string
	Domain   string
	Patterns []string
}

// DefaultCurated is a small built-in list of widely used merchants and
// services. It is a starting point, not a coverage claim: everything else is
// identified through the person's own confirmations.
func DefaultCurated() []Entry {
	return []Entry{
		{"Netflix", "netflix.com", []string{"netflix"}},
		{"Spotify", "spotify.com", []string{"spotify"}},
		{"Amazon", "amazon.com", []string{"amazon", "amzn"}},
		{"Apple", "apple.com", []string{"apple com bill", "itunes", "apple"}},
		{"Google", "google.com", []string{"google"}},
		{"Microsoft", "microsoft.com", []string{"microsoft", "msft"}},
		{"IKEA", "ikea.com", []string{"ikea"}},
		{"Lidl", "lidl.com", []string{"lidl"}},
		{"Aldi", "aldi.com", []string{"aldi"}},
		{"Tesco", "tesco.com", []string{"tesco"}},
		{"Uber", "uber.com", []string{"uber"}},
		{"Bolt", "bolt.eu", []string{"bolt"}},
		{"PayPal", "paypal.com", []string{"paypal"}},
		{"Ryanair", "ryanair.com", []string{"ryanair"}},
		{"Booking.com", "booking.com", []string{"booking com", "booking"}},
	}
}

// weakPatterns are single common words that also start or sit inside the names
// of unrelated businesses ("Bolt Hardware Store", "Apple Orchard Cafe"). Found
// inside longer text they give only [ConfidenceCuratedWeak], a suggestion below
// the display threshold; the whole text equal to them is still exact.
var weakPatterns = map[string]bool{"bolt": true, "apple": true, "booking": true, "uber": true, "google": true}

var curatedWords = func() map[string]bool {
	m := map[string]bool{}
	for _, e := range DefaultCurated() {
		for _, p := range e.Patterns {
			for _, w := range strings.Fields(Key(p)) {
				m[w] = true
			}
		}
	}
	return m
}()

func curatedWord(w string) bool { return curatedWords[w] }

// Curated is the local curated mapping source. It is pure and offline.
type Curated struct {
	entries []Entry
}

// NewCurated returns a Curated source. Patterns are reduced with [Key].
func NewCurated(entries []Entry) *Curated {
	c := &Curated{}
	for _, e := range entries {
		var ps []string
		for _, p := range e.Patterns {
			if k := Key(p); k != "" {
				ps = append(ps, k)
			}
		}
		if e.Name != "" && len(ps) > 0 {
			e.Patterns = ps
			c.entries = append(c.entries, e)
		}
	}
	return c
}

// Name implements [Source].
func (*Curated) Name() string { return SourceCurated }

// Lookup implements [Source]. One merchant matching gives an identified
// answer; several different merchants matching give an ambiguous one, never a
// guess.
func (c *Curated) Lookup(_ context.Context, s Subject) (merchants.Answer, error) {
	key := s.Key()
	best := map[string]merchants.Confidence{}
	for _, e := range c.entries {
		for _, p := range e.Patterns {
			var conf merchants.Confidence
			switch {
			case key == p:
				conf = ConfidenceCuratedExact
			case contains(key, p):
				conf = ConfidenceCuratedContains
				if weakPatterns[p] {
					conf = ConfidenceCuratedWeak
				}
			default:
				continue
			}
			if conf > best[e.Name] {
				best[e.Name] = conf
			}
		}
	}
	switch len(best) {
	case 0:
		return merchants.Answer{Status: merchants.StatusNone, Source: SourceCurated}, nil
	case 1:
		for name, conf := range best {
			return merchants.Answer{Status: merchants.StatusIdentified, Name: name, Source: SourceCurated, Confidence: conf}, nil
		}
	}
	names := make([]string, 0, len(best))
	for n := range best {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) > merchants.MaxCandidates {
		names = names[:merchants.MaxCandidates]
	}
	return merchants.Answer{Status: merchants.StatusAmbiguous, Source: SourceCurated, Candidates: names}, nil
}
