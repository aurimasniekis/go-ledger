package config

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Var documents one environment variable.
type Var struct {
	Name, Type, Default, Desc string
	Secret                    bool
	Key                       string // YAML key; "" for variables that are not config keys
	Flag                      string // command-line flag; "" when there is none
}

// Variables lists every environment variable the application reads: the two
// that locate files, then one per configuration key.
func Variables() []Var { return variablesOf(fields()) }

func variablesOf(fs []Field) []Var {
	out := []Var{
		{Name: Prefix + "CONFIG", Type: "string", Flag: "config",
			Desc: "Path of the YAML config file. The --config flag wins. A missing file is allowed when the environment supplies every required value."},
		{Name: Prefix + "ENV_FILE", Type: "string", Default: ".env",
			Desc: "Path of the .env file to load before configuration is parsed. A missing file is not an error. Only effective in the real environment, not inside a .env file."},
	}
	for _, f := range fs {
		out = append(out, Var{Name: f.Env, Type: f.TypeName(), Default: f.Default, Desc: f.Desc, Secret: f.Secret, Key: f.Path, Flag: f.FlagName()})
	}
	return out
}

// ExampleEnv renders the commented environment reference. It is the output of
// "ledger config env" and the content of .env.example. It shows defaults only,
// never a configured value, so secrets cannot appear in it.
func ExampleEnv() string { return exampleEnv(Variables()) }

func exampleEnv(vars []Var) string {
	var b strings.Builder
	b.WriteString(`# Ledger environment variables.
# Generated from the configuration definition by "ledger config env"; do not
# edit by hand. Regenerate with: make config-examples
#
# Copy this file to .env for local development. A real environment variable
# always wins over .env. Every line below is commented out and shows the
# built-in default: uncomment a line to override it. Precedence, highest first:
# command-line flags, environment variables, the YAML file, defaults.
# Lists are comma separated, durations look like 30s. Never commit a real
# secret.
`)
	for _, v := range vars {
		b.WriteString("\n")
		for _, l := range wrap(v.Desc, 76) {
			b.WriteString("# " + l + "\n")
		}
		meta := "Type: " + v.Type + "."
		if v.Default != "" {
			meta += " Default: " + v.Default + "."
		}
		if v.Secret {
			meta += " SECRET: the value is never printed or logged."
		}
		if v.Key != "" {
			meta += " YAML key: " + v.Key + "."
		} else {
			meta += " Not a YAML key."
		}
		if v.Flag != "" {
			meta += " Flag: --" + v.Flag + "."
		}
		for _, l := range wrap(meta, 76) {
			b.WriteString("# " + l + "\n")
		}
		b.WriteString("# " + v.Name + "=" + v.Default + "\n")
	}
	return b.String()
}

// ExampleYAML renders config.example.yaml: every non-secret key with its
// default, fully commented. Secrets are described but never given a key.
func ExampleYAML() string { return exampleYAML(fields()) }

func exampleYAML(fs []Field) string {
	var b strings.Builder
	b.WriteString(`# Ledger configuration file.
# Generated from the configuration definition; do not edit by hand.
# Regenerate with: make config-examples
#
# Pass it with --config or LEDGER_CONFIG. Every key can also be set with an
# environment variable (LEDGER_ plus the dotted key in upper case with dots
# turned into underscores) and, except secrets, a flag of the same name as the
# key. Precedence, highest first: flags, environment variables, this file,
# built-in defaults. Unknown keys and wrong types are rejected. Durations are
# written like "30s". Secrets never go in this file.
`)
	var open []string
	for _, f := range fs {
		segs := strings.Split(f.Path, ".")
		parents, leaf := segs[:len(segs)-1], segs[len(segs)-1]
		if f.Secret {
			// Describe it where it belongs, as a comment only.
			b.WriteString("\n")
			for _, l := range wrap("Secret "+f.Path+": "+f.Desc, 74) {
				b.WriteString(strings.Repeat("  ", len(parents)) + "# " + l + "\n")
			}
			continue
		}
		common := 0
		for common < len(open) && common < len(parents) && open[common] == parents[common] {
			common++
		}
		open = open[:common]
		for _, p := range parents[common:] {
			b.WriteString("\n" + strings.Repeat("  ", len(open)) + p + ":\n")
			open = append(open, p)
		}
		ind := strings.Repeat("  ", len(parents))
		b.WriteString("\n")
		for _, l := range wrap(f.Desc, 76-len(ind)) {
			b.WriteString(ind + "# " + l + "\n")
		}
		note := "Environment: " + f.Env
		if f.FlagName() != "" {
			note += "  Flag: --" + f.FlagName()
		}
		if len(f.Enum) > 0 {
			note += "  Allowed: " + strings.Join(f.Enum, ", ")
		}
		b.WriteString(ind + "# " + note + "\n")
		b.WriteString(ind + leaf + ": " + yamlDefault(f) + "\n")
	}
	return b.String()
}

func yamlDefault(f Field) string {
	switch f.TypeName() {
	case "integer", "boolean":
		if f.Default == "" {
			return "0"
		}
		return f.Default
	case "list":
		if f.Default == "" {
			return "[]"
		}
		parts := strings.Split(f.Default, ",")
		for i := range parts {
			parts[i] = strconv.Quote(strings.TrimSpace(parts[i]))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	return strconv.Quote(f.Default)
}

func wrap(s string, width int) []string {
	var lines []string
	cur := ""
	for _, w := range strings.Fields(s) {
		if cur != "" && len(cur)+1+len(w) > width {
			lines = append(lines, cur)
			cur = ""
		}
		if cur != "" {
			cur += " "
		}
		cur += w
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// CheckExamples reports what the committed example files lack or get wrong:
// every variable must appear in the .env example (commented or not), every
// non-secret key must be a key of the YAML example, and no secret may be.
// It is how the sync test notices a key added without example entries.
func CheckExamples(vars []Var, yamlText, envText string) []string {
	var problems []string
	envLines := map[string]bool{}
	for _, l := range strings.Split(envText, "\n") {
		l = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "#"))
		if name, _, ok := strings.Cut(l, "="); ok {
			envLines[strings.TrimSpace(name)] = true
		}
	}
	paths, err := yamlPaths(yamlText)
	if err != nil {
		return []string{"config.example.yaml: " + err.Error()}
	}
	for _, v := range vars {
		if !envLines[v.Name] {
			problems = append(problems, ".env.example has no entry for "+v.Name)
		}
		switch {
		case v.Key == "":
		case v.Secret && paths[v.Key]:
			problems = append(problems, "config.example.yaml must not contain secret key "+v.Key)
		case !v.Secret && !paths[v.Key]:
			problems = append(problems, "config.example.yaml has no entry for "+v.Key)
		}
	}
	sort.Strings(problems)
	return problems
}

func yamlPaths(text string) (map[string]bool, error) {
	var doc yaml.Node
	if err := yaml.NewDecoder(bytes.NewReader([]byte(text))).Decode(&doc); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	out := map[string]bool{}
	var walk func(n *yaml.Node, prefix string)
	walk = func(n *yaml.Node, prefix string) {
		if n.Kind == yaml.DocumentNode && len(n.Content) == 1 {
			n = n.Content[0]
		}
		if n.Kind != yaml.MappingNode {
			return
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			p := prefix + n.Content[i].Value
			if v := n.Content[i+1]; v.Kind == yaml.MappingNode {
				walk(v, p+".")
			} else {
				out[p] = true
			}
		}
	}
	walk(&doc, "")
	return out, nil
}
