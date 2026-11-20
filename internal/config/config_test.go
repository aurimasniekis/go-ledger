package config

import (
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const sentinel = "SENTINEL-kek-do-not-leak-0123456789"

type env map[string]string

func (e env) lookup(k string) (string, bool) { v, ok := e[k]; return v, ok }

type files map[string]string

func (f files) read(name string) ([]byte, error) {
	if s, ok := f[name]; ok {
		return []byte(s), nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

func load(t *testing.T, e env, f files, flags map[string]string, cfgPath string) (*Result, error) {
	t.Helper()
	return Load(Options{ConfigPath: cfgPath, Flags: flags, LookupEnv: e.lookup, ReadFile: f.read})
}

func okEnv(extra env) env {
	e := env{"LEDGER_KEK_VALUE": sentinel}
	for k, v := range extra {
		e[k] = v
	}
	return e
}

func problems(t *testing.T, err error) []string {
	t.Helper()
	var ce *Error
	if !errors.As(err, &ce) {
		t.Fatalf("error = %v, want *config.Error", err)
	}
	var out []string
	for _, p := range ce.Problems {
		out = append(out, p.String())
	}
	return out
}

func wantProblem(t *testing.T, got []string, substrs ...string) {
	t.Helper()
	for _, s := range got {
		ok := true
		for _, sub := range substrs {
			ok = ok && strings.Contains(s, sub)
		}
		if ok {
			return
		}
	}
	t.Errorf("no problem contains all of %q; problems:\n%s", substrs, strings.Join(got, "\n"))
}

func TestDefaults(t *testing.T) {
	res, err := load(t, okEnv(nil), nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	c := res.Config
	if c.HTTP.Listen != ":8080" || c.HTTP.ReadTimeout != 30*time.Second || c.Log.Level != "info" || c.HTTP.MaxBodyBytes != 1<<20 {
		t.Errorf("unexpected defaults: %+v", c)
	}
	if res.Sources["http.listen"] != "default" || res.Sources["kek.value"] != "env LEDGER_KEK_VALUE" {
		t.Errorf("sources = %v", res.Sources)
	}
}

func TestPrecedence(t *testing.T) {
	yamlText := "http:\n  listen: \":1111\"\n  read_timeout: \"11s\"\n  idle_timeout: \"1m\"\nlog:\n  level: warn\n"
	tests := []struct {
		name    string
		env     env
		files   files
		flags   map[string]string
		listen  string
		readTO  time.Duration
		idleTO  time.Duration
		listenS string
	}{
		{"defaults only", nil, nil, nil, ":8080", 30 * time.Second, 120 * time.Second, "default"},
		{"yaml over default", nil, files{"c.yaml": yamlText}, nil, ":1111", 11 * time.Second, time.Minute, "yaml"},
		{"env over yaml", env{"LEDGER_HTTP_LISTEN": ":2222"}, files{"c.yaml": yamlText}, nil, ":2222", 11 * time.Second, time.Minute, "env LEDGER_HTTP_LISTEN"},
		{"flag over env", env{"LEDGER_HTTP_LISTEN": ":2222"}, files{"c.yaml": yamlText}, map[string]string{"http.listen": ":3333"}, ":3333", 11 * time.Second, time.Minute, "flag --http.listen"},
		{"env file over yaml", env{}, files{"c.yaml": yamlText, ".env": "LEDGER_HTTP_LISTEN=:4444\n"}, nil, ":4444", 11 * time.Second, time.Minute, "env LEDGER_HTTP_LISTEN"},
		{"real env over env file", env{"LEDGER_HTTP_LISTEN": ":5555"}, files{".env": "LEDGER_HTTP_LISTEN=:4444\n"}, nil, ":5555", 30 * time.Second, 120 * time.Second, "env LEDGER_HTTP_LISTEN"},
		{"empty env is unset", env{"LEDGER_HTTP_LISTEN": ""}, files{"c.yaml": yamlText}, nil, ":1111", 11 * time.Second, time.Minute, "yaml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := okEnv(tt.env)
			path := ""
			if tt.files["c.yaml"] != "" {
				path = "c.yaml"
			}
			res, err := load(t, e, tt.files, tt.flags, path)
			if err != nil {
				t.Fatal(err)
			}
			if res.Config.HTTP.Listen != tt.listen || res.Config.HTTP.ReadTimeout != tt.readTO || res.Config.HTTP.IdleTimeout != tt.idleTO {
				t.Errorf("got listen=%q read=%v idle=%v", res.Config.HTTP.Listen, res.Config.HTTP.ReadTimeout, res.Config.HTTP.IdleTimeout)
			}
			if got := res.Sources["http.listen"]; got != tt.listenS {
				t.Errorf("source = %q, want %q", got, tt.listenS)
			}
		})
	}
}

func TestConfigPathSources(t *testing.T) {
	f := files{"a.yaml": "log:\n  level: debug\n", "b.yaml": "log:\n  level: error\n"}
	res, err := load(t, okEnv(env{"LEDGER_CONFIG": "a.yaml"}), f, nil, "")
	if err != nil || res.Config.Log.Level != "debug" || !res.ConfigFound {
		t.Fatalf("LEDGER_CONFIG: %v %+v", err, res)
	}
	res, err = load(t, okEnv(env{"LEDGER_CONFIG": "a.yaml"}), f, nil, "b.yaml")
	if err != nil || res.Config.Log.Level != "error" {
		t.Fatalf("--config must win: %v", err)
	}
	// LEDGER_CONFIG may itself come from .env.
	res, err = load(t, okEnv(nil), files{".env": "LEDGER_CONFIG=a.yaml\n", "a.yaml": f["a.yaml"]}, nil, "")
	if err != nil || res.Config.Log.Level != "debug" {
		t.Fatalf(".env LEDGER_CONFIG: %v", err)
	}
}

func TestMissingYAMLAllowedWhenEnvSuppliesRequired(t *testing.T) {
	res, err := load(t, okEnv(nil), nil, nil, "absent.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if res.ConfigFound || res.ConfigFile != "absent.yaml" {
		t.Errorf("result = %+v", res)
	}
	_, err = load(t, env{}, nil, nil, "absent.yaml")
	wantProblem(t, problems(t, err), "kek", "required")
}

func TestYAMLProblems(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want []string
	}{
		{"unknown top-level key", "bogus: 1\n", []string{"c.yaml:1", "bogus", "unknown key"}},
		{"unknown nested key", "http:\n  lisen: \":1\"\n", []string{"c.yaml:2", "http.lisen", "unknown key"}},
		{"group is not a mapping", "http: 5\n", []string{"http", "expected a mapping"}},
		{"wrong type int for string", "http:\n  listen: 8080\n", []string{"c.yaml:2", "http.listen", "expected string, got an integer"}},
		{"wrong type string for int", "http:\n  max_body_bytes: lots\n", []string{"http.max_body_bytes", "expected integer, got a string"}},
		{"wrong type for bool-ish string", "log:\n  level: true\n", []string{"log.level", "expected string, got a boolean"}},
		{"duration needs a unit", "http:\n  read_timeout: 30\n", []string{"http.read_timeout", "expected a duration"}},
		{"bad duration string", "http:\n  read_timeout: soon\n", []string{"http.read_timeout", "invalid duration"}},
		{"list expected", "http:\n  trusted_proxies: 10.0.0.1\n", []string{"http.trusted_proxies", "expected list, got a string"}},
		{"list item type", "http:\n  trusted_proxies: [1]\n", []string{"http.trusted_proxies", "list of strings"}},
		{"enum", "log:\n  level: loud\n", []string{"log.level", "must be one of"}},
		{"duplicate key", "log:\n  level: info\n  level: warn\n", []string{"c.yaml:3", "log.level", "duplicate key"}},
		{"secret in yaml", "kek:\n  value: hunter2\n", []string{"kek.value", "secrets must not be written", "LEDGER_KEK_VALUE"}},
		{"syntax error", "http: [\n", []string{"c.yaml", "invalid YAML"}},
		{"two documents", "log: {}\n---\nlog: {}\n", []string{"only one YAML document"}},
		{"not a mapping", "- a\n", []string{"expected a mapping"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := load(t, okEnv(nil), files{"c.yaml": tt.yaml}, nil, "c.yaml")
			got := problems(t, err)
			wantProblem(t, got, tt.want...)
			if strings.Contains(strings.Join(got, "\n"), "hunter2") {
				t.Errorf("secret value leaked into problems: %v", got)
			}
		})
	}
}

func TestEmptyAndNullYAMLAreFine(t *testing.T) {
	for _, y := range []string{"", "# only a comment\n", "---\n", "http:\nlog:\n"} {
		if _, err := load(t, okEnv(nil), files{"c.yaml": y}, nil, "c.yaml"); err != nil {
			t.Errorf("%q: %v", y, err)
		}
	}
}

func TestMultipleProblemsReportedTogether(t *testing.T) {
	yamlText := "bogus: 1\nhttp:\n  listen: 8080\n  read_timeout: 30\nlog:\n  level: loud\n"
	e := okEnv(env{"LEDGER_DATABASE_BUSY_TIMEOUT": "later", "LEDGER_HTTP_MAX_BODY_BYTES": "x"})
	_, err := load(t, e, files{"c.yaml": yamlText}, map[string]string{"http.idle_timeout": "nope", "nonsense": "1"}, "c.yaml")
	got := problems(t, err)
	for _, want := range [][]string{
		{"bogus", "unknown key"}, {"http.listen", "expected string"}, {"http.read_timeout", "expected a duration"},
		{"log.level", "must be one of"}, {"LEDGER_DATABASE_BUSY_TIMEOUT", "invalid duration"},
		{"LEDGER_HTTP_MAX_BODY_BYTES", "invalid integer"}, {"--http.idle_timeout", "invalid duration"}, {"--nonsense", "no such flag"},
	} {
		wantProblem(t, got, want...)
	}
	if len(got) != 8 {
		t.Errorf("got %d problems, want 8:\n%s", len(got), strings.Join(got, "\n"))
	}
	if msg := err.Error(); !strings.Contains(msg, "8 problems") {
		t.Errorf("message does not count problems: %s", msg)
	}
}

func TestEnvTypes(t *testing.T) {
	e := okEnv(env{
		"LEDGER_HTTP_READ_TIMEOUT":    "1m30s",
		"LEDGER_HTTP_TRUSTED_PROXIES": " 10.0.0.1 , 192.168.0.0/16,,::1 ",
		"LEDGER_HTTP_MAX_BODY_BYTES":  "2048",
		"LEDGER_REPORTING_TIMEZONE":   "Europe/Vilnius",
		"LEDGER_LOG_FORMAT":           "json",
	})
	res, err := load(t, e, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	c := res.Config
	if c.HTTP.ReadTimeout != 90*time.Second || c.HTTP.MaxBodyBytes != 2048 || c.Log.Format != "json" || c.Reporting.Timezone != "Europe/Vilnius" {
		t.Errorf("config = %+v", c)
	}
	if want := []string{"10.0.0.1", "192.168.0.0/16", "::1"}; !reflect.DeepEqual(c.HTTP.TrustedProxies, want) {
		t.Errorf("list = %q, want %q", c.HTTP.TrustedProxies, want)
	}
}

func TestEnvAndFlagErrorsNameTheSource(t *testing.T) {
	tests := []struct {
		name  string
		env   env
		flags map[string]string
		want  []string
	}{
		{"duration", env{"LEDGER_HTTP_READ_TIMEOUT": "30"}, nil, []string{"env LEDGER_HTTP_READ_TIMEOUT", "http.read_timeout", "invalid duration"}},
		{"int", env{"LEDGER_HTTP_MAX_BODY_BYTES": "1.5"}, nil, []string{"LEDGER_HTTP_MAX_BODY_BYTES", "invalid integer"}},
		{"enum", env{"LEDGER_LOG_LEVEL": "loud"}, nil, []string{"LEDGER_LOG_LEVEL", "must be one of"}},
		{"flag", nil, map[string]string{"http.read_timeout": "x"}, []string{"flag --http.read_timeout", "invalid duration"}},
		{"secret has no flag", nil, map[string]string{"kek.value": "x"}, []string{"flag --kek.value", "no such flag"}},
		{"non-positive duration", env{"LEDGER_HTTP_READ_TIMEOUT": "0s"}, nil, []string{"LEDGER_HTTP_READ_TIMEOUT", "positive"}},
		{"bad timezone", env{"LEDGER_REPORTING_TIMEZONE": "Mars/Base"}, nil, []string{"reporting.timezone", "unknown IANA"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := load(t, okEnv(tt.env), nil, tt.flags, "")
			wantProblem(t, problems(t, err), tt.want...)
		})
	}
}

func TestSecrets(t *testing.T) {
	t.Run("from file, trailing newline trimmed", func(t *testing.T) {
		res, err := load(t, env{"LEDGER_KEK_FILE": "/run/kek"}, files{"/run/kek": sentinel + "\n"}, nil, "")
		if err != nil || res.Config.KEK.Value != sentinel {
			t.Fatalf("err=%v", err)
		}
		if res.Sources["kek.value"] != "file /run/kek" {
			t.Errorf("source = %q", res.Sources["kek.value"])
		}
	})
	t.Run("file key in yaml", func(t *testing.T) {
		res, err := load(t, env{}, files{"c.yaml": "kek:\n  file: /run/kek\n", "/run/kek": sentinel}, nil, "c.yaml")
		if err != nil || res.Config.KEK.Value != sentinel {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("both", func(t *testing.T) {
		_, err := load(t, env{"LEDGER_KEK_FILE": "/run/kek", "LEDGER_KEK_VALUE": sentinel}, files{"/run/kek": "x"}, nil, "")
		wantProblem(t, problems(t, err), "kek.file", "only one")
	})
	t.Run("unreadable and empty", func(t *testing.T) {
		_, err := load(t, env{"LEDGER_KEK_FILE": "/nope"}, nil, nil, "")
		wantProblem(t, problems(t, err), "kek.file", "cannot read")
		_, err = load(t, env{"LEDGER_KEK_FILE": "/run/kek"}, files{"/run/kek": "\n"}, nil, "")
		wantProblem(t, problems(t, err), "kek.file", "empty")
	})
	t.Run("never in errors", func(t *testing.T) {
		_, err := load(t, okEnv(env{"LEDGER_LOG_LEVEL": "loud"}), files{"c.yaml": "kek:\n  value: " + sentinel + "\n"}, nil, "c.yaml")
		if err == nil || strings.Contains(err.Error(), sentinel) {
			t.Fatalf("secret leaked or no error: %v", err)
		}
	})
}

func TestEnvFileWiring(t *testing.T) {
	f := files{".env": "export LEDGER_LOG_LEVEL=debug\nLEDGER_HTTP_LISTEN=:7000\nOTHER=1\n", "custom.env": "LEDGER_LOG_LEVEL=error\n"}
	res, err := load(t, okEnv(env{"LEDGER_HTTP_LISTEN": ":9000"}), f, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.Log.Level != "debug" || res.Config.HTTP.Listen != ":9000" {
		t.Errorf("config = %+v", res.Config)
	}
	// Names only, and only variables the file really contributed (not shadowed ones).
	if want := []string{"LEDGER_LOG_LEVEL", "OTHER"}; !reflect.DeepEqual(res.EnvLoaded, want) {
		t.Errorf("EnvLoaded = %v, want %v", res.EnvLoaded, want)
	}
	res, err = load(t, okEnv(env{"LEDGER_ENV_FILE": "custom.env"}), f, nil, "")
	if err != nil || res.Config.Log.Level != "error" || res.EnvFile != "custom.env" {
		t.Errorf("LEDGER_ENV_FILE: err=%v res=%+v", err, res)
	}
	res, err = load(t, okEnv(env{"LEDGER_ENV_FILE": "missing.env"}), f, nil, "")
	if err != nil || res.EnvFileFound {
		t.Errorf("missing env file must not be an error: %v", err)
	}
	_, err = load(t, okEnv(nil), files{".env": "A=1\nbroken line\n"}, nil, "")
	var de *DotenvError
	if !errors.As(err, &de) || de.File != ".env" || de.Line != 2 {
		t.Errorf("err = %v, want DotenvError at .env:2", err)
	}
}

func TestFlagsRegistration(t *testing.T) {
	fset := flag.NewFlagSet("x", flag.ContinueOnError)
	fl := RegisterFlags(fset)
	if err := fset.Parse([]string{"--config=c.yaml", "--http.listen", ":1", "--http.read_timeout=5s"}); err != nil {
		t.Fatal(err)
	}
	if fl.Config != "c.yaml" || fl.Values()["http.listen"] != ":1" || fl.Values()["http.read_timeout"] != "5s" {
		t.Errorf("flags = %+v %v", fl.Config, fl.Values())
	}
	if fset.Lookup("kek.value") != nil {
		t.Error("a secret must not have a flag")
	}
	if fset.Lookup("kek.file") == nil {
		t.Error("kek.file should have a flag")
	}
}

func TestEnvNameMapping(t *testing.T) {
	if got := EnvName("http.listen"); got != "LEDGER_HTTP_LISTEN" {
		t.Errorf("EnvName = %q", got)
	}
	seen := map[string]bool{}
	for _, f := range Fields() {
		if f.Env != EnvName(f.Path) || seen[f.Env] {
			t.Errorf("field %s: bad or duplicate variable %s", f.Path, f.Env)
		}
		seen[f.Env] = true
		if f.Desc == "" {
			t.Errorf("field %s has no description", f.Path)
		}
	}
}

func TestFieldDefinitionErrors(t *testing.T) {
	type noTag struct{ A string }
	type noDesc struct {
		A string `cfg:"a"`
	}
	type badType struct {
		A float64 `cfg:"a" desc:"x"`
	}
	type dup struct {
		A string `cfg:"a_b" desc:"x"`
		B struct {
			C string `cfg:"c" desc:"x"`
		} `cfg:"a"`
		D string `cfg:"a_b" desc:"x"`
	}
	type reserved struct {
		A string `cfg:"config" desc:"x"`
	}
	for name, typ := range map[string]reflect.Type{
		"no cfg tag": reflect.TypeOf(noTag{}), "no desc": reflect.TypeOf(noDesc{}), "unsupported type": reflect.TypeOf(badType{}),
		"duplicate": reflect.TypeOf(dup{}), "reserved": reflect.TypeOf(reserved{}),
	} {
		if _, err := fieldsOf(typ); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestConfigEnvOutputHasNoSecretValue(t *testing.T) {
	t.Setenv("LEDGER_KEK_VALUE", sentinel)
	t.Setenv("LEDGER_KEK_FILE", "/tmp/"+sentinel)
	out := ExampleEnv() + ExampleYAML()
	if strings.Contains(out, sentinel) {
		t.Fatal("generated documentation contains the sentinel secret")
	}
	for _, v := range Variables() {
		if !strings.Contains(out, v.Name+"=") {
			t.Errorf("variable %s missing from env output", v.Name)
		}
	}
}

// ---- examples stay in sync with the definition ----

func repoFile(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join("..", "..", name)
}

var update = flag.Bool("update", false, "rewrite config.example.yaml and .env.example from the definition")

func TestExamplesInSync(t *testing.T) {
	want := map[string]string{"config.example.yaml": ExampleYAML(), ".env.example": ExampleEnv()}
	for name, content := range want {
		path := repoFile(t, name)
		if *update {
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%v (run: make config-examples)", err)
		}
		if string(got) != content {
			t.Errorf("%s is out of date with the configuration definition; run: make config-examples", name)
		}
	}
	y, _ := os.ReadFile(repoFile(t, "config.example.yaml"))
	e, _ := os.ReadFile(repoFile(t, ".env.example"))
	if p := CheckExamples(Variables(), string(y), string(e)); len(p) > 0 {
		t.Errorf("examples incomplete: %v", p)
	}
}

func TestSyncCheckFailsWhenKeyAddedWithoutExamples(t *testing.T) {
	y, _ := os.ReadFile(repoFile(t, "config.example.yaml"))
	e, _ := os.ReadFile(repoFile(t, ".env.example"))
	vars := append(Variables(),
		Var{Name: "LEDGER_NEW_THING", Type: "string", Desc: "x", Key: "new.thing"},
		Var{Name: "LEDGER_NEW_SECRET", Type: "string", Desc: "x", Key: "kek.value", Secret: true},
	)
	got := strings.Join(CheckExamples(vars, string(y), string(e)), "\n")
	for _, want := range []string{".env.example has no entry for LEDGER_NEW_THING", "config.example.yaml has no entry for new.thing", "LEDGER_NEW_SECRET"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if len(CheckExamples(Variables(), string(y), string(e))) != 0 {
		t.Error("the committed examples should be complete")
	}
	// A key added to a struct is picked up with no further wiring.
	type extra struct {
		X struct {
			Y time.Duration `cfg:"y" default:"1s" desc:"new"`
		} `cfg:"x"`
	}
	fs, err := fieldsOf(reflect.TypeOf(extra{}))
	if err != nil || len(fs) != 1 || fs[0].Env != "LEDGER_X_Y" {
		t.Fatalf("fields = %+v err=%v", fs, err)
	}
	if !strings.Contains(exampleYAML(fs), "y: \"1s\"") || !strings.Contains(exampleEnv(variablesOf(fs)), "# LEDGER_X_Y=1s") {
		t.Error("generators did not pick up the new field")
	}
}

func TestExampleYAMLLoadsToDefaults(t *testing.T) {
	y, _ := os.ReadFile(repoFile(t, "config.example.yaml"))
	res, err := load(t, okEnv(nil), files{"c.yaml": string(y)}, nil, "c.yaml")
	if err != nil {
		t.Fatal(err)
	}
	ref, err := load(t, okEnv(nil), nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Config, ref.Config) {
		t.Errorf("example YAML differs from defaults:\n%+v\n%+v", res.Config, ref.Config)
	}
	e, _ := os.ReadFile(repoFile(t, ".env.example"))
	if _, err := ParseDotenv(".env.example", e); err != nil {
		t.Errorf(".env.example is not valid .env: %v", err)
	}
}
