// Package mcpserver is Ledger's Model Context Protocol server (task P-MCP1,
// ADR-0037). It serves the tools of [Tools] over streamable HTTP
// ([Handler], mounted at POST /api/v1/mcp) and over standard input and output
// ([Remote], "ledger mcp stdio").
//
// A tool is only a typed way to call a Management API operation: its arguments
// are validated against its JSON Schema, then run through the operation's own
// route, with the caller's bearer token. The route authenticates the token
// from current data, checks its grant, validates the input and audits a token
// write, so MCP can never do what the HTTP API would refuse. The package
// holds no business rules of its own.
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
)

// Options configures [NewServer].
type Options struct {
	// Caller runs the operations.
	Caller Caller
	// Audit records state-changing tool calls (mcp.tool_called) with the token
	// identity. It is nil only where the server behind Caller records them
	// itself (the stdio mode, which has no principal of its own).
	Audit audit.Recorder
	// Now defaults to time.Now.
	Now func() time.Time
	// Version is reported as the server version.
	Version string
	// Log receives server diagnostics; nil discards them. It never receives
	// arguments or results.
	Log *slog.Logger
}

type server struct {
	o     Options
	tools map[string]*boundTool
}

type boundTool struct {
	Tool
	schema *jsonschema.Resolved
}

// Resource is a read-only context document: a GET operation that needs no
// grant beyond a valid token.
type resourceDef struct {
	URI, Name, Title, Description, Path string
}

var resources = []resourceDef{
	{"ledger://me", "me", "Who this token acts as", "The identity behind the token: the owner, their role and the grants the token holds now. Nothing secret.", "/me"},
	{"ledger://grants", "grants", "Grant vocabulary", "Every grant (resource:action) and role of this Ledger, so a missing grant can be named.", "/grants"},
	{"ledger://openapi", "openapi", "Management API description", "The OpenAPI document of the Management API that every tool maps to.", "/openapi.json"},
}

// NewServer builds the MCP server with every tool and resource.
func NewServer(o Options) (*mcp.Server, error) {
	if o.Caller == nil {
		return nil, fmt.Errorf("mcpserver: no caller")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	s := &server{o: o, tools: map[string]*boundTool{}}
	srv := mcp.NewServer(&mcp.Implementation{Name: "ledger", Title: "Ledger", Version: o.Version}, &mcp.ServerOptions{
		Instructions: "Ledger tracks recurring commitments, their monthly occurrences and payment state. " +
			"Amounts are exact decimal strings with a currency; currencies are never added together. " +
			"The monthly view describes recorded commitments and recorded payments, not money that is safe to spend. " +
			"A tool needs the grant it names; an error says which one is missing. Read the ledger://me resource for this token's grants.",
		Logger: o.Log,
	})
	for _, t := range registry() {
		raw, err := json.Marshal(t.Schema)
		if err != nil {
			return nil, err
		}
		var sc jsonschema.Schema
		if err := json.Unmarshal(raw, &sc); err != nil {
			return nil, fmt.Errorf("mcpserver: schema of %s: %w", t.Name, err)
		}
		res, err := sc.Resolve(nil)
		if err != nil {
			return nil, fmt.Errorf("mcpserver: schema of %s: %w", t.Name, err)
		}
		bt := &boundTool{Tool: t, schema: res}
		s.tools[t.Name] = bt
		no := false
		srv.AddTool(&mcp.Tool{
			Name:        t.Name,
			Title:       t.Title,
			Description: t.Description + " Needs the " + t.Grant.String() + " grant.",
			InputSchema: &sc,
			Meta:        mcp.Meta{"ledger/grant": t.Grant.String(), "ledger/kind": t.Kind.String()},
			Annotations: &mcp.ToolAnnotations{
				Title:           t.Title,
				ReadOnlyHint:    t.Kind == KindRead,
				DestructiveHint: boolPtr(t.Kind == KindDestructive),
				IdempotentHint:  t.Kind == KindRead || t.Kind == KindDestructive,
				OpenWorldHint:   &no,
			},
		}, s.toolHandler(bt))
	}
	for _, r := range resources {
		srv.AddResource(&mcp.Resource{URI: r.URI, Name: r.Name, Title: r.Title, Description: r.Description, MIMEType: "application/json"}, s.resourceHandler(r))
	}
	return srv, nil
}

func boolPtr(b bool) *bool { return &b }

func (s *server) toolHandler(t *boundTool) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		outcome := "failed"
		var p authz.Principal
		var havePrincipal bool
		if t.Kind != KindRead && s.o.Audit != nil {
			// Every state-changing call is audited, whatever its outcome. The
			// write itself is additionally recorded as token.write_used by the
			// route before it runs, so a write is never unaudited.
			defer func() { s.audit(ctx, t.Name, outcome) }()
		}
		p, havePrincipal = authz.FromContext(ctx)

		args, bad := s.parse(t, req.Params.Arguments)
		if bad != nil {
			return bad, nil
		}
		// The grant is checked here first, for a clear error and so a denied
		// call touches nothing; the route checks it again.
		if havePrincipal && !p.Allows(t.Grant) {
			outcome = "denied"
			return denied(t.Name, t.Grant), nil
		}
		r, err := buildRequest(t.Tool, args)
		if err != nil {
			return errorResult("invalid_argument", err.Error(), nil), nil
		}
		resp, err := s.o.Caller.Call(ctx, headerOf(req), r)
		if err != nil {
			s.o.Log.Warn("mcp tool call could not run", "tool", t.Name, "error", err.Error())
			return errorResult("unavailable", "Ledger could not be reached or answered badly: "+err.Error(), nil), nil
		}
		res := s.result(t, resp)
		if !res.IsError {
			outcome = "succeeded"
		} else if resp.Status == http.StatusForbidden {
			outcome = "denied"
		}
		return res, nil
	}
}

func headerOf(req *mcp.CallToolRequest) http.Header {
	if x := req.GetExtra(); x != nil {
		return x.Header
	}
	return nil
}

func (s *server) audit(ctx context.Context, tool, outcome string) {
	p, ok := authz.FromContext(ctx)
	if !ok {
		s.o.Log.Error("mcp write call without a principal was not audited", "tool", tool)
		return
	}
	// The caller may have gone away: the entry is still written.
	if err := s.o.Audit.Record(context.WithoutCancel(ctx), audit.MCPCall(audit.ActorOf(p), tool, outcome, s.o.Now().UTC())); err != nil {
		s.o.Log.Error("cannot record the audit entry of an MCP tool call", "tool", tool, "error", err.Error())
	}
}

// parse decodes and validates the arguments against the tool's schema.
func (s *server) parse(t *boundTool, raw json.RawMessage) (map[string]any, *mcp.CallToolResult) {
	args := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		dec := json.NewDecoder(bytes.NewReader(raw))
		if err := dec.Decode(&args); err != nil || dec.More() {
			return nil, errorResult("invalid_argument", "the arguments must be one JSON object", nil)
		}
	}
	if err := t.schema.Validate(args); err != nil {
		return nil, errorResult("invalid_argument", "invalid arguments: "+err.Error(), nil)
	}
	return args, nil
}

// buildRequest maps validated arguments onto the tool's operation: members
// named in the path fill it, "version" becomes If-Match, and the rest is the
// query of a read or the JSON body of a write.
func buildRequest(t Tool, args map[string]any) (Request, error) {
	rest := map[string]any{}
	for k, v := range args {
		rest[k] = v
	}
	path := t.Path
	for {
		i := strings.Index(path, "{")
		if i < 0 {
			break
		}
		j := strings.Index(path[i:], "}")
		if j < 0 {
			return Request{}, fmt.Errorf("tool %s has a malformed path", t.Name)
		}
		name := path[i+1 : i+j]
		v, ok := rest[name].(string)
		if !ok || v == "" {
			return Request{}, fmt.Errorf("%s is required", name)
		}
		delete(rest, name)
		path = path[:i] + url.PathEscape(v) + path[i+j+1:]
	}
	r := Request{Method: t.Method, Path: path}
	if v, ok := rest["version"]; ok {
		delete(rest, "version")
		n, ok := v.(float64)
		if !ok {
			return Request{}, fmt.Errorf("version must be an integer")
		}
		r.IfMatch = strconv.Quote(strconv.FormatInt(int64(n), 10))
	}
	if t.Method == http.MethodGet || t.Method == http.MethodDelete {
		q := url.Values{}
		for k, v := range rest {
			switch x := v.(type) {
			case []any:
				for _, e := range x {
					q.Add(k, scalar(e))
				}
			default:
				q.Set(k, scalar(x))
			}
		}
		r.Query = q
		return r, nil
	}
	body, err := json.Marshal(rest)
	if err != nil {
		return Request{}, err
	}
	r.Body = body
	return r, nil
}

func scalar(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

// result turns an API answer into a tool result: a 2xx body is the structured
// result; anything else is a tool error that carries the API's own error body.
func (s *server) result(t *boundTool, resp Response) *mcp.CallToolResult {
	if resp.Status >= 200 && resp.Status <= 299 {
		if len(bytes.TrimSpace(resp.Body)) == 0 {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("Done (HTTP %d, no content).", resp.Status)}}}
		}
		res := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(resp.Body)}}}
		var probe map[string]any
		if json.Unmarshal(resp.Body, &probe) == nil {
			res.StructuredContent = json.RawMessage(resp.Body)
		}
		return res
	}
	var env struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(resp.Body, &env)
	switch resp.Status {
	case http.StatusForbidden:
		msg := env.Message
		if msg == "" {
			msg = "forbidden"
		}
		return errorResult(codeOr(env.Code, "forbidden"), "permission denied for "+t.Name+": "+msg+" (this tool needs the "+t.Grant.String()+" grant)", json.RawMessage(resp.Body))
	case http.StatusUnauthorized:
		return errorResult("unauthenticated", "the API token was refused: it is invalid, expired, revoked or its owner is disabled", nil)
	}
	msg := env.Message
	if msg == "" {
		msg = http.StatusText(resp.Status)
	}
	return errorResult(codeOr(env.Code, "error"), fmt.Sprintf("%s failed (HTTP %d, %s): %s", t.Name, resp.Status, codeOr(env.Code, "error"), msg), json.RawMessage(resp.Body))
}

func codeOr(c, d string) string {
	if c == "" {
		return d
	}
	return c
}

func denied(tool string, g authz.Grant) *mcp.CallToolResult {
	return errorResult("forbidden", "permission denied for "+tool+": this token does not hold the "+g.String()+" grant (a write grant also allows reading the same resource). Ask for a token with that grant; read ledger://me to see what this token holds.",
		map[string]any{"code": "forbidden", "required_grant": g.String()})
}

// errorResult is a tool error: visible to the model, never a protocol error.
func errorResult(code, msg string, detail any) *mcp.CallToolResult {
	res := &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: msg}}}
	body := map[string]any{"code": code, "message": msg}
	if detail != nil {
		body["detail"] = detail
	}
	res.StructuredContent = map[string]any{"error": body}
	return res
}

func (s *server) resourceHandler(r resourceDef) mcp.ResourceHandler {
	return func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		var hdr http.Header
		if x := req.GetExtra(); x != nil {
			hdr = x.Header
		}
		resp, err := s.o.Caller.Call(ctx, hdr, Request{Method: http.MethodGet, Path: r.Path})
		if err != nil {
			return nil, fmt.Errorf("could not reach Ledger or it answered badly: %w", err)
		}
		if resp.Status < 200 || resp.Status > 299 {
			return nil, fmt.Errorf("reading %s failed (HTTP %d)", r.URI, resp.Status)
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: r.URI, MIMEType: "application/json", Text: string(resp.Body)}}}, nil
	}
}
