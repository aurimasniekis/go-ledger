package automatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/matches"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
)

// Store is what a run needs. internal/store/matches implements it.
type Store interface {
	// Snapshot materialises occurrences through month through, then reads
	// the eligible transactions, the open occurrences and every match.
	Snapshot(ctx context.Context, through calendar.YearMonth) (Snapshot, error)
	// Create stores a new match (here: as audit.System, origin auto_rule).
	Create(ctx context.Context, actor audit.Actor, n matches.New) (matches.Change, error)
	// ConfirmAuto confirms the engine's own suggestion as audit.System,
	// applying the automatic-confirmation guards with the commitment's
	// tolerance.
	ConfirmAuto(ctx context.Context, id string, expected int64, reason *string) (matches.Change, error)
	// Reject rejects a suggestion.
	Reject(ctx context.Context, actor audit.Actor, id string, expected int64, reason *string) (matches.Change, error)
}

// Recorder records an audit event on its own (a run requested through the
// API).
type Recorder interface {
	Record(ctx context.Context, e audit.Event) error
}

// Result counts what a run did.
type Result struct {
	// Candidates counts the candidate pairs evaluated, including those
	// already suggested by an earlier run.
	Candidates int `json:"candidates"`
	Confirmed  int `json:"confirmed"`
	Suggested  int `json:"suggested"`
	Dropped    int `json:"dropped"`
	// Skipped counts actions the store refused (its guards, or a person
	// changed the data since the snapshot); the next run decides again.
	Skipped int `json:"skipped"`
}

// Changed reports whether the run wrote anything.
func (r Result) Changed() bool { return r.Confirmed+r.Suggested+r.Dropped > 0 }

// Options configure an [Engine].
type Options struct {
	// Today returns the current date in the reporting time zone.
	Today func() (calendar.Date, error)
	Log   *slog.Logger
	// Recorder records the audit event of a requested run; nil records none.
	Recorder Recorder
	// Now stamps that audit event; nil means time.Now.
	Now func() time.Time
}

// Engine runs the matching. One run happens at a time; a run started while
// another is going waits for it.
type Engine struct {
	st  Store
	o   Options
	log *slog.Logger
	mu  sync.Mutex
}

// Store returns the store the engine acts through.
func (e *Engine) Store() Store { return e.st }

// New returns an engine writing through st.
func New(st Store, o Options) *Engine {
	if o.Today == nil {
		panic("automatch.New: Today is required")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	log := o.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Engine{st: st, o: o, log: log.With("job", JobName)}
}

// JobName names the engine in logs.
const JobName = "payment_matching"

// RunTargetType is the audit target type of a requested run.
const RunTargetType = "payment_matching"

// Run matches once: it reads a snapshot, plans and applies the actions. It is
// idempotent: a second run over unchanged data writes nothing. An action the
// store refuses is skipped and counted; an error reading or writing the
// database ends the run with what was done so far.
func (e *Engine) Run(ctx context.Context) (Result, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	today, err := e.o.Today()
	if err != nil {
		return Result{}, err
	}
	// Payments made up to CandidateDays before a due date are candidates, so
	// next month's occurrences must exist near a month end.
	ahead, err := today.AddDays(CandidateDays)
	if err != nil {
		return Result{}, err
	}
	snap, err := e.st.Snapshot(ctx, ahead.YearMonth())
	if err != nil {
		return Result{}, fmt.Errorf("automatch: snapshot: %w", err)
	}
	actions, candidates := plan(snap)
	res := Result{Candidates: candidates}
	for _, a := range actions {
		reason := a.Reason
		var werr error
		switch a.Kind {
		case ActionDrop:
			_, werr = e.st.Reject(ctx, audit.System, a.MatchID, a.Version, &reason)
		case ActionConfirm:
			if a.MatchID != "" {
				_, werr = e.st.ConfirmAuto(ctx, a.MatchID, a.Version, &reason)
			} else {
				_, werr = e.st.Create(ctx, audit.System, matches.New{TransactionID: a.TransactionID, OccurrenceID: a.OccurrenceID, Amount: a.Amount,
					Origin: matches.OriginAutoRule, State: matches.StateConfirmed, Reason: &reason})
				if werr != nil && refused(werr) {
					// The store's guards outrank the plan: leave a suggestion
					// for a person instead.
					e.log.Info("automatic confirmation refused; suggesting instead", "occurrence_id", a.OccurrenceID, "transaction_id", string(a.TransactionID), "error", werr.Error())
					reason = "automatic match not confirmed: the store refused the automatic confirmation (" + refusalCode(werr) + "); description similarity " + strconv.Itoa(a.Checks.Score) + "/100 (a hint only)"
					if len(reason) > matches.MaxReason {
						reason = reason[:matches.MaxReason]
					}
					a.Kind = ActionSuggest
					_, werr = e.st.Create(ctx, audit.System, matches.New{TransactionID: a.TransactionID, OccurrenceID: a.OccurrenceID, Amount: a.Amount,
						Origin: matches.OriginAutoRule, State: matches.StateSuggested, Reason: &reason})
				}
			}
		case ActionSuggest:
			_, werr = e.st.Create(ctx, audit.System, matches.New{TransactionID: a.TransactionID, OccurrenceID: a.OccurrenceID, Amount: a.Amount,
				Origin: matches.OriginAutoRule, State: matches.StateSuggested, Reason: &reason})
		}
		switch {
		case werr == nil:
			switch a.Kind {
			case ActionDrop:
				res.Dropped++
			case ActionConfirm:
				res.Confirmed++
			case ActionSuggest:
				res.Suggested++
			}
		case refused(werr):
			res.Skipped++
			e.log.Info("payment matching action skipped", "action", string(a.Kind), "occurrence_id", a.OccurrenceID, "transaction_id", string(a.TransactionID), "error", werr.Error())
		default:
			e.log.Warn("payment matching stopped", "error", werr.Error(), "confirmed", res.Confirmed, "suggested", res.Suggested, "dropped", res.Dropped)
			return res, fmt.Errorf("automatch: %s: %w", a.Kind, werr)
		}
	}
	if res.Changed() || res.Skipped > 0 {
		e.log.Info("payment matching finished", "candidates", res.Candidates, "confirmed", res.Confirmed, "suggested", res.Suggested,
			"dropped", res.Dropped, "skipped", res.Skipped)
	}
	return res, nil
}

// Request runs the matching for actor (a user or token through the API) and
// records payment_matching.run with the counts. The matches themselves are
// written and audited as the system, which the store holds to the automatic
// rules.
func (e *Engine) Request(ctx context.Context, actor audit.Actor) (Result, error) {
	res, err := e.Run(ctx)
	if err != nil {
		return res, err
	}
	if e.o.Recorder != nil {
		ev := audit.Event{Action: audit.PaymentMatchingRun, Actor: actor, TargetType: RunTargetType, TargetID: "run", At: e.o.Now().UTC(),
			Details: map[string]string{"confirmed": strconv.Itoa(res.Confirmed), "suggested": strconv.Itoa(res.Suggested),
				"dropped": strconv.Itoa(res.Dropped), "skipped": strconv.Itoa(res.Skipped)}}
		if err := e.o.Recorder.Record(ctx, ev); err != nil {
			return res, err
		}
	}
	return res, nil
}

// refusalCode is the stable code of a store refusal, for a match's reason: the
// store's error text is for logs, not for people reading a suggestion.
func refusalCode(err error) string {
	codes := []struct {
		err  error
		code string
	}{
		{matches.ErrNotCountable, "not_countable"}, {matches.ErrNotDebit, "not_debit"}, {matches.ErrCurrency, "currency"},
		{matches.ErrOverAllocated, "over_allocated"}, {matches.ErrOverpaid, "overpaid"}, {matches.ErrScale, "scale"},
		{matches.ErrAutoNotOneToOne, "not_one_to_one"}, {matches.ErrAutoAmount, "amount_outside_tolerance"}, {matches.ErrAutoDate, "date_outside_window"},
		{matches.ErrAutoModel, "model_suggestion"}, {matches.ErrDuplicate, "duplicate"}, {matches.ErrActorOrigin, "actor_origin"},
		{matches.ErrActorOverpayment, "actor_overpayment"}, {matches.ErrActorCorrect, "actor_correct"}, {matches.ErrActorUndo, "actor_undo"},
		{matches.ErrTransition, "transition"}, {matches.ErrTransactionNotFound, "transaction_not_found"}, {matches.ErrOccurrenceNotFound, "occurrence_not_found"},
	}
	for _, c := range codes {
		if errors.Is(err, c.err) {
			return "store_refused: " + c.code
		}
	}
	return "store_refused: other"
}

// refused reports whether err is the store refusing an action on its rules
// or because the data changed since the snapshot, rather than a failure.
func refused(err error) bool {
	var vc *resource.VersionConflictError
	switch {
	case errors.As(err, &vc), errors.Is(err, resource.ErrNotFound), errors.Is(err, occurrences.ErrCancelled):
		return true
	}
	for _, rule := range []error{matches.ErrNotCountable, matches.ErrNotDebit, matches.ErrCurrency, matches.ErrOverAllocated, matches.ErrOverpaid,
		matches.ErrScale, matches.ErrAutoNotOneToOne, matches.ErrAutoAmount, matches.ErrAutoDate, matches.ErrActorOrigin, matches.ErrActorOverpayment, matches.ErrActorCorrect,
		matches.ErrActorUndo, matches.ErrAutoModel, matches.ErrDuplicate, matches.ErrTransition, matches.ErrTransactionNotFound, matches.ErrOccurrenceNotFound} {
		if errors.Is(err, rule) {
			return true
		}
	}
	return false
}
