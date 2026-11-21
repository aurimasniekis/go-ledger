package frankfurter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/fx"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
)

const (
	// Name is the adapter name stored with every rate.
	Name = "frankfurter"
	// DefaultBaseURL is the public Frankfurter service.
	DefaultBaseURL = "https://api.frankfurter.dev"
	// DefaultSource is the provider key requested by default: the European
	// Central Bank's reference rates.
	DefaultSource = "ECB"
	// Blended is the source name for Frankfurter's own blend of its
	// providers (no providers parameter is sent).
	Blended = "blended"
	// DefaultTimeout bounds one request.
	DefaultTimeout = 15 * time.Second

	maxBody = 1 << 20
	// maxRetryAfter caps a requested delay so a hostile or broken value
	// cannot silence the adapter for long.
	maxRetryAfter = 24 * time.Hour
)

var sourcePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,31}$`)

// Options configures a [Client].
type Options struct {
	// BaseURL defaults to [DefaultBaseURL]. It must be an absolute http(s)
	// URL; the caller (configuration) decides which schemes it accepts.
	BaseURL string
	// Source is the Frankfurter provider key to request (for example "ECB"),
	// or [Blended]. Defaults to [DefaultSource].
	Source string
	// Timeout bounds one request; defaults to [DefaultTimeout].
	Timeout time.Duration
	// HTTPClient defaults to a client without cookies or redirects to other
	// hosts; tests pass a client for their server.
	HTTPClient *http.Client
	// Now defaults to time.Now; it resolves a relative Retry-After.
	Now func() time.Time
}

// Client implements [fx.Provider] for Frankfurter. It is safe for
// concurrent use.
type Client struct {
	base    *url.URL
	source  string
	timeout time.Duration
	hc      *http.Client
	now     func() time.Time
}

var _ fx.Provider = (*Client)(nil)

// New returns a Client.
func New(o Options) (*Client, error) {
	if o.BaseURL == "" {
		o.BaseURL = DefaultBaseURL
	}
	u, err := url.Parse(o.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, errors.New("frankfurter: base URL must be an absolute http(s) URL without credentials, query or fragment")
	}
	if o.Source == "" {
		o.Source = DefaultSource
	}
	if !sourcePattern.MatchString(o.Source) {
		return nil, errors.New("frankfurter: source must be a provider key such as ECB, or \"blended\" (letters, digits, '_', '.', '-', at most 32)")
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Client{base: u, source: o.Source, timeout: o.Timeout, hc: o.HTTPClient, now: o.Now}, nil
}

// Name implements [fx.Provider].
func (c *Client) Name() string { return Name }

// Source implements [fx.Provider].
func (c *Client) Source() string { return c.source }

// Latest implements [fx.Provider].
func (c *Client) Latest(ctx context.Context, base money.Currency, quotes []money.Currency) ([]fx.Quote, error) {
	return c.get(ctx, "latest", base, quotes, calendar.Date{})
}

// OnDate implements [fx.Provider]. With a single provider key Frankfurter
// answers a weekend or holiday with the previous publication day; the
// blended route answers with the requested date.
func (c *Client) OnDate(ctx context.Context, base money.Currency, quotes []money.Currency, date calendar.Date) ([]fx.Quote, error) {
	if date.IsZero() {
		return nil, c.fail(fx.KindPermanent, "on_date", "no_date", 0, nil)
	}
	return c.get(ctx, "on_date", base, quotes, date)
}

type wireRate struct {
	Date  string      `json:"date"`
	Base  string      `json:"base"`
	Quote string      `json:"quote"`
	Rate  json.Number `json:"rate"`
}

func (c *Client) get(ctx context.Context, op string, base money.Currency, quotes []money.Currency, date calendar.Date) ([]fx.Quote, error) {
	if base.IsZero() || len(quotes) == 0 {
		return nil, c.fail(fx.KindPermanent, op, "bad_request", 0, nil)
	}
	codes := make([]string, 0, len(quotes))
	asked := make(map[string]bool, len(quotes))
	for _, q := range quotes {
		if q.IsZero() || q == base {
			return nil, c.fail(fx.KindPermanent, op, "bad_request", 0, nil)
		}
		if !asked[q.Code()] {
			asked[q.Code()] = true
			codes = append(codes, q.Code())
		}
	}
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + "/v2/rates"
	qs := url.Values{"base": {base.Code()}, "quotes": {strings.Join(codes, ",")}}
	if c.source != Blended {
		qs.Set("providers", c.source)
	}
	if !date.IsZero() {
		qs.Set("date", date.String())
	}
	u.RawQuery = qs.Encode()

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, c.fail(fx.KindPermanent, op, "bad_request", 0, nil)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ledger-fx")
	resp, err := c.hc.Do(req)
	if err != nil {
		// The error text is not kept: it holds the request URL.
		return nil, c.fail(fx.KindUnavailable, op, "network", 0, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, c.fail(fx.KindUnavailable, op, "read", 0, nil)
	}
	switch s := resp.StatusCode; {
	case s == http.StatusOK:
	case s == http.StatusTooManyRequests:
		return nil, c.fail(fx.KindRateLimited, op, "http_429", c.retryAfter(resp.Header.Get("Retry-After")), nil)
	case s == http.StatusRequestTimeout || s >= 500:
		return nil, c.fail(fx.KindUnavailable, op, "http_"+strconv.Itoa(s), c.retryAfter(resp.Header.Get("Retry-After")), nil)
	default:
		return nil, c.fail(fx.KindPermanent, op, "http_"+strconv.Itoa(s), 0, nil)
	}
	if len(body) > maxBody {
		return nil, c.fail(fx.KindPermanent, op, "response_too_large", 0, nil)
	}
	return c.parse(op, body, base, asked, date)
}

func (c *Client) parse(op string, body []byte, base money.Currency, asked map[string]bool, date calendar.Date) ([]fx.Quote, error) {
	var wire []wireRate
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&wire); err != nil {
		return nil, c.fail(fx.KindPermanent, op, "malformed_response", 0, nil)
	}
	if dec.More() {
		return nil, c.fail(fx.KindPermanent, op, "malformed_response", 0, nil)
	}
	out := make([]fx.Quote, 0, len(wire))
	seen := map[string]bool{}
	for _, w := range wire {
		d, err := calendar.ParseDate(w.Date)
		if err != nil {
			return nil, c.fail(fx.KindPermanent, op, "malformed_response", 0, nil)
		}
		if !date.IsZero() && d.After(date) {
			// Never a later date than asked: the answer is not trusted.
			return nil, c.fail(fx.KindPermanent, op, "date_after_requested", 0, nil)
		}
		b, berr := money.ParseCurrency(w.Base)
		q, qerr := money.ParseCurrency(w.Quote)
		if berr != nil || qerr != nil || b != base || !asked[q.Code()] || seen[q.Code()] {
			return nil, c.fail(fx.KindPermanent, op, "unexpected_pair", 0, nil)
		}
		seen[q.Code()] = true
		// json.Number keeps the literal; ParseDecimal rejects exponents, so a
		// rate is exactly the digits published or an error.
		v, err := money.ParseDecimal(w.Rate.String())
		if err != nil || v.Sign() <= 0 {
			return nil, c.fail(fx.KindPermanent, op, "invalid_rate", 0, nil)
		}
		out = append(out, fx.Quote{Source: c.source, Pair: fx.Pair{Base: b, Quote: q}, Value: v, Date: d})
	}
	return out, nil
}

// retryAfter reads Retry-After as seconds or an HTTP date; zero when absent
// or unusable.
func (c *Client) retryAfter(h string) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	var d time.Duration
	if n, err := strconv.ParseInt(h, 10, 64); err == nil {
		d = time.Duration(min(max(n, 0), int64(maxRetryAfter/time.Second))) * time.Second
	} else if t, err := http.ParseTime(h); err == nil {
		d = t.Sub(c.now())
	}
	return min(max(d, 0), maxRetryAfter)
}

func (c *Client) fail(kind fx.ErrorKind, op, code string, retry time.Duration, cause error) error {
	return &fx.ProviderError{Kind: kind, Provider: Name, Op: op, Code: code, RetryAfter: retry, Err: sanitized(cause)}
}

// sanitized keeps only the sentinel of a context error so errors.Is still
// works, and drops everything else (transport errors carry URLs).
func sanitized(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, context.Canceled):
		return context.Canceled
	}
	return nil
}
