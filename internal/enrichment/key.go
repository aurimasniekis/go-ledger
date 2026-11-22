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

// MerchantString is the only bank text a remote source may be asked about
// (decision C-E2-remote, ADR-0054). Nothing is sent unless the text carries a
// positive merchant marker. The text is the counterparty name, else the
// description. It is cleaned first: control characters removed, split at white
// space and at the punctuation , / . and ;, every token that holds a digit
// removed (IBANs and account numbers, amounts, dates, references, store
// numbers) and the words iban, bic, swift, account and acct removed. The result
// is sent only when it holds a company form (UAB, Ltd, GmbH, AB, AS, SIA, Inc
// and the like) next to at least one other word, or a multi-word curated
// pattern ("apple com bill"). Business words and single curated words are not
// markers: they also sit in personal names. It is empty, and nothing is sent,
// for transfers, for names of people and for everything else without a marker.
// At most 100 characters are kept.
func (s Subject) MerchantString() string {
	used := s.Counterparty
	if strings.TrimSpace(used) == "" {
		used = s.Description
	}
	if looksLikeTransfer(s.Counterparty) || looksLikeTransfer(s.Description) {
		return ""
	}
	var out []string
	for _, t := range tokens(used) {
		if strings.IndexFunc(t, unicode.IsDigit) >= 0 || accountWords[strings.ToLower(t)] {
			continue
		}
		out = append(out, t)
	}
	if !hasMerchantMarker(out) {
		return ""
	}
	t := strings.Join(out, " ")
	if r := []rune(t); len(r) > 100 {
		t = strings.TrimSpace(string(r[:100]))
	}
	return t
}

// hasMerchantMarker: a company form beside another word, or a multi-word
// curated pattern.
func hasMerchantMarker(toks []string) bool {
	form, other := false, false
	for _, t := range toks {
		l := strings.ToLower(t)
		switch {
		case companyForms[l]:
			form = true
		case strings.IndexFunc(l, unicode.IsLetter) >= 0:
			other = true
		}
	}
	if form && other {
		return true
	}
	key := Key(strings.Join(toks, " "))
	for _, e := range DefaultCurated() {
		for _, p := range e.Patterns {
			if p = Key(p); strings.Contains(p, " ") && contains(key, p) {
				return true
			}
		}
	}
	return false
}

var accountWords = map[string]bool{"iban": true, "bic": true, "swift": true, "account": true, "acct": true}

// transferWords mark a payment between accounts or people, not a purchase.
var transferWords = map[string]bool{
	"transfer": true, "transfers": true, "sepa": true, "standing": true, "pavedimas": true, "pervedimas": true,
	"overforing": true, "ueberweisung": true, "überweisung": true, "virement": true,
}

// companyForms are the positive merchant marker.
var companyForms = map[string]bool{
	"uab": true, "ltd": true, "limited": true, "gmbh": true, "inc": true, "llc": true, "as": true, "sia": true, "oy": true,
	"ab": true, "bv": true, "sa": true, "srl": true, "plc": true, "corp": true, "co": true, "ag": true, "oü": true,
	"mb": true, "sp": true, "zoo": true, "kft": true, "sarl": true, "nv": true,
}

func tokens(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r) || strings.ContainsRune(",/.;", r)
	})
}

func looksLikeTransfer(s string) bool {
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) }) {
		if transferWords[w] {
			return true
		}
	}
	return false
}

// contains reports whether words (a Key) contains pattern (a Key) as whole,
// consecutive words.
func contains(key, pattern string) bool {
	if pattern == "" {
		return false
	}
	return strings.Contains(" "+key+" ", " "+pattern+" ")
}
