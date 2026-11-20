package outbox

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
)

// Kind is the JSON type a payload field may carry. Floats are deliberately
// absent: amounts travel as decimal strings next to a currency code.
type Kind string

const (
	KindString Kind = "string"
	KindInt    Kind = "int"
	KindBool   Kind = "bool"
)

// MaxStringBytes bounds a string payload value, which keeps free text out by size as well as by name.
const MaxStringBytes = 256

// forbiddenFragments are field-name fragments that may never be allow-listed:
// credentials, account identifiers, raw provider payloads and free text. This
// is a guard against a careless catalog entry, not the allow-list itself.
var forbiddenFragments = []string{
	"secret", "token", "password", "passwd", "credential", "cred", "apikey", "api_key", "totp", "cookie", "session",
	"iban", "account_number", "bban", "pan", "card",
	"raw", "payload", "description", "memo", "note", "remittance", "text", "message", "body", "counterparty", "name",
}

// Field is one allow-listed payload field.
type Field struct {
	Name     string
	Kind     Kind
	Required bool
	// Doc is a one-line description used by the event catalog.
	Doc string
}

// Definition declares one event: its entity, action, type version, trigger
// and the only payload fields it may carry.
type Definition struct {
	Entity  string
	Action  string
	Version int // zero means [TypeVersion]
	// Trigger says when the event is enqueued, for the catalog.
	Trigger string
	// KeyDoc describes the optional subject key, empty when the subject has none.
	KeyDoc string
	Fields []Field
}

// Type returns the event type of d.
func (d Definition) Type() string { return TypeName(d.Entity, d.Action, d.version()) }

func (d Definition) version() int {
	if d.Version == 0 {
		return TypeVersion
	}
	return d.Version
}

func (d Definition) id() string { return d.Entity + "." + d.Action }

func (d Definition) validate() error {
	if _, err := Subject(d.Entity, d.Action, ""); err != nil {
		return err
	}
	if d.version() < 1 {
		return fmt.Errorf("outbox: event %s has version %d, want >= 1", d.id(), d.version())
	}
	if err := ValidateType(d.Type()); err != nil {
		return err
	}
	if strings.TrimSpace(d.Trigger) == "" {
		return fmt.Errorf("outbox: event %s has no trigger description", d.id())
	}
	seen := map[string]bool{}
	for _, f := range d.Fields {
		if !ValidToken(f.Name) {
			return fmt.Errorf("outbox: event %s field %q must be a lowercase [a-z0-9_-]+ name", d.id(), f.Name)
		}
		if seen[f.Name] {
			return fmt.Errorf("outbox: event %s declares field %q twice", d.id(), f.Name)
		}
		seen[f.Name] = true
		if f.Kind != KindString && f.Kind != KindInt && f.Kind != KindBool {
			return fmt.Errorf("outbox: event %s field %q has unknown kind %q", d.id(), f.Name, f.Kind)
		}
		for _, bad := range forbiddenFragments {
			if strings.Contains(f.Name, bad) {
				return fmt.Errorf("outbox: event %s field %q may not be allow-listed (name contains %q: secrets, account numbers, raw bank data and free text stay out of events)", d.id(), f.Name, bad)
			}
		}
	}
	return nil
}

// Registry holds the event definitions an [Outbox] accepts. An event that is
// not registered cannot be enqueued.
type Registry struct {
	mu   sync.RWMutex
	defs map[string]Definition
}

// NewRegistry returns a registry holding defs. It panics on an invalid or
// duplicate definition, like regexp.MustCompile: catalogs are static.
func NewRegistry(defs ...Definition) *Registry {
	r := &Registry{defs: map[string]Definition{}}
	for _, d := range defs {
		if err := r.Register(d); err != nil {
			panic(err)
		}
	}
	return r
}

// Register adds d. It fails on an invalid definition or a repeated entity and action.
func (r *Registry) Register(d Definition) error {
	if err := d.validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.defs[d.id()]; dup {
		return fmt.Errorf("outbox: event %s is already registered", d.id())
	}
	d.Fields = slices.Clone(d.Fields)
	r.defs[d.id()] = d
	return nil
}

// Lookup returns the definition for entity and action.
func (r *Registry) Lookup(entity, action string) (Definition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.defs[entity+"."+action]
	return d, ok
}

// Definitions returns every definition ordered by entity then action.
func (r *Registry) Definitions() []Definition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Definition, 0, len(r.defs))
	for _, d := range r.defs {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id() < out[j].id() })
	return out
}

// checkPayload enforces the allow-list: every key declared, every value of
// its declared kind, every required field present.
func (d Definition) checkPayload(p map[string]any) error {
	fields := make(map[string]Field, len(d.Fields))
	for _, f := range d.Fields {
		fields[f.Name] = f
	}
	for k, v := range p {
		f, ok := fields[k]
		if !ok {
			return fmt.Errorf("outbox: event %s: payload field %q is not allow-listed", d.id(), k)
		}
		switch f.Kind {
		case KindString:
			s, ok := v.(string)
			if !ok {
				return fmt.Errorf("outbox: event %s: field %q must be a string", d.id(), k)
			}
			if len(s) > MaxStringBytes {
				return fmt.Errorf("outbox: event %s: field %q is %d bytes, the limit is %d", d.id(), k, len(s), MaxStringBytes)
			}
		case KindInt:
			switch v.(type) {
			case int, int32, int64:
			default:
				return fmt.Errorf("outbox: event %s: field %q must be an integer", d.id(), k)
			}
		case KindBool:
			if _, ok := v.(bool); !ok {
				return fmt.Errorf("outbox: event %s: field %q must be a bool", d.id(), k)
			}
		}
	}
	for _, f := range d.Fields {
		if _, ok := p[f.Name]; f.Required && !ok {
			return fmt.Errorf("outbox: event %s: required payload field %q is missing", d.id(), f.Name)
		}
	}
	return nil
}
