package matches_test

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
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
