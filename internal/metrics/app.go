package metrics

import (
	"runtime"
	"time"
)

// Outcome values for background job runs. The set is closed.
const (
	JobSuccess = "success"
	JobFailure = "failure"
)

// App is Ledger's metric set. Create one per process with NewApp.
type App struct {
	Registry *Registry

	Requests  *Counter   // route, method, status
	Duration  *Histogram // route, method
	InFlight  *Gauge
	Errors    *Counter // kind: the API error code
	JobRuns   *Counter // job, outcome
	JobLast   *Gauge   // job: Unix time of the last run
	JobLength *Histogram
	// BankAccounts is the number of bank accounts by freshness state (B6).
	BankAccounts *Gauge
	// Rates* describe the exchange-rate refresh (FX2c, ADR-0048). The rate
	// provider is optional: these report on it and never gate readiness.
	RatesRuns        *Counter // trigger, outcome
	RatesLastSuccess *Gauge   // Unix time of the last run that refreshed every currency
	RatesLastRun     *Gauge   // Unix time of the last finished run, whatever its outcome
	RatesProvider    *Gauge   // 1 when a rate provider is configured, else 0

	eventsPublished *Counter
	eventsDuplicate *Counter
	eventsFailed    *Counter
	eventsRetries   *Counter
	eventsPending   *Gauge
	pulseConnected  *Gauge
}

// NewApp registers the standard metrics, including build information.
func NewApp(version string) *App {
	r := NewRegistry(0)
	a := &App{Registry: r}
	a.Requests = r.NewCounter("ledger_http_requests_total",
		"HTTP requests by route template, method and status code.", "route", "method", "status")
	a.Duration = r.NewHistogram("ledger_http_request_duration_seconds",
		"HTTP request duration in seconds by route template and method.", nil, "route", "method")
	a.InFlight = r.NewGauge("ledger_http_requests_in_flight", "HTTP requests being served now.")
	a.Errors = r.NewCounter("ledger_errors_total",
		"Error responses by error code (the stable code in the error envelope).", "kind")
	a.JobRuns = r.NewCounter("ledger_job_runs_total",
		"Background job runs by job name and outcome (success or failure).", "job", "outcome")
	a.JobLast = r.NewGauge("ledger_job_last_run_timestamp_seconds",
		"Unix time the job last finished, by job name.", "job")
	a.JobLength = r.NewHistogram("ledger_job_duration_seconds",
		"Background job run duration in seconds by job name.", nil, "job")
	a.BankAccounts = r.NewGauge("ledger_bank_accounts",
		"Bank accounts by data freshness: fresh, stale (last success over 24 hours ago), failed (latest refresh failed), reconnect_required, disconnected or never_refreshed.", "freshness")
	a.RatesRuns = r.NewCounter("ledger_rates_refresh_runs_total",
		"Exchange-rate refresh runs by trigger (scheduled or manual) and outcome (success: every currency refreshed; failure: otherwise). Runs cut short by shutdown are not counted.", "trigger", "outcome")
	a.RatesLastSuccess = r.NewGauge("ledger_rates_last_success_timestamp_seconds",
		"Unix time the last exchange-rate refresh that refreshed every currency finished; absent until one has since the process started.")
	a.RatesLastRun = r.NewGauge("ledger_rates_last_run_timestamp_seconds",
		"Unix time the last exchange-rate refresh finished, whatever its outcome; absent until one has since the process started.")
	a.RatesProvider = r.NewGauge("ledger_rates_provider_enabled",
		"1 when an exchange-rate provider is configured (rates.enabled), else 0. The provider is optional and never gates readiness.")
	a.eventsPublished = r.NewCounter("ledger_events_published_total", "Events acknowledged by Pulse.")
	a.eventsDuplicate = r.NewCounter("ledger_events_duplicate_total", "Published events Pulse reported as already stored (deduplicated by event id); a subset of published.")
	a.eventsFailed = r.NewCounter("ledger_events_failed_total", "Events given up on: not delivered within the retry window, or never publishable.")
	a.eventsRetries = r.NewCounter("ledger_events_retries_total", "Failed publish attempts that were scheduled for a retry.")
	a.eventsPending = r.NewGauge("ledger_events_pending", "Events waiting to be published (outbox rows in the pending state).")
	a.pulseConnected = r.NewGauge("ledger_pulse_connected", "1 while the publisher holds a Pulse connection, else 0. Pulse is optional and never gates readiness.")
	r.NewGauge("ledger_build_info", "Build information; the value is always 1.", "version", "go_version").
		Set(1, version, runtime.Version())
	return a
}

// The Event* methods implement outbox.Meter.

// EventPublished counts an acknowledged event.
func (a *App) EventPublished() { a.eventsPublished.Inc() }

// EventDuplicate counts an acknowledged event the broker had already stored.
func (a *App) EventDuplicate() { a.eventsDuplicate.Inc() }

// EventFailed counts an event given up on.
func (a *App) EventFailed() { a.eventsFailed.Inc() }

// EventRetry counts a scheduled retry.
func (a *App) EventRetry() { a.eventsRetries.Inc() }

// SetPending sets the pending-events gauge.
func (a *App) SetPending(n int) { a.eventsPending.Set(float64(n)) }

// SetConnected sets the Pulse connection gauge.
func (a *App) SetConnected(up bool) {
	v := 0.0
	if up {
		v = 1
	}
	a.pulseConnected.Set(v)
}

// SetBankFreshness sets how many accounts are in each freshness state. The
// states are a closed set chosen by the scheduler, so cardinality is fixed.
func (a *App) SetBankFreshness(counts map[string]int) {
	for state, n := range counts {
		a.BankAccounts.Set(float64(n), state)
	}
}

// RatesRefreshed implements fx.Meter: it counts one finished refresh run and
// sets the last-run and, for a fully successful run, last-success times.
func (a *App) RatesRefreshed(trigger string, success bool, at time.Time) {
	outcome := JobSuccess
	if !success {
		outcome = JobFailure
	}
	a.RatesRuns.Inc(trigger, outcome)
	a.RatesLastRun.Set(float64(at.Unix()))
	if success {
		a.RatesLastSuccess.Set(float64(at.Unix()))
	}
}

// SetRatesProvider sets the provider-configured gauge.
func (a *App) SetRatesProvider(enabled bool) {
	v := 0.0
	if enabled {
		v = 1
	}
	a.RatesProvider.Set(v)
}

// JobDone records one finished run of the named job. The name is the job's
// fixed name (the "job" log field), never its run ID.
func (a *App) JobDone(job string, ok bool, took time.Duration) {
	outcome := JobSuccess
	if !ok {
		outcome = JobFailure
	}
	a.JobRuns.Inc(job, outcome)
	a.JobLast.Set(float64(time.Now().Unix()), job)
	a.JobLength.Observe(took.Seconds(), job)
}
