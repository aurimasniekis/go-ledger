package banking

import (
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
)

func tx(ref, amount string, day int, status BookingStatus, desc string) Transaction {
	return Transaction{
		EntryReference: ref, Status: status, ProviderStatus: string(status),
		Amount: money.MustParse(amount, "EUR"), BookingDate: calendar.MustDate(2026, time.October, day), Description: desc,
	}
}

func keys(t *testing.T, txs ...Transaction) []KeyedTransaction {
	t.Helper()
	out, err := KeyTransactions(txs)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestKeysAreDeterministicAndKindTagged(t *testing.T) {
	a := keys(t, tx("r1", "-10.00", 1, StatusBooked, "SHOP"), tx("", "-3.50", 2, StatusBooked, "COFFEE"))
	b := keys(t, tx("r1", "-10.00", 1, StatusBooked, "SHOP"), tx("", "-3.50", 2, StatusBooked, "COFFEE"))
	for i := range a {
		if a[i].Key != b[i].Key {
			t.Errorf("key %d differs between runs", i)
		}
	}
	if a[0].KeyKind != KeyEntryReference || !strings.HasPrefix(a[0].Key, "ref:") || !strings.HasSuffix(a[0].Key, "#1") {
		t.Errorf("ref key = %s (%s)", a[0].Key, a[0].KeyKind)
	}
	if a[1].KeyKind != KeyFallback || !strings.HasPrefix(a[1].Key, "fb:") {
		t.Errorf("fallback key = %s (%s)", a[1].Key, a[1].KeyKind)
	}
	if strings.Contains(a[1].Key, "COFFEE") || strings.Contains(a[0].Key, "r1") {
		t.Error("key holds raw text")
	}
}

func TestIdenticalTransactionsGetOrdinals(t *testing.T) {
	k := keys(t, tx("", "-3.50", 7, StatusBooked, "COFFEE"), tx("", "-3.50", 7, StatusBooked, "COFFEE"))
	if k[0].Key == k[1].Key {
		t.Fatal("two identical transactions share a key")
	}
	if strings.TrimSuffix(k[0].Key, "#1") != strings.TrimSuffix(k[1].Key, "#2") {
		t.Errorf("ordinals not applied to one base: %s, %s", k[0].Key, k[1].Key)
	}
}

func TestRepeatedEntryReferenceWithDifferentAmountsStaysApart(t *testing.T) {
	k := keys(t, tx("dup", "-10.00", 1, StatusBooked, "A"), tx("dup", "-20.00", 1, StatusBooked, "B"))
	if strings.TrimSuffix(k[0].Key, "#1") == strings.TrimSuffix(k[1].Key, "#1") {
		t.Error("a repeated entry reference with another amount collapsed into one key")
	}
}

func TestPendingToBookedKeepsKeyWhenIdentityIsStable(t *testing.T) {
	// With a reference: date and status may change.
	p := keys(t, tx("card-1", "-45.00", 29, StatusPending, "BOOKSHOP"))[0]
	b := keys(t, tx("card-1", "-45.00", 31, StatusBooked, "BOOKSHOP AMENDED"))[0]
	if p.Key != b.Key {
		t.Error("pending and booked with one reference got different keys")
	}
	// Without: same content, only the status changes.
	p = keys(t, tx("", "-9.00", 3, StatusPending, "BUS"))[0]
	b = keys(t, tx("", "-9.00", 3, StatusBooked, "BUS"))[0]
	if p.Key != b.Key {
		t.Error("status is part of the fallback key")
	}
	// Without, and the amount changes on booking: a new key (B2 marks the
	// pending row no longer reported).
	b = keys(t, tx("", "-9.50", 3, StatusBooked, "BUS"))[0]
	if p.Key == b.Key {
		t.Error("a different amount kept the fallback key")
	}
}

func TestScaleIsPartOfTheKey(t *testing.T) {
	// Providers report a stable scale; 1.5 and 1.50 are different source
	// texts and stay distinct rather than being silently merged.
	a := keys(t, tx("", "-1.5", 1, StatusBooked, "X"))[0]
	b := keys(t, tx("", "-1.50", 1, StatusBooked, "X"))[0]
	if a.Key == b.Key {
		t.Error("scale ignored")
	}
}

func TestKeyTransactionsRejectsInvalidWithoutLeakingContent(t *testing.T) {
	bad := tx("", "-1.00", 1, StatusBooked, "SECRET DESCRIPTION")
	bad.BookingDate = calendar.Date{}
	_, err := KeyTransactions([]Transaction{bad})
	if err == nil {
		t.Fatal("a transaction without any date was keyed")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("error leaks the description: %v", err)
	}
	if _, err := KeyTransactions([]Transaction{{Status: "weird", ProviderStatus: "X", Amount: money.MustParse("1", "EUR"), BookingDate: calendar.MustDate(2026, 1, 1)}}); err == nil {
		t.Error("an unknown status was keyed")
	}
}
