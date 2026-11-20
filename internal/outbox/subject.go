package outbox

import (
	"fmt"
	"strings"
)

const (
	// Domain is the first token after "evt." in every subject and the first
	// element of every event type.
	Domain = "ledger"
	// MaxSubjectBytes is the longest subject Pulse accepts.
	MaxSubjectBytes = 256
	// TypeVersion is the version suffix of every event type emitted today.
	// Breaking payload changes get a new version of the type, never of the subject.
	TypeVersion = 1

	subjectPrefix = "evt."
)

// ValidToken reports whether s is one subject token: one or more of
// lowercase a-z, 0-9, "_" and "-".
func ValidToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

// ValidateSubject checks a subject against the Pulse rules: the "evt."
// prefix, at least four dot-separated tokens, every token lowercase
// [a-z0-9_-]+ (so no wildcards or empty tokens) and at most 256 bytes.
func ValidateSubject(subject string) error {
	if len(subject) > MaxSubjectBytes {
		return fmt.Errorf("outbox: subject is %d bytes, the limit is %d", len(subject), MaxSubjectBytes)
	}
	rest, ok := strings.CutPrefix(subject, subjectPrefix)
	if !ok {
		return fmt.Errorf("outbox: subject %q must start with %q", subject, subjectPrefix)
	}
	tokens := strings.Split(rest, ".")
	if len(tokens) < 3 {
		return fmt.Errorf("outbox: subject %q needs at least four tokens (evt.<domain>.<entity>.<action>)", subject)
	}
	for _, t := range tokens {
		if !ValidToken(t) {
			return fmt.Errorf("outbox: subject %q has an invalid token %q (want lowercase [a-z0-9_-]+)", subject, t)
		}
	}
	return nil
}

// ValidateType checks an event type of the form <domain>.<entity>.<action>.v<N>:
// four dot-separated parts, the first three valid tokens, the last "v" and a
// positive number without leading zeros.
func ValidateType(typ string) error {
	parts := strings.Split(typ, ".")
	if len(parts) != 4 {
		return fmt.Errorf("outbox: type %q must have the form <domain>.<entity>.<action>.v<N>", typ)
	}
	for _, p := range parts[:3] {
		if !ValidToken(p) {
			return fmt.Errorf("outbox: type %q has an invalid part %q (want lowercase [a-z0-9_-]+)", typ, p)
		}
	}
	v := parts[3]
	if len(v) < 2 || v[0] != 'v' || v[1] == '0' {
		return fmt.Errorf("outbox: type %q must end in a version such as v1", typ)
	}
	for i := 1; i < len(v); i++ {
		if v[i] < '0' || v[i] > '9' {
			return fmt.Errorf("outbox: type %q must end in a version such as v1", typ)
		}
	}
	return nil
}

// Subject builds evt.<domain>.<entity>.<action>[.<key>] and validates it. key
// may be empty; when set it is one or more dot-separated valid tokens.
func Subject(entity, action, key string) (string, error) {
	s := subjectPrefix + Domain + "." + entity + "." + action
	if key != "" {
		s += "." + key
	}
	if !ValidToken(entity) || !ValidToken(action) {
		return "", fmt.Errorf("outbox: entity %q and action %q must be lowercase [a-z0-9_-]+ tokens", entity, action)
	}
	return s, ValidateSubject(s)
}

// TypeName builds <domain>.<entity>.<action>.v<version>.
func TypeName(entity, action string, version int) string {
	return fmt.Sprintf("%s.%s.%s.v%d", Domain, entity, action, version)
}
