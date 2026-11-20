package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Problem is one thing wrong with the configuration. Source says where the bad
// value came from (a YAML position, a variable, a flag).
type Problem struct{ Source, Key, Msg string }

func (p Problem) String() string {
	var parts []string
	for _, s := range []string{p.Source, p.Key} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(append(parts, p.Msg), ": ")
}

// Error reports every problem found, not just the first.
type Error struct{ Problems []Problem }

func (e *Error) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "invalid configuration (%d problem", len(e.Problems))
	if len(e.Problems) != 1 {
		b.WriteString("s")
	}
	b.WriteString("):")
	for _, p := range e.Problems {
		b.WriteString("\n  - " + p.String())
	}
	return b.String()
}

// Options are the inputs of Load. The zero value reads the process
// environment and the real file system.
type Options struct {
	// ConfigPath is the --config flag; it beats LEDGER_CONFIG.
	ConfigPath string
	// Flags maps a dotted key to the raw value of its command-line flag.
	Flags map[string]string
	// LookupEnv defaults to os.LookupEnv. ReadFile defaults to os.ReadFile.
	LookupEnv func(string) (string, bool)
	ReadFile  func(string) ([]byte, error)
	// Environ lists the process environment as KEY=VALUE, to find unknown
	// LEDGER_ variables. It defaults to os.Environ.
	Environ func() []string
}

// Result is a loaded configuration and how it came to be.
type Result struct {
	Config Config
	// Sources maps each key to where its value came from: "default", "yaml",
	// "env LEDGER_X", "flag --x" or "file <path>".
	Sources map[string]string
	// ConfigFile is the YAML path in use ("" when none was named) and
	// ConfigFound whether it existed.
	ConfigFile  string
	ConfigFound bool
	// EnvFile is the .env path that was read, EnvFileFound whether it
	// existed, and EnvLoaded the names (never values) it contributed.
	EnvFile      string
	EnvFileFound bool
	EnvLoaded    []string
	// UnknownEnv names (never values) the LEDGER_ variables, from the
	// environment or the .env file, that no key uses: usually typos.
	UnknownEnv []string
}

// unknownEnv returns the sorted names with the LEDGER_ prefix that no key or
// setting uses.
func unknownEnv(environ func() []string, pairs []EnvPair) []string {
	if environ == nil {
		environ = os.Environ
	}
	known := map[string]bool{Prefix + "CONFIG": true, Prefix + "ENV_FILE": true}
	for _, f := range fields() {
		known[f.Env] = true
	}
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if strings.HasPrefix(name, Prefix) && !known[name] && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	for _, kv := range environ() {
		name, _, _ := strings.Cut(kv, "=")
		add(name)
	}
	for _, p := range pairs {
		add(p.Key)
	}
	sort.Strings(out)
	return out
}

// Load builds the configuration. Precedence, highest first: flags, environment
// (the real environment over the .env file), YAML file, defaults. Every
// problem found is reported together in one *Error.
func Load(opts Options) (*Result, error) {
	lookup, readFile := opts.LookupEnv, opts.ReadFile
	if lookup == nil {
		lookup = os.LookupEnv
	}
	if readFile == nil {
		readFile = os.ReadFile
	}
	res := &Result{Sources: map[string]string{}}

	// .env first, so its values take part in the normal precedence. The real
	// environment always wins, and only the real one names the file.
	res.EnvFile = ".env"
	if v, ok := lookup(Prefix + "ENV_FILE"); ok && v != "" {
		res.EnvFile = v
	}
	pairs, found, err := loadDotenv(readFile, res.EnvFile)
	if err != nil {
		return nil, err
	}
	res.EnvFileFound = found
	fromFile := map[string]string{}
	for _, p := range pairs {
		if _, real := lookup(p.Key); real {
			continue
		}
		fromFile[p.Key] = p.Value
		res.EnvLoaded = append(res.EnvLoaded, p.Key)
	}
	res.UnknownEnv = unknownEnv(opts.Environ, pairs)
	// Empty means unset, so a blank "LEDGER_X=" never overrides a lower layer.
	envValue := func(k string) (string, bool) {
		v, ok := lookup(k)
		if !ok {
			v, ok = fromFile[k]
		}
		return v, ok && v != ""
	}

	res.ConfigFile = opts.ConfigPath
	if res.ConfigFile == "" {
		res.ConfigFile, _ = envValue(Prefix + "CONFIG")
	}

	var problems []Problem
	target := reflect.ValueOf(&res.Config).Elem()
	all := fields()
	byPath := map[string]Field{}

	// Layer 1: defaults.
	for _, f := range all {
		byPath[f.Path] = f
		res.Sources[f.Path] = "default"
		if f.Default == "" {
			continue
		}
		v, err := fromString(f, f.Default)
		if err != nil {
			panic(fmt.Sprintf("config: default of %s is invalid: %v", f.Path, err))
		}
		assign(target, f, v)
	}

	// Layer 2: the YAML file.
	if res.ConfigFile != "" {
		data, err := readFile(res.ConfigFile)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			problems = append(problems, Problem{Source: res.ConfigFile, Msg: fmt.Sprintf("cannot read the config file: %v", pathErr(err))})
		default:
			res.ConfigFound = true
			problems = append(problems, applyYAML(res.ConfigFile, data, all, target, res.Sources)...)
		}
	}

	// Layers 3 and 4: environment, then flags.
	set := func(f Field, src, raw string) {
		v, err := fromString(f, raw)
		if err == nil {
			err = checkEnum(f, v)
		}
		if err != nil {
			problems = append(problems, Problem{Source: src, Key: f.Path, Msg: hideSecret(f, err)})
			return
		}
		assign(target, f, v)
		res.Sources[f.Path] = src
	}
	for _, f := range all {
		if raw, ok := envValue(f.Env); ok {
			set(f, "env "+f.Env, raw)
		}
		if raw, ok := opts.Flags[f.Path]; ok && !f.Secret {
			set(f, "flag --"+f.Path, raw)
		}
	}
	for k := range opts.Flags {
		if f, ok := byPath[k]; !ok || f.Secret {
			problems = append(problems, Problem{Source: "flag --" + k, Msg: "no such flag"})
		}
	}

	problems = append(problems, resolveSecretFiles(&res.Config, res.Sources, readFile)...)
	problems = append(problems, res.Config.validate(res.Sources)...)
	if len(problems) > 0 {
		return nil, &Error{Problems: problems}
	}
	return res, nil
}

// hideSecret keeps a secret's value out of a parse error.
func hideSecret(f Field, err error) string {
	if f.Secret {
		return "invalid value (hidden)"
	}
	return err.Error()
}

func checkEnum(f Field, v any) error {
	if len(f.Enum) == 0 {
		return nil
	}
	s, _ := v.(string)
	for _, e := range f.Enum {
		if s == e {
			return nil
		}
	}
	return fmt.Errorf("must be one of %s, got %q", strings.Join(f.Enum, ", "), s)
}

// fromString parses environment, flag and default syntax.
func fromString(f Field, raw string) (any, error) {
	switch {
	case f.Type == durationType:
		d, err := time.ParseDuration(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("invalid duration %q (use a unit, for example 30s or 5m)", raw)
		}
		return d, nil
	case f.Type.Kind() == reflect.Bool:
		b, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("invalid boolean %q (use true or false)", raw)
		}
		return b, nil
	case f.Type.Kind() == reflect.Int, f.Type.Kind() == reflect.Int64:
		n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid integer %q", raw)
		}
		return n, nil
	case f.Type.Kind() == reflect.Slice:
		var out []string
		for _, part := range strings.Split(raw, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
		return out, nil
	default:
		return raw, nil
	}
}

func assign(root reflect.Value, f Field, v any) {
	dst := root.FieldByIndex(f.index)
	switch x := v.(type) {
	case time.Duration:
		dst.SetInt(int64(x))
	case int64:
		dst.SetInt(x)
	case bool:
		dst.SetBool(x)
	case string:
		dst.SetString(x)
	case []string:
		dst.Set(reflect.ValueOf(x))
	}
}

// ---- YAML ----

type yamlNode struct {
	field    *Field
	children map[string]*yamlNode
}

func buildTree(all []Field) *yamlNode {
	root := &yamlNode{children: map[string]*yamlNode{}}
	for i := range all {
		n := root
		segs := strings.Split(all[i].Path, ".")
		for j, s := range segs {
			c, ok := n.children[s]
			if !ok {
				c = &yamlNode{children: map[string]*yamlNode{}}
				n.children[s] = c
			}
			if j == len(segs)-1 {
				c.field = &all[i]
			}
			n = c
		}
	}
	return root
}

func resolveAlias(n *yaml.Node) *yaml.Node {
	for n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	return n
}

func applyYAML(name string, data []byte, all []Field, target reflect.Value, sources map[string]string) []Problem {
	var problems []Problem
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil // an empty file is fine
		}
		return []Problem{{Source: name, Msg: "invalid YAML: " + strings.TrimPrefix(err.Error(), "yaml: ")}}
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		problems = append(problems, Problem{Source: fmt.Sprintf("%s:%d", name, extra.Line), Msg: "only one YAML document is allowed"})
	}
	top := &doc
	if top.Kind == yaml.DocumentNode && len(top.Content) == 1 {
		top = top.Content[0]
	}
	if top.Tag == "!!null" {
		return problems
	}
	var walk func(n *yaml.Node, tree *yamlNode, prefix string)
	walk = func(n *yaml.Node, tree *yamlNode, prefix string) {
		n = resolveAlias(n)
		if n.Kind != yaml.MappingNode {
			problems = append(problems, Problem{fmt.Sprintf("%s:%d", name, n.Line), strings.TrimSuffix(prefix, "."), "expected a mapping of keys"})
			return
		}
		seen := map[string]bool{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], resolveAlias(n.Content[i+1])
			kpos := fmt.Sprintf("%s:%d", name, k.Line)
			vpos := fmt.Sprintf("%s:%d", name, v.Line)
			if k.Kind != yaml.ScalarNode {
				problems = append(problems, Problem{kpos, "", "keys must be plain strings"})
				continue
			}
			path := prefix + k.Value
			if seen[k.Value] {
				problems = append(problems, Problem{kpos, path, "duplicate key"})
				continue
			}
			seen[k.Value] = true
			child, ok := tree.children[k.Value]
			if !ok {
				problems = append(problems, Problem{kpos, path, "unknown key"})
				continue
			}
			if child.field == nil {
				if v.Tag != "!!null" {
					walk(v, child, path+".")
				}
				continue
			}
			f := *child.field
			if f.Secret {
				problems = append(problems, Problem{vpos, path, fmt.Sprintf("secrets must not be written in the config file; set %s instead", f.Env)})
				continue
			}
			if v.Tag == "!!null" {
				continue
			}
			val, err := fromNode(f, v)
			if err == nil {
				err = checkEnum(f, val)
			}
			if err != nil {
				problems = append(problems, Problem{vpos, path, err.Error()})
				continue
			}
			assign(target, f, val)
			sources[f.Path] = "yaml"
		}
	}
	walk(top, buildTree(all), "")
	return problems
}

func fromNode(f Field, n *yaml.Node) (any, error) {
	wrong := func() error { return fmt.Errorf("expected %s, got %s", f.TypeName(), describeNode(n)) }
	switch {
	case f.Type.Kind() == reflect.Slice:
		if n.Kind != yaml.SequenceNode {
			return nil, wrong()
		}
		var out []string
		for _, item := range n.Content {
			if item.Kind != yaml.ScalarNode || item.ShortTag() != "!!str" {
				return nil, fmt.Errorf("expected a list of strings, got an item that is %s", describeNode(item))
			}
			out = append(out, item.Value)
		}
		return out, nil
	case n.Kind != yaml.ScalarNode:
		return nil, wrong()
	case f.Type == durationType:
		if n.ShortTag() != "!!str" {
			return nil, fmt.Errorf("expected a duration written like \"30s\", got %s", describeNode(n))
		}
		return fromString(f, n.Value)
	case f.Type.Kind() == reflect.Bool:
		if n.ShortTag() != "!!bool" {
			return nil, wrong()
		}
		return fromString(f, n.Value)
	case f.Type.Kind() == reflect.Int, f.Type.Kind() == reflect.Int64:
		if n.ShortTag() != "!!int" {
			return nil, wrong()
		}
		return fromString(f, n.Value)
	default:
		if n.ShortTag() != "!!str" {
			return nil, wrong()
		}
		return n.Value, nil
	}
}

func describeNode(n *yaml.Node) string {
	switch n.Kind {
	case yaml.MappingNode:
		return "a mapping"
	case yaml.SequenceNode:
		return "a list"
	}
	switch n.ShortTag() {
	case "!!str":
		return "a string"
	case "!!int":
		return "an integer"
	case "!!float":
		return "a number"
	case "!!bool":
		return "a boolean"
	case "!!timestamp":
		return "a timestamp"
	}
	return "another type"
}
