package bankconnect_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/bankconnect"
	"github.com/aurimasniekis/go-ledger/internal/domain/bankimport"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
)

// clock is a fake clock the tests move by hand.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// runStore is a view store that remembers the runs the engine double records.
type runStore struct {
	viewStore
	mu   sync.Mutex
	runs []banking.RefreshRun
}

func (r *runStore) RefreshRuns(_ context.Context, _ banking.ConnectionID, limit int) ([]banking.RefreshRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []banking.RefreshRun // newest first
	for i := len(r.runs) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, r.runs[i])
	}
	return out, nil
}

func (r *runStore) add(run banking.RefreshRun) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs = append(r.runs, run)
}

// recordingEngine records a finished run at the fake clock, like the real
// engine does, and counts its calls.
type recordingEngine struct {
	clk     *clock
	store   *runStore
	outcome banking.RefreshOutcome
	mu      sync.Mutex
	calls   int
}

func (e *recordingEngine) Refresh(_ context.Context, id banking.ConnectionID, _ banking.Provider) (bankimport.Result, error) {
	e.mu.Lock()
	e.calls++
	n := e.calls
	e.mu.Unlock()
	run := banking.RefreshRun{ID: banking.RefreshRunID("run-" + string(rune('0'+n))), ConnectionID: id, Outcome: e.outcome, StartedAt: e.clk.now(), FinishedAt: e.clk.now()}
	e.store.add(run)
	return bankimport.Result{Run: run}, nil
}

func (e *recordingEngine) count() int { e.mu.Lock(); defer e.mu.Unlock(); return e.calls }

func cooldownSetup(t *testing.T, cooldown time.Duration, outcome banking.RefreshOutcome) (*setup, *recordingEngine, *runStore, *clock) {
	t.Helper()
	s := newSetup(t, banking.ConnectionActive, banking.NewSensitive("sess"))
	clk := &clock{t: t0}
	st := &runStore{viewStore: viewStore{memStore: s.mem}}
	s.svc.Facts = st
	s.svc.Now = clk.now
	s.svc.RefreshCooldown = cooldown
	eng := &recordingEngine{clk: clk, store: st, outcome: outcome}
	s.svc.Engine = eng
	return s, eng, st, clk
}

// D-B6c: after a refresh finishes, another one on request is refused until
// the cooldown has passed, counted from when the refresh finished.
func TestRefreshOnRequestIsRefusedInsideTheCooldownAndAllowedAfter(t *testing.T) {
	ctx := context.Background()
	s, eng, _, clk := cooldownSetup(t, 5*time.Minute, banking.RefreshSucceeded)

	if _, err := s.svc.Refresh(ctx, alice, "conn-1"); err != nil {
		t.Fatalf("the first refresh: %v", err)
	}
	clk.add(4*time.Minute + 59*time.Second)
	_, err := s.svc.Refresh(ctx, alice, "conn-1")
	var cd *bankconnect.CooldownError
	if !errors.Is(err, bankconnect.ErrRefreshCooldown) || !errors.As(err, &cd) {
		t.Fatalf("inside the window: %v, want a cooldown error", err)
	}
	if cd.RetryAfter != time.Second || !cd.Until.Equal(t0.Add(5*time.Minute)) {
		t.Errorf("retry after %s until %s, want 1s until %s", cd.RetryAfter, cd.Until, t0.Add(5*time.Minute))
	}
	if eng.count() != 1 {
		t.Fatalf("the engine ran %d times; a refused request must not start one", eng.count())
	}
	v, err := s.svc.View(ctx, "conn-1")
	if err != nil || !v.RefreshAvailableAt.Equal(t0.Add(5*time.Minute)) {
		t.Fatalf("view says available at %s (%v), want %s", v.RefreshAvailableAt, err, t0.Add(5*time.Minute))
	}

	clk.add(time.Second)
	if _, err := s.svc.Refresh(ctx, alice, "conn-1"); err != nil {
		t.Fatalf("exactly at the end of the window: %v", err)
	}
	if eng.count() != 2 {
		t.Fatalf("engine ran %d times, want 2", eng.count())
	}
	// The second refresh starts a new window.
	if _, err := s.svc.Refresh(ctx, alice, "conn-1"); !errors.Is(err, bankconnect.ErrRefreshCooldown) {
		t.Fatalf("right after the second refresh: %v", err)
	}
	clk.add(5 * time.Minute)
	v, err = s.svc.View(ctx, "conn-1")
	if err != nil || !v.RefreshAvailableAt.IsZero() {
		t.Fatalf("after the window the view must say allowed: %s %v", v.RefreshAvailableAt, err)
	}
}

// A refresh that failed also finished, and also holds the next one back.
func TestFailedRefreshStartsTheCooldownToo(t *testing.T) {
	s, _, _, _ := cooldownSetup(t, 5*time.Minute, banking.RefreshFailed)
	if _, err := s.svc.Refresh(context.Background(), alice, "conn-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.svc.Refresh(context.Background(), alice, "conn-1"); !errors.Is(err, bankconnect.ErrRefreshCooldown) {
		t.Fatalf("after a failed run: %v", err)
	}
}

// A run still going is for the engine to refuse (409 refresh_in_progress), not
// the cooldown.
func TestRunningRunIsNotACooldown(t *testing.T) {
	s, eng, st, _ := cooldownSetup(t, 5*time.Minute, banking.RefreshSucceeded)
	st.add(banking.RefreshRun{ID: "run-x", ConnectionID: "conn-1", Outcome: banking.RefreshRunning, StartedAt: t0})
	if _, err := s.svc.Refresh(context.Background(), alice, "conn-1"); errors.Is(err, bankconnect.ErrRefreshCooldown) {
		t.Fatal("a running run must not start a cooldown")
	}
	if eng.count() != 1 {
		t.Fatal("the engine must decide about a running run")
	}
}

// The scheduled refresh calls the engine, never the service: a connection in
// its cooldown is still refreshed on schedule. A zero cooldown switches the
// refusal off for requests too.
func TestScheduledRefreshAndZeroCooldownAreUnaffected(t *testing.T) {
	ctx := context.Background()
	s, eng, _, _ := cooldownSetup(t, 5*time.Minute, banking.RefreshSucceeded)
	if _, err := s.svc.Refresh(ctx, alice, "conn-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Refresh(ctx, "conn-1", s.prov); err != nil { // what the scheduler does
		t.Fatalf("scheduled refresh inside the window: %v", err)
	}
	if eng.count() != 2 {
		t.Fatalf("engine ran %d times, want 2", eng.count())
	}

	s, eng, _, _ = cooldownSetup(t, 0, banking.RefreshSucceeded)
	for range 3 {
		if _, err := s.svc.Refresh(ctx, alice, "conn-1"); err != nil {
			t.Fatalf("cooldown off: %v", err)
		}
	}
	if eng.count() != 3 {
		t.Fatalf("engine ran %d times, want 3", eng.count())
	}
}
