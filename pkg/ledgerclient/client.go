package ledgerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// APIPath is the base path of version 1 of the API.
const APIPath = "/api/v1"

const maxResponseBytes = 8 << 20

// Client calls the Management API with a bearer token. It is safe for
// concurrent use.
type Client struct {
	base  string // scheme://host[:port], no trailing slash
	token string
	hc    *http.Client
	ua    string
}

// Option configures New.
type Option func(*settings)

type settings struct {
	hc       *http.Client
	insecure bool
	timeout  time.Duration
	ua       string
}

// WithHTTPClient replaces the HTTP client (for example to set a transport or
// a CA pool). Its redirect policy is replaced: the client never follows
// redirects, so the token cannot be sent anywhere unintended. If its Timeout is
// zero, the WithTimeout value is applied to it; a non-zero Timeout is kept.
func WithHTTPClient(hc *http.Client) Option { return func(s *settings) { s.hc = hc } }

// WithTimeout sets the per-request timeout (default 30 seconds) of the default
// HTTP client, and of a WithHTTPClient client whose Timeout is zero.
func WithTimeout(d time.Duration) Option { return func(s *settings) { s.timeout = d } }

// WithInsecureHTTP allows a plain http:// URL to a host that is not a
// loopback address. Without it New refuses, because the token would cross the
// network unencrypted.
func WithInsecureHTTP() Option { return func(s *settings) { s.insecure = true } }

// WithUserAgent sets the User-Agent header.
func WithUserAgent(ua string) Option { return func(s *settings) { s.ua = ua } }

// New returns a client for the Ledger instance at baseURL (for example
// "https://ledger.example.com" or "http://127.0.0.1:8080"; a trailing
// "/api/v1" is accepted). token is an API token sent as "Authorization:
// Bearer"; it is never included in an error or log line by this package.
func New(baseURL, token string, opts ...Option) (*Client, error) {
	s := settings{timeout: 30 * time.Second, ua: "ledgerclient"}
	for _, o := range opts {
		o(&s)
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("ledgerclient: no token")
	}
	if strings.ContainsAny(token, "\r\n ") {
		return nil, errors.New("ledgerclient: the token contains whitespace")
	}
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("ledgerclient: the URL must be http:// or https:// with a host")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("ledgerclient: the URL must not hold credentials, a query or a fragment")
	}
	path := strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), APIPath)
	if path != "" {
		return nil, errors.New("ledgerclient: the URL must not have a path other than " + APIPath)
	}
	if u.Scheme == "http" && !s.insecure && !isLoopback(u.Hostname()) {
		return nil, errors.New("ledgerclient: refusing to send a token over plain http to a non-loopback host; use https")
	}
	hc := s.hc
	if hc == nil {
		hc = &http.Client{Timeout: s.timeout}
	} else {
		cp := *hc
		if cp.Timeout == 0 {
			cp.Timeout = s.timeout
		}
		hc = &cp
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{base: u.Scheme + "://" + u.Host, token: token, hc: hc, ua: s.ua}, nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// request describes one call of an operation.
type request struct {
	op      operation
	pathID  string
	pathID2 string // the second path parameter, {payment_id}
	query   url.Values
	body    any
	version int64 // If-Match when > 0
}

// do performs req and decodes a 2xx JSON answer into out (nil: ignore it).
func (c *Client) do(ctx context.Context, req request, out any) error {
	path := req.op.Path
	if req.pathID != "" {
		path = strings.Replace(path, "{id}", url.PathEscape(req.pathID), 1)
	}
	if req.pathID2 != "" {
		path = strings.Replace(path, "{payment_id}", url.PathEscape(req.pathID2), 1)
	}
	u := c.base + APIPath + path
	if len(req.query) > 0 {
		u += "?" + req.query.Encode()
	}
	var body io.Reader
	if req.body != nil {
		b, err := json.Marshal(req.body)
		if err != nil {
			return fmt.Errorf("ledgerclient: encode request: %w", err)
		}
		body = bytes.NewReader(b)
	}
	hr, err := http.NewRequestWithContext(ctx, req.op.Method, u, body)
	if err != nil {
		return fmt.Errorf("ledgerclient: build request: %w", err)
	}
	hr.Header.Set("Authorization", "Bearer "+c.token)
	hr.Header.Set("Accept", "application/json")
	hr.Header.Set("User-Agent", c.ua)
	if req.body != nil {
		ct := "application/json"
		if req.op.Method == http.MethodPatch {
			ct = "application/merge-patch+json"
		}
		hr.Header.Set("Content-Type", ct)
	}
	if req.version > 0 {
		hr.Header.Set("If-Match", strconv.Quote(strconv.FormatInt(req.version, 10)))
	}
	resp, err := c.hc.Do(hr)
	if err != nil {
		// net/http errors name the URL, never the headers.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("ledgerclient: %s %s: %w", req.op.Method, APIPath+path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("ledgerclient: read response: %w", err)
	}
	if len(data) > maxResponseBytes {
		return errors.New("ledgerclient: response is too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return decodeError(resp.StatusCode, data)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("ledgerclient: decode response: %w", err)
	}
	return nil
}

func decodeError(status int, data []byte) *APIError {
	e := &APIError{Status: status}
	if json.Unmarshal(data, e) != nil || e.Code == "" {
		// Not the API's envelope (a proxy, a crashed server): keep the status.
		*e = APIError{Status: status, Message: http.StatusText(status)}
	}
	return e
}
