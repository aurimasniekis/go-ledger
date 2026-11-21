package matches_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/matches"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
)

func (f *fixture) exec(query string, args ...any) error {
	f.t.Helper()
	return f.db.WriteTx(f.ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, query, args...)
		return err
	})
}

func (f *fixture) mustExec(query string, args ...any) {
	f.t.Helper()
	if err := f.exec(query, args...); err != nil {
		f.t.Fatal(err)
	}
}

const goneAt = "2026-11-02T09:00:00.000000000Z"

func (f *fixture) markGone(tx banking.StoredTransaction) {
	f.t.Helper()
	f.mustExec("UPDATE bank_transactions SET no_longer_reported_at = ? WHERE id = ?", goneAt, string(tx.ID))
}

func (f *fixture) markReported(tx banking.StoredTransaction) {
	f.t.Helper()
	f.mustExec("UPDATE bank_transactions SET no_longer_reported_at = NULL WHERE id = ?", string(tx.ID))
}

// TestOverpaymentCountsUncountableConfirmations: a confirmed match on a
// transaction the provider stopped reporting is still counted by the
// overpayment check, so reporting it again cannot double-count.
func TestOverpaymentCountsUncountableConfirmations(t *testing.T) {
	f := newFixture(t)
	c := f.commitment("700.00", "EUR", 2)
	o := f.occurrence(c.ID, "2026-10")
	first, second := f.tx("RENT 2026-10"), f.tx("STREAMING SERVICE")
	m1 := f.mustMatch(first, o, "700.00")
	f.markGone(first)
	if _, err := f.match(second, o, "10.00", matches.OriginUser, matches.StateConfirmed); !errors.Is(err, matches.ErrOverpaid) {
		t.Fatalf("second payment while the first is unreported: %v", err)
	}
	// Allowing the overpayment is still the user's explicit choice.
	if _, err := f.ms.Create(f.ctx, alice, matches.New{TransactionID: second.ID, OccurrenceID: o.ID, Amount: money.MustParse("10.00", "EUR"),
		Origin: matches.OriginUser, State: matches.StateConfirmed, AllowOverpayment: true}); err != nil {
		t.Fatal(err)
	}
	f.markReported(first)
	if st := f.state(o); st.Overpaid.String() != "10.00 EUR" || st.EvidenceCount != 2 {
		t.Errorf("state: %+v", st)
	}
	// Correcting a confirmed match leaves only itself out.
	if _, err := f.ms.Correct(f.ctx, alice, m1.ID, m1.Version, money.MustParse("700.00", "EUR"), false, nil); !errors.Is(err, matches.ErrOverpaid) {
		t.Errorf("correct beside an allowed overpayment: %v", err)
	}
}

func TestAutoRuleConfirmationIsOneToOne(t *testing.T) {
	f := newFixture(t)
	rent := f.tx("RENT 2026-10") // -800.00
	c := f.commitment("800.00", "EUR", 2)
	o := f.occurrence(c.ID, "2026-10")
	if _, err := f.match(rent, o, "700.00", matches.OriginAutoRule, matches.StateConfirmed); !errors.Is(err, matches.ErrAutoNotOneToOne) {
		t.Errorf("partial allocation: %v", err)
	}
	// Another open allocation of the transaction (a suggestion elsewhere).
	other := f.commitment("800.00", "EUR", 3)
	oo := f.occurrence(other.ID, "2026-10")
	if _, err := f.match(rent, oo, "800.00", matches.OriginModel, matches.StateSuggested); err != nil {
		t.Fatal(err)
	}
	if _, err := f.match(rent, o, "800.00", matches.OriginAutoRule, matches.StateConfirmed); !errors.Is(err, matches.ErrAutoNotOneToOne) {
		t.Errorf("transaction with another open match: %v", err)
	}
	// Occurrence amount.
	big := f.commitment("810.00", "EUR", 4)
	ob := f.occurrence(big.ID, "2026-10")
	if _, err := f.match(rent, ob, "800.00", matches.OriginAutoRule, matches.StateConfirmed); !errors.Is(err, matches.ErrAutoNotOneToOne) {
		t.Errorf("still the other suggestion: %v", err)
	}
	f.mustExec("DELETE FROM payment_matches WHERE state = 'suggested'")
	if _, err := f.match(rent, ob, "800.00", matches.OriginAutoRule, matches.StateConfirmed); !errors.Is(err, matches.ErrAutoAmount) {
		t.Errorf("amount differs from the occurrence: %v", err)
	}
	ch, err := f.ms.Create(f.ctx, alice, matches.New{TransactionID: rent.ID, OccurrenceID: ob.ID, Amount: money.MustParse("800.00", "EUR"),
		Origin: matches.OriginAutoRule, State: matches.StateConfirmed, AutoTolerance: money.MustParse("10.00", "EUR")})
	if err != nil || ch.Match.State != matches.StateConfirmed {
		t.Fatalf("within the tolerance: %+v %v", ch.Match, err)
	}
	// A rule suggestion is not restricted.
	if _, err := f.match(rent, o, "300.00", matches.OriginAutoRule, matches.StateSuggested); err != nil {
		t.Errorf("rule suggestion: %v", err)
	}
}

func TestAllocationScaleRefused(t *testing.T) {
	f := newFixture(t)
	c := f.commitment("800.00", "EUR", 2)
	o := f.occurrence(c.ID, "2026-10")
	if _, err := f.match(f.tx("RENT 2026-10"), o, "799.999", matches.OriginUser, matches.StateConfirmed); !errors.Is(err, matches.ErrScale) {
		t.Errorf("finer allocation: %v", err)
	}
}

func TestHistoryOfConfirmedMatchIsNeverDeleted(t *testing.T) {
	f := newFixture(t)
	c := f.commitment("800.00", "EUR", 2)
	o := f.occurrence(c.ID, "2026-10")
	m := f.mustMatch(f.tx("RENT 2026-10"), o, "800.00")
	err := f.exec("DELETE FROM payment_match_events WHERE match_id = ?", m.ID)
	if err == nil || !strings.Contains(err.Error(), "never deleted") {
		t.Errorf("delete history of a confirmed match: %v", err)
	}
	r, err := f.ms.Reverse(f.ctx, alice, m.ID, m.Version, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.exec("DELETE FROM payment_match_events WHERE match_id = ?", r.Match.ID); err == nil || !strings.Contains(err.Error(), "never deleted") {
		t.Errorf("delete history of a reversed match: %v", err)
	}
	if got, _ := f.ms.Get(f.ctx, m.ID); len(got.History) != 2 {
		t.Errorf("history: %+v", got.History)
	}
}

func TestNeedsReviewForUnreportedSuggestions(t *testing.T) {
	f := newFixture(t)
	c := f.commitment("70.00", "EUR", 30)
	o := f.occurrence(c.ID, "2026-10")
	hold := f.tx("HOTEL HOLD")
	s, err := f.match(hold, o, "70.00", matches.OriginModel, matches.StateSuggested)
	if err != nil {
		t.Fatal(err)
	}
	if s.Match.NeedsReview() {
		t.Errorf("a suggestion on a live pending transaction needs no review")
	}
	f.markGone(hold)
	got, _ := f.ms.Get(f.ctx, s.Match.ID)
	if !got.NeedsReview() || got.CountsAsEvidence() {
		t.Errorf("unreported pending suggestion: %+v", got)
	}
	f.markReported(hold)
	f.mustExec("UPDATE bank_transactions SET superseded_by = ? WHERE id = ?", string(f.tx("RENT 2026-10").ID), string(hold.ID))
	if got, _ = f.ms.Get(f.ctx, s.Match.ID); !got.NeedsReview() {
		t.Errorf("superseded pending suggestion: %+v", got)
	}
}

const (
	insertMatch = "INSERT INTO payment_matches (id, transaction_id, occurrence_id, amount, currency, origin, state, created_by_kind, decided_by_kind, decided_at, created_at, updated_at)" +
		" VALUES (?, ?, ?, '1.00', 'EUR', ?, ?, 'user', ?, ?, '2026-11-01T09:00:00.000000000Z', '2026-11-01T09:00:00.000000000Z')"
	at = "2026-11-01T09:00:00.000000000Z"
)

// TestDatabaseRefusesConfirmationOnUncountableTransactions exercises the
// triggers directly, bypassing the store's own checks.
func TestDatabaseRefusesConfirmationOnUncountableTransactions(t *testing.T) {
	f := newFixture(t)
	c := f.commitment("70.00", "EUR", 30)
	o := f.occurrence(c.ID, "2026-10")
	hold, gone, sup := f.tx("HOTEL HOLD"), f.tx("RENT 2026-10"), f.tx("STREAMING SERVICE")
	f.markGone(gone)
	f.mustExec("UPDATE bank_transactions SET superseded_by = ? WHERE id = ?", string(gone.ID), string(sup.ID))
	const want = "booked"
	for name, tx := range map[string]banking.StoredTransaction{"pending": hold, "no longer reported": gone, "superseded": sup} {
		err := f.exec(insertMatch, "01JZ0000000000000000000A01", string(tx.ID), o.ID, "user", "confirmed", "user", at)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("insert confirmed on %s: %v", name, err)
		}
		// Update: a suggestion cannot be turned into a confirmation.
		f.mustExec(insertSuggestion, "01JZ0000000000000000000B01", string(tx.ID), o.ID)
		err = f.exec("UPDATE payment_matches SET state = 'confirmed', decided_by_kind = 'user', decided_at = ? WHERE id = ?", at, "01JZ0000000000000000000B01")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("confirm a suggestion on %s: %v", name, err)
		}
		f.mustExec("DELETE FROM payment_matches WHERE id = ?", "01JZ0000000000000000000B01")
	}
}

const insertSuggestion = "INSERT INTO payment_matches (id, transaction_id, occurrence_id, amount, currency, origin, state, created_by_kind, created_at, updated_at)" +
	" VALUES (?, ?, ?, '1.00', 'EUR', 'auto_rule', 'suggested', 'user', '2026-11-01T09:00:00.000000000Z', '2026-11-01T09:00:00.000000000Z')"

// TestDatabaseRefusesOriginStateMismatch: a model suggestion is never
// confirmed and a user's match never only suggested, whatever the caller.
func TestDatabaseRefusesOriginStateMismatch(t *testing.T) {
	f := newFixture(t)
	c := f.commitment("70.00", "EUR", 30)
	o := f.occurrence(c.ID, "2026-10")
	rent := f.tx("RENT 2026-10")
	err := f.exec(insertMatch, "01JZ0000000000000000000C01", string(rent.ID), o.ID, "model_suggestion", "confirmed", "user", at)
	if err == nil || !strings.Contains(err.Error(), "starts as a suggestion") {
		t.Errorf("model confirmed: %v", err)
	}
	err = f.exec(insertMatch, "01JZ0000000000000000000C02", string(rent.ID), o.ID, "user", "suggested", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "starts as a suggestion") {
		t.Errorf("user suggested: %v", err)
	}
	err = f.exec(insertMatch, "01JZ0000000000000000000C03", string(rent.ID), o.ID, "auto_rule", "rejected", "user", at)
	if err == nil || !strings.Contains(err.Error(), "starts as a suggestion") {
		t.Errorf("born rejected: %v", err)
	}
	// Control: a valid insert through the same statement works.
	if err := f.exec(insertMatch, "01JZ0000000000000000000C04", string(rent.ID), o.ID, "user", "confirmed", "user", at); err != nil {
		t.Errorf("control insert: %v", err)
	}
}

// TestAutomationActorIsHeldToTheAutoRules: an automation actor (system or
// anonymous) cannot go around the 1:1 guard by suggesting first, by claiming origin user or by correcting.
func TestAutomationActorIsHeldToTheAutoRules(t *testing.T) {
	for _, sys := range []audit.Actor{audit.System, {Kind: audit.ActorAnonymous}} {
		t.Run(string(sys.Kind), func(t *testing.T) { automationGuard(t, sys) })
	}
}

// TestAutomationNeverUndoesAPersonsDecision is decision D33: automation may
// never unmatch or reverse, and may reject only a suggestion with no person in
// its history.
func TestAutomationNeverUndoesAPersonsDecision(t *testing.T) {
	tok := audit.Actor{Kind: audit.ActorToken, UserID: alice.UserID, TokenID: "01JD3Y4C8V9M2Q7W5R6T0XZBNT"}
	for _, sys := range []audit.Actor{audit.System, {Kind: audit.ActorAnonymous}} {
		t.Run(string(sys.Kind), func(t *testing.T) {
			undo := map[string]func(f *fixture, m matches.Match, a audit.Actor) error{
				"reject": func(f *fixture, m matches.Match, a audit.Actor) error {
					_, err := f.ms.Reject(f.ctx, a, m.ID, m.Version, nil)
					return err
				},
				"unmatch": func(f *fixture, m matches.Match, a audit.Actor) error {
					_, err := f.ms.Unmatch(f.ctx, a, m.ID, m.Version, nil)
					return err
				},
				"reverse": func(f *fixture, m matches.Match, a audit.Actor) error {
					_, err := f.ms.Reverse(f.ctx, a, m.ID, m.Version, nil)
					return err
				},
			}
			// cases build a match in the state each undo needs.
			type setup func(f *fixture) matches.Match
			confirmed := map[string]setup{
				"confirmed by a user": func(f *fixture) matches.Match {
					return f.mustMatch(f.tx("RENT 2026-10"), f.occurrence(f.commitment("800.00", "EUR", 2).ID, "2026-10"), "800.00")
				},
				"confirmed by a token": func(f *fixture) matches.Match {
					rent, o := f.tx("RENT 2026-10"), f.occurrence(f.commitment("800.00", "EUR", 2).ID, "2026-10")
					ch, err := f.ms.Create(f.ctx, tok, matches.New{TransactionID: rent.ID, OccurrenceID: o.ID, Amount: money.MustParse("800.00", "EUR"), Origin: matches.OriginUser, State: matches.StateConfirmed})
					if err != nil {
						f.t.Fatal(err)
					}
					return ch.Match
				},
				"confirmed by automation, then corrected by a user": func(f *fixture) matches.Match {
					rent, o := f.tx("RENT 2026-10"), f.occurrence(f.commitment("800.00", "EUR", 2).ID, "2026-10")
					ch, err := f.ms.Create(f.ctx, audit.System, matches.New{TransactionID: rent.ID, OccurrenceID: o.ID, Amount: money.MustParse("800.00", "EUR"), Origin: matches.OriginAutoRule, State: matches.StateConfirmed})
					if err != nil {
						f.t.Fatal(err)
					}
					ch, err = f.ms.Correct(f.ctx, alice, ch.Match.ID, ch.Match.Version, money.MustParse("700.00", "EUR"), false, nil)
					if err != nil {
						f.t.Fatal(err)
					}
					return ch.Match
				},
			}
			for name, mk := range confirmed {
				for _, u := range []string{"unmatch", "reverse"} {
					t.Run(name+"/"+u, func(t *testing.T) {
						f := newFixture(t)
						m := mk(f)
						if err := undo[u](f, m, sys); !errors.Is(err, matches.ErrActorUndo) {
							t.Fatalf("automation %s: %v", u, err)
						}
						got, err := f.ms.Get(f.ctx, m.ID)
						if err != nil || got.State != matches.StateConfirmed || got.Version != m.Version {
							t.Errorf("match changed: %+v %v", got, err)
						}
						// Control: a user can.
						if err := undo[u](f, m, alice); err != nil {
							t.Errorf("user %s: %v", u, err)
						}
					})
				}
			}
			t.Run("a suggestion a user made or corrected cannot be rejected by automation", func(t *testing.T) {
				f := newFixture(t)
				rent, o := f.tx("RENT 2026-10"), f.occurrence(f.commitment("800.00", "EUR", 2).ID, "2026-10")
				ch, err := f.match(rent, o, "300.00", matches.OriginModel, matches.StateSuggested)
				if err != nil {
					t.Fatal(err)
				}
				if err := undo["reject"](f, ch.Match, sys); !errors.Is(err, matches.ErrActorUndo) {
					t.Errorf("reject a user's suggestion: %v", err)
				}
				// An automation suggestion a user corrected.
				rent2, o2 := rent, f.occurrence(f.commitment("500.00", "EUR", 3).ID, "2026-11")
				s, err := f.ms.Create(f.ctx, sys, matches.New{TransactionID: rent2.ID, OccurrenceID: o2.ID, Amount: money.MustParse("500.00", "EUR"), Origin: matches.OriginAutoRule, State: matches.StateSuggested})
				if err != nil {
					t.Fatal(err)
				}
				c, err := f.ms.Correct(f.ctx, alice, s.Match.ID, s.Match.Version, money.MustParse("400.00", "EUR"), false, nil)
				if err != nil {
					t.Fatal(err)
				}
				if err := undo["reject"](f, c.Match, sys); !errors.Is(err, matches.ErrActorUndo) {
					t.Errorf("reject a corrected suggestion: %v", err)
				}
			})
			t.Run("automation may drop its own suggestion", func(t *testing.T) {
				f := newFixture(t)
				rent, o := f.tx("RENT 2026-10"), f.occurrence(f.commitment("800.00", "EUR", 2).ID, "2026-10")
				s, err := f.ms.Create(f.ctx, sys, matches.New{TransactionID: rent.ID, OccurrenceID: o.ID, Amount: money.MustParse("800.00", "EUR"), Origin: matches.OriginModel, State: matches.StateSuggested})
				if err != nil {
					t.Fatal(err)
				}
				if err := undo["reject"](f, s.Match, sys); err != nil {
					t.Errorf("reject own suggestion: %v", err)
				}
			})
			t.Run("automation cannot undo its own confirmation", func(t *testing.T) {
				for _, u := range []string{"unmatch", "reverse"} {
					f := newFixture(t)
					rent, o := f.tx("RENT 2026-10"), f.occurrence(f.commitment("800.00", "EUR", 2).ID, "2026-10")
					c, err := f.ms.Create(f.ctx, sys, matches.New{TransactionID: rent.ID, OccurrenceID: o.ID, Amount: money.MustParse("800.00", "EUR"), Origin: matches.OriginAutoRule, State: matches.StateConfirmed})
					if err != nil {
						t.Fatal(err)
					}
					if err := undo[u](f, c.Match, sys); !errors.Is(err, matches.ErrActorUndo) {
						t.Errorf("automation %s of its own confirmation: %v", u, err)
					}
					if got, err := f.ms.Get(f.ctx, c.Match.ID); err != nil || got.State != matches.StateConfirmed || got.Version != c.Match.Version {
						t.Errorf("match changed: %+v %v", got, err)
					}
				}
			})
			t.Run("a suggestion a token made cannot be rejected by automation", func(t *testing.T) {
				f := newFixture(t)
				rent, o := f.tx("RENT 2026-10"), f.occurrence(f.commitment("800.00", "EUR", 2).ID, "2026-10")
				s, err := f.ms.Create(f.ctx, tok, matches.New{TransactionID: rent.ID, OccurrenceID: o.ID, Amount: money.MustParse("800.00", "EUR"), Origin: matches.OriginModel, State: matches.StateSuggested})
				if err != nil {
					t.Fatal(err)
				}
				if err := undo["reject"](f, s.Match, sys); !errors.Is(err, matches.ErrActorUndo) {
					t.Errorf("reject a token's suggestion: %v", err)
				}
			})
		})
	}
}

func automationGuard(t *testing.T, sys audit.Actor) {
	create := func(f *fixture, tx banking.StoredTransaction, o occurrences.Occurrence, amount string, origin matches.Origin, st matches.State) (matches.Change, error) {
		return f.ms.Create(f.ctx, sys, matches.New{TransactionID: tx.ID, OccurrenceID: o.ID, Amount: money.MustParse(amount, "EUR"), Origin: origin, State: st})
	}
	setup := func(t *testing.T) (*fixture, banking.StoredTransaction, occurrences.Occurrence, occurrences.Occurrence) {
		f := newFixture(t)
		rent := f.tx("RENT 2026-10") // -800.00
		o := f.occurrence(f.commitment("800.00", "EUR", 2).ID, "2026-10")
		oo := f.occurrence(f.commitment("500.00", "EUR", 3).ID, "2026-10")
		return f, rent, o, oo
	}
	t.Run("confirm a partial suggestion (a)", func(t *testing.T) {
		f, rent, o, oo := setup(t)
		s, err := create(f, rent, o, "300.00", matches.OriginAutoRule, matches.StateSuggested)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.match(rent, oo, "500.00", matches.OriginModel, matches.StateSuggested); err != nil {
			t.Fatal(err)
		}
		if _, err := f.ms.Confirm(f.ctx, sys, s.Match.ID, s.Match.Version, false, nil); !errors.Is(err, matches.ErrAutoNotOneToOne) {
			t.Errorf("confirm beside another open match: %v", err)
		}
	})
	t.Run("confirm a partial suggestion alone", func(t *testing.T) {
		f, rent, o, _ := setup(t)
		s, _ := create(f, rent, o, "300.00", matches.OriginAutoRule, matches.StateSuggested)
		if _, err := f.ms.Confirm(f.ctx, sys, s.Match.ID, s.Match.Version, false, nil); !errors.Is(err, matches.ErrAutoNotOneToOne) {
			t.Errorf("partial: %v", err)
		}
	})
	t.Run("confirm an exact suggestion", func(t *testing.T) {
		f, rent, o, _ := setup(t)
		s, _ := create(f, rent, o, "800.00", matches.OriginAutoRule, matches.StateSuggested)
		if ch, err := f.ms.Confirm(f.ctx, sys, s.Match.ID, s.Match.Version, false, nil); err != nil || ch.Match.State != matches.StateConfirmed {
			t.Errorf("1:1 confirm: %+v %v", ch.Match, err)
		}
	})
	t.Run("confirm an exact model suggestion (D9)", func(t *testing.T) {
		f, rent, o, _ := setup(t)
		s, _ := create(f, rent, o, "800.00", matches.OriginModel, matches.StateSuggested)
		if _, err := f.ms.Confirm(f.ctx, sys, s.Match.ID, s.Match.Version, false, nil); !errors.Is(err, matches.ErrAutoModel) {
			t.Errorf("model confirm: %v", err)
		}
	})
	t.Run("correct (b)", func(t *testing.T) {
		f, rent, o, _ := setup(t)
		c, err := create(f, rent, o, "800.00", matches.OriginAutoRule, matches.StateConfirmed)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.ms.Correct(f.ctx, sys, c.Match.ID, c.Match.Version, money.MustParse("5.00", "EUR"), false, nil); !errors.Is(err, matches.ErrActorCorrect) {
			t.Errorf("system correct: %v", err)
		}
		if _, err := f.ms.Correct(f.ctx, alice, c.Match.ID, c.Match.Version, money.MustParse("5.00", "EUR"), false, nil); err != nil {
			t.Errorf("user correct: %v", err)
		}
	})
	t.Run("origin user (c)", func(t *testing.T) {
		f, rent, o, _ := setup(t)
		if _, err := create(f, rent, o, "300.00", matches.OriginUser, matches.StateConfirmed); !errors.Is(err, matches.ErrActorOrigin) {
			t.Errorf("system origin user: %v", err)
		}
		tok := audit.Actor{Kind: audit.ActorToken, UserID: alice.UserID, TokenID: "01JD3Y4C8V9M2Q7W5R6T0XZBNT"}
		if _, err := f.ms.Create(f.ctx, tok, matches.New{TransactionID: rent.ID, OccurrenceID: o.ID, Amount: money.MustParse("300.00", "EUR"),
			Origin: matches.OriginUser, State: matches.StateConfirmed}); err != nil {
			t.Errorf("token origin user: %v", err)
		}
	})
	t.Run("create confirmed", func(t *testing.T) {
		f, rent, o, _ := setup(t)
		if _, err := create(f, rent, o, "300.00", matches.OriginAutoRule, matches.StateConfirmed); !errors.Is(err, matches.ErrAutoNotOneToOne) {
			t.Errorf("system partial confirmation: %v", err)
		}
		if _, err := create(f, rent, o, "800.00", matches.OriginAutoRule, matches.StateConfirmed); err != nil {
			t.Errorf("system exact confirmation: %v", err)
		}
	})
	t.Run("overpayment never allowed", func(t *testing.T) {
		f, rent, o, _ := setup(t)
		if _, err := f.ms.Create(f.ctx, sys, matches.New{TransactionID: rent.ID, OccurrenceID: o.ID, Amount: money.MustParse("800.00", "EUR"),
			Origin: matches.OriginAutoRule, State: matches.StateConfirmed, AllowOverpayment: true}); !errors.Is(err, matches.ErrActorOverpayment) {
			t.Errorf("create with AllowOverpayment: %v", err)
		}
		s, err := create(f, rent, o, "800.00", matches.OriginModel, matches.StateSuggested)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.ms.Confirm(f.ctx, sys, s.Match.ID, s.Match.Version, true, nil); !errors.Is(err, matches.ErrActorOverpayment) {
			t.Errorf("confirm with allow: %v", err)
		}
	})
}
