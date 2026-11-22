package decisions_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/alerts"
	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/decisions"
	"github.com/aurimasniekis/go-ledger/internal/domain/decisions/decisionstest"
	"github.com/aurimasniekis/go-ledger/internal/domain/decisiontasks"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	org "github.com/aurimasniekis/go-ledger/internal/domain/organisation"
	alertstore "github.com/aurimasniekis/go-ledger/internal/store/alerts"
	decstore "github.com/aurimasniekis/go-ledger/internal/store/decisions"
	orgstore "github.com/aurimasniekis/go-ledger/internal/store/organisation"
)

func decstoreFilter() decstore.SuggestionFilter { return decstore.SuggestionFilter{} }

func decstoreFilterTask(t decisions.TaskName) decstore.SuggestionFilter {
	return decstore.SuggestionFilter{Task: t}
}

func (f *fixture) auditActions() []string {
	f.rec.mu.Lock()
	defer f.rec.mu.Unlock()
	var out []string
	for _, e := range f.rec.events {
		out = append(out, string(e.Action))
	}
	return out
}

// The decision tasks (L6) against real stores and the scripted provider: no
// network, no model.

type taskEnv struct {
	*fixture
	os        *orgstore.Store
	groceries org.Definition
	rent      org.Definition
	provider  *decisionstest.Provider
	obj       org.ObjectRef
}

func (f *fixture) newTasks(t *testing.T, autoApply bool, replies ...decisionstest.Reply) (*taskEnv, *decisiontasks.Service) {
	t.Helper()
	te := &taskEnv{fixture: f}
	clock := func() time.Time { return t0 }
	n := 0
	te.os = orgstore.New(f.db, f.rec, clock, func() string { n++; return fmt.Sprintf("01JD3Y4C8V9M2Q7W5R6T0XZ%03d", n) })
	var err error
	if te.groceries, err = te.os.CreateDefinition(f.ctx, alice, org.KindCategory, org.Fields{Name: "Groceries"}); err != nil {
		t.Fatal(err)
	}
	if te.rent, err = te.os.CreateDefinition(f.ctx, alice, org.KindCategory, org.Fields{Name: "Rent"}); err != nil {
		t.Fatal(err)
	}
	te.obj = org.ObjectRef{Type: org.ObjectTransaction, ID: string(f.tx.ID)}
	te.provider = decisionstest.NewProvider("openai", replies...)
	cfg := decisions.Config{Tasks: map[decisions.TaskName]decisions.TaskConfig{
		decisions.TaskCategorisation:  {Enabled: true, Route: decisions.Route{Provider: "openai", Model: "m1"}, Timeout: time.Second, MonthlyRequests: 50, AutoApply: autoApply},
		decisions.TaskLabelSuggestion: {Enabled: true, Route: decisions.Route{Provider: "openai", Model: "m1"}, Timeout: time.Second, MonthlyRequests: 50},
		decisions.TaskAlerts:          {Enabled: true, Route: decisions.Route{Provider: "openai", Model: "m1"}, Timeout: time.Second, MonthlyRequests: 50},
	}}
	for _, task := range []decisions.TaskName{decisions.TaskCategorisation, decisions.TaskLabelSuggestion, decisions.TaskAlerts} {
		if _, err := f.ds.Grant(f.ctx, alice, "openai", task, "terms-v1", decisions.DefaultFields("openai", task)); err != nil {
			t.Fatal(err)
		}
	}
	svc, err := decisions.New(cfg, []decisions.Provider{te.provider}, decisions.Deps{Consents: f.ds, Usage: f.ds, Suggestions: f.ds, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	f.ds.UseCategoryResetter(te.os)
	f.ds.UseAssigner(te.os)
	return te, decisiontasks.New(svc, f.ds, te.os)
}

func (te *taskEnv) facts() decisiontasks.Facts {
	return decisiontasks.Facts{Object: te.obj, Description: te.tx.Description, MerchantName: "Maxima", Amount: te.tx.Amount, BookingDate: te.tx.BookingDate}
}

func (te *taskEnv) category() *org.CategoryAssignment {
	te.t.Helper()
	a, err := te.os.Get(te.ctx, te.obj)
	if err != nil {
		te.t.Fatal(err)
	}
	return a.Category
}

func (te *taskEnv) actions() []string { return te.auditActions() }

const pickFirst = `{"choice":"c1","confidence":0.8}`

func TestCategorisationIsReviewedByDefault(t *testing.T) {
	f := newFixture(t, true)
	te, svc := f.newTasks(t, false, decisionstest.Reply{Output: pickFirst, Model: "m1-2026"})
	out, err := svc.SuggestCategory(f.ctx, te.facts())
	if err != nil || out.Result.Suggestion == nil || out.Applied || out.Skipped != "" {
		t.Fatalf("%+v %v", out, err)
	}
	if c := te.category(); c != nil {
		t.Fatalf("a suggestion assigned %+v before anyone accepted it", c)
	}
	sg := out.Result.Suggestion
	if sg.State != decisions.StatePending || sg.Provider != "openai" || sg.Model != "m1-2026" || *sg.Decision.Choice != te.groceries.ID {
		t.Fatalf("suggestion %+v", sg)
	}
	// What left the process: permitted fields and candidate names only.
	sent := te.provider.Sent()
	if len(sent) != 1 || !bytes.Contains(sent[0], []byte("Groceries")) || bytes.Contains(sent[0], []byte(f.tx.Counterparty)) || bytes.Contains(sent[0], []byte(te.groceries.ID)) {
		t.Fatalf("request %s", sent[0])
	}
}

func TestAcceptIsThePersonsOwnDecisionAndMovesOnce(t *testing.T) {
	f := newFixture(t, true)
	te, svc := f.newTasks(t, false, decisionstest.Reply{Output: pickFirst})
	out, _ := svc.SuggestCategory(f.ctx, te.facts())
	sg, err := svc.Accept(f.ctx, alice, out.Result.Suggestion.ID)
	if err != nil || sg.State != decisions.StateAccepted {
		t.Fatalf("%+v %v", sg, err)
	}
	c := te.category()
	if c == nil || c.Origin != org.OriginUser || c.Category.ID != te.groceries.ID {
		t.Fatalf("category %+v", c)
	}
	if _, err := svc.Accept(f.ctx, alice, sg.ID); !errors.Is(err, decisions.ErrNotPending) {
		t.Fatalf("second accept: %v", err)
	}
	if _, err := svc.Reject(f.ctx, alice, sg.ID); !errors.Is(err, decisions.ErrNotPending) {
		t.Fatalf("reject after accept: %v", err)
	}
	if !slices.Contains(te.actions(), string(audit.DecisionSuggestionAccepted)) {
		t.Fatalf("audit %v", te.actions())
	}
	// A person's category is never asked about again.
	if out, _ := svc.SuggestCategory(f.ctx, te.facts()); out.Skipped != decisiontasks.SkipAssigned || te.provider.Calls() != 1 {
		t.Fatalf("%+v calls %d", out, te.provider.Calls())
	}
}

func TestCorrectionAndRejectionAreRemembered(t *testing.T) {
	f := newFixture(t, true)
	te, svc := f.newTasks(t, false, decisionstest.Reply{Output: pickFirst}, decisionstest.Reply{Output: pickFirst})
	out, _ := svc.SuggestCategory(f.ctx, te.facts())
	sg, err := svc.Correct(f.ctx, alice, out.Result.Suggestion.ID, decisiontasks.Correction{CategoryID: &te.rent.ID})
	if err != nil || sg.State != decisions.StateRejected {
		t.Fatalf("%+v %v", sg, err)
	}
	if c := te.category(); c.Origin != org.OriginUser || c.Category.ID != te.rent.ID {
		t.Fatalf("category %+v", c)
	}
	if !slices.Contains(te.actions(), string(audit.DecisionSuggestionCorrected)) {
		t.Fatalf("audit %v", te.actions())
	}

	// Reject without an answer of their own: nothing assigned, not asked again.
	f2 := newFixture(t, true)
	te2, svc2 := f2.newTasks(t, false, decisionstest.Reply{Output: pickFirst}, decisionstest.Reply{Output: pickFirst})
	out2, _ := svc2.SuggestCategory(f2.ctx, te2.facts())
	if _, err := svc2.Reject(f2.ctx, alice, out2.Result.Suggestion.ID); err != nil {
		t.Fatal(err)
	}
	if te2.category() != nil {
		t.Fatal("a rejected suggestion assigned something")
	}
	if again, _ := svc2.SuggestCategory(f2.ctx, te2.facts()); again.Skipped != decisiontasks.SkipDeclined || te2.provider.Calls() != 1 {
		t.Fatalf("%+v calls %d", again, te2.provider.Calls())
	}
}

func TestAutoApplyIsModelOriginAuditedAndReversible(t *testing.T) {
	f := newFixture(t, true)
	te, svc := f.newTasks(t, true, decisionstest.Reply{Output: pickFirst, Model: "m1-2026"}, decisionstest.Reply{Output: pickFirst})
	out, err := svc.SuggestCategory(f.ctx, te.facts())
	if err != nil || !out.Applied || out.Result.Suggestion.State != decisions.StateAccepted {
		t.Fatalf("%+v %v", out, err)
	}
	c := te.category()
	if c == nil || c.Origin != org.OriginModel || c.Source != "openai:m1-2026" || c.Category.ID != te.groceries.ID {
		t.Fatalf("category %+v", c)
	}
	for _, want := range []audit.Action{audit.DecisionSuggestionAutoApplied, audit.OrganisationAutomaticApplied} {
		if !slices.Contains(te.actions(), string(want)) {
			t.Errorf("no %s in %v", want, te.actions())
		}
	}
	id := out.Result.Suggestion.ID
	if err := svc.Revert(f.ctx, alice, id); err != nil {
		t.Fatal(err)
	}
	if te.category() != nil {
		t.Fatal("the category is still there after the revert")
	}
	if !slices.Contains(te.actions(), string(audit.DecisionSuggestionReverted)) {
		t.Fatalf("audit %v", te.actions())
	}
	if err := svc.Revert(f.ctx, alice, id); !errors.Is(err, decisions.ErrNotReversible) {
		t.Fatalf("second revert: %v", err)
	}
	// The model is not asked again about what a person took back.
	if again, _ := svc.SuggestCategory(f.ctx, te.facts()); again.Skipped != decisiontasks.SkipDeclined {
		t.Fatalf("%+v", again)
	}
}

// The category reset and the reversal record are one transaction: when the
// record cannot be written, the category stays, nothing is audited as a revert,
// and the revert can be tried again.
func TestRevertIsAtomicWhenTheReversalRecordFails(t *testing.T) {
	f := newFixture(t, true)
	te, svc := f.newTasks(t, true, decisionstest.Reply{Output: pickFirst})
	out, _ := svc.SuggestCategory(f.ctx, te.facts())
	id := out.Result.Suggestion.ID
	if _, err := execWrite(f, "CREATE TRIGGER fail_reversal BEFORE INSERT ON decision_suggestion_reversals BEGIN SELECT RAISE(ABORT, 'record refused'); END"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Revert(f.ctx, alice, id); err == nil || errors.Is(err, decisions.ErrNotReversible) {
		t.Fatalf("revert: %v", err)
	}
	if c := te.category(); c == nil || c.Origin != org.OriginModel || c.Category.ID != te.groceries.ID {
		t.Fatalf("the category changed although the record failed: %+v", c)
	}
	if rev, _ := f.ds.Reverted(f.ctx, id); rev {
		t.Fatal("a reversal is recorded")
	}
	if _, err := execWrite(f, "DROP TRIGGER fail_reversal"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Revert(f.ctx, alice, id); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if te.category() != nil {
		t.Fatal("the category is still there after the retry")
	}
	if rev, _ := f.ds.Reverted(f.ctx, id); !rev {
		t.Fatal("no reversal recorded after the retry")
	}
}

func TestRevertNeverUndoesAPersonsOrRulesDecision(t *testing.T) {
	f := newFixture(t, true)
	te, svc := f.newTasks(t, true, decisionstest.Reply{Output: pickFirst})
	out, _ := svc.SuggestCategory(f.ctx, te.facts())
	if _, err := te.os.SetCategory(f.ctx, alice, te.obj, &te.rent.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Revert(f.ctx, alice, out.Result.Suggestion.ID); !errors.Is(err, decisions.ErrNotReversible) {
		t.Fatalf("revert: %v", err)
	}
	if c := te.category(); c.Origin != org.OriginUser || c.Category.ID != te.rent.ID {
		t.Fatalf("the person's category was touched: %+v", c)
	}
	// A suggestion that was only accepted by a person is not an auto-apply.
	f2 := newFixture(t, true)
	te2, svc2 := f2.newTasks(t, false, decisionstest.Reply{Output: pickFirst})
	o2, _ := svc2.SuggestCategory(f2.ctx, te2.facts())
	if _, err := svc2.Accept(f2.ctx, alice, o2.Result.Suggestion.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc2.Revert(f2.ctx, alice, o2.Result.Suggestion.ID); !errors.Is(err, decisions.ErrNotReversible) {
		t.Fatalf("revert of an accepted suggestion: %v", err)
	}
}

func TestAutoApplyNeverReplacesAPersonOrARule(t *testing.T) {
	for name, set := range map[string]func(te *taskEnv){
		"person": func(te *taskEnv) {
			if _, err := te.os.SetCategory(te.ctx, alice, te.obj, &te.rent.ID); err != nil {
				te.t.Fatal(err)
			}
		},
		"person chose none": func(te *taskEnv) {
			if _, err := te.os.SetCategory(te.ctx, alice, te.obj, nil); err != nil {
				te.t.Fatal(err)
			}
		},
		"rule": func(te *taskEnv) {
			if _, err := te.os.ApplyAutomatic(te.ctx, decisiontasks.SystemActor, te.obj, org.Automatic{Origin: org.OriginRule, Source: "rule-1", CategoryID: &te.rent.ID}); err != nil {
				te.t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, true)
			te, svc := f.newTasks(t, true, decisionstest.Reply{Output: pickFirst})
			set(te)
			before := te.category()
			out, err := svc.SuggestCategory(f.ctx, te.facts())
			if err != nil || out.Skipped != decisiontasks.SkipAssigned || te.provider.Calls() != 0 {
				t.Fatalf("%+v %v calls %d", out, err, te.provider.Calls())
			}
			if after := te.category(); after.Origin != before.Origin || (after.Category == nil) != (before.Category == nil) {
				t.Fatalf("category changed from %+v to %+v", before, after)
			}
		})
	}
}

func TestOutageAndInvalidOutputChangeNothing(t *testing.T) {
	f := newFixture(t, true)
	te, svc := f.newTasks(t, true,
		decisionstest.Reply{Err: &decisions.ProviderError{Kind: decisions.KindUnavailable, Message: "down"}},
		decisionstest.Reply{Output: `{"choice":"c9","confidence":0.8}`})
	for i := 0; i < 2; i++ {
		out, err := svc.SuggestCategory(f.ctx, te.facts())
		if err != nil || out.Result.Suggestion != nil || out.Result.Reason == "" || out.Applied {
			t.Fatalf("run %d: %+v %v", i, out, err)
		}
	}
	if te.category() != nil {
		t.Fatal("a failed run assigned a category")
	}
	if got, _ := f.ds.Suggestions(f.ctx, decstoreFilter()); len(got) != 0 {
		t.Fatalf("suggestions stored: %+v", got)
	}
}

func TestNoChoiceLeavesTheQueue(t *testing.T) {
	f := newFixture(t, true)
	te, svc := f.newTasks(t, false, decisionstest.Reply{Output: `{"choice":null,"confidence":0.3}`})
	out, err := svc.SuggestCategory(f.ctx, te.facts())
	if err != nil || !out.NoChoice {
		t.Fatalf("%+v %v", out, err)
	}
	if got, _ := f.ds.Suggestions(f.ctx, decstoreFilter()); len(got) != 1 || got[0].State != decisions.StateSuperseded {
		t.Fatalf("%+v", got)
	}
}

func TestTransferMerchantNamesAreNotSent(t *testing.T) {
	f := newFixture(t, true)
	te, svc := f.newTasks(t, false, decisionstest.Reply{Output: pickFirst})
	fa := te.facts()
	fa.MerchantName, fa.MerchantIsTransfer = "Jane Q Person", true
	if _, err := svc.SuggestCategory(f.ctx, fa); err != nil {
		t.Fatal(err)
	}
	if b := te.provider.Sent()[0]; bytes.Contains(b, []byte("Jane Q Person")) {
		t.Fatalf("a transfer's merchant name was sent: %s", b)
	}
}

func TestLabelSuggestionsAreReviewOnly(t *testing.T) {
	f := newFixture(t, true)
	te, svc := f.newTasks(t, true, decisionstest.Reply{Output: `{"choices":["c1"],"confidence":0.7}`})
	trip, _ := te.os.CreateDefinition(f.ctx, alice, org.KindLabel, org.Fields{Name: "Trip"})
	old, _ := te.os.CreateDefinition(f.ctx, alice, org.KindLabel, org.Fields{Name: "Old"})
	if _, err := te.os.AddLabel(f.ctx, alice, te.obj, old.ID); err != nil {
		t.Fatal(err)
	}
	out, err := svc.SuggestLabels(f.ctx, te.facts())
	if err != nil || out.Applied || out.Result.Suggestion == nil || out.Result.Suggestion.AutoApply {
		t.Fatalf("%+v %v", out, err)
	}
	if b := te.provider.Sent()[0]; bytes.Contains(b, []byte("Old")) || !bytes.Contains(b, []byte("Trip")) {
		t.Fatalf("labels the object has were offered, or the free ones were not: %s", b)
	}
	if a, _ := te.os.Get(f.ctx, te.obj); len(a.Labels) != 1 {
		t.Fatalf("a label suggestion assigned: %+v", a.Labels)
	}
	if _, err := svc.Accept(f.ctx, alice, out.Result.Suggestion.ID); err != nil {
		t.Fatal(err)
	}
	a, _ := te.os.Get(f.ctx, te.obj)
	var got *org.LabelAssignment
	for i := range a.Labels {
		if a.Labels[i].Label.ID == trip.ID {
			got = &a.Labels[i]
		}
	}
	if got == nil || got.Origin != org.OriginUser {
		t.Fatalf("labels %+v", a.Labels)
	}
	// Auto-apply for label suggestions is a configuration error (D13).
	cfg := decisions.Config{Tasks: map[decisions.TaskName]decisions.TaskConfig{
		decisions.TaskLabelSuggestion: {Enabled: true, Route: decisions.Route{Provider: "openai", Model: "m"}, Timeout: time.Second, MonthlyRequests: 5, AutoApply: true}}}
	if ps := decisions.ValidateConfig(cfg, decisions.KnownProviders()); len(ps) == 0 {
		t.Fatal("auto-apply of labels was accepted")
	}
}

func TestAlertsAreInAppUnlessAChannelIsSelected(t *testing.T) {
	f := newFixture(t, false)
	ctx := f.ctx
	st := alertstore.New(f.db, f.rec, func() time.Time { return t0 }, nil)
	today := calendar.MustDate(2026, 11, 1)
	cond, ok := alerts.ConsentExpiring("01JD3Y4C8V9M2Q7W5R6T0XZBNA", calendar.MustDate(2026, 11, 8), today, 14)
	if !ok {
		t.Fatal("no condition")
	}

	inApp := alerts.New(st, nil)
	res, err := inApp.Raise(ctx, decisiontasks.SystemActor, cond)
	if err != nil || !res.Created || res.Alert.State != alerts.StateOpen || res.Delivery != nil {
		t.Fatalf("%+v %v", res, err)
	}
	again, _ := inApp.Raise(ctx, decisiontasks.SystemActor, cond)
	if again.Created || again.Alert.ID != res.Alert.ID {
		t.Fatalf("raised twice: %+v", again)
	}
	if open, _ := st.List(ctx, alerts.StateOpen, 10); len(open) != 1 {
		t.Fatalf("open %d", len(open))
	}
	if _, err := inApp.Dismiss(ctx, alice, res.Alert.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := inApp.Dismiss(ctx, alice, res.Alert.ID); !errors.Is(err, alerts.ErrNotOpen) {
		t.Fatalf("second dismiss: %v", err)
	}
	if !slices.Contains(f.auditActions(), string(audit.AlertRaised)) || !slices.Contains(f.auditActions(), string(audit.AlertClosed)) {
		t.Fatalf("audit %v", f.auditActions())
	}

	// A selected channel receives a new alert once, and its failure keeps the alert.
	var sent []string
	ch := alerts.Channel{Name: "pulse", Notify: func(_ context.Context, a alerts.Alert) error { sent = append(sent, a.Message); return nil }}
	bad := alerts.Channel{Name: "broken", Notify: func(context.Context, alerts.Alert) error { return errors.New("down") }}
	out := alerts.New(st, nil, ch, bad)
	c2, _ := alerts.UnmatchedOverdue("01JD3Y4C8V9M2Q7W5R6T0XZBNB", calendar.MustDate(2026, 10, 20), today, money.MustParse("12.50", "EUR"))
	r, err := out.Raise(ctx, decisiontasks.SystemActor, c2)
	if err != nil || len(sent) != 1 || r.Delivery["broken"] == nil || r.Delivery["pulse"] != nil {
		t.Fatalf("%+v %v sent %v", r, err, sent)
	}
	if _, err := out.Raise(ctx, decisiontasks.SystemActor, c2); err != nil || len(sent) != 1 {
		t.Fatalf("a duplicate was sent again: %v", sent)
	}
	if got, err := st.Get(ctx, r.Alert.ID); err != nil || got.State != alerts.StateOpen {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestAlertWordingIsLabelledAndOptional(t *testing.T) {
	f := newFixture(t, false)
	te, _ := f.newTasks(t, false, decisionstest.Reply{Output: `{"text":"Your bank consent ends soon."}`, Model: "m1-2026"}, decisionstest.Reply{Err: &decisions.ProviderError{Kind: decisions.KindUnavailable, Message: "down"}})
	cfg := decisions.Config{Tasks: map[decisions.TaskName]decisions.TaskConfig{
		decisions.TaskAlerts: {Enabled: true, Route: decisions.Route{Provider: "openai", Model: "m1"}, Timeout: time.Second, MonthlyRequests: 50}}}
	runner, err := decisions.New(cfg, []decisions.Provider{te.provider}, decisions.Deps{Consents: f.ds, Usage: f.ds, Suggestions: f.ds})
	if err != nil {
		t.Fatal(err)
	}
	st := alertstore.New(f.db, f.rec, func() time.Time { return t0 }, nil)
	svc := alerts.New(st, decisiontasks.NewAlertWording(runner, f.ds))
	today := calendar.MustDate(2026, 11, 1)
	c1, _ := alerts.AmountChanged("01JD3Y4C8V9M2Q7W5R6T0XZBNC", money.MustParse("10.00", "EUR"), money.MustParse("12.00", "EUR"))
	r, err := svc.Raise(f.ctx, decisiontasks.SystemActor, c1)
	if err != nil || r.Alert.Wording == nil || r.Alert.Wording.Provider != "openai" || r.Alert.Wording.Model != "m1-2026" {
		t.Fatalf("%+v %v", r, err)
	}
	if !strings.Contains(r.Alert.Message, "10.00 EUR") || !strings.Contains(r.Alert.Message, "12.00 EUR") {
		t.Fatalf("Ledger's own message lost: %q", r.Alert.Message)
	}
	// What the model saw is Ledger's message only.
	if b := te.provider.Sent()[0]; !bytes.Contains(b, []byte("12.00 EUR")) || bytes.Contains(b, []byte("counterparty")) {
		t.Fatalf("request %s", b)
	}
	// An outage: the alert is raised without wording.
	c2, _ := alerts.ConsentExpiring("01JD3Y4C8V9M2Q7W5R6T0XZBND", calendar.MustDate(2026, 11, 3), today, 14)
	r2, err := svc.Raise(f.ctx, decisiontasks.SystemActor, c2)
	if err != nil || r2.Alert.Wording != nil || !r2.Created {
		t.Fatalf("%+v %v", r2, err)
	}
	got, _ := st.Get(f.ctx, r.Alert.ID)
	if got.Wording == nil || got.Wording.Text != "Your bank consent ends soon." {
		t.Fatalf("stored %+v", got)
	}
	// Both suggestion rows left the pending queue.
	if p, _ := f.ds.Suggestions(f.ctx, decstoreFilterTask(decisions.TaskAlerts)); len(p) != 1 || p[0].State != decisions.StateSuperseded {
		t.Fatalf("%+v", p)
	}
}

func TestADismissedAlertStaysDismissedAndAResolvedOneMayReturn(t *testing.T) {
	f := newFixture(t, false)
	st := alertstore.New(f.db, f.rec, func() time.Time { return t0 }, nil)
	var sent int
	svc := alerts.New(st, nil, alerts.Channel{Name: "pulse", Notify: func(context.Context, alerts.Alert) error { sent++; return nil }})
	today := calendar.MustDate(2026, 11, 1)
	c, _ := alerts.ConsentExpiring("01JD3Y4C8V9M2Q7W5R6T0XZBNE", calendar.MustDate(2026, 11, 8), today, 14)
	first, err := svc.Raise(f.ctx, decisiontasks.SystemActor, c)
	if err != nil || !first.Created {
		t.Fatalf("%+v %v", first, err)
	}
	if _, err := svc.Dismiss(f.ctx, alice, first.Alert.ID); err != nil {
		t.Fatal(err)
	}
	again, err := svc.Raise(f.ctx, decisiontasks.SystemActor, c)
	if err != nil || again.Created || again.Alert.ID != first.Alert.ID || again.Alert.State != alerts.StateDismissed || sent != 1 {
		t.Fatalf("a dismissal did not stick: %+v sent=%d %v", again, sent, err)
	}
	if open, _ := st.List(f.ctx, alerts.StateOpen, 10); len(open) != 0 {
		t.Fatalf("%+v", open)
	}
	// A resolved alert lets the same condition be raised anew.
	c2, _ := alerts.ConsentExpiring("01JD3Y4C8V9M2Q7W5R6T0XZBNF", calendar.MustDate(2026, 11, 8), today, 14)
	r1, _ := svc.Raise(f.ctx, decisiontasks.SystemActor, c2)
	if _, err := svc.Resolve(f.ctx, decisiontasks.SystemActor, r1.Alert.ID); err != nil {
		t.Fatal(err)
	}
	r2, err := svc.Raise(f.ctx, decisiontasks.SystemActor, c2)
	if err != nil || !r2.Created || r2.Alert.ID == r1.Alert.ID || r2.Alert.State != alerts.StateOpen {
		t.Fatalf("%+v %v", r2, err)
	}
}

func TestWordingFailureDoesNotSkipChannelsAndModelTextIsFrozen(t *testing.T) {
	f := newFixture(t, false)
	st := alertstore.New(f.db, f.rec, func() time.Time { return t0 }, nil)
	var sent int
	svc := alerts.New(st, failingWords{}, alerts.Channel{Name: "pulse", Notify: func(context.Context, alerts.Alert) error { sent++; return nil }})
	c, _ := alerts.AmountChanged("01JD3Y4C8V9M2Q7W5R6T0XZBNG", money.MustParse("10.00", "EUR"), money.MustParse("12.00", "EUR"))
	r, err := svc.Raise(f.ctx, decisiontasks.SystemActor, c)
	if err != nil || !r.Created || r.WordingErr == nil || sent != 1 {
		t.Fatalf("%+v sent=%d %v", r, sent, err)
	}
	if err := st.SetWording(f.ctx, r.Alert.ID, alerts.Wording{Text: "once", Provider: "openai", Model: "m1"}); err != nil {
		t.Fatal(err)
	}
	// The store refuses a second write, and so does the table itself.
	if err := st.SetWording(f.ctx, r.Alert.ID, alerts.Wording{Text: "twice", Provider: "openai", Model: "m1"}); !errors.Is(err, alerts.ErrNotOpen) {
		t.Fatalf("second wording: %v", err)
	}
	if _, err := execWrite(f, "UPDATE alerts SET model_text = 'x' WHERE id = ?", r.Alert.ID); err == nil {
		t.Fatal("the trigger allowed the model text to change")
	}
	if _, err := svc.Dismiss(f.ctx, alice, r.Alert.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := execWrite(f, "UPDATE alerts SET model_text = 'x' WHERE id = ?", r.Alert.ID); err == nil {
		t.Fatal("the trigger allowed the model text of a closed alert to change")
	}
}

type failingWords struct{}

func (failingWords) Word(context.Context, alerts.Alert) (alerts.Wording, bool, error) {
	return alerts.Wording{}, false, errors.New("wording broke")
}

func TestNoSecondQuestionWhileASuggestionIsPending(t *testing.T) {
	f := newFixture(t, true)
	te, svc := f.newTasks(t, false, decisionstest.Reply{Output: pickFirst}, decisionstest.Reply{Output: pickFirst})
	if out, err := svc.SuggestCategory(f.ctx, te.facts()); err != nil || out.Result.Suggestion == nil {
		t.Fatalf("%+v %v", out, err)
	}
	again, err := svc.SuggestCategory(f.ctx, te.facts())
	if err != nil || again.Skipped != decisiontasks.SkipPending || te.provider.Calls() != 1 {
		t.Fatalf("%+v calls=%d %v", again, te.provider.Calls(), err)
	}
}

func TestLabelAcceptIsAllOrNothing(t *testing.T) {
	f := newFixture(t, true)
	te, svc := f.newTasks(t, false, decisionstest.Reply{Output: `{"choices":["c1","c2"],"confidence":0.7}`})
	a, _ := te.os.CreateDefinition(f.ctx, alice, org.KindLabel, org.Fields{Name: "Aaa"})
	b, _ := te.os.CreateDefinition(f.ctx, alice, org.KindLabel, org.Fields{Name: "Bbb"})
	out, err := svc.SuggestLabels(f.ctx, te.facts())
	if err != nil || out.Result.Suggestion == nil {
		t.Fatalf("%+v %v", out, err)
	}
	// One of the two is archived after the suggestion was made.
	if _, err := te.os.SetArchived(f.ctx, alice, org.KindLabel, b.ID, b.Version, true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Accept(f.ctx, alice, out.Result.Suggestion.ID); err == nil {
		t.Fatal("accept succeeded with an archived label")
	}
	if got, _ := te.os.Get(f.ctx, te.obj); len(got.Labels) != 0 {
		t.Fatalf("a partial accept left labels behind: %+v (%s)", got.Labels, a.ID)
	}
	if sg, _ := f.ds.Suggestion(f.ctx, out.Result.Suggestion.ID); sg.State != decisions.StatePending {
		t.Fatalf("state %s", sg.State)
	}
}

func execWrite(f *fixture, q string, args ...any) (int, error) {
	err := f.db.WriteTx(f.ctx, func(ctx context.Context, tx *sql.Tx) error { _, err := tx.ExecContext(ctx, q, args...); return err })
	return 0, err
}

// A decision made after another one has won is refused and leaves no
// assignment behind: the assignment and the suggestion's state are decided in
// one transaction.
func TestLateDecisionRollsItsAssignmentBack(t *testing.T) {
	f := newFixture(t, true)
	te, svc := f.newTasks(t, false, decisionstest.Reply{Output: pickFirst})
	out, _ := svc.SuggestCategory(f.ctx, te.facts())
	id := out.Result.Suggestion.ID
	// The late decision finds the suggestion already decided (the race, made
	// deterministic): its assignment is rolled back.
	if _, err := svc.Reject(f.ctx, alice, id); err != nil {
		t.Fatal(err)
	}
	_, err := f.ds.DecideSettingCategory(f.ctx, alice, id, decisions.StateAccepted, audit.DecisionSuggestionAccepted, te.obj, &te.groceries.ID)
	if !errors.Is(err, decisions.ErrNotPending) {
		t.Fatalf("late decision: %v", err)
	}
	if c := te.category(); c != nil {
		t.Fatalf("the refused decision left %+v", c)
	}
	if sg, _ := f.ds.Suggestion(f.ctx, id); sg.State != decisions.StateRejected {
		t.Fatalf("state %s", sg.State)
	}
}

// Two decisions on one suggestion at the same moment end consistent: one wins,
// the other gets suggestion_not_pending, and the object holds the winner's
// assignment (or none, for a reject).
func TestConcurrentDecisionsEndConsistent(t *testing.T) {
	f := newFixture(t, true)
	te, svc := f.newTasks(t, false, decisionstest.Reply{Output: pickFirst})
	out, _ := svc.SuggestCategory(f.ctx, te.facts())
	id := out.Result.Suggestion.ID
	rent := te.rent.ID
	ops := []func() (decisions.Suggestion, error){
		func() (decisions.Suggestion, error) { return svc.Accept(f.ctx, alice, id) },
		func() (decisions.Suggestion, error) {
			return svc.Correct(f.ctx, alice, id, decisiontasks.Correction{CategoryID: &rent})
		},
		func() (decisions.Suggestion, error) { return svc.Reject(f.ctx, alice, id) },
	}
	errs := make([]error, len(ops))
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range ops {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = ops[i]()
		}()
	}
	close(start)
	wg.Wait()
	won := -1
	for i, err := range errs {
		switch {
		case err == nil && won == -1:
			won = i
		case errors.Is(err, decisions.ErrNotPending):
		default:
			t.Fatalf("decision %d: %v (winner %d)", i, err, won)
		}
	}
	if won == -1 {
		t.Fatal("no decision won")
	}
	sg, _ := f.ds.Suggestion(f.ctx, id)
	c := te.category()
	switch won {
	case 0:
		if sg.State != decisions.StateAccepted || c == nil || c.Category.ID != te.groceries.ID {
			t.Fatalf("accept won: %s %+v", sg.State, c)
		}
	case 1:
		if sg.State != decisions.StateRejected || c == nil || c.Category.ID != rent {
			t.Fatalf("correct won: %s %+v", sg.State, c)
		}
	default:
		if sg.State != decisions.StateRejected || c != nil {
			t.Fatalf("reject won: %s %+v", sg.State, c)
		}
	}
}
