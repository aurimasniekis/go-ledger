package decisions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

// Descriptor says what a provider adapter can do. Providers are not assumed
// interchangeable: a task is only routed to a provider whose descriptor lists
// the task's capability.
type Descriptor struct {
	// Name is the provider's configuration name (openrouter, openai,
	// anthropic, typesafe).
	Name string
	// Capabilities the adapter serves.
	Capabilities []Capability
	// Routing marks a provider that forwards requests to other providers
	// (OpenRouter). Ledger only uses it with an explicit list of allowed
	// upstream providers (decision D13: downstream sharing is disabled unless
	// the operator names them).
	Routing bool
}

// Supports reports whether the provider serves c.
func (d Descriptor) Supports(c Capability) bool { return slices.Contains(d.Capabilities, c) }

// KnownProviders returns the descriptors of the providers Ledger is to
// integrate (control default extending D13, from the R3 to R6 research):
// TypeSafe System One is decide-only; OpenRouter, OpenAI and Anthropic serve
// both. Configuration is validated against these; an adapter's own descriptor
// is checked again when it is registered.
func KnownProviders() []Descriptor {
	return []Descriptor{
		{Name: "openrouter", Capabilities: []Capability{Decide, Generate}, Routing: true},
		{Name: "openai", Capabilities: []Capability{Decide, Generate}},
		{Name: "anthropic", Capabilities: []Capability{Decide, Generate}},
		{Name: ProviderTypeSafe, Capabilities: []Capability{Decide}},
	}
}

// KnownProvider returns the descriptor of a known provider.
func KnownProvider(name string) (Descriptor, bool) {
	for _, d := range KnownProviders() {
		if d.Name == name {
			return d, true
		}
	}
	return Descriptor{}, false
}

// Provider is a decision-provider adapter. Call sends one request and returns
// the model's raw answer; it makes one attempt (the [Service] retries and
// counts attempts). It must send nothing beyond what the request carries:
// the request is already filtered by the field allowlist.
type Provider interface {
	Descriptor() Descriptor
	Call(ctx context.Context, req Request) (Response, error)
}

// FieldValue is one allowed field and its value.
type FieldValue struct {
	Field Field  `json:"field"`
	Value string `json:"value"`
}

// Option is one candidate offered to a decide task. Key is an opaque key
// ("c1", "c2", ...) the answer must use: Ledger's identifiers never leave the
// process, and the output schema stays free of user data.
type Option struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// Request is what an adapter may send: only fields the allowlist for the
// provider and purpose permits, already applied.
type Request struct {
	Task       TaskName     `json:"task"`
	Capability Capability   `json:"capability"`
	Output     OutputKind   `json:"output"`
	Model      string       `json:"model"`
	Fields     []FieldValue `json:"fields,omitempty"`
	Options    []Option     `json:"options,omitempty"`
	// MaxChoices bounds an OutputChoices answer; MaxText an OutputText one.
	MaxChoices int `json:"max_choices,omitempty"`
	MaxText    int `json:"max_text,omitempty"`
	// Schema is the task's static output schema (JSON Schema): the same for
	// every request of the task, with no user data in it.
	Schema json.RawMessage `json:"schema"`
	// Upstreams lists the upstream providers a routing provider may use; it
	// is never empty for a routing provider.
	Upstreams []string `json:"upstreams,omitempty"`
}

// Response is the model's raw answer.
type Response struct {
	// Output is the answer, validated by the service against the schema.
	Output json.RawMessage
	// Model is the model identifier the provider reports it used; empty
	// means the configured one.
	Model string
	// ResponseID is the provider's identifier of the response, if any.
	ResponseID string
}

// ErrorKind classifies a provider failure.
type ErrorKind string

// Error kinds.
const (
	// KindRetryable is a failure returned before any generation (HTTP 408,
	// 429, 502, 503, 529 and OpenRouter's in-flight budget 402; the adapter
	// decides): the only kind the service retries.
	KindRetryable ErrorKind = "retryable"
	// KindUnavailable is an outage that is not retried (network failure, a
	// 500, an error inside a 200 or mid-stream).
	KindUnavailable ErrorKind = "unavailable"
	// KindRejected is a request the provider refused (authentication,
	// configuration, policy, spend limit): neither retried nor a fallback
	// reason.
	KindRejected ErrorKind = "rejected"
)

// ProviderError is a classified adapter failure. Message must not carry
// request content, keys or provider payloads.
type ProviderError struct {
	Kind ErrorKind
	// RetryAfter is the provider's Retry-After, if it sent one.
	RetryAfter time.Duration
	Message    string
}

func (e *ProviderError) Error() string {
	if e.Message == "" {
		return "decision provider: " + string(e.Kind)
	}
	return fmt.Sprintf("decision provider: %s: %s", e.Kind, e.Message)
}

// kindOf classifies any adapter error: an unclassified one is an outage.
func kindOf(err error) (ErrorKind, time.Duration) {
	var pe *ProviderError
	if errors.As(err, &pe) {
		return pe.Kind, pe.RetryAfter
	}
	return KindUnavailable, 0
}
