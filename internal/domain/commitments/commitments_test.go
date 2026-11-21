package commitments

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
)

func fieldsOf(errs []FieldError) string {
	var out []string
	for _, e := range errs {
		out = append(out, e.Field)
	}
	return strings.Join(out, ",")
}

func TestScheduleRoundTrip(t *testing.T) {
	for _, c := range []struct {
		s    Schedule
		json string
	}{
		{Monthly(1, 15), `{"version":1,"frequency":"monthly","interval":1,"day":15}`},
		{Monthly(3, 31), `{"version":1,"frequency":"monthly","interval":3,"day":31}`},
		{Annual(time.February, 29), `{"version":1,"frequency":"annual","month":2,"day":29}`},
	} {
		b, err := json.Marshal(c.s)
		if err != nil || string(b) != c.json {
			t.Errorf("marshal %+v = %s %v, want %s", c.s, b, err, c.json)
		}
		var back Schedule
		if err := json.Unmarshal(b, &back); err != nil || back != c.s {
			t.Errorf("unmarshal %s = %+v %v", b, back, err)
		}
	}
}

func TestParseScheduleReportsEachField(t *testing.T) {
	for _, c := range []struct {
		raw, fields string
	}{
		{`[]`, "schedule"},
		{`"monthly"`, "schedule"},
		{`{}`, "schedule.version"},
		{`{"frequency":"monthly","interval":1,"day":1}`, "schedule.version"},
		{`{"version":"1","frequency":"monthly","interval":1,"day":1}`, "schedule.version"},
		{`{"version":2,"frequency":"weekly","weekday":1}`, "schedule.version"},
		{`{"version":1}`, "schedule.frequency"},
		{`{"version":1,"frequency":"weekly"}`, "schedule.frequency"},
		{`{"version":1,"frequency":"monthly"}`, "schedule.interval,schedule.day"},
		{`{"version":1,"frequency":"monthly","interval":0,"day":32}`, "schedule.interval,schedule.day"},
		{`{"version":1,"frequency":"monthly","interval":121,"day":1}`, "schedule.interval"},
		{`{"version":1,"frequency":"monthly","interval":1.5,"day":1}`, "schedule.interval"},
		{`{"version":1,"frequency":"monthly","interval":1,"day":1,"month":3}`, "schedule.month"},
		{`{"version":1,"frequency":"annual","month":13,"day":1}`, "schedule.month"},
		{`{"version":1,"frequency":"annual","month":4,"day":31}`, "schedule.day"},
		{`{"version":1,"frequency":"annual","month":2,"day":30}`, "schedule.day"},
		{`{"version":1,"frequency":"annual","month":2,"day":29,"interval":1}`, "schedule.interval"},
		{`{"version":1,"frequency":"annual","day":null,"month":1}`, "schedule.day"},
		{`{"version":1,"frequency":"annual","zz":1,"aa":2,"month":1,"day":1}`, "schedule.aa,schedule.zz"},
	} {
		_, errs := ParseSchedule([]byte(c.raw), "schedule")
		if got := fieldsOf(errs); got != c.fields {
			t.Errorf("%s: fields %q, want %q (%v)", c.raw, got, c.fields, errs)
		}
	}
	// Day 31 monthly and Feb 29 annual are valid: D6 clamps them per month and year.
	for _, raw := range []string{`{"version":1,"frequency":"monthly","interval":1,"day":31}`, `{"version":1,"frequency":"annual","month":2,"day":29}`} {
		if _, errs := ParseSchedule([]byte(raw), "schedule"); len(errs) != 0 {
			t.Errorf("%s: %v", raw, errs)
		}
	}
	var s Schedule
	if err := json.Unmarshal([]byte(`{"version":1}`), &s); err == nil {
		t.Error("UnmarshalJSON accepted an incomplete schedule")
	}
}

func valid() Fields {
	return Fields{
		Name: "VPS", Kind: KindServer, Amount: money.MustParse("12.50", "EUR"), Schedule: Monthly(1, 5),
		StartDate: calendar.MustDate(2026, time.January, 5),
	}
}

func ptr[T any](v T) *T { return &v }

func TestValidateFields(t *testing.T) {
	if errs := valid().Validate(); len(errs) != 0 {
		t.Fatalf("valid: %v", errs)
	}
	f := valid()
	f.EndDate, f.Priority, f.Notes = ptr(calendar.MustDate(2026, time.January, 5)), ptr(PriorityEssential), ptr("note")
	if errs := f.Validate(); len(errs) != 0 {
		t.Fatalf("valid with optionals: %v", errs)
	}
	bad := Fields{
		Name: "  ", Kind: "car", Amount: money.MustParse("0.00", "EUR"), Schedule: Schedule{Version: 1, Frequency: FrequencyMonthly},
		EndDate: ptr(calendar.MustDate(2025, time.January, 1)), Priority: ptr(Priority("urgent")), Notes: ptr(""),
	}
	if got := fieldsOf(bad.Validate()); got != "name,kind,amount.amount,schedule.interval,schedule.day,start_date,priority,notes" {
		t.Errorf("fields %q", got)
	}
	bad = valid()
	bad.EndDate = ptr(calendar.MustDate(2026, time.January, 4))
	bad.Amount = money.MustParse("-1", "EUR")
	bad.Name = strings.Repeat("é", MaxName+1)
	if got := fieldsOf(bad.Validate()); got != "name,amount.amount,end_date" {
		t.Errorf("fields %q", got)
	}
	bad = valid()
	bad.Amount = money.Money{}
	if got := fieldsOf(bad.Validate()); got != "amount.currency" {
		t.Errorf("fields %q", got)
	}
	// start_date is bounded below so a first read cannot materialise millennia.
	bad = valid()
	bad.StartDate = calendar.MustDate(1899, time.December, 31)
	if got := fieldsOf(bad.Validate()); got != "start_date" {
		t.Errorf("1899 start: %q", got)
	}
	bad.StartDate = MinStartDate
	if errs := bad.Validate(); len(errs) != 0 {
		t.Errorf("1900-01-01 start: %v", errs)
	}
	// The amount keeps its scale: 12.5 and 12.50 are both accepted as written.
	bad = valid()
	bad.Amount = money.MustParse("12.5", "JPY")
	if errs := bad.Validate(); len(errs) != 0 {
		t.Errorf("scale: %v", errs)
	}
}

func TestChangedFieldsAndEvents(t *testing.T) {
	a := valid()
	if got := ChangedFields(a, a); len(got) != 0 {
		t.Fatalf("same: %v", got)
	}
	b := a
	b.Amount = money.MustParse("12.5", "EUR") // equal value, different scale: a change
	b.Schedule = Monthly(1, 6)
	b.Notes = ptr("x")
	if got := strings.Join(ChangedFields(a, b), ","); got != "amount,notes,schedule" {
		t.Fatalf("changed %q", got)
	}
	actor := audit.Actor{Kind: audit.ActorUser, UserID: "01JD3Y4C8V9M2Q7W5R6T0XZBNA"}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := Commitment{ID: "01JD3Y4C8V9M2Q7W5R6T0XZBNB", Fields: b}
	for _, e := range []audit.Event{CreatedEvent(actor, c, at), UpdatedEvent(actor, c.ID, ChangedFields(a, b), at), DeletedEvent(actor, c, 0, at)} {
		if e.TargetType != "commitment" || e.TargetID != c.ID || e.Actor != actor || !e.At.Equal(at) {
			t.Errorf("event %+v", e)
		}
		for k, v := range e.Details {
			if strings.Contains(v, "VPS") || strings.Contains(v, "12.5") || k == "name" || k == "notes" {
				t.Errorf("event %s carries user content: %v", e.Action, e.Details)
			}
		}
	}
	if e := CreatedEvent(actor, c, at); e.Action != audit.CommitmentCreated || e.Details["kind"] != "server" || e.Details["currency"] != "EUR" {
		t.Errorf("created %+v", e)
	}
	if e := UpdatedEvent(actor, c.ID, []string{"amount", "notes"}, at); e.Details["fields"] != "amount,notes" {
		t.Errorf("updated %+v", e)
	}
}

func TestValidationError(t *testing.T) {
	if Validation(nil) != nil {
		t.Fatal("no errors must be nil")
	}
	err := Validation([]FieldError{{"name", "bad"}, {"kind", "worse"}})
	if err == nil || err.Error() != "invalid commitment: name: bad; kind: worse" {
		t.Fatalf("%v", err)
	}
}
