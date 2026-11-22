package httpapi

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
)

// fakeBanks is the stored bank facts the month view reads.
type fakeBanks struct {
	conns []banking.Connection
	accts map[banking.ConnectionID][]banking.StoredAccount
	sync  map[banking.AccountID]banking.AccountSync
	err   error
	// acctErr fails the account read.
	acctErr error
}

func (f fakeBanks) Connections(context.Context) ([]banking.Connection, error) { return f.conns, f.err }
func (f fakeBanks) Accounts(_ context.Context, id banking.ConnectionID) ([]banking.StoredAccount, error) {
	return f.accts[id], f.acctErr
}
func (f fakeBanks) Balances(context.Context, banking.AccountID) ([]banking.StoredBalance, error) {
	return nil, nil
}
func (f fakeBanks) AccountSync(_ context.Context, id banking.AccountID) (banking.AccountSync, error) {
	return f.sync[id], nil
}

func TestMonthBank(t *testing.T) {
	at := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	ok := at.Add(-time.Hour)
	conn := func(id string, st banking.ConnectionStatus) banking.Connection {
		return banking.Connection{ID: banking.ConnectionID(id), Status: st}
	}
	acct := func(id string) banking.StoredAccount { return banking.StoredAccount{ID: banking.AccountID(id)} }
	cases := []struct {
		name      string
		banks     fakeBanks
		nilBanks  bool
		available bool
		n         int
		state     string
		hasLast   bool
	}{
		{name: "no source", nilBanks: true, state: "none"},
		{name: "no connection", state: "none"},
		{name: "unreadable", banks: fakeBanks{err: errors.New("db")}, state: "unknown"},
		{name: "fresh", available: true, n: 1, state: "fresh", hasLast: true, banks: fakeBanks{
			conns: []banking.Connection{conn("c1", banking.ConnectionActive)},
			accts: map[banking.ConnectionID][]banking.StoredAccount{"c1": {acct("a1")}},
			sync:  map[banking.AccountID]banking.AccountSync{"a1": {LastSuccessAt: ok, LastAttemptAt: ok}}}},
		{name: "stale", available: true, n: 1, state: "stale", hasLast: true, banks: fakeBanks{
			conns: []banking.Connection{conn("c1", banking.ConnectionActive)},
			accts: map[banking.ConnectionID][]banking.StoredAccount{"c1": {acct("a1")}},
			sync:  map[banking.AccountID]banking.AccountSync{"a1": {LastSuccessAt: at.Add(-72 * time.Hour)}}}},
		{name: "never refreshed, no accounts", available: true, n: 1, state: "never_refreshed", banks: fakeBanks{
			conns: []banking.Connection{conn("c1", banking.ConnectionActive)}}},
		{name: "disconnected beats fresh", available: true, n: 2, state: "disconnected", hasLast: true, banks: fakeBanks{
			conns: []banking.Connection{conn("c1", banking.ConnectionActive), conn("c2", banking.ConnectionDisconnected)},
			accts: map[banking.ConnectionID][]banking.StoredAccount{"c1": {acct("a1")}},
			sync:  map[banking.AccountID]banking.AccountSync{"a1": {LastSuccessAt: ok}}}},
		{name: "failed, no account", available: true, n: 1, state: "failed", banks: fakeBanks{
			conns: []banking.Connection{conn("c1", banking.ConnectionFailed)}}},
		{name: "unreadable accounts", banks: fakeBanks{
			conns:   []banking.Connection{conn("c1", banking.ConnectionActive)},
			acctErr: errors.New("db")}, state: "unknown"},
		{name: "reconnect required", available: true, n: 1, state: "reconnect_required", banks: fakeBanks{
			conns: []banking.Connection{conn("c1", banking.ConnectionReconnectRequired)}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got monthBankJSON
			if tc.nilBanks {
				got = monthBank(context.Background(), nil, at, 0)
			} else {
				got = monthBank(context.Background(), tc.banks, at, 0)
			}
			if got.Available != tc.available || got.Connections != tc.n || got.State != tc.state || (got.LastSuccessAt != nil) != tc.hasLast || got.Note == "" {
				t.Fatalf("got %+v", got)
			}
		})
	}
}
