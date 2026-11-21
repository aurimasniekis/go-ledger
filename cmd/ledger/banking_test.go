package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
)

const testEBAppID = "0f6c3c1e-1b2a-4c3d-9e8f-123456789abc"

func testPEM(t *testing.T) string {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	b, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: b}))
}

func TestBankingProvidersBothModes(t *testing.T) {
	key := testPEM(t)
	cfg := config.EnableBanking{
		BaseURL:   "https://api.enablebanking.com",
		App:       config.EnableBankingApp{Enabled: true, AppID: testEBAppID, Environment: "production", PrivateKey: key},
		Developer: config.EnableBankingApp{Enabled: true, Name: "own-accounts", AppID: testEBAppID, Environment: "sandbox", PrivateKey: key},
	}
	ps, err := bankingProviders(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 2 {
		t.Fatalf("%d providers", len(ps))
	}
	a, d := ps[0].Capabilities(), ps[1].Capabilities()
	if a.Application != "enablebanking-app" || a.Mode != banking.ModeApp || a.Environment != banking.EnvironmentProduction || a.AccountAccess() != banking.AccountAccessAll {
		t.Fatalf("app %+v", a)
	}
	if d.Application != "own-accounts" || d.Mode != banking.ModeDeveloper || d.Environment != banking.EnvironmentSandbox || d.AccountAccess() != banking.AccountAccessLinkedOnly {
		t.Fatalf("developer %+v", d)
	}
	none, err := bankingProviders(config.EnableBanking{})
	if err != nil || len(none) != 0 {
		t.Fatalf("disabled: %v %v", none, err)
	}
}

func TestServeRefusesABadEnableBankingKey(t *testing.T) {
	bogus := "-----BEGIN PRIVATE KEY-----\n" + cmdSentinel + "\n-----END PRIVATE KEY-----\n"
	env := map[string]string{
		"LEDGER_KEK_VALUE":                                    cmdSentinel,
		"LEDGER_DATABASE_PATH":                                filepath.Join(t.TempDir(), "ledger.db"),
		"LEDGER_BANKING_ENABLE_BANKING_DEVELOPER_ENABLED":     "true",
		"LEDGER_BANKING_ENABLE_BANKING_DEVELOPER_APP_ID":      testEBAppID,
		"LEDGER_BANKING_ENABLE_BANKING_DEVELOPER_PRIVATE_KEY": bogus,
	}
	var errOut syncBuffer
	code := runServe(context.Background(), []string{"--http.listen=127.0.0.1:0", "--http.management_listen="}, &errOut, serveEnv{Options: testOpts(env, nil)})
	out := errOut.String()
	if code != 1 || !strings.Contains(out, "banking.enable_banking.developer.private_key") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if strings.Contains(out, cmdSentinel) || strings.Contains(out, testEBAppID) {
		t.Fatalf("logs show the key or app id:\n%s", out)
	}
}
