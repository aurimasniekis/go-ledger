// Package users holds the rules for user accounts (task A1, ADR-0021):
// usernames, roles, disabling, and the invariant that an instance never loses
// its last active administrator. Credentials (passwords, TOTP, OIDC links,
// sessions) belong to later tasks and live in their own tables.
//
// Users are disabled, never deleted, so audit entries and token ownership keep
// pointing at a real row.
package users

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
)

// User is a user account.
type User struct {
	ID          string
	Username    string
	DisplayName *string
	Role        authz.Role
	Disabled    bool
	DisabledAt  *time.Time
	Version     int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ActiveAdministrator reports whether u counts towards the last-administrator
// invariant.
func (u User) ActiveAdministrator() bool { return u.Role == authz.RoleAdministrator && !u.Disabled }

// New is a user to create.
type New struct {
	Username    string
	DisplayName *string
	Role        authz.Role
}

// Patch is a validated partial update (JSON Merge Patch semantics): a nil
// pointer leaves the field unchanged; SetDisplayName with a nil DisplayName
// clears it.
type Patch struct {
	Username       *string
	SetDisplayName bool
	DisplayName    *string
	Role           *authz.Role
	Disabled       *bool
}

// Apply returns u with p applied (no validation, no timestamps).
func (p Patch) Apply(u User) User {
	if p.Username != nil {
		u.Username = *p.Username
	}
	if p.SetDisplayName {
		u.DisplayName = p.DisplayName
	}
	if p.Role != nil {
		u.Role = *p.Role
	}
	if p.Disabled != nil {
		u.Disabled = *p.Disabled
	}
	return u
}

// Errors.
var (
	// ErrLastAdministrator: the change would leave no active administrator.
	ErrLastAdministrator = errors.New("the last active administrator cannot be disabled or given another role")
	// ErrUsernameTaken: another user has the username.
	ErrUsernameTaken = errors.New("the username is already taken")
	// ErrDisabled: the user exists but is disabled and cannot act.
	ErrDisabled = errors.New("the user is disabled")
)

// FieldError is a validation error about one field. Message is safe to show.
type FieldError struct {
	Field   string
	Message string
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Message }

// Username rules: 1 to 64 characters from a-z, 0-9, ".", "_" and "-",
// starting with a letter or digit. Lower case only, so uniqueness needs no
// case folding. The users table checks the same rule.
const MaxUsername = 64

// ValidateUsername checks a username.
func ValidateUsername(s string) error {
	if len(s) < 1 || len(s) > MaxUsername {
		return &FieldError{"username", fmt.Sprintf("username must be 1 to %d characters", MaxUsername)}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || i > 0 && (c == '.' || c == '_' || c == '-')
		if !ok {
			return &FieldError{"username", "username must use lower-case letters, digits, '.', '_' or '-', and start with a letter or digit"}
		}
	}
	return nil
}

// MaxDisplayName is the longest display name, in characters.
const MaxDisplayName = 100

// ValidateDisplayName checks an optional display name.
func ValidateDisplayName(s *string) error {
	if s == nil {
		return nil
	}
	if !utf8.ValidString(*s) || strings.TrimSpace(*s) == "" || utf8.RuneCountInString(*s) > MaxDisplayName {
		return &FieldError{"display_name", fmt.Sprintf("display_name must be 1 to %d characters and not blank", MaxDisplayName)}
	}
	return nil
}

// ValidateRole checks a role.
func ValidateRole(r authz.Role) error {
	if !r.Valid() {
		names := make([]string, 0, len(authz.Roles()))
		for _, x := range authz.Roles() {
			names = append(names, string(x))
		}
		return &FieldError{"role", "role must be one of " + strings.Join(names, ", ")}
	}
	return nil
}

// Validate checks a new user.
func (n New) Validate() error {
	if err := ValidateUsername(n.Username); err != nil {
		return err
	}
	if err := ValidateDisplayName(n.DisplayName); err != nil {
		return err
	}
	return ValidateRole(n.Role)
}

// Validate checks the fields a patch sets.
func (p Patch) Validate() error {
	if p.Username != nil {
		if err := ValidateUsername(*p.Username); err != nil {
			return err
		}
	}
	if p.SetDisplayName {
		if err := ValidateDisplayName(p.DisplayName); err != nil {
			return err
		}
	}
	if p.Role != nil {
		return ValidateRole(*p.Role)
	}
	return nil
}

// CheckLastAdministrator returns ErrLastAdministrator when changing before
// into after removes an active administrator and otherAdmins (active
// administrators other than this user) is zero.
func CheckLastAdministrator(before, after User, otherAdmins int) error {
	if before.ActiveAdministrator() && !after.ActiveAdministrator() && otherAdmins == 0 {
		return ErrLastAdministrator
	}
	return nil
}

// CreatedEvent is the audit event for a new user.
func CreatedEvent(actor audit.Actor, u User, at time.Time) audit.Event {
	return audit.Event{Action: audit.UserCreated, Actor: actor, TargetType: "user", TargetID: u.ID, At: at,
		Details: map[string]string{"username": u.Username, "role": string(u.Role)}}
}

// ChangeEvents returns the audit events for changing before into after: one
// per role change, disable or enable, and one for profile fields.
func ChangeEvents(actor audit.Actor, before, after User, at time.Time) []audit.Event {
	ev := func(a audit.Action, d map[string]string) audit.Event {
		return audit.Event{Action: a, Actor: actor, TargetType: "user", TargetID: after.ID, At: at, Details: d}
	}
	var out []audit.Event
	if before.Role != after.Role {
		out = append(out, ev(audit.UserRoleChanged, map[string]string{"from": string(before.Role), "to": string(after.Role)}))
	}
	if !before.Disabled && after.Disabled {
		out = append(out, ev(audit.UserDisabled, nil))
	}
	if before.Disabled && !after.Disabled {
		out = append(out, ev(audit.UserEnabled, nil))
	}
	var fields []string
	if before.Username != after.Username {
		fields = append(fields, "username")
	}
	if !equalPtr(before.DisplayName, after.DisplayName) {
		fields = append(fields, "display_name")
	}
	if len(fields) > 0 {
		slices.Sort(fields)
		out = append(out, ev(audit.UserUpdated, map[string]string{"fields": strings.Join(fields, ",")}))
	}
	return out
}

func equalPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// ListFilter selects users. Limit is the page size; implementations return
// up to Limit+1 users after After in ID order.
type ListFilter struct {
	Roles          []authz.Role
	Disabled       *bool
	UsernamePrefix string
	After          string
	Limit          int
}

// Store is the users port; internal/store/users implements it. Writes record
// their audit events in the same transaction.
type Store interface {
	Create(ctx context.Context, actor audit.Actor, n New) (User, error)
	Get(ctx context.Context, id string) (User, error)
	List(ctx context.Context, f ListFilter) ([]User, error)
	// Update applies p when the user still has version expected. It returns
	// ErrLastAdministrator, ErrUsernameTaken, resource.ErrNotFound or a
	// *resource.VersionConflictError.
	Update(ctx context.Context, actor audit.Actor, id string, expected int64, p Patch) (User, error)
	// CheckVersion returns the user when it still has version expected and
	// changes nothing (an empty merge patch).
	CheckVersion(ctx context.Context, id string, expected int64) (User, error)
	// Principal returns the principal of an enabled user with its current
	// role, for authenticators (sessions, tokens): ErrDisabled for a disabled
	// user, resource.ErrNotFound for an unknown one.
	Principal(ctx context.Context, id string) (authz.Principal, error)
}
