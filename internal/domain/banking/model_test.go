package banking

import "testing"

func TestModeAccountAccess(t *testing.T) {
	if ModeApp.AccountAccess() != AccountAccessAll || ModeDeveloper.AccountAccess() != AccountAccessLinkedOnly {
		t.Error("mode to account access mapping")
	}
	if Mode("sandbox").Valid() || !ModeApp.Valid() || !ModeDeveloper.Valid() {
		t.Error("Mode.Valid")
	}
	c := Capabilities{Mode: ModeDeveloper, BalanceTypes: []BalanceType{BalanceBooked}}
	if c.AccountAccess() != AccountAccessLinkedOnly || !c.SupportsBalance(BalanceBooked) || c.SupportsBalance(BalanceExpected) {
		t.Error("Capabilities helpers")
	}
}

func TestBalanceValidateRejectsUnknownAmount(t *testing.T) {
	if err := (Balance{Type: BalanceBooked, ProviderType: "CLBD"}).Validate(); err == nil {
		t.Error("a balance without an amount validated; unknown must be absent, never zero")
	}
}
