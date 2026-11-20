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

func TestConfigCheckWarnsAboutUnknownVariablesOnSuccessAndFailure(t *testing.T) {
	environ := func() []string { return []string{"LEDGER_LOG_LEVL=debug", "PATH=/bin"} }

	opts := testOpts(map[string]string{"LEDGER_KEK_VALUE": cmdSentinel}, nil)
	opts.Environ = environ
	var out, errOut bytes.Buffer
	if code := runConfig([]string{"check"}, &out, &errOut, opts); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	const warning = "warning: unknown variable LEDGER_LOG_LEVL is ignored (check the spelling)"
	if !strings.Contains(out.String(), warning) {
		t.Errorf("success output lacks the warning:\n%s", out.String())
	}

	// Validation fails (no KEK): the warning is still printed, before the error.
	opts = testOpts(nil, nil)
	opts.Environ = environ
	out.Reset()
	errOut.Reset()
	if code := runConfig([]string{"check"}, &out, &errOut, opts); code != 1 {
		t.Fatalf("exit %d", code)
	}
	w, e := strings.Index(errOut.String(), warning), strings.Index(errOut.String(), "kek: required")
	if w < 0 || e < 0 || w > e {
		t.Errorf("want the warning before the error:\n%s", errOut.String())
	}
}

func TestConfigCheckUsageShowsDefaults(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runConfig([]string{"check", "-h"}, &out, &errOut, testOpts(nil, nil)); code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"(default 127.0.0.1:8080)", "(default 15s)", "(default json)", "(default info)"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("usage lacks %q:\n%s", want, errOut.String())
		}
	}
}
