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

	PaymentPaid          = "paid"
	PaymentPartiallyPaid = "partially_paid"
	PaymentUpcoming      = "upcoming"
	PaymentDue           = "due"
	PaymentOverdue       = "overdue"

	OverridePaid   = "paid"
	OverrideUnpaid = "unpaid"

	MatchSuggested = "suggested"
	MatchConfirmed = "confirmed"
	MatchRejected  = "rejected"
	MatchReversed  = "reversed"

	OriginAutoRule = "auto_rule"
	OriginModel    = "model_suggestion"
	OriginUser     = "user"

	TokenActive  = "active"
	TokenExpired = "expired"
	TokenRevoked = "revoked"
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
	// Payment is derived when read from payment evidence, dates and the
	// manual override. Only evidence in the occurrence's currency is counted.
	Payment PaymentState `json:"payment"`
}

// PaymentState is an occurrence's derived payment state (ADR-0031). Amounts
// are in the occurrence's currency; other currencies are only listed in
// UnconvertedCurrencies and are never converted or summed.
type PaymentState struct {
	Status                string           `json:"status"`
	DerivedStatus         string           `json:"derived_status"`
	Timing                *string          `json:"timing"`
	PaidAmount            Money            `json:"paid_amount"`
	OutstandingAmount     Money            `json:"outstanding_amount"`
	OverpaidAmount        Money            `json:"overpaid_amount"`
	UnconvertedCurrencies []string         `json:"unconverted_currencies"`
	EvidenceCount         int              `json:"evidence_count"`
	GraceUntil            *string          `json:"grace_until"`
	WithinGrace           bool             `json:"within_grace"`
	Override              *PaymentOverride `json:"override"`
}

// PaymentOverride is the active manual status override of an occurrence.
// Applied is false for a "paid" override whose evidence has been removed.
type PaymentOverride struct {
	Status         string  `json:"status"`
	Reason         string  `json:"reason"`
	SetAt          string  `json:"set_at"`
	Applied        bool    `json:"applied"`
	PreviousStatus *string `json:"previous_status"`
}

// ManualPayment is a payment recorded by hand for one occurrence: user data,
// never a bank fact.
type ManualPayment struct {
	ID           string  `json:"id"`
	OccurrenceID string  `json:"occurrence_id"`
	Amount       Money   `json:"amount"`
	PaidOn       string  `json:"paid_on"`
	Note         *string `json:"note"`
	Version      int64   `json:"version"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
}

// NewManualPayment is the body of RecordManualPayment. PaidOn is YYYY-MM-DD
// and not after today in the reporting zone; the currency must be the
// occurrence's.
type NewManualPayment struct {
	Amount Money   `json:"amount"`
	PaidOn string  `json:"paid_on"`
	Note   *string `json:"note,omitempty"`
}

// StatusOverride is the body of SetStatusOverride (the version travels as
// If-Match instead).
type StatusOverride struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// MatchActor is who created or decided a payment match. UserID is nil for
// the system.
type MatchActor struct {
	Kind   string  `json:"kind"`
	UserID *string `json:"user_id"`
}

// MatchedBankTransaction is the bank fact a payment match refers to, as the
// provider reported it. Source is always "bank"; Amount is signed (a payment
// is negative).
type MatchedBankTransaction struct {
	Source        string  `json:"source"`
	ID            string  `json:"id"`
	AccountID     string  `json:"account_id"`
	Provider      string  `json:"provider"`
	BookingStatus string  `json:"booking_status"`
	Amount        Money   `json:"amount"`
	Date          string  `json:"date"`
	BookingDate   *string `json:"booking_date"`
	ValueDate     *string `json:"value_date"`
	// Description and Counterparty are the bank's text: nil (redacted)
	// unless the caller holds banking:read; see PaymentMatch.BankTextRedacted.
	Description        *string `json:"description"`
	Counterparty       *string `json:"counterparty"`
	NoLongerReportedAt *string `json:"no_longer_reported_at"`
	SupersededBy       *string `json:"superseded_by"`
	Countable          bool    `json:"countable"`
}

// MatchEvent is one entry of a payment match's history.
type MatchEvent struct {
	Action string     `json:"action"`
	State  string     `json:"state"`
	Amount Money      `json:"amount"`
	By     MatchActor `json:"by"`
	Reason *string    `json:"reason"`
	At     string     `json:"at"`
}

// PaymentMatch links a bank transaction to an occurrence with the amount
// allocated to it. Origin says who proposed it, State the decision, and
// CountsAsEvidence whether it pays the occurrence now.
type PaymentMatch struct {
	ID               string                 `json:"id"`
	OccurrenceID     string                 `json:"occurrence_id"`
	TransactionID    string                 `json:"transaction_id"`
	Amount           Money                  `json:"amount"`
	Origin           string                 `json:"origin"`
	State            string                 `json:"state"`
	CountsAsEvidence bool                   `json:"counts_as_evidence"`
	NeedsReview      bool                   `json:"needs_review"`
	BankTextRedacted bool                   `json:"bank_text_redacted"`
	CreatedBy        MatchActor             `json:"created_by"`
	DecidedBy        *MatchActor            `json:"decided_by"`
	DecidedAt        *string                `json:"decided_at"`
	Reason           *string                `json:"reason"`
	Transaction      MatchedBankTransaction `json:"transaction"`
	History          []MatchEvent           `json:"history"`
	Version          int64                  `json:"version"`
	// OccurrenceVersion is the occurrence's new version on create and action
	// responses; nil on reads.
	OccurrenceVersion *int64 `json:"occurrence_version,omitempty"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

// NewPaymentMatch is the body of CreatePaymentMatch: the user's confirmed
// match of Amount of the transaction to the occurrence.
type NewPaymentMatch struct {
	TransactionID    string  `json:"transaction_id"`
	OccurrenceID     string  `json:"occurrence_id"`
	Amount           Money   `json:"amount"`
	AllowOverpayment bool    `json:"allow_overpayment,omitempty"`
	Reason           *string `json:"reason,omitempty"`
}

// MatchDecision is the body of ConfirmPaymentMatch, RejectPaymentMatch,
// UnmatchPaymentMatch and ReversePaymentMatch (AllowOverpayment applies to
// a confirmation only). The version travels as If-Match.
type MatchDecision struct {
	AllowOverpayment bool    `json:"allow_overpayment,omitempty"`
	Reason           *string `json:"reason,omitempty"`
}

// MatchCorrection is the body of CorrectPaymentMatch.
type MatchCorrection struct {
	Amount           Money   `json:"amount"`
	AllowOverpayment bool    `json:"allow_overpayment,omitempty"`
	Reason           *string `json:"reason,omitempty"`
}

// APIToken describes an API token. It never includes the token itself.
type APIToken struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	OwnerID    string   `json:"owner_id"`
	Root       bool     `json:"root"`
	Grants     []string `json:"grants"`
	Hint       string   `json:"hint"`
	Prefix     string   `json:"prefix"`
	LastChars  string   `json:"last_chars"`
	Status     string   `json:"status"`
	CreatedAt  string   `json:"created_at"`
	ExpiresAt  *string  `json:"expires_at"`
	LastUsedAt *string  `json:"last_used_at"`
	RevokedAt  *string  `json:"revoked_at"`
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
