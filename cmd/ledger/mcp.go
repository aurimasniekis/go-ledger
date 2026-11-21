package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aurimasniekis/go-ledger/internal/mcpserver"
)

// mcpEnv is the process state "ledger mcp" reads; tests replace it.
type mcpEnv struct {
	mgmtEnv
	// Transport replaces standard input and output in tests.
	Transport mcp.Transport
}

// runMCP implements "ledger mcp stdio": an MCP server on standard input and
// output for local agents. It is a client of a running Ledger server (ADR-0038):
// every tool call goes to the server's API with the API token, found like the
// management commands find it (--token-file, LEDGER_TOKEN, client config), so the
// server authenticates the token, enforces its grants, validates the input and
// audits writes exactly as for HTTP, and the database stays with the server.
// Standard output carries the protocol only; diagnostics go to stderr.
func runMCP(ctx context.Context, args []string, stdout, stderr io.Writer, env mcpEnv) int {
	if len(args) == 0 || args[0] != "stdio" {
		if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
			mcpUsage(stdout)
			return 0
		}
		mcpUsage(stderr)
		return 2
	}
	m := &mgmt{stdout: stdout, stderr: stderr, env: env.mgmtEnv}
	fs := m.flags("mcp stdio")
	if _, rc := m.parse(fs, args[1:], false); rc != 0 {
		return max(rc, 0)
	}
	c, err := m.client()
	if err != nil {
		say(stderr, "ledger: "+printable(err.Error()))
		return 2
	}
	srv, err := mcpserver.NewServer(mcpserver.Options{Caller: mcpserver.Remote(c), Version: version})
	if err != nil {
		say(stderr, "ledger: "+printable(err.Error()))
		return 1
	}
	var tr mcp.Transport = &mcp.StdioTransport{}
	if env.Transport != nil {
		tr = env.Transport
	}
	if err := srv.Run(ctx, tr); err != nil && ctx.Err() == nil {
		say(stderr, "ledger: mcp: "+printable(err.Error()))
		return 1
	}
	return 0
}

func mcpUsage(w io.Writer) {
	say(w, "usage: ledger mcp stdio [--url URL] [--token-file PATH] [--client-config PATH] [--insecure] [--timeout D]")
	say(w)
	say(w, "Serve the Model Context Protocol on standard input and output, for a local agent.")
	say(w, "Tool calls go to a running Ledger server (default "+defaultServerURL+") with an API token,")
	say(w, "so the server's grants, validation and audit apply. The token comes from --token-file,")
	say(w, "LEDGER_TOKEN or the client config, as for the other management commands; it is never printed.")
	say(w, "The same tools are served over HTTP at POST /api/v1/mcp.")
}

// mcpContext returns a context cancelled by SIGINT or SIGTERM.
func mcpContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
