package config

import "fmt"

func (m MCP) validate(bad func(key, msg string)) {
	in := func(key string, v, lo, hi int64) {
		if v < lo || v > hi {
			bad(key, fmt.Sprintf("must be between %d and %d", lo, hi))
		}
	}
	in("mcp.requests_per_minute", int64(m.RequestsPerMinute), 1, 100000)
	in("mcp.burst", int64(m.Burst), 1, 10000)
	in("mcp.max_request_bytes", m.MaxRequestBytes, 1024, 16<<20)
}
