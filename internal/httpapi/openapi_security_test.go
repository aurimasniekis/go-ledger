package httpapi

import (
	"slices"
	"testing"

	"go.yaml.in/yaml/v3"
)

// TestOpenAPISecurityMatchesAccess: public operations declare an empty
// security (no credentials), every other operation inherits the document's
// root requirement (a bearer API token or the session cookie), and every
// security reference names a declared scheme. Generated clients read these
// requirements, so they must agree with x-ledger-access.
func TestOpenAPISecurityMatchesAccess(t *testing.T) {
	type requirement = map[string][]string
	var doc struct {
		Security   []requirement `yaml:"security"`
		Components struct {
			SecuritySchemes map[string]yaml.Node `yaml:"securitySchemes"`
		} `yaml:"components"`
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(OpenAPIYAML(), &doc); err != nil {
		t.Fatal(err)
	}
	var root []string
	for _, r := range doc.Security {
		for name := range r {
			root = append(root, name)
		}
	}
	slices.Sort(root)
	if !slices.Equal(root, []string{"bearerAuth", "sessionCookie"}) {
		t.Fatalf("root security = %v, want bearerAuth and sessionCookie", root)
	}
	for _, name := range root {
		if _, ok := doc.Components.SecuritySchemes[name]; !ok {
			t.Errorf("root security names %q, which is not a declared securityScheme", name)
		}
	}

	type operation struct {
		Access   string         `yaml:"x-ledger-access"`
		Security *[]requirement `yaml:"security"`
	}
	methods := []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}
	public, others := 0, 0
	for path, item := range doc.Paths {
		for _, method := range methods {
			node, ok := item[method]
			if !ok {
				continue
			}
			var op operation
			if err := node.Decode(&op); err != nil {
				t.Fatalf("%s %s: %v", method, path, err)
			}
			if op.Access == "" {
				t.Errorf("%s %s: no x-ledger-access", method, path)
			}
			if op.Access == "public" {
				public++
				if op.Security == nil || len(*op.Security) != 0 {
					t.Errorf("%s %s is public but its security is not empty", method, path)
				}
				continue
			}
			others++
			if op.Security != nil {
				t.Errorf("%s %s (x-ledger-access %s) overrides the root security; only public operations may", method, path, op.Access)
			}
		}
	}
	if public == 0 || others == 0 {
		t.Fatalf("checked %d public and %d other operations; both kinds must exist", public, others)
	}
}
