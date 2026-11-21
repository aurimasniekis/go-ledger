package config

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestEnableBankingDocsMatchConfig keeps docs/enable-banking.md honest: every
// LEDGER_ variable it names must exist in the configuration definition, and
// the page must hold no PEM key material or real-looking application ids.
func TestEnableBankingDocsMatchConfig(t *testing.T) {
	raw, err := os.ReadFile("../../docs/enable-banking.md")
	if err != nil {
		t.Fatal(err)
	}
	page := string(raw)
	example := ExampleEnv()
	names := regexp.MustCompile(`LEDGER_[A-Z0-9_]+`).FindAllString(page, -1)
	if len(names) == 0 {
		t.Fatal("docs/enable-banking.md names no LEDGER_ variables; the check would pass vacuously")
	}
	for _, n := range names {
		if !strings.Contains(example, n) {
			t.Errorf("docs/enable-banking.md names %s, which is not in the configuration definition", n)
		}
	}
	if strings.Contains(page, "-----BEGIN") {
		t.Error("docs/enable-banking.md must not contain PEM key material")
	}
	for _, mode := range []string{"banking.enable_banking.app", "banking.enable_banking.developer"} {
		if !strings.Contains(page, mode) {
			t.Errorf("docs/enable-banking.md does not describe %s", mode)
		}
	}
}
