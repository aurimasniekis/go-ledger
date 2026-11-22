package decisions_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/alerts"
	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/monthview"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/payments"
	alertstore "github.com/aurimasniekis/go-ledger/internal/store/alerts"
)

// The alert detectors (L6, ADR-0065) against the real alert store and fake
// readers: no network, no bank, no model.

type fakeConns struct{ list []banking.Connection }

func (f *fakeConns) Connections(context.Context) ([]banking.Connection, error) { return f.list, nil }

type fakeCommitments struct{ list []commitments.Commitment }

func (f *fakeCommitments) List(context.Context, commitments.ListFilter) ([]commitments.Commitment, error) {
	return f.list, nil
}

type fakeMonths struct {
	rows map[calendar.YearMonth][]monthview.Row
	err  error
}

func (f *fakeMonths) Month(_ context.Context, m calendar.YearMonth, _ calendar.Date) (monthview.Summary, error) {
	if f.err != nil {
		return monthview.Summary{}, f.err
	}
	return monthview.Summary{Month: m, Rows: f.rows[m]}, nil
}

func eur(s string) money.Money { return money.MustParse(s, "EUR") }

func row(id, commitment string, due calendar.Date, amount string, status payments.Status) monthview.Row {
	out := eur("0.00")
	if status == payments.StatusOverdue {
		out = eur(amount)
	}
	return monthview.Row{Occurrence: occurrences.Occurrence{ID: id, CommitmentID: commitment, DueDate: due, Amount: eur(amount)},
		State: payments.State{Status: status, Outstanding: out}}
}

type detectEnv struct {
	*fixture
	det   *alerts.Detector
	st    *alertstore.Store
	conns *fakeConns
	cmts  *fakeCommitments
	mon   *fakeMonths
	sent  []alerts.Alert
}

var today = calendar.MustDate(2026, 11, 22)

func newDetectEnv(t *testing.T) *detectEnv {
	t.Helper()
	e := &detectEnv{fixture: newFixture(t, false), conns: &fakeConns{}, cmts: &fakeCommitments{}, mon: &fakeMonths{rows: map[calendar.YearMonth][]monthview.Row{}}}
	e.st = alertstore.New(e.db, e.rec, func() time.Time { return t0 }, nil)
	ch := alerts.Channel{Name: "test", Notify: func(_ context.Context, a alerts.Alert) error { e.sent = append(e.sent, a); return nil }}
	e.det = &alerts.Detector{Alerts: alerts.New(e.st, nil, ch), Store: e.st, Connections: e.conns, Commitments: e.cmts, Months: e.mon,
		Zone: calendar.MustLoadZone("UTC"), Today: func() (calendar.Date, error) { return today, nil }, ConsentHorizonDays: 14}
	return e
}

func (e *detectEnv) run(t *testing.T) alerts.Report {
	t.Helper()
	rep, err := e.det.Run(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func (e *detectEnv) state(t *testing.T, kind alerts.Kind) map[alerts.State]int {
	t.Helper()
	all, err := e.st.List(e.ctx, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	out := map[alerts.State]int{}
	for _, a := range all {
		if a.Kind == kind {
			out[a.State]++
		}
	}
	return out
}

func conn(id string, until time.Time, st banking.ConnectionStatus) banking.Connection {
	return banking.Connection{ID: banking.ConnectionID(id), Status: st, ConsentValidUntil: until}
}

func TestDetectorConsentExpiryRaisesOnceAndResolvesOnReconnect(t *testing.T) {
	e := newDetectEnv(t)
	soon := time.Date(2026, 11, 30, 10, 0, 0, 0, time.UTC)
	e.conns.list = []banking.Connection{
		conn("c-soon", soon, banking.ConnectionActive),
		conn("c-far", soon.AddDate(0, 3, 0), banking.ConnectionActive),
		conn("c-gone", soon, banking.ConnectionDisconnected), // no consent to expire
		conn("c-pending", time.Time{}, banking.ConnectionPending),
	}
	if rep := e.run(t); rep.Raised != 1 || rep.Resolved != 0 {
		t.Fatalf("first run %+v", rep)
	}
	if rep := e.run(t); rep.Raised != 0 || rep.Resolved != 0 || len(e.sent) != 1 {
		t.Fatalf("a second run raised or sent again: %+v sent %d", rep, len(e.sent))
	}
	if e.sent[0].ObjectID != "c-soon" || e.sent[0].Facts["expires_on"] != "2026-11-30" {
		t.Fatalf("%+v", e.sent[0])
	}
	// The consent is renewed: the condition stops holding, the alert is resolved.
	e.conns.list[0].ConsentValidUntil = soon.AddDate(0, 3, 0)
	if rep := e.run(t); rep.Resolved != 1 || e.state(t, alerts.KindConsentExpiring)[alerts.StateResolved] != 1 {
		t.Fatalf("%+v", rep)
	}
	// A consent that already ended is raised as well.
	e.conns.list[0].ConsentValidUntil = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	e.conns.list[0].Status = banking.ConnectionReconnectRequired
	if rep := e.run(t); rep.Raised != 1 {
		t.Fatalf("%+v", rep)
	}
}

func TestDetectorOverdueRaisesAndResolvesWhenPaidOrCancelled(t *testing.T) {
	e := newDetectEnv(t)
	oct := calendar.MustDate(2026, 10, 5)
	m := oct.YearMonth()
	e.mon.rows[m] = []monthview.Row{
		row("o-late", "k1", oct, "40.00", payments.StatusOverdue),
		row("o-paid", "k2", oct, "15.00", payments.StatusPaid),
		row("o-upcoming", "k3", calendar.MustDate(2026, 11, 28), "9.00", payments.StatusUpcoming),
	}
	if rep := e.run(t); rep.Raised != 1 {
		t.Fatalf("%+v", rep)
	}
	got := e.sent[0]
	if got.ObjectID != "o-late" || got.Facts["outstanding"] != "40.00" || got.Facts["currency"] != "EUR" {
		t.Fatalf("%+v", got)
	}
	// A payment is matched: the row is paid and the alert resolves.
	e.mon.rows[m][0] = row("o-late", "k1", oct, "40.00", payments.StatusPaid)
	if rep := e.run(t); rep.Resolved != 1 || e.state(t, alerts.KindUnmatchedOverdue)[alerts.StateResolved] != 1 {
		t.Fatalf("%+v", rep)
	}
	// It goes unmatched again (a person unmatched it): a resolved key may return.
	e.mon.rows[m][0] = row("o-late", "k1", oct, "40.00", payments.StatusOverdue)
	if rep := e.run(t); rep.Raised != 1 || len(e.sent) != 2 {
		t.Fatalf("%+v", rep)
	}
	// The occurrence vanishes from the month (cancelled by an edit): resolved.
	e.mon.rows[m] = nil
	if rep := e.run(t); rep.Resolved != 1 {
		t.Fatalf("%+v", rep)
	}
}

func TestDetectorAmountChangeFromTheLatestPastOccurrence(t *testing.T) {
	e := newDetectEnv(t)
	rent := commitments.Commitment{ID: "k-rent", Fields: commitments.Fields{Amount: eur("500.00")}}
	e.cmts.list = []commitments.Commitment{rent}
	oct, nov := calendar.MustDate(2026, 10, 1), calendar.MustDate(2026, 11, 1)
	e.mon.rows[oct.YearMonth()] = []monthview.Row{row("o1", "k-rent", oct, "500.00", payments.StatusPaid)}
	e.mon.rows[nov.YearMonth()] = []monthview.Row{row("o2", "k-rent", nov, "500.00", payments.StatusPaid)}
	if rep := e.run(t); rep.Raised != 0 {
		t.Fatalf("an unchanged amount raised: %+v", rep)
	}
	// The amount is edited: past occurrences keep what they were generated with.
	e.cmts.list[0].Amount = eur("550.00")
	if rep := e.run(t); rep.Raised != 1 {
		t.Fatalf("%+v", rep)
	}
	a := e.sent[0]
	if a.Kind != alerts.KindAmountChanged || a.Facts["from_amount"] != "500.00" || a.Facts["to_amount"] != "550.00" || a.Facts["to_currency"] != "EUR" {
		t.Fatalf("%+v", a)
	}
	// Another edit is a new situation: the old alert is resolved, a new one raised.
	e.cmts.list[0].Amount = eur("560.00")
	if rep := e.run(t); rep.Raised != 1 || rep.Resolved != 1 {
		t.Fatalf("%+v", rep)
	}
	// Once an occurrence with the new amount has fallen due, nothing differs: resolved.
	e.mon.rows[nov.YearMonth()] = []monthview.Row{row("o2", "k-rent", calendar.MustDate(2026, 11, 20), "560.00", payments.StatusPaid)}
	if rep := e.run(t); rep.Resolved != 1 || e.state(t, alerts.KindAmountChanged)[alerts.StateOpen] != 0 {
		t.Fatalf("%+v", rep)
	}
	// A different currency is shown as itself, never converted.
	e.cmts.list[0].Amount = money.MustParse("560.00", "USD")
	if rep := e.run(t); rep.Raised != 1 || e.sent[len(e.sent)-1].Facts["to_currency"] != "USD" || e.sent[len(e.sent)-1].Facts["from_currency"] != "EUR" {
		t.Fatalf("%+v", rep)
	}
}

func TestDetectorDismissalSticksAndIsNeverResolved(t *testing.T) {
	e := newDetectEnv(t)
	soon := time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC)
	e.conns.list = []banking.Connection{conn("c1", soon, banking.ConnectionActive)}
	e.run(t)
	open, _ := e.st.List(e.ctx, alerts.StateOpen, 10)
	if len(open) != 1 {
		t.Fatalf("open %d", len(open))
	}
	if _, err := e.det.Alerts.Dismiss(e.ctx, audit.Actor{Kind: audit.ActorUser, UserID: "01JD3Y4C8V9M2Q7W5R6T0XZBNZ"}, open[0].ID); err != nil {
		t.Fatal(err)
	}
	// The condition still holds: nothing is raised or sent again.
	if rep := e.run(t); rep.Raised != 0 || len(e.sent) != 1 {
		t.Fatalf("a dismissed alert came back: %+v", rep)
	}
	// It stops holding: the dismissed alert is left as the person set it.
	e.conns.list[0].ConsentValidUntil = soon.AddDate(0, 3, 0)
	if rep := e.run(t); rep.Resolved != 0 {
		t.Fatalf("%+v", rep)
	}
	if s := e.state(t, alerts.KindConsentExpiring); s[alerts.StateDismissed] != 1 || s[alerts.StateResolved] != 0 {
		t.Fatalf("%v", s)
	}
}

func TestDetectorReadFailureResolvesNothing(t *testing.T) {
	e := newDetectEnv(t)
	oct := calendar.MustDate(2026, 10, 5)
	e.mon.rows[oct.YearMonth()] = []monthview.Row{row("o-late", "k1", oct, "40.00", payments.StatusOverdue)}
	e.run(t)
	e.mon.err = errors.New("database busy")
	e.mon.rows[oct.YearMonth()] = nil
	if _, err := e.det.Run(e.ctx); err == nil {
		t.Fatal("a failed read was not reported")
	}
	if e.state(t, alerts.KindUnmatchedOverdue)[alerts.StateOpen] != 1 {
		t.Fatal("an alert was resolved while its data could not be read")
	}
}

func TestDetectorOldOverdueOutsideTheWindowIsLeftAlone(t *testing.T) {
	e := newDetectEnv(t)
	old := calendar.MustDate(2025, 1, 5) // before the lookback window
	cond, _ := alerts.UnmatchedOverdue("o-old", old, today, eur("10.00"))
	if _, err := e.det.Alerts.Raise(e.ctx, audit.System, cond); err != nil {
		t.Fatal(err)
	}
	if rep := e.run(t); rep.Resolved != 0 || e.state(t, alerts.KindUnmatchedOverdue)[alerts.StateOpen] != 1 {
		t.Fatalf("an alert the run could not see was resolved: %+v", rep)
	}
}
