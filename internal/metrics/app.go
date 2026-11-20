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
	r.NewGauge("ledger_build_info", "Build information; the value is always 1.", "version", "go_version").
		Set(1, version, runtime.Version())
	return a
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
