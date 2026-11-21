package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
)

// TestOnlyTheRootTokenOrASessionDemotesTheRootAccount is decision D34 for the
// users routes: a token that is not the root token of the root account may not
// demote or disable the root (first) account. It gets 403 root_token_required,
// and nothing changes. Other users, and no-op changes, stay open.
func TestOnlyTheRootTokenOrASessionDemotesTheRootAccount(t *testing.T) {
	e := newSignInEnv(t, nil)
	e.addAdministrator("erin")
	ctx := context.Background()
	alice, _ := e.signIn("alice")
	rootTok := e.createToken(alice, `{"name":"terraform","root":true}`)
	narrow := e.createToken(alice, `{"name":"um","grants":["users:read","users:write"]}`)
	erin, _ := e.signIn("erin")
	erinRoot := e.createToken(erin, `{"name":"other-root","root":true}`)
	aliceUser := func() (authz.Role, bool, int64) {
		u, err := e.parts.users.Get(ctx, e.ids["alice"])
		if err != nil {
			t.Fatal(err)
		}
		return u.Role, u.Disabled, u.Version
	}
	patch := func(secret, body string) (int, string) {
		w := e.req("PATCH", "/api/v1/users/"+e.ids["alice"], body, nil, bearer(secret))
		return w.Code, w.Body.String()
	}
	for who, secret := range map[string]string{
		"a users:write token":                narrow.Secret,
		"another administrator's root token": erinRoot.Secret,
	} {
		for name, body := range map[string]string{"demote": `{"role":"read-only","version":%d}`, "disable": `{"disabled":true,"version":%d}`} {
			_, _, v := aliceUser()
			if code, out := patch(secret, fmt.Sprintf(body, v)); code != 403 || !strings.Contains(out, "root_token_required") {
				t.Errorf("%s, %s the root account: %d %s", who, name, code, out)
			}
		}
		// A stale version is a conflict, not a verdict (the check is pinned to the version).
		_, _, v := aliceUser()
		if code, out := patch(secret, fmt.Sprintf(`{"disabled":true,"version":%d}`, v+5)); code != 409 {
			t.Errorf("%s, stale version: %d %s", who, code, out)
		}
	}
	if role, dis, _ := aliceUser(); role != authz.RoleAdministrator || dis {
		t.Fatalf("a refused request changed the root account: %s disabled=%v", role, dis)
	}

	// M6: re-sending disabled:false to an enabled administrator is a no-op that stays open.
	_, _, v := aliceUser()
	if code, out := patch(narrow.Secret, fmt.Sprintf(`{"disabled":false,"version":%d}`, v)); code != 200 {
		t.Errorf("disabled:false on an enabled administrator: %d %s", code, out)
	}
	e2, ev := e.ids["erin"], int64(0)
	if u, err := e.parts.users.Get(ctx, e2); err == nil {
		ev = u.Version
	}
	if w := e.req("PATCH", "/api/v1/users/"+e2, fmt.Sprintf(`{"disabled":false,"version":%d}`, ev), nil, bearer(narrow.Secret)); w.Code != 200 {
		t.Errorf("disabled:false on an enabled administrator (erin): %d %s", w.Code, w.Body.String())
	}
	// A no-op role and a profile edit on the root account stay open.
	_, _, v = aliceUser()
	if code, out := patch(narrow.Secret, fmt.Sprintf(`{"role":"administrator","display_name":"Alice","version":%d}`, v)); code != 200 {
		t.Errorf("profile edit of the root account: %d %s", code, out)
	}

	// The root token demotes the root account (last step: it then stops being a root token).
	_, _, v = aliceUser()
	if code, out := patch(rootTok.Secret, fmt.Sprintf(`{"role":"read-only","version":%d}`, v)); code != 200 {
		t.Fatalf("root token demotes the root account: %d %s", code, out)
	}
	if role, _, _ := aliceUser(); role != authz.RoleReadOnly {
		t.Fatalf("root token's demotion did not apply: %s", role)
	}
}

// TestOnlyTheRootTokenOrASessionRevokesTheRootToken is decision D34 for
// DELETE /tokens/{id}: revoking the root token of the root account (and so its
// children) needs that token or a session. Other tokens get 403.
func TestOnlyTheRootTokenOrASessionRevokesTheRootToken(t *testing.T) {
	e := newSignInEnv(t, nil)
	e.addAdministrator("erin")
	alice, _ := e.signIn("alice")
	erin, _ := e.signIn("erin")
	rootTok := e.createToken(alice, `{"name":"terraform","root":true}`)
	writer := e.createToken(alice, `{"name":"tw","grants":["tokens:read","tokens:write"]}`)
	erinRoot := e.createToken(erin, `{"name":"other-root","root":true}`)
	del := func(id string, secret string) int {
		return e.req("DELETE", "/api/v1/tokens/"+id, "", nil, bearer(secret)).Code
	}
	status := func(id string) string {
		w := e.req("GET", "/api/v1/tokens/"+id, "", alice, nil)
		if w.Code != 200 {
			t.Fatalf("get token: %d %s", w.Code, w.Body.String())
		}
		s := w.Body.String()
		for _, st := range []string{"revoked", "active"} {
			if strings.Contains(s, `"status":"`+st+`"`) {
				return st
			}
		}
		return s
	}
	for who, secret := range map[string]string{"a tokens:write token": writer.Secret, "another administrator's root token": erinRoot.Secret} {
		w := e.req("DELETE", "/api/v1/tokens/"+rootTok.Token.ID, "", nil, bearer(secret))
		if w.Code != 403 || !strings.Contains(w.Body.String(), "root_token_required") {
			t.Errorf("%s revokes the root token: %d %s", who, w.Code, w.Body.String())
		}
	}
	if s := status(rootTok.Token.ID); s != "active" {
		t.Fatalf("root token after refused revokes: %s", s)
	}
	// Other tokens, including another administrator's root token, stay revocable by a tokens:write token.
	if c := del(erinRoot.Token.ID, writer.Secret); c != 204 {
		t.Errorf("tokens:write revokes another root token: %d", c)
	}
	// A session may revoke it; so may the root token itself.
	if w := e.req("DELETE", "/api/v1/tokens/"+rootTok.Token.ID, "", alice, nil); w.Code != 204 {
		t.Fatalf("session revokes the root token: %d %s", w.Code, w.Body.String())
	}
	if s := status(rootTok.Token.ID); s != "revoked" {
		t.Errorf("root token after session revoke: %s", s)
	}
	again := e.createToken(alice, `{"name":"terraform2","root":true}`)
	if c := del(again.Token.ID, again.Secret); c != 204 {
		t.Errorf("root token revokes itself: %d", c)
	}

	// "The root token" means a root token of the root account (D25, D32): a
	// second root token B revokes the first, A.
	a := e.createToken(alice, `{"name":"root-a","root":true}`)
	b := e.createToken(alice, `{"name":"root-b","root":true}`)
	if c := del(a.Token.ID, b.Secret); c != 204 {
		t.Errorf("root token B revokes root token A: %d", c)
	}
	if s := status(a.Token.ID); s != "revoked" {
		t.Errorf("root token A after B revoked it: %s", s)
	}
	if s := status(b.Token.ID); s != "active" {
		t.Errorf("root token B after revoking A: %s", s)
	}

	// Only root tokens are protected: a tokens:write token still revokes a
	// non-root token owned by the root account.
	plain := e.createToken(alice, `{"name":"plain","grants":["users:read"]}`)
	if c := del(plain.Token.ID, writer.Secret); c != 204 {
		t.Errorf("tokens:write revokes a non-root token of the root account: %d", c)
	}
}

// TestRootAccountNoOpsStayOpenOnceDemotedOrDisabled pins the guard's two
// preconditions: a patch only counts as demoting when the root account is
// still an administrator, and as disabling when it is still enabled. Repeating
// either change on an account a session already demoted or disabled is a no-op
// and stays open to a users:write token.
func TestRootAccountNoOpsStayOpenOnceDemotedOrDisabled(t *testing.T) {
	e := newSignInEnv(t, nil)
	e.addAdministrator("erin")
	ctx := context.Background()
	e.signIn("alice")
	erin, _ := e.signIn("erin")
	narrow := e.createToken(erin, `{"name":"um","grants":["users:read","users:write"]}`)
	version := func() int64 {
		u, err := e.parts.users.Get(ctx, e.ids["alice"])
		if err != nil {
			t.Fatal(err)
		}
		return u.Version
	}
	session := func(body string) {
		t.Helper()
		if w := e.req("PATCH", "/api/v1/users/"+e.ids["alice"], fmt.Sprintf(body, version()), erin, nil); w.Code != 200 {
			t.Fatalf("session patch %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	token := func(body string) {
		t.Helper()
		if w := e.req("PATCH", "/api/v1/users/"+e.ids["alice"], fmt.Sprintf(body, version()), nil, bearer(narrow.Secret)); w.Code != 200 {
			t.Errorf("no-op %s on the root account: %d %s", body, w.Code, w.Body.String())
		}
	}

	// M1: role read-only on an already-demoted root account.
	session(`{"role":"read-only","version":%d}`)
	token(`{"role":"read-only","version":%d}`)

	// M2: disabled:true on an already-disabled root account.
	session(`{"disabled":true,"version":%d}`)
	token(`{"disabled":true,"version":%d}`)
}

// TestOnlyTheRootTokenOrASessionRenamesTheRootAccount is decision D35: changing
// the root (first) account's username is protected like D34. Other tokens get
// 403 root_token_required (pinned to the version); a same-name patch, other
// users and a root token or session stay open.
func TestOnlyTheRootTokenOrASessionRenamesTheRootAccount(t *testing.T) {
	e := newSignInEnv(t, nil)
	e.addAdministrator("erin")
	ctx := context.Background()
	alice, _ := e.signIn("alice")
	rootTok := e.createToken(alice, `{"name":"terraform","root":true}`)
	narrow := e.createToken(alice, `{"name":"um","grants":["users:read","users:write"]}`)
	erin, _ := e.signIn("erin")
	erinRoot := e.createToken(erin, `{"name":"other-root","root":true}`)
	get := func(id string) (string, int64) {
		u, err := e.parts.users.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return u.Username, u.Version
	}
	patch := func(id, secret, body string, session *browser) (int, string) {
		_, v := get(id)
		var hdr map[string]string
		if secret != "" {
			hdr = bearer(secret)
		}
		w := e.req("PATCH", "/api/v1/users/"+id, fmt.Sprintf(body, v), session, hdr)
		return w.Code, w.Body.String()
	}
	for who, secret := range map[string]string{"a users:write token": narrow.Secret, "another administrator's root token": erinRoot.Secret} {
		if code, out := patch(e.ids["alice"], secret, `{"username":"mallory","version":%d}`, nil); code != 403 || !strings.Contains(out, "root_token_required") {
			t.Errorf("%s renames the root account: %d %s", who, code, out)
		}
		_, v := get(e.ids["alice"])
		if w := e.req("PATCH", "/api/v1/users/"+e.ids["alice"], fmt.Sprintf(`{"username":"mallory","version":%d}`, v+5), nil, bearer(secret)); w.Code != 409 {
			t.Errorf("%s, stale version: %d", who, w.Code)
		}
	}
	if name, _ := get(e.ids["alice"]); name != "alice" {
		t.Fatalf("a refused request renamed the root account: %s", name)
	}
	// The same name is a no-op and stays open.
	if code, out := patch(e.ids["alice"], narrow.Secret, `{"username":"alice","version":%d}`, nil); code != 200 {
		t.Errorf("same username on the root account: %d %s", code, out)
	}
	// Other users can be renamed by a users:write token.
	if code, out := patch(e.ids["erin"], narrow.Secret, `{"username":"erin2","version":%d}`, nil); code != 200 {
		t.Errorf("users:write renames another user: %d %s", code, out)
	}
	// A root token of the root account renames it, then a session renames it back.
	if code, out := patch(e.ids["alice"], rootTok.Secret, `{"username":"alice2","version":%d}`, nil); code != 200 {
		t.Fatalf("root token renames the root account: %d %s", code, out)
	}
	if name, _ := get(e.ids["alice"]); name != "alice2" {
		t.Fatalf("root token's rename did not apply: %s", name)
	}
	if code, out := patch(e.ids["alice"], "", `{"username":"alice","version":%d}`, alice); code != 200 {
		t.Errorf("session renames the root account: %d %s", code, out)
	}
}
