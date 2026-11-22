package config

import (
	"slices"
	"strings"
	"time"
)

// AlertChannelPulse is the alert channel that records an outbox event.
const AlertChannelPulse = "pulse"

// AlertChannels returns the normalised channel names (trimmed, lowercase,
// without duplicates or blanks).
func (a Alerts) AlertChannels() []string {
	var out []string
	for _, c := range a.Channels {
		if c = strings.ToLower(strings.TrimSpace(c)); c != "" && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out
}

// validate checks alerts.*. A channel that cannot deliver is refused rather
// than silently ignored: the operator asked for something that would not happen.
func (a Alerts) validate(pulseEnabled bool, bad func(key, msg string)) {
	for _, c := range a.AlertChannels() {
		switch c {
		case AlertChannelPulse:
			if !pulseEnabled {
				bad("alerts.channels", "pulse needs events.pulse.enabled to be true")
			}
		default:
			bad("alerts.channels", "unknown channel \""+c+"\"; the only channel is pulse (empty keeps alerts in the app)")
		}
	}
	if a.ConsentHorizonDays < 1 || a.ConsentHorizonDays > 365 {
		bad("alerts.consent_horizon_days", "must be between 1 and 365")
	}
	if a.Interval < time.Minute || a.Interval > 24*time.Hour {
		bad("alerts.interval", "must be between 1m and 24h")
	}
}
