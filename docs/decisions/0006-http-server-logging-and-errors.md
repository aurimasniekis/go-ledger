# ADR-0006: HTTP server, logging and error envelope

- Status: accepted
- Date: 2026-11-20

Numbering: 0003 is taken by the configuration ADR (and by two pending branches), and 0004 and 0005 are reserved for the next planned ADRs, so this one uses the next unreserved number, 0006.

## Context

Every later slice (health and metrics, authentication, the Management API, the UI) sits behind one HTTP server and needs the same request identity, error shape and log hygiene. Ledger handles financial data, so logs and error responses must never carry secrets.

## Decision

- **Server.** `internal/httpapi` wraps `net/http` with the timeouts, body limit and shutdown timeout from `http.*` configuration. `Run` serves until its context is cancelled (SIGINT or SIGTERM under `ledger serve`), then calls `Shutdown`, which stops accepting connections and drains in-flight requests; if `http.shutdown_timeout` expires it closes what is left and returns the error.
- **Middleware order**, outermost first: request ID, access log, panic recovery, body limit.
- **Request ID.** `X-Request-ID` is accepted if it is at most 64 characters of letters, digits, `-`, `_` and `.`; otherwise a ULID is generated. It is echoed in the response header, stored in the context, added to every log line written with that context (the logging handler does this, so handlers cannot forget) and returned as `request_id` in error bodies.
- **Error envelope.** Every non-2xx API response is `{"code","message","field"?,"request_id"}`. Stable codes: `validation_failed`, `unauthenticated`, `forbidden`, `conflict`, `rate_limited`, `dependency_unavailable`, `internal_error`, plus `not_found`, `payload_too_large` and `method_not_allowed`. Codes only grow. An error's cause is logged, never sent; anything that is not an `*httpapi.Error` becomes a generic 500. The envelope is flat; M1 may nest it under an `error` key only by superseding this ADR.
- **Body limit.** `http.max_body_bytes` is enforced twice: a declared `Content-Length` over the limit is rejected before the handler runs, and every body is wrapped in `http.MaxBytesReader` so chunked uploads fail with the same 413 envelope when read (`DecodeJSON` and `WriteError` map the error).
- **Panics** are recovered; the panic value and stack are logged, the client gets the generic 500 envelope.
- **Logging.** `log/slog`, JSON on stderr by default, `log.format=text`, `log.level`. The access log records method, path (never the query string, which may carry tokens or OIDC codes), status, bytes, duration and remote address; never headers or bodies.
- **Background logs (D17).** Each job run gets a new ULID `job_id` and a `job` name on every line (`logging.Job`). Request logs use `request_id`; the two fields are never mixed.
- **Redaction.** `internal/redact` masks (a) the value of any attribute whose key looks secret (password, token, secret, authorization, cookie, kek, totp, session, ...), (b) registered secret values wherever they occur in messages, errors and other values, and (c) bearer or basic credentials inside text. `ledger serve` registers the configured KEK; the logger and the error writer both use the process-wide default redactor. Key-based redaction is deliberately broad, so an occasional harmless field is masked.
- **Startup logging.** `ledger serve` logs the names (never values) of variables loaded from the .env file and warns about unknown `LEDGER_*` variables. `config check` and `serve` also print those warnings when validation fails.

## Consequences

- Value-based redaction only covers secrets registered with the redactor. Secrets that other slices introduce (API tokens, session keys, provider credentials) must be registered where they are created.
- `net/http`'s ServeMux answers a wrong method with a plain-text 405, not the envelope. Slice M1 should wrap the API mux so 405 uses the envelope.
- The route template is not in the access log yet; F4 adds it with the metrics.
- Readiness and health endpoints are not part of this change. `ledger serve` has a TODO hook where the store and readiness (database only, per D17) are wired once the SQLite store lands.
- No endpoint is added here, so there is no OpenAPI change.
