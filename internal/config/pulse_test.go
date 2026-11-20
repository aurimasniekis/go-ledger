package config

import "testing"

func pulseEnv(extra env) env {
	e := okEnv(env{"LEDGER_EVENTS_PULSE_ENABLED": "true"})
	for k, v := range extra {
		e[k] = v
	}
	return e
}

func TestPulseDisabledNeedsNoConnectionSettings(t *testing.T) {
	res, err := load(t, okEnv(nil), nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	p := res.Config.Events.Pulse
	if p.Enabled || p.URL != "" || p.CredsFile != "" || p.CAFile != "" || p.Name != "ledger" || p.Source != "ledger" {
		t.Fatalf("defaults %+v", p)
	}
}

func TestPulseEnabledRequiresURLAndCreds(t *testing.T) {
	_, err := load(t, pulseEnv(nil), nil, nil, "")
	got := problems(t, err)
	wantProblem(t, got, "events.pulse.url")
	wantProblem(t, got, "events.pulse.creds_file")
	_, err = load(t, pulseEnv(env{"LEDGER_EVENTS_PULSE_URL": "nats://host:4222", "LEDGER_EVENTS_PULSE_CREDS_FILE": "/x"}), nil, nil, "")
	wantProblem(t, problems(t, err), "tls://")
	_, err = load(t, pulseEnv(env{"LEDGER_EVENTS_PULSE_URL": "tls://", "LEDGER_EVENTS_PULSE_CREDS_FILE": "/x"}), nil, nil, "")
	wantProblem(t, problems(t, err), "tls://")
	// The files are not opened at load: a missing creds file must not stop startup.
	res, err := load(t, pulseEnv(env{"LEDGER_EVENTS_PULSE_URL": "tls://pulse.example.com:4222", "LEDGER_EVENTS_PULSE_CREDS_FILE": "/missing.creds", "LEDGER_EVENTS_PULSE_CA_FILE": "/missing-ca.pem"}), nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.Events.Pulse.CredsFile != "/missing.creds" {
		t.Fatalf("%+v", res.Config.Events.Pulse)
	}
}

func TestPulseSourceValidatedAtLoadEvenWhenDisabled(t *testing.T) {
	for _, src := range []string{"   ", "bad\nsource", string(make([]byte, 0)) + repeat("x", 129)} {
		_, err := load(t, okEnv(env{"LEDGER_EVENTS_PULSE_SOURCE": src}), nil, nil, "")
		if src == "   " || err != nil {
			wantProblem(t, problems(t, err), "events.pulse.source")
		}
	}
	if _, err := load(t, okEnv(env{"LEDGER_EVENTS_PULSE_SOURCE": "my ledger"}), nil, nil, ""); err != nil {
		t.Fatal(err)
	}
}

func TestPulseNameValidatedAtLoad(t *testing.T) {
	_, err := load(t, okEnv(env{"LEDGER_EVENTS_PULSE_NAME": "bad\nname"}), nil, nil, "")
	wantProblem(t, problems(t, err), "events.pulse.name")
	if _, err := load(t, okEnv(env{"LEDGER_EVENTS_PULSE_NAME": "my ledger"}), nil, nil, ""); err != nil {
		t.Fatal(err)
	}
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
