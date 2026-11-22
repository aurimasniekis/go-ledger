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
	"log/slog"
	"slices"
	"strings"
	"sync"
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
	ErrDisconnected = banking.ErrDisconnected
	// ErrConsentExpired: the consent was started longer ago than
	// Service.ConsentTTL. Start (or reconnect) again.
	ErrConsentExpired = errors.New("bankconnect: the consent was not completed in time")
	// ErrWrongInitiator: the consent was started by another user.
	ErrWrongInitiator = errors.New("bankconnect: the consent was started by another user")
)

// DefaultConsentTTL is how long a started consent may wait for its redirect.
// The lifetime is operator decision D40.3; the value still has to be checked
// against Enable Banking's documented authorisation-link validity (ADR-0040).
const DefaultConsentTTL = time.Hour

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
	Facts banking.ConsentStore
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
	// ConsentTTL is how long a started consent may wait; zero means
	// DefaultConsentTTL.
	ConsentTTL time.Duration
	// RefreshTimeout is how long a refresh on request is waited for; it must
	// be below the HTTP write timeout, or the response is dropped. At the
	// deadline the run is NOT cancelled: it goes on in the background and the
	// answer says it is still running (D40.4). Zero waits until it ends.
	RefreshTimeout time.Duration
	// RefreshCooldown is how long after a refresh of a connection finished
	// (by any trigger, any outcome) another refresh on request is refused
	// with *CooldownError (decision D-B6c, banking.refresh.request_cooldown).
	// The background scheduler does not use the service and is never held
	// back. Zero switches the cooldown off.
	RefreshCooldown time.Duration
	// Log receives housekeeping failures that are not the caller's error; nil
	// uses slog.Default.
	Log *slog.Logger

	// detached counts the refresh runs Refresh started that have not ended;
	// Wait waits for them. A Service must not be copied once used.
	detached sync.WaitGroup
	// mu guards closed and the base context of detached runs.
	mu     sync.Mutex
	closed bool // set by Wait: no further detached run may start
	base   context.Context
	cancel context.CancelFunc
}

// ErrRefreshCooldown is matched (errors.Is) by every *CooldownError: a refresh
// on request finished less than Service.RefreshCooldown ago (decision D-B6c).
var ErrRefreshCooldown = errors.New("bankconnect: a refresh of this connection finished a moment ago")

// CooldownError is returned by Refresh while the connection's cooldown after
// its last finished refresh has not ended. Nothing was started or recorded.
type CooldownError struct {
	// Until is when a refresh on request is allowed again (UTC).
	Until time.Time
	// RetryAfter is how long from now that is, always positive.
	RetryAfter time.Duration
}

func (e *CooldownError) Error() string {
	return fmt.Sprintf("bankconnect: the connection was refreshed a moment ago; a refresh on request is allowed again in %s", e.RetryAfter.Round(time.Second))
}

// Is makes errors.Is(err, ErrRefreshCooldown) true.
func (e *CooldownError) Is(target error) bool { return target == ErrRefreshCooldown }

// ErrShuttingDown is returned by Refresh once shutdown began (Wait was
// called): no new detached run may start.
var ErrShuttingDown = errors.New("bankconnect: Ledger is shutting down")

// startDetached registers a detached run under the lock, so none can start
// after Wait began. The returned context carries ctx's values, is not
// cancelled with the request and ends when CancelRefreshes is called.
func (s *Service) startDetached(ctx context.Context) (context.Context, context.CancelFunc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, ErrShuttingDown
	}
	if s.base == nil {
		s.base, s.cancel = context.WithCancel(context.Background())
	}
	bg, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(s.base, cancel)
	s.detached.Add(1)
	return bg, func() { stop(); cancel() }, nil
}

// CancelRefreshes cancels the context of every detached refresh, so the
// engine ends each run as interrupted (and it is audited) while the database
// is still open. Shutdown calls it when Wait ran out of time.
func (s *Service) CancelRefreshes() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.base == nil {
		s.base, s.cancel = context.WithCancel(context.Background())
	}
	s.cancel()
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Service) consentTTL() time.Duration {
	if s.ConsentTTL > 0 {
		return s.ConsentTTL
	}
	return DefaultConsentTTL
}

// principal is who a consent is bound to: the user (a token acts as its owner).
func principal(a audit.Actor) string { return a.UserID }

// event builds the audit event of a change, written in the change's own
// transaction.
func (s *Service) event(a audit.Action, actor audit.Actor, at time.Time, details func(banking.Connection) map[string]string) banking.AuditFunc {
	return func(c banking.Connection) audit.Event {
		return audit.Event{Action: a, Actor: actor, TargetType: "bank_connection", TargetID: string(c.ID), At: at, Details: details(c)}
	}
}

func appDetails(c banking.Connection) map[string]string {
	return map[string]string{"provider": c.Provider, "application": c.Application}
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
	conn, err := s.Facts.StartConnection(ctx, banking.NewConnection{Capabilities: caps, Institution: inst, ConsentState: state}, principal(actor), now,
		s.event(audit.BankConnectionStarted, actor, now, appDetails))
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
	now := s.now()
	conn, err = s.Facts.BeginReconnectBy(ctx, id, state, principal(actor), now, s.event(audit.BankConnectionReconnectStarted, actor, now, appDetails))
	if err != nil {
		return Started{}, err
	}
	cs, err := p.StartConsent(ctx, banking.ConsentRequest{Institution: conn.Institution, ValidUntil: s.validUntil(p.Capabilities(), conn.Institution, validFor), RedirectURL: s.RedirectURL, State: state, PSUType: banking.PSUPersonal})
	if err != nil {
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

// Completed is a finished consent.
type Completed struct {
	Connection banking.Connection
	// PreviousSessionEndFailed is set when the consent renewed a connection
	// and the provider session it replaced could not be ended: it may still
	// be live at the bank. The failure is audited as
	// bank_connection.session_end_failed.
	PreviousSessionEndFailed bool
}

// Complete finishes a consent: it claims the redirect's state, asks the
// provider for the session and activates the connection. The claim is atomic
// and happens before the provider is called, so of two completions of one
// state only one reaches the provider and the other finds
// [ErrUnknownConsent]. A state older than the consent time to live is
// [ErrConsentExpired], and one started by another user is [ErrWrongInitiator]
// (and stays valid for them). A refused first consent leaves the connection
// failed; a refused reconnect leaves a connection that needed reconnecting
// still needing it. Only a retryable failure (the bank could not be reached)
// puts the state back, so the same redirect can be completed again. When the
// consent renewed an active connection, the replaced provider session is
// ended, best effort.
func (s *Service) Complete(ctx context.Context, actor audit.Actor, r Redirect) (Completed, error) {
	now := s.now()
	claim, err := s.Facts.ClaimConsent(ctx, r.State, principal(actor), now, s.consentTTL())
	switch {
	case errors.Is(err, banking.ErrNotFound):
		return Completed{}, ErrUnknownConsent
	case errors.Is(err, banking.ErrConsentTimedOut):
		return Completed{}, ErrConsentExpired
	case errors.Is(err, banking.ErrConsentInitiator):
		return Completed{}, ErrWrongInitiator
	case err != nil:
		return Completed{}, err
	}
	conn := claim.Connection
	p, err := s.provider(conn.Application)
	if err != nil {
		s.consentFailed(ctx, claim, err)
		return Completed{}, err
	}
	sess, err := p.CompleteConsent(ctx, banking.ConsentCompletion{State: r.State, Code: r.Code, Error: r.Error})
	if err == nil && r.Error != "" {
		err = &banking.Error{Kind: banking.KindPermanent, Provider: conn.Provider, Op: "complete_consent", Code: r.Error, Err: errors.New("consent was not granted")}
	}
	if err != nil {
		s.consentFailed(ctx, claim, err)
		return Completed{}, err
	}
	reconnect := "false"
	if conn.Status != banking.ConnectionPending {
		reconnect = "true"
	}
	out, err := s.Facts.ActivateClaimed(ctx, claim, sess, now, s.event(audit.BankConnectionConnected, actor, now, func(c banking.Connection) map[string]string {
		return map[string]string{"provider": c.Provider, "application": c.Application, "reconnect": reconnect}
	}))
	if err != nil {
		// The provider granted a session Ledger could not record (or audit):
		// end it so no consent stays open at the bank unseen, and do not leave
		// the connection waiting for a code that can no longer be used.
		bg := context.WithoutCancel(ctx)
		_ = p.EndSession(bg, sess.Reference)
		if !errors.Is(err, ErrDisconnected) {
			s.consentFailed(bg, claim, &banking.Error{Kind: banking.KindPermanent, Provider: conn.Provider, Op: "activate", Code: "activation_failed", Err: errors.New("the session could not be recorded")})
		}
		return Completed{}, err
	}
	done := Completed{Connection: out}
	if old := conn.Session; !old.IsZero() && old.Reveal() != sess.Reference.Reveal() {
		done.PreviousSessionEndFailed = !s.endReplacedSession(ctx, actor, p, out, old)
	}
	return done, nil
}

// endReplacedSession ends the provider session a reconnect replaced. A
// session the provider no longer knows counts as ended. A failure is audited
// (kind and code only: never the reference) and reported to the caller.
func (s *Service) endReplacedSession(ctx context.Context, actor audit.Actor, p banking.Provider, c banking.Connection, old banking.Sensitive) bool {
	bg := context.WithoutCancel(ctx)
	err := p.EndSession(bg, old)
	if err == nil || errors.Is(err, banking.ErrReconnectRequired) {
		return true
	}
	d := appDetails(c)
	d["kind"] = string(banking.Classify(err))
	if aerr := s.Audit.Record(bg, audit.Event{Action: audit.BankSessionEndFailed, Actor: actor, TargetType: "bank_connection", TargetID: string(c.ID), At: s.now(), Details: d}); aerr != nil {
		s.log().Error("the failed end of a replaced bank session could not be audited", "connection_id", string(c.ID), "kind", d["kind"], "error", aerr.Error())
	}
	return false
}

// consentFailed records a failed consent without losing the connection's
// current data. A pending connection becomes failed; a connection that was
// reconnect-required (or failed) keeps its status with the new error; an
// active one is left alone. The state was claimed, so it is already spent
// unless the failure is one a retry can fix, when it is put back.
func (s *Service) consentFailed(ctx context.Context, claim banking.ConsentClaim, cause error) {
	ctx = context.WithoutCancel(ctx)
	switch banking.Classify(cause) {
	case banking.KindRetryable, banking.KindRateLimited, banking.KindCanceled:
		// The bank or provider could not be reached: the consent is neither
		// granted nor refused, so the state stays valid and the redirect can be
		// completed again (the callback page offers it).
		_ = s.Facts.ReleaseConsent(ctx, claim)
		return
	}
	c := claim.Connection
	// Conditional on what the claim saw: a consent completed, a disconnect or
	// a newer reconnect since then is left as it is.
	switch c.Status {
	case banking.ConnectionPending:
		_, _, _ = s.Facts.FailClaimed(ctx, claim, banking.ConnectionFailed, cause, s.now())
	case banking.ConnectionReconnectRequired, banking.ConnectionFailed:
		_, _, _ = s.Facts.FailClaimed(ctx, claim, c.Status, cause, s.now())
	}
}

// Wait closes the service, then blocks until every refresh started by Refresh
// has ended and its audit entry is written, or ctx ends (then it returns ctx's
// error). Once Wait has been called, Refresh returns ErrShuttingDown and starts
// nothing. Shutdown calls Wait after the HTTP server has stopped and before the
// database closes, so a run that was detached from its request is not cut off;
// a run still going when Wait gives up is cancelled with CancelRefreshes.
func (s *Service) Wait(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.detached.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Refresh refreshes an active connection now, through the shared engine, and
// returns the connection as it stands afterwards together with the run. A
// refresh that fails is not an error of this call: the run says how it ended
// and last good data stays. ErrRefreshInProgress (banking) means a refresh is
// already running; the caller should show that and try again later.
// *CooldownError (errors.Is ErrRefreshCooldown) means the last refresh of the
// connection finished less than RefreshCooldown ago: nothing starts.
// ErrShuttingDown means Wait has been called (shutdown began): no run starts,
// and the HTTP API answers 503 dependency_unavailable.
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
	if s.RefreshCooldown > 0 {
		runs, err := s.Facts.RefreshRuns(ctx, id, 1)
		if err != nil {
			return View{}, err
		}
		if len(runs) > 0 {
			now := s.now()
			if end := s.cooldownEnd(&runs[0], conn.ConsentGrantedAt); end.After(now) {
				return View{}, &CooldownError{Until: end, RetryAfter: end.Sub(now)}
			}
		}
	}
	// The run is detached from the request: a client that gives up, or the
	// deadline below, never cancels a refresh half way (it would be recorded
	// as a failure and count against the scheduler's backoff).
	done := make(chan refreshDone, 1)
	bg, release, err := s.startDetached(ctx)
	if err != nil {
		return View{}, err
	}
	go func() {
		defer s.detached.Done()
		defer release()
		res, rerr := s.Engine.Refresh(bg, id, p)
		// The audit entry is written even when the run was cancelled.
		bg := context.WithoutCancel(bg)
		d := refreshDone{res: res, rerr: rerr}
		if res.Run.ID != "" {
			outcome := "failed"
			if res.Run.Outcome.Valid() {
				outcome = string(res.Run.Outcome)
			}
			d.auditErr = s.record(bg, audit.BankRefreshRequested, actor, conn, map[string]string{"provider": conn.Provider, "application": conn.Application, "outcome": outcome})
			if d.auditErr != nil {
				s.log().Error("the audit entry of a bank refresh could not be written", "connection_id", string(id), "error", d.auditErr.Error())
			}
		}
		done <- d
	}()
	var deadline <-chan time.Time
	if s.RefreshTimeout > 0 {
		t := time.NewTimer(s.RefreshTimeout)
		defer t.Stop()
		deadline = t.C
	}
	select {
	case d := <-done:
		if d.res.Run.ID == "" {
			// No run started: nothing to report about a run. The connection
			// stopped being refreshable meanwhile, or the engine could not start.
			switch {
			case errors.Is(d.rerr, bankimport.ErrNotRefreshable):
				return View{}, ErrNotActive
			case d.rerr == nil:
				d.rerr = errors.New("bankconnect: the refresh returned no run")
			}
			return View{}, d.rerr
		}
		if d.auditErr != nil {
			return View{}, d.auditErr
		}
		return s.View(ctx, id)
	case <-deadline:
		// Still running: answer with what is stored and let the caller poll.
		// The goroutine finishes the run and writes the audit entry.
		v, err := s.View(ctx, id)
		if err != nil {
			return View{}, err
		}
		v.RefreshRunning = true
		return v, nil
	case <-ctx.Done():
		return View{}, ctx.Err()
	}
}

// refreshDone is what a detached refresh produced.
type refreshDone struct {
	res      bankimport.Result
	rerr     error
	auditErr error
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
	now := s.now()
	ev := s.event(audit.BankConnectionDisconnected, actor, now, appDetails)
	if conn.Version != expected {
		// Let the store produce the version conflict the API reports.
		return s.Facts.DisconnectWithAudit(ctx, id, expected, now, ev)
	}
	if !conn.Session.IsZero() && conn.Status != banking.ConnectionDisconnected {
		if p, perr := s.provider(conn.Application); perr == nil {
			if err := p.EndSession(ctx, conn.Session); err != nil && !errors.Is(err, banking.ErrReconnectRequired) {
				return banking.Connection{}, err
			}
		}
	}
	return s.Facts.DisconnectWithAudit(ctx, id, expected, now, ev)
}

func (s *Service) record(ctx context.Context, a audit.Action, actor audit.Actor, c banking.Connection, details map[string]string) error {
	if err := s.Audit.Record(ctx, audit.Event{Action: a, Actor: actor, TargetType: "bank_connection", TargetID: string(c.ID), At: s.now(), Details: details}); err != nil {
		return fmt.Errorf("bankconnect: the action happened but its audit entry could not be written: %w", err)
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
	// RefreshRunning is set when a refresh requested now outlived the wait and
	// goes on in the background (a run still marked running also counts).
	RefreshRunning bool
	// RefreshAvailableAt is when a refresh on request is allowed again; zero
	// when it is allowed now (no cooldown, none running down, or a run is
	// still going, which the engine refuses by itself).
	RefreshAvailableAt time.Time
}

// cooldownEnd returns when the cooldown after run ends, and zero when run
// does not hold one back (still running, the cooldown is off, or the run
// finished before the connection's current consent was granted: reconnecting
// is a new start, C-B6c-reconnect).
func (s *Service) cooldownEnd(run *banking.RefreshRun, consentGrantedAt time.Time) time.Time {
	if s.RefreshCooldown <= 0 || run == nil || run.Outcome == banking.RefreshRunning || run.FinishedAt.IsZero() {
		return time.Time{}
	}
	if run.FinishedAt.Before(consentGrantedAt) {
		return time.Time{}
	}
	return run.FinishedAt.UTC().Add(s.RefreshCooldown)
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
	v.RefreshRunning = v.LastRun != nil && v.LastRun.Outcome == banking.RefreshRunning
	if end := s.cooldownEnd(v.LastRun, c.ConsentGrantedAt); end.After(v.AsOf) {
		v.RefreshAvailableAt = end
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

// expire ends consents that waited longer than the time to live, so a
// pending connection nobody completed shows as failed (consent_expired).
// It is housekeeping: a failure (a busy database, say) is logged and the read
// goes on, since the answer is still right (an unexpired view of a connection
// whose consent is past its time to live is corrected by the next sweep, and
// a completion checks the time to live itself).
func (s *Service) expire(ctx context.Context) {
	if _, err := s.Facts.ExpireConsents(ctx, s.now(), s.consentTTL()); err != nil {
		s.log().Warn("expiring waiting bank consents failed", "error", err.Error())
	}
}

// View returns one connection.
func (s *Service) View(ctx context.Context, id banking.ConnectionID) (View, error) {
	s.expire(ctx)
	c, err := s.Facts.Connection(ctx, id)
	if err != nil {
		return View{}, err
	}
	return s.view(ctx, c, s.scheduleMap())
}

// List returns every connection, oldest first.
func (s *Service) List(ctx context.Context) ([]View, error) {
	s.expire(ctx)
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
