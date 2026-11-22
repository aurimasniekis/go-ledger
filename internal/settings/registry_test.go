package settings

import (
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/config"
	reg "github.com/aurimasniekis/go-ledger/internal/domain/settings"
)

// Every configuration key is classified: a member of exactly one group, or a
// deployment setting (ADR-0066 point 2). A new key fails here until it is
// placed.
func TestEveryConfigurationKeyIsClassified(t *testing.T) {
	owner := map[string]string{}
	for _, g := range reg.Groups() {
		for _, m := range g.Members {
			if prev, ok := owner[m.Key]; ok {
				t.Errorf("%s is a member of both %s and %s", m.Key, prev, g.Name)
			}
			owner[m.Key] = g.Name
		}
	}
	for _, f := range config.Fields() {
		_, member := owner[f.Path]
		switch {
		case member && reg.DeploymentKey(f.Path):
			t.Errorf("%s is both a runtime setting and a deployment setting", f.Path)
		case !member && !reg.DeploymentKey(f.Path):
			t.Errorf("%s is neither a member of a settings group nor a deployment setting (ADR-0066 point 2)", f.Path)
		}
		delete(owner, f.Path)
	}
	for key, g := range owner {
		t.Errorf("group %s names %s, which is not a configuration key", g, key)
	}
}

// The registry's secrets are exactly the configuration's secrets.
func TestRegistrySecretsMatchTheConfiguration(t *testing.T) {
	for _, g := range reg.Groups() {
		for _, m := range g.Members {
			if f, ok := config.FieldByPath(m.Key); ok && f.Secret != m.Secret {
				t.Errorf("%s: registry secret %v, configuration secret %v", m.Key, m.Secret, f.Secret)
			}
		}
	}
}
