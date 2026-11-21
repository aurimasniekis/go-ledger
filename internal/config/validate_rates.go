package config

import (
	"net/url"
	"regexp"
	"time"
)

var rateSourcePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,31}$`)

// validate checks the rate settings. The durations are checked whether or
// not rates are enabled, so turning them on later cannot fail at startup
// for a value that was already wrong; the URL and source only when enabled.
func (r Rates) validate(bad func(key, msg string)) {
	if r.StaleAfter < 24*time.Hour {
		bad("rates.stale_after", "must be at least 24h")
	}
	if r.RefreshInterval < time.Hour {
		bad("rates.refresh_interval", "must be at least 1h")
	} else if r.RefreshInterval > r.StaleAfter {
		bad("rates.refresh_interval", "must not exceed rates.stale_after")
	}
	if r.Timeout < time.Second || r.Timeout > 2*time.Minute {
		bad("rates.timeout", "must be between 1s and 2m")
	}
	if !r.Enabled {
		return
	}
	u, err := url.Parse(r.BaseURL)
	if secure := err == nil && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" &&
		(u.Scheme == "https" || u.Scheme == "http" && isLoopbackHost(u.Hostname())); !secure {
		bad("rates.base_url", "must be an https URL without credentials or query (plain http only for a loopback test server)")
	}
	if !rateSourcePattern.MatchString(r.Source) {
		bad("rates.source", "must be a Frankfurter provider key such as ECB, or blended (letters, digits, '_', '.', '-', at most 32)")
	}
}
