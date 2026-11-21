package config

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

var (
	docsEnvName     = regexp.MustCompile(`LEDGER_[A-Z0-9_]+`)
	docsUUID        = regexp.MustCompile(`[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}`)
	docsPlaceholder = regexp.MustCompile(`^00000000-0000-4000-8000-0000000000[0-9]{2}$`)
)

// TestEnableBankingDocsMatchConfig keeps docs/enable-banking.md honest: every
// LEDGER_ variable it names must be exactly a variable of the configuration
// definition, the page must hold no PEM key material, and every UUID on it
// must be an all-zero placeholder rather than a real application id.
func TestEnableBankingDocsMatchConfig(t *testing.T) {
	raw, err := os.ReadFile("../../docs/enable-banking.md")
	if err != nil {
		t.Fatal(err)
	}
	page := string(raw)

	known := map[string]bool{}
	for _, v := range Variables() {
		known[v.Name] = true
	}

	names := docsEnvName.FindAllString(page, -1)
	if len(names) == 0 {
		t.Fatal("docs/enable-banking.md names no LEDGER_ variables; the check would pass vacuously")
	}
	for _, n := range names {
		if !known[n] {
			t.Errorf("docs/enable-banking.md names %s, which is not a variable in the configuration definition", n)
		}
	}

	if strings.Contains(page, "-----BEGIN") {
		t.Error("docs/enable-banking.md must not contain PEM key material")
	}

	for _, id := range docsUUID.FindAllString(page, -1) {
		if !docsPlaceholder.MatchString(id) {
			t.Errorf("docs/enable-banking.md contains a UUID that is not a placeholder (all zeros with a 0-9 tail); application ids must not be written in the docs")
		}
	}

	for _, mode := range []string{"banking.enable_banking.app", "banking.enable_banking.developer"} {
		if !strings.Contains(page, mode) {
			t.Errorf("docs/enable-banking.md does not describe %s", mode)
		}
	}
}
