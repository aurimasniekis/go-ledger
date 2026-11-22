package enrichment

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Subject is the bank text a lookup starts from. It stays inside Ledger: the
// local sources read it, the remote source sees only what its field policy
// allows (see [RemoteQuery]).
type Subject struct {
	Description  string
	Counterparty string
}

// noise are words that bank text adds to a card payment and that say nothing
// about the merchant.
var noise = map[string]bool{
	"card": true, "payment": true, "purchase": true, "pos": true, "visa": true, "mastercard": true,
	"debit": true, "sepa": true, "transaction": true, "online": true, "ref": true, "the": true,
}

// Key reduces bank text to the lowercase words that name a merchant: letters
// only, so store numbers, dates and references drop out ("LIDL 1523 VILNIUS"
// and "Lidl 0042 Vilnius" give the same key), without noise words, at most 80
// characters. It is empty when nothing is left.
func Key(s string) string {
	words := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) })
	out := words[:0]
	for _, w := range words {
		if utf8.RuneCountInString(w) >= 2 && !noise[w] {
			out = append(out, w)
		}
	}
	k := strings.Join(out, " ")
	if r := []rune(k); len(r) > 80 {
		k = strings.TrimSpace(string(r[:80]))
	}
	return k
}

// Key is the subject's matching key: the counterparty when it gives one, else
// the description.
func (s Subject) Key() string {
	if k := Key(s.Counterparty); k != "" {
		return k
	}
	return Key(s.Description)
}

// MerchantString is the cleaned text a remote source may be asked about: the
// counterparty name, else the start of the description, without control
// characters, at most 100 characters.
func (s Subject) MerchantString() string {
	t := s.Counterparty
	if strings.TrimSpace(t) == "" {
		t = s.Description
	}
	t = strings.Join(strings.FieldsFunc(t, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }), " ")
	if r := []rune(t); len(r) > 100 {
		t = strings.TrimSpace(string(r[:100]))
	}
	return t
}

// contains reports whether words (a Key) contains pattern (a Key) as whole,
// consecutive words.
func contains(key, pattern string) bool {
	if pattern == "" {
		return false
	}
	return strings.Contains(" "+key+" ", " "+pattern+" ")
}
