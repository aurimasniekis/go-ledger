package merchants_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/merchants"
	org "github.com/aurimasniekis/go-ledger/internal/domain/organisation"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	merchantstore "github.com/aurimasniekis/go-ledger/internal/store/merchants"
	orgstore "github.com/aurimasniekis/go-ledger/internal/store/organisation"
)

func (f *fixture) count(q string, args ...any) int {
	f.t.Helper()
	var n int
	err := f.db.ReadTx(f.ctx, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, q, args...).Scan(&n)
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return n
}

// A correction creates the merchant record of a new name and reuses the
// record of a known one through its alias key (any case, spacing or Unicode
// composition); the resolved merchant names the record.
func TestDecisionsAttachToMerchantRecords(t *testing.T) {
	f := newFixture(t)
	a, b := f.tx("STREAMING SERVICE"), f.tx("COFFEE KIOSK")
	m := f.review(a.ID, merchants.Request{Decision: merchants.DecisionCorrected, Name: "Café  Example"})
	if m.MerchantID == "" || m.Review == nil || m.Review.MerchantID != m.MerchantID || m.Name != "Café Example" {
		t.Fatalf("first correction: %+v", m)
	}
	m2 := f.review(b.ID, merchants.Request{Decision: merchants.DecisionCorrected, Name: "CAFÉ EXAMPLE"})
	if m2.MerchantID != m.MerchantID || m2.Name != "Café Example" {
		t.Fatalf("a second spelling made another merchant: %+v", m2)
	}
	if n := f.count("SELECT count(*) FROM merchants"); n != 1 {
		t.Fatalf("%d merchants, want 1", n)
	}
	// The decision keeps the name as the person typed it.
	if m2.Review.Name != "CAFÉ EXAMPLE" {
		t.Fatalf("decision name = %q", m2.Review.Name)
	}
	// A correction by id records the merchant's display name.
	other, err := f.ms.CreateEntity(f.ctx, alice, "Other Shop", nil)
	if err != nil {
		t.Fatal(err)
	}
	m3 := f.review(b.ID, merchants.Request{Decision: merchants.DecisionCorrected, MerchantID: other.ID})
	if m3.MerchantID != other.ID || m3.Name != "Other Shop" || m3.Review.Name != "Other Shop" {
		t.Fatalf("correction by id: %+v", m3)
	}
	var ve *commitments.ValidationError
	if _, err := f.ms.Review(f.ctx, alice, b.ID, merchants.Request{Decision: merchants.DecisionCorrected, MerchantID: resource.NewID()}); !errors.As(err, &ve) {
		t.Fatalf("a correction to an unknown merchant: %v", err)
	}
	if _, err := f.ms.Review(f.ctx, alice, b.ID, merchants.Request{Decision: merchants.DecisionCorrected, MerchantID: other.ID, Name: "x"}); err == nil {
		t.Fatal("a correction with a name and an id was accepted")
	}
	other, _ = f.ms.GetEntity(f.ctx, other.ID)
	if _, err := f.ms.SetEntityArchived(f.ctx, alice, other.ID, other.Version, true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ms.Review(f.ctx, alice, a.ID, merchants.Request{Decision: merchants.DecisionCorrected, MerchantID: other.ID}); !errors.Is(err, merchants.ErrArchived) {
		t.Fatalf("a correction to an archived merchant: %v", err)
	}
	// A rejection attaches to no merchant.
	if m := f.review(a.ID, merchants.Request{Decision: merchants.DecisionRejected}); m.MerchantID != "" || m.Review.MerchantID != "" {
		t.Fatalf("rejection: %+v", m)
	}
}

// Acceptance: renaming a merchant keeps its decisions and assignments, and a
// new spelling added as an alias resolves to the same merchant, so an answer
// in that spelling shows the merchant and its assignments apply.
func TestRenameAndNewSpellingKeepAssignments(t *testing.T) {
	f := newFixture(t)
	os := orgstore.New(f.db, f.rec, func() time.Time { return f.now }, nil)
	a, b := f.tx("STREAMING SERVICE"), f.tx("COFFEE KIOSK")
	f.record(a.ID, identified("Streamly", 950))
	m := f.review(a.ID, merchants.Request{Decision: merchants.DecisionConfirmed})
	id := m.MerchantID
	cat, err := os.CreateDefinition(f.ctx, alice, org.KindCategory, org.Fields{Name: "Subscriptions"})
	if err != nil {
		t.Fatal(err)
	}
	obj := org.ObjectRef{Type: org.ObjectMerchant, ID: id}
	if _, err := os.SetCategory(f.ctx, alice, obj, &cat.ID); err != nil {
		t.Fatal(err)
	}
	ent, _ := f.ms.GetEntity(f.ctx, id)
	if ent, err = f.ms.RenameEntity(f.ctx, alice, id, ent.Version, "Streamly Premium"); err != nil {
		t.Fatal(err)
	}
	if len(ent.Aliases) != 2 || ent.DisplayName != "Streamly Premium" || ent.Version != 2 {
		t.Fatalf("renamed: %+v", ent)
	}
	got, err := f.ms.Get(f.ctx, a.ID)
	if err != nil || got.State != merchants.StateConfirmed || got.MerchantID != id || got.Name != "Streamly Premium" {
		t.Fatalf("after the rename: %+v %v", got, err)
	}
	if as, err := os.Get(f.ctx, obj); err != nil || as.Category == nil || as.Category.Category == nil || as.Category.Category.ID != cat.ID {
		t.Fatalf("assignment after the rename: %+v %v", as, err)
	}
	// A new spelling: once it is an alias, an answer in that spelling resolves
	// to the merchant (reliable answers show its display name).
	f.record(b.ID, identified("STREAMLY*NL", 900))
	if got, _ := f.ms.Get(f.ctx, b.ID); got.MerchantID != "" || got.Name != "STREAMLY*NL" {
		t.Fatalf("before the alias: %+v", got)
	}
	if _, err := f.ms.AddAlias(f.ctx, alice, id, "streamly*nl"); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.ms.Get(f.ctx, b.ID); got.MerchantID != id || got.Name != "Streamly Premium" || got.Enrichment.Source != "curated" || got.Enrichment.Confidence != 900 {
		t.Fatalf("after the alias: %+v", got)
	}
	// A confirmation stays applied when the source reports another spelling
	// of the same merchant.
	f.record(a.ID, identified("STREAMLY*NL", 900))
	if got, _ := f.ms.Get(f.ctx, a.ID); got.State != merchants.StateConfirmed || got.ConfirmationOutdated {
		t.Fatalf("confirmation with a new spelling: %+v", got)
	}
	// An alias another merchant has is refused, and so is a rename to it.
	other, err := f.ms.CreateEntity(f.ctx, alice, "Other", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ms.AddAlias(f.ctx, alice, other.ID, "Streamly"); !errors.Is(err, merchants.ErrAliasTaken) {
		t.Fatalf("taken alias: %v", err)
	}
	if _, err := f.ms.RenameEntity(f.ctx, alice, other.ID, other.Version, "STREAMLY PREMIUM"); !errors.Is(err, merchants.ErrAliasTaken) {
		t.Fatalf("rename to a taken alias: %v", err)
	}
	if _, err := f.ms.CreateEntity(f.ctx, alice, "streamly", nil); !errors.Is(err, merchants.ErrAliasTaken) {
		t.Fatalf("create with a taken alias: %v", err)
	}
	// Removing the alias leaves the answer without a merchant.
	ent, _ = f.ms.GetEntity(f.ctx, id)
	var aliasID string
	for _, al := range ent.Aliases {
		if al.Key == "streamly*nl" {
			aliasID = al.ID
		}
	}
	if _, err := f.ms.RemoveAlias(f.ctx, alice, id, aliasID); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.ms.Get(f.ctx, b.ID); got.MerchantID != "" {
		t.Fatalf("after removing the alias: %+v", got)
	}
	if _, err := f.ms.RemoveAlias(f.ctx, alice, id, aliasID); !errors.Is(err, merchants.ErrNoAlias) {
		t.Fatalf("removing twice: %v", err)
	}
	// A stale version is a conflict.
	var vc *resource.VersionConflictError
	if _, err := f.ms.RenameEntity(f.ctx, alice, id, 1, "Again"); !errors.As(err, &vc) {
		t.Fatalf("stale rename: %v", err)
	}
}

// Acceptance: a merge moves aliases, decisions, answers and assignments to the
// remaining merchant in one transaction; the merged one stays, archived,
// pointing at it. A person's assignment outranks an automatic one; the
// remaining merchant's stands otherwise.
func TestMergeMovesEverythingAtomically(t *testing.T) {
	f := newFixture(t)
	os := orgstore.New(f.db, f.rec, func() time.Time { return f.now }, nil)
	a, b := f.tx("STREAMING SERVICE"), f.tx("COFFEE KIOSK")
	ma := f.review(a.ID, merchants.Request{Decision: merchants.DecisionCorrected, Name: "Kiosk One"}).MerchantID
	mb := f.review(b.ID, merchants.Request{Decision: merchants.DecisionCorrected, Name: "Kiosk 1"}).MerchantID
	if ma == mb {
		t.Fatal("two names made one merchant")
	}
	f.record(f.tx("SALARY OCTOBER").ID, identified("Kiosk 1", 900))
	def := func(k org.Kind, name string) org.Definition {
		d, err := os.CreateDefinition(f.ctx, alice, k, org.Fields{Name: name})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	food, drinks := def(org.KindCategory, "Food"), def(org.KindCategory, "Drinks")
	l1, l2, l3 := def(org.KindLabel, "Daily"), def(org.KindLabel, "Cash"), def(org.KindLabel, "Work")
	objA, objB := org.ObjectRef{Type: org.ObjectMerchant, ID: ma}, org.ObjectRef{Type: org.ObjectMerchant, ID: mb}
	// a (remaining): a rule's category and a rule's label l1; b: the person's
	// category, the person's l1 and a rule's l2 and l3, with l3 also a rule on a.
	if _, err := os.ApplyAutomatic(f.ctx, audit.System, objA, org.Automatic{Origin: org.OriginRule, Source: "rule:x", CategoryID: &drinks.ID, LabelIDs: []string{l1.ID, l3.ID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.SetCategory(f.ctx, alice, objB, &food.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.AddLabel(f.ctx, alice, objB, l1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ApplyAutomatic(f.ctx, audit.System, objB, org.Automatic{Origin: org.OriginRule, Source: "rule:y", LabelIDs: []string{l2.ID, l3.ID}}); err != nil {
		t.Fatal(err)
	}
	// Refusals change nothing.
	ea, _ := f.ms.GetEntity(f.ctx, ma)
	if _, err := f.ms.Merge(f.ctx, alice, ma, ea.Version, ma); !errors.Is(err, merchants.ErrMergeSelf) {
		t.Fatalf("self merge: %v", err)
	}
	var vc *resource.VersionConflictError
	if _, err := f.ms.Merge(f.ctx, alice, ma, ea.Version+5, mb); !errors.As(err, &vc) {
		t.Fatalf("stale merge: %v", err)
	}
	if n := f.count("SELECT count(*) FROM merchant_aliases WHERE merchant_id = ?", mb); n != 1 {
		t.Fatalf("a refused merge moved aliases: %d", n)
	}
	res, err := f.ms.Merge(f.ctx, alice, ma, ea.Version, mb)
	if err != nil {
		t.Fatal(err)
	}
	if res.Aliases != 1 || res.Decisions != 1 || res.Enrichments != 1 || res.Categories != 1 || res.Labels != 2 || res.Kept != 1 || len(res.Into.Aliases) != 2 {
		t.Fatalf("merge result: %+v", res)
	}
	as, err := os.Get(f.ctx, objA)
	if err != nil || as.Category == nil || as.Category.Category == nil || as.Category.Category.ID != food.ID || as.Category.Origin != org.OriginUser {
		t.Fatalf("category after the merge (the person's outranks the rule's): %+v %v", as.Category, err)
	}
	labels := map[string]org.Origin{}
	for _, l := range as.Labels {
		labels[l.Label.ID] = l.Origin
	}
	if len(labels) != 3 || labels[l1.ID] != org.OriginUser || labels[l2.ID] != org.OriginRule || labels[l3.ID] != org.OriginRule {
		t.Fatalf("labels after the merge: %v", labels)
	}
	for _, q := range []string{
		"SELECT count(*) FROM merchant_aliases WHERE merchant_id = ?1", "SELECT count(*) FROM merchant_reviews WHERE merchant_id = ?1",
		"SELECT count(*) FROM merchant_enrichments WHERE merchant_id = ?1", "SELECT count(*) FROM category_assignments WHERE merchant_id = ?1 OR object_id = ?1",
		"SELECT count(*) FROM label_assignments WHERE merchant_id = ?1 OR object_id = ?1",
	} {
		if n := f.count(q, mb); n != 0 {
			t.Errorf("%s: %d rows left on the merged merchant", q, n)
		}
	}
	gone, err := f.ms.GetEntity(f.ctx, mb)
	if err != nil || gone.MergedInto != ma || !gone.Archived() || len(gone.Aliases) != 0 {
		t.Fatalf("merged merchant: %+v %v", gone, err)
	}
	if got, _ := f.ms.Get(f.ctx, b.ID); got.MerchantID != ma || got.Name != "Kiosk One" {
		t.Fatalf("b's decision after the merge: %+v", got)
	}
	// A merged merchant cannot change or be merged again, and nothing is
	// assigned to it.
	if _, err := f.ms.AddAlias(f.ctx, alice, mb, "x"); !errors.Is(err, merchants.ErrMerged) {
		t.Fatalf("alias on a merged merchant: %v", err)
	}
	c, err := f.ms.CreateEntity(f.ctx, alice, "Third", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ms.Merge(f.ctx, alice, c.ID, c.Version, mb); !errors.Is(err, merchants.ErrMerged) {
		t.Fatalf("merging a merged merchant: %v", err)
	}
	if _, err := os.SetCategory(f.ctx, alice, objB, &food.ID); !errors.Is(err, org.ErrObjectArchived) {
		t.Fatalf("assigning to a merged merchant: %v", err)
	}
	// Merging a into c: b, merged into a earlier, now points at c.
	if _, err := f.ms.Merge(f.ctx, alice, c.ID, c.Version, ma); err != nil {
		t.Fatal(err)
	}
	if gone, _ := f.ms.GetEntity(f.ctx, mb); gone.MergedInto != c.ID {
		t.Fatalf("chain after the second merge: %+v", gone)
	}
	merged := 0
	for _, e := range f.rec.events {
		if e.Action == audit.MerchantMerged {
			merged++
			if e.TargetType != merchants.EntityTargetType || e.Details["merged_from"] == "" {
				t.Fatalf("merge event: %+v", e)
			}
			if merged == 1 && (e.Details["enrichments"] != "1" || e.Details["replaced"] != "2") {
				t.Fatalf("the first merge's audit counts: %+v", e.Details)
			}
		}
	}
	if merged != 2 {
		t.Fatalf("%d merge events, want 2", merged)
	}
}

// A merge that fails part-way leaves everything where it was: one write
// transaction (here the audit writer refuses the event, after every row moved).
func TestFailedMergeRollsBack(t *testing.T) {
	f := newFixture(t)
	a, b := f.tx("STREAMING SERVICE"), f.tx("COFFEE KIOSK")
	ma := f.review(a.ID, merchants.Request{Decision: merchants.DecisionCorrected, Name: "One"}).MerchantID
	mb := f.review(b.ID, merchants.Request{Decision: merchants.DecisionCorrected, Name: "Two"}).MerchantID
	failing := merchantstore.New(f.db, failingAudit{}, func() time.Time { return f.now })
	ea, _ := f.ms.GetEntity(f.ctx, ma)
	if _, err := failing.Merge(f.ctx, alice, ma, ea.Version, mb); err == nil {
		t.Fatal("the merge succeeded with a failing audit writer")
	}
	if n := f.count("SELECT count(*) FROM merchant_reviews WHERE merchant_id = ?", mb); n != 1 {
		t.Fatalf("the failed merge moved decisions: %d left", n)
	}
	if gone, _ := f.ms.GetEntity(f.ctx, mb); gone.MergedInto != "" || gone.Archived() {
		t.Fatalf("the failed merge archived the merchant: %+v", gone)
	}
}

// Deleting a merchant is refused while a decision or an assignment refers to
// it; one nothing refers to goes with its aliases.
func TestDeleteMerchantOnlyWhenUnused(t *testing.T) {
	f := newFixture(t)
	a := f.tx("STREAMING SERVICE")
	used := f.review(a.ID, merchants.Request{Decision: merchants.DecisionCorrected, Name: "Used"}).MerchantID
	e, _ := f.ms.GetEntity(f.ctx, used)
	if err := f.ms.DeleteEntity(f.ctx, alice, used, e.Version); !errors.Is(err, merchants.ErrInUse) {
		t.Fatalf("deleting a used merchant: %v", err)
	}
	free, err := f.ms.CreateEntity(f.ctx, alice, "Free", []string{"Free Ltd"})
	if err != nil {
		t.Fatal(err)
	}
	f.record(f.tx("COFFEE KIOSK").ID, identified("Free Ltd", 900))
	if err := f.ms.DeleteEntity(f.ctx, alice, free.ID, free.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ms.GetEntity(f.ctx, free.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("deleted merchant: %v", err)
	}
	if got, _ := f.ms.Get(f.ctx, f.tx("COFFEE KIOSK").ID); got.MerchantID != "" || got.State != merchants.StateReliable {
		t.Fatalf("answer after deleting its merchant: %+v", got)
	}
	list, err := f.ms.ListEntities(f.ctx, merchants.EntityFilter{Limit: 10})
	if err != nil || len(list) != 1 || list[0].ID != used {
		t.Fatalf("list: %+v %v", list, err)
	}
}

// A merged merchant, and one others were merged into, cannot be deleted: the
// merged ids keep resolving to the merchant they point at.
func TestDeleteRefusesMergedMerchants(t *testing.T) {
	f := newFixture(t)
	a, err := f.ms.CreateEntity(f.ctx, alice, "Into", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.ms.CreateEntity(f.ctx, alice, "From", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ms.Merge(f.ctx, alice, a.ID, a.Version, b.ID); err != nil {
		t.Fatal(err)
	}
	a, _ = f.ms.GetEntity(f.ctx, a.ID)
	b, _ = f.ms.GetEntity(f.ctx, b.ID)
	if err := f.ms.DeleteEntity(f.ctx, alice, a.ID, a.Version); !errors.Is(err, merchants.ErrInUse) {
		t.Fatalf("deleting the merchant others were merged into: %v", err)
	}
	if err := f.ms.DeleteEntity(f.ctx, alice, b.ID, b.Version); !errors.Is(err, merchants.ErrInUse) {
		t.Fatalf("deleting a merged merchant: %v", err)
	}
	if got, err := f.ms.GetEntity(f.ctx, b.ID); err != nil || got.MergedInto != a.ID {
		t.Fatalf("merged id after the refusals: %+v %v", got, err)
	}
}

// From the PR #124 review: a person's decision about one transaction flags the
// other transactions whose answers identified the same merchant, so the next
// pass re-resolves them (here the correction makes the reused mapping the
// person's name for the same key).
func TestDecisionFlagsSameMerchantAnswersForRecheck(t *testing.T) {
	f := newFixture(t)
	a, b, c := f.tx("STREAMING SERVICE"), f.tx("COFFEE KIOSK"), f.tx("SALARY OCTOBER")
	for _, id := range []string{string(a.ID), string(b.ID)} {
		f.record(banking.TransactionID(id), identified("Lidl", 950))
	}
	f.record(c.ID, identified("Other", 950))
	f.review(a.ID, merchants.Request{Decision: merchants.DecisionCorrected, Name: "Rimi"})
	if n := f.count("SELECT count(*) FROM merchant_enrichments WHERE recheck = 1"); n != 1 {
		t.Fatalf("%d answers flagged, want b's", n)
	}
	if n := f.count("SELECT recheck FROM merchant_enrichments WHERE transaction_id = ?", string(b.ID)); n != 1 {
		t.Fatal("b's answer is not flagged")
	}
	pending, err := f.ms.Pending(f.ctx, 100, f.now.Add(-24*time.Hour), false, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range pending {
		found = found || p.ID == b.ID
	}
	if !found {
		t.Fatalf("flagged answer not pending: %+v", pending)
	}
	f.record(b.ID, identified("Lidl", 950))
	if n := f.count("SELECT count(*) FROM merchant_enrichments WHERE recheck = 1"); n != 0 {
		t.Fatalf("%d answers still flagged after the recheck", n)
	}
}
