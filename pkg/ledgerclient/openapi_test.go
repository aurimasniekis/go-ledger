package ledgerclient

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/aurimasniekis/go-ledger/internal/httpapi"
)

// These tests tie the client to the served OpenAPI document (ADR-0032): they
// fail when an operation, a query parameter, a request or response member or
// an enum changes on one side only.

type node = map[string]any

func loadDoc(t *testing.T) node {
	t.Helper()
	var doc node
	if err := yaml.Unmarshal(httpapi.OpenAPIYAML(), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func get(n any, keys ...string) any {
	for _, k := range keys {
		m, ok := n.(node)
		if !ok {
			return nil
		}
		n = m[k]
	}
	return n
}

// resolve follows a local $ref.
func resolve(doc node, n node) node {
	for {
		ref, ok := n["$ref"].(string)
		if !ok {
			return n
		}
		cur := any(doc)
		for _, k := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			cur = get(cur, k)
		}
		next, ok := cur.(node)
		if !ok {
			panic("unresolvable ref " + ref)
		}
		n = next
	}
}

// schemaProps returns the member names a schema allows and requires,
// following $ref, allOf and oneOf.
func schemaProps(doc node, s node) (props, required map[string]bool) {
	props, required = map[string]bool{}, map[string]bool{}
	s = resolve(doc, s)
	if p, ok := s["properties"].(node); ok {
		for k := range p {
			props[k] = true
		}
	}
	if r, ok := s["required"].([]any); ok {
		for _, k := range r {
			required[k.(string)] = true
		}
	}
	for _, key := range []string{"allOf", "oneOf"} {
		subs, _ := s[key].([]any)
		var first = true
		for _, sub := range subs {
			sp, sr := schemaProps(doc, sub.(node))
			for k := range sp {
				props[k] = true
			}
			if key == "allOf" {
				for k := range sr {
					required[k] = true
				}
				continue
			}
			// oneOf: only members every variant requires are required.
			if first {
				for k := range sr {
					required[k] = true
				}
				first = false
			} else {
				for k := range required {
					if !sr[k] {
						delete(required, k)
					}
				}
			}
		}
	}
	return props, required
}

func jsonTags(t reflect.Type) map[string]bool {
	out := map[string]bool{}
	for i := range t.NumField() {
		tag := t.Field(i).Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name != "" && name != "-" {
			out[name] = true
		}
	}
	return out
}

// alwaysSent returns the JSON member names a type marshals without omitempty.
func alwaysSent(t reflect.Type) map[string]bool {
	out := map[string]bool{}
	for i := range t.NumField() {
		name, opts, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" && !strings.Contains(opts, "omitempty") {
			out[name] = true
		}
	}
	return out
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// notImplemented lists documented operations the client deliberately has no
// method for, with the reason. Every other documented operation needs a row in
// operations.
var notImplemented = map[string]string{
	"getTransactionMerchant":         "merchant identity (E1, ADR-0051): the client method, CLI command and MCP tool arrive with the merchant UI and its slice of P-MCP2; the routes are usable through Client.Raw",
	"reviewTransactionMerchant":      "merchant identity (E1, ADR-0051): see getTransactionMerchant",
	"clearTransactionMerchantReview": "merchant identity (E1, ADR-0051): see getTransactionMerchant",
	"getOpenAPIJSON":                 "the API description itself; the client is built against the document",
	"getOpenAPIYAML":                 "the API description itself",
	"postMCP":                        "the Model Context Protocol endpoint (ADR-0038); MCP clients speak JSON-RPC to it, and 'ledger mcp stdio' reaches the API through Client.Raw",
	"setUserPassword":                "credential change; session/password flows are for the UI and 'ledger auth set-password', and need recent authentication that a token is not meant to bypass",
	"signIn":                         "cookie sessions belong to the browser UI; the client authenticates with API tokens",
	"signOut":                        "cookie sessions",
	"getCurrentSession":              "cookie sessions",
	"listMySessions":                 "cookie sessions",
	"endMySession":                   "cookie sessions",
	"reauthenticate":                 "cookie sessions",
	"changeMyPassword":               "cookie sessions",

	"signInSecondFactor":      "session-only: the second step of a browser sign-in, so not reachable with a bearer token",
	"listMyTokens":            "session-only: own tokens are managed with the browser session; a bearer token uses the admin token routes (listTokens)",
	"getMyToken":              "session-only: own tokens are managed with the browser session",
	"revokeMyToken":           "session-only: own tokens are managed with the browser session",
	"getMyTwoFactor":          "session-only: two-factor management is for the signed-in browser session, not reachable with a bearer token",
	"disableMyTwoFactor":      "session-only: two-factor management, not reachable with a bearer token",
	"startTOTPEnrollment":     "session-only: two-factor enrolment, not reachable with a bearer token",
	"confirmTOTPEnrollment":   "session-only: two-factor enrolment, not reachable with a bearer token",
	"regenerateRecoveryCodes": "session-only: recovery codes, not reachable with a bearer token",

	"completeBankConnection":    "browser-only: the callback page posts the bank's one-time consent state and code with the browser session; the client and CLI never handle consent codes (start and reconnect return the consent URL instead)",
	"getOutlook":                "the outlook serves the browser UI (task FX3); a typed client method comes with a CLI or MCP view of it, which reuses internal/domain/outlook",
	"getOccurrencesSummary":     "the monthly view serves the browser UI (task C4); a typed client method and CLI view come with the MCP read tool (P-MCP2), which reuses internal/domain/monthview",
	"getSignInMethods":          "what the browser sign-in page offers; the client authenticates with API tokens",
	"startOIDCSignIn":           "browser redirect to the identity provider (single sign-on), not a JSON operation",
	"oidcCallback":              "browser redirect from the identity provider, not a JSON operation",
	"recoverySignIn":            "browser redirect for a host-issued one-time sign-in link, not a JSON operation",
	"recoverySignInPage":        "HTML confirmation page of a host-issued one-time sign-in link, not a JSON operation",
	"getMyOIDCLink":             "session-only: single-sign-on links are managed with the browser session",
	"startOIDCLink":             "session-only: linking needs a browser at the identity provider and recent re-authentication",
	"unlinkMyOIDC":              "session-only: credential change with recent re-authentication",
	"startOIDCReauthentication": "session-only: re-authentication of a browser session at the identity provider",
}

func TestOperationsMatchOpenAPI(t *testing.T) {
	doc := loadDoc(t)
	paths, _ := doc["paths"].(node)
	documented := map[string]bool{}
	for path, item := range paths {
		for method, op := range item.(node) {
			o, ok := op.(node)
			if !ok || method == "parameters" {
				continue
			}
			documented[o["operationId"].(string)] = true
			_ = path
		}
	}
	implemented := map[string]bool{}
	for _, op := range operations {
		implemented[op.ID] = true
		item, _ := paths[op.Path].(node)
		o, _ := item[strings.ToLower(op.Method)].(node)
		if o == nil || o["operationId"] != op.ID {
			t.Errorf("%s: no %s %s with that operationId in openapi.yaml", op.ID, op.Method, op.Path)
			continue
		}
		var q []string
		params, _ := o["parameters"].([]any)
		for _, p := range params {
			pn := resolve(doc, p.(node))
			if pn["in"] == "query" {
				q = append(q, pn["name"].(string))
			}
		}
		if !slices.Equal(q, op.Query) {
			t.Errorf("%s: query parameters %v, openapi.yaml has %v", op.ID, op.Query, q)
		}
	}
	for id := range documented {
		if !implemented[id] && notImplemented[id] == "" {
			t.Errorf("openapi.yaml operation %s has no client method and no entry in notImplemented", id)
		}
		if implemented[id] && notImplemented[id] != "" {
			t.Errorf("%s is implemented and listed in notImplemented", id)
		}
	}
	for id := range notImplemented {
		if !documented[id] {
			t.Errorf("notImplemented lists %s, which openapi.yaml no longer has", id)
		}
	}
}

func TestTypesMatchSchemas(t *testing.T) {
	doc := loadDoc(t)
	schemas := get(doc, "components", "schemas").(node)
	ref := func(name string) node { return schemas[name].(node) }
	// request is the body schema of an operation.
	body := func(path, method string) node {
		rb := get(doc, "paths", path, method, "requestBody", "content").(node)
		for _, c := range rb {
			return get(c, "schema").(node)
		}
		return nil
	}
	cases := []struct {
		name   string
		typ    reflect.Type
		schema node
		// response types must carry every documented member; request types
		// may omit nothing either but only require what the schema requires.
		request bool
	}{
		{"Principal", reflect.TypeFor[Principal](), ref("Principal"), false},
		{"User", reflect.TypeFor[User](), ref("User"), false},
		{"Commitment", reflect.TypeFor[Commitment](), ref("Commitment"), false},
		{"Occurrence", reflect.TypeFor[Occurrence](), ref("Occurrence"), false},
		{"AuditEntry", reflect.TypeFor[AuditEntry](), ref("AuditEntry"), false},
		{"PaymentState", reflect.TypeFor[PaymentState](), ref("PaymentState"), false},
		{"PaymentOverride", reflect.TypeFor[PaymentOverride](), get(ref("PaymentState"), "properties", "override", "oneOf").([]any)[1].(node), false},
		{"ManualPayment", reflect.TypeFor[ManualPayment](), ref("ManualPayment"), false},
		{"APIToken", reflect.TypeFor[APIToken](), ref("ApiToken"), false},
		{"CreatedAPIToken", reflect.TypeFor[CreatedAPIToken](), ref("CreatedApiToken"), false},
		{"NewAPIToken", reflect.TypeFor[NewAPIToken](), ref("NewApiToken"), true},
		{"NewManualPayment", reflect.TypeFor[NewManualPayment](), ref("ManualPaymentInput"), true},
		{"Money", reflect.TypeFor[Money](), ref("PositiveMoney"), false},
		{"Schedule", reflect.TypeFor[Schedule](), ref("Schedule"), false},
		{"GrantVocabulary", reflect.TypeFor[GrantVocabulary](), ref("GrantVocabulary"), false},
		{"GrantResource", reflect.TypeFor[GrantResource](), get(ref("GrantVocabulary"), "properties", "resources", "items").(node), false},
		{"GrantRole", reflect.TypeFor[GrantRole](), get(ref("GrantVocabulary"), "properties", "roles", "items").(node), false},
		{"APIError", reflect.TypeFor[APIError](), ref("Error"), false},
		{"FieldError", reflect.TypeFor[FieldError](), get(ref("Error"), "properties", "errors", "items").(node), false},
		{"Reconciliation", reflect.TypeFor[Reconciliation](), ref("Reconciliation"), false},
		{"NewUser", reflect.TypeFor[NewUser](), body("/users", "post"), true},
		{"NewCommitment", reflect.TypeFor[NewCommitment](), body("/commitments", "post"), true},
		{"PaymentMatch", reflect.TypeFor[PaymentMatch](), ref("PaymentMatch"), false},
		{"MatchedBankTransaction", reflect.TypeFor[MatchedBankTransaction](), ref("MatchedBankTransaction"), false},
		{"MatchActor", reflect.TypeFor[MatchActor](), ref("MatchActor"), false},
		{"MatchEvent", reflect.TypeFor[MatchEvent](), get(ref("PaymentMatch"), "properties", "history", "items").(node), false},
		{"NewPaymentMatch", reflect.TypeFor[NewPaymentMatch](), body("/payment-matches", "post"), true},
		{"PaymentMatchingRun", reflect.TypeFor[PaymentMatchingRun](), ref("PaymentMatchingRun"), false},
		{"BankApplication", reflect.TypeFor[BankApplication](), ref("BankApplication"), false},
		{"BankCapabilities", reflect.TypeFor[BankCapabilities](), get(ref("BankApplication"), "properties", "capabilities").(node), false},
		{"BankInstitution", reflect.TypeFor[BankInstitution](), ref("BankInstitution"), false},
		{"BankError", reflect.TypeFor[BankError](), ref("BankError"), false},
		{"BankFreshness", reflect.TypeFor[BankFreshness](), ref("BankFreshness"), false},
		{"BankBalance", reflect.TypeFor[BankBalance](), get(ref("BankAccount"), "properties", "balances", "items").(node), false},
		{"BankAccount", reflect.TypeFor[BankAccount](), ref("BankAccount"), false},
		{"BankRefreshRun", reflect.TypeFor[BankRefreshRun](), ref("BankRefreshRun"), false},
		{"BankSchedule", reflect.TypeFor[BankSchedule](), get(ref("BankConnection"), "properties", "schedule", "oneOf").([]any)[0].(node), false},
		{"BankConnectionInstitution", reflect.TypeFor[BankConnectionInstitution](), get(ref("BankConnection"), "properties", "institution").(node), false},
		{"BankConnection", reflect.TypeFor[BankConnection](), ref("BankConnection"), false},
		{"BankConsentStart", reflect.TypeFor[BankConsentStart](), ref("BankConsentStart"), false},
		{"CachedRate", reflect.TypeFor[CachedRate](), ref("CachedRate"), false},
		{"RatePrevious", reflect.TypeFor[RatePrevious](), get(ref("CachedRate"), "properties", "previous", "oneOf").([]any)[0].(node), false},
		{"RatesRefreshRun", reflect.TypeFor[RatesRefreshRun](), ref("RatesRefreshRun"), false},
		{"RatesRunError", reflect.TypeFor[RatesRunError](), get(ref("RatesRefreshRun"), "properties", "error", "oneOf").([]any)[0].(node), false},
		{"RatesStatus", reflect.TypeFor[RatesStatus](), ref("RatesStatus"), false},
		{"NewBankConnection", reflect.TypeFor[NewBankConnection](), body("/bank-connections", "post"), true},
		{"DeferralPlan", reflect.TypeFor[DeferralPlan](), ref("DeferralPlan"), false},
		{"DeferralAdjustment", reflect.TypeFor[DeferralAdjustment](), ref("DeferralAdjustment"), false},
		{"DeferralPlanEvent", reflect.TypeFor[DeferralPlanEvent](), ref("DeferralPlanEvent"), false},
		{"NewDeferralPlan", reflect.TypeFor[NewDeferralPlan](), body("/deferral-plans", "post"), true},
	}
	// Match action bodies also allow "version"; the client sends it as If-Match.
	for path, typ := range map[string]reflect.Type{
		"/payment-matches/{id}/confirm":    reflect.TypeFor[MatchDecision](),
		"/payment-matches/{id}/correct":    reflect.TypeFor[MatchCorrection](),
		"/deferral-plans/{id}/undo":        reflect.TypeFor[DeferralUndo](),
		"/deferral-plans/{id}/adjustments": reflect.TypeFor[NewDeferralAdjustment](),
	} {
		mp, _ := schemaProps(doc, body(path, "post"))
		delete(mp, "version")
		if !slices.Equal(keys(mp), keys(jsonTags(typ))) {
			t.Errorf("%s: members %v, openapi.yaml has %v (minus version)", typ.Name(), keys(jsonTags(typ)), keys(mp))
		}
	}
	for _, c := range cases {
		props, required := schemaProps(doc, c.schema)
		have := jsonTags(c.typ)
		if !slices.Equal(keys(props), keys(have)) {
			t.Errorf("%s: members %v, openapi.yaml has %v", c.name, keys(have), keys(props))
		}
		for k := range required {
			if !have[k] {
				t.Errorf("%s: required member %s missing", c.name, k)
			}
		}
		// A response always carries every member its Go type marshals
		// (no omitempty), so the schema must require each of them.
		if !c.request {
			for k := range alwaysSent(c.typ) {
				if !required[k] {
					t.Errorf("%s: member %s is always sent but openapi.yaml does not require it", c.name, k)
				}
			}
		}
	}
	// The override body also allows "version"; the client sends it as If-Match.
	op, _ := schemaProps(doc, body("/occurrences/{id}/status-override", "put"))
	delete(op, "version")
	if !slices.Equal(keys(op), keys(jsonTags(reflect.TypeFor[StatusOverride]()))) {
		t.Errorf("StatusOverride: members %v, openapi.yaml has %v (minus version)", keys(jsonTags(reflect.TypeFor[StatusOverride]())), keys(op))
	}
	// A Page decodes the documented list envelope.
	lp, lr := schemaProps(doc, get(doc, "paths", "/users", "get", "responses", "200", "content", "application/json", "schema").(node))
	if !slices.Equal(keys(lp), keys(jsonTags(reflect.TypeFor[Page[User]]()))) || !lr["items"] || !lr["next_cursor"] {
		t.Errorf("Page members differ from the list envelope: %v", keys(lp))
	}
}

func TestEnumsMatchSchemas(t *testing.T) {
	doc := loadDoc(t)
	enum := func(name string) []string {
		var out []string
		for _, v := range get(doc, "components", "schemas", name, "enum").([]any) {
			out = append(out, v.(string))
		}
		slices.Sort(out)
		return out
	}
	check := func(name string, consts ...string) {
		slices.Sort(consts)
		if got := enum(name); !slices.Equal(got, consts) {
			t.Errorf("%s: client has %v, openapi.yaml has %v", name, consts, got)
		}
	}
	check("CommitmentKind", KindService, KindServer, KindDomain, KindSubscription, KindDebtRepayment)
	check("CommitmentPriority", PriorityEssential, PriorityImportant, PriorityFlexible)
	check("Role", RoleAdministrator, RoleReadOnly)
	check("AuditActorKind", ActorUser, ActorToken, ActorSystem, ActorAnonymous)
	check("PaymentStatus", PaymentPaid, PaymentPartiallyPaid, PaymentUpcoming, PaymentDue, PaymentOverdue, PaymentNotRecorded, PaymentCancelled)
	check("OverrideStatus", OverridePaid, OverrideUnpaid)
	check("MatchState", MatchSuggested, MatchConfirmed, MatchRejected, MatchReversed)
	check("MatchOrigin", OriginAutoRule, OriginModel, OriginUser)
	check("DeferralPlanType", DeferralPersonal, DeferralCreditorAgreed, DeferralDocumentedGrace)
	check("DeferralPlanState", DeferralActive, DeferralUndone)
	check("DeferralAdjustmentKind", AdjustmentFee, AdjustmentInterest, AdjustmentOther)
	var ts []string
	for _, v := range get(doc, "components", "schemas", "ApiToken", "properties", "status", "enum").([]any) {
		ts = append(ts, v.(string))
	}
	slices.Sort(ts)
	if !slices.Equal(ts, []string{TokenActive, TokenExpired, TokenRevoked}) {
		t.Errorf("token statuses %v", ts)
	}
	var freq []string
	for _, n := range []string{"MonthlySchedule", "AnnualSchedule"} {
		freq = append(freq, get(doc, "components", "schemas", n, "properties", "frequency", "const").(string))
	}
	slices.Sort(freq)
	if !slices.Equal(freq, []string{FrequencyAnnual, FrequencyMonthly}) {
		t.Errorf("schedule frequencies %v", freq)
	}
}

func TestErrorCodesExistInOpenAPI(t *testing.T) {
	doc := loadDoc(t)
	var codes []string
	for _, v := range get(doc, "components", "schemas", "Error", "properties", "code", "enum").([]any) {
		codes = append(codes, v.(string))
	}
	for _, c := range []string{CodeValidation, CodeUnauthenticated, CodeForbidden, CodeNotFound, CodeConflict, CodeVersionConflict, CodePrecondition, CodePendingTerms, CodeRateLimited, CodeUnavailable, CodeInternal} {
		if !slices.Contains(codes, c) {
			t.Errorf("error code %s is not in openapi.yaml", c)
		}
	}
}
