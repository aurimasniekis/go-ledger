package enablebanking

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
)

// Operation names used in errors.
const (
	opApplication     = "application"
	opInstitutions    = "institutions"
	opStartConsent    = "start_consent"
	opCompleteConsent = "complete_consent"
	opAccounts        = "accounts"
	opAccountDetails  = "account_details"
	opBalances        = "balances"
	opTransactions    = "transactions"
	opEndSession      = "end_session"
)

// Sanitized causes. Wrapped errors never hold URLs, bodies or key material
// (a *url.Error would print the request URL, which holds account uids and
// session ids).
var (
	errSigning         = errors.New("enablebanking: signing the request failed")
	errTransport       = errors.New("enablebanking: transport failure")
	errTimeout         = timeoutError{}
	errInvalidResponse = errors.New("enablebanking: response could not be decoded")
	errTooLarge        = errors.New("enablebanking: response too large")
)

type timeoutError struct{}

func (timeoutError) Error() string   { return "enablebanking: request timed out" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

var _ net.Error = timeoutError{}

// errorResponse is Enable Banking's ErrorResponse schema.
type errorResponse struct {
	Message string `json:"message"`
	Code    int    `json:"code"`
	Error   string `json:"error"`
}

// Enable Banking error codes the adapter classifies specially (OpenAPI
// ErrorCode enum, spec 1.0.0-1c03cf81).
const (
	codeExpiredSession     = "EXPIRED_SESSION"
	codeRevokedSession     = "REVOKED_SESSION"
	codeClosedSession      = "CLOSED_SESSION"
	codeSessionNotExist    = "SESSION_DOES_NOT_EXIST"
	codeWrongSessionStatus = "WRONG_SESSION_STATUS"
	codeRateLimit          = "ASPSP_RATE_LIMIT_EXCEEDED"
	codeASPSPTimeout       = "ASPSP_TIMEOUT"
	codeASPSPError         = "ASPSP_ERROR"
	codePSUActionRequired  = "ASPSP_PSU_ACTION_REQUIRED"
)

// Codes Ledger reports itself, for failures that have no Enable Banking code.
const (
	codeHTTPPrefix          = "http_"
	codeInvalidResponse     = "invalid_response"
	codeConsentRefused      = "consent_refused"
	codeEnvironmentMismatch = "environment_mismatch"
	codeApplicationMismatch = "application_mismatch"
	codeApplicationInactive = "application_inactive"
	codeSessionStatusPrefix = "session_"
)

type client struct {
	base     *url.URL
	http     *http.Client
	sign     *signer
	maxBytes int64
}

func (p *Provider) newError(op string, kind banking.ErrorKind, reason banking.Reason, code string, cause error) *banking.Error {
	return &banking.Error{Kind: kind, Reason: reason, Provider: ProviderName, Op: op, Code: code, Err: cause}
}

// do sends one request and decodes a 2xx JSON body into out (if non-nil).
// Every failure is a classified *banking.Error.
func (p *Provider) do(ctx context.Context, op, method, path string, query url.Values, body, out any) error {
	c := p.client
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = query.Encode()

	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return p.newError(op, banking.KindPermanent, banking.ReasonNone, "", errors.New("enablebanking: request could not be encoded"))
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return p.newError(op, banking.KindPermanent, banking.ReasonNone, "", errors.New("enablebanking: request could not be built"))
	}
	tok, err := c.sign.bearer()
	if err != nil {
		return p.newError(op, banking.KindPermanent, banking.ReasonNone, "", errSigning)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return p.transportError(ctx, op, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBytes+1))
	if err != nil {
		return p.transportError(ctx, op, err)
	}
	if int64(len(data)) > c.maxBytes {
		return p.newError(op, banking.KindPermanent, banking.ReasonNone, codeInvalidResponse, errTooLarge)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return p.statusError(op, resp, data)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return p.newError(op, banking.KindPermanent, banking.ReasonNone, codeInvalidResponse, errInvalidResponse)
	}
	return nil
}

func (p *Provider) transportError(ctx context.Context, op string, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		if errors.Is(cerr, context.Canceled) {
			return p.newError(op, banking.KindCanceled, banking.ReasonNone, "", context.Canceled)
		}
		return p.newError(op, banking.KindRetryable, banking.ReasonNone, "", context.DeadlineExceeded)
	}
	if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
		return p.newError(op, banking.KindRetryable, banking.ReasonNone, "", errTimeout)
	}
	return p.newError(op, banking.KindRetryable, banking.ReasonNone, "", errTransport)
}

// statusError classifies a non-2xx response by Enable Banking's error code
// first (a 401 has several causes, R1 section 4), then by HTTP status. The
// response message and detail are never kept: they may echo request data.
func (p *Provider) statusError(op string, resp *http.Response, data []byte) error {
	var er errorResponse
	_ = json.Unmarshal(data, &er)
	code := sanitizeCode(er.Error)
	cause := errors.New("enablebanking: HTTP status " + strconv.Itoa(resp.StatusCode))
	reconnect := func(r banking.Reason) error {
		return p.newError(op, banking.KindReconnectRequired, r, code, cause)
	}
	switch code {
	case codeExpiredSession:
		return reconnect(banking.ReasonConsentExpired)
	case codeRevokedSession:
		return reconnect(banking.ReasonConsentRevoked)
	case codeClosedSession, codeSessionNotExist, codeWrongSessionStatus, codePSUActionRequired:
		return reconnect(banking.ReasonSessionInvalid)
	case codeRateLimit:
		return p.rateLimited(op, resp, code, cause)
	case codeASPSPTimeout, codeASPSPError:
		e := p.newError(op, banking.KindRetryable, banking.ReasonNone, code, cause)
		e.RetryAfter, _ = retryAfter(resp.Header.Get("Retry-After"), p.now())
		return e
	}
	if code == "" {
		code = codeHTTPPrefix + strconv.Itoa(resp.StatusCode)
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return p.rateLimited(op, resp, code, cause)
	case resp.StatusCode == http.StatusRequestTimeout, resp.StatusCode >= 500:
		e := p.newError(op, banking.KindRetryable, banking.ReasonNone, code, cause)
		e.RetryAfter, _ = retryAfter(resp.Header.Get("Retry-After"), p.now())
		return e
	}
	return p.newError(op, banking.KindPermanent, banking.ReasonNone, code, cause)
}

func (p *Provider) rateLimited(op string, resp *http.Response, code string, cause error) error {
	e := p.newError(op, banking.KindRateLimited, banking.ReasonNone, code, cause)
	if d, ok := retryAfter(resp.Header.Get("Retry-After"), p.now()); ok {
		e.RetryAfter = d
	} else if code == codeRateLimit {
		e.RetryAfter = RateLimitBackoff
	}
	return e
}

// retryAfter parses a Retry-After header: delay seconds or an HTTP date.
func retryAfter(h string, now time.Time) (time.Duration, bool) {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0, false
	}
	if n, err := strconv.ParseInt(h, 10, 64); err == nil {
		if n <= 0 || n > int64(7*24*time.Hour/time.Second) {
			return 0, false
		}
		return time.Duration(n) * time.Second, true
	}
	if t, err := http.ParseTime(h); err == nil && t.After(now) {
		return t.Sub(now), true
	}
	return 0, false
}

// sanitizeCode keeps an error code only if it looks like one (upper-case
// letters, digits and underscores, at most 64 characters), so a malformed
// response cannot smuggle data into an error message.
func sanitizeCode(s string) string {
	if s == "" || len(s) > 64 {
		return ""
	}
	for _, r := range s {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
			return ""
		}
	}
	return s
}
