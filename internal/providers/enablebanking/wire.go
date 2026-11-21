package enablebanking

// Wire types: the subset of Enable Banking's OpenAPI schemas (spec
// 1.0.0-1c03cf81) the adapter reads or sends. Unknown fields are ignored.

type wireASPSP struct {
	Name    string `json:"name"`
	Country string `json:"country"`
}

type wireASPSPData struct {
	Name                   string   `json:"name"`
	Country                string   `json:"country"`
	MaximumConsentValidity int64    `json:"maximum_consent_validity"`
	PSUTypes               []string `json:"psu_types"`
}

type wireASPSPs struct {
	ASPSPs []wireASPSPData `json:"aspsps"`
}

type wireAccess struct {
	ValidUntil string `json:"valid_until"`
}

type wireStartAuthorization struct {
	Access      wireAccess `json:"access"`
	ASPSP       wireASPSP  `json:"aspsp"`
	State       string     `json:"state"`
	RedirectURL string     `json:"redirect_url"`
	PSUType     string     `json:"psu_type,omitempty"`
}

type wireStartAuthorizationResponse struct {
	URL             string `json:"url"`
	AuthorizationID string `json:"authorization_id"`
}

type wireAuthorizeSession struct {
	Code string `json:"code"`
}

type wireAuthorizeSessionResponse struct {
	SessionID string                `json:"session_id"`
	Accounts  []wireAccountResource `json:"accounts"`
	Access    wireAccess            `json:"access"`
}

type wireSessionAccount struct {
	UID                string `json:"uid"`
	IdentificationHash string `json:"identification_hash"`
}

type wireSession struct {
	Status       string               `json:"status"`
	Accounts     []string             `json:"accounts"`
	AccountsData []wireSessionAccount `json:"accounts_data"`
	Access       wireAccess           `json:"access"`
}

type wireGenericIdentification struct {
	Identification string `json:"identification"`
	SchemeName     string `json:"scheme_name"`
}

type wireAccountIdentification struct {
	IBAN  string                     `json:"iban"`
	Other *wireGenericIdentification `json:"other"`
}

type wireAccountResource struct {
	AccountID          *wireAccountIdentification  `json:"account_id"`
	AllAccountIDs      []wireGenericIdentification `json:"all_account_ids"`
	Details            string                      `json:"details"`
	Product            string                      `json:"product"`
	Currency           string                      `json:"currency"`
	CashAccountType    string                      `json:"cash_account_type"`
	UID                string                      `json:"uid"`
	IdentificationHash string                      `json:"identification_hash"`
}

type wireAmount struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
}

type wireBalance struct {
	BalanceAmount wireAmount `json:"balance_amount"`
	BalanceType   string     `json:"balance_type"`
	ReferenceDate string     `json:"reference_date"`
}

type wireBalances struct {
	Balances []wireBalance `json:"balances"`
}

type wireParty struct {
	Name string `json:"name"`
}

type wireExchangeRate struct {
	InstructedAmount *wireAmount `json:"instructed_amount"`
}

type wireTransaction struct {
	EntryReference        string            `json:"entry_reference"`
	TransactionID         string            `json:"transaction_id"`
	TransactionAmount     wireAmount        `json:"transaction_amount"`
	CreditDebitIndicator  string            `json:"credit_debit_indicator"`
	Status                string            `json:"status"`
	BookingDate           string            `json:"booking_date"`
	ValueDate             string            `json:"value_date"`
	TransactionDate       string            `json:"transaction_date"`
	Creditor              *wireParty        `json:"creditor"`
	Debtor                *wireParty        `json:"debtor"`
	RemittanceInformation []string          `json:"remittance_information"`
	ExchangeRate          *wireExchangeRate `json:"exchange_rate"`
}

type wireTransactions struct {
	Transactions    []wireTransaction `json:"transactions"`
	ContinuationKey *string           `json:"continuation_key"`
}

type wireApplication struct {
	Kid         string `json:"kid"`
	Environment string `json:"environment"`
	Active      bool   `json:"active"`
}
