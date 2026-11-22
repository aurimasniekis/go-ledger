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

func bankPaid(r monthview.Row, id string, on calendar.Date, amount money.Money) monthview.Row {
	r.Evidence = append(r.Evidence, payments.Evidence{Kind: payments.EvidenceBankTransaction, ID: id, OccurrenceID: r.Occurrence.ID, Amount: amount, Date: on})
	return r
}

func TestDetectorAmountChangeFromTheLatestMatchedBankPayment(t *testing.T) {
	e := newDetectEnv(t)
	sub := commitments.Commitment{ID: "k-sub", Fields: commitments.Fields{Amount: eur("10.00")}}
	e.cmts.list = []commitments.Commitment{sub}
	oct, nov := calendar.MustDate(2026, 10, 1), calendar.MustDate(2026, 11, 1)
	e.mon.rows[oct.YearMonth()] = []monthview.Row{bankPaid(row("o1", "k-sub", oct, "10.00", payments.StatusPaid), "b1", oct, eur("10.00"))}
	if rep := e.run(t); rep.Raised != 0 {
		t.Fatalf("a payment equal to the expected amount raised: %+v", rep)
	}
	// A price rise: the bank payment is above the expected amount.
	e.mon.rows[nov.YearMonth()] = []monthview.Row{bankPaid(row("o2", "k-sub", nov, "10.00", payments.StatusPaid), "b2", nov, eur("12.50"))}
	if rep := e.run(t); rep.Raised != 1 {
		t.Fatalf("%+v", rep)
	}
	a := e.sent[0]
	if a.Kind != alerts.KindAmountChanged || a.ObjectID != "k-sub" || a.Facts["expected_amount"] != "10.00" || a.Facts["paid_amount"] != "12.50" ||
		a.Facts["paid_currency"] != "EUR" || a.Facts["expected_currency"] != "EUR" || a.Facts["currency_differs"] != "" {
		t.Fatalf("%+v", a)
	}
	// A cheaper payment is any non-zero difference too: a new situation.
	e.mon.rows[nov.YearMonth()] = []monthview.Row{bankPaid(row("o2", "k-sub", nov, "10.00", payments.StatusPaid), "b2", nov, eur("9.99"))}
	if rep := e.run(t); rep.Raised != 1 || rep.Resolved != 1 {
		t.Fatalf("%+v", rep)
	}
	// The commitment is updated to what is being paid: resolved.
	e.cmts.list[0].Amount = eur("9.99")
	if rep := e.run(t); rep.Resolved != 1 || e.state(t, alerts.KindAmountChanged)[alerts.StateOpen] != 0 {
		t.Fatalf("%+v", rep)
	}
	// Back to a differing payment, then a later matched payment equals the expected amount: resolved.
	e.cmts.list[0].Amount = eur("10.00")
	if rep := e.run(t); rep.Raised != 1 {
		t.Fatalf("%+v", rep)
	}
	dec := calendar.MustDate(2026, 11, 15)
	e.mon.rows[nov.YearMonth()] = append(e.mon.rows[nov.YearMonth()], bankPaid(row("o3", "k-sub", dec, "10.00", payments.StatusPaid), "b3", dec, eur("10.00")))
	if rep := e.run(t); rep.Resolved != 1 || e.state(t, alerts.KindAmountChanged)[alerts.StateOpen] != 0 {
		t.Fatalf("%+v", rep)
	}
}

func TestDetectorAmountChangeIgnoresCancelledManualAndPartialBaselines(t *testing.T) {
	e := newDetectEnv(t)
	e.cmts.list = []commitments.Commitment{{ID: "k-x", Fields: commitments.Fields{Amount: eur("10.00")}}}
	oct, nov := calendar.MustDate(2026, 10, 1), calendar.MustDate(2026, 11, 1)
	e.mon.rows[oct.YearMonth()] = []monthview.Row{bankPaid(row("o1", "k-x", oct, "10.00", payments.StatusPaid), "b1", oct, eur("10.00"))}
	// A newer cancelled occurrence with a differing bank payment is never the baseline.
	e.mon.rows[nov.YearMonth()] = []monthview.Row{
		bankPaid(row("o2", "k-x", nov, "10.00", payments.StatusCancelled), "b2", nov, eur("99.00")),
		bankPaid(row("o3", "k-x", calendar.MustDate(2026, 11, 2), "10.00", payments.StatusPartiallyPaid), "b3", calendar.MustDate(2026, 11, 2), eur("4.00")),
	}
	manual := row("o4", "k-x", calendar.MustDate(2026, 11, 3), "10.00", payments.StatusPaid)
	manual.Evidence = []payments.Evidence{{Kind: payments.EvidenceManual, ID: "m1", OccurrenceID: "o4", Amount: eur("77.00"), Date: calendar.MustDate(2026, 11, 3)}}
	e.mon.rows[nov.YearMonth()] = append(e.mon.rows[nov.YearMonth()], manual)
	if rep := e.run(t); rep.Raised != 0 {
		t.Fatalf("a cancelled, partial or manual payment was a baseline: %+v", rep)
	}
}

func TestDetectorAmountChangeComparesNumericallyAcrossScales(t *testing.T) {
	e := newDetectEnv(t)
	e.cmts.list = []commitments.Commitment{{ID: "k-scale", Fields: commitments.Fields{Amount: eur("15")}}}
	oct, nov := calendar.MustDate(2026, 10, 1), calendar.MustDate(2026, 11, 1)
	// The commitment is entered as "15" and the bank reports "15.00": the same amount.
	e.mon.rows[oct.YearMonth()] = []monthview.Row{bankPaid(row("o1", "k-scale", oct, "15.00", payments.StatusPaid), "b1", oct, eur("15.00"))}
	if rep := e.run(t); rep.Raised != 0 {
		t.Fatalf("15 and 15.00 raised an alert: %+v", rep)
	}
	e.mon.rows[oct.YearMonth()] = nil
	// A real change: 12.50 paid for an occurrence expected at "15".
	e.mon.rows[nov.YearMonth()] = []monthview.Row{bankPaid(row("o2", "k-scale", nov, "15", payments.StatusPaid), "b2", nov, eur("12.50"))}
	if rep := e.run(t); rep.Raised != 1 || e.sent[0].Facts["expected_amount"] != "15" || e.sent[0].Facts["paid_amount"] != "12.50" {
		t.Fatalf("%+v", rep)
	}
	// The same situation with other scales (occurrence "15.00", bank "12.5") keeps its key: nothing is raised or resolved.
	e.mon.rows[nov.YearMonth()] = []monthview.Row{bankPaid(row("o2", "k-scale", nov, "15.00", payments.StatusPaid), "b2", nov, eur("12.5"))}
	if rep := e.run(t); rep.Raised != 0 || rep.Resolved != 0 || len(e.sent) != 1 {
		t.Fatalf("a scale change alone re-raised or resolved the alert: %+v sent %d", rep, len(e.sent))
	}
	// The commitment is updated to "12.5", which equals the paid 12.50 numerically: resolved.
	e.cmts.list[0].Amount = eur("12.5")
	if rep := e.run(t); rep.Resolved != 1 || e.state(t, alerts.KindAmountChanged)[alerts.StateOpen] != 0 {
		t.Fatalf("an update at another scale did not resolve: %+v", rep)
	}
}

func TestDetectorAmountChangeIgnoresMixedManualAndShortfallBaselines(t *testing.T) {
	e := newDetectEnv(t)
	e.cmts.list = []commitments.Commitment{{ID: "k-mix", Fields: commitments.Fields{Amount: eur("10.00")}}}
	nov := calendar.MustDate(2026, 11, 1)
	// Settled by a bank payment of 6.00 and a manual payment of 4.00: the bank piece alone is not the price.
	mixed := bankPaid(row("o1", "k-mix", nov, "10.00", payments.StatusPaid), "b1", nov, eur("6.00"))
	mixed.Evidence = append(mixed.Evidence, payments.Evidence{Kind: payments.EvidenceManual, ID: "m1", OccurrenceID: "o1", Amount: eur("4.00"), Date: nov})
	mixed.State.EvidenceCount, mixed.State.BankEvidenceCount, mixed.State.ManualEvidenceCount = 2, 1, 1
	e.mon.rows[nov.YearMonth()] = []monthview.Row{mixed}
	if rep := e.run(t); rep.Raised != 0 {
		t.Fatalf("a bank piece of a manually settled occurrence was a baseline: %+v", rep)
	}

	// Confirmed short within tolerance (9.50 of 10.00): accepted by the person, not a baseline.
	e.mon.rows[nov.YearMonth()] = nil
	oct := calendar.MustDate(2026, 10, 1)
	e.mon.rows[oct.YearMonth()] = []monthview.Row{bankPaid(row("o0", "k-mix", oct, "10.00", payments.StatusPaid), "b0", oct, eur("10.00"))}
	short := bankPaid(row("o2", "k-mix", nov, "10.00", payments.StatusPaid), "b2", nov, eur("9.50"))
	short.State.EvidenceCount, short.State.BankEvidenceCount, short.State.Shortfall = 1, 1, eur("0.50")
	e.mon.rows[nov.YearMonth()] = []monthview.Row{short}
	if rep := e.run(t); rep.Raised != 0 {
		t.Fatalf("a payment confirmed short within tolerance was a baseline: %+v", rep)
	}
	// The same payment without a confirmed shortfall is a change.
	short.State.Shortfall = money.Money{}
	e.mon.rows[nov.YearMonth()] = []monthview.Row{short}
	if rep := e.run(t); rep.Raised != 1 {
		t.Fatalf("a payment short without a tolerance confirmation was not a change: %+v", rep)
	}
}

func TestDetectorAmountChangeInAnotherCurrencyIsNotConverted(t *testing.T) {
	e := newDetectEnv(t)
	e.cmts.list = []commitments.Commitment{{ID: "k-fx", Fields: commitments.Fields{Amount: eur("10.00")}}}
	oct := calendar.MustDate(2026, 10, 1)
	e.mon.rows[oct.YearMonth()] = []monthview.Row{bankPaid(row("o1", "k-fx", oct, "10.00", payments.StatusPaid), "b1", oct, money.MustParse("10.00", "USD"))}
	if rep := e.run(t); rep.Raised != 1 {
		t.Fatalf("%+v", rep)
	}
	a := e.sent[0]
	if a.Facts["currency_differs"] != "true" || a.Facts["paid_currency"] != "USD" || a.Facts["expected_currency"] != "EUR" || a.Facts["paid_amount"] != "10.00" {
		t.Fatalf("%+v", a)
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
