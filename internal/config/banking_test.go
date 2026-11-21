package config

import (
	"strings"
	"testing"
)

const testEBAppID = "0f6c3c1e-1b2a-4c3d-9e8f-123456789abc"

func TestEnableBankingDisabledByDefault(t *testing.T) {
	res, err := load(t, okEnv(nil), nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eb := res.Config.Banking.EnableBanking
	if eb.App.Enabled || eb.Developer.Enabled || eb.BaseURL != "https://api.enablebanking.com" ||
		eb.App.Environment != "production" || eb.Developer.AppName("developer") != "enablebanking-developer" {
		t.Fatalf("defaults %+v", eb)
	}
}

func TestEnableBankingEnabledRequiresIDAndKey(t *testing.T) {
	_, err := load(t, okEnv(env{"LEDGER_BANKING_ENABLE_BANKING_DEVELOPER_ENABLED": "true"}), nil, nil, "")
	got := problems(t, err)
	wantProblem(t, got, "banking.enable_banking.developer.app_id")
	wantProblem(t, got, "LEDGER_BANKING_ENABLE_BANKING_DEVELOPER_PRIVATE_KEY")
	_, err = load(t, okEnv(env{
		"LEDGER_BANKING_ENABLE_BANKING_APP_ENABLED": "true", "LEDGER_BANKING_ENABLE_BANKING_APP_APP_ID": "not-a-uuid",
		"LEDGER_BANKING_ENABLE_BANKING_APP_PRIVATE_KEY": sentinel,
	}), nil, nil, "")
	if err == nil || !strings.Contains(err.Error(), "app.app_id") {
		t.Fatalf("bad app id accepted: %v", err)
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatal("error shows the key")
	}
}

func TestEnableBankingBothModesFromEnvAndFile(t *testing.T) {
	res, err := load(t, okEnv(env{
		"LEDGER_BANKING_ENABLE_BANKING_APP_ENABLED":                "true",
		"LEDGER_BANKING_ENABLE_BANKING_APP_APP_ID":                 testEBAppID,
		"LEDGER_BANKING_ENABLE_BANKING_APP_PRIVATE_KEY":            sentinel + "-app",
		"LEDGER_BANKING_ENABLE_BANKING_DEVELOPER_ENABLED":          "true",
		"LEDGER_BANKING_ENABLE_BANKING_DEVELOPER_APP_ID":           testEBAppID,
		"LEDGER_BANKING_ENABLE_BANKING_DEVELOPER_ENVIRONMENT":      "sandbox",
		"LEDGER_BANKING_ENABLE_BANKING_DEVELOPER_PRIVATE_KEY_FILE": "/keys/dev.pem",
	}), files{"/keys/dev.pem": sentinel + "-dev\n"}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eb := res.Config.Banking.EnableBanking
	if eb.App.PrivateKey != sentinel+"-app" || eb.Developer.PrivateKey != sentinel+"-dev\n" || eb.Developer.Environment != "sandbox" {
		t.Fatalf("%+v", eb)
	}
	if res.Sources["banking.enable_banking.developer.private_key"] != "file /keys/dev.pem" {
		t.Fatalf("sources %v", res.Sources)
	}
}

func TestEnableBankingKeyProblems(t *testing.T) {
	base := env{
		"LEDGER_BANKING_ENABLE_BANKING_APP_ENABLED": "true", "LEDGER_BANKING_ENABLE_BANKING_APP_APP_ID": testEBAppID,
		"LEDGER_BANKING_ENABLE_BANKING_APP_PRIVATE_KEY_FILE": "/k.pem",
	}
	both := okEnv(base)
	both["LEDGER_BANKING_ENABLE_BANKING_APP_PRIVATE_KEY"] = sentinel
	_, err := load(t, both, files{"/k.pem": "x"}, nil, "")
	wantProblem(t, problems(t, err), "set only one of")
	_, err = load(t, okEnv(base), files{}, nil, "")
	wantProblem(t, problems(t, err), "cannot read the file")
	_, err = load(t, okEnv(base), files{"/k.pem": "  \n"}, nil, "")
	wantProblem(t, problems(t, err), "the file is empty")
}

func TestEnableBankingKeyNotInYAML(t *testing.T) {
	_, err := load(t, okEnv(nil), files{"c.yaml": "banking:\n  enable_banking:\n    app:\n      private_key: " + sentinel + "\n"}, nil, "c.yaml")
	if err == nil || strings.Contains(err.Error(), sentinel) {
		t.Fatalf("private key accepted in YAML or echoed: %v", err)
	}
}

func TestEnableBankingNamesAndBaseURL(t *testing.T) {
	e := okEnv(env{
		"LEDGER_BANKING_ENABLE_BANKING_APP_ENABLED": "true", "LEDGER_BANKING_ENABLE_BANKING_APP_APP_ID": testEBAppID,
		"LEDGER_BANKING_ENABLE_BANKING_APP_PRIVATE_KEY": sentinel, "LEDGER_BANKING_ENABLE_BANKING_APP_NAME": "main",
		"LEDGER_BANKING_ENABLE_BANKING_DEVELOPER_ENABLED": "true", "LEDGER_BANKING_ENABLE_BANKING_DEVELOPER_APP_ID": testEBAppID,
		"LEDGER_BANKING_ENABLE_BANKING_DEVELOPER_PRIVATE_KEY": sentinel, "LEDGER_BANKING_ENABLE_BANKING_DEVELOPER_NAME": "main",
		"LEDGER_BANKING_ENABLE_BANKING_BASE_URL": "http://api.example.com",
	})
	got := problems(t, load2(t, e))
	wantProblem(t, got, "names must differ")
	wantProblem(t, got, "banking.enable_banking.base_url")
}

func load2(t *testing.T, e env) error {
	t.Helper()
	_, err := load(t, e, nil, nil, "")
	return err
}
