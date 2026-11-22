package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func TestSourceLayers(t *testing.T) {
	for src, want := range map[string]string{
		"default": LayerDefault, "api": LayerAPI, "yaml": LayerYAML, "env LEDGER_X": LayerEnvironment,
		"dotenv LEDGER_X": LayerDotenv, "flag --x": LayerFlag, "file /run/secret": LayerFile, "": LayerDefault,
	} {
		if got := Layer(src); got != want {
			t.Errorf("Layer(%q) = %q, want %q", src, got, want)
		}
		pinned := want != LayerDefault && want != LayerAPI
		if Pinned(src) != pinned {
			t.Errorf("Pinned(%q) = %v, want %v", src, !pinned, pinned)
		}
	}
}

// A value from the .env file is labelled dotenv, one from the real
// environment env, so a pin names its layer (ADR-0066 point 5).
func TestDotenvSourceIsSplitFromEnvironment(t *testing.T) {
	res, err := load(t, okEnv(env{"LEDGER_MCP_BURST": "5"}), files{".env": "LEDGER_HTTP_LISTEN=:4444\nLEDGER_MCP_BURST=6\n"}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Sources["http.listen"]; got != "dotenv LEDGER_HTTP_LISTEN" {
		t.Errorf("http.listen source = %q, want dotenv LEDGER_HTTP_LISTEN", got)
	}
	if got := res.Sources["mcp.burst"]; got != "env LEDGER_MCP_BURST" || res.Config.MCP.Burst != 5 {
		t.Errorf("mcp.burst = %d from %q, want 5 from the real environment", res.Config.MCP.Burst, got)
	}
	if strings.Join(res.EnvLoaded, ",") != "LEDGER_HTTP_LISTEN" {
		t.Errorf("EnvLoaded = %v, want only LEDGER_HTTP_LISTEN", res.EnvLoaded)
	}
}

func TestStoredLayerPrecedence(t *testing.T) {
	stored := map[string]json.RawMessage{"mcp.burst": raw("7"), "log.level": raw(`"debug"`), "banking.refresh.interval": raw(`"90m"`)}
	cases := []struct {
		name   string
		e      env
		f      files
		flags  map[string]string
		burst  int
		source string
	}{
		{"stored over default", okEnv(nil), nil, nil, 7, "api"},
		{"yaml over stored", okEnv(nil), files{"c.yaml": "mcp:\n  burst: 8\n"}, nil, 8, "yaml"},
		{"dotenv over stored", okEnv(nil), files{".env": "LEDGER_MCP_BURST=9\n"}, nil, 9, "dotenv LEDGER_MCP_BURST"},
		{"env over stored", okEnv(env{"LEDGER_MCP_BURST": "10"}), nil, nil, 10, "env LEDGER_MCP_BURST"},
		{"flag over stored", okEnv(nil), nil, map[string]string{"mcp.burst": "11"}, 11, "flag --mcp.burst"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := ""
			if _, ok := tc.f["c.yaml"]; ok {
				path = "c.yaml"
			}
			res, err := Load(Options{ConfigPath: path, Flags: tc.flags, LookupEnv: tc.e.lookup, ReadFile: tc.f.read, Stored: stored})
			if err != nil {
				t.Fatal(err)
			}
			if res.Config.MCP.Burst != tc.burst || res.Sources["mcp.burst"] != tc.source {
				t.Errorf("mcp.burst = %d from %q, want %d from %q", res.Config.MCP.Burst, res.Sources["mcp.burst"], tc.burst, tc.source)
			}
			if res.Config.Log.Level != "debug" || res.Sources["log.level"] != "api" {
				t.Errorf("log.level = %q from %q, want debug from api", res.Config.Log.Level, res.Sources["log.level"])
			}
			if res.Config.Banking.Refresh.Interval != 90*time.Minute {
				t.Errorf("banking.refresh.interval = %v, want 1h30m", res.Config.Banking.Refresh.Interval)
			}
		})
	}
}

// The write path's WithStored gives exactly what loading with the stored
// layer gives.
func TestWithStoredMatchesLoad(t *testing.T) {
	e, f := okEnv(env{"LEDGER_MCP_BURST": "4"}), files{"c.yaml": "log:\n  level: warn\n"}
	stored := map[string]json.RawMessage{"mcp.burst": raw("7"), "log.level": raw(`"debug"`), "mcp.requests_per_minute": raw("60"),
		"auth.oidc.scopes": raw(`["openid","email"]`)}
	direct, err := Load(Options{ConfigPath: "c.yaml", LookupEnv: e.lookup, ReadFile: f.read, Stored: stored})
	if err != nil {
		t.Fatal(err)
	}
	base, err := Load(Options{ConfigPath: "c.yaml", LookupEnv: e.lookup, ReadFile: f.read})
	if err != nil {
		t.Fatal(err)
	}
	layered, err := base.WithStored(stored)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(direct.Config, layered.Config) {
		t.Errorf("configs differ:\nload:        %+v\nwith stored: %+v", direct.Config.MCP, layered.Config.MCP)
	}
	if !reflect.DeepEqual(direct.Sources, layered.Sources) {
		t.Errorf("sources differ")
	}
	if base.Config.MCP.RequestsPerMinute != 120 || base.Sources["mcp.requests_per_minute"] != "default" {
		t.Errorf("WithStored changed its receiver: %d from %q", base.Config.MCP.RequestsPerMinute, base.Sources["mcp.requests_per_minute"])
	}
	// Removing a stored value falls back to the default; the pins stay.
	again, err := layered.WithStored(map[string]json.RawMessage{"mcp.burst": raw("7")})
	if err != nil {
		t.Fatal(err)
	}
	if again.Config.MCP.RequestsPerMinute != 120 || again.Sources["mcp.requests_per_minute"] != "default" {
		t.Errorf("mcp.requests_per_minute = %d from %q, want the default", again.Config.MCP.RequestsPerMinute, again.Sources["mcp.requests_per_minute"])
	}
	if again.Config.MCP.Burst != 4 || again.Sources["mcp.burst"] != "env LEDGER_MCP_BURST" {
		t.Errorf("pinned mcp.burst = %d from %q, want 4 from the environment", again.Config.MCP.Burst, again.Sources["mcp.burst"])
	}
	if again.Config.Log.Level != "warn" || len(again.Config.Auth.OIDC.Scopes) != 3 {
		t.Errorf("log.level = %q, scopes = %v; want the YAML level and the default scopes", again.Config.Log.Level, again.Config.Auth.OIDC.Scopes)
	}
}

// The whole would-be configuration is validated, cross-field rules and pinned
// values included (ADR-0066 point 10).
func TestWithStoredValidatesTheWholeConfiguration(t *testing.T) {
	e, f := okEnv(nil), files{"c.yaml": "auth:\n  session:\n    absolute_timeout: 1h\n"}
	base, err := Load(Options{ConfigPath: "c.yaml", LookupEnv: e.lookup, ReadFile: f.read})
	if err != nil {
		t.Fatal(err)
	}
	_, err = base.WithStored(map[string]json.RawMessage{"auth.session.idle_timeout": raw(`"2h"`), "mcp.burst": raw("0")})
	got := problems(t, err)
	wantProblem(t, got, "api", "auth.session.idle_timeout", "must not exceed auth.session.absolute_timeout")
	wantProblem(t, got, "api", "mcp.burst")
	_, err = base.WithStored(map[string]json.RawMessage{"rates.stale_after": raw(`"24h"`), "rates.refresh_interval": raw(`"48h"`)})
	wantProblem(t, problems(t, err), "rates.refresh_interval")
}

func TestStoredValuesAreTyped(t *testing.T) {
	cases := []struct{ key, value, want string }{
		{"mcp.burst", `"7"`, "expected an integer, got a string"},
		{"mcp.burst", "7.5", "not a whole number"},
		{"mcp.enabled", `"true"`, "expected a boolean"},
		{"banking.refresh.interval", "60", "expected a duration"},
		{"banking.refresh.interval", `"6"`, "invalid duration"},
		{"auth.oidc.scopes", `"openid"`, "expected a list of strings"},
		{"auth.oidc.scopes", `["openid", 1]`, "an item that is a number"},
		{"log.level", `"loud"`, "must be one of debug, info, warn, error"},
		{"log.level", `"info" "warn"`, "more than one JSON value"},
		{"log.level", "{", "invalid JSON"},
		{"log.level", "null", "got null"},
	}
	for _, tc := range cases {
		if _, err := NormalizeStored(tc.key, raw(tc.value)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("NormalizeStored(%s, %s) = %v, want an error with %q", tc.key, tc.value, err, tc.want)
		}
	}
	for key, pair := range map[string][2]string{
		"banking.refresh.interval": {`"90m"`, `"1h30m0s"`},
		"mcp.burst":                {"7", "7"},
		"auth.oidc.scopes":         {"[]", "[]"},
		"log.level":                {`"warn"`, `"warn"`},
	} {
		got, err := NormalizeStored(key, raw(pair[0]))
		if err != nil || string(got) != pair[1] {
			t.Errorf("NormalizeStored(%s, %s) = %s, %v; want %s", key, pair[0], got, err, pair[1])
		}
	}
	if _, err := NormalizeStored("no.such.key", raw("1")); err == nil {
		t.Error("an unknown key was accepted")
	}
}

// A secret never enters the stored layer, and the refusal never repeats it.
func TestStoredLayerRefusesSecrets(t *testing.T) {
	value := raw(`"` + sentinel + `"`)
	if _, err := NormalizeStored("auth.oidc.client_secret", value); err == nil || strings.Contains(err.Error(), sentinel) {
		t.Errorf("NormalizeStored(secret) = %v, want a refusal without the value", err)
	}
	_, err := load(t, okEnv(nil), nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	e := okEnv(nil)
	_, err = Load(Options{LookupEnv: e.lookup, ReadFile: files{}.read, Stored: map[string]json.RawMessage{
		"auth.oidc.client_secret": value, "no.such": raw("1"), "log.level": raw(`"loud"`)}})
	got := problems(t, err)
	wantProblem(t, got, "auth.oidc.client_secret", "secret is not kept")
	wantProblem(t, got, "no.such", "no such key")
	wantProblem(t, got, "log.level", "stored value")
	if strings.Contains(err.Error(), sentinel) {
		t.Error("the error repeats the secret")
	}
}

func TestCloneCopiesLists(t *testing.T) {
	var c Config
	c.Auth.OIDC.Scopes = []string{"openid"}
	d := c.Clone()
	d.Auth.OIDC.Scopes[0] = "changed"
	if c.Auth.OIDC.Scopes[0] != "openid" {
		t.Error("Clone shares a list with its original")
	}
}
