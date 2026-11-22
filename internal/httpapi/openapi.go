package httpapi

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/auth"
	"github.com/aurimasniekis/go-ledger/internal/domain/bankconnect"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/deferrals"
	"github.com/aurimasniekis/go-ledger/internal/domain/matches"
	"github.com/aurimasniekis/go-ledger/internal/domain/merchants"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/monthview"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/organisation"
	"github.com/aurimasniekis/go-ledger/internal/domain/outlook"
	"github.com/aurimasniekis/go-ledger/internal/domain/payments"
	"github.com/aurimasniekis/go-ledger/internal/domain/tokens"
	"github.com/aurimasniekis/go-ledger/internal/domain/users"
)

// APIPrefix is the base path of version 1 of the Management API. A
// backwards-incompatible change gets a new prefix (ADR-0011).
const APIPrefix = "/api/v1"

//go:embed openapi.yaml
var openAPIYAML []byte

// OpenAPIYAML returns the OpenAPI document of the API, as written.
func OpenAPIYAML() []byte { return openAPIYAML }

var openAPIJSON = sync.OnceValues(func() ([]byte, error) { return yamlToJSON(openAPIYAML) })

// OpenAPIJSON returns the OpenAPI document converted to JSON.
func OpenAPIJSON() ([]byte, error) { return openAPIJSON() }

func yamlToJSON(doc []byte) ([]byte, error) {
	var v any
	if err := yaml.Unmarshal(doc, &v); err != nil {
		return nil, fmt.Errorf("openapi: parse YAML: %w", err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("openapi: convert to JSON (keys must be strings): %w", err)
	}
	return out, nil
}

// Deps are what the API handlers call. Every field must be set to serve
// requests; TestOpenAPIContract registers the routes with zero Deps and never
// calls them.
type Deps struct {
	Users       users.Store
	Commitments commitments.Store
	// Occurrences reads materialised commitment occurrences (ADR-0027).
	Occurrences occurrences.Reader
	// Payments stores manual payments and status overrides and reads the
	// facts payment state is derived from (ADR-0031). Grace is the hook for
	// documented grace periods (task DF2); nil means none is known.
	Payments payments.Store
	Grace    payments.GraceFunc
	// Matches stores payment matches between bank transactions and
	// occurrences (ADR-0035).
	Matches matches.Store
	// Merchants stores merchant enrichments and decisions (ADR-0051).
	Merchants merchants.Store
	// MerchantSources describes the merchant-identification sources (ADR-0054).
	MerchantSources []MerchantSourceInfo
	// Organisation stores categories, labels, buckets and their assignments
	// (ADR-0053).
	Organisation organisation.Store
	// Deferrals stores deferral plans (DF1, ADR-0050); nil leaves the
	// monthly view without plans (the plan routes are registered regardless,
	// like the other stores').
	Deferrals deferrals.Store
	// Banking is the bank connection service (ADR-0040); nil in tests that
	// never call the routes.
	Banking *bankconnect.Service
	// Matching runs the automatic payment matching (ADR-0039).
	Matching MatchingRunner
	// Audit reads the audit log.
	Audit audit.Reader
	// Auth is sign-in, sessions and passwords.
	Auth *auth.Service
	// Tokens is API tokens.
	Tokens *tokens.Service
	// OIDC is single sign-on (ADR-0033); nil when it is not configured.
	OIDC *OIDCDeps
	// MCP serves POST /mcp (internal/mcpserver); nil means MCP is turned off.
	MCP http.Handler
	// Banks reads stored bank facts for the outlook (ADR-0044); nil means no
	// balance is available. Rates converts for the outlook's secondary
	// aggregate; nil means only the reporting currency itself converts.
	// ReportingCurrency is that aggregate's default currency (zero: none
	// unless a request asks). BankStaleAfter is the bank-data staleness
	// threshold (zero: banking.DefaultStaleAfter).
	Banks outlook.Banks
	Rates outlook.Converter
	// RateCache serves the exchange-rate routes (FX2c, ADR-0048); nil answers
	// them with 503.
	RateCache *RatesDeps
	// ConvertTimeout bounds the outlook's conversion phase (see
	// [outlook.Service.ConvertTimeout]); zero means unbounded.
	ConvertTimeout    time.Duration
	ReportingCurrency money.Currency
	BankStaleAfter    time.Duration
	// Zone is the reporting time zone (D5): it decides "today" for default
	// effective dates and the materialisation horizon. Now defaults to
	// time.Now.
	Zone calendar.Zone
	Now  func() time.Time
}

// RegisterAPI registers every route of the Management API under [APIPrefix].
// ledger serve calls it, and TestOpenAPIContract compares what it registers
// with openapi.yaml, so every /api/v1 route must be registered here.
//
// Every route declares its access (ADR-0021) and openapi.yaml documents it as
// x-ledger-access. The API description is public: it is the same document as
// in the source repository.
func RegisterAPI(s *Server, d Deps) {
	s.HandleAPI("GET "+APIPrefix+"/openapi.json", Public(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		doc, err := OpenAPIJSON()
		if err != nil {
			WriteError(w, r, Internal(err))
			return
		}
		writeDoc(w, "application/json", doc)
	}))
	s.HandleAPI("GET "+APIPrefix+"/openapi.yaml", Public(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeDoc(w, "application/yaml", OpenAPIYAML())
	}))
	registerAuthz(s)
	registerUsers(s, d.Users)
	registerUserPassword(s, d.Auth)
	registerSessions(s, d.Auth)
	registerTwoFactor(s, d.Auth)
	registerOIDC(s, d.Auth, d.OIDC)
	registerAudit(s, d.Audit)
	registerTokens(s, d.Tokens)
	todayFn := newToday(d.Zone, d.Now)
	registerCommitments(s, d.Commitments, todayFn)
	var plans deferrals.Reader
	if d.Deferrals != nil {
		plans = d.Deferrals
	}
	registerOccurrences(s, d.Occurrences, d.Payments, d.Commitments, plans, todayFn, d.Grace, d.Now, d.Banks, d.BankStaleAfter)
	registerPayments(s, d.Occurrences, d.Payments, todayFn, d.Grace)
	registerMatches(s, d.Matches)
	registerMerchants(s, d.Merchants)
	registerMerchantRecords(s, d.Merchants)
	registerMerchantSources(s, d.MerchantSources)
	registerOrganisation(s, d.Organisation)
	registerDeferrals(s, d.Deferrals, d.Occurrences, d.Payments, todayFn, d.Grace)
	registerBankConnections(s, d.Banking)
	registerMatching(s, d.Matching)
	registerRates(s, d.RateCache, d.Now)
	registerOutlook(s, outlook.Service{
		Months:      monthview.Service{Occurrences: d.Occurrences, Payments: d.Payments, Grace: d.Grace},
		Commitments: d.Commitments, Banks: d.Banks, Rates: d.Rates, ConvertTimeout: d.ConvertTimeout, Today: todayFn, Now: d.Now, BankStaleAfter: d.BankStaleAfter, Zone: d.Zone,
	}, d.ReportingCurrency, d.Now)
	registerMCP(s, d.MCP)
}

func writeDoc(w http.ResponseWriter, contentType string, doc []byte) {
	w.Header().Set("Content-Type", contentType+"; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(doc)
}

var httpMethods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

var pathParam = regexp.MustCompile(`\{([^}]*)\}`)

// CheckContract compares an OpenAPI document with registered routes. It
// returns one line per problem: a route the document lacks, a documented
// operation with no route, an unresolvable $ref, a missing or duplicate
// operationId, an undeclared path parameter, an operation without the
// shared error response, or an x-ledger-access that differs from the route's
// access rule (ADR-0021). Routes must lie under the document's first server URL.
func CheckContract(doc []byte, routes []Route) []string {
	var problems []string
	var root map[string]any
	if err := yaml.Unmarshal(doc, &root); err != nil {
		return []string{"document does not parse: " + err.Error()}
	}
	if _, err := yamlToJSON(doc); err != nil {
		problems = append(problems, err.Error())
	}
	if v, _ := root["openapi"].(string); !strings.HasPrefix(v, "3.") {
		problems = append(problems, fmt.Sprintf("openapi version %q is not 3.x", v))
	}
	base := ""
	if servers, _ := root["servers"].([]any); len(servers) > 0 {
		if s, _ := servers[0].(map[string]any); s != nil {
			base, _ = s["url"].(string)
		}
	}
	if base == "" {
		problems = append(problems, "servers[0].url is missing")
	}

	// $ref targets.
	walk(root, func(ref string) {
		if _, ok := resolve(root, ref); !ok {
			problems = append(problems, "unresolved $ref "+ref)
		}
	})

	documented := map[string]bool{}
	access := map[string]string{}
	recent := map[string]bool{}
	enrollment := map[string]bool{}
	opIDs := map[string]string{}
	paths, _ := root["paths"].(map[string]any)
	for p, item := range paths {
		pi, _ := item.(map[string]any)
		for _, m := range httpMethods {
			op, ok := pi[m].(map[string]any)
			if !ok {
				continue
			}
			key := strings.ToUpper(m) + " " + base + p
			documented[key] = true
			access[key], _ = op["x-ledger-access"].(string)
			recent[key], _ = op["x-ledger-recent-auth"].(bool)
			enrollment[key], _ = op["x-ledger-during-enrollment"].(bool)
			id, _ := op["operationId"].(string)
			switch {
			case id == "":
				problems = append(problems, key+": no operationId")
			case opIDs[id] != "":
				problems = append(problems, fmt.Sprintf("%s: operationId %q is also used by %s", key, id, opIDs[id]))
			default:
				opIDs[id] = key
			}
			declared := map[string]bool{}
			for _, list := range []any{pi["parameters"], op["parameters"]} {
				params, _ := list.([]any)
				for _, prm := range params {
					pm, _ := prm.(map[string]any)
					if ref, ok := pm["$ref"].(string); ok {
						r, _ := resolve(root, ref)
						pm, _ = r.(map[string]any)
					}
					if in, _ := pm["in"].(string); in == "path" {
						name, _ := pm["name"].(string)
						declared[name] = true
					}
				}
			}
			for _, sm := range pathParam.FindAllStringSubmatch(p, -1) {
				if !declared[sm[1]] {
					problems = append(problems, fmt.Sprintf("%s: path parameter %q is not declared", key, sm[1]))
				}
			}
			resp, _ := op["responses"].(map[string]any)
			def, _ := resp["default"].(map[string]any)
			if ref, _ := def["$ref"].(string); ref != "#/components/responses/Error" {
				problems = append(problems, key+": default response is not #/components/responses/Error")
			}
		}
	}

	registered := map[string]bool{}
	for _, r := range routes {
		if strings.Contains(r.Path, "...}") || strings.HasSuffix(r.Path, "{$}") || strings.HasSuffix(r.Path, "/") {
			problems = append(problems, r.String()+": API routes must not use {name...} or {$} wildcards or end in / (a trailing slash matches every deeper path)")
		}
		registered[r.String()] = true
		if !documented[r.String()] {
			problems = append(problems, r.String()+": registered but not documented in the OpenAPI document")
		} else if got, want := access[r.String()], r.Access.String(); got != want {
			problems = append(problems, fmt.Sprintf("%s: x-ledger-access is %q, the route requires %q", r, got, want))
		} else if recent[r.String()] != r.Access.RecentAuth() {
			problems = append(problems, fmt.Sprintf("%s: x-ledger-recent-auth is %v, the route's is %v", r, recent[r.String()], r.Access.RecentAuth()))
		} else if enrollment[r.String()] != r.Access.AllowedDuringEnrollment() {
			problems = append(problems, fmt.Sprintf("%s: x-ledger-during-enrollment is %v, the route's is %v", r, enrollment[r.String()], r.Access.AllowedDuringEnrollment()))
		}
	}
	for key := range documented {
		if !registered[key] {
			problems = append(problems, key+": documented in the OpenAPI document but not registered")
		}
	}
	slices.Sort(problems)
	return problems
}

// walk calls fn for every $ref value in v.
func walk(v any, fn func(string)) {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			if s, ok := x.(string); ok && k == "$ref" {
				fn(s)
				continue
			}
			walk(x, fn)
		}
	case []any:
		for _, x := range t {
			walk(x, fn)
		}
	}
}

// resolve follows a local JSON pointer ("#/a/b").
func resolve(root map[string]any, ref string) (any, bool) {
	rest, ok := strings.CutPrefix(ref, "#/")
	if !ok {
		return nil, false
	}
	var cur any = root
	for _, part := range strings.Split(rest, "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[part]; !ok {
			return nil, false
		}
	}
	return cur, true
}
