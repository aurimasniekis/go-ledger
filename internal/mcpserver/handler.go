package mcpserver

import (
	"bufio"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/httpapi"
)

// Path is the endpoint, relative to the server root.
const Path = httpapi.MCPPath

// HandlerOptions configures [Handler].
type HandlerOptions struct {
	// Limiter limits requests per API token; nil means no limit.
	Limiter *Limiter
	// MaxRequestBytes limits one request body (0: the SDK default, 4 MiB).
	MaxRequestBytes int64
}

// Handler serves srv over streamable HTTP. Mount it with
// httpapi.TokenOnly(): the route has already authenticated an API token and put
// its principal in the context.
//
// The transport is stateless with plain JSON answers: each request stands on
// its own, so there is no session to outlive a revoked token, and a client
// needs no stream. GET and DELETE are 405.
//
// The SDK's DNS-rebinding check (it rejects a loopback connection that carries
// another Host, which every reverse proxy does) is off: a token is required on
// every request, and a page in a browser has none. Cross-site browser requests
// are refused (Sec-Fetch-Site and Origin).
func Handler(srv *mcp.Server, o HandlerOptions) http.Handler {
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{
		Stateless:                  true,
		JSONResponse:               true,
		DisableLocalhostProtection: true,
		MaxRequestBodyBytes:        o.MaxRequestBytes,
	})
	var next http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := authz.FromContext(r.Context())
		if !ok || p.Kind != authz.PrincipalToken {
			httpapi.WriteError(w, r, httpapi.Unauthenticated("authentication required"))
			return
		}
		if o.Limiter != nil {
			if ok, wait := o.Limiter.Allow(p.TokenID); !ok {
				secs := int(wait/time.Second) + 1
				w.Header().Set("Retry-After", strconv.Itoa(secs))
				httpapi.WriteError(w, r, httpapi.RateLimited("too many MCP requests for this token; retry in "+strconv.Itoa(secs)+" seconds"))
				return
			}
		}
		// JSON-RPC batches are refused: one HTTP request would carry many tool
		// calls and so get around the per-token rate limit. MCP 2025-06-18
		// removed batching; the SDK still accepts a batch from a client that
		// sends no protocol version, so the first byte is checked here.
		br := bufio.NewReader(r.Body)
		if first, err := firstNonSpace(br); err == nil && first == '[' {
			httpapi.WriteError(w, r, httpapi.Validation("", "JSON-RPC batches are not supported; send one message per request"))
			return
		}
		r.Body = struct {
			io.Reader
			io.Closer
		}{br, r.Body}
		h.ServeHTTP(w, r.WithContext(WithRemoteAddr(r.Context(), r.RemoteAddr)))
	})
	return http.NewCrossOriginProtection().Handler(next)
}

// firstNonSpace returns the first byte of br that is not JSON whitespace,
// leaving it unread. Leading whitespace is consumed.
func firstNonSpace(br *bufio.Reader) (byte, error) {
	for {
		b, err := br.ReadByte()
		if err != nil {
			return 0, err
		}
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		}
		return b, br.UnreadByte()
	}
}
