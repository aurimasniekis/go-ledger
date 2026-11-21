package httpapi

import "net/http"

// MCPPath is the Model Context Protocol endpoint (task P-MCP1, ADR-0038).
const MCPPath = APIPrefix + "/mcp"

// registerMCP registers POST /mcp. h serves it (internal/mcpserver); nil means
// MCP is turned off (mcp.enabled), and the route answers 404 to a caller that
// holds a token, so the OpenAPI document and the route table stay the same
// whether or not it is enabled.
func registerMCP(s *Server, h http.Handler) {
	if h == nil {
		h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			WriteError(w, r, NotFound("the MCP endpoint is turned off (mcp.enabled is false)"))
		})
	}
	s.HandleAPI("POST "+MCPPath, TokenOnly(), h)
}
