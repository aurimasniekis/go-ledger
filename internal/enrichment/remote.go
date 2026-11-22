package enrichment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/merchants"
)

// SourceRemote is the source token of the remote adapter's answers.
const SourceRemote = "remote"

// Licence is what the remote service's licence lets Ledger do with its answers.
// The zero value is the most restrictive: no logo reference kept, no attribution
// to show, answers kept until replaced. The operator states it in configuration
// (ADR-0053); Ledger cannot verify it.
type Licence struct {
	// Attribution is shown with identities that came from the service.
	Attribution string
	// StoreLogoRef keeps the logo reference the service returned. Ledger never
	// downloads or copies a logo either way.
	StoreLogoRef bool
	// Retention is how long an answer may be kept; zero keeps it until replaced.
	Retention time.Duration
}

// RemoteOptions configures the remote adapter.
type RemoteOptions struct {
	URL string
	// APIKey is sent as a bearer token to URL only; never logged.
	APIKey string
	// Country is an optional ISO country hint; "" sends none (the field policy).
	Country string
	Timeout time.Duration
	Licence Licence
	// HTTPClient is for tests; the default never follows redirects.
	HTTPClient *http.Client
}

// Remote is the provider-neutral remote merchant source. It is only built when
// the operator enables it (D15: off by default, none approved). The request
// carries the field policy's allowlist and nothing else: the cleaned merchant
// string and, if set, the country. Amounts, dates, account data and
// identifiers have no field in the request type.
type Remote struct {
	opts   RemoteOptions
	client *http.Client
}

// RemoteQuery is everything that ever leaves the machine. Adding a field here is
// a privacy decision: it needs an ADR and a documentation change.
type RemoteQuery struct {
	Merchant string `json:"merchant"`
	Country  string `json:"country,omitempty"`
}

type remoteReply struct {
	Status     merchants.Status `json:"status"`
	Name       string           `json:"name"`
	LogoURL    string           `json:"logo_url"`
	Confidence *int             `json:"confidence_permille"`
	Candidates []string         `json:"candidates"`
}

const maxReply = 64 << 10

// NewRemote validates the options and returns the adapter.
func NewRemote(o RemoteOptions) (*Remote, error) {
	u, err := url.Parse(o.URL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("enrichment: the remote URL must be an http(s) URL without credentials or query")
	}
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	c := o.HTTPClient
	if c == nil {
		// Never follow a redirect: the request would go somewhere the operator
		// did not configure, with the key.
		c = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &Remote{opts: o, client: c}, nil
}

// Name implements [Source].
func (*Remote) Name() string { return SourceRemote }

// Licence returns the licence the adapter applies.
func (r *Remote) Licence() Licence { return r.opts.Licence }

// Query builds what would be sent for a subject.
func (r *Remote) Query(s Subject) RemoteQuery {
	return RemoteQuery{Merchant: s.MerchantString(), Country: r.opts.Country}
}

// Lookup implements [Source]. Every failure is an error without bank text,
// the key or the response body in it.
func (r *Remote) Lookup(ctx context.Context, s Subject) (merchants.Answer, error) {
	q := r.Query(s)
	if q.Merchant == "" {
		return merchants.Answer{Status: merchants.StatusNone, Source: SourceRemote}, nil
	}
	body, err := json.Marshal(q)
	if err != nil {
		return merchants.Answer{}, errors.New("enrichment: remote request could not be built")
	}
	ctx, cancel := context.WithTimeout(ctx, r.opts.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.opts.URL, bytes.NewReader(body))
	if err != nil {
		return merchants.Answer{}, errors.New("enrichment: remote request could not be built")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ledger-merchant-lookup")
	if r.opts.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.opts.APIKey)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		// err can quote the URL; the URL has no credentials or query, but keep
		// the message generic anyway.
		return merchants.Answer{}, errors.New("enrichment: the remote source did not answer")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return merchants.Answer{}, fmt.Errorf("enrichment: the remote source answered HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxReply+1))
	if err != nil || len(raw) > maxReply {
		return merchants.Answer{}, errors.New("enrichment: the remote answer was unreadable or too large")
	}
	var rep remoteReply
	if err := json.Unmarshal(raw, &rep); err != nil {
		return merchants.Answer{}, errors.New("enrichment: the remote answer was not valid JSON")
	}
	a := merchants.Answer{Status: rep.Status, Source: SourceRemote}
	switch rep.Status {
	case merchants.StatusIdentified:
		a.Name = strings.Join(strings.Fields(rep.Name), " ")
		if rep.Confidence == nil {
			return merchants.Answer{}, errors.New("enrichment: the remote answer has no confidence")
		}
		a.Confidence = merchants.Confidence(*rep.Confidence)
		a.LogoRef = r.logoRef(rep.LogoURL)
	case merchants.StatusAmbiguous:
		for _, c := range rep.Candidates {
			a.Candidates = append(a.Candidates, strings.Join(strings.Fields(c), " "))
		}
		a.Candidates = merchants.Candidates(a.Candidates)
	case merchants.StatusNone:
	default:
		return merchants.Answer{}, errors.New("enrichment: the remote answer has an unknown status")
	}
	if errs := a.Validate(); len(errs) > 0 {
		return merchants.Answer{}, errors.New("enrichment: the remote answer is not acceptable (" + errs[0].Field + ")")
	}
	return a, nil
}

// logoRef keeps the logo reference only where the licence allows it and it is
// an https URL.
func (r *Remote) logoRef(raw string) string {
	if !r.opts.Licence.StoreLogoRef || len(raw) > merchants.MaxLogoRef {
		return ""
	}
	if u, err := url.Parse(raw); err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return ""
	}
	return raw
}
