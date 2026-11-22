package alerts

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/monthview"
	"github.com/aurimasniekis/go-ledger/internal/domain/payments"
)

// Lookback is how many months before the current one the detectors read
// occurrences from. An alert about an occurrence older than that is neither
// raised nor resolved by a run: it stays as it is.
const Lookback = 13

// openLimit is the most open alerts one run reconciles (the store lists at
// most 500). A single person's Ledger stays far below it.
const openLimit = 500

// ConnectionLister lists bank connections; the bank-facts store implements it.
type ConnectionLister interface {
	Connections(ctx context.Context) ([]banking.Connection, error)
}

// CommitmentLister pages commitments; the commitment store implements it.
type CommitmentLister interface {
	List(ctx context.Context, f commitments.ListFilter) ([]commitments.Commitment, error)
}

// Months reads one month of occurrences with their derived payment state;
// [monthview.Service] implements it.
type Months interface {
	Month(ctx context.Context, month calendar.YearMonth, today calendar.Date) (monthview.Summary, error)
}

// Detector scans stored data for the three alert conditions (ADR-0065, D13),
// raises what holds and resolves what stopped holding. It reads only; it
// never changes a bank fact, a commitment or a payment.
//
// A dismissed alert is never touched: a person's dismissal sticks (ADR-0065
// section 7), so a condition that still holds is not raised again, and one
// that stopped holding stays dismissed.
type Detector struct {
	Alerts      *Service
	Store       Store
	Connections ConnectionLister
	Commitments CommitmentLister
	Months      Months
	// Zone places a consent expiry instant on a date; Now is the clock.
	Zone calendar.Zone
	// Today returns today in the reporting zone.
	Today func() (calendar.Date, error)
	// ConsentHorizonDays is how many days before a consent ends the alert is
	// raised (an ended consent is always raised).
	ConsentHorizonDays int
}

// Report counts what one run did.
type Report struct {
	Raised   int
	Resolved int
	// Delivery lists channel failures and wording failures of newly raised
	// alerts; the alerts themselves stand.
	Delivery []error
}

// Run runs every detector once. A detector that cannot read its data does not
// raise or resolve anything of its kind (so a read failure never resolves an
// alert wrongly); its error is returned after the others have run.
func (d *Detector) Run(ctx context.Context) (Report, error) {
	today, err := d.Today()
	if err != nil {
		return Report{}, fmt.Errorf("alerts: resolving today: %w", err)
	}
	var rep Report
	var errs []error
	if want, err := d.consent(ctx, today); err != nil {
		errs = append(errs, fmt.Errorf("consent expiry: %w", err))
	} else if err := d.reconcile(ctx, KindConsentExpiring, want, nil, &rep); err != nil {
		errs = append(errs, err)
	}
	if overdue, changed, err := d.occurrenceBased(ctx, today); err != nil {
		errs = append(errs, fmt.Errorf("occurrences: %w", err))
	} else {
		first, _ := today.YearMonth().AddMonths(-Lookback)
		inWindow := func(a Alert) bool {
			due, err := calendar.ParseDate(a.Facts["due_date"])
			return err == nil && due.YearMonth().Compare(first) >= 0
		}
		if err := d.reconcile(ctx, KindUnmatchedOverdue, overdue, inWindow, &rep); err != nil {
			errs = append(errs, err)
		}
		if err := d.reconcile(ctx, KindAmountChanged, changed, nil, &rep); err != nil {
			errs = append(errs, err)
		}
	}
	return rep, errors.Join(errs...)
}

// consent builds the consent-expiry conditions of the connections that hold a
// consent (active, or needing a new one), keyed by alert key.
func (d *Detector) consent(ctx context.Context, today calendar.Date) (map[string]Condition, error) {
	conns, err := d.Connections.Connections(ctx)
	if err != nil {
		return nil, err
	}
	want := map[string]Condition{}
	for _, c := range conns {
		if c.Status != banking.ConnectionActive && c.Status != banking.ConnectionReconnectRequired {
			continue
		}
		if c.ConsentValidUntil.IsZero() {
			continue
		}
		expires, err := d.Zone.DateOf(c.ConsentValidUntil)
		if err != nil {
			continue
		}
		if cond, ok := ConsentExpiring(string(c.ID), expires, today, d.ConsentHorizonDays); ok {
			want[cond.Key] = cond
		}
	}
	return want, nil
}

// occurrenceBased reads the occurrences of the lookback window once and builds
// the overdue and the amount-change conditions from them.
//
// A commitment's amount changed when the most recent matched bank payment of
// one of its past, non-cancelled occurrences differs from that occurrence's
// expected amount (a price rise, say) and the commitment's current amount does
// not already equal what was paid. It stops holding when the commitment is
// updated to match or a later matched payment equals the expected amount.
// Manual payments are not bank payments, a partly paid occurrence is not a
// price change, and an occurrence whose bank match was confirmed short within
// the commitment's tolerance is a gap the person accepted (decision
// C-L6e-amount-change addendum). None of them is a baseline. Amounts compare
// numerically. Nothing is converted: a different currency is reported as a
// currency difference.
func (d *Detector) occurrenceBased(ctx context.Context, today calendar.Date) (overdue, changed map[string]Condition, err error) {
	overdue, changed = map[string]Condition{}, map[string]Condition{}
	latest := map[string]paidRow{}
	for i := -Lookback; i <= 0; i++ {
		month, err := today.YearMonth().AddMonths(i)
		if err != nil {
			return nil, nil, err
		}
		sum, err := d.Months.Month(ctx, month, today)
		if err != nil {
			return nil, nil, fmt.Errorf("month %s: %w", month, err)
		}
		for _, r := range sum.Rows {
			o := r.Occurrence
			if r.State.Status == payments.StatusOverdue && r.State.Outstanding.Sign() > 0 {
				if cond, ok := UnmatchedOverdue(o.ID, o.DueDate, today, r.State.Outstanding); ok {
					overdue[cond.Key] = cond
				}
			}
			if o.DueDate.Before(today) && isBaseline(r) {
				if p, ok := latestBankPayment(r); ok {
					if prev, ok := latest[o.CommitmentID]; !ok || p.date.After(prev.date) || (p.date == prev.date && prev.row.Occurrence.DueDate.Before(o.DueDate)) {
						latest[o.CommitmentID] = p
					}
				}
			}
		}
	}
	after := ""
	for {
		page, err := d.Commitments.List(ctx, commitments.ListFilter{After: after, Limit: 200})
		if err != nil {
			return nil, nil, err
		}
		for _, c := range page {
			if r, ok := latest[c.ID]; ok {
				if r.paid.Equal(c.Amount) {
					continue // the commitment was updated to match
				}
				if cond, ok := AmountChanged(c.ID, r.row.Occurrence.ID, r.row.Occurrence.Amount, r.paid); ok {
					changed[cond.Key] = cond
				}
			}
		}
		if len(page) <= 200 {
			return overdue, changed, nil
		}
		after = page[199].ID
	}
}

// isBaseline reports whether an occurrence's bank payment may be the baseline
// of the amount-change alert: not cancelled, not partly paid, no manual
// payment among its evidence, no confirmed shortfall within tolerance, and
// no status that comes from an applied override (decision C-L6e-amount-override).
func isBaseline(r monthview.Row) bool {
	s := r.State
	return s.Status != payments.StatusCancelled && s.Status != payments.StatusPartiallyPaid &&
		s.ManualEvidenceCount == 0 && s.Shortfall.Sign() <= 0 && !s.OverrideApplied
}

// paidRow is the most recent matched bank payment of an occurrence: what was
// paid (the bank evidence in the currency of its newest piece) and when.
type paidRow struct {
	row  monthview.Row
	paid money.Money
	date calendar.Date
}

// latestBankPayment sums the occurrence's bank evidence in the currency of the
// newest piece; false when it has no bank evidence.
func latestBankPayment(r monthview.Row) (paidRow, bool) {
	var newest *payments.Evidence
	for i := range r.Evidence {
		ev := &r.Evidence[i]
		if ev.Kind == payments.EvidenceBankTransaction && ev.Amount.IsValid() && (newest == nil || ev.Date.After(newest.Date)) {
			newest = ev
		}
	}
	if newest == nil {
		return paidRow{}, false
	}
	sum := newest.Amount
	for i := range r.Evidence {
		ev := &r.Evidence[i]
		if ev != newest && ev.Kind == payments.EvidenceBankTransaction && ev.Amount.IsValid() && ev.Amount.Currency() == sum.Currency() {
			s, err := sum.Add(ev.Amount)
			if err != nil {
				return paidRow{}, false
			}
			sum = s
		}
	}
	return paidRow{row: r, paid: sum, date: newest.Date}, true
}

// reconcile raises every wanted condition and resolves the open alerts of the
// kind that are no longer wanted. inScope, when set, limits resolving to the
// alerts a run could see the condition for.
func (d *Detector) reconcile(ctx context.Context, kind Kind, want map[string]Condition, inScope func(Alert) bool, rep *Report) error {
	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var errs []error
	for _, k := range keys {
		res, err := d.Alerts.Raise(ctx, audit.System, want[k])
		if err != nil {
			errs = append(errs, fmt.Errorf("raising %s: %w", kind, err))
			continue
		}
		if res.Created {
			rep.Raised++
			if res.WordingErr != nil {
				rep.Delivery = append(rep.Delivery, res.WordingErr)
			}
			for _, e := range res.Delivery {
				rep.Delivery = append(rep.Delivery, e)
			}
		}
	}
	open, err := d.Store.List(ctx, StateOpen, openLimit)
	if err != nil {
		return errors.Join(append(errs, fmt.Errorf("listing open %s alerts: %w", kind, err))...)
	}
	for _, a := range open {
		if a.Kind != kind {
			continue
		}
		if _, still := want[a.Key]; still || (inScope != nil && !inScope(a)) {
			continue
		}
		if _, err := d.Alerts.Resolve(ctx, audit.System, a.ID); err != nil {
			if !errors.Is(err, ErrNotOpen) { // a person dismissed it meanwhile
				errs = append(errs, fmt.Errorf("resolving %s: %w", kind, err))
			}
			continue
		}
		rep.Resolved++
	}
	return errors.Join(errs...)
}
