package matches_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking/bankingtest"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/matches"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/payments"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
)

// record records a manual payment of the occurrence paid in October.
func (f *fixture) record(o occurrences.Occurrence, amount string) payments.ManualPayment {
	f.t.Helper()
	p, _, err := f.ps.RecordManual(f.ctx, alice, o.ID, payments.NewManual{Amount: money.MustParse(amount, o.Amount.Currency().Code()), PaidOn: d("2026-10-05")}, d("2026-11-01"))
	if err != nil {
		f.t.Fatal(err)
	}
	return p
}

// manual lists the occurrence's manual payments: ID -> the match replacing it.
func (f *fixture) manual(o occurrences.Occurrence) map[string]string {
	f.t.Helper()
	list, err := f.ps.Manual(f.ctx, o.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	out := map[string]string{}
	for _, p := range list {
		out[p.ID] = p.ReplacedByMatchID
	}
	return out
}

func (f *fixture) replacing(a audit.Actor, m matches.Match, allow bool, ids ...string) (matches.Change, error) {
	return f.ms.ConfirmReplacing(f.ctx, a, m.ID, m.Version, matches.Replacement{ManualPaymentIDs: ids, AllowOverpayment: allow, Reason: reason("the payment I recorded")})
}

// auditOf returns the recorded audit events of the action, oldest first.
func (f *fixture) auditOf(a audit.Action) []audit.Event {
	f.rec.mu.Lock()
	defer f.rec.mu.Unlock()
	var out []audit.Event
	for _, e := range f.rec.events {
		if e.Action == a {
			out = append(out, e)
		}
	}
	return out
}

// TestConfirmReplacingAndUnconfirmRestores (D45, ADR-0049): the occurrence is
// paid by the person's manual record; the bank's payment is suggested for it.
// A plain confirmation would count the payment twice. Confirming it "as the
// payment I recorded" replaces the record: the occurrence is paid by the bank
// match alone, within the tolerance (D41 2a), and the record is kept, listed
// as replaced. Undo (unconfirm) restores the record and the occurrence's state
// exactly; history and audit keep every step with its provenance.
func TestConfirmReplacingAndUnconfirmRestores(t *testing.T) {
	f := newFixture(t)
	s, o := f.suggestion()
	p := f.record(o, "13.49")
	before := f.state(o)
	if before.Status != payments.StatusPaid || before.EvidenceCount != 1 {
		t.Fatalf("paid by the manual record: %+v", before)
	}
	if _, err := f.ms.Confirm(f.ctx, alice, s.ID, s.Version, false, nil); !errors.Is(err, matches.ErrOverpaid) {
		t.Fatalf("a plain confirmation counts the payment twice: %v", err)
	}

	ch, err := f.replacing(aliceSession, s, false, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	c := ch.Match
	if c.State != matches.StateConfirmed || c.DecidedBy == nil || c.DecidedBy.Via != audit.ViaSession || !c.CountsAsEvidence() {
		t.Fatalf("confirmed: %+v", c)
	}
	st := f.state(o)
	if st.Status != payments.StatusPaid || st.EvidenceCount != 1 || st.Paid.String() != "12.99 EUR" || st.Shortfall.String() != "0.50 EUR" {
		t.Fatalf("paid by the bank match within the tolerance: %+v", st)
	}
	if amt, _ := f.tolerance(c.ID); amt == nil || *amt != "0.50" {
		t.Errorf("tolerance snapshot (bank evidence alone): %v", amt)
	}
	for _, e := range ch.Result.Facts.Evidence {
		if e.Kind != payments.EvidenceBankTransaction {
			t.Errorf("replaced record still counts: %+v", e)
		}
	}
	if got := f.manual(o); got[p.ID] != c.ID {
		t.Errorf("manual list: %v, want %s replaced by %s", got, p.ID, c.ID)
	}
	last := c.History[len(c.History)-1]
	if last.Action != matches.ActionConfirmed || strings.Join(last.Replaced, ",") != p.ID || len(last.Restored) != 0 {
		t.Errorf("history entry of the confirmation: %+v", last)
	}
	rep := f.auditOf(audit.ManualPaymentReplaced)
	if len(rep) != 1 || rep[0].Details["payment_id"] != p.ID || rep[0].Details["match_id"] != c.ID || rep[0].Details["amount"] != "13.49" ||
		rep[0].Details["via"] != "session" || rep[0].TargetID != o.ID {
		t.Errorf("manual_payment.replaced: %+v", rep)
	}
	if conf := f.auditOf(audit.PaymentMatchConfirmed); len(conf) != 1 || conf[0].Details["via"] != "session" {
		t.Errorf("payment_match.confirmed: %+v", conf)
	}

	// Undo, by the same person through MCP.
	ch, err = f.ms.Unconfirm(f.ctx, aliceMCP, c.ID, c.Version, reason("not this one"))
	if err != nil {
		t.Fatal(err)
	}
	if after := f.state(o); !sameState(before, after) {
		t.Errorf("state not restored: before %+v after %+v", before, after)
	}
	if got := f.manual(o); got[p.ID] != "" {
		t.Errorf("record still replaced after undo: %v", got)
	}
	if amt, _ := f.tolerance(c.ID); amt != nil {
		t.Errorf("tolerance snapshot kept: %v", *amt)
	}
	u := ch.Match
	if l := u.History[len(u.History)-1]; l.Action != matches.ActionUnconfirmed || strings.Join(l.Restored, ",") != p.ID || l.By.Via != audit.ViaMCP {
		t.Errorf("history entry of the undo: %+v", l)
	}
	if h := u.History[len(u.History)-2]; strings.Join(h.Replaced, ",") != p.ID {
		t.Errorf("the confirmation keeps what it replaced: %+v", h)
	}
	res := f.auditOf(audit.ManualPaymentRestored)
	if len(res) != 1 || res[0].Details["payment_id"] != p.ID || res[0].Details["by"] != "unconfirmed" || res[0].Details["via"] != "mcp" || res[0].Actor.TokenID != aliceMCP.TokenID {
		t.Errorf("manual_payment.restored: %+v", res)
	}

	// It is a suggestion again: replacing again works, and the history keeps both.
	ch, err = f.replacing(aliceAPI, u, false, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := f.scanRow("SELECT count(*) FROM manual_payment_replacements WHERE manual_payment_id = ?", []any{p.ID}, &rows); err != nil || rows != 2 {
		t.Errorf("replacement history rows: %d %v", rows, err)
	}
	if got := f.manual(o); got[p.ID] != c.ID {
		t.Errorf("replaced again: %v", got)
	}
}

// TestConfirmReplacingChecksTheRemainingEvidence: the overpayment check and
// the tolerance use the evidence that remains after the replacement.
func TestConfirmReplacingChecksTheRemainingEvidence(t *testing.T) {
	f := newFixture(t)
	s, o := f.suggestion()
	small := f.record(o, "0.50")
	big := f.record(o, "12.99")

	// Replacing only the small record leaves 12.99 manual + 12.99 bank.
	if _, err := f.replacing(alice, s, false, small.ID); !errors.Is(err, matches.ErrOverpaid) {
		t.Fatalf("overpaid with the remaining record: %v", err)
	}
	if got := f.manual(o); got[small.ID] != "" || got[big.ID] != "" {
		t.Fatalf("a refused replacement left marks: %v", got)
	}
	if m, err := f.ms.Get(f.ctx, s.ID); err != nil || m.State != matches.StateSuggested || m.Version != s.Version {
		t.Fatalf("a refused replacement changed the match: %+v %v", m, err)
	}

	// Replacing the big one leaves 0.50 manual + 12.99 bank = 13.49: paid,
	// mixed evidence, so no tolerance snapshot (D41 2a).
	ch, err := f.replacing(alice, s, false, big.ID)
	if err != nil {
		t.Fatal(err)
	}
	st := f.state(o)
	if st.Status != payments.StatusPaid || st.Paid.String() != "13.49 EUR" || st.EvidenceCount != 2 {
		t.Errorf("paid by the remaining record and the bank: %+v", st)
	}
	if amt, _ := f.tolerance(ch.Match.ID); amt != nil {
		t.Errorf("tolerance snapshot with manual evidence left: %v", *amt)
	}

	// With overpayment allowed, the person may say both records were it and
	// another payment too.
	g := newFixture(t)
	s2, o2 := g.suggestion()
	a := g.record(o2, "13.49")
	b := g.record(o2, "13.49")
	if _, err := g.replacing(alice, s2, false, a.ID); !errors.Is(err, matches.ErrOverpaid) {
		t.Fatalf("overpaid: %v", err)
	}
	if _, err := g.replacing(alice, s2, true, a.ID); err != nil {
		t.Fatalf("allowed overpayment: %v", err)
	}
	if st := g.state(o2); st.Paid.String() != "26.48 EUR" {
		t.Errorf("paid with b and the bank: %+v", st)
	}
	_ = b
}

// TestConfirmReplacingRefusals: records of another occurrence, in another
// currency, deleted, already replaced or unknown; automation; invalid lists;
// stale versions and confirmed matches.
func TestConfirmReplacingRefusals(t *testing.T) {
	f := newFixture(t)
	s, o := f.suggestion()
	p := f.record(o, "13.49")
	november := f.occurrence(o.CommitmentID, "2026-11")
	other, _, err := f.ps.RecordManual(f.ctx, alice, november.ID, payments.NewManual{Amount: money.MustParse("13.49", "EUR"), PaidOn: d("2026-10-30")}, d("2026-11-01"))
	if err != nil {
		t.Fatal(err)
	}
	gone := f.record(o, "1.00")
	if _, err := f.ps.DeleteManual(f.ctx, alice, o.ID, gone.ID, gone.Version); err != nil {
		t.Fatal(err)
	}
	// A record in another currency cannot be entered through the API; the
	// store refuses it all the same.
	f.mustExec("INSERT INTO manual_payments (id, occurrence_id, amount, currency, paid_on, created_at, updated_at) VALUES ('01JZ0000000000000000000999', ?, '13.49', 'GBP', '2026-10-05', '2026-10-05T00:00:00.000000000Z', '2026-10-05T00:00:00.000000000Z')", o.ID)

	cases := map[string]struct {
		actor audit.Actor
		ids   []string
		want  error
	}{
		"another occurrence's record": {alice, []string{other.ID}, matches.ErrReplaceOtherOccurrence},
		"another currency":            {alice, []string{"01JZ0000000000000000000999"}, matches.ErrReplaceCurrency},
		"a deleted record":            {alice, []string{gone.ID}, matches.ErrReplaceUnavailable},
		"an unknown record":           {alice, []string{resource.NewID()}, matches.ErrReplaceNotFound},
		"one good, one bad":           {alice, []string{p.ID, other.ID}, matches.ErrReplaceOtherOccurrence},
		"automation":                  {audit.System, []string{p.ID}, matches.ErrActorReplace},
	}
	for name, c := range cases {
		if _, err := f.replacing(c.actor, s, false, c.ids...); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	tooMany := make([]string, matches.MaxReplaced+1)
	for i := range tooMany {
		tooMany[i] = resource.NewID()
	}
	for name, ids := range map[string][]string{"none": nil, "twice": {p.ID, p.ID}, "too many": tooMany} {
		var ve *commitments.ValidationError
		if _, err := f.replacing(alice, s, false, ids...); !errors.As(err, &ve) || ve.Errors[0].Field != "manual_payment_ids" {
			t.Errorf("%s: %v", name, err)
		}
	}
	if got := f.manual(o); got[p.ID] != "" {
		t.Fatalf("refusals left a mark: %v", got)
	}
	if _, err := f.ms.ConfirmReplacing(f.ctx, alice, s.ID, s.Version+1, matches.Replacement{ManualPaymentIDs: []string{p.ID}}); !errors.As(err, new(*resource.VersionConflictError)) {
		t.Errorf("stale version: %v", err)
	}
	ch, err := f.replacing(alice, s, false, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.replacing(alice, ch.Match, false, p.ID); !errors.Is(err, matches.ErrTransition) {
		t.Errorf("a confirmed match: %v", err)
	}

	// A second suggestion for the occurrence cannot replace the record the
	// first one replaces.
	second, err := f.ms.Create(f.ctx, audit.System, matches.New{TransactionID: f.tx("CARD PURCHASE LONDON").ID, OccurrenceID: o.ID, Amount: money.MustParse("12.99", "EUR"),
		Origin: matches.OriginAutoRule, State: matches.StateSuggested})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.replacing(alice, second.Match, true, p.ID); !errors.Is(err, matches.ErrReplaceUnavailable) {
		t.Errorf("already replaced: %v", err)
	}
}

// TestDatabaseGuardsReplacements: the triggers of migration 0024 hold even
// for writes that bypass the store.
func TestDatabaseGuardsReplacements(t *testing.T) {
	f := newFixture(t)
	s, o := f.suggestion()
	p := f.record(o, "13.49")
	november := f.occurrence(o.CommitmentID, "2026-11")
	other, _, err := f.ps.RecordManual(f.ctx, alice, november.ID, payments.NewManual{Amount: money.MustParse("1.00", "EUR"), PaidOn: d("2026-10-30")}, d("2026-11-01"))
	if err != nil {
		t.Fatal(err)
	}
	ch, err := f.replacing(alice, s, false, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	eventID := ch.Match.History[len(ch.Match.History)-1].ID
	for _, q := range []string{
		"INSERT INTO manual_payment_replacements (id, manual_payment_id, match_id, replaced_event_id, replaced_at) VALUES ('01JZ0000000000000000000998', '" + other.ID + "', '" + s.ID + "', '" + eventID + "', '2026-11-01T09:00:00.000000000Z')",
		"INSERT INTO manual_payment_replacements (id, manual_payment_id, match_id, replaced_event_id, replaced_at) VALUES ('01JZ0000000000000000000997', '" + p.ID + "', '" + s.ID + "', '" + eventID + "', '2026-11-01T09:00:00.000000000Z')",
		"DELETE FROM manual_payment_replacements",
		"UPDATE manual_payment_replacements SET match_id = '" + s.ID + "', restored_at = '2026-11-01T09:00:00.000000000Z', restored_event_id = '" + eventID + "', replaced_at = '2026-11-02T09:00:00.000000000Z'",
	} {
		if err := f.exec(q); err == nil {
			t.Errorf("%s succeeded, want it refused", q)
		}
	}
	f.mustExec("UPDATE manual_payment_replacements SET restored_at = '2026-11-01T09:00:00.000000000Z', restored_event_id = ?", eventID)
	if err := f.exec("UPDATE manual_payment_replacements SET restored_at = '2026-11-02T09:00:00.000000000Z'"); err == nil {
		t.Error("a restoration changed twice")
	}
}

// TestUnmatchRestoresAndReverseKeepsReplacements (ADR-0049 points 4 and 5):
// unmatching says the transaction did not pay the occurrence, so the records
// it replaced count again; reversing says that payment was reversed, so they
// stay replaced.
func TestUnmatchRestoresAndReverseKeepsReplacements(t *testing.T) {
	f := newFixture(t)
	s, o := f.suggestion()
	p := f.record(o, "13.49")
	before := f.state(o)
	ch, err := f.replacing(alice, s, false, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Something else changed the occurrence: the undo window closed, so
	// unmatch is the way back.
	f.record(o, "0.01")
	f.mustExec("UPDATE manual_payments SET deleted_at = '2026-11-01T09:00:00.000000000Z', deleted_by = 'system' WHERE amount = '0.01'")
	if _, err := f.ms.Unconfirm(f.ctx, alice, ch.Match.ID, ch.Match.Version, nil); !errors.Is(err, matches.ErrUndoWindow) {
		t.Fatalf("unconfirm after another change: %v", err)
	}
	u, err := f.ms.Unmatch(f.ctx, alice, ch.Match.ID, ch.Match.Version, reason("not the payment"))
	if err != nil {
		t.Fatal(err)
	}
	if after := f.state(o); !sameState(before, after) {
		t.Errorf("unmatch did not restore the record: before %+v after %+v", before, after)
	}
	if l := u.Match.History[len(u.Match.History)-1]; l.Action != matches.ActionUnmatched || strings.Join(l.Restored, ",") != p.ID {
		t.Errorf("history of the unmatch: %+v", l)
	}
	if res := f.auditOf(audit.ManualPaymentRestored); len(res) != 1 || res[0].Details["by"] != "unmatched" {
		t.Errorf("manual_payment.restored: %+v", res)
	}

	g := newFixture(t)
	s2, o2 := g.suggestion()
	p2 := g.record(o2, "13.49")
	ch2, err := g.replacing(alice, s2, false, p2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.ms.Reverse(g.ctx, alice, ch2.Match.ID, ch2.Match.Version, reason("bounced")); err != nil {
		t.Fatal(err)
	}
	if got := g.manual(o2); got[p2.ID] != ch2.Match.ID {
		t.Errorf("reverse restored the record: %v", got)
	}
	if st := g.state(o2); st.Status == payments.StatusPaid || st.EvidenceCount != 0 {
		t.Errorf("after the reversal the occurrence is open: %+v", st)
	}
	if res := g.auditOf(audit.ManualPaymentRestored); len(res) != 0 {
		t.Errorf("reverse restored: %+v", res)
	}
}

// TestConcurrentConfirmReplacing: replacements of the same match version race
// with each other and with a plain confirmation; exactly one wins, the rest
// get a version conflict, and at most one replacement is active.
func TestConcurrentConfirmReplacing(t *testing.T) {
	f := newFixture(t)
	s, o := f.suggestion()
	p := f.record(o, "13.49")
	const n = 6
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i == 0 {
				_, errs[i] = f.ms.Confirm(f.ctx, alice, s.ID, s.Version, true, nil)
				return
			}
			_, errs[i] = f.replacing(alice, s, false, p.ID)
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.As(err, new(*resource.VersionConflictError)):
			t.Errorf("unexpected: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d winners: %v", ok, errs)
	}
	var active int
	if err := f.scanRow("SELECT count(*) FROM manual_payment_replacements WHERE restored_at IS NULL", nil, &active); err != nil {
		t.Fatal(err)
	}
	st := f.state(o)
	switch {
	case errs[0] == nil && (active != 0 || st.Paid.String() != "26.48 EUR"):
		t.Errorf("the plain confirmation won: %d active replacements, %+v", active, st)
	case errs[0] != nil && (active != 1 || st.Paid.String() != "12.99 EUR"):
		t.Errorf("a replacement won: %d active replacements, %+v", active, st)
	}
}

// manual prefixes a manual payment's entry in evidenceKinds.
const manual = string(payments.EvidenceManual) + ":"

// evidenceKinds lists the occurrence's counted evidence as kind:id.
func (f *fixture) evidenceKinds(o occurrences.Occurrence) []string {
	f.t.Helper()
	facts, err := f.ps.Facts(f.ctx, []string{o.ID})
	if err != nil {
		f.t.Fatal(err)
	}
	var out []string
	for _, e := range facts[o.ID].Evidence {
		out = append(out, string(e.Kind)+":"+e.ID)
	}
	return out
}

// TestReplacementOnHoldWhileTheMatchDoesNotCount (X5c, ADR-0049 amendment):
// when the provider stops reporting the transaction of the match that
// replaced a manual record, the match stops counting (ADR-0035) and the
// record counts again, so the occurrence stays paid by what the person
// recorded instead of showing unpaid. The replacement itself stays (the
// record is still listed as replaced, the match needs review); when the
// transaction is reported again the match counts and the record does not.
// Exactly one of them counts at any time, also in the overpayment check.
func TestReplacementOnHoldWhileTheMatchDoesNotCount(t *testing.T) {
	f := newFixture(t)
	s, o := f.suggestion()
	p := f.record(o, "13.49")
	before := f.state(o)
	ch, err := f.replacing(alice, s, false, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	c := ch.Match
	bank := string(payments.EvidenceBankTransaction) + ":" + c.ID
	if got := strings.Join(f.evidenceKinds(o), ","); got != bank {
		t.Fatalf("evidence after the replacement: %s", got)
	}

	streaming := f.tx("STREAMING SERVICE")
	f.markGone(streaming)
	if st := f.state(o); !sameState(before, st) {
		t.Errorf("while the transaction is gone the record pays the occurrence: before %+v now %+v", before, st)
	}
	if got := strings.Join(f.evidenceKinds(o), ","); got != manual+p.ID {
		t.Errorf("evidence while the transaction is gone: %s", got)
	}
	if got := f.manual(o); got[p.ID] != c.ID {
		t.Errorf("the replacement stays recorded: %v", got)
	}
	if m, err := f.ms.Get(f.ctx, c.ID); err != nil || m.CountsAsEvidence() || !m.NeedsReview() {
		t.Errorf("the match is flagged for review: %+v %v", m, err)
	}

	// The overpayment check counts the uncountable match (ADR-0035) and so
	// leaves its replaced record out: 12.99 + 0.50 fits 13.49 without
	// allowing an overpayment (counting the record too would refuse it).
	extra, err := f.match(f.tx("RENT 2026-10"), o, "0.50", matches.OriginUser, matches.StateConfirmed)
	if err != nil {
		t.Fatalf("a second payment that fits beside the replacing match: %v", err)
	}

	f.markReported(streaming)
	st := f.state(o)
	if st.Status != payments.StatusPaid || st.Paid.String() != "13.49 EUR" || st.Overpaid.Sign() != 0 || st.EvidenceCount != 2 {
		t.Errorf("reported again: the bank matches pay it, the record does not count: %+v", st)
	}
	if got := strings.Join(f.evidenceKinds(o), ","); strings.Contains(got, manual) || !strings.Contains(got, bank) || !strings.Contains(got, extra.Match.ID) {
		t.Errorf("evidence once reported again: %s", got)
	}
}

// TestUnmatchRestoreCanOverpayVisibly (X5c item 2, ADR-0049 amendment):
// restoring on unmatch runs no overpayment check. A record added after the
// replacement can take the occurrence past its amount once the replaced
// record counts again; that is the person's own records (manual payments are
// never checked for overpayment, ADR-0031), each counted once, and it shows
// as overpaid. Refusing would keep a match the person says is wrong.
func TestUnmatchRestoreCanOverpayVisibly(t *testing.T) {
	f := newFixture(t)
	s, o := f.suggestion()
	p := f.record(o, "13.49")
	ch, err := f.replacing(alice, s, false, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	q := f.record(o, "0.50")
	if st := f.state(o); st.Paid.String() != "13.49 EUR" || st.Overpaid.Sign() != 0 {
		t.Fatalf("bank match and the new record: %+v", st)
	}
	if _, err := f.ms.Unmatch(f.ctx, alice, ch.Match.ID, ch.Match.Version, reason("not the payment")); err != nil {
		t.Fatal(err)
	}
	st := f.state(o)
	if st.Status != payments.StatusPaid || st.Paid.String() != "13.99 EUR" || st.Overpaid.String() != "0.50 EUR" || st.EvidenceCount != 2 {
		t.Errorf("both records count once and the overpayment shows: %+v", st)
	}
	if got := strings.Join(f.evidenceKinds(o), ","); got != manual+p.ID+","+manual+q.ID && got != manual+q.ID+","+manual+p.ID {
		t.Errorf("evidence after the unmatch: %s", got)
	}
}

// TestReplacementChangesTheRecordsVersion (X5c item 3): whether a match
// replaces a manual payment is part of what a read returns, so replacing and
// restoring it increase its version (its ETag); a delete based on the old
// version is refused.
func TestReplacementChangesTheRecordsVersion(t *testing.T) {
	f := newFixture(t)
	s, o := f.suggestion()
	p := f.record(o, "13.49")
	version := func() int64 {
		t.Helper()
		got, err := f.ps.GetManual(f.ctx, o.ID, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got.Version
	}
	if p.Version != 1 {
		t.Fatalf("new record version %d", p.Version)
	}
	ch, err := f.replacing(aliceSession, s, false, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v := version(); v != 2 {
		t.Errorf("version after the replacement: %d", v)
	}
	if _, err := f.ps.DeleteManual(f.ctx, alice, o.ID, p.ID, 1); !errors.As(err, new(*resource.VersionConflictError)) {
		t.Errorf("delete with the version from before the replacement: %v", err)
	}
	if _, err := f.ms.Unconfirm(f.ctx, aliceSession, ch.Match.ID, ch.Match.Version, nil); err != nil {
		t.Fatal(err)
	}
	if v := version(); v != 3 {
		t.Errorf("version after the restoration: %d", v)
	}
	// A failed replacement changes nothing.
	s2, err := f.ms.Get(f.ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.replacing(alice, s2, false, p.ID, "01JD3Y4C8V9M2Q7W5R6T0XZBNZ"); err == nil {
		t.Fatal("replacement with an unknown record succeeded")
	}
	if v := version(); v != 3 {
		t.Errorf("version after a refused replacement: %d", v)
	}
}

// TestRestoreSkipsADeletedRecord (X5c item 4): a record deleted while
// replaced stays deleted and out of the evidence on unmatch; its replacement
// is closed, but no manual_payment.restored entry is written for it and its
// version is left alone.
func TestRestoreSkipsADeletedRecord(t *testing.T) {
	f := newFixture(t)
	s, o := f.suggestion()
	p := f.record(o, "13.49")
	ch, err := f.replacing(alice, s, false, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ps.DeleteManual(f.ctx, alice, o.ID, p.ID, 2); err != nil {
		t.Fatal(err)
	}
	var deletedVersion int64
	if err := f.scanRow("SELECT version FROM manual_payments WHERE id = ?", []any{p.ID}, &deletedVersion); err != nil {
		t.Fatal(err)
	}
	m, err := f.ms.Get(f.ctx, ch.Match.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ms.Unmatch(f.ctx, alice, m.ID, m.Version, reason("not the payment")); err != nil {
		t.Fatal(err)
	}
	if res := f.auditOf(audit.ManualPaymentRestored); len(res) != 0 {
		t.Errorf("restored entry for a deleted record: %+v", res)
	}
	var version int64
	var active int
	if err := f.scanRow("SELECT version, (SELECT count(*) FROM manual_payment_replacements WHERE restored_at IS NULL) FROM manual_payments WHERE id = ?", []any{p.ID}, &version, &active); err != nil {
		t.Fatal(err)
	}
	if version != deletedVersion || active != 0 {
		t.Errorf("deleted record: version %d (was %d), %d active replacements", version, deletedVersion, active)
	}
	if st := f.state(o); st.Status == payments.StatusPaid || st.EvidenceCount != 0 {
		t.Errorf("the deleted record does not count: %+v", st)
	}
}

// reReport makes the bank report the payment of old again under a new
// transaction identifier: old stops being reported and the same payment is
// reported as a new transaction. It returns the new transaction.
func (f *fixture) reReport(old banking.StoredTransaction) banking.StoredTransaction {
	f.t.Helper()
	if err := f.db.WriteTx(f.ctx, func(ctx context.Context, sqlTx *sql.Tx) error {
		// Let the next refresh reach early October again (ADR-0020 resumes a week before the cursor).
		_, err := sqlTx.ExecContext(ctx, "UPDATE bank_account_sync SET transactions_through = '2026-09-30'")
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
	f.p.Mutate(func(ds *bankingtest.Dataset) {
		ds.Accounts[0].Transactions = append(ds.Accounts[0].Transactions, banking.Transaction{
			Status: banking.StatusBooked, ProviderStatus: "BOOK", Amount: money.MustParse("-12.99", "EUR"),
			BookingDate: d("2026-10-06"), ValueDate: d("2026-10-06"), Description: "STREAMING SERVICE RE-SENT",
		})
	})
	f.refresh()
	f.markGone(old)
	return f.tx("STREAMING SERVICE RE-SENT")
}

// reReported sets up the re-reported payment: the old match replaces record p
// and is held once the bank stops reporting its transaction; the same payment
// is then suggested again as a new transaction, next.
func reReported(t *testing.T) (f *fixture, old, next matches.Match, o occurrences.Occurrence, p payments.ManualPayment) {
	t.Helper()
	f = newFixture(t)
	s, occ := f.suggestion()
	p = f.record(occ, "13.49")
	ch, err := f.replacing(alice, s, false, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	nx, err := f.match(f.reReport(f.tx("STREAMING SERVICE")), occ, "12.99", matches.OriginAutoRule, matches.StateSuggested)
	if err != nil {
		t.Fatal(err)
	}
	return f, ch.Match, nx.Match, occ, p
}

// TestReReportedPaymentReplacedAfterUnmatch (X5e, ADR-0049 amendment A): the
// bank re-reports a replaced payment under a new transaction identifier. The
// old match is held, so its record counts again and the new transaction cannot
// take the record. The documented path: unmatch the old match, which restores
// the record, then confirm the new transaction as replacing it.
func TestReReportedPaymentReplacedAfterUnmatch(t *testing.T) {
	f, old, next, o, p := reReported(t)
	if _, err := f.replacing(alice, next, false, p.ID); !errors.Is(err, matches.ErrReplaceUnavailable) {
		t.Fatalf("the held match still replaces the record: %v", err)
	}
	if st := f.state(o); st.Status != payments.StatusPaid || st.Paid.String() != "13.49 EUR" || st.Overpaid.Sign() != 0 {
		t.Fatalf("held: the record pays the occurrence: %+v", st)
	}

	if _, err := f.ms.Unmatch(f.ctx, alice, old.ID, old.Version, reason("the bank re-reported it")); err != nil {
		t.Fatal(err)
	}
	if got := f.manual(o); got[p.ID] != "" {
		t.Errorf("unmatch did not restore the record: %v", got)
	}
	if st := f.state(o); st.Paid.String() != "13.49 EUR" || st.Overpaid.Sign() != 0 || st.EvidenceCount != 1 {
		t.Errorf("restored record pays the occurrence once: %+v", st)
	}
	if m, err := f.ms.Get(f.ctx, old.ID); err != nil || m.CountsAsEvidence() || m.State == matches.StateConfirmed {
		t.Errorf("the old match after unmatch: %+v %v", m, err)
	}

	ch, err := f.replacing(alice, next, false, p.ID)
	if err != nil {
		t.Fatalf("confirm the new transaction as replacing the record: %v", err)
	}
	st := f.state(o)
	if st.Status != payments.StatusPaid || st.Paid.String() != "12.99 EUR" || st.Shortfall.String() != "0.50 EUR" ||
		st.Overpaid.Sign() != 0 || st.EvidenceCount != 1 {
		t.Errorf("paid by the new transaction within the tolerance: %+v", st)
	}
	if want := string(payments.EvidenceBankTransaction) + ":" + ch.Match.ID; strings.Join(f.evidenceKinds(o), ",") != want {
		t.Errorf("evidence after the replacement: %v, want %s", f.evidenceKinds(o), want)
	}
	if got := f.manual(o); got[p.ID] != ch.Match.ID {
		t.Errorf("the record is replaced by the new match: %v", got)
	}
}

// TestAllowOverpaymentCountsTheHeldRecordToo (X5e): the route the error hint
// and the MCP description warn against. With allow_overpayment the held record
// and the new match both count, and the second payment shows as overpaid.
func TestAllowOverpaymentCountsTheHeldRecordToo(t *testing.T) {
	f, _, next, o, _ := reReported(t)
	if _, err := f.ms.Confirm(f.ctx, alice, next.ID, next.Version, true, nil); err != nil {
		t.Fatal(err)
	}
	if st := f.state(o); st.Paid.String() != "26.48 EUR" || st.Overpaid.String() != "12.99 EUR" {
		t.Errorf("held record and new match both count: %+v", st)
	}
}

// TestHoldShowsOverpaymentTheCheckAccepts (X5e item 2, ADR-0049 amendment E):
// during a hold the overpayment check counts the replacing match's amount (12.99)
// while the state counts the held record's amount (13.49). So a 0.50 payment the
// check accepts shows as overpaid 0.50 until the transaction is reported again.
// This pins the documented behaviour; the check is not aligned to the record.
func TestHoldShowsOverpaymentTheCheckAccepts(t *testing.T) {
	f := newFixture(t)
	s, o := f.suggestion()
	p := f.record(o, "13.49")
	if _, err := f.replacing(alice, s, false, p.ID); err != nil {
		t.Fatal(err)
	}
	streaming := f.tx("STREAMING SERVICE")
	f.markGone(streaming)
	if _, err := f.match(f.tx("RENT 2026-10"), o, "0.50", matches.OriginUser, matches.StateConfirmed); err != nil {
		t.Fatalf("the check accepts 12.99 + 0.50 against 13.49: %v", err)
	}
	if st := f.state(o); st.Paid.String() != "13.99 EUR" || st.Overpaid.String() != "0.50 EUR" {
		t.Errorf("during the hold the record counts at its amount: %+v", st)
	}
	f.markReported(streaming)
	if st := f.state(o); st.Paid.String() != "13.49 EUR" || st.Overpaid.Sign() != 0 {
		t.Errorf("reported again, nothing is overpaid: %+v", st)
	}
}
