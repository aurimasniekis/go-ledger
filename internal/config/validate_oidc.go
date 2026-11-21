package config

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"
)

func resolveOIDCSecretFile(c *Config, sources map[string]string, readFile func(string) ([]byte, error)) []Problem {
	o := &c.Auth.OIDC
	if o.ClientSecretFile == "" {
		return nil
	}
	const key = "auth.oidc.client_secret_file"
	src := sources[key]
	if o.ClientSecret != "" {
		return []Problem{{Source: src, Key: key, Msg: "set only one of auth.oidc.client_secret (" + EnvName("auth.oidc.client_secret") + ") and " + key}}
	}
	data, err := readFile(o.ClientSecretFile)
	if err != nil {
		return []Problem{{Source: src, Key: key, Msg: fmt.Sprintf("cannot read the file: %v", pathErr(err))}}
	}
	v := strings.TrimRight(string(data), "\r\n")
	if v == "" {
		return []Problem{{Source: src, Key: key, Msg: "the file is empty"}}
	}
	o.ClientSecret = v
	sources["auth.oidc.client_secret"] = "file " + o.ClientSecretFile
	return nil
}

// secureURL reports whether s is an absolute https URL (or http on a loopback
// host) without a fragment.
func secureURL(s string) (*url.URL, bool) {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.Fragment != "" || u.User != nil {
		return nil, false
	}
	return u, u.Scheme == "https" || u.Scheme == "http" && isLoopbackHost(u.Hostname())
}

// validateOIDC checks the single-sign-on settings. No message contains the
// client secret.
func (a Auth) validateOIDC(bad func(key, msg string)) {
	o := a.OIDC
	if !a.Password.Enabled && !o.Enabled {
		bad("auth.password.enabled", "can only be false when auth.oidc.enabled is true: otherwise nobody could sign in")
	}
	if !o.Enabled {
		return
	}
	if n := len([]rune(o.Name)); n < 1 || n > 64 || strings.IndexFunc(o.Name, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
		bad("auth.oidc.name", "must be 1 to 64 printable characters")
	}
	if u, ok := secureURL(o.Issuer); !ok || u.RawQuery != "" {
		bad("auth.oidc.issuer", "required when auth.oidc.enabled is true: an https URL without query or fragment (plain http only for a loopback test provider)")
	}
	if o.ClientID == "" || len(o.ClientID) > 255 {
		bad("auth.oidc.client_id", "required when auth.oidc.enabled is true (at most 255 bytes)")
	}
	if o.ClientSecret == "" {
		bad("auth.oidc.client_secret", "required when auth.oidc.enabled is true: set "+EnvName("auth.oidc.client_secret")+" or auth.oidc.client_secret_file")
	}
	if u, ok := secureURL(o.RedirectURL); !ok || u.Path != OIDCCallbackPath || u.RawQuery != "" {
		bad("auth.oidc.redirect_url", "required when auth.oidc.enabled is true: Ledger's external https address followed by "+OIDCCallbackPath+" (plain http only on loopback)")
	}
	if !slices.Contains(o.Scopes, "openid") {
		bad("auth.oidc.scopes", "must include openid")
	}
	for _, s := range o.Scopes {
		if s == "" || strings.ContainsFunc(s, func(r rune) bool { return r <= ' ' || r > '~' || r == '"' || r == '\\' }) {
			bad("auth.oidc.scopes", "each scope must be printable ASCII without spaces, quotes or backslashes")
			break
		}
	}
	if o.ClockSkew < 0 || o.ClockSkew > 5*time.Minute {
		bad("auth.oidc.clock_skew", "must be from 0 to 5m")
	}
}
