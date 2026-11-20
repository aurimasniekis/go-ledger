package config

import "flag"

// Flags carries the command-line layer: --config and one flag per non-secret
// key, named by its dotted path (--http.listen).
type Flags struct {
	// Config is the value of --config.
	Config string
	values map[string]string
}

// RegisterFlags adds the configuration flags to fs. Call Values after fs.Parse.
func RegisterFlags(fs *flag.FlagSet) *Flags {
	fl := &Flags{values: map[string]string{}}
	fs.StringVar(&fl.Config, "config", "", "path of the YAML config file (default: $"+Prefix+"CONFIG)")
	for _, f := range fields() {
		if f.Secret {
			continue
		}
		fs.Var(&flagValue{fl: fl, key: f.Path, isBool: f.TypeName() == "boolean"}, f.Path, f.Desc+" ["+f.Env+"]")
	}
	return fl
}

// Values returns the raw values of the key flags that were set.
func (f *Flags) Values() map[string]string { return f.values }

// Options returns Load options for the parsed flags.
func (f *Flags) Options() Options { return Options{ConfigPath: f.Config, Flags: f.values} }

type flagValue struct {
	fl     *Flags
	key    string
	isBool bool
}

func (v *flagValue) String() string {
	if v == nil || v.fl == nil {
		return ""
	}
	return v.fl.values[v.key]
}

func (v *flagValue) Set(s string) error { v.fl.values[v.key] = s; return nil }

func (v *flagValue) IsBoolFlag() bool { return v.isBool }
