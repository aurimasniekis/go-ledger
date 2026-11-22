package httpapi

import (
	"slices"
	"testing"

	"go.yaml.in/yaml/v3"

	reg "github.com/aurimasniekis/go-ledger/internal/domain/settings"
)

// The per-group schemas of openapi.yaml name exactly the members of the
// settings registry (plus the read-only members and the secret companions), so
// the document cannot drift from what the routes serve.
func TestSettingsSchemasMatchTheRegistry(t *testing.T) {
	var doc map[string]any
	if err := yaml.Unmarshal(OpenAPIYAML(), &doc); err != nil {
		t.Fatal(err)
	}
	get := func(ref string) map[string]any {
		v, ok := resolve(doc, ref)
		m, _ := v.(map[string]any)
		if !ok || m == nil {
			t.Fatalf("no %s in openapi.yaml", ref)
		}
		return m
	}
	var singletons []string
	for _, g := range reg.Groups() {
		if g.Kind != reg.Singleton {
			continue
		}
		singletons = append(singletons, g.Name)
		schema := get("#/components/schemas/Settings" + camel(g.Name))
		var props map[string]any
		for _, part := range schema["allOf"].([]any) {
			if p, ok := part.(map[string]any)["properties"].(map[string]any); ok {
				props = p
			}
		}
		var want []string
		for _, m := range g.Members {
			want = append(want, m.Name)
			if m.Secret {
				c := secretCompanions(m.Name)
				want = append(want, c[:]...)
			}
		}
		var got []string
		for k := range props {
			got = append(got, k)
		}
		slices.Sort(want)
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("group %s: schema members %v, registry %v", g.Name, got, want)
		}
		for _, m := range g.Members {
			if p, _ := props[m.Name].(map[string]any); m.Secret && p["writeOnly"] != true {
				t.Errorf("group %s: secret %s is not writeOnly", g.Name, m.Name)
			}
		}
	}
	names := get("#/components/schemas/SettingsGroupName")["enum"].([]any)
	var enum []string
	for _, n := range names {
		enum = append(enum, n.(string))
	}
	slices.Sort(enum)
	slices.Sort(singletons)
	if !slices.Equal(enum, singletons) {
		t.Errorf("SettingsGroupName enum %v, singleton groups %v", enum, singletons)
	}
	meta := get("#/components/schemas/SettingsGroupMeta")["properties"].(map[string]any)
	for _, m := range readOnlySettingMembers {
		if meta[m] == nil {
			t.Errorf("read-only member %s is not in SettingsGroupMeta", m)
		}
	}
}

func camel(s string) string {
	out, up := []byte{}, true
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '_':
			up = true
		case up && s[i] >= 'a' && s[i] <= 'z':
			out, up = append(out, s[i]-32), false
		default:
			out, up = append(out, s[i]), false
		}
	}
	return string(out)
}
