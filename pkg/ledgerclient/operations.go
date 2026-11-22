package ledgerclient

import "net/http"

// operation is one OpenAPI operation the client implements. TestOperations
// compares this table with openapi.yaml: method, path, operationId and query
// parameters must match, and every documented operation must be here or in
// the test's list of deliberate exclusions.
type operation struct {
	ID     string
	Method string
	Path   string
	// Query lists the documented query parameter names (including the shared
	// limit and cursor) in the order the OpenAPI document gives them.
	Query []string
}

var (
	opGetMe        = operation{ID: "getMe", Method: http.MethodGet, Path: "/me"}
	opGetGrants    = operation{ID: "getGrantVocabulary", Method: http.MethodGet, Path: "/grants"}
	opListAudit    = operation{ID: "listAuditEntries", Method: http.MethodGet, Path: "/audit", Query: []string{"limit", "cursor", "action", "action[prefix]", "actor_kind", "actor_user_id", "actor_token_id", "target_type", "target_id", "at[gte]", "at[lt]"}}
	opListUsers    = operation{ID: "listUsers", Method: http.MethodGet, Path: "/users", Query: []string{"limit", "cursor", "role", "disabled", "username[prefix]"}}
	opCreateUser   = operation{ID: "createUser", Method: http.MethodPost, Path: "/users"}
	opGetUser      = operation{ID: "getUser", Method: http.MethodGet, Path: "/users/{id}"}
	opUpdateUser   = operation{ID: "updateUser", Method: http.MethodPatch, Path: "/users/{id}"}
	opListCommit   = operation{ID: "listCommitments", Method: http.MethodGet, Path: "/commitments", Query: []string{"limit", "cursor", "kind", "priority", "currency"}}
	opCreateCommit = operation{ID: "createCommitment", Method: http.MethodPost, Path: "/commitments"}
	opGetCommit    = operation{ID: "getCommitment", Method: http.MethodGet, Path: "/commitments/{id}"}
	opUpdateCommit = operation{ID: "updateCommitment", Method: http.MethodPatch, Path: "/commitments/{id}"}
	opDeleteCommit = operation{ID: "deleteCommitment", Method: http.MethodDelete, Path: "/commitments/{id}"}
	opListOccur    = operation{ID: "listOccurrences", Method: http.MethodGet, Path: "/occurrences", Query: []string{"limit", "cursor", "month", "commitment_id", "include_cancelled"}}
	opGetOccur     = operation{ID: "getOccurrence", Method: http.MethodGet, Path: "/occurrences/{id}"}

	opListManualPayments  = operation{ID: "listManualPayments", Method: http.MethodGet, Path: "/occurrences/{id}/manual-payments", Query: []string{"limit", "cursor"}}
	opRecordManualPayment = operation{ID: "recordManualPayment", Method: http.MethodPost, Path: "/occurrences/{id}/manual-payments"}
	opGetManualPayment    = operation{ID: "getManualPayment", Method: http.MethodGet, Path: "/occurrences/{id}/manual-payments/{payment_id}"}
	opDeleteManualPayment = operation{ID: "deleteManualPayment", Method: http.MethodDelete, Path: "/occurrences/{id}/manual-payments/{payment_id}"}
	opSetOverride         = operation{ID: "setOccurrenceStatusOverride", Method: http.MethodPut, Path: "/occurrences/{id}/status-override"}
	opUndoOverride        = operation{ID: "undoOccurrenceStatusOverride", Method: http.MethodDelete, Path: "/occurrences/{id}/status-override"}

	opListMatches    = operation{ID: "listPaymentMatches", Method: http.MethodGet, Path: "/payment-matches", Query: []string{"limit", "cursor", "transaction_id", "occurrence_id", "state", "origin"}}
	opCreateMatch    = operation{ID: "createPaymentMatch", Method: http.MethodPost, Path: "/payment-matches"}
	opGetMatch       = operation{ID: "getPaymentMatch", Method: http.MethodGet, Path: "/payment-matches/{id}"}
	opConfirmMatch   = operation{ID: "confirmPaymentMatch", Method: http.MethodPost, Path: "/payment-matches/{id}/confirm"}
	opRejectMatch    = operation{ID: "rejectPaymentMatch", Method: http.MethodPost, Path: "/payment-matches/{id}/reject"}
	opUnmatchMatch   = operation{ID: "unmatchPaymentMatch", Method: http.MethodPost, Path: "/payment-matches/{id}/unmatch"}
	opUnconfirmMatch = operation{ID: "unconfirmPaymentMatch", Method: http.MethodPost, Path: "/payment-matches/{id}/unconfirm"}
	opReplaceMatch   = operation{ID: "confirmPaymentMatchReplacing", Method: http.MethodPost, Path: "/payment-matches/{id}/confirm-replacing"}
	opCorrectMatch   = operation{ID: "correctPaymentMatch", Method: http.MethodPost, Path: "/payment-matches/{id}/correct"}
	opReverseMatch   = operation{ID: "reversePaymentMatch", Method: http.MethodPost, Path: "/payment-matches/{id}/reverse"}
	opRunMatching    = operation{ID: "runPaymentMatching", Method: http.MethodPost, Path: "/payment-matching/runs"}

	opListDeferrals        = operation{ID: "listDeferralPlans", Method: http.MethodGet, Path: "/deferral-plans", Query: []string{"limit", "cursor", "occurrence_id", "commitment_id", "state", "planned_month"}}
	opCreateDeferral       = operation{ID: "createDeferralPlan", Method: http.MethodPost, Path: "/deferral-plans"}
	opGetDeferral          = operation{ID: "getDeferralPlan", Method: http.MethodGet, Path: "/deferral-plans/{id}"}
	opReviseDeferral       = operation{ID: "reviseDeferralPlan", Method: http.MethodPatch, Path: "/deferral-plans/{id}"}
	opUndoDeferral         = operation{ID: "undoDeferralPlan", Method: http.MethodPost, Path: "/deferral-plans/{id}/undo"}
	opDeferralHistory      = operation{ID: "listDeferralPlanHistory", Method: http.MethodGet, Path: "/deferral-plans/{id}/history", Query: []string{"limit", "cursor"}}
	opAddDeferralAdjust    = operation{ID: "addDeferralAdjustment", Method: http.MethodPost, Path: "/deferral-plans/{id}/adjustments"}
	opRemoveDeferralAdjust = operation{ID: "removeDeferralAdjustment", Method: http.MethodDelete, Path: "/deferral-plans/{id}/adjustments/{adjustment_id}"}

	opListTokens           = operation{ID: "listTokens", Method: http.MethodGet, Path: "/tokens", Query: []string{"limit", "cursor", "owner_id"}}
	opGetToken             = operation{ID: "getToken", Method: http.MethodGet, Path: "/tokens/{id}"}
	opRevokeToken          = operation{ID: "revokeToken", Method: http.MethodDelete, Path: "/tokens/{id}"}
	opListBankApps         = operation{ID: "listBankApplications", Method: http.MethodGet, Path: "/bank-applications"}
	opListBankInstitutions = operation{ID: "listBankInstitutions", Method: http.MethodGet, Path: "/bank-applications/{application}/institutions", Query: []string{"country"}}
	opListBankConns        = operation{ID: "listBankConnections", Method: http.MethodGet, Path: "/bank-connections", Query: []string{"limit", "cursor"}}
	opStartBankConn        = operation{ID: "startBankConnection", Method: http.MethodPost, Path: "/bank-connections"}
	opGetBankConn          = operation{ID: "getBankConnection", Method: http.MethodGet, Path: "/bank-connections/{id}"}
	opReconnectBankConn    = operation{ID: "reconnectBankConnection", Method: http.MethodPost, Path: "/bank-connections/{id}/reconnect"}
	opRefreshBankConn      = operation{ID: "refreshBankConnection", Method: http.MethodPost, Path: "/bank-connections/{id}/refresh"}
	opDisconnectBankConn   = operation{ID: "disconnectBankConnection", Method: http.MethodPost, Path: "/bank-connections/{id}/disconnect"}

	opListRates    = operation{ID: "listRates", Method: http.MethodGet, Path: "/rates", Query: []string{"limit", "cursor", "provider", "source", "base", "quote", "date[gte]", "date[lte]"}}
	opRatesStatus  = operation{ID: "getRatesStatus", Method: http.MethodGet, Path: "/rates/status"}
	opRefreshRates = operation{ID: "refreshRates", Method: http.MethodPost, Path: "/rates/refreshes"}

	// opCreateMyToken needs a root token of the root account (decision D25).
	opCreateMyToken = operation{ID: "createMyToken", Method: http.MethodPost, Path: "/auth/tokens"}
)

// operations is every implemented operation. Resources of tasks still in
// flight are added here, with a method and a test row, when their OpenAPI
// operations land.
var operations = []operation{
	opGetMe, opGetGrants, opListAudit,
	opListUsers, opCreateUser, opGetUser, opUpdateUser,
	opListCommit, opCreateCommit, opGetCommit, opUpdateCommit, opDeleteCommit,
	opListOccur, opGetOccur,
	opListManualPayments, opRecordManualPayment, opGetManualPayment, opDeleteManualPayment,
	opSetOverride, opUndoOverride,
	opListMatches, opCreateMatch, opGetMatch, opConfirmMatch, opRejectMatch, opUnmatchMatch, opCorrectMatch, opReverseMatch, opUnconfirmMatch, opReplaceMatch,
	opRunMatching,
	opListDeferrals, opCreateDeferral, opGetDeferral, opReviseDeferral, opUndoDeferral, opDeferralHistory, opAddDeferralAdjust, opRemoveDeferralAdjust,
	opListBankApps, opListBankInstitutions, opListBankConns, opStartBankConn, opGetBankConn, opReconnectBankConn, opRefreshBankConn, opDisconnectBankConn,
	opListRates, opRatesStatus, opRefreshRates,
	opListTokens, opGetToken, opRevokeToken, opCreateMyToken,
}
