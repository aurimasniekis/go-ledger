package matches_test

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/matches"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/payments"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
)

var (
	aliceSession = audit.Actor{Kind: audit.ActorUser, UserID: alice.UserID, Via: audit.ViaSession}
	aliceMCP     = audit.Actor{Kind: audit.ActorToken, UserID: alice.UserID, TokenID: "01JD3Y4C8V9M2Q7W5R6T0XZBN0", Via: audit.ViaMCP}
	aliceAPI     = audit.Actor{Kind: audit.ActorToken, UserID: alice.UserID, TokenID: "01JD3Y4C8V9M2Q7W5R6T0XZBN0", Via: audit.ViaToken}
	bob          = audit.Actor{Kind: audit.ActorUser, UserID: "01JD3Y4C8V9M2Q7W5R6T0XZBNB", Via: audit.ViaSession}
	bobToken     = audit.Actor{Kind: audit.ActorToken, UserID: bob.UserID, TokenID: "01JD3Y4C8V9M2Q7W5R6T0XZBN1", Via: audit.ViaToken}
)

// suggestion is the engine's suggestion that STREAMING SERVICE (12.99) pays
// October's occurrence of a 13.49 commitment with a 0.50 tolerance.
func (f *fixture) suggestion() (matches.Match, occurrences.Occurrence) {
	f.t.Helper()
	o := f.occurrence(f.named("Streaming service", "13.49", 5, "0.50").ID, "2026-10")
	ch, err := f.ms.Create(f.ctx, audit.System, matches.New{TransactionID: f.tx("STREAMING SERVICE").ID, OccurrenceID: o.ID, Amount: money.MustParse("12.99", "EUR"),
		Origin: matches.OriginAutoRule, State: matches.StateSuggested, Reason: reason("description similarity 80/100 (a hint only)")})
	if err != nil {
		f.t.Fatal(err)
	}
	return ch.Match, o
}

func (f *fixture) confirmAs(a audit.Actor, m matches.Match) matches.Match {
	f.t.Helper()
	ch, err := f.ms.Confirm(f.ctx, a, m.ID, m.Version, false, reason("it is the streaming bill"))
	if err != nil {
		f.t.Fatal(err)
	}
	return ch.Match
}

func (f *fixture) tolerance(id string) (amount, currency *string) {
	f.t.Helper()
	if err := f.scanRow("SELECT tolerance_amount, tolerance_currency FROM payment_matches WHERE id = ?", []any{id}, &amount, &currency); err != nil {
		f.t.Fatal(err)
	}
	return amount, currency
}

func sameState(a, b payments.State) bool {
	return a.Status == b.Status && a.Paid.String() == b.Paid.String() && a.Outstanding.String() == b.Outstanding.String() &&
		a.Shortfall.String() == b.Shortfall.String() && a.EvidenceCount == b.EvidenceCount
}

// TestUnconfirmRestoresTheSuggestion (D42 1a, ADR-0045): the person who
// confirmed a suggestion takes it back, here through MCP with their token. The
// match is the suggestion it was (no decision, the engine's reason, no
// tolerance snapshot), the occurrence's payment state is what it was, and the
// history and audit keep every step with how it came.
func TestUnconfirmRestoresTheSuggestion(t *testing.T) {
	f := newFixture(t)
	s, o := f.suggestion()
	before := f.state(o)
	if before.Status == payments.StatusPaid || before.EvidenceCount != 0 {
		t.Fatalf("before: %+v", before)
	}
	c := f.confirmAs(aliceSession, s)
	if st := f.state(o); st.Status != payments.StatusPaid || st.Shortfall.String() != "0.50 EUR" {
		t.Fatalf("confirmed within the tolerance: %+v", st)
	}
	if amt, _ := f.tolerance(c.ID); amt == nil || *amt != "0.50" {
		t.Fatalf("tolerance snapshot at confirmation: %v", amt)
	}
	if c.DecidedBy == nil || c.DecidedBy.Via != audit.ViaSession || c.CreatedBy.Via != audit.ViaSystem {
		t.Fatalf("provenance of the confirmation: created %+v decided %+v", c.CreatedBy, c.DecidedBy)
	}

	// A stale version is refused and changes nothing.
	if _, err := f.ms.Unconfirm(f.ctx, aliceMCP, c.ID, c.Version-1, nil); !errors.As(err, new(*resource.VersionConflictError)) {
		t.Fatalf("stale unconfirm: %v", err)
	}
	ch, err := f.ms.Unconfirm(f.ctx, aliceMCP, c.ID, c.Version, reason("wrong month"))
	if err != nil {
		t.Fatal(err)
	}
	u := ch.Match
	if u.State != matches.StateSuggested || u.DecidedBy != nil || u.DecidedAt != nil || u.Reason == nil || *u.Reason != *s.Reason || u.Version != c.Version+1 ||
		u.Amount.String() != s.Amount.String() || u.CountsAsEvidence() {
		t.Fatalf("unconfirmed: %+v", u)
	}
	if amt, cur := f.tolerance(u.ID); amt != nil || cur != nil {
		t.Errorf("tolerance snapshot kept after unconfirm: %v %v", amt, cur)
	}
	if after := f.state(o); !sameState(before, after) {
		t.Errorf("state not restored: before %+v after %+v", before, after)
	}
	if ch.Result.Occurrence.Version <= c.Version {
		t.Errorf("occurrence version did not move: %d", ch.Result.Occurrence.Version)
	}
	want := []struct {
		a   matches.Action
		st  matches.State
		via audit.Via
	}{{matches.ActionSuggested, matches.StateSuggested, audit.ViaSystem}, {matches.ActionConfirmed, matches.StateConfirmed, audit.ViaSession},
		{matches.ActionUnconfirmed, matches.StateSuggested, audit.ViaMCP}}
	if len(u.History) != len(want) {
		t.Fatalf("history: %+v", u.History)
	}
	for i, w := range want {
		if e := u.History[i]; e.Action != w.a || e.State != w.st || e.By.Via != w.via {
			t.Errorf("history[%d] = %s %s via %q, want %s %s via %q", i, e.Action, e.State, e.By.Via, w.a, w.st, w.via)
		}
	}
	if r := u.History[2].Reason; r == nil || *r != "wrong month" {
		t.Errorf("unconfirm reason in the history: %v", r)
	}
	f.rec.mu.Lock()
	last := f.rec.events[len(f.rec.events)-1]
	f.rec.mu.Unlock()
	if last.Action != audit.PaymentMatchUnconfirmed || last.Details["from"] != "confirmed" || last.Details["via"] != "mcp" || last.Actor.TokenID != aliceMCP.TokenID {
		t.Errorf("audit: %+v", last)
	}
	// It is a suggestion again: a person may confirm it again.
	if again := f.confirmAs(aliceAPI, u); again.State != matches.StateConfirmed || again.DecidedBy.Via != audit.ViaToken {
		t.Errorf("confirmed again: %+v", again)
	}
}

// TestUnconfirmTwiceRestoresTheSuggestionsReason: confirm, unconfirm,
// confirm, unconfirm leaves the engine's reason, not the first undo's text.
func TestUnconfirmTwiceRestoresTheSuggestionsReason(t *testing.T) {
	f := newFixture(t)
	s, _ := f.suggestion()
	want := *s.Reason
	c := f.confirmAs(alice, s)
	ch, err := f.ms.Unconfirm(f.ctx, alice, c.ID, c.Version, reason("first undo"))
	if err != nil {
		t.Fatal(err)
	}
	c = f.confirmAs(alice, ch.Match)
	ch, err = f.ms.Unconfirm(f.ctx, alice, c.ID, c.Version, reason("second undo"))
	if err != nil {
		t.Fatal(err)
	}
	if ch.Match.Reason == nil || *ch.Match.Reason != want {
		t.Errorf("reason %v, want %q", ch.Match.Reason, want)
	}
}

// TestUnconfirmedHistoryKeptOnlyWhileDecided: after an unconfirm
// the match is a suggestion again, so history_kept no longer protects it
// (ADR-0045 limitations); while confirmed it does.
func TestUnconfirmedHistoryKeptOnlyWhileDecided(t *testing.T) {
	f := newFixture(t)
	s, _ := f.suggestion()
	c := f.confirmAs(alice, s)
	if err := f.exec("DELETE FROM payment_match_events WHERE match_id = ?", c.ID); err == nil || !strings.Contains(err.Error(), "never deleted") {
		t.Fatalf("confirmed history deleted: %v", err)
	}
	if _, err := f.ms.Unconfirm(f.ctx, alice, c.ID, c.Version, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.exec("DELETE FROM payment_match_events WHERE match_id = ?", c.ID); err != nil {
		t.Fatalf("history of a suggestion again: %v", err)
	}
}

// TestUnconfirmOnlyByTheDecider: another person, their token, automation, a
// confirmation automation made, and a match created confirmed are refused.
func TestUnconfirmOnlyByTheDecider(t *testing.T) {
	f := newFixture(t)
	s, _ := f.suggestion()
	c := f.confirmAs(aliceAPI, s)
	for name, a := range map[string]audit.Actor{"another user": bob, "another user's token": bobToken} {
		if _, err := f.ms.Unconfirm(f.ctx, a, c.ID, c.Version, nil); !errors.Is(err, matches.ErrUndoNotDecider) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := f.ms.Unconfirm(f.ctx, audit.System, c.ID, c.Version, nil); !errors.Is(err, matches.ErrActorUndo) {
		t.Errorf("automation: %v", err)
	}
	// The confirming token's owner, by session, counts as the decider.
	if _, err := f.ms.Unconfirm(f.ctx, aliceSession, c.ID, c.Version, nil); err != nil {
		t.Errorf("the decider by session: %v", err)
	}

	// The engine's own confirmation is not a person's: nobody may unconfirm it.
	g := newFixture(t)
	streaming := g.occurrence(g.named("Streaming service", "13.49", 5, "0.50").ID, "2026-10")
	if res := g.run(g.matcher()); res.Confirmed != 1 {
		t.Fatalf("run: %+v", res)
	}
	auto := g.matchesOf(streaming)[0]
	if _, err := g.ms.Unconfirm(g.ctx, aliceSession, auto.ID, auto.Version, nil); !errors.Is(err, matches.ErrNotUnconfirmable) && !errors.Is(err, matches.ErrUndoNotDecider) {
		t.Errorf("automatic confirmation: %v", err)
	}

	// A match created confirmed was never a suggestion.
	o := f.occurrence(f.commitment("800.00", "EUR", 2).ID, "2026-10")
	m := f.mustMatch(f.tx("RENT 2026-10"), o, "800.00")
	if _, err := f.ms.Unconfirm(f.ctx, alice, m.ID, m.Version, nil); !errors.Is(err, matches.ErrNotUnconfirmable) || !errors.Is(err, matches.ErrTransition) {
		t.Errorf("created confirmed: %v", err)
	}
	// Only a confirmed match can be unconfirmed.
	r, err := f.ms.Unmatch(f.ctx, alice, m.ID, m.Version, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ms.Unconfirm(f.ctx, alice, r.Match.ID, r.Match.Version, nil); !errors.Is(err, matches.ErrTransition) {
		t.Errorf("rejected: %v", err)
	}
}

// TestUnconfirmWindow: the window closes when the match is corrected, when
// the person makes another match decision, or when anything else changes the
// occurrence; another person's change elsewhere leaves it open.
func TestUnconfirmWindow(t *testing.T) {
	closed := func(t *testing.T, f *fixture, m matches.Match) {
		t.Helper()
		cur, err := f.ms.Get(f.ctx, m.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.ms.Unconfirm(f.ctx, alice, cur.ID, cur.Version, nil); !errors.Is(err, matches.ErrUndoWindow) {
			t.Errorf("unconfirm: %v, want the window closed", err)
		}
		if got, _ := f.ms.Get(f.ctx, m.ID); got.State != matches.StateConfirmed {
			t.Errorf("state after a refused unconfirm: %s", got.State)
		}
	}
	t.Run("corrected since", func(t *testing.T) {
		f := newFixture(t)
		s, _ := f.suggestion()
		c := f.confirmAs(alice, s)
		if _, err := f.ms.Correct(f.ctx, alice, c.ID, c.Version, money.MustParse("12.00", "EUR"), false, nil); err != nil {
			t.Fatal(err)
		}
		closed(t, f, c)
	})
	t.Run("the person decided another match since", func(t *testing.T) {
		f := newFixture(t)
		s, _ := f.suggestion()
		c := f.confirmAs(alice, s)
		rent := f.occurrence(f.commitment("800.00", "EUR", 2).ID, "2026-10")
		if _, err := f.ms.Create(f.ctx, aliceMCP, matches.New{TransactionID: f.tx("RENT 2026-10").ID, OccurrenceID: rent.ID, Amount: money.MustParse("800.00", "EUR"),
			Origin: matches.OriginUser, State: matches.StateConfirmed}); err != nil {
			t.Fatal(err)
		}
		closed(t, f, c)
	})
	t.Run("another match touched the occurrence", func(t *testing.T) {
		f := newFixture(t)
		s, o := f.suggestion()
		c := f.confirmAs(alice, s)
		if _, err := f.ms.Create(f.ctx, bob, matches.New{TransactionID: f.tx("RENT 2026-10").ID, OccurrenceID: o.ID, Amount: money.MustParse("0.50", "EUR"),
			Origin: matches.OriginUser, State: matches.StateConfirmed}); err != nil {
			t.Fatal(err)
		}
		closed(t, f, c)
	})
	t.Run("a manual payment touched the occurrence", func(t *testing.T) {
		f := newFixture(t)
		s, o := f.suggestion()
		c := f.confirmAs(alice, s)
		if _, _, err := f.ps.RecordManual(f.ctx, bob, o.ID, payments.NewManual{Amount: money.MustParse("0.50", "EUR"), PaidOn: d("2026-10-06")}, d("2026-11-01")); err != nil {
			t.Fatal(err)
		}
		closed(t, f, c)
	})
	t.Run("another person's change elsewhere leaves it open", func(t *testing.T) {
		f := newFixture(t)
		s, o := f.suggestion()
		before := f.state(o)
		c := f.confirmAs(alice, s)
		rent := f.occurrence(f.commitment("800.00", "EUR", 2).ID, "2026-10")
		if _, err := f.ms.Create(f.ctx, bob, matches.New{TransactionID: f.tx("RENT 2026-10").ID, OccurrenceID: rent.ID, Amount: money.MustParse("800.00", "EUR"),
			Origin: matches.OriginUser, State: matches.StateConfirmed}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.ms.Unconfirm(f.ctx, alice, c.ID, c.Version, nil); err != nil {
			t.Fatalf("unconfirm: %v", err)
		}
		if after := f.state(o); !sameState(before, after) {
			t.Errorf("state not restored: before %+v after %+v", before, after)
		}
	})
	t.Run("a later decision with an earlier rowid still closes it", func(t *testing.T) {
		f := newFixture(t)
		s, _ := f.suggestion()
		c := f.confirmAs(alice, s)
		rent := f.occurrence(f.commitment("800.00", "EUR", 2).ID, "2026-10")
		other, err := f.ms.Create(f.ctx, alice, matches.New{TransactionID: f.tx("RENT 2026-10").ID, OccurrenceID: rent.ID, Amount: money.MustParse("800.00", "EUR"),
			Origin: matches.OriginUser, State: matches.StateConfirmed})
		if err != nil {
			t.Fatal(err)
		}
		// Rowids are not a safe order across matches (deletions free and
		// reuse them): give the later entry the lowest rowid.
		f.mustExec("DROP TRIGGER payment_match_events_append_only")
		f.mustExec("UPDATE payment_match_events SET rowid = -1, at = '2099-01-01T00:00:00.000000000Z' WHERE match_id = ?", other.Match.ID)
		closed(t, f, c)
	})
	t.Run("a clock stepped back does not hide a later decision", func(t *testing.T) {
		f := newFixture(t)
		s, _ := f.suggestion()
		f.clock.set(t0.Add(time.Hour))
		c := f.confirmAs(alice, s)
		// The clock steps back: the next decision is written with an earlier
		// time but a later insertion order.
		f.clock.set(t0)
		rent := f.occurrence(f.commitment("800.00", "EUR", 2).ID, "2026-10")
		b, err := f.ms.Create(f.ctx, audit.System, matches.New{TransactionID: f.tx("RENT 2026-10").ID, OccurrenceID: rent.ID, Amount: money.MustParse("800.00", "EUR"),
			Origin: matches.OriginAutoRule, State: matches.StateSuggested, Reason: reason("rent suggestion")})
		if err != nil {
			t.Fatal(err)
		}
		f.confirmAs(alice, b.Match)
		closed(t, f, c)
	})
	t.Run("confirmed before provenance was recorded", func(t *testing.T) {
		f := newFixture(t)
		s, _ := f.suggestion()
		c := f.confirmAs(alice, s)
		// An entry migrated from before migration 0023 has no occurrence
		// version: its window is closed. (The history is append-only, so
		// the trigger is lifted for this one row.)
		f.mustExec("DROP TRIGGER payment_match_events_append_only")
		f.mustExec("UPDATE payment_match_events SET occurrence_version = NULL WHERE match_id = ?", c.ID)
		closed(t, f, c)
	})
}

// TestConcurrentUnconfirmAndCorrect: an unconfirm and a correction of the same
// version race; exactly one wins and the other gets a version conflict.
func TestConcurrentUnconfirmAndCorrect(t *testing.T) {
	f := newFixture(t)
	s, o := f.suggestion()
	c := f.confirmAs(alice, s)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errs[0] = f.ms.Unconfirm(f.ctx, alice, c.ID, c.Version, nil)
	}()
	go func() {
		defer wg.Done()
		_, errs[1] = f.ms.Correct(f.ctx, alice, c.ID, c.Version, money.MustParse("12.50", "EUR"), false, nil)
	}()
	wg.Wait()
	conflicts, ok := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.As(err, new(*resource.VersionConflictError)):
			conflicts++
		default:
			t.Errorf("unexpected: %v", err)
		}
	}
	if ok != 1 || conflicts != 1 {
		t.Fatalf("results %v: want one success and one version conflict", errs)
	}
	got, err := f.ms.Get(f.ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	st := f.state(o)
	switch {
	case errs[0] == nil && (got.State != matches.StateSuggested || st.EvidenceCount != 0):
		t.Errorf("unconfirm won but the match is %s with %d evidence", got.State, st.EvidenceCount)
	case errs[1] == nil && (got.State != matches.StateConfirmed || got.Amount.String() != "12.50 EUR"):
		t.Errorf("correct won but the match is %s %s", got.State, got.Amount)
	}
}
