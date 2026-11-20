package money

import (
	"encoding/json"
	"testing"
)

func TestMoneyJSONRejectsNull(t *testing.T) {
	for _, in := range []string{
		`{"amount":null,"currency":"EUR"}`,
		`{"amount":"1.00","currency":null}`,
		`{"amount":null,"currency":null}`,
	} {
		var m Money
		if err := json.Unmarshal([]byte(in), &m); err == nil {
			t.Errorf("%s decoded to %v without error", in, m)
		}
	}
}

func TestDecimalJSONRejectsNullAndNumbers(t *testing.T) {
	type dto struct {
		Amount Decimal `json:"amount"`
	}
	for _, in := range []string{`{"amount":null}`, `{"amount":1.5}`, `{"amount":""}`, `{"amount":true}`} {
		var v dto
		if err := json.Unmarshal([]byte(in), &v); err == nil {
			t.Errorf("%s decoded to %s without error", in, v.Amount)
		}
	}
	var v dto
	if err := json.Unmarshal([]byte(`{"amount":"1.50"}`), &v); err != nil || v.Amount.String() != "1.50" {
		t.Errorf("valid decimal: %s, %v", v.Amount, err)
	}
}

func TestRoundHalfEvenRejectsNegativeScale(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("negative scale must panic, not be treated as 0")
		}
	}()
	MustParseDecimal("1.5").RoundHalfEven(-1)
}
