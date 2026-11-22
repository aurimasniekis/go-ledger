package httpapi

import (
	"reflect"
	"slices"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"go.yaml.in/yaml/v3"
)

// TestCommitmentBodySchemaRefs pins which schemas the commitment create and
// update bodies use: create takes the full forms, the merge-patch update takes
// the patch forms.
func TestCommitmentBodySchemaRefs(t *testing.T) {
	type schema struct {
		Properties map[string]struct {
			Ref string `yaml:"$ref"`
		} `yaml:"properties"`
	}
	type op struct {
		RequestBody struct {
			Content map[string]struct {
				Schema schema `yaml:"schema"`
			} `yaml:"content"`
		} `yaml:"requestBody"`
	}
	var doc struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(OpenAPIYAML(), &doc); err != nil {
		t.Fatal(err)
	}
	check := func(path, method, media, amount, sched string) {
		t.Helper()
		node, ok := doc.Paths[path][method]
		if !ok {
			t.Fatalf("%s %s not documented", method, path)
		}
		var item op
		if err := node.Decode(&item); err != nil {
			t.Fatal(err)
		}
		s, ok := item.RequestBody.Content[media]
		if !ok {
			t.Fatalf("%s %s has no %s body", method, path, media)
		}
		for name, want := range map[string]string{"amount": amount, "schedule": sched} {
			if got := s.Schema.Properties[name].Ref; got != "#/components/schemas/"+want {
				t.Errorf("%s %s body %s = %q, want %s", method, path, name, got, want)
			}
		}
	}
	check("/commitments", "post", "application/json", "PositiveMoney", "Schedule")
	check("/commitments/{id}", "patch", "application/merge-patch+json", "MoneyPatch", "SchedulePatch")
}

// TestLeewayInputMatchesServer pins the leeway request schema to what the
// server accepts: the members decodeLeeway reads, the domain's kinds and
// limits, a create that must give kind, and a merge patch that may send only
// the members that change.
func TestLeewayInputMatchesServer(t *testing.T) {
	var doc map[string]any
	if err := yaml.Unmarshal(OpenAPIYAML(), &doc); err != nil {
		t.Fatal(err)
	}
	obj := func(v any, what string) map[string]any {
		t.Helper()
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("%s is not an object", what)
		}
		return m
	}
	schemas := obj(obj(doc["components"], "components")["schemas"], "schemas")
	input := obj(schemas["LeewayInput"], "LeewayInput")
	if input["additionalProperties"] != false {
		t.Error("LeewayInput must refuse unknown members")
	}
	if _, ok := input["required"]; ok {
		t.Error("LeewayInput must not require members: a patch sends only what changes")
	}
	props := obj(input["properties"], "LeewayInput.properties")
	var members []string
	for name := range props {
		members = append(members, name)
	}
	want := slices.Clone(leewayMembers)
	slices.Sort(members)
	slices.Sort(want)
	if !slices.Equal(members, want) {
		t.Fatalf("LeewayInput members %v, server reads %v", members, want)
	}
	var kinds []string
	for _, k := range obj(props["kind"], "kind")["enum"].([]any) {
		kinds = append(kinds, k.(string))
	}
	var wantKinds []string
	for _, k := range commitments.LeewayKinds() {
		wantKinds = append(wantKinds, string(k))
	}
	if !slices.Equal(kinds, wantKinds) {
		t.Errorf("kind enum %v, want %v", kinds, wantKinds)
	}
	if got := obj(props["days"], "days")["maximum"]; got != commitments.MaxLeewayDays {
		t.Errorf("days maximum = %v, want %d", got, commitments.MaxLeewayDays)
	}
	for member, limit := range map[string]int{"conditions": commitments.MaxLeewayText, "consequences": commitments.MaxLeewayText, "source": commitments.MaxLeewaySrc} {
		if got := obj(props[member], member)["maxLength"]; got != limit {
			t.Errorf("%s maxLength = %v, want %d", member, got, limit)
		}
	}

	paths := obj(doc["paths"], "paths")
	// leeway is the request schema of one media type of one operation.
	leeway := func(path, method, media string) map[string]any {
		t.Helper()
		op := obj(obj(paths[path], path)[method], method)
		content := obj(obj(op["requestBody"], "requestBody")["content"], "content")
		schema := obj(obj(content[media], media)["schema"], "schema")
		return obj(obj(schema["properties"], "properties")["leeway"], "leeway")
	}
	const ref = "#/components/schemas/LeewayInput"
	// A create takes a full leeway with kind (the server refuses none).
	first := obj(leeway("/commitments", "post", "application/json")["oneOf"].([]any)[0], "create leeway")
	allOf, ok := first["allOf"].([]any)
	if !ok || len(allOf) != 2 || obj(allOf[0], "create allOf[0]")["$ref"] != ref || !reflect.DeepEqual(obj(allOf[1], "create allOf[1]")["required"], []any{"kind"}) {
		t.Errorf("create leeway = %v, want LeewayInput requiring kind", first)
	}
	// A merge patch takes the partial form.
	patch := obj(leeway("/commitments/{id}", "patch", "application/merge-patch+json")["oneOf"].([]any)[0], "patch leeway")
	if patch["$ref"] != ref {
		t.Errorf("patch leeway = %v, want %s", patch, ref)
	}
}
