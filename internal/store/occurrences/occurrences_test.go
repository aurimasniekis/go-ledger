package occurrences_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/store"
	commitmentstore "github.com/aurimasniekis/go-ledger/internal/store/commitments"
	occstore "github.com/aurimasniekis/go-ledger/internal/store/occurrences"
)

type recorder struct{ events []audit.Event }

func (r *recorder) WriteAudit(_ context.Context, tx *sql.Tx, e audit.Event) error {
	if tx == nil {
		return errors.New("no transaction")
	}
	if err := audit.Validate(e); err != nil {
		return err
	}
	r.events = append(r.events, e)
	return nil
}

var alice = audit.Actor{Kind: audit.ActorUser, UserID: "01JD3Y4C8V9M2Q7W5R6T0XZBNA"}

// testPayments stands in for task C3's payment evidence: a row here makes
// the occurrence it names paid. Like C3's tables it refers to occurrences with
// ON DELETE RESTRICT.
const testPayments = "CREATE TABLE test_payments (occurrence_id TEXT NOT NULL REFERENCES commitment_occurrences (id) ON DELETE RESTRICT) STRICT"

type fixture struct {
	db  *store.DB
	cs  *commitmentstore.Store
	occ *occstore.Store
	rec *recorder
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "ledger.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, testPayments)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC) }
	occ := occstore.New(db, occstore.Options{Now: now,
		Evidence: []string{"EXISTS (SELECT 1 FROM test_payments p WHERE p.occurrence_id = o.id)"}})
	rec := &recorder{}
	return &fixture{db: db, cs: commitmentstore.NewWithOccurrences(db, rec, now, nil, occ), occ: occ, rec: rec}
}

func d(s string) calendar.Date {
	v, err := calendar.ParseDate(s)
	if err != nil {
		panic(err)
	}
	return v
}

func ym(s string) calendar.YearMonth {
	v, err := calendar.ParseYearMonth(s)
	if err != nil {
		panic(err)
	}
	return v
}

func (f *fixture) create(t *testing.T, s commitments.Schedule, start, amount string) commitments.Commitment {
	t.Helper()
	c, err := f.cs.Create(context.Background(), alice, commitments.Fields{Name: "Hosting", Kind: commitments.KindServer,
		Amount: money.MustParse(amount, "EUR"), Schedule: s, StartDate: d(start)})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (f *fixture) month(t *testing.T, m string) []occurrences.Occurrence {
	t.Helper()
	out, err := f.occ.List(context.Background(), occurrences.ListFilter{Month: ym(m), Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// all returns every stored occurrence as "due@amount#version", by due date.
func (f *fixture) all(t *testing.T) string {
	t.Helper()
	var out []string
	err := f.db.ReadTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT due_date, amount, version FROM commitment_occurrences ORDER BY due_date")
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var due, amount, version string
			if err := rows.Scan(&due, &amount, &version); err != nil {
				return err
			}
			out = append(out, due+"@"+amount+"#"+version)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(out, " ")
}

func (f *fixture) pay(t *testing.T, id string) {
	t.Helper()
	if err := f.db.WriteTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO test_payments (occurrence_id) VALUES (?)", id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMaterialisationIsIdempotent(t *testing.T) {
	f := newFixture(t)
	f.create(t, commitments.Monthly(1, 31), "2024-01-31", "12.50")
	apr := f.month(t, "2024-04")
	if len(apr) != 1 || apr[0].DueDate != d("2024-04-30") || apr[0].Month != ym("2024-04") || apr[0].Amount.String() != "12.50 EUR" {
		t.Fatalf("April: %+v", apr)
	}
	want := "2024-01-31@12.50#1 2024-02-29@12.50#1 2024-03-31@12.50#1 2024-04-30@12.50#1"
	if got := f.all(t); got != want {
		t.Fatalf("materialised\n got %s\nwant %s", got, want)
	}
	// Reading again, or an earlier month, regenerates nothing.
	again := f.month(t, "2024-04")
	if len(again) != 1 || again[0].ID != apr[0].ID || again[0].Version != 1 {
		t.Fatalf("second read: %+v", again)
	}
	if feb := f.month(t, "2024-02"); len(feb) != 1 || feb[0].DueDate != d("2024-02-29") {
		t.Fatalf("February: %+v", feb)
	}
	if got := f.all(t); got != want {
		t.Fatalf("after re-reads\n got %s\nwant %s", got, want)
	}
	if len(f.rec.events) != 1 { // only commitment.created: materialisation is not an audited change
		t.Fatalf("audit %+v", f.rec.events)
	}
	if got, err := f.occ.Get(context.Background(), apr[0].ID); err != nil || got.DueDate != apr[0].DueDate {
		t.Fatalf("get: %+v %v", got, err)
	}
	if _, err := f.occ.Get(context.Background(), "01JD3Y4C8V9M2Q7W5R6T0XZBNZ"); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("get missing: %v", err)
	}
}

func TestAnnualFebruary29Stored(t *testing.T) {
	f := newFixture(t)
	f.create(t, commitments.Annual(time.February, 29), "2024-02-29", "15.000")
	f.month(t, "2028-02")
	if got, want := f.all(t), "2024-02-29@15.000#1 2025-02-28@15.000#1 2026-02-28@15.000#1 2027-02-28@15.000#1 2028-02-29@15.000#1"; got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
}

func TestEditKeepsPaidHistory(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	c := f.create(t, commitments.Monthly(1, 10), "2026-01-01", "10.00")
	f.month(t, "2026-06")
	byMonth := map[string]occurrences.Occurrence{}
	for _, m := range []string{"2026-01", "2026-02", "2026-03", "2026-04", "2026-05", "2026-06"} {
		byMonth[m] = f.month(t, m)[0]
	}
	f.pay(t, byMonth["2026-02"].ID)
	f.pay(t, byMonth["2026-05"].ID)

	next := c.Fields
	next.Amount = money.MustParse("12.00", "EUR")
	next.Schedule = commitments.Monthly(1, 20)
	if _, err := f.cs.Update(ctx, alice, c.ID, 1, next, d("2026-03-15"), d("2026-03-15")); err != nil {
		t.Fatal(err)
	}
	// Jan and Mar are before the effective date, Feb and May are paid: all
	// unchanged. Apr and Jun move to the 20th at the new amount.
	want := "2026-01-10@10.00#1 2026-02-10@10.00#1 2026-03-10@10.00#1 2026-04-20@12.00#2 2026-05-10@10.00#1 2026-06-20@12.00#2"
	if got := f.all(t); got != want {
		t.Fatalf("after edit\n got %s\nwant %s", got, want)
	}
	if apr := f.month(t, "2026-04"); apr[0].ID != byMonth["2026-04"].ID {
		t.Error("an unpaid occurrence is rewritten in place, keeping its id")
	}
	// Months not yet materialised follow the current terms.
	if jul := f.month(t, "2026-07"); len(jul) != 1 || jul[0].DueDate != d("2026-07-20") || jul[0].Amount.String() != "12.00 EUR" {
		t.Fatalf("July: %+v", jul)
	}
	last := f.rec.events[len(f.rec.events)-1]
	if last.Action != audit.OccurrencesRegenerated || last.TargetID != c.ID || last.Details["effective_from"] != "2026-03-15" ||
		last.Details["updated"] != "2" || last.Details["kept_paid"] != "1" || last.Details["created"] != "0" || last.Details["removed"] != "0" {
		t.Fatalf("regeneration audit %+v", last)
	}

	// Ending the commitment early removes unpaid future occurrences only.
	ended := next
	ended.EndDate = new(calendar.Date)
	*ended.EndDate = d("2026-04-30")
	if _, err := f.cs.Update(ctx, alice, c.ID, 2, ended, d("2026-04-01"), d("2026-03-15")); err != nil {
		t.Fatal(err)
	}
	want = "2026-01-10@10.00#1 2026-02-10@10.00#1 2026-03-10@10.00#1 2026-04-20@12.00#2 2026-05-10@10.00#1"
	if got := f.all(t); got != want {
		t.Fatalf("after end date\n got %s\nwant %s", got, want)
	}

	// Payment history blocks deleting the commitment; nothing is lost.
	if err := f.cs.Delete(ctx, alice, c.ID, 3); !errors.Is(err, commitments.ErrInUse) {
		t.Fatalf("delete with paid occurrences: %v", err)
	}
	if got := f.all(t); got != want {
		t.Fatalf("a refused delete changed occurrences: %s", got)
	}
}

func TestEditBeforeAnythingIsMaterialised(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	c := f.create(t, commitments.Monthly(1, 5), "2026-01-01", "10.00")
	next := c.Fields
	next.Amount = money.MustParse("11.00", "EUR")
	if _, err := f.cs.Update(ctx, alice, c.ID, 1, next, d("2026-06-01"), d("2026-03-15")); err != nil {
		t.Fatal(err)
	}
	// The months before the effective date keep the terms in force then.
	if mar := f.month(t, "2026-03"); mar[0].Amount.String() != "10.00 EUR" {
		t.Fatalf("March: %+v", mar)
	}
	if jun := f.month(t, "2026-06"); jun[0].Amount.String() != "11.00 EUR" {
		t.Fatalf("June: %+v", jun)
	}
	// Changing only the name does not touch occurrences and is not a regeneration.
	renamed := next
	renamed.Name = "Renamed"
	n := len(f.rec.events)
	if _, err := f.cs.Update(ctx, alice, c.ID, 2, renamed, d("2026-01-01"), d("2026-03-15")); err != nil {
		t.Fatal(err)
	}
	if len(f.rec.events) != n+1 || f.rec.events[n].Action != audit.CommitmentUpdated {
		t.Fatalf("rename audit %+v", f.rec.events[n:])
	}
}

func TestDeleteRemovesUnpaidOccurrences(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	c := f.create(t, commitments.Monthly(1, 5), "2026-01-01", "10.00")
	f.month(t, "2026-03")
	if err := f.cs.Delete(ctx, alice, c.ID, 1); err != nil {
		t.Fatal(err)
	}
	if got := f.all(t); got != "" {
		t.Fatalf("left behind: %s", got)
	}
}

func TestListOrderFilterAndCursor(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	a := f.create(t, commitments.Monthly(1, 20), "2026-01-01", "1.00")
	b := f.create(t, commitments.Monthly(1, 5), "2026-01-01", "2.00")
	c := f.create(t, commitments.Monthly(1, 12), "2026-01-01", "3.00")
	page, err := f.occ.List(ctx, occurrences.ListFilter{Month: ym("2026-02"), Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 3 || page[0].CommitmentID != b.ID || page[1].CommitmentID != c.ID || page[2].CommitmentID != a.ID {
		t.Fatalf("order by due date: %+v", page)
	}
	rest, err := f.occ.List(ctx, occurrences.ListFilter{Month: ym("2026-02"), Limit: 2, After: page[1].ID})
	if err != nil || len(rest) != 1 || rest[0].CommitmentID != a.ID {
		t.Fatalf("after cursor: %+v %v", rest, err)
	}
	only, err := f.occ.List(ctx, occurrences.ListFilter{Month: ym("2026-02"), Limit: 10, CommitmentIDs: []string{c.ID}})
	if err != nil || len(only) != 1 || only[0].CommitmentID != c.ID {
		t.Fatalf("commitment filter: %+v %v", only, err)
	}
	if _, err := f.occ.List(ctx, occurrences.ListFilter{Month: ym("2026-02"), Limit: 2, After: "01JD3Y4C8V9M2Q7W5R6T0XZBNZ"}); !errors.Is(err, occurrences.ErrCursorGone) {
		t.Fatalf("unknown cursor: %v", err)
	}
}

// TestFutureDatedTermsAreNotOverwritten is the regression for a future-dated
// terms change silently overwritten by a later edit that takes effect earlier.
// Today is 2026-03-15; the definition is monthly on day 10 at 10.00.
func TestFutureDatedTermsAreNotOverwritten(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	today := d("2026-03-15")
	c := f.create(t, commitments.Monthly(1, 10), "2026-01-01", "10.00")
	f.month(t, "2026-04")

	priced := c.Fields
	priced.Amount = money.MustParse("12.00", "EUR")
	if _, err := f.cs.Update(ctx, alice, c.ID, 1, priced, d("2026-09-01"), today); err != nil {
		t.Fatal(err)
	}
	f.month(t, "2026-09")
	before := f.all(t)
	if !strings.Contains(before, "2026-08-10@10.00#1 2026-09-10@12.00") {
		t.Fatalf("future price change: %s", before)
	}

	// A day edit effective today, or a deliberate reach-back, would rewrite
	// April to August with the September amount: both are refused.
	moved := priced
	moved.Schedule = commitments.Monthly(1, 11)
	for _, eff := range []string{"2026-03-15", "2026-01-01", "2026-08-31"} {
		var pe *occurrences.PendingTermsError
		_, err := f.cs.Update(ctx, alice, c.ID, 2, moved, d(eff), today)
		if !errors.As(err, &pe) || pe.Pending != d("2026-09-01") || pe.Effective != d(eff) {
			t.Fatalf("effective %s: %v", eff, err)
		}
	}
	if got := f.all(t); got != before {
		t.Fatalf("a refused edit changed occurrences\n got %s\nwant %s", got, before)
	}
	if got, _ := f.cs.Get(ctx, c.ID); got.Version != 2 || got.Schedule != priced.Schedule {
		t.Fatalf("a refused edit changed the definition: %+v", got)
	}

	// An edit effective on or after the pending date supersedes it.
	if _, err := f.cs.Update(ctx, alice, c.ID, 2, moved, d("2026-09-01"), today); err != nil {
		t.Fatal(err)
	}
	if got := f.all(t); !strings.Contains(got, "2026-08-10@10.00#1 2026-09-11@12.00#3") {
		t.Fatalf("superseding edit: %s", got)
	}
	// ...and the new pending date is the one that guards now.
	later := moved
	later.Amount = money.MustParse("13.00", "EUR")
	if _, err := f.cs.Update(ctx, alice, c.ID, 3, later, d("2026-12-01"), today); err != nil {
		t.Fatal(err)
	}
	if _, err := f.cs.Update(ctx, alice, c.ID, 4, later, d("2026-09-01"), today); err != nil {
		t.Fatalf("no change is not an edit: %v", err)
	}
	earlier := later
	earlier.Schedule = commitments.Monthly(1, 12)
	var pe *occurrences.PendingTermsError
	if _, err := f.cs.Update(ctx, alice, c.ID, 4, earlier, d("2026-09-01"), today); !errors.As(err, &pe) || pe.Pending != d("2026-12-01") {
		t.Fatalf("second pending date: %v", err)
	}
	// Once the pending date has passed, nothing is pending.
	if _, err := f.cs.Update(ctx, alice, c.ID, 4, earlier, d("2026-09-01"), d("2026-12-02")); err != nil {
		t.Fatalf("after the pending date: %v", err)
	}
}

func TestTermsEditEffectiveTodayLeavesNothingPending(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	today := d("2026-03-15")
	c := f.create(t, commitments.Monthly(1, 10), "2026-01-01", "10.00")
	a := c.Fields
	a.Amount = money.MustParse("11.00", "EUR")
	if _, err := f.cs.Update(ctx, alice, c.ID, 1, a, d("2026-06-01"), today); err != nil {
		t.Fatal(err)
	}
	b := a
	b.Amount = money.MustParse("12.00", "EUR")
	if _, err := f.cs.Update(ctx, alice, c.ID, 2, b, d("2026-06-01"), today); err != nil { // equal date supersedes
		t.Fatal(err)
	}
	var marker sql.NullString
	err := f.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT pending_effective_from FROM commitment_occurrence_horizons WHERE commitment_id = ?", c.ID).Scan(&marker)
	})
	if err != nil || marker.String != "2026-06-01" {
		t.Fatalf("marker %v %v", marker, err)
	}
}

func TestRegeneratedAndDeletedAuditCounts(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	c := f.create(t, commitments.Monthly(1, 10), "2026-01-01", "10.00")
	f.month(t, "2026-05")
	next := c.Fields
	next.Amount = money.MustParse("12.00", "EUR")
	if _, err := f.cs.Update(ctx, alice, c.ID, 1, next, d("2026-03-15"), d("2026-03-15")); err != nil {
		t.Fatal(err)
	}
	last := f.rec.events[len(f.rec.events)-1]
	// January to March 10 are unpaid history before the effective date.
	if last.Action != audit.OccurrencesRegenerated || last.Details["kept_history"] != "1" {
		t.Fatalf("regeneration audit %+v", last)
	}
	if err := f.cs.Delete(ctx, alice, c.ID, 2); err != nil {
		t.Fatal(err)
	}
	del := f.rec.events[len(f.rec.events)-1]
	if del.Action != audit.CommitmentDeleted || del.Details["unpaid_occurrences_removed"] != "5" {
		t.Fatalf("delete audit %+v", del)
	}
}

// occurrenceReferenceProblems lists foreign keys to commitment_occurrences(id)
// that are neither ON DELETE CASCADE nor ON DELETE RESTRICT with the table
// named in an evidence expression (F6: otherwise an edit could rewrite a paid
// occurrence in place).
func occurrenceReferenceProblems(t *testing.T, db *store.DB, evidence []string) []string {
	t.Helper()
	var problems []string
	err := db.ReadTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT m.name, f.\"from\", f.on_delete FROM sqlite_schema m, pragma_foreign_key_list(m.name) f"+
			" WHERE m.type = 'table' AND f.\"table\" = 'commitment_occurrences'")
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var table, col, onDelete string
			if err := rows.Scan(&table, &col, &onDelete); err != nil {
				return err
			}
			covered := false
			for _, e := range evidence {
				covered = covered || strings.Contains(e, table)
			}
			switch {
			case onDelete == "CASCADE":
			case onDelete == "RESTRICT" && covered:
			default:
				problems = append(problems, table+"."+col+" ON DELETE "+onDelete)
			}
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return problems
}

func TestOccurrenceReferencesAreEvidenceOrCascade(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t) // has test_payments (RESTRICT)
	evidence := []string{"EXISTS (SELECT 1 FROM test_payments p WHERE p.occurrence_id = o.id)"}
	if p := occurrenceReferenceProblems(t, f.db, evidence); len(p) != 0 {
		t.Fatalf("test fixture: %v", p)
	}
	// The real schema: every reference in use must be covered by DefaultEvidence.
	real, err := store.Open(ctx, filepath.Join(t.TempDir(), "real.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = real.Close() })
	if p := occurrenceReferenceProblems(t, real, occstore.DefaultEvidence); len(p) != 0 {
		t.Fatalf("add the evidence expression to DefaultEvidence (or use ON DELETE CASCADE): %v", p)
	}
	// The check itself: an uncovered RESTRICT reference is reported.
	if p := occurrenceReferenceProblems(t, f.db, nil); len(p) != 1 || !strings.HasPrefix(p[0], "test_payments.occurrence_id") {
		t.Fatalf("uncovered reference not reported: %v", p)
	}
}
