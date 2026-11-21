// Package bankconnect is the user-facing side of bank connections (task B4,
// ADR-0040): which provider applications are configured, starting and
// completing consent, reconnecting, refreshing on request, disconnecting, and
// reading every connection with the freshness of each of its accounts.
//
// It sits between the HTTP API and the provider boundary
// (internal/domain/banking). It never reads or returns provider session
// references, authorisation codes, account handles or full account
// identifiers: a connection and its accounts are shown by Ledger's own IDs
// and a short identifier hint. Bank facts are only read here; user data is
// never touched.
package bankconnect

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/bankimport"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
)

// Errors a caller maps to answers. Provider failures are returned as
// [*banking.Error] (wrapped), so the caller can tell reconnect-required from
// retryable from permanent.
var (
	// ErrUnknownApplication: no configured provider application has the name.
	ErrUnknownApplication = errors.New("bankconnect: no such provider application is configured")
	// ErrUnknownInstitution: the provider does not offer the institution.
	ErrUnknownInstitution = errors.New("bankconnect: the provider does not offer this institution")
	// ErrUnknownConsent: the redirect's state matches no consent in progress
	// (never started, already completed, or replaced by a newer start).
	ErrUnknownConsent = errors.New("bankconnect: no consent is waiting for this redirect")
	// ErrNotActive: a refresh needs an active connection.
	ErrNotActive = errors.New("bankconnect: the connection is not active")
	// ErrPending: the connection's first consent has not completed.
	ErrPending = errors.New("bankconnect: the connection's consent has not completed")
	// ErrNoRedirect: no redirect URL is configured, so consent cannot start.
	ErrNoRedirect = errors.New("bankconnect: banking.redirect_url is not configured")
	// ErrDisconnected: the connection was disconnected on purpose.
	ErrDisconnected = errors.New("bankconnect: the connection is disconnected")
)

// Refresher runs one refresh. *bankimport.Engine implements it; it must be the
// engine the background scheduler uses, so one connection is never refreshed
// twice at once (ADR-0028).
type Refresher interface {
	Refresh(ctx context.Context, id banking.ConnectionID, p banking.Provider) (bankimport.Result, error)
}

// ScheduleStatus returns the scheduler's view of the connections, or nil when
// the scheduler is off.
type ScheduleStatus func() []bankimport.ConnectionStatus

// Service is the connection service. Facts, Providers, Engine and Audit are
// required.
type Service struct {
	Facts banking.FactStore
	// Providers maps a provider application name to its adapter.
	Providers map[string]banking.Provider
	Engine    Refresher
	Audit     audit.Recorder
	// RedirectURL is where the bank sends the user back (the UI's callback
	// page); empty means connecting is not configured.
	RedirectURL string
	// Schedule is optional.
	Schedule ScheduleStatus
	// StaleAfter is the freshness threshold; zero means banking.DefaultStaleAfter.
	StaleAfter time.Duration
	Now        func() time.Time
	// NewState returns an unguessable consent state; nil uses 32 random bytes.
	NewState func() string
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) staleAfter() time.Duration {
	if s.StaleAfter > 0 {
		return s.StaleAfter
	}
	return banking.DefaultStaleAfter
}

func (s *Service) state() string {
	if s.NewState != nil {
		return s.NewState()
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("bankconnect: no randomness: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Application is a configured provider application as shown to the user.
type Application struct {
	Provider      string
	Name          string
	Mode          banking.Mode
	Environment   banking.Environment
	AccountAccess banking.AccountAccess
	Capabilities  banking.Capabilities
}

// Applications lists the configured applications, sorted by name.
func (s *Service) Applications() []Application {
	out := make([]Application, 0, len(s.Providers))
	for _, p := range s.Providers {
		c := p.Capabilities()
		out = append(out, Application{Provider: c.Provider, Name: c.Application, Mode: c.Mode, Environment: c.Environment, AccountAccess: c.AccountAccess(), Capabilities: c})
	}
	slices.SortFunc(out, func(a, b Application) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func (s *Service) provider(app string) (banking.Provider, error) {
	p, ok := s.Providers[app]
	if !ok {
		return nil, ErrUnknownApplication
	}
	return p, nil
}

// Institutions lists the banks an application offers in a country. The call
// is a read: it never changes anything.
func (s *Service) Institutions(ctx context.Context, app, country string) ([]banking.Institution, error) {
	p, err := s.provider(app)
	if err != nil {
		return nil, err
	}
	return p.Institutions(ctx, country)
}

// Start is a request to connect a bank.
type Start struct {
	Application string
	// Institution is looked up with the provider: only an institution it
	// offers in Country can be connected.
	InstitutionID string
	Country       string
	PSUType       banking.PSUType
	// ValidFor is the consent length asked for; zero asks for the longest the
	// institution or provider allows. The bank may cap it.
	ValidFor time.Duration
}

// Started is a connection whose consent was begun and where to send the user.
type Started struct {
	Connection       banking.Connection
	AuthorizationURL string
}

func (s *Service) validUntil(c banking.Capabilities, i banking.Institution, forDur time.Duration) time.Time {
	d := forDur
	longest := c.MaxConsentValidity
	if i.MaxConsentValidity > 0 && (longest == 0 || i.MaxConsentValidity < longest) {
		longest = i.MaxConsentValidity
	}
	if d <= 0 || (longest > 0 && d > longest) {
		d = longest
	}
	if d <= 0 {
		d = 90 * 24 * time.Hour
	}
	return s.now().Add(d)
}

// Start records a pending connection and asks the provider to begin consent.
// StartConsent is not an idempotent call and is never retried here. If the
// provider refuses, the connection stays visible as failed (with the error
// kind and code, never a payload) so the failure is not lost.
func (s *Service) Start(ctx context.Context, actor audit.Actor, st Start) (Started, error) {
	if s.RedirectURL == "" {
		return Started{}, ErrNoRedirect
	}
	p, err := s.provider(st.Application)
	if err != nil {
		return Started{}, err
	}
	insts, err := p.Institutions(ctx, st.Country)
	if err != nil {
		return Started{}, err
	}
	i := slices.IndexFunc(insts, func(i banking.Institution) bool { return i.ID == st.InstitutionID })
	if i < 0 {
		return Started{}, ErrUnknownInstitution
	}
	inst := insts[i]
	caps := p.Capabilities()
	state := s.state()
	psu := st.PSUType
	if psu == "" {
		psu = banking.PSUPersonal
	}
	now := s.now()
	conn, err := s.Facts.CreateConnection(ctx, banking.NewConnection{Capabilities: caps, Institution: inst, ConsentState: state}, now)
	if err != nil {
		return Started{}, err
	}
	cs, err := p.StartConsent(ctx, banking.ConsentRequest{Institution: inst, ValidUntil: s.validUntil(caps, inst, st.ValidFor), RedirectURL: s.RedirectURL, State: state, PSUType: psu})
	if err != nil {
		if _, serr := s.Facts.SetConnectionStatus(context.WithoutCancel(ctx), conn.ID, banking.ConnectionFailed, err, s.now()); serr != nil {
			return Started{}, errors.Join(err, serr)
		}
		return Started{}, err
	}
	if err := s.record(ctx, audit.BankConnectionStarted, actor, conn, map[string]string{"provider": conn.Provider, "application": conn.Application}); err != nil {
		return Started{}, err
	}
	return Started{Connection: conn, AuthorizationURL: cs.AuthorizationURL}, nil
}

// Reconnect begins a new consent for an existing connection (consent expired,
// revoked, or the user wants to renew it). The connection keeps its status
// and its data until the new consent completes, so a cancelled or failed
// reconnect leaves it as it was.
func (s *Service) Reconnect(ctx context.Context, actor audit.Actor, id banking.ConnectionID, validFor time.Duration) (Started, error) {
	if s.RedirectURL == "" {
		return Started{}, ErrNoRedirect
	}
	conn, err := s.Facts.Connection(ctx, id)
	if err != nil {
		return Started{}, err
	}
	if conn.Status == banking.ConnectionPending {
		return Started{}, ErrPending
	}
	p, err := s.provider(conn.Application)
	if err != nil {
		return Started{}, err
	}
	state := s.state()
	conn, err = s.Facts.BeginReconnect(ctx, id, state, s.now())
	if err != nil {
		return Started{}, err
	}
	cs, err := p.StartConsent(ctx, banking.ConsentRequest{Institution: conn.Institution, ValidUntil: s.validUntil(p.Capabilities(), conn.Institution, validFor), RedirectURL: s.RedirectURL, State: state, PSUType: banking.PSUPersonal})
	if err != nil {
		return Started{}, err
	}
	if err := s.record(ctx, audit.BankConnectionReconnectStarted, actor, conn, map[string]string{"provider": conn.Provider, "application": conn.Application}); err != nil {
		return Started{}, err
	}
	return Started{Connection: conn, AuthorizationURL: cs.AuthorizationURL}, nil
}

// Redirect is what the bank sent the user back with. Code is never logged or
// stored.
type Redirect struct {
	State string
	Code  banking.Sensitive
	// Error is the provider's error code when the user cancelled or the bank
	// refused.
	Error string
}

// Complete finishes a consent: it finds the connection by the redirect's
// state, asks the provider for the session and activates the connection. The
// state works once: completing clears it, so replaying a redirect finds
// nothing. A refused first consent leaves the connection failed; a refused
// reconnect leaves a connection that needed reconnecting still needing it. A
// retryable failure (the bank could not be reached) changes nothing, so the
// same redirect can be completed again.
func (s *Service) Complete(ctx context.Context, actor audit.Actor, r Redirect) (banking.Connection, error) {
	conn, err := s.Facts.ConnectionByConsentState(ctx, r.State)
	if errors.Is(err, banking.ErrNotFound) {
		return banking.Connection{}, ErrUnknownConsent
	}
	if err != nil {
		return banking.Connection{}, err
	}
	p, err := s.provider(conn.Application)
	if err != nil {
		return banking.Connection{}, err
	}
	sess, err := p.CompleteConsent(ctx, banking.ConsentCompletion{State: r.State, Code: r.Code, Error: r.Error})
	if err == nil && r.Error != "" {
		err = &banking.Error{Kind: banking.KindPermanent, Provider: conn.Provider, Op: "complete_consent", Code: r.Error, Err: errors.New("consent was not granted")}
	}
	if err != nil {
		s.consentFailed(ctx, conn, err)
		return banking.Connection{}, err
	}
	out, err := s.Facts.ActivateConnection(ctx, conn.ID, sess, s.now())
	if err != nil {
		return banking.Connection{}, err
	}
	reconnect := "false"
	if conn.Status != banking.ConnectionPending {
		reconnect = "true"
	}
	if err := s.record(ctx, audit.BankConnectionConnected, actor, out, map[string]string{"provider": out.Provider, "application": out.Application, "reconnect": reconnect}); err != nil {
		return out, err
	}
	return out, nil
}

// consentFailed records a failed consent without losing the connection's
// current data. A pending connection becomes failed; a connection that was
// reconnect-required (or failed) keeps its status with the new error; an
// active or disconnected one is left alone apart from its consent state
// being spent.
func (s *Service) consentFailed(ctx context.Context, c banking.Connection, cause error) {
	switch banking.Classify(cause) {
	case banking.KindRetryable, banking.KindRateLimited, banking.KindCanceled:
		// The bank or provider could not be reached: the consent is neither
		// granted nor refused, so the state stays valid and the redirect can be
		// completed again (the callback page offers it).
		return
	}
	ctx = context.WithoutCancel(ctx)
	switch c.Status {
	case banking.ConnectionPending:
		_, _ = s.Facts.SetConnectionStatus(ctx, c.ID, banking.ConnectionFailed, cause, s.now())
	case banking.ConnectionReconnectRequired, banking.ConnectionFailed:
		_, _ = s.Facts.SetConnectionStatus(ctx, c.ID, c.Status, cause, s.now())
	}
}

// Refresh refreshes an active connection now, through the shared engine, and
// returns the connection as it stands afterwards together with the run. A
// refresh that fails is not an error of this call: the run says how it ended
// and last good data stays. ErrRefreshInProgress (banking) means a refresh is
// already running; the caller should show that and try again later.
func (s *Service) Refresh(ctx context.Context, actor audit.Actor, id banking.ConnectionID) (View, error) {
	conn, err := s.Facts.Connection(ctx, id)
	if err != nil {
		return View{}, err
	}
	switch conn.Status {
	case banking.ConnectionPending:
		return View{}, ErrPending
	case banking.ConnectionDisconnected:
		return View{}, ErrDisconnected
	}
	if conn.Status != banking.ConnectionActive {
		// reconnect_required and failed connections cannot be fetched from:
		// the session is gone, so retrying cannot help (ADR-0013).
		return View{}, ErrNotActive
	}
	p, err := s.provider(conn.Application)
	if err != nil {
		return View{}, err
	}
	_, rerr := s.Engine.Refresh(ctx, id, p)
	if errors.Is(rerr, banking.ErrRefreshInProgress) {
		return View{}, rerr
	}
	outcome := "failed"
	if runs, err := s.Facts.RefreshRuns(ctx, id, 1); err == nil && len(runs) > 0 && runs[0].Outcome.Valid() {
		outcome = string(runs[0].Outcome)
	}
	if err := s.record(ctx, audit.BankRefreshRequested, actor, conn, map[string]string{"provider": conn.Provider, "application": conn.Application, "outcome": outcome}); err != nil {
		return View{}, err
	}
	return s.View(ctx, id)
}

// Disconnect ends the provider session and marks the connection
// disconnected, keeping its accounts, balances and transactions as history. A
// session the provider no longer knows counts as ended. If the provider
// cannot be reached, nothing changes and the error is returned, so the user
// is never told a consent was ended when it may still be live.
func (s *Service) Disconnect(ctx context.Context, actor audit.Actor, id banking.ConnectionID, expected int64) (banking.Connection, error) {
	conn, err := s.Facts.Connection(ctx, id)
	if err != nil {
		return banking.Connection{}, err
	}
	if conn.Version != expected {
		// Let the store produce the version conflict the API reports.
		return s.Facts.DisconnectConnection(ctx, id, expected, s.now())
	}
	if !conn.Session.IsZero() && conn.Status != banking.ConnectionDisconnected {
		if p, perr := s.provider(conn.Application); perr == nil {
			if err := p.EndSession(ctx, conn.Session); err != nil && !errors.Is(err, banking.ErrReconnectRequired) {
				return banking.Connection{}, err
			}
		}
	}
	out, err := s.Facts.DisconnectConnection(ctx, id, expected, s.now())
	if err != nil {
		return banking.Connection{}, err
	}
	if err := s.record(ctx, audit.BankConnectionDisconnected, actor, out, map[string]string{"provider": out.Provider, "application": out.Application}); err != nil {
		return out, err
	}
	return out, nil
}

func (s *Service) record(ctx context.Context, a audit.Action, actor audit.Actor, c banking.Connection, details map[string]string) error {
	if err := s.Audit.Record(ctx, audit.Event{Action: a, Actor: actor, TargetType: "bank_connection", TargetID: string(c.ID), At: s.now(), Details: details}); err != nil {
		return fmt.Errorf("bankconnect: the change was made but its audit entry could not be written: %w", err)
	}
	return nil
}

// Balance is the latest balance of one type of an account. There is no
// balance when the bank never reported one: never a zero standing in for
// unknown.
type Balance struct {
	Type          banking.BalanceType
	Amount        money.Money
	ReferenceDate string
	ObtainedAt    time.Time
}

// AccountView is an account with its freshness.
type AccountView struct {
	Account   banking.StoredAccount
	Sync      banking.AccountSync
	Freshness banking.Freshness
	Balances  []Balance
	IDHint    string
	NoLonger  bool
}

// View is a connection with its accounts, its latest refresh run and the
// scheduler's state for it.
type View struct {
	Connection banking.Connection
	Accounts   []AccountView
	LastRun    *banking.RefreshRun
	Schedule   *bankimport.ConnectionStatus
	// ConsentExpiresInDays is set while the connection is active and its
	// consent ends within ConsentWarnDays.
	AsOf time.Time
}

// ConsentWarnDays is how far ahead an expiring consent is called out.
const ConsentWarnDays = 14

// idHint is the last four characters of an IBAN (or other identifier), so
// accounts can be told apart without showing the number.
func idHint(a banking.StoredAccount) string {
	for _, id := range []banking.Sensitive{a.Identifiers.IBAN, a.Identifiers.BBAN, a.Identifiers.MaskedPAN, a.Identifiers.Other} {
		v := strings.ReplaceAll(id.Reveal(), " ", "")
		if len(v) >= 8 {
			return v[len(v)-4:]
		}
	}
	return ""
}

func (s *Service) view(ctx context.Context, c banking.Connection, sched map[banking.ConnectionID]bankimport.ConnectionStatus) (View, error) {
	v := View{Connection: c, AsOf: s.now()}
	accs, err := s.Facts.Accounts(ctx, c.ID)
	if err != nil {
		return View{}, err
	}
	for _, a := range accs {
		sync, err := s.Facts.AccountSync(ctx, a.ID)
		if err != nil {
			return View{}, err
		}
		bals, err := s.Facts.Balances(ctx, a.ID)
		if err != nil {
			return View{}, err
		}
		av := AccountView{Account: a, Sync: sync, Freshness: banking.AccountFreshness(c.Status, sync, v.AsOf, s.staleAfter()),
			Balances: latestBalances(bals), IDHint: idHint(a), NoLonger: !a.NoLongerReportedAt.IsZero()}
		v.Accounts = append(v.Accounts, av)
	}
	if runs, err := s.Facts.RefreshRuns(ctx, c.ID, 1); err != nil {
		return View{}, err
	} else if len(runs) > 0 {
		v.LastRun = &runs[0]
	}
	if st, ok := sched[c.ID]; ok {
		v.Schedule = &st
	}
	return v, nil
}

func latestBalances(all []banking.StoredBalance) []Balance {
	latest := map[banking.BalanceType]banking.StoredBalance{}
	for _, b := range all {
		if cur, ok := latest[b.Type]; !ok || b.ObtainedAt.After(cur.ObtainedAt) {
			latest[b.Type] = b
		}
	}
	var out []Balance
	for _, t := range []banking.BalanceType{banking.BalanceBooked, banking.BalanceAvailable, banking.BalanceExpected, banking.BalanceOther} {
		if b, ok := latest[t]; ok {
			ref := ""
			if !b.ReferenceDate.IsZero() {
				ref = b.ReferenceDate.String()
			}
			out = append(out, Balance{Type: t, Amount: b.Amount, ReferenceDate: ref, ObtainedAt: b.ObtainedAt})
		}
	}
	return out
}

func (s *Service) scheduleMap() map[banking.ConnectionID]bankimport.ConnectionStatus {
	m := map[banking.ConnectionID]bankimport.ConnectionStatus{}
	if s.Schedule == nil {
		return m
	}
	for _, st := range s.Schedule() {
		m[st.ConnectionID] = st
	}
	return m
}

// View returns one connection.
func (s *Service) View(ctx context.Context, id banking.ConnectionID) (View, error) {
	c, err := s.Facts.Connection(ctx, id)
	if err != nil {
		return View{}, err
	}
	return s.view(ctx, c, s.scheduleMap())
}

// List returns every connection, oldest first.
func (s *Service) List(ctx context.Context) ([]View, error) {
	cs, err := s.Facts.Connections(ctx)
	if err != nil {
		return nil, err
	}
	sched := s.scheduleMap()
	out := make([]View, 0, len(cs))
	for _, c := range cs {
		v, err := s.view(ctx, c, sched)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}
