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
	opMarkAdjustmentPaid   = operation{ID: "markDeferralAdjustmentPaid", Method: http.MethodPost, Path: "/deferral-plans/{id}/adjustments/{adjustment_id}/paid"}
	opUnmarkAdjustmentPaid = operation{ID: "unmarkDeferralAdjustmentPaid", Method: http.MethodPost, Path: "/deferral-plans/{id}/adjustments/{adjustment_id}/unpaid"}

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

	// Organisation (tasks O1 and O2, ADR-0053).
	opListCategories      = operation{ID: "listCategories", Method: http.MethodGet, Path: "/categories", Query: []string{"limit", "cursor", "archived", "parent_id", "name[prefix]"}}
	opCreateCategory      = operation{ID: "createCategory", Method: http.MethodPost, Path: "/categories"}
	opGetCategory         = operation{ID: "getCategory", Method: http.MethodGet, Path: "/categories/{id}"}
	opUpdateCategory      = operation{ID: "updateCategory", Method: http.MethodPatch, Path: "/categories/{id}"}
	opArchiveCategory     = operation{ID: "archiveCategory", Method: http.MethodPost, Path: "/categories/{id}/archive"}
	opUnarchiveCategory   = operation{ID: "unarchiveCategory", Method: http.MethodPost, Path: "/categories/{id}/unarchive"}
	opDeleteCategory      = operation{ID: "deleteCategory", Method: http.MethodDelete, Path: "/categories/{id}", Query: []string{"reassign_to"}}
	opListLabels          = operation{ID: "listLabels", Method: http.MethodGet, Path: "/labels", Query: []string{"limit", "cursor", "archived", "name[prefix]"}}
	opCreateLabel         = operation{ID: "createLabel", Method: http.MethodPost, Path: "/labels"}
	opGetLabel            = operation{ID: "getLabel", Method: http.MethodGet, Path: "/labels/{id}"}
	opUpdateLabel         = operation{ID: "updateLabel", Method: http.MethodPatch, Path: "/labels/{id}"}
	opArchiveLabel        = operation{ID: "archiveLabel", Method: http.MethodPost, Path: "/labels/{id}/archive"}
	opUnarchiveLabel      = operation{ID: "unarchiveLabel", Method: http.MethodPost, Path: "/labels/{id}/unarchive"}
	opDeleteLabel         = operation{ID: "deleteLabel", Method: http.MethodDelete, Path: "/labels/{id}", Query: []string{"reassign_to"}}
	opListBuckets         = operation{ID: "listBuckets", Method: http.MethodGet, Path: "/buckets", Query: []string{"limit", "cursor", "archived", "name[prefix]"}}
	opCreateBucket        = operation{ID: "createBucket", Method: http.MethodPost, Path: "/buckets"}
	opGetBucket           = operation{ID: "getBucket", Method: http.MethodGet, Path: "/buckets/{id}"}
	opUpdateBucket        = operation{ID: "updateBucket", Method: http.MethodPatch, Path: "/buckets/{id}"}
	opArchiveBucket       = operation{ID: "archiveBucket", Method: http.MethodPost, Path: "/buckets/{id}/archive"}
	opUnarchiveBucket     = operation{ID: "unarchiveBucket", Method: http.MethodPost, Path: "/buckets/{id}/unarchive"}
	opDeleteBucket        = operation{ID: "deleteBucket", Method: http.MethodDelete, Path: "/buckets/{id}", Query: []string{"reassign_to"}}
	opListBucketMembers   = operation{ID: "listBucketMembers", Method: http.MethodGet, Path: "/buckets/{id}/members", Query: []string{"limit", "cursor"}}
	opAddBucketMember     = operation{ID: "addBucketMember", Method: http.MethodPut, Path: "/buckets/{id}/members/{object_type}/{object_id}"}
	opRemoveBucketMember  = operation{ID: "removeBucketMember", Method: http.MethodDelete, Path: "/buckets/{id}/members/{object_type}/{object_id}"}
	opGetObjectOrg        = operation{ID: "getObjectOrganisation", Method: http.MethodGet, Path: "/organisation/{object_type}/{object_id}"}
	opSetObjectCategory   = operation{ID: "setObjectCategory", Method: http.MethodPut, Path: "/organisation/{object_type}/{object_id}/category"}
	opResetObjectCategory = operation{ID: "resetObjectCategory", Method: http.MethodDelete, Path: "/organisation/{object_type}/{object_id}/category"}
	opAddObjectLabel      = operation{ID: "addObjectLabel", Method: http.MethodPut, Path: "/organisation/{object_type}/{object_id}/labels/{label_id}"}
	opRemoveObjectLabel   = operation{ID: "removeObjectLabel", Method: http.MethodDelete, Path: "/organisation/{object_type}/{object_id}/labels/{label_id}"}

	// Merchants (tasks E1, E2, M1, E3; ADR-0051, ADR-0054, ADR-0058).
	opListMerchants                  = operation{ID: "listMerchants", Method: http.MethodGet, Path: "/merchants", Query: []string{"limit", "cursor", "archived"}}
	opCreateMerchant                 = operation{ID: "createMerchant", Method: http.MethodPost, Path: "/merchants"}
	opGetMerchant                    = operation{ID: "getMerchant", Method: http.MethodGet, Path: "/merchants/{id}"}
	opUpdateMerchant                 = operation{ID: "updateMerchant", Method: http.MethodPatch, Path: "/merchants/{id}"}
	opDeleteMerchant                 = operation{ID: "deleteMerchant", Method: http.MethodDelete, Path: "/merchants/{id}"}
	opArchiveMerchant                = operation{ID: "archiveMerchant", Method: http.MethodPost, Path: "/merchants/{id}/archive"}
	opUnarchiveMerchant              = operation{ID: "unarchiveMerchant", Method: http.MethodPost, Path: "/merchants/{id}/unarchive"}
	opAddMerchantAlias               = operation{ID: "addMerchantAlias", Method: http.MethodPost, Path: "/merchants/{id}/aliases"}
	opRemoveMerchantAlias            = operation{ID: "removeMerchantAlias", Method: http.MethodDelete, Path: "/merchants/{id}/aliases/{alias_id}"}
	opMergeMerchants                 = operation{ID: "mergeMerchants", Method: http.MethodPost, Path: "/merchants/{id}/merge"}
	opListMerchantSources            = operation{ID: "listMerchantSources", Method: http.MethodGet, Path: "/merchants/sources"}
	opGetTransactionMerchant         = operation{ID: "getTransactionMerchant", Method: http.MethodGet, Path: "/transactions/{id}/merchant"}
	opReviewTransactionMerchant      = operation{ID: "reviewTransactionMerchant", Method: http.MethodPut, Path: "/transactions/{id}/merchant/review"}
	opClearTransactionMerchantReview = operation{ID: "clearTransactionMerchantReview", Method: http.MethodDelete, Path: "/transactions/{id}/merchant/review"}

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
	opListDeferrals, opCreateDeferral, opGetDeferral, opReviseDeferral, opUndoDeferral, opDeferralHistory, opAddDeferralAdjust, opRemoveDeferralAdjust, opMarkAdjustmentPaid, opUnmarkAdjustmentPaid,
	opListBankApps, opListBankInstitutions, opListBankConns, opStartBankConn, opGetBankConn, opReconnectBankConn, opRefreshBankConn, opDisconnectBankConn,
	opListRates, opRatesStatus, opRefreshRates,
	opListTokens, opGetToken, opRevokeToken, opCreateMyToken,
	opListCategories, opCreateCategory, opGetCategory, opUpdateCategory, opArchiveCategory, opUnarchiveCategory, opDeleteCategory, opListLabels, opCreateLabel, opGetLabel, opUpdateLabel, opArchiveLabel, opUnarchiveLabel, opDeleteLabel, opListBuckets, opCreateBucket, opGetBucket, opUpdateBucket, opArchiveBucket, opUnarchiveBucket, opDeleteBucket, opListBucketMembers, opAddBucketMember, opRemoveBucketMember, opGetObjectOrg, opSetObjectCategory, opResetObjectCategory, opAddObjectLabel, opRemoveObjectLabel,
	opListMerchants, opCreateMerchant, opGetMerchant, opUpdateMerchant, opDeleteMerchant, opArchiveMerchant, opUnarchiveMerchant, opAddMerchantAlias, opRemoveMerchantAlias, opMergeMerchants, opListMerchantSources,
	opGetTransactionMerchant, opReviewTransactionMerchant, opClearTransactionMerchantReview,
}
