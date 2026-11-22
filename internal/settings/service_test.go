package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	reg "github.com/aurimasniekis/go-ledger/internal/domain/settings"
)

const sentinel = "SENTINEL-settings-secret-0123456789"

var t0 = time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)

// memStore is an in-memory [Store] with the same version rules as the
// SQLite one.
type memStore struct {
	mu     sync.Mutex
	rows   map[string]reg.Stored
	events []audit.Event
	fail   error
}

func newMemStore() *memStore { return &memStore{rows: map[string]reg.Stored{}} }

func (m *memStore) Groups(context.Context) ([]reg.Stored, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []reg.Stored
	for _, r := range m.rows {
		out = append(out, r)
	}
	return out, nil
}

func (m *memStore) Write(_ context.Context, group string, expected int64, decide func(reg.Stored) (reg.Change, error)) (reg.Stored, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.rows[group]
	if !ok {
		cur = reg.Stored{Name: group, Version: 1, Values: map[string]json.RawMessage{}}
	}
	if cur.Version != expected {
		return reg.Stored{}, &resource.VersionConflictError{Expected: expected, Current: cur.Version}
	}
	ch, err := decide(cur)
	if err != nil {
		return reg.Stored{}, err
	}
	if ch.Event == nil {
		return cur, nil
	}
	if m.fail != nil {
		return reg.Stored{}, m.fail
	}
	if err := audit.Validate(audit.Event{Action: ch.Event.Action, Actor: ch.Event.Actor, Details: ch.Event.Details, At: t0}); err != nil {
		return reg.Stored{}, err
	}
	next := reg.Stored{Name: group, Exists: true, Version: cur.Version + 1, CreatedAt: cur.CreatedAt, UpdatedAt: t0, Values: ch.Values}
	if !cur.Exists {
		next.CreatedAt = t0
	}
	m.rows[group] = next
	m.events = append(m.events, *ch.Event)
	return next, nil
}

type env map[string]string

func baseConfig(t *testing.T, e env, yaml string) *config.Result {
	t.Helper()
	e["LEDGER_KEK_VALUE"] = sentinel + "-kek"
	read := func(name string) ([]byte, error) {
		if name == "c.yaml" && yaml != "" {
			return []byte(yaml), nil
		}
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	path := ""
	if yaml != "" {
		path = "c.yaml"
	}
	res, err := config.Load(config.Options{ConfigPath: path, LookupEnv: func(k string) (string, bool) { v, ok := e[k]; return v, ok },
		ReadFile: read, Environ: func() []string { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func open(t *testing.T, base *config.Result, st reg.Store) *Service {
	t.Helper()
	s, err := Open(context.Background(), base, st, Options{Now: func() time.Time { return t0 }})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

var admin = audit.Actor{Kind: audit.ActorUser, UserID: "01JD3Y4C8V9M2Q7W5R6T0XZBNA"}

func patch(kv ...string) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = json.RawMessage(kv[i+1])
	}
	return out
}

// A group without a row is synthesised with version 1; the first write needs
// version 1 and creates the row with version 2 (ADR-0066 point 9).
func TestSynthesisedVersionAndFirstWrite(t *testing.T) {
	ctx := context.Background()
	st := newMemStore()
	s := open(t, baseConfig(t, env{}, ""), st)
	v, err := s.View("mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	if v.Version != 1 || !v.UpdatedAt.IsZero() || v.Values["burst"] != json.Number("30") || v.Sources["burst"] != "default" || v.Apply.State != Applied {
		t.Fatalf("synthesised view = %+v", v)
	}
	if _, err := s.Update(ctx, admin, "mcp", 2, patch("burst", "5")); !isConflict(err, 2, 1) {
		t.Fatalf("first write with version 2: %v, want a version conflict", err)
	}
	snap, err := s.Update(ctx, admin, "mcp", 1, patch("burst", "5", "enabled", "false"))
	if err != nil {
		t.Fatal(err)
	}
	if c := snap.Config(); c.MCP.Burst != 5 || c.MCP.Enabled {
		t.Errorf("effective mcp = %+v", c.MCP)
	}
	if g := snap.Group("mcp"); !g.Exists || g.Version != 2 || !g.UpdatedAt.Equal(t0) {
		t.Errorf("group state = %+v, want the row at version 2", g)
	}
	if s.Snapshot() != snap {
		t.Error("the new snapshot was not published")
	}
	v, _ = s.View("mcp", nil)
	if v.Version != 2 || v.Sources["burst"] != "api" || v.Values["enabled"] != false {
		t.Errorf("view after the write = %+v", v)
	}
	if len(st.events) != 1 || st.events[0].Action != audit.SettingsUpdated ||
		!reflect.DeepEqual(st.events[0].Details, map[string]string{"group": "mcp", "fields": "burst,enabled"}) {
		t.Errorf("audit = %+v", st.events)
	}
	// An empty patch, or one that changes nothing, keeps the version and
	// records nothing.
	for _, p := range []map[string]json.RawMessage{{}, patch("burst", "5")} {
		snap, err := s.Update(ctx, admin, "mcp", 2, p)
		if err != nil || snap.Group("mcp").Version != 2 {
			t.Errorf("no-op write: version %d, %v", snap.Group("mcp").Version, err)
		}
	}
	if len(st.events) != 1 {
		t.Errorf("a no-op write was audited: %+v", st.events)
	}
	// The stale version is refused.
	if _, err := s.Update(ctx, admin, "mcp", 1, patch("burst", "6")); !isConflict(err, 1, 2) {
		t.Errorf("stale write: %v", err)
	}
	// null removes the stored value: the default is back.
	snap, err = s.Update(ctx, admin, "mcp", 2, patch("burst", "null"))
	if err != nil {
		t.Fatal(err)
	}
	if c := snap.Config(); c.MCP.Burst != 30 || snap.Source("mcp.burst") != "default" || snap.Group("mcp").Version != 3 {
		t.Errorf("after null: burst %d from %q, version %d", c.MCP.Burst, snap.Source("mcp.burst"), snap.Group("mcp").Version)
	}
	if string(st.rows["mcp"].Values["enabled"]) != "false" || len(st.rows["mcp"].Values) != 1 {
		t.Errorf("stored = %v, want only enabled", st.rows["mcp"].Values)
	}
}

func isConflict(err error, expected, current int64) bool {
	var vc *resource.VersionConflictError
	return errors.As(err, &vc) && vc.Expected == expected && vc.Current == current
}

// reg.Stored values apply at the next start, under the configuration's pins.
func TestOpenAppliesTheStoredLayer(t *testing.T) {
	st := newMemStore()
	st.rows["logging"] = reg.Stored{Name: "logging", Exists: true, Version: 2, Values: patch("level", `"debug"`)}
	st.rows["mcp"] = reg.Stored{Name: "mcp", Exists: true, Version: 4, Values: patch("burst", "9", "requests_per_minute", "60")}
	s := open(t, baseConfig(t, env{"LEDGER_MCP_BURST": "3"}, ""), st)
	c := s.Snapshot().Config()
	if c.Log.Level != "debug" || c.MCP.RequestsPerMinute != 60 || c.MCP.Burst != 3 {
		t.Errorf("effective = level %q, rpm %d, burst %d", c.Log.Level, c.MCP.RequestsPerMinute, c.MCP.Burst)
	}
	v, _ := s.View("mcp", nil)
	if v.Version != 4 || v.Sources["burst"] != "environment" || strings.Join(v.Pinned, ",") != "burst" {
		t.Errorf("view = %+v", v)
	}
	// An invalid stored layer stops startup, naming the key.
	st.rows["logging"] = reg.Stored{Name: "logging", Exists: true, Version: 2, Values: patch("level", `"loud"`)}
	if _, err := Open(context.Background(), baseConfig(t, env{}, ""), st, Options{}); err == nil || !strings.Contains(err.Error(), "log.level") {
		t.Errorf("Open with an invalid stored value: %v", err)
	}
	st.rows["logging"] = reg.Stored{Name: "logging", Exists: true, Version: 2, Values: patch("format", `"text"`)}
	if _, err := Open(context.Background(), baseConfig(t, env{}, ""), st, Options{}); err == nil || !strings.Contains(err.Error(), "logging.format") {
		t.Errorf("Open with a deployment key in the stored layer: %v", err)
	}
	delete(st.rows, "logging")
	st.rows["nope"] = reg.Stored{Name: "nope", Exists: true, Version: 2, Values: patch()}
	if _, err := Open(context.Background(), baseConfig(t, env{}, ""), st, Options{}); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("Open with an unknown group: %v", err)
	}
}

func TestPinnedMembersAreRefused(t *testing.T) {
	ctx := context.Background()
	st := newMemStore()
	s := open(t, baseConfig(t, env{"LEDGER_MCP_BURST": "3"}, "mcp:\n  requests_per_minute: 50\n"), st)
	_, err := s.Update(ctx, admin, "mcp", 1, patch("burst", "4", "requests_per_minute", "null", "max_request_bytes", "4096"))
	var pe *reg.PinnedError
	if !errors.As(err, &pe) || !reflect.DeepEqual(pe.Keys, []reg.PinnedKey{{Key: "mcp.burst", Layer: "environment"}, {Key: "mcp.requests_per_minute", Layer: "yaml"}}) {
		t.Fatalf("err = %v, want both pins with their layers", err)
	}
	if strings.Contains(err.Error(), "4096") || len(st.rows) != 0 {
		t.Errorf("err %q repeats a value, or something was stored", err)
	}
	// Unpinned members of the same group can still be changed.
	if _, err := s.Update(ctx, admin, "mcp", 1, patch("max_request_bytes", "4096")); err != nil {
		t.Errorf("unpinned member: %v", err)
	}
}

// Until their gates exist (S2, S3), sensitive members, secrets and the
// reporting zone are refused, and a refused secret is never repeated.
func TestGatedMembersAreRefused(t *testing.T) {
	ctx := context.Background()
	st := newMemStore()
	s := open(t, baseConfig(t, env{}, ""), st)
	for group, p := range map[string]map[string]json.RawMessage{
		"oidc":                      patch("client_secret", `"`+sentinel+`"`, "issuer", `"https://id.example.com"`),
		"banking":                   patch("redirect_url", `"https://ledger.example.com/x"`, "stale_after", `"48h"`),
		"reporting":                 patch("timezone", `"UTC"`),
		"decision_providers.openai": patch("api_key", `"`+sentinel+`"`),
		"decision_tasks.alerts":     patch("enabled", "true"),
		"auth":                      patch("password.min_length", "16"),
	} {
		_, err := s.Update(ctx, admin, group, 1, p)
		var ve *reg.ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%s: err = %v, want a validation error", group, err)
			continue
		}
		for _, fe := range ve.Fields {
			if fe.Message != reg.MsgNotWritableYet {
				t.Errorf("%s: %s: %q", group, fe.Field, fe.Message)
			}
		}
		if strings.Contains(err.Error(), sentinel) {
			t.Errorf("%s: the error repeats the secret", group)
		}
	}
	if len(st.rows) != 0 || len(st.events) != 0 {
		t.Errorf("a gated write was stored or audited: %v %v", st.rows, st.events)
	}
	// An ordinary member next to the gated one in a mixed group works alone.
	if _, err := s.Update(ctx, admin, "banking", 1, patch("stale_after", `"48h"`)); err != nil {
		t.Errorf("banking.stale_after: %v", err)
	}
	if _, err := s.Update(ctx, admin, "reporting", 1, patch("currency", `"EUR"`)); err != nil {
		t.Errorf("reporting.currency: %v", err)
	}
	// Views never show a secret, even one set in the environment.
	s2 := open(t, baseConfig(t, env{"LEDGER_AUTH_OIDC_CLIENT_SECRET": sentinel, "LEDGER_DECISIONS_PROVIDERS_OPENAI_API_KEY": sentinel}, ""), newMemStore())
	for _, g := range reg.Groups() {
		v, err := s2.View(g.Name, nil)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(fmt.Sprintf("%v", v), sentinel) {
			t.Errorf("view of %s shows a secret", g.Name)
		}
	}
}

func TestWritesAreValidatedAsAWhole(t *testing.T) {
	ctx := context.Background()
	st := newMemStore()
	s := open(t, baseConfig(t, env{}, "rates:\n  stale_after: 24h\n"), st)
	_, err := s.Update(ctx, admin, "rates", 1, patch("refresh_interval", `"48h"`, "timeout", `"1h"`, "bogus", "1", "source", "7"))
	var ve *reg.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v", err)
	}
	fields := map[string]string{}
	for _, fe := range ve.Fields {
		fields[fe.Field] = fe.Message
	}
	if _, ok := fields["bogus"]; !ok || !strings.Contains(fields["rates.source"], "expected a string") {
		t.Errorf("first-pass errors = %v", fields)
	}
	// Once the members parse, the whole configuration is checked: the
	// interval against the configured staleness, the timeout's range.
	_, err = s.Update(ctx, admin, "rates", 1, patch("refresh_interval", `"48h"`, "timeout", `"1h"`))
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v", err)
	}
	fields = map[string]string{}
	for _, fe := range ve.Fields {
		fields[fe.Field] = fe.Message
	}
	if _, ok := fields["rates.refresh_interval"]; !ok {
		t.Errorf("cross-field errors = %v", fields)
	}
	if _, ok := fields["rates.timeout"]; !ok {
		t.Errorf("range errors = %v", fields)
	}
	if len(st.rows) != 0 || s.Snapshot().Config().Rates.Timeout != 15*time.Second {
		t.Error("an invalid write changed something")
	}
	if _, err := s.Update(ctx, admin, "nope", 1, nil); !errors.Is(err, reg.ErrUnknownGroup) {
		t.Errorf("unknown group: %v", err)
	}
}

func TestResetKeepsPinnedStoredValues(t *testing.T) {
	ctx := context.Background()
	st := newMemStore()
	st.rows["mcp"] = reg.Stored{Name: "mcp", Exists: true, Version: 3, Values: patch("burst", "9", "requests_per_minute", "60")}
	s := open(t, baseConfig(t, env{"LEDGER_MCP_BURST": "3"}, ""), st)
	// A group without a row: nothing changes, version 1 stays.
	snap, err := s.Reset(ctx, admin, "logging", 1)
	if err != nil || snap.Group("logging").Version != 1 || len(st.events) != 0 {
		t.Fatalf("reset without a row: %v, version %d", err, snap.Group("logging").Version)
	}
	if _, err := s.Reset(ctx, admin, "mcp", 1); !isConflict(err, 1, 3) {
		t.Errorf("stale reset: %v", err)
	}
	snap, err = s.Reset(ctx, admin, "mcp", 3)
	if err != nil {
		t.Fatal(err)
	}
	if c := snap.Config(); c.MCP.RequestsPerMinute != 120 || c.MCP.Burst != 3 || snap.Group("mcp").Version != 4 {
		t.Errorf("after reset: %+v, version %d", c.MCP, snap.Group("mcp").Version)
	}
	if string(st.rows["mcp"].Values["burst"]) != "9" || len(st.rows["mcp"].Values) != 1 {
		t.Errorf("stored after reset = %v, want the pinned burst kept", st.rows["mcp"].Values)
	}
	if len(st.events) != 1 || st.events[0].Action != audit.SettingsReset || st.events[0].Details["fields"] != "requests_per_minute" {
		t.Errorf("audit = %+v", st.events)
	}
}

type codeErr struct{ code string }

func (e codeErr) Error() string {
	return "rebuild failed for https://secret.example.com/?token=" + sentinel
}
func (e codeErr) ApplyCode() string { return e.code }

// Subscribers run after the commit with the new snapshot; a failure is
// reported as a code, never a value, and does not roll the write back
// (ADR-0066 points 15 and 17).
func TestSubscriptionsAndApplyState(t *testing.T) {
	ctx := context.Background()
	st := newMemStore()
	s := open(t, baseConfig(t, env{}, ""), st)
	var got []string
	cancel := s.Subscribe(func(_ context.Context, snap *Snapshot) error {
		got = append(got, snap.Config().Log.Level)
		if snap.Config().Log.Level == "error" {
			return codeErr{"logger_rebuild_failed"}
		}
		return nil
	}, "logging")
	if _, err := s.Update(ctx, admin, "logging", 1, patch("level", `"debug"`)); err != nil {
		t.Fatal(err)
	}
	if st := s.ApplyState("logging"); st.State != Applied || !st.At.Equal(t0) {
		t.Errorf("apply state = %+v", st)
	}
	// Another group's write does not notify the logging subscriber.
	if _, err := s.Update(ctx, admin, "mcp", 1, patch("burst", "4")); err != nil {
		t.Fatal(err)
	}
	if st := s.ApplyState("mcp"); st.State != RestartRequired {
		t.Errorf("mcp has no live consumer yet: state %+v", st)
	}
	if _, err := s.Update(ctx, admin, "logging", 2, patch("level", `"error"`)); err != nil {
		t.Fatal(err)
	}
	st2 := s.ApplyState("logging")
	if st2.State != Failed || st2.Code != "logger_rebuild_failed" {
		t.Errorf("apply state = %+v", st2)
	}
	if s.Snapshot().Config().Log.Level != "error" {
		t.Error("a failed apply rolled the setting back")
	}
	v, _ := s.View("logging", nil)
	if strings.Contains(fmt.Sprintf("%+v", v), sentinel) {
		t.Error("the view repeats the applier's error")
	}
	if strings.Join(got, ",") != "debug,error" {
		t.Errorf("subscriber saw %v", got)
	}
	// A code that is not a plain word is replaced.
	s.Subscribe(func(context.Context, *Snapshot) error { return codeErr{"Bad Code " + sentinel} }, "logging")
	cancel()
	if _, err := s.Update(ctx, admin, "logging", 3, patch("level", `"warn"`)); err != nil {
		t.Fatal(err)
	}
	if st := s.ApplyState("logging"); st.Code != "apply_failed" || len(got) != 2 {
		t.Errorf("apply state = %+v, cancelled subscriber ran: %v", st, got)
	}
}

// A store failure after validation (the commit failing) publishes nothing.
func TestFailedCommitPublishesNothing(t *testing.T) {
	st := newMemStore()
	st.fail = errors.New("disk full")
	s := open(t, baseConfig(t, env{}, ""), st)
	before := s.Snapshot()
	if _, err := s.Update(context.Background(), admin, "mcp", 1, patch("burst", "4")); err == nil {
		t.Fatal("the write succeeded")
	}
	if s.Snapshot() != before {
		t.Error("a snapshot was published for a failed commit")
	}
}

// Requests keep the snapshot they started with: a snapshot never changes.
func TestSnapshotsAreImmutable(t *testing.T) {
	s := open(t, baseConfig(t, env{}, ""), newMemStore())
	snap := s.Snapshot()
	c := snap.Config()
	c.Auth.OIDC.Scopes[0] = "changed"
	if snap.Config().Auth.OIDC.Scopes[0] != "openid" {
		t.Error("changing a copy changed the snapshot")
	}
	if _, err := s.Update(context.Background(), admin, "mcp", 1, patch("burst", "4")); err != nil {
		t.Fatal(err)
	}
	if snap.Config().MCP.Burst != 30 || s.Snapshot().Config().MCP.Burst != 4 {
		t.Error("an old snapshot changed with the write")
	}
}
