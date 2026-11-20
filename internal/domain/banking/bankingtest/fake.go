// Package bankingtest provides a deterministic, in-memory banking provider
// for tests and for exercising later slices (import engine, connection UI)
// without live bank access. It implements [banking.Provider] for either
// access mode (decision D10) and behaves like the provider boundary
// documents: per-session account handles that change on re-authorisation,
// stable external ids, paged transactions with continuation keys bound to
// session and window, empty pages that say "keep going", consent expiry and
// injectable failures of every kind in the taxonomy.
//
// It lives under internal/domain so that finance packages can use it in
// tests while the lint and import tests forbid them every real adapter
// under internal/providers. It contains no provider-specific code.
package bankingtest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
)

// ProviderName is the fake's provider name.
const ProviderName = "fake"

// Op names a provider operation, for fault injection and call records.
type Op string

const (
	OpInstitutions    Op = "institutions"
	OpStartConsent    Op = "start_consent"
	OpCompleteConsent Op = "complete_consent"
	OpAccounts        Op = "accounts"
	OpBalances        Op = "balances"
	OpTransactions    Op = "transactions"
	OpEndSession      Op = "end_session"
)

// Account is one account in a [Dataset].
type Account struct {
	// Account is reported as is, except Handle, which the fake assigns per
	// session.
	Account banking.Account
	// Linked says whether the account is linked to the application, which
	// matters only in [banking.ModeDeveloper].
	Linked       bool
	Balances     []banking.Balance
	Transactions []banking.Transaction
}

// Dataset is everything the fake bank knows.
type Dataset struct {
	Institutions []banking.Institution
	Accounts     []Account
}

// Options configure a [Provider].
type Options struct {
	Mode banking.Mode
	// Application defaults to "fake-" + Mode.
	Application string
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// PageSize is the number of transactions per page; 0 means 10.
	PageSize int
	// EmptyFirstPage makes the first page of every listing empty with a
	// continuation key, as some banks do.
	EmptyFirstPage bool
	// ConsentValidity caps consent; 0 means 180 days.
	ConsentValidity time.Duration
}

// Provider is the fake. Use [New]; the zero value is not usable.
type Provider struct {
	mu       sync.Mutex
	opts     Options
	caps     banking.Capabilities
	data     Dataset
	consents map[string]bool // pending consent states
	sessions map[string]*session
	sessionN int
	faults   map[Op][]error
	calls    []Op
}

type session struct {
	validUntil time.Time
	revoked    bool
	ended      bool
	handles    map[string]int // handle -> account index
}

var _ banking.Provider = (*Provider)(nil)

// New returns a fake serving data. It panics on an invalid mode, since that
// is a test bug.
func New(opts Options, data Dataset) *Provider {
	if !opts.Mode.Valid() {
		panic(fmt.Sprintf("bankingtest: invalid mode %q", opts.Mode))
	}
	if opts.Application == "" {
		opts.Application = "fake-" + string(opts.Mode)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.PageSize <= 0 {
		opts.PageSize = 10
	}
	if opts.ConsentValidity <= 0 {
		opts.ConsentValidity = 180 * 24 * time.Hour
	}
	return &Provider{
		opts: opts,
		caps: banking.Capabilities{
			Provider:                ProviderName,
			Application:             opts.Application,
			Mode:                    opts.Mode,
			Environment:             banking.EnvironmentSandbox,
			BalanceTypes:            []banking.BalanceType{banking.BalanceBooked, banking.BalanceAvailable},
			PendingTransactions:     true,
			StableEntryReferences:   true,
			MaxConsentValidity:      opts.ConsentValidity,
			MaxHistoryDays:          730,
			BackgroundFetchesPerDay: 4,
		},
		data:     cloneDataset(data),
		consents: map[string]bool{},
		sessions: map[string]*session{},
		faults:   map[Op][]error{},
	}
}

// FailNext queues errors returned, in order, by the next calls of op (one
// error per call). Build them with [Failure].
func (p *Provider) FailNext(op Op, errs ...error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.faults[op] = append(p.faults[op], errs...)
}

// Failure builds a classified error as an adapter would return it.
func Failure(op Op, kind banking.ErrorKind, reason banking.Reason, code string, retryAfter time.Duration) error {
	return &banking.Error{Kind: kind, Reason: reason, Provider: ProviderName, Op: string(op), Code: code, RetryAfter: retryAfter}
}

// Calls returns the operations called so far, in order.
func (p *Provider) Calls() []Op {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.calls)
}

// Revoke marks every existing session as revoked by the user at the bank.
func (p *Provider) Revoke() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.sessions {
		s.revoked = true
	}
}

// Mutate changes the dataset, for example to book a pending transaction or
// add a new one between imports.
func (p *Provider) Mutate(fn func(*Dataset)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fn(&p.data)
}

// AuthorizationCode returns the code the fake bank redirects back with for a
// consent started with state.
func AuthorizationCode(state string) banking.Sensitive {
	return banking.NewSensitive("fake-code-" + state)
}

// Capabilities implements [banking.Provider].
func (p *Provider) Capabilities() banking.Capabilities {
	c := p.caps
	c.BalanceTypes = slices.Clone(c.BalanceTypes)
	return c
}

// begin records the call, applies a queued fault and checks the context.
// It must be called with p.mu held.
func (p *Provider) begin(ctx context.Context, op Op) error {
	p.calls = append(p.calls, op)
	if err := ctx.Err(); err != nil {
		return err
	}
	if q := p.faults[op]; len(q) > 0 {
		p.faults[op] = q[1:]
		return q[0]
	}
	return nil
}

func (p *Provider) fail(op Op, kind banking.ErrorKind, reason banking.Reason, code string) error {
	return Failure(op, kind, reason, code, 0)
}

// Institutions implements [banking.Provider].
func (p *Provider) Institutions(ctx context.Context, country string) ([]banking.Institution, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.begin(ctx, OpInstitutions); err != nil {
		return nil, err
	}
	var out []banking.Institution
	for _, in := range p.data.Institutions {
		if in.Country == country {
			out = append(out, in)
		}
	}
	return out, nil
}

// StartConsent implements [banking.Provider].
func (p *Provider) StartConsent(ctx context.Context, req banking.ConsentRequest) (banking.ConsentStart, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.begin(ctx, OpStartConsent); err != nil {
		return banking.ConsentStart{}, err
	}
	if req.State == "" || req.RedirectURL == "" {
		return banking.ConsentStart{}, p.fail(OpStartConsent, banking.KindPermanent, "", "invalid_request")
	}
	known := false
	for _, in := range p.data.Institutions {
		known = known || (in.ID == req.Institution.ID && in.Country == req.Institution.Country)
	}
	if !known {
		return banking.ConsentStart{}, p.fail(OpStartConsent, banking.KindPermanent, "", "unknown_institution")
	}
	p.consents[req.State] = true
	u := url.URL{Scheme: "https", Host: "bank.example", Path: "/authorize", RawQuery: url.Values{"state": {req.State}}.Encode()}
	return banking.ConsentStart{AuthorizationURL: u.String(), Reference: "fake-auth-" + shortHash(req.State)}, nil
}

// CompleteConsent implements [banking.Provider]. The code must be
// [AuthorizationCode] of the state; a completion carrying an Error is a
// refused consent (permanent).
func (p *Provider) CompleteConsent(ctx context.Context, req banking.ConsentCompletion) (banking.Session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.begin(ctx, OpCompleteConsent); err != nil {
		return banking.Session{}, err
	}
	if !p.consents[req.State] {
		return banking.Session{}, p.fail(OpCompleteConsent, banking.KindPermanent, "", "unknown_state")
	}
	delete(p.consents, req.State)
	if req.Error != "" {
		return banking.Session{}, p.fail(OpCompleteConsent, banking.KindPermanent, "", "consent_refused")
	}
	if !req.Code.Equal(AuthorizationCode(req.State)) {
		return banking.Session{}, p.fail(OpCompleteConsent, banking.KindPermanent, "", "invalid_code")
	}
	p.sessionN++
	ref := "fake-session-" + strconv.Itoa(p.sessionN)
	s := &session{validUntil: p.opts.Now().UTC().Add(p.opts.ConsentValidity), handles: map[string]int{}}
	p.sessions[ref] = s
	return banking.Session{Reference: banking.NewSensitive(ref), ValidUntil: s.validUntil, Accounts: p.visibleAccounts(ref, s)}, nil
}

// visibleAccounts assigns this session's handles. Called with p.mu held.
func (p *Provider) visibleAccounts(ref string, s *session) []banking.Account {
	out := []banking.Account{}
	for i, a := range p.data.Accounts {
		if p.opts.Mode == banking.ModeDeveloper && !a.Linked {
			continue
		}
		h := ref + "-acct-" + strconv.Itoa(i+1)
		s.handles[h] = i
		acc := a.Account
		acc.Handle = banking.NewSensitive(h)
		out = append(out, acc)
	}
	return out
}

// session checks a session reference. Called with p.mu held.
func (p *Provider) session(op Op, ref banking.Sensitive) (*session, error) {
	s, ok := p.sessions[ref.Reveal()]
	switch {
	case !ok || s.ended:
		return nil, p.fail(op, banking.KindReconnectRequired, banking.ReasonSessionInvalid, "session_invalid")
	case s.revoked:
		return nil, p.fail(op, banking.KindReconnectRequired, banking.ReasonConsentRevoked, "consent_revoked")
	case !p.opts.Now().Before(s.validUntil):
		return nil, p.fail(op, banking.KindReconnectRequired, banking.ReasonConsentExpired, "session_expired")
	}
	return s, nil
}

// Accounts implements [banking.Provider].
func (p *Provider) Accounts(ctx context.Context, ref banking.Sensitive) ([]banking.Account, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.begin(ctx, OpAccounts); err != nil {
		return nil, err
	}
	s, err := p.session(OpAccounts, ref)
	if err != nil {
		return nil, err
	}
	return p.visibleAccounts(ref.Reveal(), s), nil
}

func (p *Provider) account(op Op, s *session, handle banking.Sensitive) (*Account, error) {
	i, ok := s.handles[handle.Reveal()]
	if !ok || i >= len(p.data.Accounts) {
		return nil, p.fail(op, banking.KindPermanent, "", "unknown_account")
	}
	return &p.data.Accounts[i], nil
}

// Balances implements [banking.Provider].
func (p *Provider) Balances(ctx context.Context, ref, handle banking.Sensitive) ([]banking.Balance, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.begin(ctx, OpBalances); err != nil {
		return nil, err
	}
	s, err := p.session(OpBalances, ref)
	if err != nil {
		return nil, err
	}
	a, err := p.account(OpBalances, s, handle)
	if err != nil {
		return nil, err
	}
	return slices.Clone(a.Balances), nil
}

// Transactions implements [banking.Provider]. Transactions are returned in
// dataset order, filtered to the inclusive window by [banking.Transaction.Date].
func (p *Provider) Transactions(ctx context.Context, ref, handle banking.Sensitive, q banking.TransactionQuery) (banking.TransactionPage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.begin(ctx, OpTransactions); err != nil {
		return banking.TransactionPage{}, err
	}
	s, err := p.session(OpTransactions, ref)
	if err != nil {
		return banking.TransactionPage{}, err
	}
	a, err := p.account(OpTransactions, s, handle)
	if err != nil {
		return banking.TransactionPage{}, err
	}
	if !q.From.IsZero() && !q.To.IsZero() && q.From.After(q.To) {
		return banking.TransactionPage{}, p.fail(OpTransactions, banking.KindPermanent, "", "wrong_transactions_period")
	}
	var all []banking.Transaction
	for _, t := range a.Transactions {
		d := t.Date()
		if (!q.From.IsZero() && d.Before(q.From)) || (!q.To.IsZero() && d.After(q.To)) {
			continue
		}
		all = append(all, t)
	}
	prefix := continuationPrefix(ref.Reveal(), handle.Reveal(), q)
	offset := 0
	if q.Continuation != "" {
		rest, ok := strings.CutPrefix(q.Continuation, prefix)
		n, err := strconv.Atoi(rest)
		if !ok || err != nil || n < 0 || n > len(all) {
			return banking.TransactionPage{}, p.fail(OpTransactions, banking.KindPermanent, "", "invalid_continuation_key")
		}
		offset = n
	} else if p.opts.EmptyFirstPage {
		return banking.TransactionPage{Transactions: []banking.Transaction{}, Next: prefix + "0"}, nil
	}
	end := min(offset+p.opts.PageSize, len(all))
	page := banking.TransactionPage{Transactions: slices.Clone(all[offset:end])}
	if end < len(all) {
		page.Next = prefix + strconv.Itoa(end)
	}
	return page, nil
}

// continuationPrefix binds a key to the session, account and window.
func continuationPrefix(session, handle string, q banking.TransactionQuery) string {
	return "fk-" + shortHash(session+"|"+handle+"|"+q.From.String()+"|"+q.To.String()) + "-"
}

// EndSession implements [banking.Provider].
func (p *Provider) EndSession(ctx context.Context, ref banking.Sensitive) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.begin(ctx, OpEndSession); err != nil {
		return err
	}
	if s, ok := p.sessions[ref.Reveal()]; ok {
		s.ended = true
	}
	return nil
}

func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:6])
}

func cloneDataset(d Dataset) Dataset {
	out := Dataset{Institutions: slices.Clone(d.Institutions)}
	for _, a := range d.Accounts {
		a.Balances = slices.Clone(a.Balances)
		a.Transactions = slices.Clone(a.Transactions)
		out.Accounts = append(out.Accounts, a)
	}
	return out
}
