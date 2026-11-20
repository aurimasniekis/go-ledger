package config

import (
	"net/url"
	"strings"
	"unicode"
)

// validate checks events.pulse. The source rule runs even while publishing is
// off, so a typo shows up before anyone turns it on; the connection settings are
// required only when enabled. Files are not opened here: a missing creds file
// must never stop startup, the publisher reports it and retries.
func (p Pulse) validate(bad func(key, msg string)) {
	src := strings.TrimSpace(p.Source)
	if src == "" || len(src) > 128 || strings.IndexFunc(src, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
		bad("events.pulse.source", "must be 1 to 128 printable characters")
	}
	if !p.Enabled {
		return
	}
	if p.URL == "" {
		bad("events.pulse.url", "required when events.pulse.enabled is true, for example tls://pulse.example.com:4222")
	} else if u, err := url.Parse(p.URL); err != nil || u.Scheme != "tls" || u.Hostname() == "" {
		bad("events.pulse.url", "must be a tls:// URL with a host, for example tls://pulse.example.com:4222 (plain connections are not supported)")
	}
	if p.CredsFile == "" {
		bad("events.pulse.creds_file", "required when events.pulse.enabled is true: the path of the Pulse .creds file")
	}
}
