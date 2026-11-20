package config

import (
	"strings"
	"testing"
)

func TestListenAndTrustedProxiesValidation(t *testing.T) {
	tests := []struct {
		name string
		env  env
		want []string // substrings of problems; none means valid
	}{
		{"defaults", nil, nil},
		{"all interfaces", env{"LEDGER_HTTP_LISTEN": ":8080"}, nil},
		{"ipv6", env{"LEDGER_HTTP_LISTEN": "[::1]:9000"}, nil},
		{"no port", env{"LEDGER_HTTP_LISTEN": "garbage"}, []string{"http.listen"}},
		{"bad port", env{"LEDGER_HTTP_LISTEN": "127.0.0.1:99999"}, []string{"http.listen"}},
		{"valid proxies", env{"LEDGER_HTTP_TRUSTED_PROXIES": "10.0.0.1, 192.168.0.0/16,::1,fd00::/8"}, nil},
		{"bad proxies reported together", env{"LEDGER_HTTP_LISTEN": "garbage", "LEDGER_HTTP_TRUSTED_PROXIES": "not-an-ip,10.0.0.1,999.1.1.1/77"},
			[]string{"http.listen", "entry 1", "entry 3"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := load(t, okEnv(tt.env), nil, nil, "")
			if len(tt.want) == 0 {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err, w)
				}
			}
		})
	}
}

func TestManagementListenValidationAndDefault(t *testing.T) {
	if res, err := load(t, okEnv(nil), nil, nil, ""); err != nil || res.Config.HTTP.ManagementListen != "127.0.0.1:9090" {
		t.Errorf("default: %v %q", err, res.Config.HTTP.ManagementListen)
	}
	if res, err := load(t, okEnv(nil), files{"c.yaml": "http:\n  management_listen: \"\"\n"}, nil, "c.yaml"); err != nil || res.Config.HTTP.ManagementListen != "" {
		t.Errorf("empty in YAML (a blank variable counts as unset): %v %q", err, res.Config.HTTP.ManagementListen)
	}
	if _, err := load(t, okEnv(env{"LEDGER_HTTP_MANAGEMENT_LISTEN": "nonsense"}), nil, nil, ""); err == nil || !strings.Contains(err.Error(), "http.management_listen") {
		t.Errorf("bad value accepted: %v", err)
	}
}

func TestDefaultsListenLoopbackAndLogJSON(t *testing.T) {
	res, err := load(t, okEnv(nil), nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.HTTP.Listen != "127.0.0.1:8080" || res.Config.Log.Format != "json" {
		t.Errorf("listen=%q format=%q", res.Config.HTTP.Listen, res.Config.Log.Format)
	}
}

func TestUnknownEnvNamesOnly(t *testing.T) {
	e := okEnv(env{"LEDGER_HTTP_LISTN": "secret-value-1", "OTHER": "x"})
	res, err := Load(Options{
		LookupEnv: e.lookup,
		ReadFile:  files{".env": "LEDGER_TYPO_IN_FILE=secret-value-2\nLEDGER_LOG_LEVEL=debug\n"}.read,
		Environ: func() []string {
			out := []string{"PATH=/bin", "LEDGER_CONFIG=x", "LEDGER_ENV_FILE=y"}
			for k, v := range e {
				out = append(out, k+"="+v)
			}
			return out
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(res.UnknownEnv, ",")
	if got != "LEDGER_HTTP_LISTN,LEDGER_TYPO_IN_FILE" {
		t.Errorf("UnknownEnv = %q", got)
	}
	if strings.Contains(got, "secret-value") {
		t.Error("values leaked")
	}
}
