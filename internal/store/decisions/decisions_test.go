package decisions_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	"github.com/aurimasniekis/go-ledger/internal/domain/decisions"
	"github.com/aurimasniekis/go-ledger/internal/domain/decisions/decisionstest"
	"github.com/aurimasniekis/go-ledger/internal/store"
	"github.com/aurimasniekis/go-ledger/internal/store/bankfacts"
	decstore "github.com/aurimasniekis/go-ledger/internal/store/decisions"
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

var (
	alice = audit.Actor{Kind: audit.ActorUser, UserID: "01JD3Y4C8V9M2Q7W5R6T0XZBNA"}
	t0    = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
)

type fixture struct {
	t     *testing.T
	ctx   context.Context
	db    *store.DB
	ds    *decstore.Store
	rec   *recorder
	facts *bankfacts.Store
	tx    banking.StoredTransaction
}

func newFixture(t *testing.T, withBank bool) *fixture {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "ledger.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f := &fixture{t: t, ctx: ctx, db: db, rec: &recorder{}, facts: bankfacts.New(db, nil)}
	clock := func() time.Time { return t0 }
	f.ds = decstore.New(db, f.rec, clock, nil)
	if withBank {
		f.importBank(clock)
	}
	return f
}

func (f *fixture) importBank(clock func() time.Time) {
	t, ctx := f.t, f.ctx
	p := bankingtest.New(bankingtest.Options{Mode: banking.ModeApp, Now: clock, PageSize: 3}, bankingtest.Standard())
	engine := bankimport.New(f.facts, bankimport.Options{Now: clock, Sleep: func(context.Context, time.Duration) error { return nil },
		Jitter: func(d time.Duration) time.Duration { return d }})
	inst := bankingtest.Standard().Institutions[0]
	if _, err := p.StartConsent(ctx, banking.ConsentRequest{Institution: inst, RedirectURL: "https://ledger.example/cb", State: "s", PSUType: banking.PSUPersonal}); err != nil {
		t.Fatal(err)
	}
	conn, err := f.facts.CreateConnection(ctx, banking.NewConnection{Capabilities: p.Capabilities(), Institution: inst, ConsentState: "s"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := p.CompleteConsent(ctx, banking.ConsentCompletion{State: "s", Code: bankingtest.AuthorizationCode("s")})
	if err != nil {
		t.Fatal(err)
	}
	if conn, err = f.facts.ActivateConnection(ctx, conn.ID, sess, t0); err != nil {
		t.Fatal(err)
	}
	if res, err := engine.Refresh(ctx, conn.ID, p); err != nil || res.Run.Outcome != banking.RefreshSucceeded {
		t.Fatalf("refresh: %v %+v", err, res.Run)
	}
	accs, err := f.facts.Accounts(ctx, conn.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range accs {
		txs, err := f.facts.Transactions(ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, tx := range txs {
			if tx.Counterparty != "" && tx.Description != "" {
				f.tx = tx
				return
			}
		}
	}
	t.Fatal("the fake bank has no transaction with a counterparty")
}

// dump returns every row of every table but the ones named, as text.
func (f *fixture) dump(skip ...string) map[string]string {
	f.t.Helper()
	out := map[string]string{}
	err := f.db.ReadTx(f.ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT name FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
		if err != nil {
			return err
		}
		var tables []string
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				return err
			}
			if !slices.Contains(skip, n) {
				tables = append(tables, n)
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, table := range tables {
			r, err := tx.QueryContext(ctx, "SELECT * FROM "+table+" ORDER BY 1")
			if err != nil {
				return err
			}
			cols, _ := r.Columns()
			var b strings.Builder
			for r.Next() {
				vals := make([]any, len(cols))
				ptrs := make([]any, len(cols))
				for i := range vals {
					ptrs[i] = &vals[i]
				}
				if err := r.Scan(ptrs...); err != nil {
					return err
				}
				fmt.Fprintln(&b, vals...)
			}
			if err := r.Close(); err != nil {
				return err
			}
			out[table] = b.String()
		}
		return nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *fixture) input() decisions.Input {
	tx := f.tx
	return decisions.Input{
		Object: decisions.ObjectRef{Type: "transaction", ID: string(tx.ID)},
		Values: map[decisions.Field]string{
			decisions.FieldDescription:      tx.Description,
			decisions.FieldAmount:           tx.Amount.Amount().String(),
			decisions.FieldCurrency:         tx.Amount.Currency().String(),
			decisions.FieldBookingDate:      tx.Date().String(),
			decisions.FieldCounterpartyName: tx.Counterparty,
			decisions.FieldIBAN:             "LT601010012345678901",
		},
		Candidates: []decisions.Candidate{{ID: "01JD3Y4C8V9M2Q7W5R6T0XZBN1", Name: "Groceries"}, {ID: "01JD3Y4C8V9M2Q7W5R6T0XZBN2", Name: "Rent"}},
	}
}

func (f *fixture) service(p decisions.Provider, task decisions.TaskName, tc decisions.TaskConfig) *decisions.Service {
	f.t.Helper()
	s, err := decisions.New(decisions.Config{Tasks: map[decisions.TaskName]decisions.TaskConfig{task: tc}}, []decisions.Provider{p},
		decisions.Deps{Consents: f.ds, Usage: f.ds, Suggestions: f.ds, Now: func() time.Time { return t0 },
			Sleep: func(context.Context, time.Duration) error { return nil }})
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

func cfg(provider string) decisions.TaskConfig {
	return decisions.TaskConfig{Enabled: true, Route: decisions.Route{Provider: provider, Model: "m"}, Timeout: time.Second, MonthlyRequests: 10, Retries: 2}
}

// TestOutageLeavesTheDatabaseUnchanged: against a database with imported
// bank facts, a provider outage (and every other failure) changes no row of
// any table apart from the request count; nothing resembling a payment, a
// match or an assignment is written.
func TestOutageLeavesTheDatabaseUnchanged(t *testing.T) {
	f := newFixture(t, true)
	if _, err := f.ds.Grant(f.ctx, alice, "openai", decisions.TaskCategorisation, "v1", decisions.Permitted()); err != nil {
		t.Fatal(err)
	}
	before := f.dump("decision_usage")
	for name, reply := range map[string]decisionstest.Reply{
		"unavailable":    {Err: &decisions.ProviderError{Kind: decisions.KindUnavailable}},
		"rate limited":   {Err: &decisions.ProviderError{Kind: decisions.KindRetryable, RetryAfter: time.Millisecond}},
		"rejected":       {Err: &decisions.ProviderError{Kind: decisions.KindRejected}},
		"invalid output": {Output: `{"choice":"c1","confidence":0.9,"payment":"made"}`},
		"hung":           {Block: true},
	} {
		t.Run(name, func(t *testing.T) {
			p := decisionstest.NewProvider("openai", reply, reply, reply)
			tc := cfg("openai")
			tc.Timeout = 50 * time.Millisecond
			res, err := f.service(p, decisions.TaskCategorisation, tc).Run(f.ctx, decisions.TaskCategorisation, f.input())
			if err != nil || res.Suggestion != nil || res.Reason == "" {
				t.Fatalf("%+v %v", res, err)
			}
			after := f.dump("decision_usage")
			for table, rows := range before {
				if after[table] != rows {
					t.Errorf("table %s changed on %s", table, name)
				}
			}
		})
	}
	if n, _ := f.ds.Attempts(f.ctx, decisions.TaskCategorisation, "2026-11"); n != 7 {
		t.Errorf("attempts counted %d, want 7 (3 for the retried one)", n)
	}
}

// TestSuggestionIsStoredApartFromFacts: a valid answer writes only a
// suggestion row; bank facts, matches, payments and assignments are untouched,
// and the excluded fields of the stored transaction never left.
func TestSuggestionIsStoredApartFromFacts(t *testing.T) {
	f := newFixture(t, true)
	if _, err := f.ds.Grant(f.ctx, alice, "openai", decisions.TaskCategorisation, "v1", decisions.Permitted()); err != nil {
		t.Fatal(err)
	}
	before := f.dump("decision_usage", "decision_suggestions")
	tc := cfg("openai")
	tc.AutoApply = true
	p := decisionstest.NewProvider("openai", decisionstest.Reply{Output: `{"choice":"c2","confidence":0.8}`, Model: "m-2026", ResponseID: "r1"},
		decisionstest.Reply{Output: `{"choice":"c1","confidence":0.6}`})
	s := f.service(p, decisions.TaskCategorisation, tc)
	res, err := s.Run(f.ctx, decisions.TaskCategorisation, f.input())
	if err != nil || res.Suggestion == nil {
		t.Fatalf("%+v %v", res, err)
	}
	for _, b := range p.Sent() {
		if strings.Contains(string(b), f.tx.Counterparty) || strings.Contains(string(b), "LT601010012345678901") || strings.Contains(string(b), string(f.tx.ID)) {
			t.Fatalf("an excluded value left: %s", b)
		}
	}
	after := f.dump("decision_usage", "decision_suggestions")
	for table, rows := range before {
		if after[table] != rows {
			t.Errorf("table %s changed", table)
		}
	}
	if _, err := s.Run(f.ctx, decisions.TaskCategorisation, f.input()); err != nil {
		t.Fatal(err)
	}
	obj := decisions.ObjectRef{Type: "transaction", ID: string(f.tx.ID)}
	all, err := f.ds.Suggestions(f.ctx, decstore.SuggestionFilter{Object: &obj})
	if err != nil || len(all) != 2 {
		t.Fatalf("%v %v", all, err)
	}
	newer, older := all[0], all[1]
	if newer.ID == older.ID || newer.State != decisions.StatePending || older.State != decisions.StateSuperseded || older.DecidedAt == nil {
		t.Fatalf("states %+v / %+v", newer, older)
	}
	if older.Model != "m-2026" || older.ResponseID != "r1" || !older.AutoApply || older.Decision.Choice == nil ||
		*older.Decision.Choice != "01JD3Y4C8V9M2Q7W5R6T0XZBN2" || older.Decision.Confidence != "0.8" {
		t.Fatalf("stored %+v", older)
	}
	if pending, _ := f.ds.Suggestions(f.ctx, decstore.SuggestionFilter{Task: decisions.TaskCategorisation, State: decisions.StatePending}); len(pending) != 1 {
		t.Fatalf("pending %v", pending)
	}
}

func TestConsentIsPerProviderAndPurpose(t *testing.T) {
	f := newFixture(t, false)
	c, err := f.ds.Grant(f.ctx, alice, "typesafe", decisions.TaskPaymentMatching, "typesafe-matching-v1", decisions.Permitted())
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []struct {
		provider string
		purpose  decisions.TaskName
		want     bool
	}{
		{"typesafe", decisions.TaskPaymentMatching, true},
		{"typesafe", decisions.TaskCategorisation, false},
		{"openrouter", decisions.TaskPaymentMatching, false},
		{"openai", decisions.TaskPaymentMatching, false},
		{"anthropic", decisions.TaskPaymentMatching, false},
	} {
		if _, got, err := f.ds.Granted(f.ctx, k.provider, k.purpose); err != nil || got != k.want {
			t.Errorf("%s/%s: %v %v", k.provider, k.purpose, got, err)
		}
	}
	if _, err := f.ds.Grant(f.ctx, alice, "typesafe", decisions.TaskPaymentMatching, "again", decisions.Permitted()); !errors.Is(err, decisions.ErrConsentExists) {
		t.Fatalf("second grant: %v", err)
	}
	for _, bad := range [][3]string{{"mystery", "alerts", "v"}, {"openai", "budgeting", "v"}, {"openai", "alerts", " "}} {
		if _, err := f.ds.Grant(f.ctx, alice, bad[0], decisions.TaskName(bad[1]), bad[2], decisions.Permitted()); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	r, err := f.ds.Revoke(f.ctx, alice, "typesafe", decisions.TaskPaymentMatching)
	if err != nil || r.ID != c.ID || r.Active() || r.Version != 2 {
		t.Fatalf("revoke %+v %v", r, err)
	}
	if _, got, _ := f.ds.Granted(f.ctx, "typesafe", decisions.TaskPaymentMatching); got {
		t.Fatal("a revoked consent still counts")
	}
	if _, err := f.ds.Revoke(f.ctx, alice, "typesafe", decisions.TaskPaymentMatching); !errors.Is(err, decisions.ErrNoConsent) {
		t.Fatalf("second revoke: %v", err)
	}
	if _, err := f.ds.Grant(f.ctx, alice, "typesafe", decisions.TaskPaymentMatching, "typesafe-matching-v2", decisions.Permitted()); err != nil {
		t.Fatalf("grant after revoke: %v", err)
	}
	all, err := f.ds.Consents(f.ctx)
	if err != nil || len(all) != 2 || all[0].Active() || !all[1].Active() || all[0].RevokedBy == nil || all[0].GrantedBy.UserID != alice.UserID {
		t.Fatalf("history %+v %v", all, err)
	}
	var actions []string
	for _, e := range f.rec.events {
		actions = append(actions, string(e.Action)+":"+e.Details["provider"]+"/"+e.Details["purpose"])
	}
	want := []string{"decision_consent.granted:typesafe/payment_matching", "decision_consent.revoked:typesafe/payment_matching", "decision_consent.granted:typesafe/payment_matching"}
	if !slices.Equal(actions, want) {
		t.Fatalf("audit %v", actions)
	}
}

func TestDatabaseGuardsConsentsAndSuggestions(t *testing.T) {
	f := newFixture(t, false)
	if _, err := f.ds.Grant(f.ctx, alice, "openai", decisions.TaskAlerts, "v1", decisions.Permitted()); err != nil {
		t.Fatal(err)
	}
	sg, err := f.ds.Record(f.ctx, decisions.NewSuggestion{Task: decisions.TaskAlerts, Provider: "openai", Model: "m",
		Object: decisions.ObjectRef{Type: "occurrence", ID: "o1"}, Decision: decisions.Decision{Text: "x"}, CreatedAt: t0})
	if err != nil {
		t.Fatal(err)
	}
	for name, q := range map[string]string{
		"consent delete":         "DELETE FROM decision_consents",
		"consent edit":           "UPDATE decision_consents SET terms = 'other'",
		"consent fields edit":    "UPDATE decision_consents SET fields = '[\"amount\"]'",
		"second active consent":  "INSERT INTO decision_consents (id, provider, purpose, terms, fields, granted_by_kind, granted_at) SELECT '01JD3Y4C8V9M2Q7W5R6T0XZBN9', provider, purpose, 'x', fields, 'system', granted_at FROM decision_consents",
		"suggestion edit":        "UPDATE decision_suggestions SET decision = '{}'",
		"auto-apply on alerts":   "INSERT INTO decision_suggestions (id, task, provider, model, object_type, object_id, decision, auto_apply, created_at) SELECT '01JD3Y4C8V9M2Q7W5R6T0XZBN8', 'alerts', 'openai', 'm', 'occurrence', 'o2', '{}', 1, created_at FROM decision_suggestions",
		"two pending":            "INSERT INTO decision_suggestions (id, task, provider, model, object_type, object_id, decision, auto_apply, created_at) SELECT '01JD3Y4C8V9M2Q7W5R6T0XZBN7', task, provider, model, object_type, object_id, '{}', 0, created_at FROM decision_suggestions",
		"unknown provider":       "INSERT INTO decision_suggestions (id, task, provider, model, object_type, object_id, decision, auto_apply, created_at) SELECT '01JD3Y4C8V9M2Q7W5R6T0XZBN6', task, 'mystery', model, 'x', 'o3', '{}', 0, created_at FROM decision_suggestions",
		"pending with a decided": "UPDATE decision_suggestions SET decided_at = created_at",
	} {
		err := f.db.WriteTx(f.ctx, func(ctx context.Context, tx *sql.Tx) error { _, err := tx.ExecContext(ctx, q); return err })
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := f.db.WriteTx(f.ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE decision_suggestions SET state = 'rejected', decided_at = created_at, version = 2 WHERE id = ?", sg.ID)
		return err
	}); err != nil {
		t.Fatalf("a pending suggestion could not be rejected: %v", err)
	}
	if err := f.db.WriteTx(f.ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE decision_suggestions SET state = 'accepted' WHERE id = ?", sg.ID)
		return err
	}); err == nil {
		t.Fatal("a decided suggestion changed state again")
	}
}

func TestReserveCountsUpToTheCap(t *testing.T) {
	f := newFixture(t, false)
	for i := range 3 {
		ok, err := f.ds.Reserve(f.ctx, decisions.TaskAlerts, "2026-11", 3)
		if err != nil || !ok {
			t.Fatalf("attempt %d: %v %v", i, ok, err)
		}
	}
	if ok, _ := f.ds.Reserve(f.ctx, decisions.TaskAlerts, "2026-11", 3); ok {
		t.Fatal("a fourth attempt was counted under a cap of 3")
	}
	if ok, _ := f.ds.Reserve(f.ctx, decisions.TaskAlerts, "2026-12", 3); !ok {
		t.Fatal("a new month did not start a new count")
	}
	if ok, _ := f.ds.Reserve(f.ctx, decisions.TaskCategorisation, "2026-11", 0); ok {
		t.Fatal("a zero cap counted an attempt")
	}
	if _, err := f.ds.Reserve(f.ctx, decisions.TaskAlerts, "November", 3); err == nil {
		t.Fatal("a bad period was accepted")
	}
	if n, _ := f.ds.Attempts(f.ctx, decisions.TaskAlerts, "2026-11"); n != 3 {
		t.Fatalf("attempts %d", n)
	}
}

func TestReserveHoldsUnderConcurrency(t *testing.T) {
	f := newFixture(t, false)
	var wg sync.WaitGroup
	var mu sync.Mutex
	granted := 0
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := f.ds.Reserve(f.ctx, decisions.TaskLabelSuggestion, "2026-11", 7)
			if err != nil {
				t.Error(err)
			}
			if ok {
				mu.Lock()
				granted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if granted != 7 {
		t.Fatalf("granted %d of a cap of 7", granted)
	}
}

func TestConsentRecordsTheFieldSet(t *testing.T) {
	f := newFixture(t, false)
	narrow := []decisions.Field{decisions.FieldDescription, decisions.FieldMerchantName}
	c, err := f.ds.Grant(f.ctx, alice, "typesafe", decisions.TaskPaymentMatching, "v1", narrow)
	if err != nil || !slices.Equal(c.Fields, narrow) {
		t.Fatalf("grant %+v %v", c, err)
	}
	got, ok, err := f.ds.Granted(f.ctx, "typesafe", decisions.TaskPaymentMatching)
	if err != nil || !ok || !slices.Equal(got, narrow) {
		t.Fatalf("granted %v %v %v", got, ok, err)
	}
	if !decisions.Covers(got, []decisions.Field{decisions.FieldDescription}) || decisions.Covers(got, []decisions.Field{decisions.FieldDescription, decisions.FieldAmount}) {
		t.Fatal("Covers: narrowing must be fine and widening refused")
	}
	if last := f.rec.events[len(f.rec.events)-1]; last.Details["fields"] != "description,merchant_name" {
		t.Fatalf("audit details %v", last.Details)
	}
	for _, bad := range [][]decisions.Field{nil, {}, {"iban"}, {decisions.FieldAmount, decisions.FieldAmount}} {
		if _, err := f.ds.Grant(f.ctx, alice, "openai", decisions.TaskAlerts, "v1", bad); err == nil {
			t.Errorf("accepted fields %v", bad)
		}
	}
}
