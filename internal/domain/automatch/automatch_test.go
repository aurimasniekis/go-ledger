package automatch

import (
	"context"
	"errors"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/matches"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
)

func date(s string) calendar.Date {
	d, err := calendar.ParseDate(s)
	if err != nil {
		panic(err)
	}
	return d
}

func eur(s string) money.Money { return money.MustParse(s, "EUR") }

func booked(id, amount, day, text string) Transaction {
	return Transaction{ID: banking.TransactionID(id), Amount: eur(amount), Status: banking.StatusBooked, Date: date(day), Text: text}
}

func pending(id, amount, day, text string) Transaction {
	t := booked(id, amount, day, text)
	t.Status = banking.StatusPending
	return t
}

func occ(id, name, amount, due string) Occurrence {
	return Occurrence{ID: id, CommitmentID: "c-" + id, Name: name, DueDate: date(due), Amount: eur(amount)}
}

func withTolerance(o Occurrence, t string) Occurrence {
	m := eur(t)
	o.Tolerance = &m
	return o
}

// kinds renders a plan as "kind:tx>occ" items for compact comparisons.
func kinds(plan []Action) string {
	var parts []string
	for _, a := range plan {
		s := string(a.Kind) + ":" + string(a.TransactionID) + ">" + a.OccurrenceID
		if a.MatchID != "" {
			s += "#" + a.MatchID
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

func only(t *testing.T, plan []Action) Action {
	t.Helper()
	if len(plan) != 1 {
		t.Fatalf("plan %q, want one action", kinds(plan))
	}
	return plan[0]
}

// AC: an exact match is confirmed automatically.
func TestExactMatchConfirms(t *testing.T) {
	a := only(t, Plan(Snapshot{
		Transactions: []Transaction{booked("t1", "-800.00", "2026-10-02", "RENT 2026-10 Example Landlord")},
		Occurrences:  []Occurrence{occ("o1", "Rent", "800.00", "2026-10-01")},
	}))
	if a.Kind != ActionConfirm || a.MatchID != "" || a.Amount.String() != "800.00 EUR" {
		t.Fatalf("action %+v", a)
	}
	if !a.Checks.Pass() || a.Checks.Amount != AmountExact || a.Checks.Days != 1 || a.Checks.Score != 100 {
		t.Errorf("checks %+v", a.Checks)
	}
	if !strings.HasPrefix(a.Reason, "automatic match confirmed: exact amount, booked, 1 days") {
		t.Errorf("reason %q", a.Reason)
	}
	if strings.Contains(a.Reason, "RENT") || strings.Contains(a.Reason, "Landlord") {
		t.Errorf("bank text in the reason: %q", a.Reason)
	}
}

// AC: near amounts only suggest; with a tolerance a shortfall within it
// confirms, a payment above the amount never does.
func TestNearAmounts(t *testing.T) {
	tx := booked("t1", "-12.49", "2026-10-05", "STREAMING SERVICE")
	a := only(t, Plan(Snapshot{Transactions: []Transaction{tx}, Occurrences: []Occurrence{occ("o1", "Streaming", "12.99", "2026-10-05")}}))
	if a.Kind != ActionSuggest || a.Checks.Amount != AmountDiffers || a.Checks.Difference.String() != "0.50 EUR" {
		t.Fatalf("no tolerance: %+v", a)
	}
	if !strings.Contains(a.Reason, "amount_differs (by 0.50 EUR)") {
		t.Errorf("reason %q", a.Reason)
	}

	a = only(t, Plan(Snapshot{Transactions: []Transaction{tx}, Occurrences: []Occurrence{withTolerance(occ("o1", "Streaming", "12.99", "2026-10-05"), "0.50")}}))
	if a.Kind != ActionConfirm || a.Checks.Amount != AmountTolerance || a.Tolerance.String() != "0.50 EUR" {
		t.Fatalf("within the tolerance: %+v", a)
	}
	a = only(t, Plan(Snapshot{Transactions: []Transaction{tx}, Occurrences: []Occurrence{withTolerance(occ("o1", "Streaming", "12.99", "2026-10-05"), "0.49")}}))
	if a.Kind != ActionSuggest || a.Checks.Amount != AmountDiffers {
		t.Fatalf("just beyond the tolerance: %+v", a)
	}

	// Above the amount: would overpay, whatever the tolerance.
	above := booked("t2", "-13.49", "2026-10-05", "STREAMING SERVICE")
	a = only(t, Plan(Snapshot{Transactions: []Transaction{above}, Occurrences: []Occurrence{withTolerance(occ("o1", "Streaming", "12.99", "2026-10-05"), "1.00")}}))
	if a.Kind != ActionSuggest || a.Checks.Amount != AmountAbove || !strings.Contains(a.Reason, "would overpay") {
		t.Fatalf("above the amount: %+v", a)
	}

	// Outside the 10% band and the tolerance: not even a candidate.
	far := booked("t3", "-10.79", "2026-10-05", "STREAMING SERVICE")
	if plan := Plan(Snapshot{Transactions: []Transaction{far}, Occurrences: []Occurrence{occ("o1", "Streaming", "12.00", "2026-10-05")}}); len(plan) != 0 {
		t.Errorf("outside the band: %q", kinds(plan))
	}
	// Exactly 10% is inside the band.
	edge := booked("t4", "-10.80", "2026-10-05", "")
	if a := only(t, Plan(Snapshot{Transactions: []Transaction{edge}, Occurrences: []Occurrence{occ("o1", "Streaming", "12.00", "2026-10-05")}})); a.Kind != ActionSuggest {
		t.Errorf("at the band edge: %+v", a)
	}
}

// AC: two candidate transactions for one occurrence are ambiguous: both are
// suggested, neither confirmed.
func TestTwoCandidateTransactionsAreAmbiguous(t *testing.T) {
	plan := Plan(Snapshot{
		Transactions: []Transaction{booked("t1", "-9.99", "2026-10-10", "MUSIC"), booked("t2", "-9.99", "2026-10-11", "VIDEO")},
		Occurrences:  []Occurrence{occ("o1", "Music", "9.99", "2026-10-10")},
	})
	if got := kinds(plan); got != "suggest:t1>o1 suggest:t2>o1" {
		t.Fatalf("plan %q", got)
	}
	for _, a := range plan {
		if a.Checks.Transactions != 2 || !strings.Contains(a.Reason, "several_transactions (2 candidate transactions") {
			t.Errorf("%+v", a)
		}
	}
	// A higher description score does not break the tie (t1 scores 100).
	if plan[0].Checks.Score != 100 || plan[0].Kind != ActionSuggest {
		t.Errorf("score must not break ambiguity: %+v", plan[0])
	}
}

func TestTwoCandidateOccurrencesAreAmbiguous(t *testing.T) {
	plan := Plan(Snapshot{
		Transactions: []Transaction{booked("t1", "-9.99", "2026-10-10", "")},
		Occurrences:  []Occurrence{occ("o1", "Music", "9.99", "2026-10-10"), occ("o2", "Video", "9.99", "2026-10-12")},
	})
	if got := kinds(plan); got != "suggest:t1>o1 suggest:t1>o2" {
		t.Fatalf("plan %q", got)
	}
	if !strings.Contains(plan[0].Reason, "several_occurrences (2") {
		t.Errorf("reason %q", plan[0].Reason)
	}
}

// AC: two identical transactions (a duplicate) never auto-confirm; a
// transaction already assigned is not a candidate at all.
func TestDuplicateTransaction(t *testing.T) {
	plan := Plan(Snapshot{
		Transactions: []Transaction{booked("t1", "-3.50", "2026-10-07", "COFFEE KIOSK"), booked("t2", "-3.50", "2026-10-07", "COFFEE KIOSK")},
		Occurrences:  []Occurrence{occ("o1", "Coffee", "3.50", "2026-10-07")},
	})
	if got := kinds(plan); got != "suggest:t1>o1 suggest:t2>o1" {
		t.Fatalf("duplicates: %q", got)
	}
	// Already assigned: last month's rent is confirmed on t1, so t1 is no
	// candidate for this month and t2 stands alone.
	plan = Plan(Snapshot{
		Transactions: []Transaction{booked("t1", "-800.00", "2026-10-01", ""), booked("t2", "-800.00", "2026-10-02", "")},
		Occurrences:  []Occurrence{occ("o2", "Rent", "800.00", "2026-10-01")},
		Matches:      []Existing{{ID: "m1", TransactionID: "t1", OccurrenceID: "o1", State: matches.StateConfirmed, Origin: matches.OriginUser, ByPerson: true}},
	})
	if got := kinds(plan); got != "confirm:t2>o2" {
		t.Fatalf("assigned transaction: %q", got)
	}
}

// AC: a pending transaction only produces a suggestion; once booked, the
// engine confirms its own suggestion.
func TestPendingTransactionOnlySuggests(t *testing.T) {
	o := occ("o1", "Books", "45.00", "2026-10-29")
	a := only(t, Plan(Snapshot{Transactions: []Transaction{pending("t1", "-45.00", "2026-10-29", "CARD PURCHASE BOOKSHOP")}, Occurrences: []Occurrence{o}}))
	if a.Kind != ActionSuggest || a.Checks.Booked || !strings.Contains(a.Reason, "pending (only a booked transaction can be confirmed)") {
		t.Fatalf("pending: %+v", a)
	}
	own := Existing{ID: "m1", TransactionID: "t1", OccurrenceID: "o1", State: matches.StateSuggested, Origin: matches.OriginAutoRule, Version: 1, BySystem: true}
	// Still pending: nothing to do (already in the review queue).
	if plan := Plan(Snapshot{Transactions: []Transaction{pending("t1", "-45.00", "2026-10-29", "")}, Occurrences: []Occurrence{o}, Matches: []Existing{own}}); len(plan) != 0 {
		t.Fatalf("still pending: %q", kinds(plan))
	}
	a = only(t, Plan(Snapshot{Transactions: []Transaction{booked("t1", "-45.00", "2026-10-31", "")}, Occurrences: []Occurrence{o}, Matches: []Existing{own}}))
	if a.Kind != ActionConfirm || a.MatchID != "m1" || a.Version != 1 {
		t.Fatalf("booked: %+v", a)
	}
}

// AC: the description similarity alone never confirms (or even makes a
// candidate); a perfect score with any failing safeguard stays a suggestion,
// and a zero score does not stop a confirmation.
func TestSimilarityScoreAloneNeverConfirms(t *testing.T) {
	name := "Streaming Service"
	text := "STREAMING SERVICE"
	if Score(name, text) != 100 {
		t.Fatalf("score %d", Score(name, text))
	}
	o := occ("o1", name, "12.99", "2026-10-05")
	// Perfect text, wrong amount: no candidate.
	if plan := Plan(Snapshot{Transactions: []Transaction{booked("t1", "-25.00", "2026-10-05", text)}, Occurrences: []Occurrence{o}}); len(plan) != 0 {
		t.Errorf("perfect text, other amount: %q", kinds(plan))
	}
	// Perfect text, too far from the due date: no candidate.
	if plan := Plan(Snapshot{Transactions: []Transaction{booked("t1", "-12.99", "2026-10-13", text)}, Occurrences: []Occurrence{o}}); len(plan) != 0 {
		t.Errorf("perfect text, 8 days away: %q", kinds(plan))
	}
	// Perfect text, 4 days away (outside D9's 3): suggestion only.
	a := only(t, Plan(Snapshot{Transactions: []Transaction{booked("t1", "-12.99", "2026-10-09", text)}, Occurrences: []Occurrence{o}}))
	if a.Kind != ActionSuggest || a.Checks.Score != 100 || !strings.Contains(a.Reason, "date_outside_window (4 days") {
		t.Errorf("perfect text, 4 days: %+v", a)
	}
	// Perfect text, pending: suggestion only.
	if a := only(t, Plan(Snapshot{Transactions: []Transaction{pending("t1", "-12.99", "2026-10-05", text)}, Occurrences: []Occurrence{o}})); a.Kind != ActionSuggest {
		t.Errorf("perfect text, pending: %+v", a)
	}
	// No shared word at all, every safeguard passes: confirmed.
	if a := only(t, Plan(Snapshot{Transactions: []Transaction{booked("t1", "-12.99", "2026-10-06", "XYZ 0042")}, Occurrences: []Occurrence{o}})); a.Kind != ActionConfirm || a.Checks.Score != 0 {
		t.Errorf("zero score, all safeguards pass: %+v", a)
	}
	for _, code := range (Checks{Score: 100}).Failed() {
		if strings.Contains(code, "score") || strings.Contains(code, "similar") {
			t.Errorf("the score is a safeguard: %v", code)
		}
	}
}

func TestDateWindow(t *testing.T) {
	o := occ("o1", "Rent", "800.00", "2026-10-01")
	for day, want := range map[string]ActionKind{"2026-09-28": ActionConfirm, "2026-10-04": ActionConfirm, "2026-09-27": ActionSuggest, "2026-10-08": ActionSuggest, "2026-10-09": ""} {
		plan := Plan(Snapshot{Transactions: []Transaction{booked("t1", "-800.00", day, "")}, Occurrences: []Occurrence{o}})
		var got ActionKind
		if len(plan) == 1 {
			got = plan[0].Kind
		}
		if got != want {
			t.Errorf("%s: %q, want %q", day, got, want)
		}
	}
}

func TestCurrencyAndDirection(t *testing.T) {
	o := occ("o1", "Rent", "800.00", "2026-10-01")
	gbp := Transaction{ID: "t1", Amount: money.MustParse("-800.00", "GBP"), Status: banking.StatusBooked, Date: date("2026-10-01")}
	incoming := booked("t2", "800.00", "2026-10-01", "")
	cancelled := booked("t3", "-800.00", "2026-10-01", "")
	cancelled.Status = banking.StatusCancelled
	if plan := Plan(Snapshot{Transactions: []Transaction{gbp, incoming, cancelled}, Occurrences: []Occurrence{o}}); len(plan) != 0 {
		t.Errorf("plan %q", kinds(plan))
	}
}

// A user rejection (or unmatch, or reversal) closes the pair for good: the
// engine never suggests it again, so a rejection cannot loop.
func TestPersonDecisionsCloseThePair(t *testing.T) {
	tx := booked("t1", "-800.00", "2026-10-02", "")
	o := occ("o1", "Rent", "800.00", "2026-10-01")
	for name, m := range map[string]Existing{
		"rejected by a user":  {ID: "m1", TransactionID: "t1", OccurrenceID: "o1", State: matches.StateRejected, Origin: matches.OriginAutoRule, BySystem: true, ByPerson: true},
		"unmatched by a user": {ID: "m1", TransactionID: "t1", OccurrenceID: "o1", State: matches.StateRejected, Origin: matches.OriginUser, ByPerson: true},
		"reversed":            {ID: "m1", TransactionID: "t1", OccurrenceID: "o1", State: matches.StateReversed, Origin: matches.OriginAutoRule, BySystem: true, ByPerson: true},
	} {
		if plan := Plan(Snapshot{Transactions: []Transaction{tx}, Occurrences: []Occurrence{o}, Matches: []Existing{m}}); len(plan) != 0 {
			t.Errorf("%s: %q", name, kinds(plan))
		}
	}
	// Dropped by the engine itself: the pair may come back.
	dropped := Existing{ID: "m1", TransactionID: "t1", OccurrenceID: "o1", State: matches.StateRejected, Origin: matches.OriginAutoRule, BySystem: true}
	if got := kinds(Plan(Snapshot{Transactions: []Transaction{tx}, Occurrences: []Occurrence{o}, Matches: []Existing{dropped}})); got != "confirm:t1>o1" {
		t.Errorf("after the engine's own drop: %q", got)
	}
	// A rejected pair is not a candidate, so it does not make another pair
	// ambiguous either.
	other := booked("t2", "-800.00", "2026-10-01", "")
	rejected := Existing{ID: "m1", TransactionID: "t1", OccurrenceID: "o1", State: matches.StateRejected, Origin: matches.OriginAutoRule, BySystem: true, ByPerson: true}
	if got := kinds(Plan(Snapshot{Transactions: []Transaction{tx, other}, Occurrences: []Occurrence{o}, Matches: []Existing{rejected}})); got != "confirm:t2>o1" {
		t.Errorf("rejected pair beside another candidate: %q", got)
	}
}

// Decision D33: matches a person touched, model suggestions and
// confirmations are never acted on; only the engine's own untouched
// suggestions are dropped when they stop being candidates.
func TestAutomationNeverTouchesPersonsMatches(t *testing.T) {
	o := occ("o1", "Rent", "800.00", "2026-10-01")
	tx := booked("t1", "-800.00", "2026-10-02", "")
	corrected := Existing{ID: "m1", TransactionID: "t1", OccurrenceID: "o1", State: matches.StateSuggested, Origin: matches.OriginAutoRule, BySystem: true, ByPerson: true}
	model := Existing{ID: "m2", TransactionID: "t1", OccurrenceID: "o1", State: matches.StateSuggested, Origin: matches.OriginModel, BySystem: true}
	for name, m := range map[string]Existing{"corrected by a person": corrected, "model suggestion": model} {
		// Would pass every safeguard, but the engine leaves it to a person.
		if plan := Plan(Snapshot{Transactions: []Transaction{tx}, Occurrences: []Occurrence{o}, Matches: []Existing{m}}); len(plan) != 0 {
			t.Errorf("%s, still a candidate: %q", name, kinds(plan))
		}
		// No longer a candidate (occurrence paid): not dropped either.
		if plan := Plan(Snapshot{Transactions: []Transaction{tx}, Matches: []Existing{m}}); len(plan) != 0 {
			t.Errorf("%s, stale: %q", name, kinds(plan))
		}
	}
	// The engine's own suggestion that went stale is dropped.
	own := Existing{ID: "m3", TransactionID: "t1", OccurrenceID: "o1", State: matches.StateSuggested, Origin: matches.OriginAutoRule, BySystem: true, Version: 4}
	a := only(t, Plan(Snapshot{Matches: []Existing{own}}))
	if a.Kind != ActionDrop || a.MatchID != "m3" || a.Version != 4 {
		t.Fatalf("stale own suggestion: %+v", a)
	}
	// An open suggestion of the transaction with another occurrence that is
	// not dropped (a person's) blocks the confirmation.
	elsewhere := Existing{ID: "m4", TransactionID: "t1", OccurrenceID: "o9", State: matches.StateSuggested, Origin: matches.OriginAutoRule, BySystem: true, ByPerson: true}
	a = only(t, Plan(Snapshot{Transactions: []Transaction{tx}, Occurrences: []Occurrence{o}, Matches: []Existing{elsewhere}}))
	if a.Kind != ActionSuggest || a.Checks.OtherOpen != 1 || !strings.Contains(a.Reason, "transaction_has_open_match") {
		t.Fatalf("other open match: %+v", a)
	}
}

// A pending row superseded by its booked successor leaves the snapshot: the
// engine drops its suggestion on the pending row and confirms the booked one,
// in that order.
func TestSupersededPendingIsReplaced(t *testing.T) {
	own := Existing{ID: "m1", TransactionID: "pending", OccurrenceID: "o1", State: matches.StateSuggested, Origin: matches.OriginAutoRule, BySystem: true, Version: 1}
	plan := Plan(Snapshot{Transactions: []Transaction{booked("booked", "-45.00", "2026-10-31", "")}, Occurrences: []Occurrence{occ("o1", "Books", "45.00", "2026-10-29")}, Matches: []Existing{own}})
	if got := kinds(plan); got != "drop:pending>o1#m1 confirm:booked>o1" {
		t.Fatalf("plan %q", got)
	}
}

// The plan is a function of the data, not of its order.
func TestPlanIsDeterministic(t *testing.T) {
	s := Snapshot{
		Transactions: []Transaction{
			booked("t1", "-3.50", "2026-10-07", "COFFEE"), booked("t2", "-3.50", "2026-10-07", "COFFEE"),
			booked("t3", "-800.00", "2026-10-02", "RENT"), pending("t4", "-45.00", "2026-10-29", "BOOKS"),
			booked("t5", "-12.49", "2026-10-05", "STREAMING"),
		},
		Occurrences: []Occurrence{
			occ("o1", "Coffee", "3.50", "2026-10-07"), occ("o2", "Rent", "800.00", "2026-10-01"),
			occ("o3", "Books", "45.00", "2026-10-29"), occ("o4", "Streaming", "12.99", "2026-10-05"),
		},
		Matches: []Existing{{ID: "m9", TransactionID: "gone", OccurrenceID: "o9", State: matches.StateSuggested, Origin: matches.OriginAutoRule, BySystem: true}},
	}
	want := Plan(s)
	if got := kinds(want); got != "drop:gone>o9#m9 confirm:t3>o2 suggest:t5>o4 suggest:t1>o1 suggest:t2>o1 suggest:t4>o3" {
		t.Fatalf("plan %q", got)
	}
	r := rand.New(rand.NewPCG(1, 2))
	for range 20 {
		c := Snapshot{Transactions: append([]Transaction(nil), s.Transactions...), Occurrences: append([]Occurrence(nil), s.Occurrences...), Matches: s.Matches}
		r.Shuffle(len(c.Transactions), func(i, j int) { c.Transactions[i], c.Transactions[j] = c.Transactions[j], c.Transactions[i] })
		r.Shuffle(len(c.Occurrences), func(i, j int) { c.Occurrences[i], c.Occurrences[j] = c.Occurrences[j], c.Occurrences[i] })
		if got := Plan(c); !reflect.DeepEqual(got, want) {
			t.Fatalf("shuffled input gave %q", kinds(got))
		}
	}
}

func TestScore(t *testing.T) {
	for _, c := range []struct {
		name, text string
		want       int
	}{
		{"Rent", "RENT 2026-10 Example Landlord", 100},
		{"Home internet", "INTERNET PROVIDER", 50},
		{"VPS", "vps.example hosting", 100},
		{"TV", "TV LICENCE", 0}, // words shorter than three are ignored
		{"Gym", "", 0},
		{"Šildymas", "ŠILDYMAS UAB", 100},
	} {
		if got := Score(c.name, c.text); got != c.want {
			t.Errorf("Score(%q, %q) = %d, want %d", c.name, c.text, got, c.want)
		}
	}
}

// fakeStore records the engine's writes and can refuse them.
type fakeStore struct {
	snap    Snapshot
	calls   []string
	refuse  map[string]error // by "kind:tx>occ"
	through calendar.YearMonth
}

func (f *fakeStore) Snapshot(_ context.Context, through calendar.YearMonth) (Snapshot, error) {
	f.through = through
	return f.snap, nil
}

func (f *fakeStore) record(kind string, actor audit.Actor, tx banking.TransactionID, occ string) error {
	if actor != audit.System {
		return errors.New("not the system")
	}
	key := kind + ":" + string(tx) + ">" + occ
	f.calls = append(f.calls, key)
	return f.refuse[key]
}

func (f *fakeStore) Create(_ context.Context, actor audit.Actor, n matches.New) (matches.Change, error) {
	if n.Origin != matches.OriginAutoRule {
		return matches.Change{}, errors.New("origin")
	}
	return matches.Change{}, f.record("create-"+string(n.State), actor, n.TransactionID, n.OccurrenceID)
}

func (f *fakeStore) ConfirmAuto(_ context.Context, id string, _ int64, _ money.Money, _ *string) (matches.Change, error) {
	return matches.Change{}, f.record("confirm", audit.System, "", id)
}

func (f *fakeStore) Reject(_ context.Context, actor audit.Actor, id string, _ int64, _ *string) (matches.Change, error) {
	return matches.Change{}, f.record("reject", actor, "", id)
}

type recordFunc func(context.Context, audit.Event) error

func (r recordFunc) Record(ctx context.Context, e audit.Event) error { return r(ctx, e) }

func TestEngineRunAppliesPlanAndFallsBack(t *testing.T) {
	st := &fakeStore{snap: Snapshot{
		Transactions: []Transaction{booked("t1", "-800.00", "2026-10-02", ""), booked("t2", "-12.49", "2026-10-05", "")},
		Occurrences:  []Occurrence{occ("o1", "Rent", "800.00", "2026-10-01"), occ("o2", "Streaming", "12.99", "2026-10-05")},
		Matches:      []Existing{{ID: "m9", TransactionID: "gone", OccurrenceID: "o9", State: matches.StateSuggested, Origin: matches.OriginAutoRule, BySystem: true}},
	}, refuse: map[string]error{"create-confirmed:t1>o1": matches.ErrOverpaid}}
	var events []audit.Event
	e := New(st, Options{Today: func() (calendar.Date, error) { return date("2026-10-28"), nil },
		Recorder: recordFunc(func(_ context.Context, ev audit.Event) error { events = append(events, ev); return nil })})
	actor := audit.Actor{Kind: audit.ActorUser, UserID: "01JD3Y4C8V9M2Q7W5R6T0XZBNA"}
	res, err := e.Request(context.Background(), actor)
	if err != nil {
		t.Fatal(err)
	}
	// The store refused the confirmation: the engine leaves a suggestion.
	if got := strings.Join(st.calls, " "); got != "reject:>m9 create-confirmed:t1>o1 create-suggested:t1>o1 create-suggested:t2>o2" {
		t.Errorf("calls %q", got)
	}
	if res != (Result{Candidates: 2, Confirmed: 0, Suggested: 2, Dropped: 1}) {
		t.Errorf("result %+v", res)
	}
	// Occurrences are materialised far enough ahead for early payments.
	if st.through.String() != "2026-11" {
		t.Errorf("through %s", st.through)
	}
	if len(events) != 1 || events[0].Action != audit.PaymentMatchingRun || events[0].Actor != actor || events[0].Details["suggested"] != "2" {
		t.Fatalf("audit %+v", events)
	}
	if err := audit.Validate(events[0]); err != nil {
		t.Error(err)
	}
	// Run (the after-import trigger) records no run event.
	if _, err := e.Run(context.Background()); err != nil || len(events) != 1 {
		t.Errorf("run: %v %d", err, len(events))
	}
}

func TestEngineStopsOnStoreFailure(t *testing.T) {
	boom := errors.New("disk I/O error")
	st := &fakeStore{snap: Snapshot{
		Transactions: []Transaction{booked("t1", "-800.00", "2026-10-02", "")},
		Occurrences:  []Occurrence{occ("o1", "Rent", "800.00", "2026-10-01")},
	}, refuse: map[string]error{"create-confirmed:t1>o1": boom}}
	e := New(st, Options{Today: func() (calendar.Date, error) { return date("2026-10-28"), nil }})
	if _, err := e.Run(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
}
