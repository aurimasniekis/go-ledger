package config

import (
	"slices"
	"testing"
	"time"
)

func TestAlertDefaultsAreInAppOnly(t *testing.T) {
	res, err := load(t, okEnv(nil), nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	a := res.Config.Alerts
	if len(a.AlertChannels()) != 0 || a.ConsentHorizonDays != 14 || a.Interval != time.Hour {
		t.Fatalf("defaults %+v", a)
	}
}

func TestAlertChannelPulseNeedsPulseEnabled(t *testing.T) {
	_, err := load(t, okEnv(env{"LEDGER_ALERTS_CHANNELS": "pulse"}), nil, nil, "")
	wantProblem(t, problems(t, err), "alerts.channels")
	res, err := load(t, pulseEnv(env{"LEDGER_ALERTS_CHANNELS": " Pulse, pulse ,", "LEDGER_EVENTS_PULSE_URL": "tls://pulse.example.com:4222", "LEDGER_EVENTS_PULSE_CREDS_FILE": "/x.creds"}), nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Config.Alerts.AlertChannels(); !slices.Equal(got, []string{"pulse"}) {
		t.Fatalf("%v", got)
	}
}

func TestAlertSettingsAreValidated(t *testing.T) {
	for k, v := range map[string]string{
		"LEDGER_ALERTS_CHANNELS":             "email",
		"LEDGER_ALERTS_CONSENT_HORIZON_DAYS": "0",
		"LEDGER_ALERTS_INTERVAL":             "10s",
	} {
		_, err := load(t, okEnv(env{k: v}), nil, nil, "")
		wantProblem(t, problems(t, err), "alerts.")
	}
}
