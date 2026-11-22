package merchants

import "testing"

// Alias keys (task M1, ADR-0058): Unicode NFC, white space collapsed, lower
// case; invalid display text has no key.
func TestAliasKey(t *testing.T) {
	for in, want := range map[string]string{
		"  Acme   Shop ":         "acme shop",
		"Cafe\u0301 Example":     "caf\u00e9 example",
		"CAF\u00c9 EXAMPLE":      "caf\u00e9 example",
		"Lidl\tVilnius\n":        "lidl vilnius",
		"\u0160IAULI\u0173 Shop": "\u0161iauli\u0173 shop",
	} {
		if got, ok := AliasKey(in); !ok || got != want {
			t.Errorf("AliasKey(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "   ", "a\u202eb", string([]byte{0xff})} {
		if k, ok := AliasKey(bad); ok {
			t.Errorf("AliasKey(%q) = %q, want no key", bad, k)
		}
	}
	// A line separator is white space to the key, but no display name.
	if errs := CheckName("name", "line\u2028break"); len(errs) == 0 {
		t.Error("a name with a line separator was accepted")
	}
	if NormalizeDisplayName("  Cafe\u0301   Shop ") != "Caf\u00e9 Shop" {
		t.Error("display names are not trimmed, collapsed and composed")
	}
}

// A correction gives a name or a merchant id, not both, and only a correction
// takes a merchant id.
func TestRequestMerchantID(t *testing.T) {
	cases := []struct {
		r  Request
		ok bool
	}{
		{Request{Decision: DecisionCorrected, MerchantID: "01JZ0000000000000000000001"}, true},
		{Request{Decision: DecisionCorrected, MerchantID: "01JZ0000000000000000000001", Name: "x"}, false},
		{Request{Decision: DecisionConfirmed, MerchantID: "01JZ0000000000000000000001"}, false},
		{Request{Decision: DecisionRejected, MerchantID: "01JZ0000000000000000000001"}, false},
	}
	for _, c := range cases {
		if errs := c.r.Validate(); (len(errs) == 0) != c.ok {
			t.Errorf("%+v: %v, want ok = %v", c.r, errs, c.ok)
		}
	}
}

// The resolved merchant shows the record's display name, and a confirmation
// stands for another spelling of the same record.
func TestResolveWithMerchantRecords(t *testing.T) {
	e := &Enrichment{Status: StatusIdentified, Name: "LIDL*1523", Confidence: 950, Source: "curated", MerchantID: "M1", MerchantName: "Lidl"}
	if m := Resolve(e, nil, DefaultThreshold); m.Name != "Lidl" || m.MerchantID != "M1" || m.State != StateReliable {
		t.Errorf("reliable: %+v", m)
	}
	r := &Review{Decision: DecisionConfirmed, Name: "Lidl", MerchantID: "M1", MerchantName: "Lidl"}
	if m := Resolve(e, r, DefaultThreshold); m.State != StateConfirmed || m.ConfirmationOutdated || m.Name != "Lidl" {
		t.Errorf("confirmation of another spelling: %+v", m)
	}
	other := &Enrichment{Status: StatusIdentified, Name: "Rimi", Confidence: 950, Source: "curated", MerchantID: "M2", MerchantName: "Rimi"}
	if m := Resolve(other, r, DefaultThreshold); !m.ConfirmationOutdated || m.State != StateReliable {
		t.Errorf("confirmation of another merchant: %+v", m)
	}
	c := &Review{Decision: DecisionCorrected, Name: "lidl", MerchantID: "M1", MerchantName: "Lidl Renamed"}
	if m := Resolve(nil, c, DefaultThreshold); m.Name != "Lidl Renamed" || m.MerchantID != "M1" {
		t.Errorf("correction: %+v", m)
	}
}
