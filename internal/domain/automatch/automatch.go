// Package automatch is the automatic payment-matching engine (task X2,
// ADR-0038). It pairs outgoing bank transactions with open commitment
// occurrences, confirms a pair only when every safeguard of decision D9
// passes, and otherwise leaves a suggestion for a person to review.
//
// The engine is deterministic and offline: [Plan] is a pure function of a
// [Snapshot] (the same data always gives the same actions, in the same order),
// and nothing here calls a bank, a model or the network. Writes go through
// the payment-match store as [audit.System], which applies its own guards
// whatever the engine decided (ADR-0035 point 11): automatic confirmation is
// 1:1 only, never on a pending or uncountable transaction, never an
// overpayment, and automation never undoes a person's decision (decision D33).
//
// Candidate generation uses the same currency (amounts are never converted,
// decision D5), an amount band and a date window around the due date. The
// transaction description is only a hint: its similarity score orders and
// explains suggestions and never confirms anything.
//
// A pair is confirmed automatically only when all of these hold (decision
// D9): the same currency; the transaction's whole amount equals the
// occurrence's amount, or falls short of it by no more than the commitment's
// tolerance; the transaction is booked (pending ones only produce suggestions,
// decision D8); it is dated within [ConfirmDays] of the due date; the
// occurrence has exactly one candidate transaction and the transaction exactly
// one candidate occurrence; and neither is already assigned (the occurrence
// has no payment evidence or confirmed match, the transaction no confirmed or
// other open match).
package automatch

import (
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/matches"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
)

// Policy values. ConfirmDays is decision D9; the candidate window and band
// are proposed in ADR-0038 (not operator decisions).
const (
	// ConfirmDays is how many days before or after the due date a
	// transaction may be dated and still be confirmed automatically (D9).
	ConfirmDays = 3
	// CandidateDays is how many days before or after the due date a
	// transaction may be dated and still be suggested.
	CandidateDays = 7
	// NearPercent is the amount band for suggestions: a transaction whose
	// amount differs from the occurrence's by at most this percentage (or by
	// at most the commitment's tolerance, if larger) is a candidate.
	NearPercent = 10
)

// Transaction is a bank transaction the engine may match: outgoing (a
// negative amount), still reported, not superseded, booked or pending. The
// snapshot leaves every other transaction out.
type Transaction struct {
	ID     banking.TransactionID
	Amount money.Money // signed source amount; negative
	Status banking.BookingStatus
	// Date is the booking date, else the value date, else the transaction
	// date.
	Date calendar.Date
	// Text is the description and counterparty, used only for the hint
	// score. It is never logged, stored in a reason or sent anywhere.
	Text string
}

// Magnitude is the transaction amount without its sign.
func (t Transaction) Magnitude() money.Money { return t.Amount.Abs() }

// Occurrence is an open occurrence: not cancelled, with no payment evidence
// (manual payment or countable confirmed match) and no confirmed match. The
// snapshot leaves every other occurrence out.
type Occurrence struct {
	ID           string
	CommitmentID string
	// Name is the commitment's name, used only for the hint score.
	Name    string
	DueDate calendar.Date
	Amount  money.Money
	// Tolerance is the commitment's match tolerance; nil when none.
	Tolerance *money.Money
}

// Existing is a stored payment match, in any state.
type Existing struct {
	ID            string
	TransactionID banking.TransactionID
	OccurrenceID  string
	State         matches.State
	Origin        matches.Origin
	Version       int64
	// ByPerson is true when a user or token made, decided or corrected the
	// match at any point of its history: automation never changes it.
	ByPerson bool
	// BySystem is true when the system created it (the engine's own match).
	BySystem bool
}

// own reports whether the engine may act on the match: its own auto_rule
// suggestion that no person has touched (decision D33).
func (e Existing) own() bool {
	return e.State == matches.StateSuggested && e.Origin == matches.OriginAutoRule && e.BySystem && !e.ByPerson
}

// blocks reports whether the match closes its pair to the engine for good: a
// person rejected, unmatched or reversed it. A rejection by the engine itself
// (it dropped a suggestion that went stale) does not block: the pair may be
// suggested again when it becomes a candidate again.
func (e Existing) blocks() bool {
	return e.State == matches.StateReversed || (e.State == matches.StateRejected && e.ByPerson)
}

// Snapshot is what one run reads.
type Snapshot struct {
	Transactions []Transaction
	Occurrences  []Occurrence
	Matches      []Existing
}

// ActionKind is what an action does.
type ActionKind string

// Action kinds, in the order a run applies them.
const (
	// ActionDrop rejects one of the engine's own suggestions that is no longer
	// a candidate (decision D33 allows automation to drop its own suggestions
	// and nothing else).
	ActionDrop ActionKind = "drop"
	// ActionConfirm creates a confirmed match (MatchID empty) or confirms the
	// engine's own suggestion (MatchID set), as origin auto_rule.
	ActionConfirm ActionKind = "confirm"
	// ActionSuggest creates a suggestion for review.
	ActionSuggest ActionKind = "suggest"
)

// Action is one write the run makes.
type Action struct {
	Kind ActionKind
	// MatchID and Version name an existing match (drop, or confirm of a
	// suggestion).
	MatchID string
	Version int64

	TransactionID banking.TransactionID
	OccurrenceID  string
	// Amount is the allocation: the transaction's whole magnitude (automatic
	// matching is 1:1 only, decision D8).
	Amount money.Money
	// Tolerance is the commitment's tolerance, passed to the store's own
	// check of an automatic confirmation; the zero value means none.
	Tolerance money.Money
	// Checks are the safeguards as evaluated (not set for a drop).
	Checks Checks
	// Reason is kept on the match and in its history. It never holds bank
	// text.
	Reason string
}

// AmountResult is how the amounts compare.
type AmountResult string

// Amount results. Only exact and within_tolerance pass.
const (
	AmountExact     AmountResult = "exact"
	AmountTolerance AmountResult = "within_tolerance"
	// AmountAbove is a payment larger than the occurrence's amount: confirming
	// it would overpay, which only a person may allow.
	AmountAbove AmountResult = "above_expected"
	// AmountDiffers is a payment smaller than the amount by more than the
	// tolerance (or any difference without a tolerance).
	AmountDiffers AmountResult = "differs"
)

// Checks are the D9 safeguards for one candidate pair.
type Checks struct {
	Amount AmountResult
	// Difference is the absolute difference between the amounts.
	Difference money.Money
	Booked     bool
	// Days is how many days the transaction is dated from the due date
	// (absolute).
	Days int
	// Transactions counts the occurrence's candidate transactions;
	// Occurrences the transaction's candidate occurrences.
	Transactions int
	Occurrences  int
	// OtherOpen counts the transaction's open matches with other
	// occurrences that this run does not drop.
	OtherOpen int
	// Score is the description similarity, 0 to 100. It is a hint only and
	// is not one of the safeguards.
	Score int
}

// Failed lists the safeguards that did not pass, as stable codes, in a fixed
// order. The score never appears: it is not a safeguard.
func (c Checks) Failed() []string {
	var out []string
	if c.Amount != AmountExact && c.Amount != AmountTolerance {
		out = append(out, "amount_"+string(c.Amount))
	}
	if !c.Booked {
		out = append(out, "pending")
	}
	if c.Days > ConfirmDays {
		out = append(out, "date_outside_window")
	}
	if c.Transactions != 1 {
		out = append(out, "several_transactions")
	}
	if c.Occurrences != 1 {
		out = append(out, "several_occurrences")
	}
	if c.OtherOpen != 0 {
		out = append(out, "transaction_has_open_match")
	}
	return out
}

// Pass reports whether every safeguard passed.
func (c Checks) Pass() bool { return len(c.Failed()) == 0 }

// reason explains the checks in words a person can read, with the codes of
// [Checks.Failed]. It never holds bank text.
func (c Checks) reason() string {
	var parts []string
	for _, code := range c.Failed() {
		switch code {
		case "amount_above_expected":
			parts = append(parts, code+" (by "+c.Difference.String()+"; confirming would overpay)")
		case "amount_differs":
			parts = append(parts, code+" (by "+c.Difference.String()+")")
		case "pending":
			parts = append(parts, code+" (only a booked transaction can be confirmed)")
		case "date_outside_window":
			parts = append(parts, code+" ("+strconv.Itoa(c.Days)+" days from the due date; at most "+strconv.Itoa(ConfirmDays)+")")
		case "several_transactions":
			parts = append(parts, code+" ("+strconv.Itoa(c.Transactions)+" candidate transactions for this occurrence)")
		case "several_occurrences":
			parts = append(parts, code+" ("+strconv.Itoa(c.Occurrences)+" candidate occurrences for this transaction)")
		case "transaction_has_open_match":
			parts = append(parts, code+" ("+strconv.Itoa(c.OtherOpen)+" other open match)")
		default:
			parts = append(parts, code)
		}
	}
	hint := "description similarity " + strconv.Itoa(c.Score) + "/100 (a hint only)"
	if len(parts) == 0 {
		amount := "exact amount"
		if c.Amount == AmountTolerance {
			amount = "within the commitment's tolerance (short by " + c.Difference.String() + ")"
		}
		return "automatic match confirmed: " + amount + ", booked, " + strconv.Itoa(c.Days) +
			" days from the due date, the only candidate on both sides; " + hint
	}
	return "automatic match not confirmed: " + strings.Join(parts, "; ") + "; " + hint
}

type pair struct {
	tx  banking.TransactionID
	occ string
}

// Plan decides the actions for s. It is deterministic: drops first (by match
// ID), then confirmations, then suggestions, each by due date, occurrence ID
// and transaction ID.
func Plan(s Snapshot) []Action {
	actions, _ := plan(s)
	return actions
}

// plan is [Plan] that also counts the candidate pairs, including those
// already in the review queue.
func plan(s Snapshot) ([]Action, int) {
	txs := slices.Clone(s.Transactions)
	slices.SortFunc(txs, func(a, b Transaction) int { return strings.Compare(string(a.ID), string(b.ID)) })
	occs := slices.Clone(s.Occurrences)
	slices.SortFunc(occs, func(a, b Occurrence) int {
		if c := a.DueDate.Compare(b.DueDate); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	ms := slices.Clone(s.Matches)
	slices.SortFunc(ms, func(a, b Existing) int { return strings.Compare(a.ID, b.ID) })

	// Transactions already assigned (any confirmed match) are not candidates;
	// pairs a person closed never come back; open matches are remembered per
	// pair and per transaction.
	assigned := map[banking.TransactionID]bool{}
	blocked := map[pair]bool{}
	open := map[pair]Existing{}
	for _, m := range ms {
		p := pair{m.TransactionID, m.OccurrenceID}
		switch {
		case m.State == matches.StateConfirmed:
			assigned[m.TransactionID] = true
			open[p] = m
		case m.State == matches.StateSuggested:
			open[p] = m
		case m.blocks():
			blocked[p] = true
		}
	}

	// Candidate pairs.
	type cand struct {
		t Transaction
		o Occurrence
	}
	var cands []cand
	isCand := map[pair]bool{}
	perTx := map[banking.TransactionID]int{}
	perOcc := map[string]int{}
	for _, o := range occs {
		for _, t := range txs {
			if assigned[t.ID] || blocked[pair{t.ID, o.ID}] || !candidate(t, o) {
				continue
			}
			cands = append(cands, cand{t, o})
			isCand[pair{t.ID, o.ID}] = true
			perTx[t.ID]++
			perOcc[o.ID]++
		}
	}

	// The engine's own suggestions that are no longer candidates are dropped.
	var drops []Action
	dropped := map[string]bool{}
	for _, m := range ms {
		if !m.own() || isCand[pair{m.TransactionID, m.OccurrenceID}] {
			continue
		}
		dropped[m.ID] = true
		drops = append(drops, Action{Kind: ActionDrop, MatchID: m.ID, Version: m.Version, TransactionID: m.TransactionID, OccurrenceID: m.OccurrenceID,
			Reason: "automatic suggestion dropped: no longer a candidate (the transaction was assigned or changed, or the occurrence was paid or changed)"})
	}
	otherOpen := map[banking.TransactionID][]Existing{}
	for _, m := range ms {
		if m.State.Open() && !dropped[m.ID] {
			otherOpen[m.TransactionID] = append(otherOpen[m.TransactionID], m)
		}
	}

	var confirms, suggests []Action
	for _, c := range cands {
		p := pair{c.t.ID, c.o.ID}
		ch := evaluate(c.t, c.o)
		ch.Transactions, ch.Occurrences = perOcc[c.o.ID], perTx[c.t.ID]
		for _, m := range otherOpen[c.t.ID] {
			if m.OccurrenceID != c.o.ID {
				ch.OtherOpen++
			}
		}
		var tol money.Money
		if c.o.Tolerance != nil {
			tol = *c.o.Tolerance
		}
		a := Action{TransactionID: c.t.ID, OccurrenceID: c.o.ID, Amount: c.t.Magnitude(), Tolerance: tol, Checks: ch, Reason: ch.reason()}
		existing, has := open[p]
		switch {
		case has && !existing.own():
			// A person's match, a model suggestion or a confirmation: never
			// touched.
			continue
		case has && ch.Pass():
			a.Kind, a.MatchID, a.Version = ActionConfirm, existing.ID, existing.Version
			confirms = append(confirms, a)
		case has:
			// Still only a suggestion: already in the review queue.
			continue
		case ch.Pass():
			a.Kind = ActionConfirm
			confirms = append(confirms, a)
		default:
			a.Kind = ActionSuggest
			suggests = append(suggests, a)
		}
	}
	return append(append(drops, confirms...), suggests...), len(cands)
}

// candidate reports whether t could pay o: the same currency, dated within
// [CandidateDays] of the due date, and an amount within the band.
func candidate(t Transaction, o Occurrence) bool {
	if t.Amount.Sign() >= 0 || t.Amount.Currency() != o.Amount.Currency() {
		return false
	}
	if t.Status != banking.StatusBooked && t.Status != banking.StatusPending {
		return false
	}
	if days(t.Date, o.DueDate) > CandidateDays {
		return false
	}
	diff, err := t.Magnitude().Sub(o.Amount)
	if err != nil {
		return false
	}
	diff = diff.Abs()
	if o.Tolerance != nil {
		if c, err := diff.Cmp(*o.Tolerance); err == nil && c <= 0 {
			return true
		}
	}
	// |diff| * 100 <= amount * NearPercent, in exact decimals.
	lhs := diff.Amount().Mul(money.NewDecimal(100, 0))
	rhs := o.Amount.Amount().Mul(money.NewDecimal(NearPercent, 0))
	return lhs.Cmp(rhs) <= 0
}

// evaluate fills in the checks that depend on the pair alone.
func evaluate(t Transaction, o Occurrence) Checks {
	ch := Checks{Booked: t.Status == banking.StatusBooked, Days: days(t.Date, o.DueDate), Score: Score(o.Name, t.Text)}
	mag := t.Magnitude()
	diff, _ := mag.Sub(o.Amount) // same currency: checked by candidate
	ch.Difference = diff.Abs()
	switch c, _ := mag.Cmp(o.Amount); {
	case c == 0:
		ch.Amount = AmountExact
	case c > 0:
		ch.Amount = AmountAbove
	case o.Tolerance != nil:
		if d, err := ch.Difference.Cmp(*o.Tolerance); err == nil && d <= 0 {
			ch.Amount = AmountTolerance
		} else {
			ch.Amount = AmountDiffers
		}
	default:
		ch.Amount = AmountDiffers
	}
	return ch
}

// days is the absolute number of days between a and b.
func days(a, b calendar.Date) int {
	n := a.DaysUntil(b)
	if n < 0 {
		return -n
	}
	return n
}

// Score is the description similarity of a commitment name and a
// transaction's text: the percentage (0 to 100) of the name's words of three
// or more letters or digits that appear in the text, ignoring case. It is a
// hint for people reading suggestions; the engine never confirms on it.
func Score(name, text string) int {
	want := words(name)
	if len(want) == 0 {
		return 0
	}
	have := map[string]bool{}
	for _, w := range words(text) {
		have[w] = true
	}
	n := 0
	for _, w := range want {
		if have[w] {
			n++
		}
	}
	return n * 100 / len(want)
}

// words splits s into distinct lower-case words of at least three letters or
// digits, in first-seen order.
func words(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len([]rune(f)) >= 3 && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}
