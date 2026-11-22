package decisions_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/bankimport"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/decisions"
	"github.com/aurimasniekis/go-ledger/internal/domain/decisions/decisionstest"
	"github.com/aurimasniekis/go-ledger/internal/domain/decisiontasks"
	"github.com/aurimasniekis/go-ledger/internal/metrics"
)

// The decision triggers (L6d): a fake provider only, no network and no model.

func (te *taskEnv) trigger(svc *decisiontasks.Service, enabled func(decisions.TaskName) bool, merchants decisiontasks.MerchantLookup) (*decisiontasks.Trigger, *int) {
	failed := new(int)
	return &decisiontasks.Trigger{Tasks: svc, Source: te.facts_(), Merchants: merchants, Enabled: enabled,
		OnError: func(decisions.TaskName, error) { *failed++ }}, failed
}

func (te *taskEnv) facts_() decisiontasks.TransactionSource { return te.fixture.facts }

func (te *taskEnv) newOne() map[banking.AccountID][]banking.TransactionID {
	return map[banking.AccountID][]banking.TransactionID{te.tx.AccountID: {te.tx.ID}}
}

func catOnly(n decisions.TaskName) bool { return n == decisions.TaskCategorisation }

func TestTriggerAsksOnceForANewTransaction(t *testing.T) {
	f := newFixture(t, true)
	te, svc := f.newTasks(t, false, decisionstest.Reply{Output: pickFirst})
	tr, failed := te.trigger(svc, catOnly, func(context.Context, banking.TransactionID) (decisiontasks.MerchantInfo, error) {
		return decisiontasks.MerchantInfo{Name: "Maxima"}, nil
	})
	st := tr.AfterRefresh(f.ctx, te.newOne())
	if st.Asked != 1 || st.Failed != 0 || *failed != 0 || te.provider.Calls() != 1 {
		t.Fatalf("%+v calls=%d", st, te.provider.Calls())
	}
	sent := string(te.provider.Sent()[0])
	if !strings.Contains(sent, "Maxima") || strings.Contains(sent, f.tx.Counterparty) && f.tx.Counterparty != "Maxima" {
		t.Fatalf("what was sent: %s", sent)
	}
	// Pending: a second pass asks nothing.
	st = tr.AfterRefresh(f.ctx, te.newOne())
	if st.Skipped != 1 || st.Asked != 0 || te.provider.Calls() != 1 {
		t.Fatalf("pending: %+v calls=%d", st, te.provider.Calls())
	}
}

// Every question is counted by task and outcome on the metric, and a failed
// one reaches OnError with its error. No transaction data is in a label.
func TestTriggerCountsQuestionsOnTheMetric(t *testing.T) {
	app := metrics.NewApp("test")
	f := newFixture(t, true)
	te, svc := f.newTasks(t, false, decisionstest.Reply{Output: pickFirst})
	tr, _ := te.trigger(svc, catOnly, nil)
	tr.Meter = app
	tr.AfterRefresh(f.ctx, te.newOne()) // asked
	tr.AfterRefresh(f.ctx, te.newOne()) // skipped: the suggestion is pending

	// No scripted reply: the question fails.
	f2 := newFixture(t, true)
	te2, svc2 := f2.newTasks(t, false)
	tr2, _ := te2.trigger(svc2, catOnly, nil)
	tr2.Meter = app
	var errs []error
	tr2.OnError = func(task decisions.TaskName, err error) {
		if task != decisions.TaskCategorisation {
			t.Errorf("OnError task %q", task)
		}
		errs = append(errs, err)
	}
	if st := tr2.AfterRefresh(f2.ctx, te2.newOne()); st.Failed != 1 {
		t.Fatalf("failed pass: %+v", st)
	}
	if len(errs) != 1 || errs[0] == nil {
		t.Fatalf("OnError got %v", errs)
	}

	var b bytes.Buffer
	if err := app.Registry.Write(&b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"ledger_decision_questions_total{task=\"categorisation\",outcome=\"asked\"} 1\n",
		"ledger_decision_questions_total{task=\"categorisation\",outcome=\"skipped\"} 1\n",
		"ledger_decision_questions_total{task=\"categorisation\",outcome=\"failed\"} 1\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if id := string(te.tx.ID); id == "" || strings.Contains(out, id) {
		t.Errorf("a transaction ID is empty or reached the metric output")
	}
}

func TestTriggerLeavesATransfersMerchantNameOut(t *testing.T) {
	f := newFixture(t, true)
	te, svc := f.newTasks(t, false, decisionstest.Reply{Output: pickFirst}, decisionstest.Reply{Output: pickFirst})
	tr, _ := te.trigger(svc, catOnly, func(context.Context, banking.TransactionID) (decisiontasks.MerchantInfo, error) {
		return decisiontasks.MerchantInfo{Name: "Jane Roe", IsTransfer: true}, nil
	})
	tr.AfterRefresh(f.ctx, te.newOne())
	if sent := string(te.provider.Sent()[0]); strings.Contains(sent, "Jane Roe") {
		t.Fatalf("the transfer's merchant name was sent: %s", sent)
	}
	// An unreadable merchant fails closed too.
	f2 := newFixture(t, true)
	te2, svc2 := f2.newTasks(t, false, decisionstest.Reply{Output: pickFirst})
	tr2, _ := te2.trigger(svc2, catOnly, func(context.Context, banking.TransactionID) (decisiontasks.MerchantInfo, error) {
		return decisiontasks.MerchantInfo{Name: "Jane Roe"}, errors.New("lookup failed")
	})
	tr2.AfterRefresh(f2.ctx, te2.newOne())
	if te2.provider.Calls() != 1 || strings.Contains(string(te2.provider.Sent()[0]), "Jane Roe") {
		t.Fatalf("calls=%d", te2.provider.Calls())
	}
}

func TestTriggerDisabledTasksCostNothing(t *testing.T) {
	f := newFixture(t, true)
	te, svc := f.newTasks(t, false, decisionstest.Reply{Output: pickFirst})
	tr, _ := te.trigger(svc, func(decisions.TaskName) bool { return false }, nil)
	if st := tr.AfterRefresh(f.ctx, te.newOne()); st != (decisiontasks.RefreshStats{}) || te.provider.Calls() != 0 {
		t.Fatalf("%+v calls=%d", st, te.provider.Calls())
	}
	tr.Enabled = nil
	if st := tr.AfterRefresh(f.ctx, te.newOne()); st != (decisiontasks.RefreshStats{}) || te.provider.Calls() != 0 {
		t.Fatalf("nil Enabled: %+v", st)
	}
}

func TestTriggerDoesNotAskAgainAfterADecline(t *testing.T) {
	f := newFixture(t, true)
	te, svc := f.newTasks(t, false, decisionstest.Reply{Output: pickFirst}, decisionstest.Reply{Output: pickFirst})
	tr, _ := te.trigger(svc, catOnly, nil)
	tr.AfterRefresh(f.ctx, te.newOne())
	list, err := f.ds.Suggestions(f.ctx, decstoreFilterTask(decisions.TaskCategorisation))
	if err != nil || len(list) != 1 {
		t.Fatalf("%v %d", err, len(list))
	}
	if _, err := svc.Reject(f.ctx, alice, list[0].ID); err != nil {
		t.Fatal(err)
	}
	if st := tr.AfterRefresh(f.ctx, te.newOne()); st.Asked != 0 || st.Skipped != 1 || te.provider.Calls() != 1 {
		t.Fatalf("declined: %+v calls=%d", st, te.provider.Calls())
	}
}

// A provider that fails costs the import nothing: the refresh succeeds, the
// failure is counted and logged.
func TestAProviderFailureNeverFailsAnImport(t *testing.T) {
	var te *taskEnv
	var tr *decisiontasks.Trigger
	var failed *int
	var passes []decisiontasks.RefreshStats
	f := newFixtureHook(t, false, func(ctx context.Context, res bankimport.Result) {
		in := map[banking.AccountID][]banking.TransactionID{}
		for _, a := range res.Accounts {
			in[a.ID] = a.Inserted
		}
		passes = append(passes, tr.AfterRefresh(ctx, in))
	})
	// No scripted reply: every call fails as an unavailable provider.
	var svc *decisiontasks.Service
	te, svc = f.newTasks(t, false)
	tr, failed = te.trigger(svc, catOnly, nil)
	f.importBank(func() time.Time { return t0 })
	if len(passes) != 1 || passes[0].Considered == 0 || passes[0].Failed != passes[0].Considered || *failed != passes[0].Failed {
		t.Fatalf("passes %+v failed=%d", passes, *failed)
	}
	if te.provider.Calls() != passes[0].Considered {
		t.Fatalf("calls %d, considered %d", te.provider.Calls(), passes[0].Considered)
	}
}
