package banking_test

import (
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
)

func TestAccountFreshness(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	ok := banking.AccountSync{LastAttemptAt: now.Add(-time.Hour), LastSuccessAt: now.Add(-time.Hour)}
	old := banking.AccountSync{LastAttemptAt: now.Add(-25 * time.Hour), LastSuccessAt: now.Add(-25 * time.Hour)}
	failed := banking.AccountSync{LastAttemptAt: now.Add(-time.Minute), LastSuccessAt: now.Add(-time.Hour), LastErrorKind: banking.KindRetryable, LastErrorCode: "unavailable"}
	for _, tc := range []struct {
		name  string
		conn  banking.ConnectionStatus
		sync  banking.AccountSync
		after time.Duration
		want  banking.FreshnessState
	}{
		{"fresh", banking.ConnectionActive, ok, 0, banking.FreshnessFresh},
		{"older than 24h", banking.ConnectionActive, old, 0, banking.FreshnessStale},
		{"custom threshold", banking.ConnectionActive, ok, 30 * time.Minute, banking.FreshnessStale},
		{"latest attempt failed", banking.ConnectionActive, failed, 0, banking.FreshnessFailed},
		{"never refreshed", banking.ConnectionActive, banking.AccountSync{}, 0, banking.FreshnessNeverRefreshed},
		{"attempted, never succeeded", banking.ConnectionActive, banking.AccountSync{LastAttemptAt: now}, 0, banking.FreshnessNeverRefreshed},
		{"reconnect required", banking.ConnectionReconnectRequired, ok, 0, banking.FreshnessReconnectRequired},
		{"disconnected", banking.ConnectionDisconnected, ok, 0, banking.FreshnessDisconnected},
		{"consent failed", banking.ConnectionFailed, ok, 0, banking.FreshnessDisconnected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := banking.AccountFreshness(tc.conn, tc.sync, now, tc.after)
			if f.State != tc.want || f.Stale != (tc.want != banking.FreshnessFresh) {
				t.Errorf("freshness = %s (stale %v), want %s", f.State, f.Stale, tc.want)
			}
			if !f.LastSuccessAt.Equal(tc.sync.LastSuccessAt) || f.LastErrorCode != tc.sync.LastErrorCode {
				t.Error("timestamps or error not carried through")
			}
		})
	}
}

func TestCountable(t *testing.T) {
	booked := banking.StoredTransaction{KeyedTransaction: banking.KeyedTransaction{Transaction: banking.Transaction{Status: banking.StatusBooked}}}
	if !booked.Countable() {
		t.Error("a reported booked transaction is not countable")
	}
	for name, tx := range map[string]banking.StoredTransaction{
		"pending":            {KeyedTransaction: banking.KeyedTransaction{Transaction: banking.Transaction{Status: banking.StatusPending}}},
		"cancelled":          {KeyedTransaction: banking.KeyedTransaction{Transaction: banking.Transaction{Status: banking.StatusCancelled}}},
		"no longer reported": {KeyedTransaction: booked.KeyedTransaction, NoLongerReportedAt: time.Now()},
		"superseded":         {KeyedTransaction: booked.KeyedTransaction, SupersededBy: "x"},
	} {
		if tx.Countable() {
			t.Errorf("%s transaction is countable", name)
		}
	}
}
