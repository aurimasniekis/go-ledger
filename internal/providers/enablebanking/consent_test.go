package enablebanking_test

import (
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/providers/enablebanking"
)

func TestDefaultHTTPClientTimeouts(t *testing.T) {
	c := enablebanking.NewHTTPClient()
	if c.Timeout != 30*time.Second || enablebanking.DefaultRequestTimeout != 30*time.Second {
		t.Fatalf("total timeout %v", c.Timeout)
	}
	if enablebanking.DefaultConnectTimeout != 10*time.Second {
		t.Fatalf("connect timeout %v", enablebanking.DefaultConnectTimeout)
	}
}

func TestStartConsentValidity(t *testing.T) {
	e := newTestEnv(t, banking.ModeApp)
	insts, err := e.p.Institutions(e.ctx, "FI")
	if err != nil {
		t.Fatal(err)
	}
	req := banking.ConsentRequest{Institution: insts[0], RedirectURL: "https://ledger.example/callback", State: "state-1"}

	for name, vu := range map[string]time.Time{"past": t0.Add(-time.Hour), "now": t0} {
		r := req
		r.ValidUntil = vu
		if _, err := e.p.StartConsent(e.ctx, r); err == nil {
			t.Fatalf("%s valid_until accepted", name)
		}
	}
	if e.f.authBody != nil {
		t.Fatal("a refused request reached the bank")
	}

	// A missing PSU type is sent as personal.
	req.ValidUntil = t0.Add(30 * 24 * time.Hour)
	if _, err := e.p.StartConsent(e.ctx, req); err != nil {
		t.Fatal(err)
	}
	if e.f.authBody["psu_type"] != "personal" {
		t.Fatalf("psu_type %v", e.f.authBody["psu_type"])
	}

	// An unknown bank maximum leaves the requested value as it is.
	req.Institution.MaxConsentValidity = 0
	req.ValidUntil = t0.Add(400 * 24 * time.Hour)
	if _, err := e.p.StartConsent(e.ctx, req); err != nil {
		t.Fatal(err)
	}
	got := e.f.authBody["access"].(map[string]any)["valid_until"].(string)
	if want := t0.Add(400 * 24 * time.Hour).UTC().Format(time.RFC3339); got != want {
		t.Fatalf("valid_until %s, want %s", got, want)
	}
}
