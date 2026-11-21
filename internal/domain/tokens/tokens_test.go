package tokens

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"sync"
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
type memUsers struct {
	roles    map[string]authz.Role
	disabled map[string]bool
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
	adminID  = "01JD3Y4C8V9M2Q7W5R6T0XZBNA"
	readerID = "01JD3Y4C8V9M2Q7W5R6T0XZBNB"
)

type env struct {
	svc   *Service
	store *memStore
	users *memUsers
	now   time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{store: newMemStore(), users: &memUsers{roles: map[string]authz.Role{adminID: authz.RoleAdministrator, readerID: authz.RoleReadOnly}, disabled: map[string]bool{}},
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
	if _, err := e.svc.Create(ctx, audit.ActorOf(token), token, New{Name: "x", Grants: authz.Grants{commitmentsRead}}); !errors.Is(err, ErrNotUser) {
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
