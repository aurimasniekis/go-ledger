// Package redact keeps secrets out of logs and error responses. A Redactor
// masks registered secret values wherever they appear in text, and masks the
// value of any attribute whose key names a secret (password, token, ...).
// Logs (internal/logging) and API errors (internal/httpapi) both use it.
package redact

import (
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Mask replaces a redacted value.
const Mask = "[REDACTED]"

// sensitiveFragments are lower-case substrings of attribute names that mark
// the value as secret. The list errs on the side of hiding too much.
var sensitiveFragments = []string{
	"password", "passwd", "passphrase", "secret", "token", "authorization",
	"cookie", "api_key", "apikey", "credential", "kek", "totp", "private_key",
	"session", "otp",
}

// SensitiveKey reports whether a log attribute, header or field name denotes a
// secret, such as "password", "Authorization" or "api-token".
func SensitiveKey(name string) bool {
	n := strings.ToLower(strings.ReplaceAll(name, "-", "_"))
	for _, f := range sensitiveFragments {
		if strings.Contains(n, f) {
			return true
		}
	}
	return false
}

// bearer matches an Authorization-style credential written inside free text.
var bearer = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{6,}`)

// Redactor masks registered secret values. It is safe for concurrent use.
type Redactor struct {
	mu      sync.RWMutex
	secrets []string // longest first, so a secret containing another is masked whole
}

// New returns a Redactor that already knows the given secrets.
func New(secrets ...string) *Redactor {
	r := &Redactor{}
	r.Add(secrets...)
	return r
}

var def = New()

// Default is the process-wide Redactor. The server registers configuration
// secrets with it at startup; the logger and the error writer consult it.
func Default() *Redactor { return def }

// Add registers secret values. Empty values are ignored.
func (r *Redactor) Add(secrets ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range secrets {
		if s == "" {
			continue
		}
		r.secrets = append(r.secrets, s)
	}
	sort.SliceStable(r.secrets, func(i, j int) bool { return len(r.secrets[i]) > len(r.secrets[j]) })
}

// Text masks every registered secret and any bearer or basic credential in s.
func (r *Redactor) Text(s string) string {
	if r == nil || s == "" {
		return s
	}
	r.mu.RLock()
	for _, sec := range r.secrets {
		s = strings.ReplaceAll(s, sec, Mask)
	}
	r.mu.RUnlock()
	return bearer.ReplaceAllString(s, "$1 "+Mask)
}

// ReplaceAttr is a slog HandlerOptions.ReplaceAttr function: it masks values
// under secret-looking keys, and masks registered secrets inside strings,
// errors and other values (including the log message).
func (r *Redactor) ReplaceAttr(_ []string, a slog.Attr) slog.Attr {
	if a.Key != "" && SensitiveKey(a.Key) {
		return slog.String(a.Key, Mask)
	}
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, r.Text(v.String()))
	case slog.KindAny:
		if v.Any() == nil {
			return a
		}
		text := fmt.Sprint(v.Any())
		if masked := r.Text(text); masked != text {
			return slog.String(a.Key, masked)
		}
	}
	return a
}
