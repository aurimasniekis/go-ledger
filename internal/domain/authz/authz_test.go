package authz

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

func TestParseGrant(t *testing.T) {
	for _, s := range []string{"users:read", "users:write", "audit:read"} {
		g, err := ParseGrant(s)
		if err != nil || g.String() != s {
			t.Errorf("ParseGrant(%q) = %v, %v", s, g, err)
		}
	}
	for _, s := range []string{"", "users", "users:", ":read", "users:delete", "audit:write", "*:read", "users:*", "Users:read", "users:read:x"} {
		if _, err := ParseGrant(s); !errors.Is(err, ErrUnknownGrant) {
			t.Errorf("ParseGrant(%q) = %v, want ErrUnknownGrant", s, err)
		}
	}
}

func TestGrantJSON(t *testing.T) {
	gs := Grants{G(ResourceUsers, ActionWrite)}
	b, err := json.Marshal(gs)
	if err != nil || string(b) != `["users:write"]` {
		t.Fatalf("marshal: %s %v", b, err)
	}
	var back Grants
	if err := json.Unmarshal([]byte(`["users:read","audit:read"]`), &back); err != nil || len(back) != 2 {
		t.Fatalf("unmarshal: %v %v", back, err)
	}
	if err := json.Unmarshal([]byte(`["users:delete"]`), &back); err == nil {
		t.Fatal("unknown grant decoded")
	}
	if _, err := json.Marshal(Grants{G("nope", ActionRead)}); err == nil {
		t.Fatal("invalid grant encoded")
	}
}

func TestWriteImpliesReadOnly(t *testing.T) {
	s, err := NewGrants(G(ResourceUsers, ActionWrite))
	if err != nil {
		t.Fatal(err)
	}
	if !s.Allows(G(ResourceUsers, ActionRead)) || !s.Allows(G(ResourceUsers, ActionWrite)) {
		t.Fatal("users:write must allow users read and write")
	}
	if s.Allows(G(ResourceAudit, ActionRead)) {
		t.Fatal("users:write must not allow another resource")
	}
	r, _ := NewGrants(G(ResourceUsers, ActionRead))
	if r.Allows(G(ResourceUsers, ActionWrite)) {
		t.Fatal("read must not allow write")
	}
	if AllGrants().Allows(G("nope", ActionRead)) {
		t.Fatal("an invalid grant is never allowed")
	}
}

func TestNewGrantsNormalizesAndValidates(t *testing.T) {
	s, err := NewGrants(G(ResourceUsers, ActionWrite), G(ResourceAudit, ActionRead), G(ResourceUsers, ActionWrite))
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Strings(); !slices.Equal(got, []string{"audit:read", "users:write"}) {
		t.Fatalf("got %v", got)
	}
	if _, err := NewGrants(G(ResourceAudit, ActionWrite)); !errors.Is(err, ErrUnknownGrant) {
		t.Fatalf("audit:write accepted: %v", err)
	}
}

func TestRoleGrants(t *testing.T) {
	admin := RoleAdministrator.Grants()
	if !slices.Equal(admin, AllGrants()) {
		t.Fatalf("administrator = %v, want every grant", admin)
	}
	ro := RoleReadOnly.Grants()
	for _, g := range ro {
		if g.Action != ActionRead {
			t.Errorf("read-only role holds %s", g)
		}
	}
	for _, ri := range Resources() {
		for _, a := range ri.Actions {
			g := G(ri.Name, a)
			want := a == ActionRead && !ri.AdminOnly
			if got := ro.Allows(g); got != want {
				t.Errorf("read-only allows %s = %v, want %v", g, got, want)
			}
		}
	}
	if Role("owner").Grants() != nil || Role("owner").Valid() {
		t.Fatal("unknown role must hold nothing")
	}
	for _, r := range Roles() {
		if !r.Valid() {
			t.Errorf("%s not valid", r)
		}
	}
}

func TestEscalationCheck(t *testing.T) {
	ro := RoleReadOnly.Grants()
	if ro.Covers(Grants{G(ResourceUsers, ActionWrite)}) {
		t.Fatal("read-only creator covers users:write")
	}
	if !RoleAdministrator.Grants().Covers(AllGrants()) {
		t.Fatal("administrator must cover every grant")
	}
	w := Grants{G(ResourceUsers, ActionWrite)}
	if !w.Covers(Grants{G(ResourceUsers, ActionRead)}) {
		t.Fatal("write must cover read")
	}
}

func TestTokenPrincipalIsLimitedByOwnerRole(t *testing.T) {
	tok := Grants{G(ResourceUsers, ActionWrite), G(ResourceAudit, ActionRead)}
	p := TokenPrincipal("T", "U", RoleAdministrator, tok)
	if !p.Allows(G(ResourceUsers, ActionWrite)) || p.Kind != PrincipalToken || p.TokenID != "T" || p.UserID != "U" {
		t.Fatalf("admin-owned token: %+v", p)
	}
	if p.Allows(G("nope", ActionRead)) {
		t.Fatal("invalid grant allowed")
	}
	// The owner is demoted: the token keeps only what read-only allows.
	p = TokenPrincipal("T", "U", RoleReadOnly, tok)
	if p.Allows(G(ResourceUsers, ActionWrite)) || p.Allows(G(ResourceUsers, ActionRead)) || p.Allows(G(ResourceAudit, ActionRead)) {
		t.Fatalf("demoted owner's token still holds %v", p.Grants)
	}
	// A token never gains grants it was not given.
	p = TokenPrincipal("T", "U", RoleAdministrator, Grants{G(ResourceAudit, ActionRead)})
	if p.Allows(G(ResourceUsers, ActionRead)) {
		t.Fatal("token gained a grant from its owner")
	}
}

func TestPrincipalContext(t *testing.T) {
	if _, ok := FromContext(context.Background()); ok {
		t.Fatal("empty context has a principal")
	}
	p := UserPrincipal("U", RoleReadOnly)
	got, ok := FromContext(WithPrincipal(context.Background(), p))
	if !ok || got.UserID != "U" || got.Kind != PrincipalUser || !slices.Equal(got.Grants, RoleReadOnly.Grants()) {
		t.Fatalf("got %+v %v", got, ok)
	}
}

func TestResourcesIsACopy(t *testing.T) {
	rs := Resources()
	rs[0].Actions[0] = "x"
	rs[0].Name = "x"
	if Resources()[0].Name == "x" || Resources()[0].Actions[0] == "x" {
		t.Fatal("Resources exposes the vocabulary")
	}
	if !slices.IsSortedFunc(Resources(), func(a, b ResourceInfo) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	}) {
		t.Fatal("vocabulary not sorted by name")
	}
}
