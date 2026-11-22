package config

import (
	"fmt"
	"strings"

	"github.com/aurimasniekis/go-ledger/internal/domain/decisions"
)

// decisionTask pairs a task name with its configuration slot.
type decisionTask struct {
	name decisions.TaskName
	cfg  *DecisionTask
}

func (d *Decisions) tasks() []decisionTask {
	return []decisionTask{
		{decisions.TaskCategorisation, &d.Tasks.Categorisation}, {decisions.TaskLabelSuggestion, &d.Tasks.LabelSuggestion},
		{decisions.TaskAlerts, &d.Tasks.Alerts}, {decisions.TaskPaymentMatching, &d.Tasks.PaymentMatching},
	}
}

// providerKeys returns each provider's key and key file fields.
func (d *Decisions) providerKeys() map[string][2]*string {
	p := &d.Providers
	return map[string][2]*string{
		"openrouter": {&p.OpenRouter.APIKey, &p.OpenRouter.APIKeyFile},
		"openai":     {&p.OpenAI.APIKey, &p.OpenAI.APIKeyFile},
		"anthropic":  {&p.Anthropic.APIKey, &p.Anthropic.APIKeyFile},
		"typesafe":   {&p.TypeSafe.APIKey, &p.TypeSafe.APIKeyFile},
	}
}

// APIKey returns the resolved API key of a decision provider ("" if unset).
func (d Decisions) APIKey(provider string) string {
	if k, ok := d.providerKeys()[provider]; ok {
		return *k[0]
	}
	return ""
}

// Domain converts the settings into the decision layer's configuration. The
// problems are keyed by configuration path; the configuration is usable only
// when there are none.
func (d Decisions) Domain() (decisions.Config, []Problem) {
	var ps []Problem
	out := decisions.Config{Tasks: map[decisions.TaskName]decisions.TaskConfig{}, Upstreams: map[string][]string{}}
	var ups []string
	for _, u := range d.Providers.OpenRouter.AllowedUpstreams {
		if u = strings.TrimSpace(u); u != "" {
			ups = append(ups, u)
		}
	}
	if len(ups) > 0 {
		out.Upstreams["openrouter"] = ups
	}
	for _, t := range d.tasks() {
		key := "decisions.tasks." + string(t.name)
		c := t.cfg
		tc := decisions.TaskConfig{Enabled: c.Enabled, Route: decisions.Route{Provider: strings.TrimSpace(c.Provider), Model: strings.TrimSpace(c.Model)},
			Capability: decisions.Capability(strings.TrimSpace(c.Capability)), Timeout: c.Timeout, MonthlyRequests: c.MonthlyRequests,
			Retries: c.Retries, AutoApply: c.AutoApply}
		if len(c.Fields) > 0 {
			fs, problems := decisions.ParseFields(c.Fields)
			for _, p := range problems {
				ps = append(ps, Problem{Key: key + ".fields", Msg: p})
			}
			if fs == nil {
				fs = []decisions.Field{}
			}
			tc.Fields = fs
		}
		for i, f := range c.Fallback {
			provider, model, ok := strings.Cut(strings.TrimSpace(f), ":")
			if !ok || provider == "" || model == "" {
				ps = append(ps, Problem{Key: key + ".fallback", Msg: fmt.Sprintf("entry %d must be provider:model", i+1)})
				continue
			}
			tc.Fallback = append(tc.Fallback, decisions.Route{Provider: provider, Model: model})
		}
		out.Tasks[t.name] = tc
	}
	return out, ps
}

// validate checks the decision settings: shapes always, routes and keys for
// enabled tasks. Messages never contain a key.
func (d Decisions) validate(bad func(key, msg string)) {
	for _, t := range d.tasks() {
		key := "decisions.tasks." + string(t.name)
		c := t.cfg
		if c.Timeout < 1e9 || c.Timeout > decisions.MaxTimeout {
			bad(key+".timeout", "must be between 1s and 2m")
		}
		if c.MonthlyRequests < 1 || c.MonthlyRequests > 1_000_000 {
			bad(key+".monthly_requests", "must be from 1 to 1000000")
		}
		if c.Retries < 0 || c.Retries > decisions.MaxRetries {
			bad(key+".retries", "must be from 0 to 2 (decision D13)")
		}
		if c.Enabled && strings.TrimSpace(c.Provider) == "" {
			bad(key+".provider", "is required when the task is enabled: openrouter, openai, anthropic or typesafe")
		}
		if c.Enabled && strings.TrimSpace(c.Model) == "" {
			bad(key+".model", "is required when the task is enabled")
		}
	}
	cfg, ps := d.Domain()
	for _, p := range ps {
		bad(p.Key, p.Msg)
	}
	for _, p := range decisions.ValidateConfig(cfg, decisions.KnownProviders()) {
		name, msg, _ := strings.Cut(p, ": ")
		bad("decisions.tasks."+name, msg)
	}
	keys := d.providerKeys()
	needed := map[string]string{}
	for _, t := range d.tasks() {
		tc := cfg.Tasks[t.name]
		if !tc.Enabled {
			continue
		}
		for _, r := range append([]decisions.Route{tc.Route}, tc.Fallback...) {
			if _, ok := keys[r.Provider]; ok && needed[r.Provider] == "" {
				needed[r.Provider] = string(t.name)
			}
		}
	}
	for provider, task := range needed {
		if *keys[provider][0] == "" {
			k := "decisions.providers." + provider + ".api_key"
			bad(k, fmt.Sprintf("is required because decisions.tasks.%s uses %s: set %s or %s_file", task, provider, EnvName(k), k))
		}
	}
}

// resolveDecisionKeyFiles reads each *_file key into its value.
func resolveDecisionKeyFiles(c *Config, sources map[string]string, readFile func(string) ([]byte, error)) []Problem {
	var ps []Problem
	for provider, k := range c.Decisions.providerKeys() {
		if *k[1] == "" {
			continue
		}
		prefix := "decisions.providers." + provider
		key := prefix + ".api_key_file"
		src := sources[key]
		if *k[0] != "" {
			ps = append(ps, Problem{Source: src, Key: key, Msg: "set only one of " + prefix + ".api_key (" + EnvName(prefix+".api_key") + ") and " + key})
			continue
		}
		data, err := readFile(*k[1])
		if err != nil {
			ps = append(ps, Problem{Source: src, Key: key, Msg: fmt.Sprintf("cannot read the file: %v", pathErr(err))})
			continue
		}
		v := strings.TrimRight(string(data), "\r\n")
		if strings.TrimSpace(v) == "" {
			ps = append(ps, Problem{Source: src, Key: key, Msg: "the file is empty"})
			continue
		}
		*k[0] = v
		sources[prefix+".api_key"] = "file " + *k[1]
	}
	return ps
}
