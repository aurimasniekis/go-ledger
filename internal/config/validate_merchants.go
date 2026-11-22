package config

import (
	"net/url"
	"regexp"
	"time"
	"unicode/utf8"
)

var countryCodePattern = regexp.MustCompile(`^[A-Z]{2}$`)

// validate checks the remote merchant source. Ranges are checked whether or not
// it is enabled; the URL only when it is.
func (r MerchantRemote) validate(bad func(key, msg string)) {
	const p = "merchants.enrichment.remote."
	if r.Timeout < time.Second || r.Timeout > time.Minute {
		bad(p+"timeout", "must be between 1s and 1m")
	}
	if r.Retention != 0 && (r.Retention < 24*time.Hour || r.Retention > 8760*time.Hour) {
		bad(p+"retention", "must be 0 or between 24h and 8760h")
	}
	if r.Country != "" && !countryCodePattern.MatchString(r.Country) {
		bad(p+"country", "must be a two-letter upper-case ISO 3166-1 code such as LT, or empty")
	}
	if utf8.RuneCountInString(r.Attribution) > 200 {
		bad(p+"attribution", "must be at most 200 characters")
	}
	if !r.Enabled {
		return
	}
	u, err := url.Parse(r.URL)
	if secure := err == nil && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" &&
		(u.Scheme == "https" || u.Scheme == "http" && isLoopbackHost(u.Hostname())); !secure {
		bad(p+"url", "is required when enabled: an https URL without credentials or query (plain http only for a loopback test server)")
	}
}
