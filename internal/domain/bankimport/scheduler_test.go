package bankimport

import (
	"context"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
)

// fakeSchedule is a ScheduleStore with fixed connections and no history.
type fakeSchedule struct{ conns []banking.Connection }

func (f fakeSchedule) Connections(context.Context) ([]banking.Connection, error) {
	return f.conns, nil
}

func (fakeSchedule) RefreshRuns(context.Context, banking.ConnectionID, int) ([]banking.RefreshRun, error) {
	return nil, nil
}

func (fakeSchedule) Accounts(context.Context, banking.ConnectionID) ([]banking.StoredAccount, error) {
	return nil, nil
}

func (fakeSchedule) AccountSync(context.Context, banking.AccountID) (banking.AccountSync, error) {
	return banking.AccountSync{}, nil
}

// refuses is a Refresher for a connection that is not refreshable.
type refuses struct{}

func (refuses) Refresh(context.Context, banking.ConnectionID, banking.Provider) (Result, error) {
	return Result{}, ErrNotRefreshable
}

// stubProvider only describes its application; the scheduler calls nothing else.
type stubProvider struct{ banking.Provider }

func (stubProvider) Capabilities() banking.Capabilities {
	return banking.Capabilities{Application: "app"}
}

// A pass over a connection that cannot be refreshed must wait PollEvery, not
// zero: a zero wait would make the loop spin on that connection.
func TestPassWaitsPollEveryWhenRefreshIsNotPossible(t *testing.T) {
	now := time.Date(2026, time.November, 21, 9, 0, 0, 0, time.UTC)
	const poll = 7 * time.Minute
	st := fakeSchedule{conns: []banking.Connection{{ID: "conn-1", Application: "app", Status: banking.ConnectionActive}}}
	s := NewScheduler(st, refuses{}, map[string]banking.Provider{"app": stubProvider{}}, SchedulerOptions{
		Now: func() time.Time { return now }, PollEvery: poll,
	})
	if got := s.Pass(context.Background()); got != poll {
		t.Fatalf("Pass = %v, want PollEvery %v", got, poll)
	}
	status := s.Status()
	if len(status) != 1 || status[0].Schedule != ScheduleNotActive {
		t.Fatalf("status = %+v, want one connection in %s", status, ScheduleNotActive)
	}
}
