package merchants

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func identified(name string, conf Confidence) *Enrichment {
	return &Enrichment{TransactionID: "T", Status: StatusIdentified, Name: name, LogoRef: "logo:" + name, Source: "curated", Confidence: conf, RetrievedAt: time.Unix(1, 0)}
}

func TestResolveEnrichmentOnly(t *testing.T) {
	for _, c := range []struct {
		name       string
		e          *Enrichment
		state      State
		shown      string
		logo       string
		needReview bool
	}{
		{"reliable at the threshold", identified("Acme", DefaultThreshold), StateReliable, "Acme", "logo:Acme", false},
		{"reliable above", identified("Acme", 1000), StateReliable, "Acme", "logo:Acme", false},
		{"uncertain below the threshold is not shown", identified("Acme", DefaultThreshold-1), StateUncertain, "", "", true},
		{"ambiguous is not shown", &Enrichment{Status: StatusAmbiguous, Candidates: []string{"A", "B"}, Source: "curated"}, StateAmbiguous, "", "", true},
		{"none", &Enrichment{Status: StatusNone, Source: "curated"}, StateNone, "", "", false},
		{"unavailable", &Enrichment{Status: StatusUnavailable, Source: "curated"}, StateUnavailable, "", "", false},
		{"never looked up", nil, StateNotLookedUp, "", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := Resolve(c.e, nil, DefaultThreshold)
			if m.State != c.state || m.Name != c.shown || m.LogoRef != c.logo || m.NeedsReview != c.needReview {
				t.Fatalf("got %+v, want state %s name %q logo %q review %v", m, c.state, c.shown, c.logo, c.needReview)
			}
			if m.Fallback() != (c.shown == "") {
				t.Fatalf("Fallback() = %v for name %q", m.Fallback(), m.Name)
			}
		})
	}
}

func TestResolveThresholdIsAParameter(t *testing.T) {
	e := identified("Acme", 500)
	if m := Resolve(e, nil, 500); m.State != StateReliable {
		t.Fatalf("at a lower threshold: %s", m.State)
	}
	if m := Resolve(e, nil, 501); m.State != StateUncertain {
		t.Fatalf("at a higher threshold: %s", m.State)
	}
}

func TestResolveDecisionsWinOverEnrichment(t *testing.T) {
	low := identified("Acme", 100)
	if m := Resolve(low, &Review{Decision: DecisionConfirmed, Name: "Acme"}, DefaultThreshold); m.State != StateConfirmed || m.Name != "Acme" || m.LogoRef != "logo:Acme" || m.IdentitySource != "user" {
		t.Fatalf("a confirmation of a low-confidence name: %+v", m)
	}
	amb := &Enrichment{Status: StatusAmbiguous, Candidates: []string{"A", "B"}, Source: "curated"}
	if m := Resolve(amb, &Review{Decision: DecisionCorrected, Name: "Mine"}, DefaultThreshold); m.State != StateCorrected || m.Name != "Mine" || m.LogoRef != "" || m.NeedsReview {
		t.Fatalf("a correction of an ambiguous answer: %+v", m)
	}
	if m := Resolve(identified("Acme", 1000), &Review{Decision: DecisionRejected}, DefaultThreshold); m.State != StateRejected || m.Name != "" || !m.Fallback() {
		t.Fatalf("a rejection hides even a certain identity: %+v", m)
	}
	// A correction and a rejection stand even when no enrichment exists or it changed.
	if m := Resolve(nil, &Review{TransactionID: "T", Decision: DecisionCorrected, Name: "Mine"}, DefaultThreshold); m.State != StateCorrected || m.TransactionID != "T" {
		t.Fatalf("a correction without enrichment: %+v", m)
	}
}

func TestResolveOutdatedConfirmation(t *testing.T) {
	r := &Review{Decision: DecisionConfirmed, Name: "Acme"}
	m := Resolve(identified("Acme Ltd", 1000), r, DefaultThreshold)
	if !m.ConfirmationOutdated || !m.NeedsReview || m.State != StateReliable {
		t.Fatalf("the source now says another name: %+v", m)
	}
	m = Resolve(identified("Acme Ltd", 100), r, DefaultThreshold)
	if !m.ConfirmationOutdated || m.State != StateUncertain || m.Name != "" {
		t.Fatalf("an outdated confirmation grants nothing: %+v", m)
	}
	if m := Resolve(nil, r, DefaultThreshold); !m.ConfirmationOutdated || m.State != StateNotLookedUp {
		t.Fatalf("a confirmation without enrichment: %+v", m)
	}
}

func TestAnswerValidation(t *testing.T) {
	ok := []Answer{
		{Status: StatusIdentified, Name: "Acme", Source: "curated", Confidence: 900},
		{Status: StatusIdentified, Name: "Acme", LogoRef: "logo:1", Source: "x-1.y_z"},
		{Status: StatusAmbiguous, Candidates: []string{"A", "B"}, Source: "curated"},
		{Status: StatusNone, Source: "curated"},
		{Status: StatusUnavailable, Source: "curated"},
	}
	for _, a := range ok {
		if errs := a.Validate(); len(errs) != 0 {
			t.Errorf("%+v rejected: %v", a, errs)
		}
	}
	bad := []Answer{
		{Status: StatusIdentified, Source: "curated"},
		{Status: StatusIdentified, Name: "Acme", Source: "Curated"},
		{Status: StatusIdentified, Name: "Acme", Source: ""},
		{Status: StatusIdentified, Name: "Acme", Source: "c", Confidence: 1001},
		{Status: StatusIdentified, Name: "Acme", Source: "c", Confidence: -1},
		{Status: StatusIdentified, Name: "Acme\n", Source: "c"},
		{Status: StatusIdentified, Name: " Acme", Source: "c"},
		// U+2028 and U+2029 separate lines and paragraphs: never in a name.
		{Status: StatusIdentified, Name: "Acme\u2028Ltd", Source: "c"},
		{Status: StatusIdentified, Name: "Acme\u2029Ltd", Source: "c"},
		{Status: StatusIdentified, Name: "Acme", LogoRef: "logo\u2028", Source: "c"},
		{Status: StatusAmbiguous, Candidates: []string{"A\u2029", "B"}, Source: "c"},
		{Status: StatusIdentified, Name: "Acme", Source: "c", Candidates: []string{"A"}},
		{Status: StatusAmbiguous, Candidates: []string{"A"}, Source: "c"},
		{Status: StatusAmbiguous, Name: "A", Candidates: []string{"A", "B"}, Source: "c"},
		{Status: StatusAmbiguous, Candidates: []string{"A", "B", "C", "D", "E", "F"}, Source: "c"},
		{Status: StatusAmbiguous, Candidates: []string{"A", "A"}, Source: "c"},
		{Status: StatusAmbiguous, Candidates: []string{"A", "B", "B"}, Source: "c"},
		{Status: StatusIdentified, Name: "Acme\u202e", Source: "c"},
		{Status: StatusAmbiguous, Candidates: []string{"A", "B\u200b"}, Source: "c"},
		{Status: StatusNone, Name: "A", Source: "c"},
		{Status: StatusNone, Source: "c", Confidence: 5},
		{Status: StatusUnavailable, Source: "c", LogoRef: "x"},
		{Status: "maybe", Source: "c"},
		{Status: StatusIdentified, Name: strings.Repeat("a", MaxName+1), Source: "c"},
	}
	for _, a := range bad {
		if errs := a.Validate(); len(errs) == 0 {
			t.Errorf("%+v accepted", a)
		}
	}
}

func TestRequestRejectsFormatCharacters(t *testing.T) {
	for _, name := range []string{"Acme\u202eevil", "Ac\u200bme", "Acme\u2066"} {
		if errs := (Request{Decision: DecisionCorrected, Name: name}).Validate(); len(errs) == 0 {
			t.Errorf("name %q accepted", name)
		}
	}
}

func TestRequestValidation(t *testing.T) {
	for _, r := range []Request{{Decision: DecisionConfirmed}, {Decision: DecisionRejected}, {Decision: DecisionCorrected, Name: "Mine"}} {
		if errs := r.Validate(); len(errs) != 0 {
			t.Errorf("%+v rejected: %v", r, errs)
		}
	}
	for _, r := range []Request{{Decision: DecisionCorrected}, {Decision: DecisionConfirmed, Name: "x"}, {Decision: DecisionRejected, Name: "x"}, {Decision: "x"}, {Decision: DecisionCorrected, Name: "a\tb"}} {
		if errs := r.Validate(); len(errs) == 0 {
			t.Errorf("%+v accepted", r)
		}
	}
	if got := (Request{Decision: DecisionCorrected, Name: "  Corner   Shop \n"}).Normalized().Name; got != "Corner Shop" {
		t.Errorf("normalized name %q", got)
	}
}

// TestNothingFinancialIsImported pins that merchant identity cannot affect
// amounts or payment state: neither this package nor its store imports a
// package that computes them or that holds payment facts.
func TestNothingFinancialIsImported(t *testing.T) {
	forbidden := []string{"/domain/money", "/domain/payments", "/domain/matches", "/domain/occurrences", "/domain/automatch", "/domain/monthview", "/domain/outlook", "/domain/fx", "/store/payments", "/store/matches", "/store/occurrences"}
	for _, dir := range []string{".", "../../store/merchants"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no files in %s: %v", dir, err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			af, err := parser.ParseFile(token.NewFileSet(), f, src, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range af.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				for _, bad := range forbidden {
					if strings.HasSuffix(p, bad) {
						t.Errorf("%s imports %s", f, p)
					}
				}
			}
		}
	}
}
