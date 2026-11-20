package config

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"
)

// Field describes one leaf key of the configuration.
type Field struct {
	Path    string // dotted YAML key, for example "http.listen"
	Env     string // environment variable, for example "LEDGER_HTTP_LISTEN"
	Default string // default in environment-variable syntax; "" means none
	Desc    string
	Secret  bool
	Enum    []string
	Type    reflect.Type
	index   []int
}

var durationType = reflect.TypeOf(time.Duration(0))

// EnvName maps a dotted key to its environment variable: dots become
// underscores and the result is upper-cased under Prefix.
func EnvName(path string) string {
	return Prefix + strings.ToUpper(strings.ReplaceAll(path, ".", "_"))
}

// TypeName is the documentation name of the field's type.
func (f Field) TypeName() string {
	switch {
	case f.Type == durationType:
		return "duration"
	case f.Type.Kind() == reflect.Slice:
		return "list"
	case f.Type.Kind() == reflect.Int, f.Type.Kind() == reflect.Int64:
		return "integer"
	case f.Type.Kind() == reflect.Bool:
		return "boolean"
	default:
		return "string"
	}
}

// Value returns the field's current value in c.
func (f Field) Value(c *Config) any {
	return reflect.ValueOf(c).Elem().FieldByIndex(f.index).Interface()
}

// FlagName returns the command-line flag for the field, or "" when it has none
// because it is a secret.
func (f Field) FlagName() string {
	if f.Secret {
		return ""
	}
	return f.Path
}

var fields = sync.OnceValue(func() []Field {
	fs, err := fieldsOf(reflect.TypeOf(Config{}))
	if err != nil {
		panic("config: invalid Config definition: " + err.Error())
	}
	return fs
})

// Fields returns every leaf key in declaration order.
func Fields() []Field { return append([]Field(nil), fields()...) }

func fieldsOf(t reflect.Type) ([]Field, error) {
	var out []Field
	if err := walkType(t, "", nil, &out); err != nil {
		return nil, err
	}
	seenPath, seenEnv := map[string]bool{}, map[string]bool{}
	for _, f := range out {
		if seenPath[f.Path] || seenEnv[f.Env] {
			return nil, fmt.Errorf("duplicate key or variable for %q (%s)", f.Path, f.Env)
		}
		seenPath[f.Path], seenEnv[f.Env] = true, true
		if f.Env == EnvName("config") || f.Env == EnvName("env_file") {
			return nil, fmt.Errorf("%s is reserved", f.Env)
		}
	}
	return out, nil
}

func walkType(t reflect.Type, prefix string, index []int, out *[]Field) error {
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		seg := sf.Tag.Get("cfg")
		if seg == "" {
			return fmt.Errorf("field %s has no cfg tag", sf.Name)
		}
		path := prefix + seg
		idx := append(append([]int(nil), index...), i)
		if sf.Type.Kind() == reflect.Struct {
			if err := walkType(sf.Type, path+".", idx, out); err != nil {
				return err
			}
			continue
		}
		f := Field{
			Path: path, Env: EnvName(path), Default: sf.Tag.Get("default"),
			Desc: sf.Tag.Get("desc"), Secret: sf.Tag.Get("secret") == "true",
			Type: sf.Type, index: idx,
		}
		if e := sf.Tag.Get("enum"); e != "" {
			f.Enum = strings.Split(e, ",")
		}
		if f.Desc == "" {
			return fmt.Errorf("field %s has no desc tag", path)
		}
		switch {
		case sf.Type == durationType, sf.Type.Kind() == reflect.String, sf.Type.Kind() == reflect.Bool,
			sf.Type.Kind() == reflect.Int, sf.Type.Kind() == reflect.Int64,
			sf.Type.Kind() == reflect.Slice && sf.Type.Elem().Kind() == reflect.String:
		default:
			return fmt.Errorf("field %s has unsupported type %s", path, sf.Type)
		}
		*out = append(*out, f)
	}
	return nil
}
