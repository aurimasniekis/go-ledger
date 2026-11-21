package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
)

// resolveSecretFiles reads a secret named by a *_file key into its value field.
func resolveSecretFiles(c *Config, sources map[string]string, readFile func(string) ([]byte, error)) []Problem {
	ps := resolveKEKFile(c, sources, readFile)
	for _, slot := range []struct {
		mode string
		app  *EnableBankingApp
	}{{"app", &c.Banking.EnableBanking.App}, {"developer", &c.Banking.EnableBanking.Developer}} {
		ps = append(ps, resolveKeyFile(slot.app, "banking.enable_banking."+slot.mode, sources, readFile)...)
	}
	return ps
}

// resolveKeyFile reads an Enable Banking private key file into the value.
func resolveKeyFile(a *EnableBankingApp, prefix string, sources map[string]string, readFile func(string) ([]byte, error)) []Problem {
	if a.PrivateKeyFile == "" {
		return nil
	}
	key := prefix + ".private_key_file"
	src := sources[key]
	if a.PrivateKey != "" {
		return []Problem{{Source: src, Key: key, Msg: "set only one of " + prefix + ".private_key (" + EnvName(prefix+".private_key") + ") and " + key}}
	}
	data, err := readFile(a.PrivateKeyFile)
	if err != nil {
		return []Problem{{Source: src, Key: key, Msg: fmt.Sprintf("cannot read the file: %v", pathErr(err))}}
	}
	if strings.TrimSpace(string(data)) == "" {
		return []Problem{{Source: src, Key: key, Msg: "the file is empty"}}
	}
	a.PrivateKey = string(data)
	sources[prefix+".private_key"] = "file " + a.PrivateKeyFile
	return nil
}

func resolveKEKFile(c *Config, sources map[string]string, readFile func(string) ([]byte, error)) []Problem {
	if c.KEK.File == "" {
		return nil
	}
	src := sources["kek.file"]
	if c.KEK.Value != "" {
		return []Problem{{Source: src, Key: "kek.file", Msg: "set only one of kek.value (" + EnvName("kek.value") + ") and kek.file"}}
	}
	data, err := readFile(c.KEK.File)
	if err != nil {
		return []Problem{{Source: src, Key: "kek.file", Msg: fmt.Sprintf("cannot read the file: %v", pathErr(err))}}
	}
	v := strings.TrimRight(string(data), "\r\n")
	if v == "" {
		return []Problem{{Source: src, Key: "kek.file", Msg: "the file is empty"}}
	}
	c.KEK.Value = v
	sources["kek.value"] = "file " + c.KEK.File
	return nil
}

// pathErr drops the path from a file error, which the caller already names.
func pathErr(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// validate checks ranges and cross-field rules. sources locates the origin of
// a bad value for the message.
func (c *Config) validate(sources map[string]string) []Problem {
	var ps []Problem
	bad := func(key, msg string) { ps = append(ps, Problem{Source: sources[key], Key: key, Msg: msg}) }
	if c.HTTP.ManagementListen != "" {
		if _, port, err := net.SplitHostPort(c.HTTP.ManagementListen); err != nil {
			bad("http.management_listen", "must be host:port, for example 127.0.0.1:9090, or empty to use the main listener")
		} else if p, err := strconv.Atoi(port); err != nil || p < 0 || p > 65535 {
			bad("http.management_listen", "port must be a number from 0 to 65535")
		}
	}
	if strings.TrimSpace(c.HTTP.Listen) == "" {
		bad("http.listen", "must not be empty")
	} else if _, port, err := net.SplitHostPort(c.HTTP.Listen); err != nil {
		bad("http.listen", "must be host:port, for example 127.0.0.1:8080 or :8080")
	} else if n, err := strconv.Atoi(port); err != nil || n < 0 || n > 65535 {
		bad("http.listen", "port must be a number from 0 to 65535")
	}
	for i, p := range c.HTTP.TrustedProxies {
		p = strings.TrimSpace(p)
		if _, err := netip.ParsePrefix(p); err == nil {
			continue
		}
		if _, err := netip.ParseAddr(p); err == nil {
			continue
		}
		bad("http.trusted_proxies", fmt.Sprintf("entry %d is not an IP address or CIDR range", i+1))
	}
	for _, d := range []struct {
		key string
		val time.Duration
	}{
		{"http.read_header_timeout", c.HTTP.ReadHeaderTimeout}, {"http.read_timeout", c.HTTP.ReadTimeout},
		{"http.write_timeout", c.HTTP.WriteTimeout}, {"http.idle_timeout", c.HTTP.IdleTimeout},
		{"http.shutdown_timeout", c.HTTP.ShutdownTimeout}, {"database.busy_timeout", c.Database.BusyTimeout},
	} {
		if d.val <= 0 {
			bad(d.key, "must be a positive duration")
		}
	}
	if c.HTTP.MaxBodyBytes <= 0 {
		bad("http.max_body_bytes", "must be positive")
	}
	if strings.TrimSpace(c.Database.Path) == "" {
		bad("database.path", "must not be empty")
	}
	if tz := c.Reporting.Timezone; tz != "" {
		if _, err := calendar.LoadZone(tz); err != nil { // same rules as the zone the app builds: "Local" is refused
			bad("reporting.timezone", err.Error())
		}
	}
	for _, d := range []struct {
		key string
		val time.Duration
	}{{"banking.refresh.interval", c.Banking.Refresh.Interval}, {"banking.refresh.max_backoff", c.Banking.Refresh.MaxBackoff}} {
		if d.val < time.Minute {
			bad(d.key, "must be at least 1m")
		}
	}
	c.Events.Pulse.validate(bad)
	c.Banking.EnableBanking.validate(bad)
	if c.KEK.Value == "" && c.KEK.File == "" {
		ps = append(ps, Problem{Key: "kek", Msg: "required: set " + EnvName("kek.value") + " or " + EnvName("kek.file")})
	}
	return ps
}
