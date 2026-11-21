package ledgerclient

// Enumerations of the API (checked against the OpenAPI document by tests).
const (
	KindService       = "service"
	KindServer        = "server"
	KindDomain        = "domain"
	KindSubscription  = "subscription"
	KindDebtRepayment = "debt_repayment"

	PriorityEssential = "essential"
	PriorityImportant = "important"
	PriorityFlexible  = "flexible"

	RoleAdministrator = "administrator"
	RoleReadOnly      = "read-only"

	FrequencyMonthly = "monthly"
	FrequencyAnnual  = "annual"

	ActorUser      = "user"
	ActorToken     = "token"
	ActorSystem    = "system"
	ActorAnonymous = "anonymous"
)

// Money is an exact amount with its currency. Amount is a decimal string that
// keeps its scale ("12.50"); never convert it to a float.
type Money struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// Schedule is a commitment's versioned schedule spec. A monthly schedule uses
// Interval and Day; an annual one Month and Day.
type Schedule struct {
	Version   int    `json:"version"`
	Frequency string `json:"frequency"`
	Interval  *int   `json:"interval,omitempty"`
	Month     *int   `json:"month,omitempty"`
	Day       int    `json:"day"`
}

// Principal is who a request acts as, with its effective grants (GET /me).
type Principal struct {
	Kind    string   `json:"kind"`
	UserID  string   `json:"user_id"`
	TokenID *string  `json:"token_id"`
	Role    string   `json:"role"`
	Grants  []string `json:"grants"`
}

// GrantResource is one resource of the grant vocabulary.
type GrantResource struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Actions     []string `json:"actions"`
	AdminOnly   bool     `json:"admin_only"`
}

// GrantRole is one role with the grants it holds.
type GrantRole struct {
	Name   string   `json:"name"`
	Grants []string `json:"grants"`
}

// GrantVocabulary is the answer of GET /grants.
type GrantVocabulary struct {
	Resources []GrantResource `json:"resources"`
	Roles     []GrantRole     `json:"roles"`
}

// User is a user account.
type User struct {
	ID          string  `json:"id"`
	Username    string  `json:"username"`
	DisplayName *string `json:"display_name"`
	Role        string  `json:"role"`
	Disabled    bool    `json:"disabled"`
	DisabledAt  *string `json:"disabled_at"`
	Version     int64   `json:"version"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// NewUser is the body of CreateUser.
type NewUser struct {
	Username    string  `json:"username"`
	DisplayName *string `json:"display_name,omitempty"`
	Role        string  `json:"role"`
}

// Commitment is a recurring commitment definition.
type Commitment struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Amount    Money    `json:"amount"`
	Schedule  Schedule `json:"schedule"`
	StartDate string   `json:"start_date"`
	EndDate   *string  `json:"end_date"`
	Priority  *string  `json:"priority"`
	Notes     *string  `json:"notes"`
	Version   int64    `json:"version"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
}

// NewCommitment is the body of CreateCommitment.
type NewCommitment struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Amount    Money    `json:"amount"`
	Schedule  Schedule `json:"schedule"`
	StartDate string   `json:"start_date"`
	EndDate   *string  `json:"end_date,omitempty"`
	Priority  *string  `json:"priority,omitempty"`
	Notes     *string  `json:"notes,omitempty"`
}

// Occurrence is one due date of a commitment.
type Occurrence struct {
	ID           string `json:"id"`
	CommitmentID string `json:"commitment_id"`
	Month        string `json:"month"`
	DueDate      string `json:"due_date"`
	Amount       Money  `json:"amount"`
	Version      int64  `json:"version"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

// AuditEntry is one entry of the append-only audit log.
type AuditEntry struct {
	ID          string            `json:"id"`
	Action      string            `json:"action"`
	ActorKind   string            `json:"actor_kind"`
	ActorUserID *string           `json:"actor_user_id"`
	ActorToken  *string           `json:"actor_token_id"`
	TargetType  *string           `json:"target_type"`
	TargetID    *string           `json:"target_id"`
	At          string            `json:"at"`
	Details     map[string]string `json:"details"`
}

// MergePatch is a JSON Merge Patch (RFC 7396) body for an update: an absent
// key is unchanged, a nil value (sent as null) clears an optional member, and
// nested maps merge member by member. The "version" member, if present, is the
// precondition; the Update methods send the version as If-Match instead.
type MergePatch map[string]any
