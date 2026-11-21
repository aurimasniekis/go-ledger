package config

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestOIDCDefaults(t *testing.T) {
	res, err := load(t, okEnv(nil), nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	a := res.Config.Auth
	if !a.Password.Enabled || a.OIDC.Enabled || a.OIDC.Require2FA || a.OIDC.Provisioning.Enabled || a.OIDC.Provisioning.Role != "read-only" ||
		a.OIDC.ClockSkew != time.Minute || !slices.Equal(a.OIDC.Scopes, []string{"openid", "email", "profile"}) || a.OIDC.Name != "Single sign-on" {
		t.Fatalf("defaults %+v", a)
	}
}

func oidcEnv(extra env) env {
	e := env{
		"LEDGER_AUTH_OIDC_ENABLED":       "true",
		"LEDGER_AUTH_OIDC_ISSUER":        "https://id.example.com/realms/home",
		"LEDGER_AUTH_OIDC_CLIENT_ID":     "ledger",
		"LEDGER_AUTH_OIDC_CLIENT_SECRET": sentinel,
		"LEDGER_AUTH_OIDC_REDIRECT_URL":  "https://ledger.example.com/api/v1/auth/oidc/callback",
	}
	for k, v := range extra {
		e[k] = v
	}
	return okEnv(e)
}

func TestOIDCEnabled(t *testing.T) {
	res, err := load(t, oidcEnv(env{"LEDGER_AUTH_PASSWORD_ENABLED": "false", "LEDGER_AUTH_OIDC_SCOPES": "openid,email"}), nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	o := res.Config.Auth.OIDC
	if !o.Enabled || o.ClientSecret != sentinel || res.Config.Auth.Password.Enabled || !slices.Equal(o.Scopes, []string{"openid", "email"}) {
		t.Fatalf("%+v", o)
	}
	// A loopback test provider may use plain http.
	if _, err := load(t, oidcEnv(env{"LEDGER_AUTH_OIDC_ISSUER": "http://127.0.0.1:5556", "LEDGER_AUTH_OIDC_REDIRECT_URL": "http://localhost:8080/api/v1/auth/oidc/callback"}), nil, nil, ""); err != nil {
		t.Fatal(err)
	}
}

func TestOIDCSecretFromFile(t *testing.T) {
	e := oidcEnv(nil)
	delete(e, "LEDGER_AUTH_OIDC_CLIENT_SECRET")
	e["LEDGER_AUTH_OIDC_CLIENT_SECRET_FILE"] = "/run/secret"
	res, err := load(t, e, files{"/run/secret": sentinel + "\n"}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.Auth.OIDC.ClientSecret != sentinel || res.Sources["auth.oidc.client_secret"] != "file /run/secret" {
		t.Fatalf("%q %v", res.Config.Auth.OIDC.ClientSecret, res.Sources)
	}
	_, err = load(t, oidcEnv(env{"LEDGER_AUTH_OIDC_CLIENT_SECRET_FILE": "/run/secret"}), files{"/run/secret": "x"}, nil, "")
	wantProblem(t, problems(t, err), "auth.oidc.client_secret_file")
	e["LEDGER_AUTH_OIDC_CLIENT_SECRET_FILE"] = "/empty"
	_, err = load(t, e, files{"/empty": "\n"}, nil, "")
	wantProblem(t, problems(t, err), "auth.oidc.client_secret_file")
}

func TestOIDCValidation(t *testing.T) {
	_, err := load(t, okEnv(env{"LEDGER_AUTH_PASSWORD_ENABLED": "false"}), nil, nil, "")
	wantProblem(t, problems(t, err), "auth.password.enabled")

	_, err = load(t, okEnv(env{"LEDGER_AUTH_OIDC_ENABLED": "true"}), nil, nil, "")
	got := problems(t, err)
	for _, k := range []string{"auth.oidc.issuer", "auth.oidc.client_id", "LEDGER_AUTH_OIDC_CLIENT_SECRET", "auth.oidc.redirect_url"} {
		wantProblem(t, got, k)
	}
	for k, v := range map[string]string{
		"LEDGER_AUTH_OIDC_ISSUER":            "http://id.example.com",
		"LEDGER_AUTH_OIDC_REDIRECT_URL":      "https://ledger.example.com/callback",
		"LEDGER_AUTH_OIDC_SCOPES":            "email,profile",
		"LEDGER_AUTH_OIDC_CLOCK_SKEW":        "10m",
		"LEDGER_AUTH_OIDC_NAME":              strings.Repeat("n", 65),
		"LEDGER_AUTH_OIDC_PROVISIONING_ROLE": "owner",
	} {
		_, err := load(t, oidcEnv(env{k: v}), nil, nil, "")
		if err == nil {
			t.Errorf("%s=%s accepted", k, v)
			continue
		}
		if strings.Contains(err.Error(), sentinel) {
			t.Fatalf("an error shows the client secret: %v", err)
		}
	}
	// The secret may not come from the YAML file.
	_, err = load(t, okEnv(nil), files{"c.yaml": "auth:\n  oidc:\n    client_secret: nope\n"}, nil, "c.yaml")
	if err == nil {
		t.Fatal("client_secret accepted from YAML")
	}
}
