package outbox

import (
	"errors"
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

// forbiddenNames are field names that may never be allow-listed: credentials,
// account identifiers, raw provider payloads and free text. Matching is
// exact, on the whole name or on a whole "_"/"-" separated token of it, so
// credit_limit, merchant_name and company_id are fine. This is a guard against
// a careless catalog entry, not the allow-list itself: every field is also
// typed, size-bounded and reviewed.
var (
	forbiddenTokens = map[string]bool{
		"secret": true, "secrets": true, "token": true, "tokens": true, "password": true, "passwd": true,
		"credential": true, "credentials": true, "apikey": true, "totp": true, "cookie": true, "session": true,
		"iban": true, "bban": true, "swift": true,
		"raw": true, "payload": true, "description": true, "memo": true, "remittance": true,
		// Free text and people: any field that names one of these is refused, whatever the prefix.
		"note": true, "notes": true, "text": true, "body": true, "freetext": true,
		"counterparty": true, "holder": true, "debtor": true, "creditor": true, "card": true,
	}
	forbiddenNames = map[string]bool{
		"api_key": true, "account_number": true, "account_no": true, "pan": true, "cvv": true,
		// Too common as suffixes (merchant_name, error_message) to refuse as tokens.
		"name": true, "message": true,
	}
)

func forbiddenField(name string) (string, bool) {
	if forbiddenNames[name] {
		return name, true
	}
	for _, t := range strings.FieldsFunc(name, func(r rune) bool { return r == '_' || r == '-' }) {
		if forbiddenTokens[t] {
			return t, true
		}
	}
	return "", false
}

// KeyKind is the shape of the optional subject key an event may carry.
type KeyKind string

const (
	// KeyNone: the subject has no key (the zero value).
	KeyNone KeyKind = ""
	// KeyULID: one token that is a lowercase ULID (26 Crockford base32 characters),
	// the id of the entity the event is about.
	KeyULID KeyKind = "ulid"
)

// Doc describes the key for the event catalog; empty when there is none.
func (k KeyKind) Doc() string {
	if k == KeyULID {
		return "lowercase ULID of the entity"
	}
	return ""
}

func (k KeyKind) check(key string) error {
	switch k {
	case KeyNone:
		if key != "" {
			return errors.New("the event declares no subject key")
		}
	case KeyULID:
		if key != "" && !validULIDKey(key) {
			return errors.New("the subject key must be a lowercase ULID (26 characters of 0-9 a-z without i, l, o, u)")
		}
	}
	return nil
}

func validULIDKey(s string) bool {
	if len(s) != 26 || s[0] > '7' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'z' || c == 'i' || c == 'l' || c == 'o' || c == 'u') {
			return false
		}
	}
	return true
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
	// Key declares the shape of the optional subject key; KeyNone means the
	// subject has no key and Enqueue rejects one.
	Key    KeyKind
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
	if d.Key != KeyNone && d.Key != KeyULID {
		return fmt.Errorf("outbox: event %s has unknown key kind %q", d.id(), d.Key)
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
		if bad, hit := forbiddenField(f.Name); hit {
			return fmt.Errorf("outbox: event %s field %q may not be allow-listed (%q: secrets, account numbers, raw bank data and free text stay out of events)", d.id(), f.Name, bad)
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
