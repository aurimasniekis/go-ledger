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
	opListOccur    = operation{ID: "listOccurrences", Method: http.MethodGet, Path: "/occurrences", Query: []string{"limit", "cursor", "month", "commitment_id"}}
	opGetOccur     = operation{ID: "getOccurrence", Method: http.MethodGet, Path: "/occurrences/{id}"}

	opListManualPayments  = operation{ID: "listManualPayments", Method: http.MethodGet, Path: "/occurrences/{id}/manual-payments", Query: []string{"limit", "cursor"}}
	opRecordManualPayment = operation{ID: "recordManualPayment", Method: http.MethodPost, Path: "/occurrences/{id}/manual-payments"}
	opGetManualPayment    = operation{ID: "getManualPayment", Method: http.MethodGet, Path: "/occurrences/{id}/manual-payments/{payment_id}"}
	opDeleteManualPayment = operation{ID: "deleteManualPayment", Method: http.MethodDelete, Path: "/occurrences/{id}/manual-payments/{payment_id}"}
	opSetOverride         = operation{ID: "setOccurrenceStatusOverride", Method: http.MethodPut, Path: "/occurrences/{id}/status-override"}
	opUndoOverride        = operation{ID: "undoOccurrenceStatusOverride", Method: http.MethodDelete, Path: "/occurrences/{id}/status-override"}

	opListTokens  = operation{ID: "listTokens", Method: http.MethodGet, Path: "/tokens", Query: []string{"limit", "cursor", "owner_id"}}
	opGetToken    = operation{ID: "getToken", Method: http.MethodGet, Path: "/tokens/{id}"}
	opRevokeToken = operation{ID: "revokeToken", Method: http.MethodDelete, Path: "/tokens/{id}"}
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
	opListTokens, opGetToken, opRevokeToken, opCreateMyToken,
}
