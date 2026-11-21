package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/bankconnect"
	"github.com/aurimasniekis/go-ledger/internal/domain/bankimport"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
)

// Bank connection routes (task B4, ADR-0040).
const (
	BankApplicationsPath = APIPrefix + "/bank-applications"
	BankConnectionsPath  = APIPrefix + "/bank-connections"
)

var (
	grantConnectionsRead  = authz.G(authz.ResourceConnections, authz.ActionRead)
	grantConnectionsWrite = authz.G(authz.ResourceConnections, authz.ActionWrite)
)

// Error codes of bank connections. A client tells the user what to do from the
// code: reconnect (bank_reconnect_required, connection_not_active), wait and
// retry (bank_unavailable, bank_rate_limited, refresh_in_progress), or fix the
// request (bank_request_rejected).
const (
	CodeBankReconnectRequired Code = "bank_reconnect_required"  // 409: consent expired or revoked; only a new consent helps, retrying cannot
	CodeBankUnavailable       Code = "bank_unavailable"         // 503: the provider or bank failed for now; retrying later may work
	CodeBankRateLimited       Code = "bank_rate_limited"        // 429: the provider or bank asked for a pause; Retry-After says how long when known
	CodeBankRejected          Code = "bank_request_rejected"    // 422: the provider refused the request; retrying the same request cannot work
	CodeConnectionNotActive   Code = "connection_not_active"    // 409: the connection is pending, disconnected, failed or needs a new consent
	CodeRefreshInProgress     Code = "refresh_in_progress"      // 409: a refresh of this connection is already running
	CodeConsentUnknown        Code = "consent_not_found"        // 409: the redirect matches no consent in progress (already used or replaced)
	CodeConnectUnavailable    Code = "bank_connect_unavailable" // 409: banking.redirect_url is not configured
)

type bankCapabilitiesJSON struct {
	BalanceTypes            []banking.BalanceType `json:"balance_types"`
	PendingTransactions     bool                  `json:"pending_transactions"`
	MaxConsentValidityDays  int                   `json:"max_consent_validity_days"`
	MaxHistoryDays          int                   `json:"max_history_days"`
	BackgroundFetchesPerDay int                   `json:"background_fetches_per_day"`
}

type bankApplicationJSON struct {
	Provider      string                `json:"provider"`
	Name          string                `json:"name"`
	Mode          banking.Mode          `json:"mode"`
	Environment   banking.Environment   `json:"environment"`
	AccountAccess banking.AccountAccess `json:"account_access"`
	Capabilities  bankCapabilitiesJSON  `json:"capabilities"`
}

type bankInstitutionJSON struct {
	ID                     string `json:"id"`
	Name                   string `json:"name"`
	Country                string `json:"country"`
	MaxConsentValidityDays int    `json:"max_consent_validity_days"`
}

type bankErrorJSON struct {
	Kind banking.ErrorKind `json:"kind"`
	Code string            `json:"code"`
}

type bankFreshnessJSON struct {
	State         banking.FreshnessState `json:"state"`
	Stale         bool                   `json:"stale"`
	LastSuccessAt *time.Time             `json:"last_success_at"`
	LastAttemptAt *time.Time             `json:"last_attempt_at"`
	LastError     *bankErrorJSON         `json:"last_error"`
	NoticeCode    *string                `json:"notice_code"`
}

type bankBalanceJSON struct {
	Type          banking.BalanceType `json:"type"`
	Amount        money.Money         `json:"amount"`
	ReferenceDate *string             `json:"reference_date"`
	ObtainedAt    time.Time           `json:"obtained_at"`
}

type bankAccountJSON struct {
	ID                  banking.AccountID `json:"id"`
	Name                string            `json:"name"`
	Product             string            `json:"product"`
	Currency            string            `json:"currency"`
	IDHint              *string           `json:"identifier_hint"`
	NoLongerReported    bool              `json:"no_longer_reported"`
	TransactionsThrough *string           `json:"transactions_through"`
	Freshness           bankFreshnessJSON `json:"freshness"`
	Balances            []bankBalanceJSON `json:"balances"`
}

type bankRunJSON struct {
	ID         string                 `json:"id"`
	StartedAt  time.Time              `json:"started_at"`
	FinishedAt *time.Time             `json:"finished_at"`
	Outcome    banking.RefreshOutcome `json:"outcome"`
	Error      *bankErrorJSON         `json:"error"`
	// Counts are what the run stored; none is a bank payload.
	AccountsFailed       int `json:"accounts_failed"`
	TransactionsInserted int `json:"transactions_inserted"`
	TransactionsRejected int `json:"transactions_rejected"`
}

type bankScheduleJSON struct {
	State               bankimport.ScheduleState `json:"state"`
	NextAttemptAt       *time.Time               `json:"next_attempt_at"`
	ConsecutiveFailures int                      `json:"consecutive_failures"`
}

// bankConnectionJSON is a connection with its accounts. Status is what the
// connection is; freshness is how current its data is. needs_reconnect is true
// only when a new consent is the way forward, which is never the case for a
// retryable failure.
type bankConnectionJSON struct {
	ID                 banking.ConnectionID     `json:"id"`
	Provider           string                   `json:"provider"`
	Application        string                   `json:"application"`
	Mode               banking.Mode             `json:"mode"`
	Environment        banking.Environment      `json:"environment"`
	AccountAccess      banking.AccountAccess    `json:"account_access"`
	Institution        bankInstitutionJSON      `json:"institution"`
	Status             banking.ConnectionStatus `json:"status"`
	NeedsReconnect     bool                     `json:"needs_reconnect"`
	Freshness          banking.FreshnessState   `json:"freshness"`
	ConsentGrantedAt   *time.Time               `json:"consent_granted_at"`
	ConsentValidUntil  *time.Time               `json:"consent_valid_until"`
	ConsentExpiresSoon bool                     `json:"consent_expires_soon"`
	LastError          *bankErrorJSON           `json:"last_error"`
	Accounts           []bankAccountJSON        `json:"accounts"`
	LastRun            *bankRunJSON             `json:"last_refresh_run"`
	Schedule           *bankScheduleJSON        `json:"schedule"`
	AsOf               time.Time                `json:"as_of"`
	Version            int64                    `json:"version"`
	CreatedAt          time.Time                `json:"created_at"`
	UpdatedAt          time.Time                `json:"updated_at"`
	// PreviousSessionEndFailed is only ever set by completeBankConnection:
	// a reconnect replaced a provider session that could not be ended.
	PreviousSessionEndFailed bool `json:"previous_session_end_failed,omitempty"`
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	t = t.UTC()
	return &t
}

func bankErr(kind banking.ErrorKind, code string) *bankErrorJSON {
	if kind == "" {
		return nil
	}
	return &bankErrorJSON{Kind: kind, Code: code}
}

func days(d time.Duration) int { return int(d / (24 * time.Hour)) }

// freshnessRank orders states from worst to best for the connection-level
// summary.
var freshnessRank = map[banking.FreshnessState]int{
	banking.FreshnessReconnectRequired: 0, banking.FreshnessDisconnected: 1, banking.FreshnessFailed: 2,
	banking.FreshnessStale: 3, banking.FreshnessNeverRefreshed: 4, banking.FreshnessFresh: 5,
}

func toBankConnectionJSON(v bankconnect.View) bankConnectionJSON {
	c := v.Connection
	out := bankConnectionJSON{ID: c.ID, Provider: c.Provider, Application: c.Application, Mode: c.Mode, Environment: c.Environment, AccountAccess: c.AccountAccess,
		Institution: bankInstitutionJSON{ID: c.Institution.ID, Name: c.Institution.Name, Country: c.Institution.Country},
		Status:      c.Status, NeedsReconnect: c.Status == banking.ConnectionReconnectRequired,
		ConsentGrantedAt: timePtr(c.ConsentGrantedAt), ConsentValidUntil: timePtr(c.ConsentValidUntil),
		LastError: bankErr(c.LastErrorKind, c.LastErrorCode), Accounts: []bankAccountJSON{},
		AsOf: v.AsOf, Version: c.Version, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
	if c.Status == banking.ConnectionActive && !c.ConsentValidUntil.IsZero() && c.ConsentValidUntil.Sub(v.AsOf) <= bankconnect.ConsentWarnDays*24*time.Hour {
		out.ConsentExpiresSoon = true
	}
	switch c.Status {
	case banking.ConnectionReconnectRequired:
		out.Freshness = banking.FreshnessReconnectRequired
	case banking.ConnectionActive:
		out.Freshness = banking.FreshnessNeverRefreshed
	case banking.ConnectionFailed:
		out.Freshness = banking.FreshnessFailed
	default:
		out.Freshness = banking.FreshnessDisconnected
	}
	if c.Status == banking.ConnectionActive && len(v.Accounts) > 0 {
		out.Freshness = banking.FreshnessFresh
	}
	for _, a := range v.Accounts {
		aj := bankAccountJSON{ID: a.Account.ID, Name: a.Account.Name, Product: a.Account.Product, Currency: a.Account.Currency.String(), NoLongerReported: a.NoLonger,
			Balances: []bankBalanceJSON{},
			Freshness: bankFreshnessJSON{State: a.Freshness.State, Stale: a.Freshness.Stale, LastSuccessAt: timePtr(a.Freshness.LastSuccessAt), LastAttemptAt: timePtr(a.Freshness.LastAttemptAt),
				LastError: bankErr(a.Freshness.LastErrorKind, a.Freshness.LastErrorCode)}}
		if a.Freshness.NoticeCode != "" {
			n := a.Freshness.NoticeCode
			aj.Freshness.NoticeCode = &n
		}
		if a.IDHint != "" {
			h := a.IDHint
			aj.IDHint = &h
		}
		if !a.Sync.TransactionsThrough.IsZero() {
			s := a.Sync.TransactionsThrough.String()
			aj.TransactionsThrough = &s
		}
		for _, b := range a.Balances {
			bj := bankBalanceJSON{Type: b.Type, Amount: b.Amount, ObtainedAt: b.ObtainedAt}
			if b.ReferenceDate != "" {
				d := b.ReferenceDate
				bj.ReferenceDate = &d
			}
			aj.Balances = append(aj.Balances, bj)
		}
		if !a.NoLonger && c.Status == banking.ConnectionActive && freshnessRank[a.Freshness.State] < freshnessRank[out.Freshness] {
			out.Freshness = a.Freshness.State
		}
		out.Accounts = append(out.Accounts, aj)
	}
	if r := v.LastRun; r != nil {
		out.LastRun = &bankRunJSON{ID: string(r.ID), StartedAt: r.StartedAt.UTC(), FinishedAt: timePtr(r.FinishedAt), Outcome: r.Outcome, Error: bankErr(r.ErrorKind, r.ErrorCode),
			AccountsFailed: r.AccountsFailed, TransactionsInserted: r.TransactionsInserted, TransactionsRejected: r.TransactionsRejected}
	}
	if s := v.Schedule; s != nil {
		out.Schedule = &bankScheduleJSON{State: s.Schedule, NextAttemptAt: timePtr(s.NextAttemptAt), ConsecutiveFailures: s.ConsecutiveFailures}
	}
	return out
}

// bankError maps the connection service's and the provider's errors to API
// errors. Provider failures keep their classification in the code, so a
// client can show "reconnect" for one and "try again later" for another.
func bankError(w http.ResponseWriter, err error) error {
	retryAfter := func(d time.Duration) {
		if d > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int((d+time.Second-1)/time.Second)))
		}
	}
	conflict := func(code Code, msg string) error {
		return &Error{Status: http.StatusConflict, Code: code, Message: msg, Cause: err}
	}
	var pe *banking.Error
	switch {
	case errors.Is(err, bankconnect.ErrUnknownApplication):
		return NotFound("no such provider application is configured")
	case errors.Is(err, bankconnect.ErrUnknownInstitution):
		return Validation("institution_id", "the provider does not offer this institution in that country")
	case errors.Is(err, bankconnect.ErrUnknownConsent):
		return conflict(CodeConsentUnknown, "no consent is waiting for this redirect: it was already used, replaced by a newer attempt, or never started here. Start the connection again")
	case errors.Is(err, bankconnect.ErrConsentExpired):
		return conflict(CodeConsentUnknown, "this consent was not completed in time and has expired. Start the connection again")
	case errors.Is(err, bankconnect.ErrWrongInitiator):
		return conflict(CodeConsentUnknown, "this consent was started by another user and cannot be completed here")
	case errors.Is(err, bankconnect.ErrNoRedirect):
		return conflict(CodeConnectUnavailable, "connecting a bank is not set up: banking.redirect_url is not configured")
	case errors.Is(err, bankconnect.ErrPending):
		return conflict(CodeConnectionNotActive, "the connection's consent has not completed yet")
	case errors.Is(err, bankconnect.ErrDisconnected):
		return conflict(CodeConnectionNotActive, "the connection was disconnected; reconnect it to fetch data again")
	case errors.Is(err, bankconnect.ErrNotActive):
		return conflict(CodeConnectionNotActive, "the connection cannot be refreshed until its consent is renewed: reconnect it")
	case errors.Is(err, banking.ErrRefreshInProgress):
		retryAfter(30 * time.Second)
		return conflict(CodeRefreshInProgress, "a refresh of this connection is already running; try again shortly")
	case errors.Is(err, banking.ErrNotFound):
		return NotFound("no such bank connection")
	case errors.As(err, &pe):
		code := ""
		if pe.Code != "" {
			code = " (" + pe.Code + ")"
		}
		switch pe.Kind {
		case banking.KindReconnectRequired:
			return conflict(CodeBankReconnectRequired, "the bank consent has ended or was withdrawn"+code+"; reconnect the bank. Retrying will not help")
		case banking.KindRateLimited:
			retryAfter(pe.RetryAfter)
			return &Error{Status: http.StatusTooManyRequests, Code: CodeBankRateLimited, Message: "the bank or provider asked for a pause" + code + "; try again later", Cause: err}
		case banking.KindRetryable, banking.KindCanceled:
			retryAfter(pe.RetryAfter)
			return &Error{Status: http.StatusServiceUnavailable, Code: CodeBankUnavailable, Message: "the bank or provider is not available right now" + code + "; try again later. Nothing was lost", Cause: err}
		default:
			return &Error{Status: http.StatusUnprocessableEntity, Code: CodeBankRejected, Message: "the provider refused the request" + code + "; trying again unchanged will not help", Cause: err}
		}
	}
	return err
}

// decodeBankBody reads an optional JSON object body, refusing members other
// than allowed.
func decodeBankBody(r *http.Request, allowed ...string) (map[string]json.RawMessage, error) {
	doc, err := decodeObject(r)
	if err != nil {
		return nil, err
	}
	for _, k := range slices.Sorted(maps.Keys(doc)) {
		if !slices.Contains(allowed, k) {
			return nil, Validation(k, "unknown field "+k)
		}
	}
	return doc, nil
}

func bodyString(doc map[string]json.RawMessage, key string, required bool, max int) (string, error) {
	raw, ok := doc[key]
	if !ok || isJSONNull(raw) {
		if required {
			return "", Validation(key, key+" is required")
		}
		return "", nil
	}
	var s string
	if json.Unmarshal(raw, &s) != nil || (required && strings.TrimSpace(s) == "") || len(s) > max {
		return "", Validation(key, fmt.Sprintf("%s must be text of at most %d characters", key, max))
	}
	return s, nil
}

func bodyDays(doc map[string]json.RawMessage, key string) (time.Duration, error) {
	raw, ok := doc[key]
	if !ok || isJSONNull(raw) {
		return 0, nil
	}
	var n int
	if json.Unmarshal(raw, &n) != nil || n < 1 || n > 3650 {
		return 0, Validation(key, key+" must be a whole number of days from 1 to 3650")
	}
	return time.Duration(n) * 24 * time.Hour, nil
}

// registerBankConnections registers the bank connection routes (task B4,
// ADR-0040). Reading needs connections:read and everything that changes
// something connections:write; both are administrator grants. Nothing here
// returns a provider session, authorisation code, account handle or full
// account identifier.
func registerBankConnections(s *Server, svc *bankconnect.Service) {
	item := BankConnectionsPath + "/{id}"
	startedJSON := func(st bankconnect.Started, view bankconnect.View) map[string]any {
		return map[string]any{"connection": toBankConnectionJSON(view), "authorization_url": st.AuthorizationURL}
	}
	connID := func(r *http.Request) (banking.ConnectionID, error) {
		id := r.PathValue("id")
		if !resource.ValidID(id) {
			return "", NotFound("no such bank connection")
		}
		return banking.ConnectionID(id), nil
	}
	write := func(w http.ResponseWriter, status int, v bankconnect.View) {
		SetVersion(w, v.Connection.Version)
		WriteJSON(w, status, toBankConnectionJSON(v))
	}

	s.HandleAPI("GET "+BankApplicationsPath, Requires(grantConnectionsRead), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		apps := svc.Applications()
		out := make([]bankApplicationJSON, len(apps))
		for i, a := range apps {
			c := a.Capabilities
			out[i] = bankApplicationJSON{Provider: a.Provider, Name: a.Name, Mode: a.Mode, Environment: a.Environment, AccountAccess: a.AccountAccess,
				Capabilities: bankCapabilitiesJSON{BalanceTypes: append([]banking.BalanceType{}, c.BalanceTypes...), PendingTransactions: c.PendingTransactions,
					MaxConsentValidityDays: days(c.MaxConsentValidity), MaxHistoryDays: c.MaxHistoryDays, BackgroundFetchesPerDay: c.BackgroundFetchesPerDay}}
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}))

	s.HandleAPI("GET "+BankApplicationsPath+"/{application}/institutions", Requires(grantConnectionsRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		country := r.URL.Query().Get("country")
		if len(country) != 2 || strings.ToUpper(country) != country || strings.Trim(country, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
			WriteError(w, r, Validation("country", "country must be an ISO 3166-1 alpha-2 code in capitals, such as DE"))
			return
		}
		insts, err := svc.Institutions(r.Context(), r.PathValue("application"), country)
		if err != nil {
			WriteError(w, r, bankError(w, err))
			return
		}
		out := make([]bankInstitutionJSON, len(insts))
		for i, in := range insts {
			out[i] = bankInstitutionJSON{ID: in.ID, Name: in.Name, Country: in.Country, MaxConsentValidityDays: days(in.MaxConsentValidity)}
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}))

	s.HandleAPI("GET "+BankConnectionsPath, Requires(grantConnectionsRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, err := ParseList(r, ListSpec{})
		if err != nil {
			WriteError(w, r, err)
			return
		}
		views, err := svc.List(r.Context())
		if err != nil {
			WriteError(w, r, bankError(w, err))
			return
		}
		slices.SortFunc(views, func(a, b bankconnect.View) int {
			return strings.Compare(string(a.Connection.ID), string(b.Connection.ID))
		})
		out := make([]bankConnectionJSON, 0, len(views))
		for _, v := range views {
			if q.After != "" && string(v.Connection.ID) <= q.After {
				continue
			}
			out = append(out, toBankConnectionJSON(v))
			if len(out) > q.Limit {
				break
			}
		}
		WriteJSON(w, http.StatusOK, NewPage(out, q, func(c bankConnectionJSON) string { return string(c.ID) }))
	}))

	s.HandleAPI("POST "+BankConnectionsPath, Requires(grantConnectionsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		doc, err := decodeBankBody(r, "application", "institution_id", "country", "psu_type", "valid_for_days")
		if err != nil {
			WriteError(w, r, err)
			return
		}
		var st bankconnect.Start
		if st.Application, err = bodyString(doc, "application", true, 64); err == nil {
			if st.InstitutionID, err = bodyString(doc, "institution_id", true, 256); err == nil {
				if st.Country, err = bodyString(doc, "country", true, 2); err == nil && (len(st.Country) != 2 || strings.ToUpper(st.Country) != st.Country) {
					err = Validation("country", "country must be an ISO 3166-1 alpha-2 code in capitals, such as DE")
				}
			}
		}
		if err == nil {
			var psu string
			if psu, err = bodyString(doc, "psu_type", false, 16); err == nil {
				switch banking.PSUType(psu) {
				case "", banking.PSUPersonal, banking.PSUBusiness:
					st.PSUType = banking.PSUType(psu)
				default:
					err = Validation("psu_type", "psu_type must be personal or business")
				}
			}
		}
		if err == nil {
			st.ValidFor, err = bodyDays(doc, "valid_for_days")
		}
		if err != nil {
			WriteError(w, r, err)
			return
		}
		started, err := svc.Start(r.Context(), actor(r), st)
		if err != nil {
			WriteError(w, r, bankError(w, err))
			return
		}
		view, err := svc.View(r.Context(), started.Connection.ID)
		if err != nil {
			WriteError(w, r, bankError(w, err))
			return
		}
		w.Header().Set("Location", BankConnectionsPath+"/"+string(view.Connection.ID))
		SetVersion(w, view.Connection.Version)
		WriteJSON(w, http.StatusCreated, startedJSON(started, view))
	}))

	// The bank's redirect lands on the UI's callback page, which sends what it
	// carries here with the signed-in session. The state works once.
	s.HandleAPI("POST "+BankConnectionsPath+"/complete", Requires(grantConnectionsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		doc, err := decodeBankBody(r, "state", "code", "error")
		if err != nil {
			WriteError(w, r, err)
			return
		}
		var rd bankconnect.Redirect
		if rd.State, err = bodyString(doc, "state", true, 256); err != nil {
			WriteError(w, r, err)
			return
		}
		code, err := bodyString(doc, "code", false, 4096)
		if err == nil {
			rd.Error, err = bodyString(doc, "error", false, 256)
		}
		if err == nil && code == "" && rd.Error == "" {
			err = Validation("code", "code is required unless error is given")
		}
		if err != nil {
			WriteError(w, r, err)
			return
		}
		rd.Code = banking.NewSensitive(code)
		done, err := svc.Complete(r.Context(), actor(r), rd)
		if err != nil {
			WriteError(w, r, bankError(w, err))
			return
		}
		view, err := svc.View(r.Context(), done.Connection.ID)
		if err != nil {
			WriteError(w, r, bankError(w, err))
			return
		}
		out := toBankConnectionJSON(view)
		out.PreviousSessionEndFailed = done.PreviousSessionEndFailed
		SetVersion(w, view.Connection.Version)
		WriteJSON(w, http.StatusOK, out)
	}))

	s.HandleAPI("GET "+item, Requires(grantConnectionsRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := connID(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		v, err := svc.View(r.Context(), id)
		if err != nil {
			WriteError(w, r, bankError(w, err))
			return
		}
		write(w, http.StatusOK, v)
	}))

	s.HandleAPI("POST "+item+"/reconnect", Requires(grantConnectionsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := connID(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		doc, err := decodeBankBody(r, "valid_for_days")
		if err != nil {
			WriteError(w, r, err)
			return
		}
		valid, err := bodyDays(doc, "valid_for_days")
		if err != nil {
			WriteError(w, r, err)
			return
		}
		started, err := svc.Reconnect(r.Context(), actor(r), id, valid)
		if err != nil {
			WriteError(w, r, bankError(w, err))
			return
		}
		view, err := svc.View(r.Context(), id)
		if err != nil {
			WriteError(w, r, bankError(w, err))
			return
		}
		SetVersion(w, view.Connection.Version)
		WriteJSON(w, http.StatusOK, startedJSON(started, view))
	}))

	s.HandleAPI("POST "+item+"/refresh", Requires(grantConnectionsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := connID(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		if _, err := decodeBankBody(r); err != nil {
			WriteError(w, r, err)
			return
		}
		v, err := svc.Refresh(r.Context(), actor(r), id)
		if err != nil {
			WriteError(w, r, bankError(w, err))
			return
		}
		write(w, http.StatusOK, v)
	}))

	s.HandleAPI("POST "+item+"/disconnect", Requires(grantConnectionsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := connID(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		doc, err := decodeBankBody(r, "version")
		if err != nil {
			WriteError(w, r, err)
			return
		}
		var bodyVersion *int64
		if raw, ok := doc["version"]; ok && !isJSONNull(raw) {
			var n int64
			if json.Unmarshal(raw, &n) != nil || n < 1 {
				WriteError(w, r, Validation("version", "version must be a positive integer"))
				return
			}
			bodyVersion = &n
		}
		expected, err := ExpectedVersion(r, bodyVersion)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		if _, err := svc.Disconnect(r.Context(), actor(r), id, expected); err != nil {
			var vc *resource.VersionConflictError
			if errors.As(err, &vc) {
				if cur, gerr := svc.View(r.Context(), id); gerr == nil {
					WriteError(w, r, VersionConflict(cur.Connection.Version, toBankConnectionJSON(cur)))
					return
				}
			}
			WriteError(w, r, bankError(w, err))
			return
		}
		v, err := svc.View(r.Context(), id)
		if err != nil {
			WriteError(w, r, bankError(w, err))
			return
		}
		write(w, http.StatusOK, v)
	}))
}
