package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/users"
)

// racingUsers returns the user as it was when the guard read it. The
// operator's change (which bumps the version) lands after that read, so the
// version the token predicted no longer equals the version read.
type racingUsers struct {
	users.Store
	read users.User
}

func (s racingUsers) Get(context.Context, string) (users.User, error) { return s.read, nil }

// TestAdministratorPatchGuardIsPinnedToTheVersion covers the review race: a
// non-root token predicts the version after a concurrent change and sends it,
// so the guard (which judged the older state) must answer version_conflict
// instead of passing the write through.
func TestAdministratorPatchGuardIsPinnedToTheVersion(t *testing.T) {
	admin, readOnly := authz.RoleAdministrator, authz.RoleReadOnly
	no := false
	tok := authz.TokenPrincipal("T", "O", authz.RoleAdministrator, authz.RoleAdministrator.Grants())
	cases := []struct {
		name  string
		read  users.User // state the guard read, at version 5
		patch users.Patch
		// refusedAtCurrent: at the version read, the guard refuses (an escalation).
		refusedAtCurrent bool
	}{
		{"operator demotes an administrator, token re-promotes", users.User{ID: "bob", Role: readOnly, Version: 5}, users.Patch{Role: &admin}, true},
		{"operator disables an administrator, token re-enables", users.User{ID: "bob", Role: admin, Disabled: true, Version: 5}, users.Patch{Disabled: &no}, true},
		{"operator promotes a disabled user, token re-enables", users.User{ID: "bob", Role: readOnly, Disabled: true, Version: 5}, users.Patch{Disabled: &no}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPatch, "/users/bob", nil)
			req = req.WithContext(authz.WithPrincipal(req.Context(), tok))
			st := racingUsers{read: c.read}
			// The token sends the predicted version 6: version_conflict.
			err := guardAdministratorPatch(req, st, "bob", 6, c.patch)
			var apiErr *Error
			if !errors.As(err, &apiErr) || apiErr.Code != CodeVersionConflict {
				t.Fatalf("stale read: %v, want version_conflict", err)
			}
			// At the version the guard read, it still judges and refuses
			// escalations (re-enabling a non-administrator stays open).
			err = guardAdministratorPatch(req, st, "bob", 5, c.patch)
			if c.refusedAtCurrent != (err != nil) || (err != nil && (!errors.As(err, &apiErr) || apiErr.Code != CodeRootTokenRequired)) {
				t.Fatalf("current version: %v, refused want %v", err, c.refusedAtCurrent)
			}
		})
	}
}
