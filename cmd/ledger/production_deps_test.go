package main

import (
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// forbiddenProductionDeps are packages the production binary must never link:
// the testing package (and its subpackages) and the store test helpers. The
// seed hook in internal/store already refuses to run outside a test binary,
// but only a build-time check proves the binary itself stays clean.
var forbiddenProductionDeps = []string{
	"testing",
	"github.com/aurimasniekis/go-ledger/internal/store/storetest",
}

// TestProductionBinaryDoesNotLinkTestHelpers fails when go list reports a
// forbidden package in the dependency closure of cmd/ledger's non-test build.
// Test-only imports (testmain_test.go) do not appear in that closure, so they
// are fine.
func TestProductionBinaryDoesNotLinkTestHelpers(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	deps := strings.Fields(string(out))
	if err := checkForbiddenDeps(deps); err != nil {
		t.Fatal(err)
	}
}

// checkForbiddenDeps returns an error naming every dependency that is a
// forbidden package or lies under the testing tree.
func checkForbiddenDeps(deps []string) error {
	var found []string
	for _, dep := range deps {
		if dep == "testing" || strings.HasPrefix(dep, "testing/") || slices.Contains(forbiddenProductionDeps, dep) {
			found = append(found, dep)
		}
	}
	if len(found) > 0 {
		return fmt.Errorf("production binary links test-only packages: %s", strings.Join(found, ", "))
	}
	return nil
}
