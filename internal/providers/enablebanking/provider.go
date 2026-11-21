package enablebanking

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
)

// Provider is the adapter for one Enable Banking application. It is safe for
// concurrent use. Use [New].
type Provider struct {
	cfg    Config
	caps   banking.Capabilities
	client client
	now    func() time.Time

	mu      sync.Mutex
	details map[string]banking.Account // per-session account details, see accountDetails
}

var _ banking.Provider = (*Provider)(nil)

// maxCachedDetails bounds the per-session account-details cache.
const maxCachedDetails = 1024

// New returns the adapter for cfg. It makes no network call; [Provider.Verify]
// checks the application against Enable Banking.
func New(cfg Config) (*Provider, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" {
		return nil, errBaseURL
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = NewHTTPClient()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.TokenTTL == 0 {
		cfg.TokenTTL = DefaultTokenTTL
	}
	if cfg.MaxResponseBytes <= 0 {
		cfg.MaxResponseBytes = defaultMaxResponseBytes
	}
	p := &Provider{
		cfg: cfg,
		now: cfg.Now,
		caps: banking.Capabilities{
			Provider:    ProviderName,
			Application: cfg.Application,
			Mode:        cfg.Mode,
			Environment: cfg.Environment,
			BalanceTypes: []banking.BalanceType{
				banking.BalanceBooked, banking.BalanceAvailable, banking.BalanceExpected, banking.BalanceOther,
			},
			PendingTransactions: true,
			// entry_reference is documented as unique and immutable per
			// account identification hash (R1 section 6); ADR-0013 keys
			// tolerate banks that omit or repeat it.
			StableEntryReferences: true,
			// Consent and history limits are per bank (Institution);
			// Enable Banking documents no provider-wide value.
			MaxConsentValidity: 0,
			MaxHistoryDays:     0,
			// Approximate and per bank; Enable Banking documents "about four"
			// [U: not a provider-wide or exact limit].
			BackgroundFetchesPerDay: 4,
		},
		details: map[string]banking.Account{},
	}
	p.client = client{
		base:     base,
		http:     cfg.HTTPClient,
		maxBytes: cfg.MaxResponseBytes,
		sign:     &signer{kid: cfg.AppID, key: cfg.PrivateKey, ttl: cfg.TokenTTL, now: cfg.Now},
	}
	return p, nil
}

var errBaseURL = errors.New("enablebanking: base URL must be an absolute http or https URL")

// Capabilities implements [banking.Provider].
func (p *Provider) Capabilities() banking.Capabilities {
	c := p.caps
	c.BalanceTypes = append([]banking.BalanceType(nil), c.BalanceTypes...)
	return c
}

// Verify checks the configured application against GET /application: the
// JWT must be accepted, the application id must match, its environment must
// be the configured one and it must be active. The mode cannot be checked:
// the documented response does not say whether a production application is
// restricted (research R1). Errors are permanent and carry a code only.
func (p *Provider) Verify(ctx context.Context) error {
	var app wireApplication
	if err := p.do(ctx, opApplication, http.MethodGet, "/application", nil, nil, &app); err != nil {
		return err
	}
	if !strings.EqualFold(app.Kid, p.cfg.AppID) {
		return p.newError(opApplication, banking.KindPermanent, banking.ReasonNone, codeApplicationMismatch, nil)
	}
	want := map[banking.Environment]string{banking.EnvironmentProduction: "PRODUCTION", banking.EnvironmentSandbox: "SANDBOX"}[p.cfg.Environment]
	if app.Environment != want {
		return p.newError(opApplication, banking.KindPermanent, banking.ReasonNone, codeEnvironmentMismatch, nil)
	}
	if !app.Active {
		return p.newError(opApplication, banking.KindPermanent, banking.ReasonNone, codeApplicationInactive, nil)
	}
	return nil
}

// Institutions implements [banking.Provider] with GET /aspsps.
func (p *Provider) Institutions(ctx context.Context, country string) ([]banking.Institution, error) {
	country = strings.ToUpper(strings.TrimSpace(country))
	if len(country) != 2 {
		return nil, p.newError(opInstitutions, banking.KindPermanent, banking.ReasonNone, "invalid_country", nil)
	}
	var res wireASPSPs
	if err := p.do(ctx, opInstitutions, http.MethodGet, "/aspsps", url.Values{"country": {country}}, nil, &res); err != nil {
		return nil, err
	}
	out := make([]banking.Institution, 0, len(res.ASPSPs))
	for _, a := range res.ASPSPs {
		inst := banking.Institution{ID: a.Name, Name: a.Name, Country: a.Country}
		if a.MaximumConsentValidity > 0 {
			inst.MaxConsentValidity = time.Duration(a.MaximumConsentValidity) * time.Second
		}
		out = append(out, inst)
	}
	return out, nil
}

// StartConsent implements [banking.Provider] with POST /auth. The requested
// validity is capped at the bank's maximum when known, because Enable
// Banking refuses a longer one; the session then expires exactly at the
// value sent (R1 section 4).
func (p *Provider) StartConsent(ctx context.Context, req banking.ConsentRequest) (banking.ConsentStart, error) {
	if req.Institution.ID == "" || req.State == "" || req.RedirectURL == "" || req.ValidUntil.IsZero() {
		return banking.ConsentStart{}, p.newError(opStartConsent, banking.KindPermanent, banking.ReasonNone, "invalid_request", nil)
	}
	psuType := req.PSUType
	if psuType == "" {
		psuType = banking.PSUPersonal
	}
	now := p.now().UTC()
	validUntil := req.ValidUntil.UTC()
	if m := req.Institution.MaxConsentValidity; m > 0 {
		if limit := now.Add(m - time.Minute); validUntil.After(limit) {
			validUntil = limit
		}
	}
	if !validUntil.After(now) {
		return banking.ConsentStart{}, p.newError(opStartConsent, banking.KindPermanent, banking.ReasonNone, "invalid_request", nil)
	}
	body := wireStartAuthorization{
		Access:      wireAccess{ValidUntil: validUntil.Format(time.RFC3339)},
		ASPSP:       wireASPSP{Name: req.Institution.ID, Country: req.Institution.Country},
		State:       req.State,
		RedirectURL: req.RedirectURL,
		PSUType:     string(psuType),
	}
	var res wireStartAuthorizationResponse
	if err := p.do(ctx, opStartConsent, http.MethodPost, "/auth", nil, body, &res); err != nil {
		return banking.ConsentStart{}, err
	}
	if res.URL == "" {
		return banking.ConsentStart{}, p.newError(opStartConsent, banking.KindPermanent, banking.ReasonNone, codeInvalidResponse, errInvalidResponse)
	}
	return banking.ConsentStart{AuthorizationURL: res.URL, Reference: res.AuthorizationID}, nil
}

// CompleteConsent implements [banking.Provider] with POST /sessions. Ledger
// checks the state against its own records before calling this; a redirect
// that carries an error is a refused consent and makes no call.
func (p *Provider) CompleteConsent(ctx context.Context, req banking.ConsentCompletion) (banking.Session, error) {
	if req.Error != "" {
		return banking.Session{}, p.newError(opCompleteConsent, banking.KindPermanent, banking.ReasonNone, codeConsentRefused, nil)
	}
	if req.Code.IsZero() {
		return banking.Session{}, p.newError(opCompleteConsent, banking.KindPermanent, banking.ReasonNone, "invalid_request", nil)
	}
	var res wireAuthorizeSessionResponse
	if err := p.do(ctx, opCompleteConsent, http.MethodPost, "/sessions", nil, wireAuthorizeSession{Code: req.Code.Reveal()}, &res); err != nil {
		return banking.Session{}, err
	}
	if res.SessionID == "" {
		return banking.Session{}, p.newError(opCompleteConsent, banking.KindPermanent, banking.ReasonNone, codeInvalidResponse, errInvalidResponse)
	}
	s := banking.Session{
		Reference:  banking.NewSensitive(res.SessionID),
		ValidUntil: parseTime(res.Access.ValidUntil),
		Accounts:   make([]banking.Account, 0, len(res.Accounts)),
	}
	for _, a := range res.Accounts {
		acc := mapAccount(a, "")
		p.remember(res.SessionID, a.UID, acc)
		s.Accounts = append(s.Accounts, acc)
	}
	return s, nil
}

// Accounts implements [banking.Provider]: GET /sessions/{id} for the
// session's status and accounts, then the details of each account (cached
// per session, since banks limit background fetches). In developer mode
// Enable Banking leaves unlinked accounts out, so an empty list is normal.
func (p *Provider) Accounts(ctx context.Context, session banking.Sensitive) ([]banking.Account, error) {
	if session.IsZero() {
		return nil, p.newError(opAccounts, banking.KindReconnectRequired, banking.ReasonSessionInvalid, "", nil)
	}
	var s wireSession
	if err := p.do(ctx, opAccounts, http.MethodGet, "/sessions/"+url.PathEscape(session.Reveal()), nil, nil, &s); err != nil {
		return nil, err
	}
	if err := p.sessionStatus(opAccounts, s.Status); err != nil {
		return nil, err
	}
	data := s.AccountsData
	if len(data) == 0 {
		for _, uid := range s.Accounts {
			data = append(data, wireSessionAccount{UID: uid})
		}
	}
	out := make([]banking.Account, 0, len(data))
	for _, sa := range data {
		if sa.UID == "" {
			continue
		}
		acc, err := p.accountDetails(ctx, session.Reveal(), sa)
		if err != nil {
			return nil, err
		}
		out = append(out, acc)
	}
	return out, nil
}

// sessionStatus maps a SessionStatus other than AUTHORIZED to a reconnect.
func (p *Provider) sessionStatus(op, status string) error {
	switch status {
	case "AUTHORIZED":
		return nil
	case "EXPIRED":
		return p.newError(op, banking.KindReconnectRequired, banking.ReasonConsentExpired, codeSessionStatusPrefix+"expired", nil)
	case "REVOKED":
		return p.newError(op, banking.KindReconnectRequired, banking.ReasonConsentRevoked, codeSessionStatusPrefix+"revoked", nil)
	case "":
		return p.newError(op, banking.KindPermanent, banking.ReasonNone, codeInvalidResponse, errInvalidResponse)
	}
	code := codeSessionStatusPrefix + strings.ToLower(sanitizeCode(status))
	return p.newError(op, banking.KindReconnectRequired, banking.ReasonSessionInvalid, code, nil)
}

func detailsKey(session, uid string) string {
	sum := sha256.Sum256([]byte(session + "\x00" + uid))
	return hex.EncodeToString(sum[:])
}

func (p *Provider) remember(session, uid string, acc banking.Account) {
	if uid == "" || acc.ExternalID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.details) >= maxCachedDetails {
		clear(p.details)
	}
	p.details[detailsKey(session, uid)] = acc
}

func (p *Provider) accountDetails(ctx context.Context, session string, sa wireSessionAccount) (banking.Account, error) {
	p.mu.Lock()
	acc, ok := p.details[detailsKey(session, sa.UID)]
	p.mu.Unlock()
	if ok {
		return acc, nil
	}
	var res wireAccountResource
	if err := p.do(ctx, opAccountDetails, http.MethodGet, "/accounts/"+url.PathEscape(sa.UID)+"/details", nil, nil, &res); err != nil {
		return banking.Account{}, err
	}
	if res.UID == "" {
		res.UID = sa.UID
	}
	acc = mapAccount(res, sa.IdentificationHash)
	p.remember(session, sa.UID, acc)
	return acc, nil
}

// Balances implements [banking.Provider] with GET /accounts/{uid}/balances.
func (p *Provider) Balances(ctx context.Context, session banking.Sensitive, account banking.Sensitive) ([]banking.Balance, error) {
	if account.IsZero() {
		return nil, p.newError(opBalances, banking.KindPermanent, banking.ReasonNone, "invalid_request", nil)
	}
	var res wireBalances
	if err := p.do(ctx, opBalances, http.MethodGet, "/accounts/"+url.PathEscape(account.Reveal())+"/balances", nil, nil, &res); err != nil {
		return nil, err
	}
	out := make([]banking.Balance, 0, len(res.Balances))
	for _, b := range res.Balances {
		out = append(out, mapBalance(b))
	}
	return out, nil
}

// Transactions implements [banking.Provider] with GET
// /accounts/{uid}/transactions. A zero From asks for strategy=longest (the
// earliest history the bank serves, R1 section 6); Enable Banking refuses a
// date_to without date_from, so To is then applied to the page here.
func (p *Provider) Transactions(ctx context.Context, session banking.Sensitive, account banking.Sensitive, q banking.TransactionQuery) (banking.TransactionPage, error) {
	if account.IsZero() {
		return banking.TransactionPage{}, p.newError(opTransactions, banking.KindPermanent, banking.ReasonNone, "invalid_request", nil)
	}
	v := url.Values{}
	if q.From.IsZero() {
		v.Set("strategy", "longest")
	} else {
		v.Set("date_from", q.From.String())
		if !q.To.IsZero() {
			v.Set("date_to", q.To.String())
		}
	}
	if q.Continuation != "" {
		v.Set("continuation_key", q.Continuation)
	}
	var res wireTransactions
	if err := p.do(ctx, opTransactions, http.MethodGet, "/accounts/"+url.PathEscape(account.Reveal())+"/transactions", v, nil, &res); err != nil {
		return banking.TransactionPage{}, err
	}
	page := banking.TransactionPage{Transactions: make([]banking.Transaction, 0, len(res.Transactions))}
	if res.ContinuationKey != nil {
		page.Next = *res.ContinuationKey
	}
	for _, t := range res.Transactions {
		tx := mapTransaction(t)
		if q.From.IsZero() && !q.To.IsZero() && tx.Date().After(q.To) {
			continue
		}
		page.Transactions = append(page.Transactions, tx)
	}
	return page, nil
}

// EndSession implements [banking.Provider] with DELETE /sessions/{id}. A
// session that is already closed, expired, revoked or unknown is not an
// error.
func (p *Provider) EndSession(ctx context.Context, session banking.Sensitive) error {
	if session.IsZero() {
		return nil
	}
	err := p.do(ctx, opEndSession, http.MethodDelete, "/sessions/"+url.PathEscape(session.Reveal()), nil, nil, nil)
	if e, ok := errors.AsType[*banking.Error](err); ok {
		switch e.Code {
		case codeClosedSession, codeSessionNotExist, codeExpiredSession, codeRevokedSession:
			return nil
		}
	}
	return err
}
