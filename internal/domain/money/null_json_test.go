package money

import (
	"encoding/json"
	"testing"
)

func TestCurrencyJSONRejectsNonStrings(t *testing.T) {
	for _, in := range []string{`{"c":null}`, `{"c":1}`, `{"c":true}`, `{"c":""}`} {
		var v struct{ C Currency }
		if err := json.Unmarshal([]byte(in), &v); err == nil {
			t.Errorf("%s decoded to %v, want error", in, v.C)
		}
	}
	var v struct{ C Currency }
	if err := json.Unmarshal([]byte(`{"C":"EUR"}`), &v); err != nil || v.C.String() != "EUR" {
		t.Errorf("EUR: %v %v", v.C, err)
	}
}
