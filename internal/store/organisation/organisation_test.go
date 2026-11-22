package organisation_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/bankimport"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking/bankingtest"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	org "github.com/aurimasniekis/go-ledger/internal/domain/organisation"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/store"
	"github.com/aurimasniekis/go-ledger/internal/store/bankfacts"
	commitmentstore "github.com/aurimasniekis/go-ledger/internal/store/commitments"
	merchantstore "github.com/aurimasniekis/go-ledger/internal/store/merchants"
	orgstore "github.com/aurimasniekis/go-ledger/internal/store/organisation"
)

type recorder struct {
	mu     sync.Mutex
	events []audit.Event
}

func (r *recorder) WriteAudit(_ context.Context, tx *sql.Tx, e audit.Event) error {
	if tx == nil {
		return errors.New("no transaction")
	}
	if err := audit.Validate(e); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *recorder) actions() []string {
	var out []string
	for _, e := range r.events {
		out = append(out, string(e.Action))
	}
	return out
}

var (
	alice  = audit.Actor{Kind: audit.ActorUser, UserID: "01JD3Y4C8V9M2Q7W5R6T0XZBNA"}
	system = audit.Actor{Kind: audit.ActorSystem}
	t0     = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
)

type fixture struct {
	t      *testing.T
	ctx    context.Context
	db     *store.DB
	now    time.Time
	facts  *bankfacts.Store
	os     *orgstore.Store
	cs     *commitmentstore.Store
	rec    *recorder
	p      *bankingtest.Provider
	engine *bankimport.Engine
	conn   banking.Connection
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "ledger.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f := &fixture{t: t, ctx: ctx, db: db, now: t0, rec: &recorder{}, facts: bankfacts.New(db, nil)}
	clock := func() time.Time { return f.now }
	f.os = orgstore.New(db, f.rec, clock, nil)
	f.cs = commitmentstore.New(db, f.rec, clock, nil)
	f.p = bankingtest.New(bankingtest.Options{Mode: banking.ModeApp, Now: clock, PageSize: 3}, bankingtest.Standard())
	f.engine = bankimport.New(f.facts, bankimport.Options{Now: clock, Sleep: func(context.Context, time.Duration) error { return nil },
		Jitter: func(d time.Duration) time.Duration { return d }})
	inst := bankingtest.Standard().Institutions[0]
	if _, err := f.p.StartConsent(ctx, banking.ConsentRequest{Institution: inst, RedirectURL: "https://ledger.example/cb", State: "s", PSUType: banking.PSUPersonal}); err != nil {
		t.Fatal(err)
	}
	conn, err := f.facts.CreateConnection(ctx, banking.NewConnection{Capabilities: f.p.Capabilities(), Institution: inst, ConsentState: "s"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := f.p.CompleteConsent(ctx, banking.ConsentCompletion{State: "s", Code: bankingtest.AuthorizationCode("s")})
	if err != nil {
		t.Fatal(err)
	}
	if f.conn, err = f.facts.ActivateConnection(ctx, conn.ID, sess, t0); err != nil {
		t.Fatal(err)
	}
	f.refresh()
	return f
}

func (f *fixture) refresh() {
	f.t.Helper()
	res, err := f.engine.Refresh(f.ctx, f.conn.ID, f.p)
	if err != nil || res.Run.Outcome != banking.RefreshSucceeded {
		f.t.Fatalf("refresh: %v %+v", err, res.Run)
	}
}

func (f *fixture) tx(description string) banking.StoredTransaction {
	f.t.Helper()
	accs, err := f.facts.Accounts(f.ctx, f.conn.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, a := range accs {
		txs, err := f.facts.Transactions(f.ctx, a.ID)
		if err != nil {
			f.t.Fatal(err)
		}
		for _, t := range txs {
			if t.Description == description {
				return t
			}
		}
	}
	f.t.Fatalf("no transaction %q", description)
	return banking.StoredTransaction{}
}

func (f *fixture) account() org.ObjectRef {
	f.t.Helper()
	accs, err := f.facts.Accounts(f.ctx, f.conn.ID)
	if err != nil || len(accs) == 0 {
		f.t.Fatalf("accounts: %v %d", err, len(accs))
	}
	return org.ObjectRef{Type: org.ObjectAccount, ID: string(accs[0].ID)}
}

func (f *fixture) commitment(name string) (commitments.Commitment, org.ObjectRef) {
	f.t.Helper()
	c, err := f.cs.Create(f.ctx, alice, commitments.Fields{Name: name, Kind: commitments.KindSubscription, Amount: money.MustParse("9.99", "EUR"),
		Schedule: commitments.Schedule{Version: 1, Frequency: commitments.FrequencyMonthly, Interval: 1, Day: 5}, StartDate: calendar.MustDate(2026, time.January, 1)})
	if err != nil {
		f.t.Fatal(err)
	}
	return c, org.ObjectRef{Type: org.ObjectCommitment, ID: c.ID}
}

func txRef(tx banking.StoredTransaction) org.ObjectRef {
	return org.ObjectRef{Type: org.ObjectTransaction, ID: string(tx.ID)}
}

func (f *fixture) def(k org.Kind, name string, parent *string) org.Definition {
	f.t.Helper()
	d, err := f.os.CreateDefinition(f.ctx, alice, k, org.Fields{Name: name, ParentID: parent})
	if err != nil {
		f.t.Fatalf("create %s %q: %v", k, name, err)
	}
	return d
}

func (f *fixture) archive(d org.Definition) org.Definition {
	f.t.Helper()
	d, err := f.os.SetArchived(f.ctx, alice, d.Kind, d.ID, d.Version, true)
	if err != nil {
		f.t.Fatal(err)
	}
	return d
}

func ptr(s string) *string { return &s }

func labelNames(a org.Assignments) []string {
	var out []string
	for _, l := range a.Labels {
		out = append(out, l.Label.Name)
	}
	return out
}

func TestDefinitionsCreateListUpdate(t *testing.T) {
	f := newFixture(t)
	home := f.def(org.KindCategory, "  Home   costs ", nil)
	if home.Name != "Home costs" || home.Version != 1 || home.ParentID != nil || home.Archived() {
		t.Fatalf("created = %+v", home)
	}
	rent := f.def(org.KindCategory, "Rent", &home.ID)
	if rent.ParentID == nil || *rent.ParentID != home.ID {
		t.Fatalf("child = %+v", rent)
	}
	// Names are unique among siblings whatever their case; the same name may
	// sit under another parent, and a label may share a category's name.
	if _, err := f.os.CreateDefinition(f.ctx, alice, org.KindCategory, org.Fields{Name: "home COSTS"}); !errors.Is(err, org.ErrDuplicateName) {
		t.Fatalf("duplicate top-level name: %v", err)
	}
	other := f.def(org.KindCategory, "Leisure", nil)
	f.def(org.KindCategory, "Rent", &other.ID)
	if _, err := f.os.CreateDefinition(f.ctx, alice, org.KindCategory, org.Fields{Name: "RENT", ParentID: &home.ID}); !errors.Is(err, org.ErrDuplicateName) {
		t.Fatalf("duplicate sibling name: %v", err)
	}
	f.def(org.KindLabel, "Home costs", nil)
	if _, err := f.os.CreateDefinition(f.ctx, alice, org.KindLabel, org.Fields{Name: "home costs"}); !errors.Is(err, org.ErrDuplicateName) {
		t.Fatalf("duplicate label: %v", err)
	}

	// Two levels only.
	var ve *commitments.ValidationError
	if _, err := f.os.CreateDefinition(f.ctx, alice, org.KindCategory, org.Fields{Name: "Deep", ParentID: &rent.ID}); !errors.As(err, &ve) || ve.Errors[0].Field != "parent_id" {
		t.Fatalf("a third level was accepted: %v", err)
	}
	if _, err := f.os.UpdateDefinition(f.ctx, alice, org.KindCategory, home.ID, 1, org.Fields{Name: "Home costs", ParentID: &other.ID}); !errors.As(err, &ve) {
		t.Fatalf("a parent with children became a child: %v", err)
	}
	if _, err := f.os.UpdateDefinition(f.ctx, alice, org.KindCategory, home.ID, 1, org.Fields{Name: "Home costs", ParentID: &home.ID}); !errors.As(err, &ve) {
		t.Fatalf("a category became its own parent: %v", err)
	}
	if _, err := f.os.CreateDefinition(f.ctx, alice, org.KindCategory, org.Fields{Name: "Ghost", ParentID: ptr(resource.NewID())}); !errors.As(err, &ve) {
		t.Fatalf("unknown parent: %v", err)
	}
	if _, err := f.os.CreateDefinition(f.ctx, alice, org.KindLabel, org.Fields{Name: "x", ParentID: &home.ID}); !errors.As(err, &ve) {
		t.Fatalf("a label with a parent: %v", err)
	}

	// Rename keeps the identity and bumps the version; stale versions conflict;
	// a no-op is not a change.
	f.now = f.now.Add(time.Hour)
	renamed, err := f.os.UpdateDefinition(f.ctx, alice, org.KindCategory, rent.ID, 1, org.Fields{Name: "Rent and mortgage", ParentID: rent.ParentID})
	if err != nil || renamed.ID != rent.ID || renamed.Version != 2 || !renamed.UpdatedAt.After(rent.UpdatedAt) {
		t.Fatalf("rename = %+v %v", renamed, err)
	}
	var vc *resource.VersionConflictError
	if _, err := f.os.UpdateDefinition(f.ctx, alice, org.KindCategory, rent.ID, 1, org.Fields{Name: "Again", ParentID: rent.ParentID}); !errors.As(err, &vc) || vc.Current != 2 {
		t.Fatalf("stale rename: %v", err)
	}
	same, err := f.os.UpdateDefinition(f.ctx, alice, org.KindCategory, rent.ID, 2, org.Fields{Name: "Rent and mortgage", ParentID: rent.ParentID})
	if err != nil || same.Version != 2 {
		t.Fatalf("no-op update = %+v %v", same, err)
	}
	moved, err := f.os.UpdateDefinition(f.ctx, alice, org.KindCategory, rent.ID, 2, org.Fields{Name: "Rent and mortgage", ParentID: &other.ID})
	if err != nil || moved.ParentID == nil || *moved.ParentID != other.ID || moved.Version != 3 {
		t.Fatalf("move = %+v %v", moved, err)
	}
	if _, err := f.os.GetDefinition(f.ctx, org.KindCategory, resource.NewID()); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("unknown get: %v", err)
	}
	if _, err := f.os.GetDefinition(f.ctx, org.KindLabel, home.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("a category found as a label: %v", err)
	}

	// Filters.
	top, err := f.os.ListDefinitions(f.ctx, org.KindCategory, org.ListFilter{Parent: ptr("")})
	if err != nil || len(top) != 2 {
		t.Fatalf("top-level = %v %v", top, err)
	}
	kids, _ := f.os.ListDefinitions(f.ctx, org.KindCategory, org.ListFilter{Parent: &other.ID})
	if len(kids) != 2 {
		t.Fatalf("children of %q = %d, want 2", other.Name, len(kids))
	}
	if got, _ := f.os.ListDefinitions(f.ctx, org.KindCategory, org.ListFilter{NamePrefix: "REN"}); len(got) != 2 {
		t.Fatalf("name prefix = %d, want 2", len(got))
	}
	if got, _ := f.os.ListDefinitions(f.ctx, org.KindCategory, org.ListFilter{NamePrefix: "%"}); len(got) != 0 {
		t.Fatalf("a LIKE wildcard in the prefix matched %d", len(got))
	}
	page, _ := f.os.ListDefinitions(f.ctx, org.KindCategory, org.ListFilter{Limit: 2})
	if len(page) != 3 || page[0].ID >= page[1].ID {
		t.Fatalf("limit+1 page = %d items", len(page))
	}
	if got, _ := f.os.ListDefinitions(f.ctx, org.KindCategory, org.ListFilter{After: page[1].ID}); len(got) != 2 {
		t.Fatalf("after = %d, want 2", len(got))
	}
}

func TestArchiveKeepsHistoryAndBlocksNewUse(t *testing.T) {
	f := newFixture(t)
	tx := f.tx("COFFEE KIOSK")
	obj := txRef(tx)
	groceries := f.def(org.KindCategory, "Groceries", nil)
	fun := f.def(org.KindLabel, "Fun", nil)
	if _, err := f.os.SetCategory(f.ctx, alice, obj, &groceries.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.os.AddLabel(f.ctx, alice, obj, fun.ID); err != nil {
		t.Fatal(err)
	}
	groceries = f.archive(groceries)
	fun = f.archive(fun)
	if !groceries.Archived() || groceries.Version != 2 {
		t.Fatalf("archived = %+v", groceries)
	}
	// History stays, and says the definition is archived.
	got, err := f.os.Get(f.ctx, obj)
	if err != nil || got.Category == nil || got.Category.Category == nil || got.Category.Category.ID != groceries.ID || !got.Category.Category.Archived() ||
		len(got.Labels) != 1 || !got.Labels[0].Label.Archived() {
		t.Fatalf("history after archive = %+v %v", got, err)
	}
	// Nothing new may use it, by the person or automatically.
	other := txRef(f.tx("STREAMING SERVICE"))
	if _, err := f.os.SetCategory(f.ctx, alice, other, &groceries.ID); !errors.Is(err, org.ErrArchived) {
		t.Fatalf("assigning an archived category: %v", err)
	}
	if _, err := f.os.AddLabel(f.ctx, alice, other, fun.ID); !errors.Is(err, org.ErrArchived) {
		t.Fatalf("adding an archived label: %v", err)
	}
	if _, err := f.os.CreateDefinition(f.ctx, alice, org.KindCategory, org.Fields{Name: "Sub", ParentID: &groceries.ID}); !errors.Is(err, org.ErrArchived) {
		t.Fatalf("a child under an archived parent: %v", err)
	}
	res, err := f.os.ApplyAutomatic(f.ctx, system, other, org.Automatic{Origin: org.OriginRule, Source: "r", CategoryID: &groceries.ID, LabelIDs: []string{fun.ID}})
	if err != nil || res.Changed() || len(res.Kept) != 2 || res.Kept[0].Reason != org.KeptArchived || res.Kept[1].Reason != org.KeptArchived {
		t.Fatalf("automatic use of archived definitions = %+v %v", res, err)
	}
	// Archiving twice changes nothing; unarchiving restores use.
	again, err := f.os.SetArchived(f.ctx, alice, org.KindCategory, groceries.ID, groceries.Version, true)
	if err != nil || again.Version != groceries.Version {
		t.Fatalf("archive twice = %+v %v", again, err)
	}
	back, err := f.os.SetArchived(f.ctx, alice, org.KindCategory, groceries.ID, groceries.Version, false)
	if err != nil || back.Archived() {
		t.Fatalf("unarchive = %+v %v", back, err)
	}
	if _, err := f.os.SetCategory(f.ctx, alice, other, &groceries.ID); err != nil {
		t.Fatalf("assigning after unarchive: %v", err)
	}
	var vc *resource.VersionConflictError
	if _, err := f.os.SetArchived(f.ctx, alice, org.KindCategory, groceries.ID, 1, true); !errors.As(err, &vc) {
		t.Fatalf("archive with a stale version: %v", err)
	}
	// An archived definition can be renamed, which frees its name.
	fun, err = f.os.UpdateDefinition(f.ctx, alice, org.KindLabel, fun.ID, fun.Version, org.Fields{Name: "Fun (old)"})
	if err != nil || !fun.Archived() {
		t.Fatalf("rename archived = %+v %v", fun, err)
	}
	f.def(org.KindLabel, "Fun", nil)
	archivedOnly := true
	if got, _ := f.os.ListDefinitions(f.ctx, org.KindLabel, org.ListFilter{Archived: &archivedOnly}); len(got) != 1 {
		t.Fatalf("archived filter = %d", len(got))
	}
	active := false
	if got, _ := f.os.ListDefinitions(f.ctx, org.KindLabel, org.ListFilter{Archived: &active}); len(got) != 1 {
		t.Fatalf("active filter = %d", len(got))
	}
}

func TestCategoryHierarchyArchiveOrder(t *testing.T) {
	f := newFixture(t)
	parent := f.def(org.KindCategory, "Home", nil)
	child := f.def(org.KindCategory, "Rent", &parent.ID)
	if _, err := f.os.SetArchived(f.ctx, alice, org.KindCategory, parent.ID, parent.Version, true); !errors.Is(err, org.ErrHasActiveChildren) {
		t.Fatalf("archive a parent with an active child: %v", err)
	}
	child = f.archive(child)
	parent = f.archive(parent)
	if _, err := f.os.SetArchived(f.ctx, alice, org.KindCategory, child.ID, child.Version, false); !errors.Is(err, org.ErrParentArchived) {
		t.Fatalf("unarchive under an archived parent: %v", err)
	}
	if _, err := f.os.UpdateDefinition(f.ctx, alice, org.KindCategory, child.ID, child.Version, org.Fields{Name: "Rent", ParentID: nil}); err != nil {
		t.Fatalf("moving an archived child to the top: %v", err)
	}
	if _, err := f.os.SetArchived(f.ctx, alice, org.KindCategory, parent.ID, parent.Version, false); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteOnlyWhenUnusedOrReassigned(t *testing.T) {
	f := newFixture(t)
	a, b := txRef(f.tx("COFFEE KIOSK")), txRef(f.tx("STREAMING SERVICE"))
	_, c := f.commitment("Streaming")
	old, repl := f.def(org.KindCategory, "Old", nil), f.def(org.KindCategory, "New", nil)
	for _, o := range []org.ObjectRef{a, b, c} {
		if _, err := f.os.SetCategory(f.ctx, alice, o, &old.ID); err != nil {
			t.Fatal(err)
		}
	}
	var inUse *org.InUseError
	if _, err := f.os.DeleteDefinition(f.ctx, alice, org.KindCategory, old.ID, old.Version, ""); !errors.As(err, &inUse) || inUse.Assignments != 3 {
		t.Fatalf("delete of an assigned category: %v", err)
	}
	if _, err := f.os.GetDefinition(f.ctx, org.KindCategory, old.ID); err != nil {
		t.Fatalf("a refused delete removed the category: %v", err)
	}
	var ve *commitments.ValidationError
	if _, err := f.os.DeleteDefinition(f.ctx, alice, org.KindCategory, old.ID, old.Version, old.ID); !errors.As(err, &ve) {
		t.Fatalf("reassign to itself: %v", err)
	}
	if _, err := f.os.DeleteDefinition(f.ctx, alice, org.KindCategory, old.ID, old.Version, resource.NewID()); !errors.As(err, &ve) {
		t.Fatalf("reassign to nothing: %v", err)
	}
	lbl := f.def(org.KindLabel, "L", nil)
	if _, err := f.os.DeleteDefinition(f.ctx, alice, org.KindCategory, old.ID, old.Version, lbl.ID); !errors.As(err, &ve) {
		t.Fatalf("reassign to another kind: %v", err)
	}
	archived := f.archive(f.def(org.KindCategory, "Archived", nil))
	if _, err := f.os.DeleteDefinition(f.ctx, alice, org.KindCategory, old.ID, old.Version, archived.ID); !errors.Is(err, org.ErrArchived) {
		t.Fatalf("reassign to an archived category: %v", err)
	}
	var vc *resource.VersionConflictError
	if _, err := f.os.DeleteDefinition(f.ctx, alice, org.KindCategory, old.ID, 9, repl.ID); !errors.As(err, &vc) {
		t.Fatalf("delete with a stale version: %v", err)
	}
	res, err := f.os.DeleteDefinition(f.ctx, alice, org.KindCategory, old.ID, old.Version, repl.ID)
	if err != nil || res.Reassigned != 3 {
		t.Fatalf("delete with reassignment = %+v %v", res, err)
	}
	for _, o := range []org.ObjectRef{a, b, c} {
		got, err := f.os.Get(f.ctx, o)
		if err != nil || got.Category == nil || got.Category.Category == nil || got.Category.Category.ID != repl.ID || got.Category.Origin != org.OriginUser {
			t.Fatalf("after reassignment %v = %+v %v", o, got.Category, err)
		}
	}
	if _, err := f.os.GetDefinition(f.ctx, org.KindCategory, old.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("deleted category still found: %v", err)
	}
	// An unused one is simply deleted.
	if res, err := f.os.DeleteDefinition(f.ctx, alice, org.KindCategory, archived.ID, archived.Version, ""); err != nil || res.Reassigned != 0 {
		t.Fatalf("delete unused = %+v %v", res, err)
	}
	// A category with children is never deleted.
	parent := f.def(org.KindCategory, "Parent", nil)
	f.def(org.KindCategory, "Child", &parent.ID)
	if _, err := f.os.DeleteDefinition(f.ctx, alice, org.KindCategory, parent.ID, parent.Version, ""); !errors.Is(err, org.ErrHasChildren) {
		t.Fatalf("delete a parent: %v", err)
	}
	if _, err := f.os.DeleteDefinition(f.ctx, alice, org.KindCategory, parent.ID, parent.Version, repl.ID); !errors.Is(err, org.ErrHasChildren) {
		t.Fatalf("delete a parent with reassignment: %v", err)
	}
}

func TestDeleteLabelAndBucketReassignment(t *testing.T) {
	f := newFixture(t)
	a, b := txRef(f.tx("COFFEE KIOSK")), txRef(f.tx("STREAMING SERVICE"))
	oldL, newL := f.def(org.KindLabel, "Old", nil), f.def(org.KindLabel, "New", nil)
	for _, o := range []org.ObjectRef{a, b} {
		if _, err := f.os.AddLabel(f.ctx, alice, o, oldL.ID); err != nil {
			t.Fatal(err)
		}
	}
	// a already has the replacement; b's person removed it.
	if _, err := f.os.AddLabel(f.ctx, alice, a, newL.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.os.AddLabel(f.ctx, alice, b, newL.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.os.RemoveLabel(f.ctx, alice, b, newL.ID); err != nil {
		t.Fatal(err)
	}
	// A removal of the old label is remembered, and goes with it.
	third := txRef(f.tx("CARD PURCHASE LONDON"))
	if _, err := f.os.AddLabel(f.ctx, alice, third, oldL.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.os.RemoveLabel(f.ctx, alice, third, oldL.ID); err != nil {
		t.Fatal(err)
	}
	var inUse *org.InUseError
	if _, err := f.os.DeleteDefinition(f.ctx, alice, org.KindLabel, oldL.ID, oldL.Version, ""); !errors.As(err, &inUse) || inUse.Assignments != 2 {
		t.Fatalf("delete of an assigned label: %v", err)
	}
	res, err := f.os.DeleteDefinition(f.ctx, alice, org.KindLabel, oldL.ID, oldL.Version, newL.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Reassigned != 0 {
		// a keeps its own row; b's person removed the replacement, which stands.
		t.Fatalf("reassigned = %d, want 0", res.Reassigned)
	}
	ga, _ := f.os.Get(f.ctx, a)
	gb, _ := f.os.Get(f.ctx, b)
	if !slices.Equal(labelNames(ga), []string{"New"}) || len(gb.Labels) != 0 || len(gb.Removed) != 1 {
		t.Fatalf("after label delete: a=%v b=%+v", labelNames(ga), gb)
	}
	if g, _ := f.os.Get(f.ctx, third); len(g.Labels) != 0 || len(g.Removed) != 0 {
		t.Fatalf("the deleted label's removal marker stayed: %+v", g)
	}

	// Buckets: members move to the replacement.
	_, c1 := f.commitment("One")
	_, c2 := f.commitment("Two")
	bo, bn := f.def(org.KindBucket, "Old", nil), f.def(org.KindBucket, "New", nil)
	for _, o := range []org.ObjectRef{c1, c2} {
		if err := f.os.AddToBucket(f.ctx, alice, bo.ID, o); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.os.AddToBucket(f.ctx, alice, bn.ID, c2); err != nil {
		t.Fatal(err)
	}
	if _, err := f.os.DeleteDefinition(f.ctx, alice, org.KindBucket, bo.ID, bo.Version, ""); !errors.As(err, &inUse) || inUse.Assignments != 2 {
		t.Fatalf("delete of a bucket with members: %v", err)
	}
	if res, err := f.os.DeleteDefinition(f.ctx, alice, org.KindBucket, bo.ID, bo.Version, bn.ID); err != nil || res.Reassigned != 1 {
		t.Fatalf("bucket delete = %+v %v", res, err)
	}
	members, _ := f.os.ListMembers(f.ctx, bn.ID, org.MemberFilter{})
	if len(members) != 2 {
		t.Fatalf("members after delete = %d, want 2", len(members))
	}
}

func TestCategorySingleValuedAndOrigins(t *testing.T) {
	f := newFixture(t)
	obj := txRef(f.tx("COFFEE KIOSK"))
	food, fun := f.def(org.KindCategory, "Food", nil), f.def(org.KindCategory, "Fun", nil)
	got, err := f.os.Get(f.ctx, obj)
	if err != nil || got.Category != nil || len(got.Labels) != 0 {
		t.Fatalf("empty = %+v %v", got, err)
	}
	// Single-valued: a second assignment replaces the first.
	if _, err := f.os.SetCategory(f.ctx, alice, obj, &food.ID); err != nil {
		t.Fatal(err)
	}
	got, err = f.os.SetCategory(f.ctx, alice, obj, &fun.ID)
	if err != nil || got.Category.Category.ID != fun.ID || got.Category.Origin != org.OriginUser || got.Category.By.UserID != alice.UserID {
		t.Fatalf("replaced = %+v %v", got.Category, err)
	}
	var n int
	_ = f.db.ReadTx(f.ctx, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT count(*) FROM category_assignments").Scan(&n)
	})
	if n != 1 {
		t.Fatalf("%d category rows for one object", n)
	}
	// The person's "none" is an assignment, and reset forgets it.
	got, err = f.os.SetCategory(f.ctx, alice, obj, nil)
	if err != nil || got.Category == nil || got.Category.Category != nil || got.Category.Origin != org.OriginUser {
		t.Fatalf("none = %+v %v", got.Category, err)
	}
	got, err = f.os.ResetCategory(f.ctx, alice, obj)
	if err != nil || got.Category != nil {
		t.Fatalf("reset = %+v %v", got.Category, err)
	}
	if _, err := f.os.ResetCategory(f.ctx, alice, obj); !errors.Is(err, org.ErrNoAssignment) {
		t.Fatalf("second reset: %v", err)
	}
	var ve *commitments.ValidationError
	if _, err := f.os.SetCategory(f.ctx, alice, obj, ptr(resource.NewID())); !errors.As(err, &ve) {
		t.Fatalf("unknown category: %v", err)
	}
	if _, err := f.os.SetCategory(f.ctx, alice, org.ObjectRef{Type: org.ObjectTransaction, ID: resource.NewID()}, &food.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("unknown object: %v", err)
	}
	if _, err := f.os.SetCategory(f.ctx, alice, org.ObjectRef{Type: "widget", ID: resource.NewID()}, &food.ID); !errors.As(err, &ve) {
		t.Fatalf("unknown object type: %v", err)
	}
}

func TestAutomaticNeverOverridesTheUser(t *testing.T) {
	f := newFixture(t)
	obj := txRef(f.tx("COFFEE KIOSK"))
	food, fun := f.def(org.KindCategory, "Food", nil), f.def(org.KindCategory, "Fun", nil)
	l1, l2, l3 := f.def(org.KindLabel, "L1", nil), f.def(org.KindLabel, "L2", nil), f.def(org.KindLabel, "L3", nil)
	model := org.Automatic{Origin: org.OriginModel, Source: "provider/model-1", CategoryID: &food.ID, LabelIDs: []string{l1.ID}}
	rule := org.Automatic{Origin: org.OriginRule, Source: "rule:coffee", CategoryID: &fun.ID, LabelIDs: []string{l1.ID, l2.ID}}

	res, err := f.os.ApplyAutomatic(f.ctx, system, obj, model)
	if err != nil || !res.CategoryApplied || len(res.LabelsApplied) != 1 {
		t.Fatalf("model = %+v %v", res, err)
	}
	// The same proposal again changes nothing (and writes no audit event).
	before := len(f.rec.events)
	if res, _ = f.os.ApplyAutomatic(f.ctx, system, obj, model); res.Changed() || len(f.rec.events) != before {
		t.Fatalf("repeat changed something: %+v", res)
	}
	// A rule outranks a model...
	res, err = f.os.ApplyAutomatic(f.ctx, system, obj, rule)
	if err != nil || !res.CategoryApplied || len(res.LabelsApplied) != 2 {
		t.Fatalf("rule = %+v %v", res, err)
	}
	// ...and a model cannot take it back.
	res, _ = f.os.ApplyAutomatic(f.ctx, system, obj, model)
	if res.Changed() || len(res.Kept) != 2 {
		t.Fatalf("model over rule = %+v", res)
	}
	got, _ := f.os.Get(f.ctx, obj)
	if got.Category.Category.ID != fun.ID || got.Category.Origin != org.OriginRule || got.Category.Source != "rule:coffee" || got.Category.By.Kind != audit.ActorSystem {
		t.Fatalf("category = %+v", got.Category)
	}

	// The person's correction wins over every automatic run, rule or model.
	if _, err := f.os.SetCategory(f.ctx, alice, obj, &food.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.os.RemoveLabel(f.ctx, alice, obj, l2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.os.AddLabel(f.ctx, alice, obj, l3.ID); err != nil {
		t.Fatal(err)
	}
	for _, a := range []org.Automatic{rule, model, {Origin: org.OriginRule, Source: "rule:other", CategoryID: &fun.ID, LabelIDs: []string{l2.ID, l3.ID}}} {
		res, err := f.os.ApplyAutomatic(f.ctx, system, obj, a)
		if err != nil {
			t.Fatal(err)
		}
		if res.CategoryApplied || slices.Contains(res.LabelsApplied, l2.ID) || slices.Contains(res.LabelsApplied, l3.ID) {
			t.Fatalf("an automatic run changed a user decision: %+v", res)
		}
	}
	got, _ = f.os.Get(f.ctx, obj)
	if got.Category.Category.ID != food.ID || got.Category.Origin != org.OriginUser || !slices.Equal(labelNames(got), []string{"L1", "L3"}) ||
		len(got.Removed) != 1 || got.Removed[0].Label.ID != l2.ID {
		t.Fatalf("after automatic runs: %+v removed %+v", got.Category, got.Removed)
	}
	// A cleared category is a decision too.
	other := txRef(f.tx("STREAMING SERVICE"))
	if _, err := f.os.SetCategory(f.ctx, alice, other, nil); err != nil {
		t.Fatal(err)
	}
	if res, _ := f.os.ApplyAutomatic(f.ctx, system, other, rule); res.CategoryApplied {
		t.Fatal("a rule assigned a category the person cleared")
	}
	// Until the person hands it back.
	if _, err := f.os.ResetCategory(f.ctx, alice, other); err != nil {
		t.Fatal(err)
	}
	if res, _ := f.os.ApplyAutomatic(f.ctx, system, other, rule); !res.CategoryApplied {
		t.Fatal("a rule could not assign after the person reset the category")
	}
	// The person can re-add a removed label, and then it is theirs.
	if _, err := f.os.AddLabel(f.ctx, alice, obj, l2.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = f.os.Get(f.ctx, obj)
	if !slices.Equal(labelNames(got), []string{"L1", "L2", "L3"}) || len(got.Removed) != 0 {
		t.Fatalf("after re-adding: %v", labelNames(got))
	}
	// A label the person removed that never had an automatic row stays removed.
	if _, err := f.os.RemoveLabel(f.ctx, alice, obj, l3.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.os.RemoveLabel(f.ctx, alice, obj, l3.ID); !errors.Is(err, org.ErrNoAssignment) {
		t.Fatalf("removing a removed label: %v", err)
	}
	var ve *commitments.ValidationError
	if _, err := f.os.ApplyAutomatic(f.ctx, system, obj, org.Automatic{Origin: org.OriginUser, Source: "x"}); !errors.As(err, &ve) {
		t.Fatalf("a user-origin automatic proposal: %v", err)
	}
	if _, err := f.os.ApplyAutomatic(f.ctx, system, obj, org.Automatic{Origin: org.OriginRule, Source: "x", LabelIDs: []string{resource.NewID()}}); !errors.As(err, &ve) {
		t.Fatalf("an unknown label: %v", err)
	}
}

func bankSnapshot(t *testing.T, f *fixture) string {
	t.Helper()
	var s string
	err := f.db.ReadTx(f.ctx, func(ctx context.Context, q *sql.Tx) error {
		return q.QueryRowContext(ctx, "SELECT group_concat(id || '|' || source_amount || '|' || source_currency || '|' || raw_description || '|' || counterparty_name || '|' || ifnull(entry_reference,'') || '|' || booking_status, ';') FROM (SELECT * FROM bank_transactions ORDER BY id)").Scan(&s)
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestRefreshDoesNotAlterAssignments is the acceptance criterion of O1: a bank
// refresh, a re-import and a pending item booking in place leave what the
// person assigned exactly as it was, and assigning changes no bank fact.
func TestRefreshDoesNotAlterAssignments(t *testing.T) {
	f := newFixture(t)
	pending := f.tx("CARD PURCHASE BOOKSHOP")
	obj := txRef(pending)
	books, extra := f.def(org.KindCategory, "Books", nil), f.def(org.KindCategory, "Other", nil)
	lbl := f.def(org.KindLabel, "Gift", nil)
	removed := f.def(org.KindLabel, "Auto", nil)
	bank := bankSnapshot(t, f)
	if _, err := f.os.SetCategory(f.ctx, alice, obj, &books.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.os.AddLabel(f.ctx, alice, obj, lbl.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.os.ApplyAutomatic(f.ctx, system, obj, org.Automatic{Origin: org.OriginRule, Source: "r", LabelIDs: []string{removed.ID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.os.RemoveLabel(f.ctx, alice, obj, removed.ID); err != nil {
		t.Fatal(err)
	}
	if bankSnapshot(t, f) != bank {
		t.Fatal("assigning changed a bank fact")
	}
	want, _ := f.os.Get(f.ctx, obj)

	f.now = f.now.Add(2 * time.Hour)
	f.refresh()
	f.p.Mutate(bankingtest.BookPending)
	f.now = f.now.Add(2 * time.Hour)
	f.refresh()
	after := f.tx("CARD PURCHASE BOOKSHOP")
	if after.ID != pending.ID || after.Status != banking.StatusBooked {
		t.Fatalf("the re-import did not update the row in place: %+v", after)
	}
	got, err := f.os.Get(f.ctx, obj)
	if err != nil {
		t.Fatal(err)
	}
	if !equalAssignments(got, want) {
		t.Fatalf("assignments changed by the refresh:\nbefore %+v\nafter  %+v", want, got)
	}
	// An automatic run after the refresh still cannot override them.
	res, _ := f.os.ApplyAutomatic(f.ctx, system, obj, org.Automatic{Origin: org.OriginRule, Source: "r", CategoryID: &extra.ID, LabelIDs: []string{removed.ID}})
	if res.Changed() {
		t.Fatalf("a rule changed user assignments after the refresh: %+v", res)
	}
}

func equalAssignments(a, b org.Assignments) bool {
	key := func(x org.Assignments) string {
		var sb strings.Builder
		if c := x.Category; c != nil {
			sb.WriteString("cat:" + string(c.Origin) + ":" + c.At.String())
			if c.Category != nil {
				sb.WriteString(":" + c.Category.ID)
			}
		}
		for _, l := range x.Labels {
			sb.WriteString("|l:" + l.Label.ID + ":" + string(l.Origin) + ":" + l.At.String())
		}
		for _, l := range x.Removed {
			sb.WriteString("|r:" + l.Label.ID + ":" + string(l.Origin) + ":" + l.At.String())
		}
		return sb.String()
	}
	return key(a) == key(b)
}

func (f *fixture) link(old, booked banking.TransactionID) {
	f.t.Helper()
	err := f.db.WriteTx(f.ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE bank_transactions SET superseded_by = ? WHERE id = ?", string(booked), string(old))
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
}

func TestAssignmentsSurviveSupersededPending(t *testing.T) {
	f := newFixture(t)
	pending := f.tx("CARD PURCHASE BOOKSHOP")
	booked := f.tx("CARD PURCHASE LONDON")
	food, fun := f.def(org.KindCategory, "Food", nil), f.def(org.KindCategory, "Fun", nil)
	keep, drop := f.def(org.KindLabel, "Keep", nil), f.def(org.KindLabel, "Drop", nil)
	if _, err := f.os.SetCategory(f.ctx, alice, txRef(pending), &food.ID); err != nil {
		t.Fatal(err)
	}
	for _, l := range []org.Definition{keep, drop} {
		if _, err := f.os.AddLabel(f.ctx, alice, txRef(pending), l.ID); err != nil {
			t.Fatal(err)
		}
	}
	f.link(pending.ID, booked.ID)
	got, err := f.os.Get(f.ctx, txRef(booked))
	if err != nil || got.Category == nil || got.Category.Category.ID != food.ID || !got.Category.Inherited || len(got.Labels) != 2 || !got.Labels[0].Inherited {
		t.Fatalf("booking did not inherit: %+v %v", got, err)
	}
	// A rule cannot override an inherited user decision.
	if res, _ := f.os.ApplyAutomatic(f.ctx, system, txRef(booked), org.Automatic{Origin: org.OriginRule, Source: "r", CategoryID: &fun.ID, LabelIDs: []string{keep.ID}}); res.Changed() {
		t.Fatalf("a rule overrode an inherited decision: %+v", res)
	}
	// The booking's own decision replaces the inherited one, and removing an
	// inherited label hides it on the booking only.
	if _, err := f.os.SetCategory(f.ctx, alice, txRef(booked), &fun.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.os.RemoveLabel(f.ctx, alice, txRef(booked), drop.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = f.os.Get(f.ctx, txRef(booked))
	if got.Category.Category.ID != fun.ID || got.Category.Inherited || !slices.Equal(labelNames(got), []string{"Keep"}) || len(got.Removed) != 1 {
		t.Fatalf("booking's own decision: %+v %v", got.Category, labelNames(got))
	}
	if p, _ := f.os.Get(f.ctx, txRef(pending)); p.Category.Category.ID != food.ID || len(p.Labels) != 2 {
		t.Fatalf("the pending row's own assignments changed: %+v", p)
	}
	// Resetting the booking's category lets the inherited one apply again.
	if got, _ = f.os.ResetCategory(f.ctx, alice, txRef(booked)); got.Category.Category.ID != food.ID || !got.Category.Inherited {
		t.Fatalf("after reset: %+v", got.Category)
	}
}

// A booking can be linked to its pending row after it was imported and
// classified automatically. The person's inherited decision (category, label
// and a removal) still wins over the booking's own rule or model rows (F1).
func TestInheritedUserDecisionBeatsOwnAutomaticRows(t *testing.T) {
	f := newFixture(t)
	pending := f.tx("CARD PURCHASE BOOKSHOP")
	booked := f.tx("CARD PURCHASE LONDON")
	food, fun := f.def(org.KindCategory, "Food", nil), f.def(org.KindCategory, "Fun", nil)
	keep, drop, other := f.def(org.KindLabel, "Keep", nil), f.def(org.KindLabel, "Drop", nil), f.def(org.KindLabel, "Other", nil)
	if _, err := f.os.SetCategory(f.ctx, alice, txRef(pending), &food.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.os.AddLabel(f.ctx, alice, txRef(pending), drop.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.os.AddLabel(f.ctx, alice, txRef(pending), keep.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.os.RemoveLabel(f.ctx, alice, txRef(pending), keep.ID); err != nil {
		t.Fatal(err)
	}
	// The booking was classified by a model before it was linked.
	if res, err := f.os.ApplyAutomatic(f.ctx, system, txRef(booked), org.Automatic{Origin: org.OriginModel, Source: "m", CategoryID: &fun.ID, LabelIDs: []string{keep.ID, drop.ID, other.ID}}); err != nil || !res.Changed() {
		t.Fatalf("model run: %+v %v", res, err)
	}
	f.link(pending.ID, booked.ID)
	got, err := f.os.Get(f.ctx, txRef(booked))
	if err != nil || got.Category == nil || got.Category.Category.ID != food.ID || got.Category.Origin != org.OriginUser || !got.Category.Inherited {
		t.Fatalf("category: %+v %v", got.Category, err)
	}
	if names := labelNames(got); !slices.Equal(names, []string{"Drop", "Other"}) {
		t.Fatalf("labels = %v, want Drop (inherited, the person's) and Other (the model's own)", names)
	}
	if len(got.Removed) != 1 || got.Removed[0].Label.ID != keep.ID || got.Removed[0].Origin != org.OriginUser || !got.Removed[0].Inherited {
		t.Fatalf("the person's removal of Keep did not survive: %+v", got.Removed)
	}
	// The own row of a stronger origin still wins: a rule beats an inherited model row.
	pending2, booked2 := f.tx("COFFEE KIOSK"), f.tx("STREAMING SERVICE")
	if _, err := f.os.ApplyAutomatic(f.ctx, system, txRef(pending2), org.Automatic{Origin: org.OriginModel, Source: "m", CategoryID: &food.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.os.ApplyAutomatic(f.ctx, system, txRef(booked2), org.Automatic{Origin: org.OriginRule, Source: "r", CategoryID: &fun.ID}); err != nil {
		t.Fatal(err)
	}
	f.link(pending2.ID, booked2.ID)
	if got, _ = f.os.Get(f.ctx, txRef(booked2)); got.Category.Category.ID != fun.ID || got.Category.Inherited {
		t.Fatalf("an own rule row lost to an inherited model row: %+v", got.Category)
	}
}

// Resetting a category the booking only inherits is a 409-class error, not
// "nothing there" (F4).
func TestResetOfAnInheritedCategoryIsRefused(t *testing.T) {
	f := newFixture(t)
	pending, booked := f.tx("CARD PURCHASE BOOKSHOP"), f.tx("CARD PURCHASE LONDON")
	food := f.def(org.KindCategory, "Food", nil)
	if _, err := f.os.SetCategory(f.ctx, alice, txRef(pending), &food.ID); err != nil {
		t.Fatal(err)
	}
	f.link(pending.ID, booked.ID)
	if _, err := f.os.ResetCategory(f.ctx, alice, txRef(booked)); !errors.Is(err, org.ErrInheritedAssignment) {
		t.Fatalf("reset of an inherited category: %v", err)
	}
	if _, err := f.os.ResetCategory(f.ctx, alice, txRef(f.tx("COFFEE KIOSK"))); !errors.Is(err, org.ErrNoAssignment) {
		t.Fatalf("reset with nothing assigned: %v", err)
	}
}

// Reassigning on delete moves the person's rows as the person's; a rule's or
// model's row keeps origin, source, who and when (F3, decision C-O1-reassign).
func TestReassignKeepsAutomaticProvenance(t *testing.T) {
	f := newFixture(t)
	a, b, c := txRef(f.tx("COFFEE KIOSK")), txRef(f.tx("STREAMING SERVICE")), txRef(f.tx("CARD PURCHASE LONDON"))
	oldC, newC := f.def(org.KindCategory, "Old", nil), f.def(org.KindCategory, "New", nil)
	oldL, newL := f.def(org.KindLabel, "OldL", nil), f.def(org.KindLabel, "NewL", nil)
	if _, err := f.os.SetCategory(f.ctx, alice, a, &oldC.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.os.AddLabel(f.ctx, alice, a, oldL.ID); err != nil {
		t.Fatal(err)
	}
	for o, origin := range map[org.ObjectRef]org.Origin{b: org.OriginModel, c: org.OriginRule} {
		if _, err := f.os.ApplyAutomatic(f.ctx, system, o, org.Automatic{Origin: origin, Source: "src-" + string(origin), CategoryID: &oldC.ID, LabelIDs: []string{oldL.ID}}); err != nil {
			t.Fatal(err)
		}
	}
	before := map[org.ObjectRef]org.Assignments{}
	for _, o := range []org.ObjectRef{a, b, c} {
		before[o], _ = f.os.Get(f.ctx, o)
	}
	f.now = f.now.Add(time.Hour)
	bob := audit.Actor{Kind: audit.ActorUser, UserID: "01JD3Y4C8V9M2Q7W5R6T0XZBNB"}
	if _, err := f.os.DeleteDefinition(f.ctx, bob, org.KindCategory, oldC.ID, oldC.Version, newC.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.os.DeleteDefinition(f.ctx, bob, org.KindLabel, oldL.ID, oldL.Version, newL.ID); err != nil {
		t.Fatal(err)
	}
	for _, o := range []org.ObjectRef{a, b, c} {
		was, now := before[o], mustGet(t, f, o)
		if now.Category.Category.ID != newC.ID || len(now.Labels) != 1 || now.Labels[0].Label.ID != newL.ID {
			t.Fatalf("%v not moved: %+v", o, now)
		}
		if o == a {
			if now.Category.Origin != org.OriginUser || now.Category.By.UserID != bob.UserID || !now.Category.At.After(was.Category.At) || now.Labels[0].By.UserID != bob.UserID {
				t.Fatalf("the person's rows should move as the mover's: %+v", now)
			}
			continue
		}
		wc, wl := was.Category, was.Labels[0]
		if now.Category.Origin != wc.Origin || now.Category.Source != wc.Source || now.Category.By != wc.By || !now.Category.At.Equal(wc.At) ||
			now.Labels[0].Origin != wl.Origin || now.Labels[0].Source != wl.Source || now.Labels[0].By != wl.By || !now.Labels[0].At.Equal(wl.At) {
			t.Fatalf("%v automatic provenance changed: before %+v %+v, after %+v %+v", o, wc, wl, now.Category, now.Labels[0])
		}
	}
}

func mustGet(t *testing.T, f *fixture, o org.ObjectRef) org.Assignments {
	t.Helper()
	got, err := f.os.Get(f.ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestBucketsHoldCommitmentsAndAccountsOnly(t *testing.T) {
	f := newFixture(t)
	bucket := f.def(org.KindBucket, "Essentials", nil)
	_, c := f.commitment("Rent")
	acc := f.account()
	if err := f.os.AddToBucket(f.ctx, alice, bucket.ID, c); err != nil {
		t.Fatal(err)
	}
	if err := f.os.AddToBucket(f.ctx, alice, bucket.ID, c); err != nil {
		t.Fatalf("adding twice: %v", err)
	}
	if err := f.os.AddToBucket(f.ctx, alice, bucket.ID, acc); err != nil {
		t.Fatal(err)
	}
	for _, o := range []org.ObjectRef{txRef(f.tx("COFFEE KIOSK")), {Type: org.ObjectMerchant, ID: resource.NewID()}} {
		if err := f.os.AddToBucket(f.ctx, alice, bucket.ID, o); !errors.Is(err, org.ErrNotApplicable) {
			t.Fatalf("a %s in a bucket: %v", o.Type, err)
		}
	}
	if err := f.os.AddToBucket(f.ctx, alice, bucket.ID, org.ObjectRef{Type: org.ObjectCommitment, ID: resource.NewID()}); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("an unknown commitment: %v", err)
	}
	got, _ := f.os.Get(f.ctx, c)
	if len(got.Buckets) != 1 || got.Buckets[0].ID != bucket.ID {
		t.Fatalf("buckets of the commitment = %+v", got.Buckets)
	}
	members, err := f.os.ListMembers(f.ctx, bucket.ID, org.MemberFilter{Limit: 1})
	if err != nil || len(members) != 2 || members[0].Object.Type != org.ObjectAccount {
		t.Fatalf("members = %+v %v", members, err)
	}
	rest, _ := f.os.ListMembers(f.ctx, bucket.ID, org.MemberFilter{After: string(members[0].Object.Type) + ":" + members[0].Object.ID})
	if len(rest) != 1 || rest[0].Object != c {
		t.Fatalf("second page = %+v", rest)
	}
	// An archived bucket keeps its members and refuses new ones; members can leave.
	bucket = f.archive(bucket)
	if err := f.os.AddToBucket(f.ctx, alice, bucket.ID, c); !errors.Is(err, org.ErrArchived) {
		t.Fatalf("adding to an archived bucket: %v", err)
	}
	if got, _ := f.os.Get(f.ctx, c); len(got.Buckets) != 1 || !got.Buckets[0].Archived() {
		t.Fatalf("history of an archived bucket = %+v", got.Buckets)
	}
	if err := f.os.RemoveFromBucket(f.ctx, alice, bucket.ID, c); err != nil {
		t.Fatal(err)
	}
	if err := f.os.RemoveFromBucket(f.ctx, alice, bucket.ID, c); !errors.Is(err, org.ErrNoAssignment) {
		t.Fatalf("removing twice: %v", err)
	}
	// Buckets carry no money: nothing in the schema refers to an amount.
	_ = f.db.ReadTx(f.ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT m.name FROM sqlite_schema s, pragma_table_info(s.name) m WHERE s.type = 'table' AND s.name IN ('buckets', 'bucket_members', 'categories', 'labels', 'category_assignments', 'label_assignments') AND (m.name LIKE '%amount%' OR m.name LIKE '%currency%' OR m.name LIKE '%status%')")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var n string
			_ = rows.Scan(&n)
			t.Errorf("organisation table has money or status column %q", n)
		}
		return nil
	})
}

// Merchant assignments key on the merchant record's id (task M1, ADR-0057):
// renaming the merchant or adding a spelling keeps them, a name is no object
// id, an archived merchant gets nothing new, and the audit target is the id.
func TestMerchantsAreAssignableByID(t *testing.T) {
	f := newFixture(t)
	ms := merchantstore.New(f.db, f.rec, func() time.Time { return f.now })
	m, err := ms.CreateEntity(f.ctx, alice, "Acme Shop", nil)
	if err != nil {
		t.Fatal(err)
	}
	obj := org.ObjectRef{Type: org.ObjectMerchant, ID: m.ID}
	cat, lbl := f.def(org.KindCategory, "Shopping", nil), f.def(org.KindLabel, "Online", nil)
	if _, err := f.os.SetCategory(f.ctx, alice, obj, &cat.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.os.AddLabel(f.ctx, alice, obj, lbl.ID); err != nil {
		t.Fatal(err)
	}
	if m, err = ms.RenameEntity(f.ctx, alice, m.ID, m.Version, "ACME Online"); err != nil {
		t.Fatal(err)
	}
	if _, err = ms.AddAlias(f.ctx, alice, m.ID, "Acme Shop Vilnius"); err != nil {
		t.Fatal(err)
	}
	got, err := f.os.Get(f.ctx, obj)
	if err != nil || got.Category == nil || got.Category.Category == nil || got.Category.Category.ID != cat.ID || len(got.Labels) != 1 {
		t.Fatalf("merchant after rename and a new alias = %+v %v", got, err)
	}
	var ve *commitments.ValidationError
	if _, err := f.os.SetCategory(f.ctx, alice, org.ObjectRef{Type: org.ObjectMerchant, ID: "acme shop"}, &cat.ID); !errors.As(err, &ve) {
		t.Fatalf("a merchant name as object id: %v", err)
	}
	if _, err := f.os.SetCategory(f.ctx, alice, org.ObjectRef{Type: org.ObjectMerchant, ID: resource.NewID()}, &cat.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("an unknown merchant: %v", err)
	}
	m, _ = ms.GetEntity(f.ctx, m.ID)
	if _, err := ms.SetEntityArchived(f.ctx, alice, m.ID, m.Version, true); err != nil {
		t.Fatal(err)
	}
	other := f.def(org.KindLabel, "Weekly", nil)
	if _, err := f.os.AddLabel(f.ctx, alice, obj, other.ID); !errors.Is(err, org.ErrObjectArchived) {
		t.Fatalf("a label on an archived merchant: %v", err)
	}
	if got, err := f.os.Get(f.ctx, obj); err != nil || len(got.Labels) != 1 {
		t.Fatalf("an archived merchant keeps its assignments: %+v %v", got, err)
	}
	if _, err := f.os.RemoveLabel(f.ctx, alice, obj, lbl.ID); err != nil {
		t.Fatalf("removing a label from an archived merchant: %v", err)
	}
	found := false
	for _, e := range f.rec.events {
		if e.Action == audit.OrganisationAssignmentChanged && e.TargetType == "merchant" {
			found = true
			if e.TargetID != m.ID {
				t.Fatalf("audit target = %q, want the merchant id", e.TargetID)
			}
		}
	}
	if !found {
		t.Fatal("no assignment event for the merchant")
	}
}

func TestDeletedCommitmentTakesItsAssignments(t *testing.T) {
	f := newFixture(t)
	c, obj := f.commitment("Gym")
	cat := f.def(org.KindCategory, "Health", nil)
	bucket := f.def(org.KindBucket, "Optional", nil)
	if _, err := f.os.SetCategory(f.ctx, alice, obj, &cat.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.os.AddToBucket(f.ctx, alice, bucket.ID, obj); err != nil {
		t.Fatal(err)
	}
	if err := f.cs.Delete(f.ctx, alice, c.ID, c.Version); err != nil {
		t.Fatalf("a commitment with assignments could not be deleted: %v", err)
	}
	if _, err := f.os.Get(f.ctx, obj); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("assignments of a deleted commitment: %v", err)
	}
	if res, err := f.os.DeleteDefinition(f.ctx, alice, org.KindCategory, cat.ID, cat.Version, ""); err != nil || res.Reassigned != 0 {
		t.Fatalf("the category was still in use: %+v %v", res, err)
	}
	if members, _ := f.os.ListMembers(f.ctx, bucket.ID, org.MemberFilter{}); len(members) != 0 {
		t.Fatalf("members of a deleted commitment: %+v", members)
	}
}

func TestPurgeBlockedByDecisionsButNotByAutomaticAssignments(t *testing.T) {
	f := newFixture(t)
	tx := f.tx("STREAMING SERVICE")
	cat := f.def(org.KindCategory, "Media", nil)
	lbl := f.def(org.KindLabel, "Auto", nil)
	if _, err := f.os.ApplyAutomatic(f.ctx, system, txRef(tx), org.Automatic{Origin: org.OriginModel, Source: "p/m", CategoryID: &cat.ID, LabelIDs: []string{lbl.ID}}); err != nil {
		t.Fatal(err)
	}
	conn, _ := f.facts.Connection(f.ctx, f.conn.ID)
	// Derived assignments go with the bank facts; a decision blocks the purge.
	other := f.tx("COFFEE KIOSK")
	if _, err := f.os.SetCategory(f.ctx, alice, txRef(other), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.facts.PurgeConnection(f.ctx, f.conn.ID, conn.Version); !errors.Is(err, banking.ErrPurgeBlocked) {
		t.Fatalf("purge with a decision: %v", err)
	}
	if _, err := f.os.ResetCategory(f.ctx, alice, txRef(other)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.facts.PurgeConnection(f.ctx, f.conn.ID, conn.Version); err != nil {
		t.Fatalf("purge with automatic assignments only: %v", err)
	}
}

func TestAuditAndAtomicity(t *testing.T) {
	f := newFixture(t)
	obj := txRef(f.tx("COFFEE KIOSK"))
	cat := f.def(org.KindCategory, "Food", nil)
	lbl := f.def(org.KindLabel, "Fun", nil)
	if _, err := f.os.SetCategory(f.ctx, alice, obj, &cat.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.os.AddLabel(f.ctx, alice, obj, lbl.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.os.RemoveLabel(f.ctx, alice, obj, lbl.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.os.UpdateDefinition(f.ctx, alice, org.KindLabel, lbl.ID, 1, org.Fields{Name: "Fun!"}); err != nil {
		t.Fatal(err)
	}
	lbl, err := f.os.GetDefinition(f.ctx, org.KindLabel, lbl.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.archive(lbl)
	want := []string{"organisation.definition_created", "organisation.definition_created", "organisation.assignment_changed", "organisation.assignment_changed",
		"organisation.assignment_changed", "organisation.definition_updated", "organisation.definition_archived"}
	if got := f.rec.actions(); !slices.Equal(got, want) {
		t.Fatalf("audit actions = %v, want %v", got, want)
	}
	for _, e := range f.rec.events {
		for k, v := range e.Details {
			if strings.Contains(v, "Fun") || strings.Contains(v, "Food") {
				t.Errorf("audit detail %s carries a name: %q", k, v)
			}
		}
	}
	// Reads and refused changes are not audited.
	n := len(f.rec.events)
	_, _ = f.os.Get(f.ctx, obj)
	_, _ = f.os.AddLabel(f.ctx, alice, obj, lbl.ID)
	if len(f.rec.events) != n {
		t.Fatalf("a read or a refused change was audited: %v", f.rec.actions()[n:])
	}
	// A failed audit write rolls the change back.
	bad := orgstore.New(f.db, failingAudit{}, func() time.Time { return t0 }, nil)
	if _, err := bad.CreateDefinition(f.ctx, alice, org.KindLabel, org.Fields{Name: "Ghost"}); err == nil {
		t.Fatal("create succeeded although the audit write failed")
	}
	if _, err := bad.SetCategory(f.ctx, alice, obj, nil); err == nil {
		t.Fatal("assignment succeeded although the audit write failed")
	}
	if got, _ := f.os.Get(f.ctx, obj); got.Category == nil || got.Category.Category == nil {
		t.Fatalf("the category changed through a failed audit: %+v", got.Category)
	}
	if got, _ := f.os.ListDefinitions(f.ctx, org.KindLabel, org.ListFilter{NamePrefix: "ghost"}); len(got) != 0 {
		t.Fatal("a definition survived a failed audit write")
	}
}

type failingAudit struct{}

func (failingAudit) WriteAudit(context.Context, *sql.Tx, audit.Event) error {
	return errors.New("audit down")
}

func TestSchemaEnforcesTheRules(t *testing.T) {
	f := newFixture(t)
	a := f.def(org.KindCategory, "A", nil)
	b := f.def(org.KindCategory, "B", &a.ID)
	exec := func(q string, args ...any) error {
		return f.db.WriteTx(f.ctx, func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, q, args...)
			return err
		})
	}
	// A third level is refused by the database as well.
	if err := exec("UPDATE categories SET parent_id = ? WHERE id = ?", b.ID, a.ID); err == nil {
		t.Error("the database accepted a cycle of two levels")
	}
	c := f.def(org.KindCategory, "C", nil)
	if err := exec("UPDATE categories SET parent_id = ? WHERE id = ?", b.ID, c.ID); err == nil {
		t.Error("the database accepted a third level")
	}
	// An automatic assignment needs a source; the person's has none; an
	// automatic one cannot record a removal; a rule cannot record "none".
	obj := txRef(f.tx("COFFEE KIOSK"))
	ins := "INSERT INTO category_assignments (object_type, object_id, transaction_id, category_id, origin, source, set_by_kind, set_by_via, set_at) VALUES ('transaction', ?, ?, ?, ?, ?, 'system', 'system', '2026-01-01T00:00:00.000000000Z')"
	for _, bad := range [][]any{{obj.ID, obj.ID, a.ID, "rule", ""}, {obj.ID, obj.ID, a.ID, "user", "x"}, {obj.ID, obj.ID, nil, "rule", "x"}, {obj.ID, obj.ID, a.ID, "robot", "x"}, {"other", obj.ID, a.ID, "rule", "x"}} {
		if err := exec(ins, bad...); err == nil {
			t.Errorf("the database accepted %v", bad)
		}
	}
	if err := exec(ins, obj.ID, obj.ID, a.ID, "rule", "x"); err != nil {
		t.Errorf("a valid automatic assignment was refused: %v", err)
	}
	// The category cannot be deleted from under an assignment.
	if err := exec("DELETE FROM categories WHERE id = ?", a.ID); err == nil {
		t.Error("the database deleted an assigned category")
	}
}
