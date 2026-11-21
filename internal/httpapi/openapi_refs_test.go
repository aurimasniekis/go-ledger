package httpapi

import (
	"testing"

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
