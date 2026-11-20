package banking

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/domain/money"
)

const secretIBAN = "XX00FAKE0000000000000001"

func TestSensitiveNeverFormats(t *testing.T) {
	s := NewSensitive(secretIBAN)
	acc := Account{ExternalID: "ext", Currency: money.MustParseCurrency("EUR"), Handle: NewSensitive("handle-1"), Identifiers: AccountIdentifiers{IBAN: s}}
	var out []string
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X"} {
		out = append(out, fmt.Sprintf(verb, s), fmt.Sprintf(verb, acc))
	}
	out = append(out, s.String(), fmt.Sprint(s), fmt.Sprintln(acc))
	b, err := json.Marshal(acc)
	if err != nil {
		t.Fatal(err)
	}
	out = append(out, string(b))
	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("account", "iban", s, "account", acc)
	out = append(out, logs.String())
	for _, o := range out {
		if strings.Contains(o, secretIBAN) || strings.Contains(o, "handle-1") || strings.Contains(o, "5858") {
			t.Errorf("sensitive value leaked: %s", o)
		}
	}
	if s.Reveal() != secretIBAN {
		t.Error("Reveal lost the value")
	}
	if NewSensitive("").String() != "" || !NewSensitive("").IsZero() {
		t.Error("empty Sensitive should print empty")
	}
	if !s.Equal(NewSensitive(secretIBAN)) || s.Equal(NewSensitive("other")) {
		t.Error("Equal")
	}
}
