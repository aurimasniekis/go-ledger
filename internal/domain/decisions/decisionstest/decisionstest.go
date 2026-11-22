// Package decisionstest has offline test doubles for the decision layer: a
// scripted provider that records exactly what it would have sent, and memory
// implementations of the consent, usage and suggestion ports.
package decisionstest

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"

	"github.com/aurimasniekis/go-ledger/internal/domain/decisions"
)

// Reply is one scripted answer: an error, or an output.
type Reply struct {
	Output     string
	Model      string
	ResponseID string
	Err        error
	// Block waits for the context to end before answering (a hung provider).
	Block bool
}

// Provider is a scripted provider. Every request it receives is serialised
// to JSON, as an adapter would put it on the wire, and kept in Sent.
type Provider struct {
	Desc decisions.Descriptor

	mu      sync.Mutex
	replies []Reply
	sent    [][]byte
}

// NewProvider returns a provider named name with the known descriptor (or
// the given capabilities when name is not a known provider).
func NewProvider(name string, replies ...Reply) *Provider {
	d, ok := decisions.KnownProvider(name)
	if !ok {
		d = decisions.Descriptor{Name: name, Capabilities: []decisions.Capability{decisions.Decide, decisions.Generate}}
	}
	return &Provider{Desc: d, replies: replies}
}

// Script appends replies.
func (p *Provider) Script(replies ...Reply) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.replies = append(p.replies, replies...)
}

// Descriptor implements decisions.Provider.
func (p *Provider) Descriptor() decisions.Descriptor { return p.Desc }

// Call implements decisions.Provider.
func (p *Provider) Call(ctx context.Context, req decisions.Request) (decisions.Response, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return decisions.Response{}, err
	}
	p.mu.Lock()
	p.sent = append(p.sent, b)
	var r Reply
	if len(p.replies) > 0 {
		r, p.replies = p.replies[0], p.replies[1:]
	} else {
		r = Reply{Err: &decisions.ProviderError{Kind: decisions.KindUnavailable, Message: "no scripted reply"}}
	}
	p.mu.Unlock()
	if r.Block {
		<-ctx.Done()
		return decisions.Response{}, ctx.Err()
	}
	if r.Err != nil {
		return decisions.Response{}, r.Err
	}
	return decisions.Response{Output: json.RawMessage(r.Output), Model: r.Model, ResponseID: r.ResponseID}, nil
}

// Sent returns the serialised requests, oldest first.
func (p *Provider) Sent() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.sent)
}

// Calls counts the requests received.
func (p *Provider) Calls() int { return len(p.Sent()) }

// Consents is a memory consent record keyed by provider and purpose.
type Consents struct {
	mu      sync.Mutex
	granted map[string]bool
	Err     error
}

// Grant records consent for one provider and purpose.
func (c *Consents) Grant(provider string, purpose decisions.TaskName) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.granted == nil {
		c.granted = map[string]bool{}
	}
	c.granted[provider+"\x00"+string(purpose)] = true
}

// Granted implements decisions.Consents.
func (c *Consents) Granted(_ context.Context, provider string, purpose decisions.TaskName) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.granted[provider+"\x00"+string(purpose)], c.Err
}

// Usage is a memory request counter.
type Usage struct {
	mu     sync.Mutex
	counts map[string]int
}

// Reserve implements decisions.Usage.
func (u *Usage) Reserve(_ context.Context, task decisions.TaskName, period string, limit int) (bool, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.counts == nil {
		u.counts = map[string]int{}
	}
	k := string(task) + "/" + period
	if u.counts[k] >= limit {
		return false, nil
	}
	u.counts[k]++
	return true, nil
}

// Count returns the attempts counted for a task in a period.
func (u *Usage) Count(task decisions.TaskName, period string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.counts[string(task)+"/"+period]
}

// Suggestions is a memory suggestion store.
type Suggestions struct {
	mu  sync.Mutex
	All []decisions.Suggestion
	n   int
}

// Record implements decisions.Suggestions.
func (s *Suggestions) Record(_ context.Context, ns decisions.NewSuggestion) (decisions.Suggestion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, old := range s.All {
		if old.State == decisions.StatePending && old.Task == ns.Task && old.Object == ns.Object {
			s.All[i].State = decisions.StateSuperseded
			s.All[i].Version++
		}
	}
	s.n++
	sg := decisions.Suggestion{NewSuggestion: ns, ID: fmt.Sprintf("s%d", s.n), State: decisions.StatePending, Version: 1}
	s.All = append(s.All, sg)
	return sg, nil
}

// Len counts stored suggestions.
func (s *Suggestions) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.All)
}
