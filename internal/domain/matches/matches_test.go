package matches_test

import (
	"errors"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/matches"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/payments"
)

func eur(s string) money.Money { return money.MustParse(s, "EUR") }

func booked(amount string) matches.Transaction {
	return matches.Transaction{ID: "t", Status: banking.StatusBooked, Amount: eur(amount), Date: calendar.MustDate(2026, time.October, 2)}
}

func occ(amount string) occurrences.Occurrence {
	return occurrences.Occurrence{ID: "o", Amount: eur(amount), DueDate: calendar.MustDate(2026, time.October, 2)}
}

func TestNewValidate(t *testing.T) {
	ok := matches.New{TransactionID: "t", OccurrenceID: "o", Amount: eur("1.00"), Origin: matches.OriginUser, State: matches.StateConfirmed}
	if errs := ok.Validate(); len(errs) != 0 {
		t.Fatalf("valid: %v", errs)
	}
	blank := " "
	for name, c := range map[string]struct {
		n     matches.New
		field string
	}{
		"no transaction":  {func() matches.New { n := ok; n.TransactionID = ""; return n }(), "transaction_id"},
		"zero amount":     {func() matches.New { n := ok; n.Amount = eur("0"); return n }(), "amount.amount"},
		"negative amount": {func() matches.New { n := ok; n.Amount = eur("-1"); return n }(), "amount.amount"},
		"no amount":       {func() matches.New { n := ok; n.Amount = money.Money{}; return n }(), "amount"},
		"bad origin":      {func() matches.New { n := ok; n.Origin = "magic"; return n }(), "origin"},
		"model confirmed": {func() matches.New { n := ok; n.Origin = matches.OriginModel; return n }(), "state"},
		"user suggestion": {func() matches.New { n := ok; n.State = matches.StateSuggested; return n }(), "state"},
		"created reversed": {func() matches.New {
			n := ok
			n.Origin = matches.OriginAutoRule
			n.State = matches.StateReversed
			return n
		}(), "state"},
		"blank reason": {func() matches.New { n := ok; n.Reason = &blank; return n }(), "reason"},
	} {
		errs := c.n.Validate()
		if len(errs) != 1 || errs[0].Field != c.field {
			t.Errorf("%s: %v", name, errs)
		}
	}
}

func TestCheckTargetAndConfirm(t *testing.T) {
	if err := matches.CheckTarget(booked("12.00"), occ("12.00"), eur("12.00")); !errors.Is(err, matches.ErrNotDebit) {
		t.Errorf("credit: %v", err)
	}
	if err := matches.CheckTarget(booked("-12.00"), occurrences.Occurrence{Amount: money.MustParse("12.00", "USD")}, eur("12.00")); !errors.Is(err, matches.ErrCurrency) {
		t.Errorf("currency: %v", err)
	}
	if err := matches.CheckTarget(booked("-12.00"), occ("20.00"), eur("12.01")); !errors.Is(err, matches.ErrOverAllocated) {
		t.Errorf("more than the transaction: %v", err)
	}
	pending := booked("-12.00")
	pending.Status = banking.StatusPending
	if err := matches.CheckConfirm(pending, nil, eur("12.00"), occ("12.00"), payments.Facts{}, false); !errors.Is(err, matches.ErrNotCountable) {
		t.Errorf("pending: %v", err)
	}
	gone := booked("-12.00")
	gone.NoLongerReportedAt = time.Now()
	if err := matches.CheckConfirm(gone, nil, eur("12.00"), occ("12.00"), payments.Facts{}, false); !errors.Is(err, matches.ErrNotCountable) {
		t.Errorf("no longer reported: %v", err)
	}
	// Exact decimals: 7.005 + 5.000 is more than 12.00.
	if err := matches.CheckConfirm(booked("-12.00"), []money.Money{eur("7.005")}, eur("5.000"), occ("20.00"), payments.Facts{}, false); !errors.Is(err, matches.ErrOverAllocated) {
		t.Errorf("allocations past the amount: %v", err)
	}
	if err := matches.CheckConfirm(booked("-12.00"), []money.Money{eur("7.00")}, eur("5.00"), occ("20.00"), payments.Facts{}, false); err != nil {
		t.Errorf("exactly the amount: %v", err)
	}
	facts := payments.Facts{Evidence: []payments.Evidence{{Kind: payments.EvidenceManual, Amount: eur("8.00")}, {Kind: payments.EvidenceManual, Amount: money.MustParse("99.00", "USD")}}}
	if err := matches.CheckConfirm(booked("-12.00"), nil, eur("4.01"), occ("12.00"), facts, false); !errors.Is(err, matches.ErrOverpaid) {
		t.Errorf("overpaid: %v", err)
	}
	if err := matches.CheckConfirm(booked("-12.00"), nil, eur("4.00"), occ("12.00"), facts, false); err != nil {
		t.Errorf("exactly paid (other currencies ignored): %v", err)
	}
	if err := matches.CheckConfirm(booked("-12.00"), nil, eur("12.00"), occ("12.00"), facts, true); err != nil {
		t.Errorf("allowed overpayment: %v", err)
	}
}

func TestEvidenceAndAudit(t *testing.T) {
	m := matches.Match{ID: "01JD3Y4C8V9M2Q7W5R6T0XZBNA", OccurrenceID: "01JD3Y4C8V9M2Q7W5R6T0XZBNB", TransactionID: "01JD3Y4C8V9M2Q7W5R6T0XZBNC",
		Amount: eur("5.00"), Origin: matches.OriginAutoRule, State: matches.StateConfirmed, Transaction: booked("-5.00")}
	if !m.CountsAsEvidence() {
		t.Error("confirmed on a booked transaction does not count")
	}
	m.Transaction.SupersededBy = "x"
	if m.CountsAsEvidence() {
		t.Error("a superseded transaction counts")
	}
	m.State = matches.StateSuggested
	m.Transaction.SupersededBy = ""
	if m.CountsAsEvidence() {
		t.Error("a suggestion counts")
	}
	for _, a := range []matches.Action{matches.ActionSuggested, matches.ActionConfirmed, matches.ActionRejected, matches.ActionUnmatched, matches.ActionCorrected, matches.ActionReversed} {
		e := matches.AuditEvent(a, audit.System, m, "", time.Now())
		if err := audit.Validate(e); err != nil {
			t.Errorf("%s: %v", a, err)
		}
		if e.Details["from"] != "none" || e.Details["currency"] != "EUR" {
			t.Errorf("%s details: %v", a, e.Details)
		}
		for _, v := range e.Details {
			if v == "5.00" {
				t.Errorf("%s carries the amount", a)
			}
		}
	}
	for _, s := range matches.States() {
		if s.Final() == s.Open() {
			t.Errorf("%s is both or neither open and final", s)
		}
	}
}

func TestCheckTargetScale(t *testing.T) {
	if err := matches.CheckTarget(booked("-3.50"), occ("3.50"), eur("3.499")); !errors.Is(err, matches.ErrScale) {
		t.Errorf("finer than the transaction: %v", err)
	}
	if err := matches.CheckTarget(booked("-3.50"), occ("3.50"), eur("3.4")); err != nil {
		t.Errorf("coarser is fine: %v", err)
	}
}

func TestCheckAuto(t *testing.T) {
	none := money.Money{}
	if err := matches.CheckAuto(booked("-12.00"), 0, eur("12.00"), occ("12.00"), none); err != nil {
		t.Errorf("1:1 exact: %v", err)
	}
	if err := matches.CheckAuto(booked("-12.00"), 0, eur("7.00"), occ("7.00"), none); !errors.Is(err, matches.ErrAutoNotOneToOne) {
		t.Errorf("partial: %v", err)
	}
	if err := matches.CheckAuto(booked("-12.00"), 1, eur("12.00"), occ("12.00"), none); !errors.Is(err, matches.ErrAutoNotOneToOne) {
		t.Errorf("another open allocation: %v", err)
	}
	if err := matches.CheckAuto(booked("-12.00"), 0, eur("12.00"), occ("12.50"), none); !errors.Is(err, matches.ErrAutoAmount) {
		t.Errorf("amount differs: %v", err)
	}
	if err := matches.CheckAuto(booked("-12.00"), 0, eur("12.00"), occ("12.50"), eur("0.50")); err != nil {
		t.Errorf("within tolerance: %v", err)
	}
	if err := matches.CheckAuto(booked("-12.00"), 0, eur("12.00"), occ("12.51"), eur("0.50")); !errors.Is(err, matches.ErrAutoAmount) {
		t.Errorf("beyond tolerance: %v", err)
	}
	// The transaction must be dated within 3 days of the due date.
	late := booked("-12.00")
	late.Date = calendar.MustDate(2026, time.October, 5)
	if err := matches.CheckAuto(late, 0, eur("12.00"), occ("12.00"), none); err != nil {
		t.Errorf("3 days late: %v", err)
	}
	late.Date = calendar.MustDate(2026, time.October, 6)
	if err := matches.CheckAuto(late, 0, eur("12.00"), occ("12.00"), none); !errors.Is(err, matches.ErrAutoDate) {
		t.Errorf("4 days late: %v", err)
	}
	early := booked("-12.00")
	early.Date = calendar.MustDate(2026, time.September, 28)
	if err := matches.CheckAuto(early, 0, eur("12.00"), occ("12.00"), none); !errors.Is(err, matches.ErrAutoDate) {
		t.Errorf("4 days early: %v", err)
	}
}

func TestNeedsReview(t *testing.T) {
	mk := func(state matches.State, mut func(*matches.Transaction)) matches.Match {
		tx := booked("-5.00")
		mut(&tx)
		return matches.Match{State: state, Transaction: tx}
	}
	gone := time.Now()
	for name, c := range map[string]struct {
		m    matches.Match
		want bool
	}{
		"confirmed and countable":       {mk(matches.StateConfirmed, func(*matches.Transaction) {}), false},
		"confirmed, no longer reported": {mk(matches.StateConfirmed, func(x *matches.Transaction) { x.NoLongerReportedAt = gone }), true},
		"suggestion on a live pending":  {mk(matches.StateSuggested, func(x *matches.Transaction) { x.Status = banking.StatusPending }), false},
		"suggestion on a vanished pending": {mk(matches.StateSuggested, func(x *matches.Transaction) {
			x.Status = banking.StatusPending
			x.NoLongerReportedAt = gone
		}), true},
		"suggestion on a superseded pending": {mk(matches.StateSuggested, func(x *matches.Transaction) {
			x.Status = banking.StatusPending
			x.SupersededBy = "01JD3Y4C8V9M2Q7W5R6T0XZBNC"
		}), true},
		"rejected on a vanished one": {mk(matches.StateRejected, func(x *matches.Transaction) { x.NoLongerReportedAt = gone }), false},
	} {
		if got := c.m.NeedsReview(); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}
