package config

import (
	"net/url"
	"regexp"
	"strings"
	"time"
)

var (
	uuidPattern    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	appNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
)

// AppName returns the application's configured name, or the default for its
// mode.
func (a EnableBankingApp) AppName(mode string) string {
	if a.Name != "" {
		return a.Name
	}
	return "enablebanking-" + mode
}

// validate checks the Enable Banking applications. The key's PEM content is
// parsed by the adapter at startup; here only presence is checked, and no
// message contains the app id or the key.
func (e EnableBanking) validate(bad func(key, msg string)) {
	enabled := false
	names := map[string]string{}
	for _, s := range e.Slots() {
		if !s.App.Enabled {
			continue
		}
		enabled = true
		p := "banking.enable_banking." + s.Mode
		if !uuidPattern.MatchString(s.App.AppID) {
			bad(p+".app_id", "required when "+p+".enabled is true: the application id (a UUID) from the Enable Banking Control Panel")
		}
		if s.App.PrivateKey == "" {
			bad(p+".private_key", "required when "+p+".enabled is true: set "+EnvName(p+".private_key")+" or "+p+".private_key_file")
		}
		name := s.App.AppName(s.Mode)
		if !appNamePattern.MatchString(name) {
			bad(p+".name", "must be 1 to 64 lower-case letters, digits, '-' or '_'")
		} else if other, dup := names[name]; dup {
			bad(p+".name", "is also the name of the "+other+" application; names must differ")
		}
		names[name] = s.Mode
	}
	if !enabled {
		return
	}
	u, err := url.Parse(e.BaseURL)
	secure := err == nil && u.Host != "" && (u.Scheme == "https" || u.Scheme == "http" && isLoopbackHost(u.Hostname()))
	if !secure {
		bad("banking.enable_banking.base_url", "must be an https URL (plain http only for a loopback test server)")
	}
}

// BankRedirectPath is the path of the page that finishes a bank consent; the
// path of banking.redirect_url must be exactly this.
const BankRedirectPath = "/connections/callback"

// validateRedirect checks banking.redirect_url when it is set: an absolute
// https URL (http only on loopback) with no query or fragment, ending in
// [BankRedirectPath].
func (b Banking) validateRedirect(bad func(key, msg string)) {
	if b.ConsentTTL < time.Minute || b.ConsentTTL > 24*time.Hour {
		bad("banking.consent_ttl", "must be between 1 minute and 24 hours")
	}
	if b.StaleAfter < time.Minute || b.StaleAfter > 30*24*time.Hour {
		bad("banking.stale_after", "must be between 1 minute and 30 days")
	}
	if b.RedirectURL == "" {
		return
	}
	u, err := url.Parse(b.RedirectURL)
	secure := err == nil && u.Host != "" && (u.Scheme == "https" || u.Scheme == "http" && isLoopbackHost(u.Hostname()))
	switch {
	case !secure:
		bad("banking.redirect_url", "must be an absolute https URL (plain http only on loopback)")
	case u.RawQuery != "" || u.Fragment != "" || u.User != nil:
		bad("banking.redirect_url", "must have no query, fragment or user information")
	case u.Path != BankRedirectPath:
		bad("banking.redirect_url", "must end in "+BankRedirectPath)
	}
}

func isLoopbackHost(h string) bool {
	return h == "localhost" || strings.HasPrefix(h, "127.") || h == "::1"
}
