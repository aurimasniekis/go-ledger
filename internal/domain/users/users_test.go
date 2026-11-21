package users

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
)

func ptr[T any](v T) *T { return &v }

func TestValidateUsername(t *testing.T) {
	for _, ok := range []string{"a", "alice", "a.b_c-d", "0day", strings.Repeat("x", 64)} {
		if err := ValidateUsername(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Alice", ".a", "-a", "_a", "a b", "ä", "a@b", strings.Repeat("x", 65)} {
		var fe *FieldError
		if err := ValidateUsername(bad); !errors.As(err, &fe) || fe.Field != "username" {
			t.Errorf("%q accepted: %v", bad, err)
		}
	}
}

func TestValidateDisplayNameAndRole(t *testing.T) {
	if ValidateDisplayName(nil) != nil || ValidateDisplayName(ptr("Alice Ö")) != nil || ValidateDisplayName(ptr(strings.Repeat("é", 100))) != nil {
		t.Fatal("valid display name refused")
	}
	for _, bad := range []string{"", "   ", strings.Repeat("é", 101), "\xff"} {
		if ValidateDisplayName(ptr(bad)) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if ValidateRole("owner") == nil || ValidateRole(authz.RoleReadOnly) != nil {
		t.Fatal("role validation")
	}
	if (New{Username: "a", Role: "x"}).Validate() == nil || (New{Username: "a", Role: authz.RoleAdministrator}).Validate() != nil {
		t.Fatal("New.Validate")
	}
	if (Patch{Role: ptr(authz.Role("x"))}).Validate() == nil || (Patch{Username: ptr("A")}).Validate() == nil ||
		(Patch{SetDisplayName: true, DisplayName: ptr("")}).Validate() == nil || (Patch{SetDisplayName: true}).Validate() != nil {
		t.Fatal("Patch.Validate")
	}
}

func TestLastAdministrator(t *testing.T) {
	admin := User{ID: "A", Role: authz.RoleAdministrator}
	cases := []struct {
		name   string
		after  User
		others int
		want   error
	}{
		{"demote last", User{ID: "A", Role: authz.RoleReadOnly}, 0, ErrLastAdministrator},
		{"disable last", User{ID: "A", Role: authz.RoleAdministrator, Disabled: true}, 0, ErrLastAdministrator},
		{"demote with another", User{ID: "A", Role: authz.RoleReadOnly}, 1, nil},
		{"rename last", User{ID: "A", Username: "x", Role: authz.RoleAdministrator}, 0, nil},
	}
	for _, c := range cases {
		if got := CheckLastAdministrator(admin, c.after, c.others); !errors.Is(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	// A disabled administrator was not counted, so changing it is fine.
	if err := CheckLastAdministrator(User{Role: authz.RoleAdministrator, Disabled: true}, User{Role: authz.RoleReadOnly, Disabled: true}, 0); err != nil {
		t.Fatal(err)
	}
}

func TestChangeEvents(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	actor := audit.Actor{Kind: audit.ActorUser, UserID: "ADMIN"}
	before := User{ID: "U", Username: "a", Role: authz.RoleReadOnly}
	after := Patch{Username: ptr("b"), SetDisplayName: true, DisplayName: ptr("B"), Role: ptr(authz.RoleAdministrator), Disabled: ptr(true)}.Apply(before)
	evs := ChangeEvents(actor, before, after, at)
	var got []string
	for _, e := range evs {
		if e.TargetID != "U" || e.TargetType != "user" || e.Actor != actor || !e.At.Equal(at) {
			t.Errorf("event %+v", e)
		}
		got = append(got, string(e.Action)+" "+e.Details["from"]+e.Details["to"]+e.Details["fields"])
	}
	want := "user.role_changed read-onlyadministrator|user.disabled |user.updated display_name,username"
	if strings.Join(got, "|") != want {
		t.Fatalf("events %q\nwant   %q", strings.Join(got, "|"), want)
	}
	if evs := ChangeEvents(actor, after, Patch{Disabled: ptr(false)}.Apply(after), at); len(evs) != 1 || evs[0].Action != audit.UserEnabled {
		t.Fatalf("enable: %+v", evs)
	}
	if evs := ChangeEvents(actor, before, before, at); len(evs) != 0 {
		t.Fatalf("no change made events: %+v", evs)
	}
	c := CreatedEvent(actor, User{ID: "U", Username: "a", Role: authz.RoleReadOnly}, at)
	if c.Action != audit.UserCreated || c.Details["role"] != "read-only" || c.Details["username"] != "a" {
		t.Fatalf("created: %+v", c)
	}
}

func TestActorOf(t *testing.T) {
	if a := audit.ActorOf(authz.UserPrincipal("U", authz.RoleReadOnly)); a != (audit.Actor{Kind: audit.ActorUser, UserID: "U"}) {
		t.Fatalf("%+v", a)
	}
	if a := audit.ActorOf(authz.TokenPrincipal("T", "U", authz.RoleAdministrator, nil)); a != (audit.Actor{Kind: audit.ActorToken, UserID: "U", TokenID: "T"}) {
		t.Fatalf("%+v", a)
	}
}
