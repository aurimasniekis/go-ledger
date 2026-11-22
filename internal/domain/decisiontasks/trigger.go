package decisiontasks

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/decisions"
	"github.com/aurimasniekis/go-ledger/internal/domain/organisation"
)

// Suggester is the part of [Service] the trigger uses.
type Suggester interface {
	SuggestCategory(ctx context.Context, f Facts) (Outcome, error)
	SuggestLabels(ctx context.Context, f Facts) (Outcome, error)
}

// TransactionSource reads an account's stored transactions
// ([banking.FactStore] implements it).
type TransactionSource interface {
	Transactions(ctx context.Context, id banking.AccountID) ([]banking.StoredTransaction, error)
}

// MerchantInfo is what is known of a transaction's merchant.
type MerchantInfo struct {
	// Name is the merchant record's display name; empty when unknown.
	Name string
	// IsTransfer is true when the merchant is classified as a transfer.
	IsTransfer bool
}

// MerchantLookup returns the merchant of a transaction. A failure is not an
// error for the trigger: the merchant is then left out of what is sent.
type MerchantLookup func(ctx context.Context, id banking.TransactionID) (MerchantInfo, error)

// Trigger asks the decision tasks about transactions a refresh has just stored
// (task L6, ADR-0065). It never fails an import: [Trigger.AfterRefresh] has no
// result, every failure is logged and counted, and the import was stored
// before it runs. It is idempotent: the tasks skip an object with a pending
// suggestion or one a person turned down, and only the transactions the
// refresh inserted are considered, so a repeated refresh asks nothing new.
type Trigger struct {
	Tasks     Suggester
	Source    TransactionSource
	Merchants MerchantLookup
	// Enabled says whether a task is on; a task that is off costs nothing
	// (no lookup, no request).
	Enabled func(decisions.TaskName) bool
	Log     *slog.Logger
	// OnError is called once per failed question (for a visible counter); it
	// may be nil.
	OnError func(task decisions.TaskName)
}

// RefreshStats says what a trigger pass did.
type RefreshStats struct {
	Considered, Asked, Skipped, Failed int
}

// AfterRefresh runs the enabled tasks for the transactions res inserted. It
// is meant for the import engine's AfterRefresh hook.
func (t *Trigger) AfterRefresh(ctx context.Context, accounts map[banking.AccountID][]banking.TransactionID) RefreshStats {
	var st RefreshStats
	cat, lab := t.on(decisions.TaskCategorisation), t.on(decisions.TaskLabelSuggestion)
	if !cat && !lab {
		return st
	}
	for acct, ids := range accounts {
		if len(ids) == 0 || ctx.Err() != nil {
			continue
		}
		want := make(map[banking.TransactionID]bool, len(ids))
		for _, id := range ids {
			want[id] = true
		}
		stored, err := t.Source.Transactions(ctx, acct)
		if err != nil {
			t.fail(ctx, "", fmt.Errorf("reading the new transactions: %w", err), &st)
			continue
		}
		for _, tx := range stored {
			if !want[tx.ID] || ctx.Err() != nil {
				continue
			}
			st.Considered++
			f := t.facts(ctx, tx)
			if cat {
				t.run(ctx, decisions.TaskCategorisation, t.Tasks.SuggestCategory, f, &st)
			}
			if lab {
				t.run(ctx, decisions.TaskLabelSuggestion, t.Tasks.SuggestLabels, f, &st)
			}
		}
	}
	return st
}

func (t *Trigger) on(n decisions.TaskName) bool { return t.Enabled != nil && t.Enabled(n) }

// facts builds the question about one stored transaction. Only the fields the
// decision layer may send are set: no IBAN, account number, counterparty name
// or balance has a place in [Facts], and the merchant name is left out for a
// transfer or when the merchant cannot be read.
func (t *Trigger) facts(ctx context.Context, tx banking.StoredTransaction) Facts {
	f := Facts{
		Object:      organisation.ObjectRef{Type: organisation.ObjectTransaction, ID: string(tx.ID)},
		Description: tx.Description,
		Amount:      tx.Amount,
		BookingDate: tx.BookingDate,
	}
	if t.Merchants == nil {
		return f
	}
	m, err := t.Merchants(ctx, tx.ID)
	if err != nil {
		// Fail closed: without the classification the name is withheld.
		f.MerchantIsTransfer = true
		return f
	}
	f.MerchantName, f.MerchantIsTransfer = m.Name, m.IsTransfer
	return f
}

func (t *Trigger) run(ctx context.Context, task decisions.TaskName, ask func(context.Context, Facts) (Outcome, error), f Facts, st *RefreshStats) {
	out, err := ask(ctx, f)
	switch {
	case err != nil:
		t.fail(ctx, task, err, st)
	case out.Skipped != "":
		st.Skipped++
	case out.Result.Reason != "":
		// A provider or policy failure (outage, no consent, budget): the
		// decision layer reports it as a reason, not an error. Say so.
		t.fail(ctx, task, fmt.Errorf("no suggestion: %s", out.Result.Reason), st)
	default:
		st.Asked++
	}
}

func (t *Trigger) fail(ctx context.Context, task decisions.TaskName, err error, st *RefreshStats) {
	st.Failed++
	if t.OnError != nil {
		t.OnError(task)
	}
	if t.Log != nil && ctx.Err() == nil {
		// The error text may carry provider detail, never bank text: the
		// decision layer's errors name the failure, not the input.
		t.Log.Warn("decision task after a bank refresh failed", "task", string(task), "error", err.Error())
	}
}
