package config

import (
	"testing"
	"time"
)

func TestMerchantRemoteIsOffByDefault(t *testing.T) {
	res, err := load(t, okEnv(nil), nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	r := res.Config.Merchants.Enrichment.Remote
	if r.Enabled || r.URL != "" || r.StoreLogoRef || r.Country != "" || r.Timeout != 10*time.Second || r.Retention != 720*time.Hour {
		t.Fatalf("defaults %+v", r)
	}
}

func TestMerchantRemoteValidation(t *testing.T) {
	on := env{"LEDGER_MERCHANTS_ENRICHMENT_REMOTE_ENABLED": "true"}
	with := func(k, v string) env {
		e := env{}
		for a, b := range on {
			e[a] = b
		}
		e[k] = v
		return e
	}
	for name, tt := range map[string]struct {
		env  env
		want string
	}{
		"enabled without url": {on, "merchants.enrichment.remote.url"},
		"plain http":          {with("LEDGER_MERCHANTS_ENRICHMENT_REMOTE_URL", "http://lookup.example.com/"), "merchants.enrichment.remote.url"},
		"credentials in url":  {with("LEDGER_MERCHANTS_ENRICHMENT_REMOTE_URL", "https://u:p@lookup.example.com/"), "merchants.enrichment.remote.url"},
		"query in url":        {with("LEDGER_MERCHANTS_ENRICHMENT_REMOTE_URL", "https://lookup.example.com/?key=1"), "merchants.enrichment.remote.url"},
		"timeout":             {env{"LEDGER_MERCHANTS_ENRICHMENT_REMOTE_TIMEOUT": "5m"}, "merchants.enrichment.remote.timeout"},
		"retention too short": {env{"LEDGER_MERCHANTS_ENRICHMENT_REMOTE_RETENTION": "1h"}, "merchants.enrichment.remote.retention"},
		"country":             {env{"LEDGER_MERCHANTS_ENRICHMENT_REMOTE_COUNTRY": "lithuania"}, "merchants.enrichment.remote.country"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := load(t, okEnv(tt.env), nil, nil, "")
			if err == nil || !contains(err.Error(), tt.want) {
				t.Fatalf("want a problem with %s, got %v", tt.want, err)
			}
		})
	}
	good := with("LEDGER_MERCHANTS_ENRICHMENT_REMOTE_URL", "https://lookup.example.com/v1/lookup")
	good["LEDGER_MERCHANTS_ENRICHMENT_REMOTE_COUNTRY"] = "LT"
	good["LEDGER_MERCHANTS_ENRICHMENT_REMOTE_RETENTION"] = "0"
	if _, err := load(t, okEnv(good), nil, nil, ""); err != nil {
		t.Fatal(err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
