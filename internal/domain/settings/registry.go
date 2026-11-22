// Package settings holds the domain side of Ledger's runtime settings
// (ADR-0066): the registry of setting groups and their members, the port of
// the store that keeps the stored settings layer, and the errors of a write.
// The service that joins them with the configuration loader (snapshot, write
// path, subscriptions) is internal/settings: finance and store packages may
// not import the configuration package, which reaches a provider adapter.
//
// The effective configuration is config.Config. A runtime setting's value
// comes, highest first, from a flag, the environment (real, then .env), the
// YAML file, the stored API value or the built-in default. A key set by a
// configuration layer is pinned and cannot be written through the API.
// Deployment settings (point 2) and fixed policy (point 3) are not in the
// registry at all.
package settings

import (
	"regexp"
	"slices"
	"strings"
)

// Grant resources a group needs (ADR-0066 point 18). The authorization
// vocabulary gains them with the API routes (task S1).
const (
	GrantSettings     = "settings"
	GrantIntegrations = "integrations"
)

// Kind tells a singleton group from a keyed object. Both always exist for the
// API and have one stored row at most.
type Kind string

const (
	// Singleton is one group at /settings/{group}.
	Singleton Kind = "singleton"
	// Keyed is one object of a fixed set of keys, such as one decision task.
	Keyed Kind = "keyed"
)

// Member is one setting of a group.
type Member struct {
	// Name is the member's name in the group's representation and in the
	// stored JSON document, for example "refresh.interval".
	Name string
	// Key is the configuration key, for example "banking.refresh.interval".
	Key string
	// Sensitive writes need recent authentication or the root token (point
	// 19). Until that gate exists (task S2) they are refused.
	Sensitive bool
	// Secret marks a write-only secret (point 12). It is never part of a
	// representation and never kept in the stored JSON document.
	Secret bool
	// Reclassifies marks the reporting zone: changing it needs an explicit
	// reclassification (point 6a, task S3). Until then it is refused.
	Reclassifies bool
}

// Group is a set of runtime settings consumed by one component.
type Group struct {
	Name    string
	Kind    Kind
	Grant   string
	Members []Member
}

// Member returns the member called name.
func (g Group) Member(name string) (Member, bool) {
	i := slices.IndexFunc(g.Members, func(m Member) bool { return m.Name == name })
	if i < 0 {
		return Member{}, false
	}
	return g.Members[i], true
}

// MemberByKey returns the member whose configuration key is key.
func (g Group) MemberByKey(key string) (Member, bool) {
	i := slices.IndexFunc(g.Members, func(m Member) bool { return m.Key == key })
	if i < 0 {
		return Member{}, false
	}
	return g.Members[i], true
}

// NamePattern is the rule for a group name, the same rule the setting_groups
// table checks: a singleton name, or a keyed object's collection and key
// joined by a dot.
var NamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}(\.[a-z][a-z0-9_]{0,62})?$`)

// memberSpec lists a member relative to a group's key prefix.
type memberSpec struct {
	name      string
	sensitive bool
	secret    bool
}

func plain(names ...string) []memberSpec {
	out := make([]memberSpec, len(names))
	for i, n := range names {
		out[i] = memberSpec{name: n}
	}
	return out
}

func sensitive(names ...string) []memberSpec {
	out := make([]memberSpec, len(names))
	for i, n := range names {
		out[i] = memberSpec{name: n, sensitive: true}
	}
	return out
}

func secret(name string) memberSpec { return memberSpec{name: name, sensitive: true, secret: true} }

func group(name string, kind Kind, grant, prefix string, specs ...[]memberSpec) Group {
	g := Group{Name: name, Kind: kind, Grant: grant}
	for _, list := range specs {
		for _, s := range list {
			g.Members = append(g.Members, Member{Name: s.name, Key: prefix + "." + s.name, Sensitive: s.sensitive, Secret: s.secret})
		}
	}
	return g
}

// The decision providers and tasks of ADR-0060, keyed objects of ADR-0066
// point 8.
var (
	decisionProviders = []string{"openrouter", "openai", "anthropic", "typesafe"}
	decisionTasks     = []string{"categorisation", "label_suggestion", "alerts", "payment_matching"}
)

// registry is every runtime-setting group of ADR-0066 point 8, in a stable
// order. Bank applications, consents and rules are collections with tables of
// their own (tasks S7, S9, X2b) and are not groups. Members that later tasks
// add to the configuration (merchants.enrichment.remote.api_key,
// events.pulse.creds and ca_pem) join their groups in those tasks.
var registry = func() []Group {
	reporting := group("reporting", Singleton, GrantSettings, "reporting", plain("timezone", "currency"))
	reporting.Members[0].Reclassifies = true
	gs := []Group{
		reporting,
		group("banking", Singleton, GrantSettings, "banking",
			sensitive("redirect_url"),
			plain("consent_ttl", "stale_after", "refresh.enabled", "refresh.interval", "refresh.request_cooldown", "refresh.max_backoff"),
			sensitive("enable_banking.base_url")),
		group("rates", Singleton, GrantSettings, "rates",
			plain("enabled"), sensitive("base_url"), plain("source", "refresh_interval", "stale_after", "timeout")),
		group("merchant_enrichment", Singleton, GrantIntegrations, "merchants.enrichment.remote",
			sensitive("enabled", "url", "country", "timeout", "attribution", "store_logo_ref", "retention")),
		group("auth", Singleton, GrantSettings, "auth",
			sensitive("password.enabled", "password.min_length",
				"session.idle_timeout", "session.absolute_timeout", "session.reauth_window",
				"sign_in.account_max_failures", "sign_in.address_max_failures", "sign_in.window", "sign_in.device_cookie_ttl",
				"require_2fa")),
		group("oidc", Singleton, GrantSettings, "auth.oidc",
			sensitive("enabled", "name", "issuer", "client_id"), []memberSpec{secret("client_secret")},
			sensitive("redirect_url", "scopes", "require_2fa", "clock_skew", "provisioning.enabled", "provisioning.role")),
		group("pulse", Singleton, GrantIntegrations, "events.pulse", sensitive("enabled", "url", "source", "name")),
		group("mcp", Singleton, GrantSettings, "mcp", plain("enabled", "requests_per_minute", "burst", "max_request_bytes")),
		group("logging", Singleton, GrantSettings, "log", plain("level")),
		// The alert keys (task L6, ADR-0065) arrived after ADR-0066 was
		// written; by its point 1 they are runtime settings. channels is
		// sensitive because it lets alert events leave the machine (point 19).
		group("alerts", Singleton, GrantSettings, "alerts", sensitive("channels"), plain("consent_horizon_days", "interval")),
	}
	for _, p := range decisionProviders {
		specs := [][]memberSpec{{secret("api_key")}}
		if p == "openrouter" {
			specs = append(specs, sensitive("allowed_upstreams"))
		}
		gs = append(gs, group("decision_providers."+p, Keyed, GrantIntegrations, "decisions.providers."+p, specs...))
	}
	for _, t := range decisionTasks {
		gs = append(gs, group("decision_tasks."+t, Keyed, GrantIntegrations, "decisions.tasks."+t,
			sensitive("enabled", "provider", "model", "capability", "timeout", "monthly_requests", "retries", "auto_apply", "fields", "fallback")))
	}
	return gs
}()

// Groups returns every group, in a stable order.
func Groups() []Group {
	out := make([]Group, len(registry))
	for i, g := range registry {
		out[i] = g
		out[i].Members = slices.Clone(g.Members)
	}
	return out
}

// Lookup returns the group called name.
func Lookup(name string) (Group, bool) {
	for _, g := range registry {
		if g.Name == name {
			g.Members = slices.Clone(g.Members)
			return g, true
		}
	}
	return Group{}, false
}

// GroupOfKey returns the group and member that own a configuration key.
func GroupOfKey(key string) (Group, Member, bool) {
	for _, g := range registry {
		if m, ok := g.MemberByKey(key); ok {
			g.Members = slices.Clone(g.Members)
			return g, m, true
		}
	}
	return Group{}, Member{}, false
}

// deploymentKeys are the deployment settings of ADR-0066 point 2 that are not
// *_file keys: configuration only, shown read-only through the API. The
// operator approved this list on 2026-11-22.
var deploymentKeys = []string{
	"database.path", "database.busy_timeout",
	"http.listen", "http.management_listen", "http.read_header_timeout", "http.read_timeout",
	"http.write_timeout", "http.idle_timeout", "http.shutdown_timeout", "http.max_body_bytes",
	"http.trusted_proxies",
	"log.format",
	"kek.value", "kek.file",
}

// DeploymentKey reports whether key is a deployment setting (ADR-0066 point
// 2): a key the API never writes. Every *_file key is one, and so are the
// keys of the two configured Enable Banking slots, which the bank
// applications collection shows read-only (point 8, task S7).
func DeploymentKey(key string) bool {
	switch {
	case slices.Contains(deploymentKeys, key), strings.HasSuffix(key, "_file"):
		return true
	case strings.HasPrefix(key, "banking.enable_banking.app."), strings.HasPrefix(key, "banking.enable_banking.developer."):
		return true
	}
	return false
}
