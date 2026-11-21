package enablebanking_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/bankimport"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/store"
	"github.com/aurimasniekis/go-ledger/internal/store/bankfacts"
)

// End-to-end refresh: the adapter, talking to the offline fake, feeds the
// B2 import engine and the SQLite bank-fact store, in both modes.

type refreshEnv struct {
	*env
	facts  *bankfacts.Store
	engine *bankimport.Engine
	conn   banking.Connection
	logs   *bytes.Buffer
	sess   banking.Session
}

func newRefreshEnv(t *testing.T, mode banking.Mode) *refreshEnv {
	t.Helper()
	e := newTestEnv(t, mode)
	db, err := store.Open(e.ctx, filepath.Join(t.TempDir(), "ledger.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	r := &refreshEnv{env: e, facts: bankfacts.New(db, nil), logs: &bytes.Buffer{}}
	r.engine = bankimport.New(r.facts, bankimport.Options{
		Now:    e.clock.Now,
		Sleep:  func(context.Context, time.Duration) error { return nil },
		Jitter: func(d time.Duration) time.Duration { return d },
		Log:    slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})

	insts, err := e.p.Institutions(e.ctx, "FI")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.p.StartConsent(e.ctx, banking.ConsentRequest{Institution: insts[0], ValidUntil: t0.Add(90 * 24 * time.Hour), RedirectURL: "https://ledger.example/callback", State: "st", PSUType: banking.PSUPersonal}); err != nil {
		t.Fatal(err)
	}
	conn, err := r.facts.CreateConnection(e.ctx, banking.NewConnection{Capabilities: e.p.Capabilities(), Institution: insts[0], ConsentState: "st"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	r.sess, err = e.p.CompleteConsent(e.ctx, banking.ConsentCompletion{State: "st", Code: banking.NewSensitive(syntheticCode)})
	if err != nil {
		t.Fatal(err)
	}
	if r.conn, err = r.facts.ActivateConnection(e.ctx, conn.ID, r.sess, t0); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *refreshEnv) accounts() map[string]banking.StoredAccount {
	r.t.Helper()
	accs, err := r.facts.Accounts(r.ctx, r.conn.ID)
	if err != nil {
		r.t.Fatal(err)
	}
	out := map[string]banking.StoredAccount{}
	for _, a := range accs {
		out[a.ExternalID] = a
	}
	return out
}

// assertLogsClean checks the refresh logs hold no identifiers, session
// references, descriptions, counterparties or response text.
func (r *refreshEnv) assertLogsClean() {
	r.t.Helper()
	logs := r.logs.String()
	if logs == "" {
		r.t.Fatal("no logs captured")
	}
	forbidden := []string{syntheticIBAN1, "SYNTHETIC-BBAN", "1111", r.sess.Reference.Reveal(), "uid-", testAppID, echoMarker, "SALARY", "Example Employer", "synthetic-ref", "Bearer"}
	for _, f := range forbidden {
		if strings.Contains(logs, f) {
			r.t.Fatalf("logs contain %q:\n%s", f, logs)
		}
	}
}

func TestRefreshThroughImportEngineBothModes(t *testing.T) {
	for _, mode := range bothModes {
		t.Run(string(mode), func(t *testing.T) {
			r := newRefreshEnv(t, mode)
			if r.conn.Mode != mode || r.conn.Provider != enablebankingName || r.conn.AccountAccess != mode.AccountAccess() {
				t.Fatalf("connection %+v", r.conn)
			}
			res, err := r.engine.Refresh(r.ctx, r.conn.ID, r.p)
			if err != nil {
				t.Fatalf("refresh: %v (run %+v)", err, res.Run)
			}
			accs := r.accounts()
			wantAccounts := map[banking.Mode]int{banking.ModeApp: 2, banking.ModeDeveloper: 1}[mode]
			if len(accs) != wantAccounts {
				t.Fatalf("%d accounts stored, want %d", len(accs), wantAccounts)
			}
			eur := accs[hashEUR]
			txs, err := r.facts.Transactions(r.ctx, eur.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(txs) != 6 {
				t.Fatalf("%d EUR transactions stored, want 6 (one contradictory row dropped)", len(txs))
			}
			if res.Run.TransactionsRejected != 1 || res.Run.BalancesRejected != 1 {
				t.Fatalf("run counts %+v", res.Run.RefreshCounts)
			}
			countable := 0
			for _, tx := range txs {
				if tx.Countable() {
					countable++
				}
			}
			if countable != 4 {
				t.Fatalf("%d countable rows, want 4 booked", countable)
			}
			bals, err := r.facts.Balances(r.ctx, eur.ID)
			if err != nil || len(bals) != 3 {
				t.Fatalf("balances %d, %v", len(bals), err)
			}

			// A second refresh is idempotent.
			res2, err := r.engine.Refresh(r.ctx, r.conn.ID, r.p)
			if err != nil {
				t.Fatal(err)
			}
			if res2.Run.TransactionsInserted != 0 {
				t.Fatalf("second refresh inserted %d", res2.Run.TransactionsInserted)
			}
			txs2, _ := r.facts.Transactions(r.ctx, eur.ID)
			if len(txs2) != 6 {
				t.Fatalf("%d transactions after second refresh", len(txs2))
			}
			r.assertLogsClean()
		})
	}
}

const enablebankingName = "enablebanking"

func TestRefreshExpiredConsentMarksReconnect(t *testing.T) {
	for _, mode := range bothModes {
		t.Run(string(mode), func(t *testing.T) {
			r := newRefreshEnv(t, mode)
			if _, err := r.engine.Refresh(r.ctx, r.conn.ID, r.p); err != nil {
				t.Fatal(err)
			}
			eur := r.accounts()[hashEUR]
			before, _ := r.facts.Transactions(r.ctx, eur.ID)
			// The bank ends the session early (as EXPIRED_SESSION).
			r.f.setSession("EXPIRED")
			_, err := r.engine.Refresh(r.ctx, r.conn.ID, r.p)
			if !errors.Is(err, banking.ErrConsentExpired) {
				t.Fatalf("refresh error %v", err)
			}
			c, err := r.facts.Connection(r.ctx, r.conn.ID)
			if err != nil || c.Status != banking.ConnectionReconnectRequired {
				t.Fatalf("connection status %s, %v", c.Status, err)
			}
			after, _ := r.facts.Transactions(r.ctx, eur.ID)
			if len(after) != len(before) {
				t.Fatal("expired consent changed stored facts")
			}
			sync, err := r.facts.AccountSync(r.ctx, eur.ID)
			if err != nil {
				t.Fatal(err)
			}
			if f := banking.AccountFreshness(c.Status, sync, r.clock.Now(), 0); f.State != banking.FreshnessReconnectRequired || !f.Stale {
				t.Fatalf("freshness %+v", f)
			}
			r.assertLogsClean()
		})
	}
}

func TestRefreshRateLimitAndOutageKeepData(t *testing.T) {
	for _, tc := range []struct {
		name string
		ft   fault
		kind banking.ErrorKind
	}{
		{"bank rate limit", fault{match: "/transactions", status: 429, file: "error_rate_limit.json"}, banking.KindRateLimited},
		{"outage", fault{match: "/balances", status: 503, raw: "maintenance"}, banking.KindRetryable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRefreshEnv(t, banking.ModeDeveloper)
			if _, err := r.engine.Refresh(r.ctx, r.conn.ID, r.p); err != nil {
				t.Fatal(err)
			}
			eur := r.accounts()[hashEUR]
			before, _ := r.facts.Transactions(r.ctx, eur.ID)
			r.clock.Add(25 * time.Hour)
			// Enough failures to outlast the engine's three retries.
			for range 4 {
				r.f.fail(tc.ft)
			}
			res, err := r.engine.Refresh(r.ctx, r.conn.ID, r.p)
			if banking.Classify(err) != tc.kind {
				t.Fatalf("refresh error %v, want kind %s", err, tc.kind)
			}
			if res.Run.Outcome == banking.RefreshSucceeded {
				t.Fatalf("outcome %s", res.Run.Outcome)
			}
			c, _ := r.facts.Connection(r.ctx, r.conn.ID)
			if c.Status != banking.ConnectionActive {
				t.Fatalf("connection status %s: a rate limit or outage must not need a reconnect", c.Status)
			}
			after, _ := r.facts.Transactions(r.ctx, eur.ID)
			if len(after) != len(before) {
				t.Fatal("failed refresh changed stored facts")
			}
			sync, _ := r.facts.AccountSync(r.ctx, eur.ID)
			if f := banking.AccountFreshness(c.Status, sync, r.clock.Now(), 0); !f.Stale {
				t.Fatalf("freshness %+v: data must show as stale", f)
			}
			r.assertLogsClean()
		})
	}
}
