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
// (decision C-E2-remote, ADR-0054). It is empty, and nothing is sent, when the
// transaction looks like a transfer (transfer words in the counterparty or
// description) or the counterparty looks like a person. Otherwise it
// is the counterparty name, else the description, with every token that holds a
// digit removed (which takes out IBANs and account numbers, amounts, dates,
// references and store numbers), the words iban, bic, swift, account and acct
// removed, control characters removed, and at most 100 characters kept.
func (s Subject) MerchantString() string {
	used := s.Counterparty
	if strings.TrimSpace(used) == "" {
		used = s.Description
	}
	if looksLikeTransfer(s.Counterparty) || looksLikeTransfer(s.Description) {
		return ""
	}
	toks := tokens(used)
	if strings.TrimSpace(s.Counterparty) != "" && looksLikePerson(toks) {
		return ""
	}
	var out []string
	for _, t := range toks {
		if strings.IndexFunc(t, unicode.IsDigit) >= 0 || accountWords[strings.ToLower(t)] {
			continue
		}
		out = append(out, t)
	}
	t := strings.Join(out, " ")
	if r := []rune(t); len(r) > 100 {
		t = strings.TrimSpace(string(r[:100]))
	}
	if strings.IndexFunc(t, unicode.IsLetter) < 0 {
		return ""
	}
	return t
}

var accountWords = map[string]bool{"iban": true, "bic": true, "swift": true, "account": true, "acct": true}

// transferWords mark a payment between accounts or people, not a purchase.
var transferWords = map[string]bool{
	"transfer": true, "transfers": true, "sepa": true, "standing": true, "pavedimas": true, "pervedimas": true,
	"overforing": true, "ueberweisung": true, "überweisung": true, "virement": true,
}

// companyForms and businessWords tell a merchant from a person's name.
var companyForms = map[string]bool{
	"uab": true, "ltd": true, "limited": true, "gmbh": true, "inc": true, "llc": true, "as": true, "sia": true, "oy": true,
	"ab": true, "bv": true, "sa": true, "srl": true, "plc": true, "corp": true, "co": true, "ag": true, "oü": true,
	"mb": true, "sp": true, "zoo": true, "kft": true, "sarl": true, "nv": true,
}
var businessWords = map[string]bool{
	"shop": true, "store": true, "market": true, "supermarket": true, "cafe": true, "coffee": true, "bar": true, "pub": true,
	"restaurant": true, "pharmacy": true, "hotel": true, "bank": true, "taxi": true, "pizza": true, "kiosk": true, "bakery": true,
	"hardware": true, "services": true, "service": true, "group": true, "online": true, "express": true, "mobile": true,
	"energy": true, "insurance": true, "telecom": true, "fitness": true, "gym": true, "books": true, "foods": true,
}

func tokens(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) })
}

func looksLikeTransfer(s string) bool {
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) }) {
		if transferWords[w] {
			return true
		}
	}
	return false
}

// looksLikePerson: two or three purely alphabetic words, none a company form or
// a common business word. It is a heuristic (the bank data carries no
// counterparty kind) and errs towards sending nothing.
func looksLikePerson(toks []string) bool {
	if len(toks) < 2 || len(toks) > 3 {
		return false
	}
	for _, t := range toks {
		l := strings.ToLower(strings.Trim(t, ".,"))
		if companyForms[l] || businessWords[l] || curatedWord(l) {
			return false
		}
		for _, c := range t {
			if !unicode.IsLetter(c) && c != '-' && c != '\'' && c != '.' {
				return false
			}
		}
	}
	return true
}

// contains reports whether words (a Key) contains pattern (a Key) as whole,
// consecutive words.
func contains(key, pattern string) bool {
	if pattern == "" {
		return false
	}
	return strings.Contains(" "+key+" ", " "+pattern+" ")
}
