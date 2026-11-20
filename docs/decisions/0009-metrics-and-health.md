# ADR-0009: Metrics, health and readiness

- Status: accepted
- Date: 2026-11-20

Numbering: 0008 is reserved for the UI toolchain ADR (F7).

## Context

Ledger needs Prometheus metrics, a liveness probe and a readiness probe. The listener that serves them is unauthenticated by design, and its labels must stay bounded.

## Decision

- **Endpoints.** `GET /metrics` (Prometheus text format 0.0.4), `GET /healthz` (200 while the process can answer) and `GET /readyz` (200 when every required dependency is reachable, else 503). Readiness bodies name each check and say `ok` or `failed`; error text is logged, never returned.
- **Listener.** `http.management_listen` defaults to `127.0.0.1:9090`: a second server on loopback, with the same timeouts and middleware, serves the three endpoints, and the main listener does not. Setting it to the empty string (YAML or `--http.management_listen=`; a blank environment variable counts as unset) serves them on the main listener instead, with a startup warning. A non-loopback management address also logs a warning.
- **Unauthenticated by design.** The management listener has no authentication, so it must stay on loopback or a private network and never face a public interface. Metrics reveal route names, traffic volumes and error rates, never request content. When served on the main listener, the same endpoints are public if that listener is.
- **Readiness gates on the database only (D17).** `internal/health` holds a pluggable list of `Checker`s. Optional integrations (bank provider, OIDC, Pulse, models) are never registered, so a disabled or failing integration cannot fail readiness; they are reported on a status endpoint in later work. **Until F11 wires the store into `serve`, `/readyz` has no checks**: `ledger serve` does not open the database yet, so the list is empty and `/readyz` reports `ready` with no checks. It therefore does not yet prove the database is reachable. `TODO(F11: wire store into serve)` in `cmd/ledger/serve.go` registers `health.Func("database", db.PingContext)` once `serve` opens the store.
- **Metrics.** `ledger_http_requests_total{route,method,status}`, `ledger_http_request_duration_seconds{route,method}`, `ledger_http_requests_in_flight`, `ledger_errors_total{kind}` (the error-envelope code), `ledger_job_runs_total{job,outcome}`, `ledger_job_last_run_timestamp_seconds{job}`, `ledger_job_duration_seconds{job}`, `ledger_build_info{version,go_version}` and `ledger_metrics_dropped_series_total{metric}`. Background jobs call `metrics.App.JobDone(job, ok, took)` with the same name that `logging.Job` puts in the `job` log field; `job_id` is never a label.
- **Bounded labels.** `route` is the registered ServeMux pattern without its method (`/api/things/{id}`); unmatched requests are `unmatched`; `method` is a known HTTP verb or `OTHER`; `status` is the response code (`aborted` when a handler aborts the connection). Label values are cut to at most 64 bytes, at a whole UTF-8 rune, and invalid UTF-8 is replaced with U+FFFD. Two values longer than 64 bytes that share their first 64 bytes are one series: the cut merges them silently. That is the accepted cost of the guard, not a way to make labels unbounded. Each metric keeps at most 500 series: further combinations are dropped and counted in `ledger_metrics_dropped_series_total`. Metric and label names are checked at registration, which panics on an invalid name, a `__` prefix, a duplicate label or a histogram label named `le`, as it does on a duplicate metric. Callers must still pass only enum-like values, never IDs, raw paths or free text.
- **Own text writer, not client_golang.** `prometheus/client_golang` would add several modules (client_model, common, procfs, protobuf and their dependencies) to a program with a deliberately small dependency set, and Ledger needs only counters, gauges and histograms. The writer in `internal/metrics` (about 300 lines, standard library only) covers those types, label escaping and stable output, and is tested. It does not export Go runtime or process metrics; if those are wanted later, adopt client_golang then, behind the same `App` type.

## Consequences

- Scrapers must reach the management address; in a container, publish that port deliberately and only to the scraper's network.
- Features add their own metrics through `metrics.Registry` and own their label sets; the cardinality guard is a backstop, not a license for unbounded labels.
- The catch-all route is counted as `unmatched`, so 405s (not produced yet, see ADR-0006) will appear under the same label until M1 changes routing.
