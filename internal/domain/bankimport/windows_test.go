package bankimport_test

import (
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/bankimport"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
)

func d(m time.Month, day int) calendar.Date { return calendar.MustDate(2026, m, day) }

func TestWindows(t *testing.T) {
	today := d(time.November, 1)
	e := bankimport.New(nil, bankimport.Options{OverlapDays: 7, InitialHistoryDays: 90, ChunkDays: 31})
	for _, tc := range []struct {
		name       string
		sync       banking.AccountSync
		maxHistory int
		want       []bankimport.Window
	}{
		{
			name: "first refresh: initial history in chunks, oldest first",
			want: []bankimport.Window{{d(time.August, 4), d(time.September, 3)}, {d(time.September, 4), d(time.October, 4)}, {d(time.October, 5), today}},
		},
		{
			name:       "first refresh capped by the provider's history",
			maxHistory: 10,
			want:       []bankimport.Window{{d(time.October, 23), today}},
		},
		{
			name: "resume: overlap before the last complete window",
			sync: banking.AccountSync{TransactionsThrough: d(time.October, 30)},
			want: []bankimport.Window{{d(time.October, 23), today}},
		},
		{
			name: "resume after a long gap fills the gap",
			sync: banking.AccountSync{TransactionsThrough: d(time.June, 30)},
			want: []bankimport.Window{{d(time.June, 23), d(time.July, 23)}, {d(time.July, 24), d(time.August, 23)}, {d(time.August, 24), d(time.September, 23)}, {d(time.September, 24), d(time.October, 24)}, {d(time.October, 25), today}},
		},
		{
			name: "reaches back to an open pending item",
			sync: banking.AccountSync{TransactionsThrough: d(time.October, 31), OldestOpenPending: d(time.October, 10)},
			want: []bankimport.Window{{d(time.October, 10), today}},
		},
		{
			name: "but not beyond the initial history",
			sync: banking.AccountSync{TransactionsThrough: d(time.October, 31), OldestOpenPending: d(time.January, 10)},
			want: []bankimport.Window{{d(time.August, 4), d(time.September, 3)}, {d(time.September, 4), d(time.October, 4)}, {d(time.October, 5), today}},
		},
		{
			name: "a cursor in the future still fetches today",
			sync: banking.AccountSync{TransactionsThrough: d(time.December, 31)},
			want: []bankimport.Window{{today, today}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := e.Windows(today, tc.sync, tc.maxHistory)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("windows = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("window %d = %v, want %v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestWindowsAreContiguousWholeDays(t *testing.T) {
	today := d(time.November, 1)
	for _, chunk := range []int{1, 2, 7, 31, 365} {
		e := bankimport.New(nil, bankimport.Options{ChunkDays: chunk, InitialHistoryDays: 100})
		ws, err := e.Windows(today, banking.AccountSync{}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if ws[len(ws)-1].To != today {
			t.Errorf("chunk %d: last window ends %s", chunk, ws[len(ws)-1].To)
		}
		for i, w := range ws {
			if w.From.After(w.To) || w.From.DaysUntil(w.To)+1 > chunk {
				t.Errorf("chunk %d: window %v", chunk, w)
			}
			if i > 0 {
				if next, _ := ws[i-1].To.AddDays(1); next != w.From {
					t.Errorf("chunk %d: gap or overlap before %v", chunk, w)
				}
			}
		}
	}
}
