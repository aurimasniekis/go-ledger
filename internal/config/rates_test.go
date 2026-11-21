package config

import (
	"testing"
	"time"
)

func TestRatesDefaultsAreDecisionD11(t *testing.T) {
	res, err := load(t, okEnv(nil), nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	r := res.Config.Rates
	if r.Enabled || r.BaseURL != "https://api.frankfurter.dev" || r.Source != "ECB" ||
		r.RefreshInterval != 24*time.Hour || r.StaleAfter != 5*24*time.Hour || r.Timeout != 15*time.Second {
		t.Fatalf("defaults %+v", r)
	}
}

func TestRatesSourceKeyCaseIsNormalised(t *testing.T) {
	for in, want := range map[string]string{"ecb": "ECB", "Ecb": "ECB", "ECB": "ECB", "blended": "blended", "Blended": "blended", "BLENDED": "blended"} {
		res, err := load(t, okEnv(env{"LEDGER_RATES_ENABLED": "true", "LEDGER_RATES_SOURCE": in}), nil, nil, "")
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got := res.Config.Rates.Source; got != want {
			t.Errorf("source %q became %q, want %q", in, got, want)
		}
	}
}

func TestRatesValidation(t *testing.T) {
	for name, tt := range map[string]struct {
		env  env
		want string
	}{
		"stale below a day":         {env{"LEDGER_RATES_STALE_AFTER": "12h"}, "rates.stale_after"},
		"refresh below an hour":     {env{"LEDGER_RATES_REFRESH_INTERVAL": "10m"}, "rates.refresh_interval"},
		"refresh above stale":       {env{"LEDGER_RATES_REFRESH_INTERVAL": "200h"}, "rates.refresh_interval"},
		"timeout too short":         {env{"LEDGER_RATES_TIMEOUT": "10ms"}, "rates.timeout"},
		"timeout too long":          {env{"LEDGER_RATES_TIMEOUT": "10m"}, "rates.timeout"},
		"plain http":                {env{"LEDGER_RATES_ENABLED": "true", "LEDGER_RATES_BASE_URL": "http://rates.example.com"}, "rates.base_url"},
		"credentials in url":        {env{"LEDGER_RATES_ENABLED": "true", "LEDGER_RATES_BASE_URL": "https://u:p@rates.example.com"}, "rates.base_url"},
		"query in url":              {env{"LEDGER_RATES_ENABLED": "true", "LEDGER_RATES_BASE_URL": "https://rates.example.com/?k=1"}, "rates.base_url"},
		"bad source":                {env{"LEDGER_RATES_ENABLED": "true", "LEDGER_RATES_SOURCE": "EC B"}, "rates.source"},
		"source that edits the url": {env{"LEDGER_RATES_ENABLED": "true", "LEDGER_RATES_SOURCE": "ECB&date=1"}, "rates.source"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := load(t, okEnv(tt.env), nil, nil, "")
			wantProblem(t, problems(t, err), tt.want)
		})
	}
}

func TestRatesAcceptedSettings(t *testing.T) {
	for name, e := range map[string]env{
		"loopback http test server": {"LEDGER_RATES_ENABLED": "true", "LEDGER_RATES_BASE_URL": "http://127.0.0.1:9999"},
		"blended":                   {"LEDGER_RATES_ENABLED": "true", "LEDGER_RATES_SOURCE": "blended"},
		"dotted key":                {"LEDGER_RATES_ENABLED": "true", "LEDGER_RATES_SOURCE": "TCMB.FOREX"},
		"disabled ignores the url":  {"LEDGER_RATES_BASE_URL": "ftp://x"},
		"six hourly, week stale":    {"LEDGER_RATES_REFRESH_INTERVAL": "6h", "LEDGER_RATES_STALE_AFTER": "168h"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := load(t, okEnv(e), nil, nil, ""); err != nil {
				t.Fatal(err)
			}
		})
	}
}
