package main

import (
	"bytes"
	"io/fs"
	"strings"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/config"
)

const cmdSentinel = "SENTINEL-cmd-secret-9876543210"

func testOpts(env, files map[string]string) config.Options {
	return config.Options{
		LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok },
		ReadFile: func(n string) ([]byte, error) {
			if s, ok := files[n]; ok {
				return []byte(s), nil
			}
			return nil, &fs.PathError{Op: "open", Path: n, Err: fs.ErrNotExist}
		},
	}
}

func TestConfigEnvPrintsEveryVariableWithoutSecrets(t *testing.T) {
	var out, errOut bytes.Buffer
	opts := testOpts(map[string]string{"LEDGER_KEK_VALUE": cmdSentinel}, nil)
	if code := runConfig([]string{"env"}, &out, &errOut, opts); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if strings.Contains(out.String(), cmdSentinel) {
		t.Fatal("config env leaked the secret")
	}
	for _, v := range config.Variables() {
		if !strings.Contains(out.String(), v.Name+"=") {
			t.Errorf("missing %s", v.Name)
		}
	}
	if !strings.Contains(out.String(), "LEDGER_HTTP_LISTEN=127.0.0.1:8080") {
		t.Error("default not shown")
	}
}

func TestConfigCheck(t *testing.T) {
	env := map[string]string{"LEDGER_KEK_VALUE": cmdSentinel, "LEDGER_LOG_LEVEL": "debug"}
	files := map[string]string{".env": "LEDGER_HTTP_LISTEN=:9\n", "c.yaml": "log:\n  format: json\n"}
	var out, errOut bytes.Buffer
	code := runConfig([]string{"check", "--config", "c.yaml", "--database.path=x.db"}, &out, &errOut, testOpts(env, files))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	got := out.String()
	for _, want := range []string{
		"configuration OK", "config file: c.yaml", "LEDGER_HTTP_LISTEN",
		"log.level = \"debug\"  [env LEDGER_LOG_LEVEL]",
		"log.format = \"json\"  [yaml]",
		"database.path = \"x.db\"  [flag --database.path]",
		"kek.value = <set>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got+errOut.String(), cmdSentinel) {
		t.Fatal("secret leaked")
	}
}

func TestConfigCheckFailsWithAllProblems(t *testing.T) {
	env := map[string]string{"LEDGER_LOG_LEVEL": "loud"}
	files := map[string]string{"c.yaml": "bogus: 1\n"}
	var out, errOut bytes.Buffer
	code := runConfig([]string{"check", "--config=c.yaml"}, &out, &errOut, testOpts(env, files))
	if code != 1 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"3 problems", "c.yaml:1: bogus: unknown key", "LEDGER_LOG_LEVEL", "kek: required"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr lacks %q:\n%s", want, errOut.String())
		}
	}
	if out.Len() != 0 {
		t.Errorf("stdout should be empty on failure: %q", out.String())
	}
}

func TestConfigCheckMalformedEnvFile(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runConfig([]string{"check"}, &out, &errOut, testOpts(nil, map[string]string{".env": "A=1\noops\n"}))
	if code != 1 || !strings.Contains(errOut.String(), ".env:2: expected KEY=VALUE") {
		t.Fatalf("exit %d, stderr %q", code, errOut.String())
	}
}

func TestConfigUsageErrors(t *testing.T) {
	for _, args := range [][]string{nil, {"bogus"}, {"env", "x"}, {"check", "extra"}, {"check", "--nope"}} {
		var out, errOut bytes.Buffer
		if code := runConfig(args, &out, &errOut, testOpts(nil, nil)); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}
