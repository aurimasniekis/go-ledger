package mcpserver

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/aurimasniekis/go-ledger/internal/httpapi"
	"github.com/aurimasniekis/go-ledger/pkg/ledgerclient"
)

// Request is one call of a Management API operation. Path is relative to
// /api/v1.
type Request struct {
	Method  string
	Path    string
	Query   url.Values
	Body    []byte
	IfMatch string
	// Tool is the MCP tool the call is for. A remote caller names it to the
	// server in httpapi.MCPToolHeader, for the audit log.
	Tool string
}

// Response is the answer of an operation, whatever its status.
type Response struct {
	Status int
	Body   []byte
	// Location is the Location header of the answer (the created resource).
	Location string
	// Truncated reports that the body was too large to keep and is empty: the
	// operation ran and Status is its real status.
	Truncated bool
}

// Caller runs an API operation as the caller of the MCP request. Both
// implementations go through the API's own routes, so validation, grants and
// the audit of token writes are those of a direct HTTP call (ADR-0038).
type Caller interface {
	// Call runs r. hdr holds the headers of the MCP request, from which an
	// in-process caller takes the bearer token; a remote caller has its own.
	Call(ctx context.Context, hdr http.Header, r Request) (Response, error)
}

type remoteAddrKey struct{}

// WithRemoteAddr returns ctx carrying the client address of the MCP request,
// which an in-process call reuses as its own.
func WithRemoteAddr(ctx context.Context, addr string) context.Context {
	return context.WithValue(ctx, remoteAddrKey{}, addr)
}

const maxInProcessResponse = 8 << 20

// InProcess returns a Caller that serves operations with h, the server's own
// handler (the full handler, so request limits and logging apply). The bearer
// token of the MCP request is forwarded, so the route authenticates it from
// current data and revocation applies at once.
func InProcess(h http.Handler) Caller { return inProcess{h} }

type inProcess struct{ h http.Handler }

func (c inProcess) Call(ctx context.Context, hdr http.Header, r Request) (Response, error) {
	u := "http://ledger.invalid" + ledgerclient.APIPath + r.Path
	if len(r.Query) > 0 {
		u += "?" + r.Query.Encode()
	}
	var body *bytes.Reader
	if r.Body != nil {
		body = bytes.NewReader(r.Body)
	} else {
		body = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(httpapi.WithMCPCall(ctx), r.Method, u, body)
	if err != nil {
		return Response{}, err
	}
	if v := hdr.Values("Authorization"); len(v) > 0 {
		req.Header["Authorization"] = append([]string(nil), v...)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ledger-mcp")
	if r.Body != nil {
		ct := "application/json"
		if r.Method == http.MethodPatch {
			ct = "application/merge-patch+json"
		}
		req.Header.Set("Content-Type", ct)
		req.ContentLength = int64(len(r.Body))
	}
	if r.IfMatch != "" {
		req.Header.Set("If-Match", r.IfMatch)
	}
	req.RemoteAddr = "127.0.0.1:0"
	if a, ok := ctx.Value(remoteAddrKey{}).(string); ok && a != "" {
		req.RemoteAddr = a
	}
	rec := &recorder{header: http.Header{}, status: http.StatusOK}
	c.h.ServeHTTP(rec, req)
	// A body that is too large is not an error: the operation has already
	// run, and a successful write must not be reported as failed.
	if rec.tooBig {
		return Response{Status: rec.status, Location: rec.header.Get("Location"), Truncated: true}, nil
	}
	return Response{Status: rec.status, Body: rec.buf.Bytes(), Location: rec.header.Get("Location")}, nil
}

// recorder is a minimal http.ResponseWriter that keeps a bounded body.
type recorder struct {
	header http.Header
	status int
	wrote  bool
	buf    bytes.Buffer
	tooBig bool
}

func (r *recorder) Header() http.Header { return r.header }

func (r *recorder) WriteHeader(code int) {
	if !r.wrote {
		r.wrote, r.status = true, code
	}
}

func (r *recorder) Write(p []byte) (int, error) {
	r.WriteHeader(http.StatusOK)
	if r.buf.Len()+len(p) > maxInProcessResponse {
		r.tooBig = true
		return len(p), nil
	}
	return r.buf.Write(p)
}

// Remote returns a Caller that calls a running Ledger server with an API
// token (the stdio mode, ADR-0038). Every call is authenticated and
// authorised by that server.
func Remote(c *ledgerclient.Client) Caller { return remote{c} }

type remote struct{ c *ledgerclient.Client }

func (c remote) Call(ctx context.Context, _ http.Header, r Request) (Response, error) {
	var extra http.Header
	if r.Tool != "" && r.Method != http.MethodGet {
		extra = http.Header{httpapi.MCPToolHeader: {r.Tool}}
	}
	resp, err := c.c.RawHeader(ctx, r.Method, r.Path, r.Query, r.Body, r.IfMatch, extra)
	if err != nil {
		return Response{}, errors.New(strings.TrimPrefix(err.Error(), "ledgerclient: "))
	}
	// A too-large answer is not a failure either: the call completed (a write
	// has happened), so it is reported as truncated, never as unavailable.
	return Response{Status: resp.Status, Body: resp.Body, Location: resp.Location, Truncated: resp.Truncated}, nil
}
