package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/users"
)

// TestOnlyTheRootTokenCreatesAdministratorsOrSetsPasswords is decision D32: a
// token principal may create or promote an administrator, re-enable a disabled
// administrator or set any password only when it is the root token of the root
// account. Every other token (even one holding users:write, or an
// administrator's own root token) gets 403 root_token_required, and nothing
// changes. Sessions keep their recent-sign-in rule.
func TestOnlyTheRootTokenCreatesAdministratorsOrSetsPasswords(t *testing.T) {
	e := newSignInEnv(t, nil)
	e.addAdministrator("erin")
	e.addAdministrator("dina")
	ctx := context.Background()
	alice, _ := e.signIn("alice")
	rootTok := e.createToken(alice, `{"name":"terraform","root":true}`)
	narrow := e.createToken(alice, `{"name":"um","grants":["users:read","users:write"]}`)
	erin, _ := e.signIn("erin")
	erinRoot := e.createToken(erin, `{"name":"other-root","root":true}`)
	dina, _ := e.parts.users.Get(ctx, e.ids["dina"])
	if _, err := e.parts.users.Update(ctx, audit.System, dina.ID, dina.Version, users.Patch{Disabled: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	version := func(name string) int64 {
		u, err := e.parts.users.Get(ctx, e.ids[name])
		if err != nil {
			t.Fatal(err)
		}
		return u.Version
	}
	role := func(name string) authz.Role {
		u, _ := e.parts.users.Get(ctx, e.ids[name])
		return u.Role
	}
	disabled := func(name string) bool {
		u, _ := e.parts.users.Get(ctx, e.ids[name])
		return u.Disabled
	}
	type attempt struct {
		name, method, path, body string
	}
	attempts := func() []attempt {
		return []attempt{
			{"create an administrator", "POST", "/api/v1/users", `{"username":"newadmin","role":"administrator"}`},
			{"promote to administrator", "PATCH", "/api/v1/users/" + e.ids["bob"], fmt.Sprintf(`{"role":"administrator","version":%d}`, version("bob"))},
			{"re-enable a disabled administrator", "PATCH", "/api/v1/users/" + e.ids["dina"], fmt.Sprintf(`{"disabled":false,"version":%d}`, version("dina"))},
			{"set another user's password", "PUT", "/api/v1/users/" + e.ids["bob"] + "/password", `{"password":"a-new-long-password"}`},
			{"set the owner's own password", "PUT", "/api/v1/users/" + e.ids["alice"] + "/password", `{"password":"a-new-long-password"}`},
		}
	}

	basePW := len(e.auditActions(audit.PasswordChanged))

	// Non-root tokens are refused on every path with the structured code.
	for who, secret := range map[string]string{
		"a users:write token of the root account": narrow.Secret,
		"another administrator's root token":      erinRoot.Secret,
	} {
		for _, a := range attempts() {
			w := e.req(a.method, a.path, a.body, nil, bearer(secret))
			if w.Code != 403 || !strings.Contains(w.Body.String(), "root_token_required") {
				t.Errorf("%s, %s: %d %s", who, a.name, w.Code, w.Body.String())
			}
		}
	}
	if ok, err := e.hasUser("newadmin"); err != nil || ok {
		t.Fatalf("a refused request created the administrator: %v %v", ok, err)
	}
	if role("bob") != authz.RoleReadOnly || !disabled("dina") {
		t.Fatalf("a refused request changed a user: bob=%s dina disabled=%v", role("bob"), disabled("dina"))
	}
	if n := len(e.auditActions(audit.PasswordChanged)); n != basePW {
		t.Fatalf("refused password requests audited %d password changes", n-basePW)
	}

	// Sessions are unchanged: an administrator needs recent sign-in, not a
	// root token.
	e.clock.advance(11 * time.Minute)
	w := e.req("PUT", "/api/v1/users/"+e.ids["bob"]+"/password", `{"password":"a-new-long-password"}`, alice, nil)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "reauthentication_required") {
		t.Fatalf("stale session: %d %s", w.Code, w.Body.String())
	}

	// Safe uses stay open to a users:write token: non-administrator users,
	// profile edits, demoting and disabling.
	if w := e.req("POST", "/api/v1/users", `{"username":"viewer","role":"read-only"}`, nil, bearer(narrow.Secret)); w.Code != 201 {
		t.Errorf("create a read-only user: %d %s", w.Code, w.Body.String())
	}
	if w := e.req("PATCH", "/api/v1/users/"+e.ids["bob"], fmt.Sprintf(`{"display_name":"Bob","version":%d}`, version("bob")), nil, bearer(narrow.Secret)); w.Code != 200 {
		t.Errorf("edit a profile: %d %s", w.Code, w.Body.String())
	}
	if w := e.req("PATCH", "/api/v1/users/"+e.ids["erin"], fmt.Sprintf(`{"disabled":true,"version":%d}`, version("erin")), nil, bearer(narrow.Secret)); w.Code != 200 {
		t.Errorf("disable an administrator: %d %s", w.Code, w.Body.String())
	}
	if w := e.req("PATCH", "/api/v1/users/"+e.ids["erin"], fmt.Sprintf(`{"disabled":false,"version":%d}`, version("erin")), nil, bearer(narrow.Secret)); w.Code != 403 || !strings.Contains(w.Body.String(), "root_token_required") {
		t.Errorf("re-enable the administrator it disabled: %d %s", w.Code, w.Body.String())
	}
	// Idempotent and non-escalating changes stay open: re-enabling a disabled
	// read-only user, and a no-op role on an administrator.
	for _, step := range []string{`{"disabled":true,"version":%d}`, `{"disabled":false,"version":%d}`} {
		if w := e.req("PATCH", "/api/v1/users/"+e.ids["bob"], fmt.Sprintf(step, version("bob")), nil, bearer(narrow.Secret)); w.Code != 200 {
			t.Errorf("bob %s: %d %s", step, w.Code, w.Body.String())
		}
	}
	if disabled("bob") || role("bob") != authz.RoleReadOnly {
		t.Errorf("bob after re-enable: role=%s disabled=%v", role("bob"), disabled("bob"))
	}
	if w := e.req("PATCH", "/api/v1/users/"+e.ids["alice"], fmt.Sprintf(`{"role":"administrator","version":%d}`, version("alice")), nil, bearer(narrow.Secret)); w.Code != 200 {
		t.Errorf("no-op role administrator on an administrator: %d %s", w.Code, w.Body.String())
	}

	// The root token of the root account succeeds on each path.
	for _, a := range attempts() {
		w := e.req(a.method, a.path, a.body, nil, bearer(rootTok.Secret))
		if w.Code != 200 && w.Code != 201 {
			t.Errorf("root token, %s: %d %s", a.name, w.Code, w.Body.String())
		}
	}
	if role("bob") != authz.RoleAdministrator || disabled("dina") {
		t.Fatalf("root token changes did not apply: bob=%s dina disabled=%v", role("bob"), disabled("dina"))
	}
	if ok, err := e.hasUser("newadmin"); err != nil || !ok {
		t.Fatalf("the root token's administrator was not created: %v %v", ok, err)
	}
	if got := len(e.auditActions(audit.PasswordChanged)); got != basePW+2 {
		t.Fatalf("password changes audited = %d, want %d", got-basePW, 2)
	}
}

func (e *signInEnv) hasUser(name string) (bool, error) {
	list, err := e.parts.users.List(context.Background(), users.ListFilter{UsernamePrefix: name, Limit: 10})
	if err != nil {
		return false, err
	}
	for _, u := range list {
		if u.Username == name {
			return true, nil
		}
	}
	return false, nil
}
