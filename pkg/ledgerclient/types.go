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
	PaymentCancelled     = "cancelled"
	PaymentNotRecorded   = "not_recorded"

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
	// MatchTolerance is how far a booked payment may fall short of Amount and
	// still be confirmed automatically; nil when none is set (only the exact
	// amount confirms automatically).
	MatchTolerance *Money `json:"match_tolerance"`
	Version        int64  `json:"version"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
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
	// MatchTolerance is optional; see Commitment.MatchTolerance.
	MatchTolerance *Money `json:"match_tolerance,omitempty"`
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
	// CancelledAt and CancelledReason are set when the occurrence was cancelled
	// instead of removed because deleted payments still refer to it. A
	// cancelled occurrence is not part of totals, overdue counts or the
	// outlook.
	CancelledAt     *string `json:"cancelled_at"`
	CancelledReason *string `json:"cancelled_reason"`
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
	ShortfallAmount       Money            `json:"shortfall_amount"`
	UnconvertedCurrencies []string         `json:"unconverted_currencies"`
	EvidenceCount         int              `json:"evidence_count"`
	ManualEvidenceCount   int              `json:"manual_evidence_count"`
	BankEvidenceCount     int              `json:"bank_evidence_count"`
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
	// Via is how the change came: session, token, mcp or system; nil for
	// changes recorded before provenance was.
	Via *string `json:"via"`
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

// PaymentMatchingRun counts what one automatic payment-matching run did.
type PaymentMatchingRun struct {
	// Candidates counts the candidate pairs evaluated.
	Candidates int `json:"candidates"`
	// Confirmed counts matches confirmed automatically.
	Confirmed int `json:"confirmed"`
	// Suggested counts new suggestions left for review.
	Suggested int `json:"suggested"`
	// Dropped counts the engine's own suggestions dropped as no longer
	// candidates.
	Dropped int `json:"dropped"`
	// Skipped counts actions the server's guards refused.
	Skipped int `json:"skipped"`
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
	// ParentTokenID is the token that created this one through the API; nil when none.
	ParentTokenID *string `json:"parent_token_id"`
}

// NewAPIToken is the body of CreateMyToken: a name and either grants or Root.
type NewAPIToken struct {
	Name   string   `json:"name"`
	Grants []string `json:"grants,omitempty"`
	Root   bool     `json:"root,omitempty"`
	// ExpiresAt is an RFC 3339 time in the future; nil means never.
	ExpiresAt *string `json:"expires_at,omitempty"`
}

// CreatedAPIToken answers CreateMyToken. Secret is the token itself and is
// never shown again: store it at once.
type CreatedAPIToken struct {
	Secret string   `json:"secret"`
	Token  APIToken `json:"token"`
	// Warning explains a root token's power; nil for other tokens.
	Warning *string `json:"warning"`
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

// BankCapabilities is what a provider application can do. Zero means not
// known, never none.
type BankCapabilities struct {
	BalanceTypes            []string `json:"balance_types"`
	PendingTransactions     bool     `json:"pending_transactions"`
	MaxConsentValidityDays  int      `json:"max_consent_validity_days"`
	MaxHistoryDays          int      `json:"max_history_days"`
	BackgroundFetchesPerDay int      `json:"background_fetches_per_day"`
}

// BankApplication is a configured provider application. Mode and Environment
// use the provider's own terms: app and developer mode are not production and
// sandbox.
type BankApplication struct {
	Provider      string           `json:"provider"`
	Name          string           `json:"name"`
	Mode          string           `json:"mode"`
	Environment   string           `json:"environment"`
	AccountAccess string           `json:"account_access"`
	Capabilities  BankCapabilities `json:"capabilities"`
}

// BankInstitution is a bank an application offers in a country.
type BankInstitution struct {
	ID                     string `json:"id"`
	Name                   string `json:"name"`
	Country                string `json:"country"`
	MaxConsentValidityDays int    `json:"max_consent_validity_days"`
}

// BankError says why a refresh or an account's last attempt failed. Kind is
// reconnect_required (only a new consent helps), rate_limited or retryable
// (try later), permanent or canceled. It carries no bank payload.
type BankError struct {
	Kind string `json:"kind"`
	Code string `json:"code"`
}

// BankFreshness is how current one account's data is.
type BankFreshness struct {
	State         string     `json:"state"`
	Stale         bool       `json:"stale"`
	LastSuccessAt *string    `json:"last_success_at"`
	LastAttemptAt *string    `json:"last_attempt_at"`
	LastError     *BankError `json:"last_error"`
	NoticeCode    *string    `json:"notice_code"`
}

// BankBalance is the latest balance of one type, with when Ledger obtained it.
type BankBalance struct {
	Type          string  `json:"type"`
	Amount        Money   `json:"amount"`
	ReferenceDate *string `json:"reference_date"`
	ObtainedAt    string  `json:"obtained_at"`
}

// BankAccount is an account of a connection. IdentifierHint is only the last
// four characters of the account number.
type BankAccount struct {
	ID                  string        `json:"id"`
	Name                string        `json:"name"`
	Product             string        `json:"product"`
	Currency            string        `json:"currency"`
	IdentifierHint      *string       `json:"identifier_hint"`
	NoLongerReported    bool          `json:"no_longer_reported"`
	TransactionsThrough *string       `json:"transactions_through"`
	Freshness           BankFreshness `json:"freshness"`
	Balances            []BankBalance `json:"balances"`
}

// BankRefreshRun is one refresh of a connection and what it stored.
type BankRefreshRun struct {
	ID                   string     `json:"id"`
	StartedAt            string     `json:"started_at"`
	FinishedAt           *string    `json:"finished_at"`
	Outcome              string     `json:"outcome"`
	Error                *BankError `json:"error"`
	AccountsFailed       int        `json:"accounts_failed"`
	TransactionsInserted int        `json:"transactions_inserted"`
	TransactionsRejected int        `json:"transactions_rejected"`
}

// BankSchedule is the background scheduler's view of a connection.
type BankSchedule struct {
	State               string  `json:"state"`
	NextAttemptAt       *string `json:"next_attempt_at"`
	ConsecutiveFailures int     `json:"consecutive_failures"`
}

// BankConnectionInstitution is the bank a connection is with.
type BankConnectionInstitution struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Country string `json:"country"`
}

// BankConnection is a connection to a bank with its accounts. Status is what
// the connection is, Freshness how current its data is; a failed refresh never
// changes stored data. NeedsReconnect is true only when a new consent is the
// way forward. While RefreshRunning is true a refresh is still going on:
// read the connection again later.
type BankConnection struct {
	ID                 string                    `json:"id"`
	Provider           string                    `json:"provider"`
	Application        string                    `json:"application"`
	Mode               string                    `json:"mode"`
	Environment        string                    `json:"environment"`
	AccountAccess      string                    `json:"account_access"`
	Institution        BankConnectionInstitution `json:"institution"`
	Status             string                    `json:"status"`
	NeedsReconnect     bool                      `json:"needs_reconnect"`
	Freshness          string                    `json:"freshness"`
	ConsentGrantedAt   *string                   `json:"consent_granted_at"`
	ConsentValidUntil  *string                   `json:"consent_valid_until"`
	ConsentExpiresSoon bool                      `json:"consent_expires_soon"`
	LastError          *BankError                `json:"last_error"`
	Accounts           []BankAccount             `json:"accounts"`
	LastRefreshRun     *BankRefreshRun           `json:"last_refresh_run"`
	RefreshRunning     bool                      `json:"refresh_running"`
	Schedule           *BankSchedule             `json:"schedule"`
	AsOf               string                    `json:"as_of"`
	Version            int64                     `json:"version"`
	CreatedAt          string                    `json:"created_at"`
	UpdatedAt          string                    `json:"updated_at"`
	// PreviousSessionEndFailed is only set by the completion of a reconnect.
	PreviousSessionEndFailed bool `json:"previous_session_end_failed,omitempty"`
}

// BankConsentStart is the answer of starting or renewing a connection.
// AuthorizationURL is the bank's or provider's page: the person opens it in a
// browser and approves there. Ledger and this client never see bank
// credentials; the connection becomes active when the bank redirects to
// Ledger's callback page.
type BankConsentStart struct {
	Connection       BankConnection `json:"connection"`
	AuthorizationURL string         `json:"authorization_url"`
}

// NewBankConnection starts a connection (needs connections:write).
type NewBankConnection struct {
	Application   string `json:"application"`
	InstitutionID string `json:"institution_id"`
	// Country is an ISO 3166-1 alpha-2 code in capitals.
	Country string `json:"country"`
	// PSUType is "personal" or "business"; empty for the default.
	PSUType string `json:"psu_type,omitempty"`
	// ValidForDays is the requested consent validity; 0 for the bank's own.
	ValidForDays int `json:"valid_for_days,omitempty"`
}

// CachedRate is one cached exchange rate with its provenance and freshness
// (task FX2c). One unit of Base is worth Value units of Quote. Value is the
// exact decimal the provider published, never a float.
type CachedRate struct {
	Provider         string `json:"provider"`
	Source           string `json:"source"`
	Base             string `json:"base"`
	Quote            string `json:"quote"`
	Value            string `json:"value"`
	Date             string `json:"date"`
	RetrievedAt      string `json:"retrieved_at"`
	FirstRetrievedAt string `json:"first_retrieved_at"`
	ConfirmedThrough string `json:"confirmed_through"`
	// Stale is decision D11 evaluated now: a stale rate is never used in a
	// converted total. StaleReason is not_retrieved_recently or
	// rate_date_too_old, nil when the rate is not stale.
	Stale       bool    `json:"stale"`
	StaleReason *string `json:"stale_reason"`
	// Previous is the value the provider revised away, nil when it never did.
	Previous *RatePrevious `json:"previous"`
}

// RatePrevious is a rate value a provider revised away.
type RatePrevious struct {
	Value     string `json:"value"`
	RevisedAt string `json:"revised_at"`
}

// RatesRunError is the first failure of a refresh run: a kind and a short
// code, never a URL or a provider message.
type RatesRunError struct {
	Kind string `json:"kind"`
	Code string `json:"code"`
}

// RatesRefreshRun is one exchange-rate refresh run.
type RatesRefreshRun struct {
	Trigger    string `json:"trigger"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
	// Outcome is succeeded, partial or failed.
	Outcome             string         `json:"outcome"`
	CurrenciesRefreshed int            `json:"currencies_refreshed"`
	Error               *RatesRunError `json:"error"`
}

// RatesStatus reports on the optional rate provider and the last refresh.
type RatesStatus struct {
	Enabled             bool             `json:"enabled"`
	Provider            string           `json:"provider"`
	Source              string           `json:"source"`
	ReportingCurrency   *string          `json:"reporting_currency"`
	RefreshAvailable    bool             `json:"refresh_available"`
	RefreshIntervalSecs int              `json:"refresh_interval_seconds"`
	RefreshAfterSecs    int              `json:"refresh_after_seconds"`
	StaleAfterDays      int              `json:"stale_after_days"`
	LastRun             *RatesRefreshRun `json:"last_run"`
	LastSuccessAt       *string          `json:"last_success_at"`
	AsOf                string           `json:"as_of"`
}
