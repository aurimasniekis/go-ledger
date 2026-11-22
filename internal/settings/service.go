// Package settings runs Ledger's runtime settings (ADR-0066): the immutable
// snapshot of the effective configuration, the write path that validates the
// whole would-be configuration before anything is committed, and the
// subscriptions through which components apply a change without a restart.
// The registry of groups and members and the store port live in
// internal/domain/settings; this package joins them with the configuration
// loader, which finance and store packages may not import.
package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	reg "github.com/aurimasniekis/go-ledger/internal/domain/settings"
)

// Apply states of a group (ADR-0066 point 17).
const (
	// Applied: every component that consumes the group runs with its
	// current values.
	Applied = "applied"
	// Failed: a component could not rebuild after the last write; the
	// setting stays committed.
	Failed = "failed"
	// RestartRequired: the group was written, but no component applies it
	// live yet, so the new values take effect at the next start.
	RestartRequired = "restart_required"
)

// ApplyState says whether a group's values are in force. Code is a stable
// error code, never a value or an error text.
type ApplyState struct {
	State string
	Code  string
	At    time.Time
}

// ApplyCoder is an error a subscriber returns to name a stable error code for
// [ApplyState] (for example "discovery_failed"). Codes are lower-case words
// joined by underscores; anything else is reported as "apply_failed".
type ApplyCoder interface{ ApplyCode() string }

var codePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func applyCode(err error) string {
	var c ApplyCoder
	if errors.As(err, &c) && codePattern.MatchString(c.ApplyCode()) {
		return c.ApplyCode()
	}
	return "apply_failed"
}

// Applier rebuilds a component from a new snapshot. It runs after the write
// committed; an error does not roll the setting back (ADR-0066 point 17). An
// applier must not write settings itself.
type Applier func(ctx context.Context, snap *Snapshot) error

// GroupState is one group's stored row in a snapshot.
type GroupState struct {
	Exists    bool
	Version   int64
	UpdatedAt time.Time
	Values    map[string]json.RawMessage
}

// Snapshot is an immutable view of the effective configuration and the stored
// layer. Requests keep the snapshot they started with.
type Snapshot struct {
	config  config.Config
	sources map[string]string
	groups  map[string]GroupState
}

// Config returns a copy of the effective configuration.
func (s *Snapshot) Config() config.Config { return s.config.Clone() }

// Source returns where the key's effective value comes from, as recorded by
// the configuration loader ("default", "api", "yaml", "env LEDGER_X", ...).
func (s *Snapshot) Source(key string) string { return s.sources[key] }

// Sources returns a copy of every key's source.
func (s *Snapshot) Sources() map[string]string {
	out := make(map[string]string, len(s.sources))
	for k, v := range s.sources {
		out[k] = v
	}
	return out
}

// Group returns the stored state of a group (version 1 and Exists false when
// it has no row yet).
func (s *Snapshot) Group(name string) GroupState {
	g, ok := s.groups[name]
	if !ok {
		return GroupState{Version: 1}
	}
	return g
}

// Service owns the settings snapshot and serialises writes.
type Service struct {
	base  *config.Result
	store reg.Store
	now   func() time.Time

	snap atomic.Pointer[Snapshot]

	mu    sync.Mutex // serialises writes and their notifications
	subMu sync.Mutex
	subs  map[string][]*subscription
	apply map[string]ApplyState
}

type subscription struct{ fn Applier }

// Options carries what tests replace.
type Options struct {
	// Now defaults to time.Now.
	Now func() time.Time
}

// Open reads the stored layer and builds the first snapshot over base, the
// configuration loaded without a stored layer. An invalid stored layer is an
// error (*config.Error, naming the stored keys), which stops startup.
func Open(ctx context.Context, base *config.Result, st reg.Store, opts Options) (*Service, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	rows, err := st.Groups(ctx)
	if err != nil {
		return nil, err
	}
	groups := map[string]GroupState{}
	for _, r := range rows {
		groups[r.Name] = GroupState{Exists: true, Version: r.Version, UpdatedAt: r.UpdatedAt, Values: r.Values}
	}
	flat, err := flatten(groups)
	if err != nil {
		return nil, err
	}
	res, err := base.WithStored(flat)
	if err != nil {
		return nil, err
	}
	s := &Service{base: base, store: st, now: opts.Now, subs: map[string][]*subscription{}, apply: map[string]ApplyState{}}
	s.snap.Store(&Snapshot{config: res.Config, sources: res.Sources, groups: groups})
	return s, nil
}

// flatten turns the stored rows into the loader's stored layer, keyed by
// configuration key. A row or member the registry does not know is an error.
func flatten(groups map[string]GroupState) (map[string]json.RawMessage, error) {
	flat := map[string]json.RawMessage{}
	var problems []config.Problem
	for _, name := range sortedNames(groups) {
		g, ok := reg.Lookup(name)
		if !ok {
			problems = append(problems, config.Problem{Source: config.LayerAPI, Key: name, Msg: "stored settings group is unknown"})
			continue
		}
		for _, member := range sortedNames(groups[name].Values) {
			m, ok := g.Member(member)
			if !ok || m.Secret {
				problems = append(problems, config.Problem{Source: config.LayerAPI, Key: name + "." + member, Msg: "stored setting is not a member of its group"})
				continue
			}
			flat[m.Key] = groups[name].Values[member]
		}
	}
	if len(problems) > 0 {
		return nil, &config.Error{Problems: problems}
	}
	return flat, nil
}

func sortedNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Snapshot returns the current snapshot.
func (s *Service) Snapshot() *Snapshot { return s.snap.Load() }

// Subscribe registers fn for changes of the named groups. It returns a
// function that removes the subscription. fn runs after a write to one of the
// groups commits, with the new snapshot, one write at a time.
func (s *Service) Subscribe(fn Applier, groups ...string) (cancel func()) {
	sub := &subscription{fn: fn}
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for _, g := range groups {
		if _, ok := reg.Lookup(g); !ok {
			panic("settings: Subscribe to unknown group " + g)
		}
		s.subs[g] = append(s.subs[g], sub)
	}
	return func() {
		s.subMu.Lock()
		defer s.subMu.Unlock()
		for _, g := range groups {
			s.subs[g] = slices.DeleteFunc(s.subs[g], func(x *subscription) bool { return x == sub })
		}
	}
}

// ApplyState returns a group's apply state. A group nobody wrote since the
// start is applied: every component was built from it.
func (s *Service) ApplyState(group string) ApplyState {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	if st, ok := s.apply[group]; ok {
		return st
	}
	return ApplyState{State: Applied}
}

// Update applies a merge patch to a group: each member is set to its value, or
// removed from the stored layer with JSON null (falling back to the
// configuration or the default). expected is the version the caller read
// (If-Match). The whole would-be configuration is validated before anything
// is committed; an empty patch, or one that changes nothing, keeps the
// version and records nothing.
func (s *Service) Update(ctx context.Context, actor audit.Actor, group string, expected int64, patch map[string]json.RawMessage) (*Snapshot, error) {
	g, ok := reg.Lookup(group)
	if !ok {
		return nil, reg.ErrUnknownGroup
	}
	snap := s.Snapshot()
	normal := map[string]json.RawMessage{}
	var fes []reg.FieldError
	var pinned []reg.PinnedKey
	for _, name := range sortedNames(patch) {
		m, ok := g.Member(name)
		if !ok {
			fes = append(fes, reg.FieldError{Field: name, Message: "no such setting in group " + g.Name})
			continue
		}
		if m.Secret || m.Sensitive || m.Reclassifies {
			fes = append(fes, reg.FieldError{Field: m.Key, Message: reg.MsgNotWritableYet})
			continue
		}
		raw := bytes.TrimSpace(patch[name])
		if string(raw) == "null" {
			normal[name] = nil
		} else {
			v, err := config.NormalizeStored(m.Key, raw)
			if err != nil {
				fes = append(fes, reg.FieldError{Field: m.Key, Message: err.Error()})
				continue
			}
			normal[name] = v
		}
		if src := snap.Source(m.Key); config.Pinned(src) {
			pinned = append(pinned, reg.PinnedKey{Key: m.Key, Layer: config.Layer(src)})
		}
	}
	if len(fes) > 0 {
		return nil, &reg.ValidationError{Fields: fes}
	}
	if len(pinned) > 0 {
		return nil, &reg.PinnedError{Keys: pinned}
	}
	return s.write(ctx, actor, g, expected, reg.ActionUpdated, func(cur map[string]json.RawMessage) map[string]json.RawMessage {
		next := cloneValues(cur)
		for name, v := range normal {
			if v == nil {
				delete(next, name)
			} else {
				next[name] = v
			}
		}
		return next
	})
}

// Reset removes every unpinned member from the stored layer, so each falls
// back to its default (the DELETE of a settings resource). Stored values of
// pinned members are kept (ADR-0066 point 6). A group without a row changes
// nothing and keeps version 1.
func (s *Service) Reset(ctx context.Context, actor audit.Actor, group string, expected int64) (*Snapshot, error) {
	g, ok := reg.Lookup(group)
	if !ok {
		return nil, reg.ErrUnknownGroup
	}
	snap := s.Snapshot()
	var fes []reg.FieldError
	for _, name := range sortedNames(snap.Group(g.Name).Values) {
		if m, ok := g.Member(name); ok && (m.Secret || m.Sensitive || m.Reclassifies) && !config.Pinned(snap.Source(m.Key)) {
			fes = append(fes, reg.FieldError{Field: m.Key, Message: reg.MsgNotWritableYet})
		}
	}
	if len(fes) > 0 {
		return nil, &reg.ValidationError{Fields: fes}
	}
	return s.write(ctx, actor, g, expected, reg.ActionReset, func(cur map[string]json.RawMessage) map[string]json.RawMessage {
		next := map[string]json.RawMessage{}
		for name, v := range cur {
			if m, ok := g.Member(name); ok && config.Pinned(snap.Source(m.Key)) {
				next[name] = v
			}
		}
		return next
	})
}

func cloneValues(m map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// changedMembers lists, sorted, the members whose stored value differs.
func changedMembers(a, b map[string]json.RawMessage) []string {
	var out []string
	for k, v := range a {
		if w, ok := b[k]; !ok || !bytes.Equal(v, w) {
			out = append(out, k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func (s *Service) write(ctx context.Context, actor audit.Actor, g reg.Group, expected int64, action audit.Action,
	next func(cur map[string]json.RawMessage) map[string]json.RawMessage) (*Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.Snapshot()
	var result *config.Result
	row, err := s.store.Write(ctx, g.Name, expected, func(cur reg.Stored) (reg.Change, error) {
		values := next(cur.Values)
		changed := changedMembers(cur.Values, values)
		if len(changed) == 0 {
			return reg.Change{Values: cur.Values}, nil
		}
		groups := make(map[string]GroupState, len(before.groups)+1)
		for k, v := range before.groups {
			groups[k] = v
		}
		groups[g.Name] = GroupState{Values: values}
		flat, err := flatten(groups)
		if err == nil {
			result, err = s.base.WithStored(flat)
		}
		if err != nil {
			return reg.Change{}, validationFrom(err)
		}
		return reg.Change{Values: values, Event: &audit.Event{Action: action, Actor: actor,
			Details: map[string]string{"group": g.Name, "fields": strings.Join(changed, ",")}}}, nil
	})
	if err != nil {
		return nil, err
	}
	if result == nil { // nothing changed
		return before, nil
	}
	groups := make(map[string]GroupState, len(before.groups)+1)
	for k, v := range before.groups {
		groups[k] = v
	}
	groups[g.Name] = GroupState{Exists: row.Exists, Version: row.Version, UpdatedAt: row.UpdatedAt, Values: row.Values}
	after := &Snapshot{config: result.Config, sources: result.Sources, groups: groups}
	s.snap.Store(after)
	s.notify(context.WithoutCancel(ctx), before, after)
	return after, nil
}

// validationFrom turns the loader's problems into per-field errors named by
// configuration key. The loader never puts a secret into a message.
func validationFrom(err error) error {
	var ce *config.Error
	if !errors.As(err, &ce) {
		return err
	}
	fes := make([]reg.FieldError, len(ce.Problems))
	for i, p := range ce.Problems {
		fes[i] = reg.FieldError{Field: p.Key, Message: p.Msg}
	}
	return &reg.ValidationError{Fields: fes}
}

// notify runs the appliers of every group whose effective values or sources
// changed, and records each group's apply state.
func (s *Service) notify(ctx context.Context, before, after *Snapshot) {
	for _, g := range reg.Groups() {
		if !groupChanged(g, before, after) {
			continue
		}
		s.subMu.Lock()
		subs := slices.Clone(s.subs[g.Name])
		s.subMu.Unlock()
		state := ApplyState{State: Applied, At: s.now().UTC()}
		if len(subs) == 0 {
			state.State = RestartRequired
		}
		for _, sub := range subs {
			if err := sub.fn(ctx, after); err != nil {
				state = ApplyState{State: Failed, Code: applyCode(err), At: s.now().UTC()}
			}
		}
		s.subMu.Lock()
		s.apply[g.Name] = state
		s.subMu.Unlock()
	}
}

func groupChanged(g reg.Group, before, after *Snapshot) bool {
	for _, m := range g.Members {
		if before.sources[m.Key] != after.sources[m.Key] {
			return true
		}
		if m.Secret {
			continue
		}
		f, ok := config.FieldByPath(m.Key)
		if !ok {
			continue
		}
		a, errA := config.StoredValue(f, &before.config)
		b, errB := config.StoredValue(f, &after.config)
		if errA != nil || errB != nil || !bytes.Equal(a, b) {
			return true
		}
	}
	return false
}

// View is one group's representation (ADR-0066 point 9): the effective value
// of every member except secrets, with its source layer.
type View struct {
	Group     string
	Version   int64
	UpdatedAt time.Time // zero while the group has no row
	Values    map[string]any
	Sources   map[string]string
	Pinned    []string
	Apply     ApplyState
}

// View returns a group's representation from snap (the current snapshot when
// nil). Secrets are never part of it.
func (s *Service) View(group string, snap *Snapshot) (View, error) {
	g, ok := reg.Lookup(group)
	if !ok {
		return View{}, reg.ErrUnknownGroup
	}
	if snap == nil {
		snap = s.Snapshot()
	}
	st := snap.Group(g.Name)
	v := View{Group: g.Name, Version: st.Version, UpdatedAt: st.UpdatedAt, Values: map[string]any{}, Sources: map[string]string{}, Apply: s.ApplyState(g.Name)}
	for _, m := range g.Members {
		if m.Secret {
			continue
		}
		f, ok := config.FieldByPath(m.Key)
		if !ok {
			return View{}, fmt.Errorf("settings: member %s names no configuration key", m.Key)
		}
		raw, err := config.StoredValue(f, &snap.config)
		if err != nil {
			return View{}, err
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber() // integers stay exact
		var val any
		if err := dec.Decode(&val); err != nil {
			return View{}, err
		}
		src := snap.Source(m.Key)
		v.Values[m.Name] = val
		v.Sources[m.Name] = config.Layer(src)
		if config.Pinned(src) {
			v.Pinned = append(v.Pinned, m.Name)
		}
	}
	return v, nil
}
