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
// (decisions C-E2-remote and C-E2-forms-2, ADR-0054). It is built from the
// counterparty name alone: description text is never sent and never a
// fallback. The name is cleaned first: control characters removed, split at
// white space and at the punctuation , / . and ;, every token that holds a
// digit removed (IBANs, account numbers, store numbers) and the words iban,
// bic, swift, account and acct removed. The result is sent only when it starts
// or ends, as a whole token in any case, with one of the unambiguous company
// forms in companyFormList and holds at least one other word with a letter in
// it. A bare form, a form plus punctuation ("UAB &"), the short ambiguous forms
// (as, co, sa, ab, ag, sp, oy, nv, bv), names of people and transfers send
// nothing. At most 100 characters are kept.
func (s Subject) MerchantString() string {
	if looksLikeTransfer(s.Counterparty) || looksLikeTransfer(s.Description) {
		return ""
	}
	var out []string
	for _, t := range tokens(s.Counterparty) {
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

// hasMerchantMarker: an unambiguous company form as the first or last token,
// beside at least one other word that holds a letter.
func hasMerchantMarker(toks []string) bool {
	if len(toks) < 2 || (!isCompanyForm(toks[0]) && !isCompanyForm(toks[len(toks)-1])) {
		return false
	}
	for _, t := range toks {
		if !isCompanyForm(t) && strings.IndexFunc(t, unicode.IsLetter) >= 0 {
			return true
		}
	}
	return false
}

func isCompanyForm(t string) bool { return companyForms[strings.ToLower(t)] }

var accountWords = map[string]bool{"iban": true, "bic": true, "swift": true, "account": true, "acct": true}

// transferWords mark a payment between accounts or people, not a purchase.
var transferWords = map[string]bool{
	"transfer": true, "transfers": true, "sepa": true, "standing": true, "pavedimas": true, "pervedimas": true,
	"overforing": true, "ueberweisung": true, "überweisung": true, "virement": true,
}

// companyFormList is the fixed list of unambiguous company forms that mark a
// counterparty as a business (decision C-E2-forms-2). Short forms that are
// also ordinary words or initials (as, co, sa, ab, ag, sp, oy, nv, bv) are not
// on it and never count. It is unexported and never changed after init: the
// lookup set companyForms is built from it once, and callers outside this
// package cannot modify either.
var companyFormList = []string{"uab", "mb", "sia", "ltd", "limited", "llc", "gmbh", "inc", "plc", "corp", "srl", "sarl", "kft", "oü"}

var companyForms = func() map[string]bool {
	m := map[string]bool{}
	for _, f := range companyFormList {
		m[f] = true
	}
	return m
}()

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
