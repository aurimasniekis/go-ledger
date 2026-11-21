package tokens

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/domain/users"
)

// memStore is an in-memory [Store] (domain tests cannot import the SQLite store).
type memStore struct {
	mu      sync.Mutex
	tokens  map[string]Token
	events  []audit.Event
	touches int
}

func newMemStore() *memStore { return &memStore{tokens: map[string]Token{}} }

func (m *memStore) Create(_ context.Context, t Token, events ...audit.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens[t.ID] = t
	m.events = append(m.events, events...)
	return nil
}

func (m *memStore) ByHash(_ context.Context, hash string) (Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tokens {
		if t.TokenHash == hash {
			return t, nil
		}
	}
	return Token{}, resource.ErrNotFound
}

func (m *memStore) Get(_ context.Context, id string) (Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tokens[id]
	if !ok {
		return Token{}, resource.ErrNotFound
	}
	return t, nil
}

func (m *memStore) List(context.Context, ListFilter) ([]Token, error) { return nil, nil }

func (m *memStore) Revoke(_ context.Context, id, owner string, at time.Time, events ...audit.Event) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tokens[id]
	if !ok || owner != "" && t.OwnerID != owner {
		return false, resource.ErrNotFound
	}
	if t.RevokedAt != nil {
		return false, nil
	}
	t.RevokedAt = &at
	m.tokens[id] = t
	m.events = append(m.events, events...)
	return true, nil
}

func (m *memStore) Touch(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.tokens[id]
	t.LastUsedAt = &at
	m.tokens[id] = t
	m.touches++
	return nil
}

// memUsers maps user IDs to roles; a user missing from roles is unknown.
// root is the root account ("" for none).
type memUsers struct {
	roles    map[string]authz.Role
	disabled map[string]bool
	root     string
}

func (u *memUsers) RootAccountID(context.Context) (string, error) {
	if u.root == "" {
		return "", resource.ErrNotFound
	}
	return u.root, nil
}

func (u *memUsers) Principal(_ context.Context, id string) (authz.Principal, error) {
	r, ok := u.roles[id]
	switch {
	case !ok:
		return authz.Principal{}, resource.ErrNotFound
	case u.disabled[id]:
		return authz.Principal{}, users.ErrDisabled
	}
	return authz.UserPrincipal(id, r), nil
}

const (
	adminID      = "01JD3Y4C8V9M2Q7W5R6T0XZBNA"
	readerID     = "01JD3Y4C8V9M2Q7W5R6T0XZBNB"
	otherAdminID = "01JD3Y4C8V9M2Q7W5R6T0XZBNC"
)

type env struct {
	svc   *Service
	store *memStore
	users *memUsers
	now   time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{store: newMemStore(), users: &memUsers{roles: map[string]authz.Role{adminID: authz.RoleAdministrator, readerID: authz.RoleReadOnly}, disabled: map[string]bool{}, root: adminID},
		now: time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)}
	n := 0
	e.svc = NewService(e.store, e.users, Options{Now: func() time.Time { return e.now }, NewID: func() string {
		n++
		return "01JD3Y4C8V9M2Q7W5R6T0XZC" + string("0123456789ABCDEFGHJKMNPQRSTVWXYZ"[n/32]) + string("0123456789ABCDEFGHJKMNPQRSTVWXYZ"[n%32])
	}})
	return e
}

var (
	commitmentsRead  = authz.G(authz.ResourceCommitments, authz.ActionRead)
	commitmentsWrite = authz.G(authz.ResourceCommitments, authz.ActionWrite)
	usersWrite       = authz.G(authz.ResourceUsers, authz.ActionWrite)
)

func (e *env) create(t *testing.T, owner string, n New) Issued {
	t.Helper()
	p, err := e.users.Principal(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	iss, err := e.svc.Create(context.Background(), audit.ActorOf(p), p, n)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return iss
}

var tokenFormat = regexp.MustCompile(`^ldgr_[0-9A-Za-z]{43}$`)

func TestTokenFormatAndStorage(t *testing.T) {
	e := newEnv(t)
	seen := map[string]bool{}
	for range 50 {
		iss := e.create(t, adminID, New{Name: "ci", Grants: authz.Grants{commitmentsRead}})
		s := iss.Secret
		if !tokenFormat.MatchString(s) || !WellFormed(s) || seen[s] {
			t.Fatalf("token %q: format %v, repeated %v", s[:9], tokenFormat.MatchString(s), seen[s])
		}
		seen[s] = true
		tk := iss.Token
		if tk.TokenHash != Hash(s) || len(tk.TokenHash) != 64 || tk.DisplayPrefix != s[:9] || tk.LastChars != s[len(s)-4:] || tk.Hint() != s[:9]+"…"+s[len(s)-4:] {
			t.Fatalf("stored fields do not match the token: %+v", tk)
		}
		if strings.Contains(tk.TokenHash, s[5:]) {
			t.Fatal("the hash contains the token")
		}
	}
	for _, bad := range []string{"", "ldgr_", "ldgr_" + strings.Repeat("a", 42), "ldgr_" + strings.Repeat("a", 44), "LDGR_" + strings.Repeat("a", 43),
		"ldgr_" + strings.Repeat("a", 42) + "-", "xxxx_" + strings.Repeat("a", 43)} {
		if WellFormed(bad) {
			t.Errorf("%q is well formed", bad)
		}
	}
}

func TestCreateRefusesEscalation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	reader, _ := e.users.Principal(ctx, readerID)
	admin, _ := e.users.Principal(ctx, adminID)
	cases := []struct {
		name    string
		by      authz.Principal
		n       New
		missing []string
	}{
		{"read-only asks for a write grant", reader, New{Name: "x", Grants: authz.Grants{commitmentsWrite}}, []string{"commitments:write"}},
		{"read-only asks for an admin-only read", reader, New{Name: "x", Grants: authz.Grants{authz.G(authz.ResourceAudit, authz.ActionRead), commitmentsRead}}, []string{"audit:read"}},
		{"read-only asks for root", reader, New{Name: "x", Root: true}, []string{"root"}},
		{"narrowed principal asks for root", authz.Principal{Kind: authz.PrincipalUser, UserID: adminID, Role: authz.RoleAdministrator, Grants: authz.Grants{commitmentsWrite}},
			New{Name: "x", Root: true}, []string{"root"}},
	}
	for _, c := range cases {
		_, err := e.svc.Create(ctx, audit.ActorOf(c.by), c.by, c.n)
		var ee *EscalationError
		if !errors.As(err, &ee) || !errors.Is(err, ErrEscalation) || !slices.Equal(ee.Grants, c.missing) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	token := authz.TokenPrincipal("01JD3Y4C8V9M2Q7W5R6T0XZBNT", adminID, authz.RoleAdministrator, authz.AllGrants())
	if _, err := e.svc.Create(ctx, audit.ActorOf(token), token, New{Name: "x", Grants: authz.Grants{commitmentsRead}}); !errors.Is(err, ErrTokenCannotCreate) {
		t.Errorf("a token created a token: %v", err)
	}
	if len(e.store.tokens) != 0 || len(e.store.events) != 0 {
		t.Fatalf("refused creations stored %d tokens, %d events", len(e.store.tokens), len(e.store.events))
	}
	// Within the creator's grants is fine; write implies read.
	if _, err := e.svc.Create(ctx, audit.ActorOf(reader), reader, New{Name: "x", Grants: authz.Grants{commitmentsRead}}); err != nil {
		t.Errorf("read-only reader token: %v", err)
	}
	if _, err := e.svc.Create(ctx, audit.ActorOf(admin), admin, New{Name: "x", Root: true}); err != nil {
		t.Errorf("administrator root token: %v", err)
	}
}

func TestCreateValidates(t *testing.T) {
	e := newEnv(t)
	admin, _ := e.users.Principal(context.Background(), adminID)
	past := e.now.Add(-time.Second)
	beyond := time.Date(9999, 12, 31, 23, 59, 59, 0, time.FixedZone("west", -3600))
	cases := map[string]struct {
		n     New
		field string
	}{
		"no name":           {New{Grants: authz.Grants{commitmentsRead}}, "name"},
		"long name":         {New{Name: strings.Repeat("n", MaxNameBytes+1), Grants: authz.Grants{commitmentsRead}}, "name"},
		"control character": {New{Name: "a\nb", Grants: authz.Grants{commitmentsRead}}, "name"},
		"padded name":       {New{Name: " a", Grants: authz.Grants{commitmentsRead}}, "name"},
		"no grants":         {New{Name: "a"}, "grants"},
		"root with grants":  {New{Name: "a", Root: true, Grants: authz.Grants{commitmentsRead}}, "grants"},
		"unknown grant":     {New{Name: "a", Grants: authz.Grants{{Resource: "nope", Action: authz.ActionRead}}}, "grants"},
		"expired":           {New{Name: "a", Grants: authz.Grants{commitmentsRead}, ExpiresAt: &past}, "expires_at"},
		"year 10000 in UTC": {New{Name: "a", Grants: authz.Grants{commitmentsRead}, ExpiresAt: &beyond}, "expires_at"},
	}
	for name, c := range cases {
		_, err := e.svc.Create(context.Background(), audit.ActorOf(admin), admin, c.n)
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Field != c.field {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestCreatedEventCarriesNoSecret(t *testing.T) {
	e := newEnv(t)
	exp := e.now.Add(24 * time.Hour)
	iss := e.create(t, adminID, New{Name: "deploy", Grants: authz.Grants{commitmentsWrite, usersWrite}, ExpiresAt: &exp})
	if len(e.store.events) != 1 {
		t.Fatalf("events: %+v", e.store.events)
	}
	ev := e.store.events[0]
	want := map[string]string{"grants": "commitments:write,users:write", "root": "false", "expires_at": "2026-11-22T09:00:00Z"}
	if ev.Action != audit.TokenCreated || ev.TargetType != "token" || ev.TargetID != iss.Token.ID || ev.Actor.UserID != adminID || len(ev.Details) != 3 {
		t.Fatalf("event %+v", ev)
	}
	for k, v := range want {
		if ev.Details[k] != v {
			t.Errorf("detail %s = %q, want %q", k, ev.Details[k], v)
		}
	}
	if err := audit.Validate(ev); err != nil {
		t.Fatal(err)
	}
	root := e.create(t, adminID, New{Name: "root", Root: true})
	if ev := e.store.events[1]; ev.Details["root"] != "true" || ev.Details["grants"] != "" || ev.Details["expires_at"] != "never" || root.Token.ExpiresAt != nil {
		t.Errorf("root event %+v", ev)
	}
}

func TestJoinGrantsFitsTheAuditLimit(t *testing.T) {
	var gs authz.Grants
	for i := range 40 {
		gs = append(gs, authz.Grant{Resource: authz.Resource("resource" + string(rune('a'+i%26)) + string(rune('a'+i/26))), Action: authz.ActionWrite})
	}
	s := joinGrants(gs)
	if len(s) > audit.MaxValueBytes || !strings.HasSuffix(s, ",…") || strings.Contains(strings.TrimSuffix(s, ",…"), "…") {
		t.Fatalf("%d bytes: %q", len(s), s)
	}
}

func TestAuthenticate(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	iss := e.create(t, adminID, New{Name: "ci", Grants: authz.Grants{commitmentsWrite, usersWrite}})
	p, err := e.svc.Authenticate(ctx, iss.Secret)
	if err != nil || p.Kind != authz.PrincipalToken || p.TokenID != iss.Token.ID || p.UserID != adminID || !p.Allows(usersWrite) || p.Allows(authz.G(authz.ResourceAudit, authz.ActionRead)) {
		t.Fatalf("principal %+v %v", p, err)
	}
	// Demoting the owner narrows the token on the next request: the
	// read-only role allows neither write grant (authz.Grants.Intersect keeps
	// only grants the role allows as they are).
	e.users.roles[adminID] = authz.RoleReadOnly
	if p, err := e.svc.Authenticate(ctx, iss.Secret); err != nil || len(p.Grants) != 0 || p.Role != authz.RoleReadOnly {
		t.Fatalf("demoted owner: %+v %v", p.Grants, err)
	}
	e.users.roles[adminID] = authz.RoleAdministrator
	// Disabling the owner stops it.
	e.users.disabled[adminID] = true
	if _, err := e.svc.Authenticate(ctx, iss.Secret); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("disabled owner: %v", err)
	}
	e.users.disabled[adminID] = false
	// Unknown, malformed and altered tokens are all the same error.
	altered := iss.Secret[:len(iss.Secret)-1] + map[bool]string{true: "b", false: "a"}[strings.HasSuffix(iss.Secret, "a")]
	for _, bad := range []string{"", "Bearer " + iss.Secret, iss.Secret + " ", altered, "ldgr_" + strings.Repeat("Z", 43)} {
		if _, err := e.svc.Authenticate(ctx, bad); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("token %d chars: %v", len(bad), err)
		}
	}
	// Revocation applies to the next request; revoking again is a no-op.
	if err := e.svc.Revoke(ctx, audit.Actor{Kind: audit.ActorUser, UserID: adminID}, iss.Token.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(ctx, iss.Secret); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("revoked: %v", err)
	}
	before := len(e.store.events)
	if err := e.svc.Revoke(ctx, audit.Actor{Kind: audit.ActorUser, UserID: adminID}, iss.Token.ID, ""); err != nil || len(e.store.events) != before {
		t.Fatalf("second revoke: %v, %d new events", err, len(e.store.events)-before)
	}
	if err := e.svc.Revoke(ctx, audit.Actor{Kind: audit.ActorUser, UserID: readerID}, iss.Token.ID, readerID); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("revoking another owner's token: %v", err)
	}
}

func TestRootTokenFollowsTheOwnersRole(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	iss := e.create(t, adminID, New{Name: "root", Root: true})
	p, err := e.svc.Authenticate(ctx, iss.Secret)
	if err != nil || !slices.Equal(p.Grants, authz.AllGrants()) {
		t.Fatalf("root: %v %v", p.Grants, err)
	}
	e.users.roles[adminID] = authz.RoleReadOnly
	if p, err := e.svc.Authenticate(ctx, iss.Secret); err != nil || !slices.Equal(p.Grants, authz.RoleReadOnly.Grants()) {
		t.Fatalf("root of a demoted owner: %v %v", p.Grants, err)
	}
}

func TestExpiryAndLastUsed(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	exp := e.now.Add(time.Hour)
	iss := e.create(t, adminID, New{Name: "ci", Grants: authz.Grants{commitmentsRead}, ExpiresAt: &exp})
	if _, err := e.svc.Authenticate(ctx, iss.Secret); err != nil || e.store.touches != 1 {
		t.Fatalf("first use: %v, %d touches", err, e.store.touches)
	}
	e.now = e.now.Add(30 * time.Second)
	if _, err := e.svc.Authenticate(ctx, iss.Secret); err != nil || e.store.touches != 1 {
		t.Fatalf("second use within a minute: %v, %d touches", err, e.store.touches)
	}
	e.now = e.now.Add(30 * time.Second)
	if _, err := e.svc.Authenticate(ctx, iss.Secret); err != nil || e.store.touches != 2 {
		t.Fatalf("use a minute later: %v, %d touches", err, e.store.touches)
	}
	tk, _ := e.store.Get(ctx, iss.Token.ID)
	if tk.Status(e.now) != StatusActive || tk.LastUsedAt == nil || !tk.LastUsedAt.Equal(e.now) {
		t.Fatalf("token %+v", tk)
	}
	e.now = exp
	if _, err := e.svc.Authenticate(ctx, iss.Secret); !errors.Is(err, ErrInvalidToken) || tk.Status(e.now) != StatusExpired {
		t.Fatalf("at expiry: %v %s", err, tk.Status(e.now))
	}
}

// slowStore makes Touch behave like a writer that is held: it waits for its
// context and fails, as the real store does behind a long write transaction.
type slowStore struct {
	*memStore
	calls atomic.Int32
}

func (s *slowStore) Touch(ctx context.Context, _ string, _ time.Time) error {
	s.calls.Add(1)
	<-ctx.Done()
	return ctx.Err()
}

// TestAuthenticateSurvivesAFailedLastUseWrite: a last-use write that cannot
// finish neither fails nor holds up the request, and is logged.
func TestAuthenticateSurvivesAFailedLastUseWrite(t *testing.T) {
	e := newEnv(t)
	iss := e.create(t, adminID, New{Name: "ci", Grants: authz.Grants{commitmentsRead}})
	var logs bytes.Buffer
	st := &slowStore{memStore: e.store}
	svc := NewService(st, e.users, Options{Now: func() time.Time { return e.now }, TouchTimeout: 20 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	start := time.Now()
	if _, err := svc.Authenticate(context.Background(), iss.Secret); err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("request waited %s", d)
	}
	if !strings.Contains(logs.String(), "last-use update skipped") || strings.Contains(logs.String(), iss.Secret) {
		t.Fatalf("log: %s", logs.String())
	}
}

// ctxStore records whether a last-use write was given a live context.
type ctxStore struct {
	*memStore
	cancelled chan error
}

func (s *ctxStore) Touch(ctx context.Context, id string, at time.Time) error {
	s.cancelled <- ctx.Err()
	return s.memStore.Touch(ctx, id, at)
}

// TestLastUseOutlivesTheRequest: the write is not tied to the request's
// context, so a request that is cancelled before or during Authenticate still
// records its use.
func TestLastUseOutlivesTheRequest(t *testing.T) {
	e := newEnv(t)
	iss := e.create(t, adminID, New{Name: "ci", Grants: authz.Grants{commitmentsRead}})
	st := &ctxStore{memStore: e.store, cancelled: make(chan error, 1)}
	svc := NewService(st, e.users, Options{Now: func() time.Time { return e.now }, TouchTimeout: time.Second,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.Authenticate(ctx, iss.Secret); err != nil {
		t.Fatalf("authenticate with a cancelled request: %v", err)
	}
	if err := <-st.cancelled; err != nil {
		t.Fatalf("last-use write got a dead context: %v", err)
	}
	if tk, _ := e.store.Get(context.Background(), iss.Token.ID); tk.LastUsedAt == nil {
		t.Fatal("the cancelled request's use was not recorded")
	}
}

// TestExpiryProblem pins the rule Create and the CLI share.
func TestExpiryProblem(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	beyond := time.Date(9999, 12, 31, 23, 59, 59, 0, time.FixedZone("west", -3600))
	for name, c := range map[string]struct {
		at   time.Time
		want string
	}{
		"now":               {now, "must be in the future"},
		"future":            {now.Add(time.Hour), ""},
		"last UTC instant":  {time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC), ""},
		"year 10000 in UTC": {beyond, "must be before the year 10000 (UTC)"},
	} {
		if got := ExpiryProblem(c.at, now); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

// TestLastUseIsSingleFlight: concurrent requests of one token issue one write.
func TestLastUseIsSingleFlight(t *testing.T) {
	e := newEnv(t)
	iss := e.create(t, adminID, New{Name: "ci", Grants: authz.Grants{commitmentsRead}})
	st := &slowStore{memStore: e.store}
	svc := NewService(st, e.users, Options{Now: func() time.Time { return e.now }, TouchTimeout: 300 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.Authenticate(context.Background(), iss.Secret); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := st.calls.Load(); n != 1 {
		t.Fatalf("%d last-use writes for concurrent requests, want 1", n)
	}
}

// TestOnlyRootTokensOfTheRootAccountCreate is decision D25 at the service:
// Authenticate marks only a root token of the root account as a token
// creator, and Create checks it again from the stored token and the current
// root account, so a forged flag does not help.
func TestOnlyRootTokensOfTheRootAccountCreate(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.users.roles[otherAdminID] = authz.RoleAdministrator
	rootTok := e.create(t, adminID, New{Name: "terraform", Root: true})
	narrow := e.create(t, adminID, New{Name: "ci", Grants: authz.AllGrants()})
	otherRoot := e.create(t, otherAdminID, New{Name: "other", Root: true})
	auth := func(iss Issued) authz.Principal {
		t.Helper()
		p, err := e.svc.Authenticate(ctx, iss.Secret)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := auth(rootTok)
	if !p.CanCreateTokens {
		t.Fatal("the root account's root token is not marked as a token creator")
	}
	for name, iss := range map[string]Issued{"non-root token": narrow, "another administrator's root token": otherRoot} {
		if auth(iss).CanCreateTokens {
			t.Errorf("%s is marked as a token creator", name)
		}
	}
	events := len(e.store.events)
	child, err := e.svc.Create(ctx, audit.ActorOf(p), p, New{Name: "child", Grants: authz.Grants{commitmentsRead}})
	if err != nil {
		t.Fatalf("root token creates: %v", err)
	}
	if child.Token.OwnerID != adminID || len(e.store.events) != events+1 {
		t.Fatalf("child %+v, %d new events", child.Token, len(e.store.events)-events)
	}
	ev := e.store.events[len(e.store.events)-1]
	if ev.Actor.Kind != audit.ActorToken || ev.Actor.TokenID != rootTok.Token.ID || ev.Details["parent_token_id"] != rootTok.Token.ID || audit.Validate(ev) != nil {
		t.Fatalf("token.created by a token: %+v (%v)", ev, audit.Validate(ev))
	}
	if _, err := e.svc.Create(ctx, audit.ActorOf(p), p, New{Name: "root2", Root: true}); err != nil {
		t.Fatalf("root token creates a root token: %v", err)
	}

	// A forged flag on any other token is refused from current data.
	for name, iss := range map[string]Issued{"non-root token": narrow, "another administrator's root token": otherRoot, "child": child} {
		q := auth(iss)
		q.CanCreateTokens = true
		if _, err := e.svc.Create(ctx, audit.ActorOf(q), q, New{Name: "x", Grants: authz.Grants{commitmentsRead}}); !errors.Is(err, ErrTokenCannotCreate) {
			t.Errorf("forged %s: %v", name, err)
		}
	}
	unknown := authz.TokenPrincipal("01JD3Y4C8V9M2Q7W5R6T0XZBNT", adminID, authz.RoleAdministrator, authz.AllGrants())
	unknown.CanCreateTokens = true
	if _, err := e.svc.Create(ctx, audit.ActorOf(unknown), unknown, New{Name: "x", Grants: authz.Grants{commitmentsRead}}); !errors.Is(err, ErrTokenCannotCreate) {
		t.Errorf("unknown token: %v", err)
	}
	// A principal that authenticated before the token was revoked is refused.
	if err := e.svc.Revoke(ctx, audit.System, rootTok.Token.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Create(ctx, audit.ActorOf(p), p, New{Name: "x", Grants: authz.Grants{commitmentsRead}}); !errors.Is(err, ErrTokenCannotCreate) {
		t.Errorf("revoked root token: %v", err)
	}
	// With no root account at all, no token creates.
	e.users.root = ""
	r2 := e.create(t, adminID, New{Name: "r", Root: true})
	if q := auth(r2); q.CanCreateTokens {
		t.Error("a root token is a creator without a root account")
	}
}
