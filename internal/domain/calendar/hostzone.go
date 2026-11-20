package calendar

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// HostZoneName returns the IANA name of the host's local zone, so that setup
// can capture it once as the configured reporting zone (decision D5). It
// looks at $TZ, then the target of /etc/localtime, then /etc/timezone. ok is
// false when no name loads; the caller should then ask for one explicitly
// rather than guess (UTC is a safe suggestion, not a silent default).
func HostZoneName() (name string, ok bool) {
	target, _ := os.Readlink("/etc/localtime")
	file, _ := os.ReadFile("/etc/timezone")
	return hostZoneName(os.Getenv("TZ"), target, string(file))
}

func hostZoneName(tz, localtimeTarget, timezoneFile string) (string, bool) {
	candidates := []string{strings.TrimPrefix(tz, ":")}
	if localtimeTarget != "" {
		p := filepath.ToSlash(localtimeTarget)
		if _, after, found := strings.Cut(p, "zoneinfo/"); found {
			candidates = append(candidates, after)
		}
	}
	candidates = append(candidates, strings.TrimSpace(timezoneFile))
	for _, c := range candidates {
		if c == "" || c == "Local" || strings.HasPrefix(c, "/") {
			continue
		}
		if _, err := time.LoadLocation(c); err == nil {
			return c, true
		}
	}
	return "", false
}
