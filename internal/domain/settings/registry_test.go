package settings

import (
	"strings"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
)

func TestRegistryShape(t *testing.T) {
	seen := map[string]bool{}
	for _, g := range Groups() {
		if !NamePattern.MatchString(g.Name) || seen[g.Name] {
			t.Errorf("group name %q is invalid or repeated", g.Name)
		}
		seen[g.Name] = true
		if (g.Kind == Keyed) != strings.Contains(g.Name, ".") {
			t.Errorf("group %s: kind %s does not match its name", g.Name, g.Kind)
		}
		if g.Grant != GrantSettings && g.Grant != GrantIntegrations {
			t.Errorf("group %s: grant %q", g.Name, g.Grant)
		}
		var names []string
		for _, m := range g.Members {
			if m.Secret && !m.Sensitive {
				t.Errorf("%s: a secret is always a sensitive write (point 19)", m.Key)
			}
			if g.Grant == GrantIntegrations && !m.Sensitive {
				t.Errorf("%s: every integrations member is sensitive (point 8)", m.Key)
			}
			names = append(names, m.Name)
		}
		// The audit entry lists the member names; all of them must fit.
		if n := len(strings.Join(names, ",")); n > audit.MaxValueBytes {
			t.Errorf("group %s: member names take %d bytes, more than an audit detail holds", g.Name, n)
		}
	}
	// The sensitive members of the mixed groups are the ones point 8 names.
	for group, want := range map[string]string{"banking": "redirect_url,enable_banking.base_url", "rates": "base_url", "reporting": "", "mcp": "", "logging": ""} {
		g, _ := Lookup(group)
		var got []string
		for _, m := range g.Members {
			if m.Sensitive {
				got = append(got, m.Name)
			}
		}
		if strings.Join(got, ",") != want {
			t.Errorf("group %s: sensitive members %v, want %q", group, got, want)
		}
	}
	if g, m, ok := GroupOfKey("reporting.timezone"); !ok || g.Name != "reporting" || !m.Reclassifies {
		t.Errorf("reporting.timezone: group %q, reclassifies %v", g.Name, m.Reclassifies)
	}
	for _, key := range []string{"database.path", "http.trusted_proxies", "log.format", "kek.value", "kek.file", "events.pulse.creds_file",
		"merchants.enrichment.remote.api_key_file", "banking.enable_banking.app.app_id"} {
		if !DeploymentKey(key) {
			t.Errorf("%s is not a deployment setting", key)
		}
	}
}
