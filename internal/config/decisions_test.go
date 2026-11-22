package config

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/decisions"
)

func TestDecisionDefaultsAreOff(t *testing.T) {
	res, err := load(t, okEnv(nil), nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	d := res.Config.Decisions
	cfg, ps := d.Domain()
	if len(ps) > 0 || cfg.AnyEnabled() || len(cfg.Upstreams) > 0 {
		t.Fatalf("%+v %v", cfg, ps)
	}
	c := d.Tasks.Categorisation
	if c.Timeout != 30*time.Second || c.Retries != 2 || c.MonthlyRequests != 500 || c.AutoApply || len(c.Fallback) != 0 {
		t.Fatalf("defaults %+v", c)
	}
}

func decisionEnv(extra env) env {
	e := env{
		"LEDGER_DECISIONS_TASKS_CATEGORISATION_ENABLED":  "true",
		"LEDGER_DECISIONS_TASKS_CATEGORISATION_PROVIDER": "anthropic",
		"LEDGER_DECISIONS_TASKS_CATEGORISATION_MODEL":    "claude-haiku-5-5",
		"LEDGER_DECISIONS_PROVIDERS_ANTHROPIC_API_KEY":   sentinel,
	}
	for k, v := range extra {
		e[k] = v
	}
	return okEnv(e)
}

func TestDecisionTaskConfiguration(t *testing.T) {
	res, err := load(t, decisionEnv(env{
		"LEDGER_DECISIONS_TASKS_CATEGORISATION_FIELDS":            "description, candidate_names",
		"LEDGER_DECISIONS_TASKS_CATEGORISATION_AUTO_APPLY":        "true",
		"LEDGER_DECISIONS_TASKS_CATEGORISATION_FALLBACK":          "openrouter:vendor/model",
		"LEDGER_DECISIONS_PROVIDERS_OPENROUTER_ALLOWED_UPSTREAMS": "anthropic",
		"LEDGER_DECISIONS_PROVIDERS_OPENROUTER_API_KEY_FILE":      "/run/or",
	}), files{"/run/or": "or-key\n"}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg, ps := res.Config.Decisions.Domain()
	tc := cfg.Tasks[decisions.TaskCategorisation]
	if len(ps) > 0 || !tc.Enabled || tc.Provider != "anthropic" || !tc.AutoApply ||
		!slices.Equal(tc.Fields, []decisions.Field{decisions.FieldDescription, decisions.FieldCandidateNames}) ||
		!slices.Equal(tc.Fallback, []decisions.Route{{Provider: "openrouter", Model: "vendor/model"}}) ||
		!slices.Equal(cfg.Upstreams["openrouter"], []string{"anthropic"}) {
		t.Fatalf("%+v %v", cfg, ps)
	}
	if res.Config.Decisions.APIKey("openrouter") != "or-key" || res.Config.Decisions.APIKey("anthropic") != sentinel {
		t.Fatal("keys not resolved")
	}
}

func TestDecisionValidation(t *testing.T) {
	for name, tt := range map[string]struct {
		env  env
		want string
	}{
		"excluded field":            {env{"LEDGER_DECISIONS_TASKS_CATEGORISATION_FIELDS": "description,iban,candidate_names"}, "decisions.tasks.categorisation.fields"},
		"unknown field":             {env{"LEDGER_DECISIONS_TASKS_CATEGORISATION_FIELDS": "everything"}, "decisions.tasks.categorisation.fields"},
		"decide without candidates": {env{"LEDGER_DECISIONS_TASKS_CATEGORISATION_FIELDS": "description"}, "decisions.tasks.categorisation"},
		"typesafe alerts": {env{"LEDGER_DECISIONS_TASKS_ALERTS_ENABLED": "true", "LEDGER_DECISIONS_TASKS_ALERTS_PROVIDER": "typesafe",
			"LEDGER_DECISIONS_TASKS_ALERTS_MODEL": "x", "LEDGER_DECISIONS_PROVIDERS_TYPESAFE_API_KEY": "k"}, "decisions.tasks.alerts"},
		"auto-apply labels": {env{"LEDGER_DECISIONS_TASKS_LABEL_SUGGESTION_ENABLED": "true", "LEDGER_DECISIONS_TASKS_LABEL_SUGGESTION_PROVIDER": "anthropic",
			"LEDGER_DECISIONS_TASKS_LABEL_SUGGESTION_MODEL": "x", "LEDGER_DECISIONS_TASKS_LABEL_SUGGESTION_AUTO_APPLY": "true"}, "decisions.tasks.label_suggestion"},
		"openrouter without upstreams": {env{"LEDGER_DECISIONS_TASKS_CATEGORISATION_PROVIDER": "openrouter", "LEDGER_DECISIONS_PROVIDERS_OPENROUTER_API_KEY": "k"}, "decisions.tasks.categorisation"},
		"missing key":                  {env{"LEDGER_DECISIONS_PROVIDERS_ANTHROPIC_API_KEY": ""}, "decisions.providers.anthropic.api_key"},
		"fallback key missing":         {env{"LEDGER_DECISIONS_TASKS_CATEGORISATION_FALLBACK": "openai:m"}, "decisions.providers.openai.api_key"},
		"bad fallback":                 {env{"LEDGER_DECISIONS_TASKS_CATEGORISATION_FALLBACK": "openai"}, "decisions.tasks.categorisation.fallback"},
		"no provider":                  {env{"LEDGER_DECISIONS_TASKS_CATEGORISATION_PROVIDER": ""}, "decisions.tasks.categorisation.provider"},
		"no model":                     {env{"LEDGER_DECISIONS_TASKS_CATEGORISATION_MODEL": ""}, "decisions.tasks.categorisation.model"},
		"capability mismatch":          {env{"LEDGER_DECISIONS_TASKS_CATEGORISATION_CAPABILITY": "generate"}, "decisions.tasks.categorisation"},
		"three retries":                {env{"LEDGER_DECISIONS_TASKS_CATEGORISATION_RETRIES": "3"}, "decisions.tasks.categorisation.retries"},
		"no cap":                       {env{"LEDGER_DECISIONS_TASKS_CATEGORISATION_MONTHLY_REQUESTS": "0"}, "decisions.tasks.categorisation.monthly_requests"},
		"timeout too long":             {env{"LEDGER_DECISIONS_TASKS_ALERTS_TIMEOUT": "5m"}, "decisions.tasks.alerts.timeout"},
		"both key and file":            {env{"LEDGER_DECISIONS_PROVIDERS_ANTHROPIC_API_KEY_FILE": "/run/k"}, "decisions.providers.anthropic.api_key_file"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := load(t, decisionEnv(tt.env), files{"/run/k": "k"}, nil, "")
			got := problems(t, err)
			wantProblem(t, got, tt.want)
			for _, p := range got {
				if strings.Contains(p, sentinel) {
					t.Fatalf("a problem shows the key: %s", p)
				}
			}
		})
	}
}

func TestDecisionKeyIsEnvironmentOrFileOnly(t *testing.T) {
	_, err := load(t, okEnv(nil), files{"c.yaml": "decisions:\n  providers:\n    openai:\n      api_key: " + sentinel + "\n"}, nil, "c.yaml")
	got := problems(t, err)
	wantProblem(t, got, "secrets must not be written")
	for _, p := range got {
		if strings.Contains(p, sentinel) {
			t.Fatalf("the key leaked into a problem: %s", p)
		}
	}
	if _, err := load(t, okEnv(nil), nil, map[string]string{"decisions.providers.openai.api_key": "x"}, ""); err == nil {
		t.Fatal("a key was accepted as a flag")
	}
}

func TestDisabledTasksNeedNoKey(t *testing.T) {
	if _, err := load(t, okEnv(env{"LEDGER_DECISIONS_TASKS_ALERTS_PROVIDER": "typesafe"}), nil, nil, ""); err != nil {
		t.Fatal(err)
	}
}
