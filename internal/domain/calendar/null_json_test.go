package calendar

import (
	"encoding/json"
	"testing"
)

func TestYearMonthJSONRejectsNonStrings(t *testing.T) {
	for _, in := range []string{`{"M":null}`, `{"M":202601}`, `{"M":true}`, `{"M":""}`} {
		var v struct{ M YearMonth }
		if err := json.Unmarshal([]byte(in), &v); err == nil {
			t.Errorf("%s decoded to %v, want error", in, v.M)
		}
	}
	var v struct{ M YearMonth }
	if err := json.Unmarshal([]byte(`{"M":"2026-01"}`), &v); err != nil || v.M.String() != "2026-01" {
		t.Errorf("2026-01: %v %v", v.M, err)
	}
}
