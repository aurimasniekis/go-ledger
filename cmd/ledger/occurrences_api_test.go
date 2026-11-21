package main

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
)

type occurrencePage struct {
	Items []struct {
		ID           string `json:"id"`
		CommitmentID string `json:"commitment_id"`
		Month        string `json:"month"`
		DueDate      string `json:"due_date"`
		Amount       struct {
			Amount   string `json:"amount"`
			Currency string `json:"currency"`
		} `json:"amount"`
		Version int64 `json:"version"`
	} `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

func (f *apiFixture) occurrences(t *testing.T, query string, who string) occurrencePage {
	t.Helper()
	rec := f.do("GET", "/api/v1/occurrences?"+query, "", map[string]string{"X-Test-User": f.ids[who]})
	if rec.Code != 200 {
		t.Fatalf("GET occurrences?%s: %d %s", query, rec.Code, rec.Body.String())
	}
	var p occurrencePage
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func (f *apiFixture) createCommitment(t *testing.T, body string) string {
	t.Helper()
	rec := f.do("POST", "/api/v1/commitments", body, map[string]string{"X-Test-User": f.ids["alice"]})
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var c struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &c)
	return c.ID
}

const monthEndBody = `{"name":"VPS","kind":"server","amount":{"amount":"10.00","currency":"EUR"},"schedule":{"version":1,"frequency":"monthly","interval":1,"day":31},"start_date":"2026-01-01"}`

// TestOccurrencesTimezoneBoundary checks that "today", the default effective
// date of an edit, is the date in the reporting time zone (D5): at 22:30 UTC
// on January 31 it is already February 1 in Vilnius, so the January 31
// occurrence is history there, while in UTC it is still due today and
// follows the edit.
func TestOccurrencesTimezoneBoundary(t *testing.T) {
	instant := time.Date(2026, 1, 31, 22, 30, 0, 0, time.UTC)
	for _, c := range []struct {
		zone, janAmount string
	}{
		{"Europe/Vilnius", "10.00"},
		{"UTC", "12.00"},
	} {
		t.Run(c.zone, func(t *testing.T) {
			f := newAPIFixtureIn(t, calendar.MustLoadZone(c.zone))
			f.now = instant
			id := f.createCommitment(t, monthEndBody)
			if p := f.occurrences(t, "month=2026-01", "alice"); len(p.Items) != 1 || p.Items[0].DueDate != "2026-01-31" || p.Items[0].Month != "2026-01" {
				t.Fatalf("January: %+v", p)
			}
			rec := f.do("PATCH", "/api/v1/commitments/"+id, `{"amount":{"amount":"12.00"}}`, map[string]string{"X-Test-User": f.ids["alice"], "If-Match": `"1"`})
			if rec.Code != 200 {
				t.Fatalf("patch: %d %s", rec.Code, rec.Body.String())
			}
			jan := f.occurrences(t, "month=2026-01", "alice")
			if jan.Items[0].Amount.Amount != c.janAmount {
				t.Errorf("January 31 amount %s, want %s", jan.Items[0].Amount.Amount, c.janAmount)
			}
			feb := f.occurrences(t, "month=2026-02", "bob") // read-only users read occurrences
			if len(feb.Items) != 1 || feb.Items[0].DueDate != "2026-02-28" || feb.Items[0].Amount.Amount != "12.00" {
				t.Errorf("February: %+v", feb)
			}
			wantFrom := map[string]string{"Europe/Vilnius": "2026-02-01", "UTC": "2026-01-31"}[c.zone]
			if !strings.Contains(f.log.String(), `"detail_effective_from":"`+wantFrom+`"`) {
				t.Errorf("regeneration audit lacks effective_from %s: %s", wantFrom, f.log.String())
			}
		})
	}
}

func TestOccurrencesAPI(t *testing.T) {
	f := newAPIFixture(t)
	f.now = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	admin := map[string]string{"X-Test-User": f.ids["alice"]}
	id := f.createCommitment(t, monthEndBody)
	f.createCommitment(t, strings.Replace(monthEndBody, `"day":31`, `"day":5`, 1))
	f.createCommitment(t, strings.Replace(monthEndBody, `"day":31`, `"day":15`, 1))

	// Pages in due-date order.
	p := f.occurrences(t, "month=2026-04&limit=2", "alice")
	if len(p.Items) != 2 || p.Items[0].DueDate != "2026-04-05" || p.Items[1].DueDate != "2026-04-15" || p.NextCursor == nil {
		t.Fatalf("first page: %+v", p)
	}
	p2 := f.occurrences(t, "month=2026-04&limit=2&cursor="+url.QueryEscape(*p.NextCursor), "alice")
	if len(p2.Items) != 1 || p2.Items[0].DueDate != "2026-04-30" || p2.NextCursor != nil {
		t.Fatalf("second page: %+v", p2)
	}
	if one := f.occurrences(t, "month=2026-04&commitment_id="+id, "alice"); len(one.Items) != 1 || one.Items[0].CommitmentID != id {
		t.Fatalf("filter: %+v", one)
	}
	occID := p2.Items[0].ID
	if rec := f.do("GET", "/api/v1/occurrences/"+occID, "", admin); rec.Code != 200 || rec.Header().Get("ETag") != `"1"` || !strings.Contains(rec.Body.String(), `"due_date":"2026-04-30"`) {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}

	// An explicit effective date reaches back over overdue unpaid occurrences.
	rec := f.do("PATCH", "/api/v1/commitments/"+id, `{"schedule":{"day":28},"effective_from":"2026-04-01","version":1}`, admin)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "effective_from") {
		t.Fatalf("patch with effective_from: %d %s", rec.Code, rec.Body.String())
	}
	rec = f.do("GET", "/api/v1/occurrences/"+occID, "", admin)
	if !strings.Contains(rec.Body.String(), `"due_date":"2026-04-28"`) || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("rewritten occurrence: %s %s", rec.Header().Get("ETag"), rec.Body.String())
	}
	if mar := f.occurrences(t, "month=2026-03&commitment_id="+id, "alice"); mar.Items[0].DueDate != "2026-03-31" {
		t.Errorf("March is before the effective date: %+v", mar)
	}

	bad := []struct{ method, path, body, field string }{
		{"GET", "/api/v1/occurrences", "", "month"},
		{"GET", "/api/v1/occurrences?month=2026-13", "", "month"},
		{"GET", "/api/v1/occurrences?month=2026-01&month=2026-02", "", "month"},
		{"GET", "/api/v1/occurrences?month=2031-12", "", "month"}, // beyond 60 months ahead
		{"GET", "/api/v1/occurrences?month=2026-01&commitment_id=nope", "", "commitment_id"},
		{"PATCH", "/api/v1/commitments/" + id, `{"amount":{"amount":"1.00"},"effective_from":"tomorrow","version":2}`, "effective_from"},
		{"PATCH", "/api/v1/commitments/" + id, `{"amount":{"amount":"1.00"},"effective_from":null,"version":2}`, "effective_from"},
		{"PATCH", "/api/v1/commitments/" + id, `{"amount":{"amount":"1.00"},"effective_from":"2032-01-01","version":2}`, "effective_from"},
	}
	for _, b := range bad {
		rec := f.do(b.method, b.path, b.body, admin)
		if rec.Code != 400 || errFields(t, rec.Body.Bytes()) != b.field {
			t.Errorf("%s %s %s: %d %s", b.method, b.path, b.body, rec.Code, rec.Body.String())
		}
	}
	if rec := f.do("GET", "/api/v1/occurrences/01JD3Y4C8V9M2Q7W5R6T0XZBNZ", "", admin); rec.Code != 404 {
		t.Errorf("missing occurrence: %d", rec.Code)
	}
}

// TestFutureDatedTermsAPI repeats the sequence that used to turn April to
// August into the September price: today 2026-03-15, monthly day 10 at 10.00.
func TestFutureDatedTermsAPI(t *testing.T) {
	f := newAPIFixture(t)
	f.now = time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)
	admin := map[string]string{"X-Test-User": f.ids["alice"]}
	id := f.createCommitment(t, strings.Replace(monthEndBody, `"day":31`, `"day":10`, 1))
	f.occurrences(t, "month=2026-04", "alice")

	if rec := f.do("PATCH", "/api/v1/commitments/"+id, `{"amount":{"amount":"12.00"},"effective_from":"2026-09-01","version":1}`, admin); rec.Code != 200 {
		t.Fatalf("future price: %d %s", rec.Code, rec.Body.String())
	}
	for _, body := range []string{`{"schedule":{"day":11},"version":2}`, `{"schedule":{"day":11},"effective_from":"2026-01-01","version":2}`} {
		rec := f.do("PATCH", "/api/v1/commitments/"+id, body, admin)
		if rec.Code != 409 || !strings.Contains(rec.Body.String(), `"code":"pending_terms_change"`) || errFields(t, rec.Body.Bytes()) != "effective_from" ||
			!strings.Contains(rec.Body.String(), "2026-09-01") {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	for m, want := range map[string]string{"2026-04": "10.00", "2026-08": "10.00", "2026-09": "12.00"} {
		p := f.occurrences(t, "month="+m, "alice")
		if len(p.Items) != 1 || p.Items[0].Amount.Amount != want || p.Items[0].DueDate != m+"-10" {
			t.Errorf("%s: %+v, want %s on the 10th", m, p, want)
		}
	}
	// On or after the pending date it is accepted.
	if rec := f.do("PATCH", "/api/v1/commitments/"+id, `{"schedule":{"day":11},"effective_from":"2026-09-01","version":2}`, admin); rec.Code != 200 {
		t.Fatalf("at the pending date: %d %s", rec.Code, rec.Body.String())
	}
}

func TestStartDateLowerBoundAPI(t *testing.T) {
	f := newAPIFixture(t)
	rec := f.do("POST", "/api/v1/commitments", strings.Replace(monthEndBody, "2026-01-01", "1899-12-31", 1), map[string]string{"X-Test-User": f.ids["alice"]})
	if rec.Code != 400 || errFields(t, rec.Body.Bytes()) != "start_date" {
		t.Fatalf("1899: %d %s", rec.Code, rec.Body.String())
	}
}
