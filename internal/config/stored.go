package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The stored settings layer (ADR-0066). Runtime settings written through the
// Management API are kept in the database and form one more layer of the
// configuration, between the built-in defaults and the YAML file. A key set by
// any configuration layer (YAML, the environment, the .env file, a flag or a
// secret file) is pinned: its stored value, if any, is kept but not used.

// Layer names, as the settings API reports them.
const (
	LayerDefault     = "default"
	LayerAPI         = "api"
	LayerYAML        = "yaml"
	LayerEnvironment = "environment"
	LayerDotenv      = "dotenv"
	LayerFlag        = "flag"
	LayerFile        = "file"
)

// Layer reduces a source recorded in [Result.Sources] to its layer name:
// "default", "api", "yaml", "environment", "dotenv", "flag" or "file". The
// rest of the source (a variable name or a file path) is left out.
func Layer(source string) string {
	head, _, _ := strings.Cut(source, " ")
	switch head {
	case "env":
		return LayerEnvironment
	case "dotenv", "flag", "file", "yaml", "api", "default":
		return head
	}
	return LayerDefault
}

// Pinned reports whether a source is a configuration layer, which pins the key
// against writes through the API (ADR-0066 point 5).
func Pinned(source string) bool {
	switch Layer(source) {
	case LayerYAML, LayerEnvironment, LayerDotenv, LayerFlag, LayerFile:
		return true
	}
	return false
}

// FieldByPath returns the field with the dotted key path.
func FieldByPath(path string) (Field, bool) {
	for _, f := range fields() {
		if f.Path == path {
			return f, true
		}
	}
	return Field{}, false
}

// NormalizeStored parses a stored value of the key path and returns it in the
// one canonical JSON form the stored layer keeps (durations as Go duration
// strings, lists as arrays of strings). The error never repeats a secret.
// Secrets are never stored in this layer (ADR-0066 point 12 keeps them apart).
func NormalizeStored(path string, raw json.RawMessage) (json.RawMessage, error) {
	f, ok := FieldByPath(path)
	if !ok {
		return nil, errors.New("no such key")
	}
	if f.Secret {
		return nil, errors.New("a secret is not kept in the stored settings layer")
	}
	v, err := fromJSON(f, raw)
	if err != nil {
		return nil, errors.New(hideSecret(f, err))
	}
	return encodeStored(v)
}

// StoredValue returns the canonical stored form of the field's value in c, the
// form [NormalizeStored] returns. It is meant for comparing values; for a
// secret it returns an error and no value.
func StoredValue(f Field, c *Config) (json.RawMessage, error) {
	if f.Secret {
		return nil, errors.New("a secret is not kept in the stored settings layer")
	}
	return encodeStored(f.Value(c))
}

func encodeStored(v any) (json.RawMessage, error) {
	switch x := v.(type) {
	case time.Duration:
		return json.Marshal(x.String())
	case []string:
		if x == nil {
			x = []string{}
		}
		return json.Marshal(x)
	default:
		return json.Marshal(x)
	}
}

// fromJSON parses the JSON form of a value: a string for strings and
// durations (written with a unit, like "30s"), a boolean, an integer, or a
// list of strings.
func fromJSON(f Field, raw json.RawMessage) (any, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, fmt.Errorf("expected %s, got null", f.TypeName())
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("expected %s, got invalid JSON", f.TypeName())
	}
	if dec.More() {
		return nil, fmt.Errorf("expected %s, got more than one JSON value", f.TypeName())
	}
	var out any
	switch {
	case f.Type == durationType:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("expected a duration written like \"30s\", got %s", describeJSON(v))
		}
		d, err := fromString(f, s)
		if err != nil {
			return nil, err
		}
		out = d
	case f.Type.Kind() == reflect.Bool:
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("expected a boolean, got %s", describeJSON(v))
		}
		out = b
	case f.Type.Kind() == reflect.Int, f.Type.Kind() == reflect.Int64:
		n, ok := v.(json.Number)
		if !ok {
			return nil, fmt.Errorf("expected an integer, got %s", describeJSON(v))
		}
		i, err := strconv.ParseInt(n.String(), 10, 64)
		if err != nil {
			return nil, errors.New("expected an integer, got a number that is not a whole number in range")
		}
		out = i
	case f.Type.Kind() == reflect.Slice:
		items, ok := v.([]any)
		if !ok {
			return nil, fmt.Errorf("expected a list of strings, got %s", describeJSON(v))
		}
		list := make([]string, 0, len(items))
		for _, it := range items {
			s, ok := it.(string)
			if !ok {
				return nil, fmt.Errorf("expected a list of strings, got an item that is %s", describeJSON(it))
			}
			list = append(list, s)
		}
		out = list
	default:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("expected a string, got %s", describeJSON(v))
		}
		out = s
	}
	if err := checkEnum(f, out); err != nil {
		return nil, err
	}
	return out, nil
}

func describeJSON(v any) string {
	switch v.(type) {
	case string:
		return "a string"
	case bool:
		return "a boolean"
	case json.Number:
		return "a number"
	case []any:
		return "a list"
	case map[string]any:
		return "an object"
	case nil:
		return "null"
	}
	return "another type"
}

// applyStored assigns the stored layer over the defaults and records "api" as
// the source of each stored key.
func applyStored(target reflect.Value, byPath map[string]Field, stored map[string]json.RawMessage, sources map[string]string) []Problem {
	var problems []Problem
	for _, path := range sortedKeys(stored) {
		f, ok := byPath[path]
		switch {
		case !ok:
			problems = append(problems, Problem{Source: LayerAPI, Key: path, Msg: "stored setting names no such key"})
			continue
		case f.Secret:
			problems = append(problems, Problem{Source: LayerAPI, Key: path, Msg: "a secret is not kept in the stored settings layer"})
			continue
		}
		v, err := fromJSON(f, stored[path])
		if err != nil {
			problems = append(problems, Problem{Source: LayerAPI, Key: path, Msg: "stored value: " + hideSecret(f, err)})
			continue
		}
		assign(target, f, v)
		sources[path] = LayerAPI
	}
	return problems
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// WithStored returns the configuration that results from replacing the stored
// settings layer of r with stored, validated with the same rules as [Load].
// Pinned keys (set by a configuration layer) keep their configured value;
// every other key takes its stored value, else its default. The configuration
// files, the environment and the secret files are not read again: r already
// holds what they gave. r is not changed.
//
// A write through the settings API builds the would-be configuration with this
// and commits only when it returns no error (ADR-0066 point 10).
func (r *Result) WithStored(stored map[string]json.RawMessage) (*Result, error) {
	out := *r
	out.Config = r.Config.Clone()
	out.Sources = make(map[string]string, len(r.Sources))
	for k, v := range r.Sources {
		out.Sources[k] = v
	}
	target := reflect.ValueOf(&out.Config).Elem()
	all := fields()
	byPath := make(map[string]Field, len(all))
	unpinned := map[string]Field{}
	for _, f := range all {
		byPath[f.Path] = f
		if Pinned(out.Sources[f.Path]) {
			continue
		}
		unpinned[f.Path] = f
		resetToDefault(target, f)
		out.Sources[f.Path] = LayerDefault
	}
	// Stored values of pinned keys are kept by the caller but not applied
	// (ADR-0066 point 6); they are still checked, so a bad one is reported.
	layer := make(map[string]json.RawMessage, len(stored))
	var problems []Problem
	for path, raw := range stored {
		if f, ok := byPath[path]; ok && !f.Secret {
			if _, free := unpinned[path]; !free {
				if _, err := fromJSON(f, raw); err != nil {
					problems = append(problems, Problem{Source: LayerAPI, Key: path, Msg: "stored value: " + err.Error()})
				}
				continue
			}
		}
		layer[path] = raw
	}
	problems = append(problems, applyStored(target, byPath, layer, out.Sources)...)
	problems = append(problems, out.Config.validate(out.Sources)...)
	if len(problems) > 0 {
		return nil, &Error{Problems: problems}
	}
	return &out, nil
}

func resetToDefault(target reflect.Value, f Field) {
	if f.Default == "" {
		dst := target.FieldByIndex(f.index)
		dst.Set(reflect.Zero(dst.Type()))
		return
	}
	v, err := fromString(f, f.Default)
	if err != nil {
		panic(fmt.Sprintf("config: default of %s is invalid: %v", f.Path, err))
	}
	assign(target, f, v)
}

// Clone returns a deep copy of c: the lists are copied too, so a snapshot that
// is shared between goroutines is never changed through another copy.
func (c Config) Clone() Config {
	out := c
	root := reflect.ValueOf(&out).Elem()
	for _, f := range fields() {
		dst := root.FieldByIndex(f.index)
		if dst.Kind() == reflect.Slice && !dst.IsNil() {
			cp := reflect.MakeSlice(dst.Type(), dst.Len(), dst.Len())
			reflect.Copy(cp, dst)
			dst.Set(cp)
		}
	}
	return out
}
