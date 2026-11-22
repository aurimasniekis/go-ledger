// Package config loads Ledger's configuration: one typed struct filled from
// built-in defaults, an optional YAML file, environment variables (optionally
// seeded from a .env file) and command-line flags, in that order of increasing
// precedence. See docs/decisions/0004-configuration.md.
//
// The Config struct is the single source of truth. Every leaf field's tags
// define its YAML key, environment variable, flag, default and description,
// so adding a field automatically adds its variable, its flag and its entries
// in "ledger config env"; the sync test then forces config.example.yaml and
// .env.example to be regenerated.
package config

import (
	"time"
	_ "time/tzdata" // reporting.timezone must resolve on hosts without a zoneinfo database
)

// Prefix is the environment variable prefix (decision D2).
const Prefix = "LEDGER_"

// Config is the complete application configuration.
//
// Tags on leaf fields: cfg is the key segment, default the built-in default in
// the same syntax as an environment variable, desc the documentation, secret
// marks a value that may only come from the environment (or a *_file key),
// enum lists the allowed values.
type Config struct {
	HTTP      HTTP      `cfg:"http"`
	Database  Database  `cfg:"database"`
	Log       Log       `cfg:"log"`
	Reporting Reporting `cfg:"reporting"`
	KEK       KEK       `cfg:"kek"`
	Events    Events    `cfg:"events"`
	Banking   Banking   `cfg:"banking"`
	Rates     Rates     `cfg:"rates"`
	Merchants Merchants `cfg:"merchants"`
	Auth      Auth      `cfg:"auth"`
	MCP       MCP       `cfg:"mcp"`
	Decisions Decisions `cfg:"decisions"`
}

// MCP configures the Model Context Protocol endpoint, POST /api/v1/mcp
// (ADR-0038). The defaults are proposals, not operator decisions.
type MCP struct {
	Enabled           bool  `cfg:"enabled" default:"true" desc:"Serve the MCP endpoint (streamable HTTP, POST /api/v1/mcp). Every call needs an API token and is limited to what the token's grants allow, exactly as through the HTTP API. false removes the endpoint (404). The stdio mode (ledger mcp stdio) is a separate client-side command and is not affected."`
	RequestsPerMinute int   `cfg:"requests_per_minute" default:"120" desc:"Sustained MCP requests allowed per API token per minute (1 to 100000); more are refused with 429 rate_limited and Retry-After. Counted in memory per process, reset on restart."`
	Burst             int   `cfg:"burst" default:"30" desc:"Most MCP requests one API token may send at once before the per-minute rate applies (1 to 10000)."`
	MaxRequestBytes   int64 `cfg:"max_request_bytes" default:"262144" desc:"Maximum size of one MCP request body in bytes (1024 to 16777216); larger bodies are refused with 413. The general http.max_body_bytes applies as well."`
}

// Auth configures password sign-in and sessions (ADR-0026). The defaults are
// proposals, not operator decisions yet.
type Auth struct {
	Password AuthPassword `cfg:"password"`
	Session  AuthSession  `cfg:"session"`
	SignIn   AuthSignIn   `cfg:"sign_in"`
	OIDC     AuthOIDC     `cfg:"oidc"`
	// Require2FA confines signed-in users without TOTP to enrolment (ADR-0030).
	Require2FA bool `cfg:"require_2fa" default:"false" desc:"Require two-factor authentication (TOTP) for everyone. A signed-in user without it can only enrol an authenticator, see their session, re-authenticate and sign out; every other request is refused with 403 two_factor_enrollment_required. API tokens are not affected. While it is on, users cannot disable their own TOTP (the host command ledger auth reset-2fa can)."`
}

// AuthPassword configures the password policy.
type AuthPassword struct {
	Enabled   bool `cfg:"enabled" default:"true" desc:"Allow signing in with a username and password. false refuses password sign-in and password re-authentication for everyone (403 password_sign_in_disabled) and sends the sign-in page to single sign-on; it needs auth.oidc.enabled. Linking an OIDC identity never turns password sign-in off by itself. If the identity provider is unavailable while this is false, the host can issue a one-time sign-in with ledger auth recovery-sign-in (docs/oidc.md)."`
	MinLength int  `cfg:"min_length" default:"12" desc:"Minimum password length in bytes (1 to 1024). Length is the only rule: there are no composition rules."`
}

// AuthSession configures sign-in sessions.
type AuthSession struct {
	IdleTimeout     time.Duration `cfg:"idle_timeout" default:"30m" desc:"A session not used for this long ends. Must not exceed the absolute timeout."`
	AbsoluteTimeout time.Duration `cfg:"absolute_timeout" default:"12h" desc:"A session ends this long after sign-in, however much it is used."`
	ReauthWindow    time.Duration `cfg:"reauth_window" default:"10m" desc:"How long a password check counts as recent for sensitive actions (credential changes, token creation, user management). After it, the user confirms their password again."`
}

// OIDCCallbackPath is the path of the OIDC redirect (callback) route; the
// path of auth.oidc.redirect_url must be exactly this.
const OIDCCallbackPath = "/api/v1/auth/oidc/callback"

// AuthOIDC configures sign-in with one OpenID Connect provider (ADR-0033).
// The defaults are proposals, except those decision D16 settled.
type AuthOIDC struct {
	Enabled          bool                 `cfg:"enabled" default:"false" desc:"Allow signing in with one OpenID Connect identity provider (authorization code flow with PKCE). Users link their identity while signed in; an identity that is not linked gets no access unless auth.oidc.provisioning.enabled is true."`
	Name             string               `cfg:"name" default:"Single sign-on" desc:"Name of the identity provider shown on the sign-in page and recorded in audit entries: 1 to 64 printable characters."`
	Issuer           string               `cfg:"issuer" desc:"Issuer URL of the provider, exactly as its discovery document states it (a trailing slash matters), for example https://id.example.com/realms/home. https only (plain http only for a loopback test provider). Links are keyed by issuer and subject: changing the issuer leaves every existing link unusable."`
	ClientID         string               `cfg:"client_id" desc:"Client ID registered for Ledger at the provider. Required when auth.oidc.enabled is true."`
	ClientSecret     string               `cfg:"client_secret" secret:"true" desc:"Client secret registered for Ledger at the provider. Secret: environment only, never in the YAML file and never a flag. Set this or client_secret_file."`
	ClientSecretFile string               `cfg:"client_secret_file" desc:"Path of a file that contains the client secret. Alternative to the client_secret variable; trailing newlines are ignored. Only the path is configuration; the secret is never logged."`
	RedirectURL      string               `cfg:"redirect_url" desc:"Callback URL registered at the provider: Ledger's external address followed by /api/v1/auth/oidc/callback, for example https://ledger.example.com/api/v1/auth/oidc/callback. https only (plain http only on loopback)."`
	Scopes           []string             `cfg:"scopes" default:"openid,email,profile" desc:"Scopes requested (a list in YAML, comma separated in the variable). Must include openid. email and profile only fill in display data and a provisioned account's username; identity is never taken from them."`
	Require2FA       bool                 `cfg:"require_2fa" default:"false" desc:"Ask users who have Ledger TOTP enabled for their code after an OIDC sign-in too, and apply auth.require_2fa to OIDC sessions. false (the default, decision D16) trusts the identity provider's own authentication."`
	ClockSkew        time.Duration        `cfg:"clock_skew" default:"1m" desc:"Clock difference tolerated when checking ID token times (exp, nbf, iat, auth_time): 0 to 5m."`
	Provisioning     AuthOIDCProvisioning `cfg:"provisioning"`
}

// AuthOIDCProvisioning configures accounts created on first OIDC sign-in.
type AuthOIDCProvisioning struct {
	Enabled bool   `cfg:"enabled" default:"false" desc:"Create an account, with no password, the first time an identity that is linked to no account signs in, and link it. Off by default: unknown identities are refused. Accounts are never matched by email address."`
	Role    string `cfg:"role" default:"read-only" enum:"administrator,read-only" desc:"Role of accounts created by provisioning."`
}

// AuthSignIn configures sign-in rate limits. They are kept in memory and
// reset when the server restarts.
type AuthSignIn struct {
	AccountMaxFailures int           `cfg:"account_max_failures" default:"10" desc:"Failed sign-ins (and re-authentications) allowed per username within auth.sign_in.window before further attempts for it are refused with 429."`
	AddressMaxFailures int           `cfg:"address_max_failures" default:"30" desc:"Failed sign-ins allowed per client address (an IPv6 address counts as its /64) within auth.sign_in.window before further attempts from it are refused with 429."`
	Window             time.Duration `cfg:"window" default:"15m" desc:"Sliding window over which failed sign-ins are counted."`
	DeviceCookieTTL    time.Duration `cfg:"device_cookie_ttl" default:"2160h" desc:"Lifetime of the device cookie a browser gets at each complete password sign-in (decision D23-1b). While it is valid, that browser's failed sign-ins count against the cookie instead of the username, so a username locked out by someone else's guesses does not lock out the owner's known browsers; the per-address limit, the password and the second factor still apply. Changing a user's password revokes all of that user's device cookies. Default 2160h (90 days), accepted by the operator (D30, 2026-11-21)."`
}

// HTTP configures the HTTP server that serves the API and the embedded UI.
type HTTP struct {
	Listen            string        `cfg:"listen" default:"127.0.0.1:8080" desc:"Address the HTTP server listens on, as host:port. The default accepts local connections only; use :8080 to listen on every interface."`
	ReadHeaderTimeout time.Duration `cfg:"read_header_timeout" default:"10s" desc:"Maximum time to read a request's headers."`
	ReadTimeout       time.Duration `cfg:"read_timeout" default:"30s" desc:"Maximum time to read an entire request, body included."`
	WriteTimeout      time.Duration `cfg:"write_timeout" default:"60s" desc:"Maximum time to write a response."`
	IdleTimeout       time.Duration `cfg:"idle_timeout" default:"120s" desc:"Maximum time an idle keep-alive connection stays open."`
	ShutdownTimeout   time.Duration `cfg:"shutdown_timeout" default:"15s" desc:"How long graceful shutdown waits for in-flight requests before closing connections."`
	MaxBodyBytes      int64         `cfg:"max_body_bytes" default:"1048576" desc:"Maximum size of a request body in bytes."`
	ManagementListen  string        `cfg:"management_listen" default:"127.0.0.1:9090" desc:"Address of the management listener that serves /metrics, /healthz and /readyz, as host:port. Unauthenticated by design: keep it on loopback or a private network, never a public interface. Empty (set in the YAML file or with --http.management_listen=; a blank environment variable counts as unset) serves them on the main listener instead."`
	TrustedProxies    []string      `cfg:"trusted_proxies" desc:"IP addresses or CIDR ranges of reverse proxies whose forwarding headers are trusted (a list in YAML, comma separated in the variable). Empty trusts none. Behind a reverse proxy, list it here: otherwise every client has the proxy's address, and the per-address sign-in limit becomes one lockout shared by everyone, the owner included (ADR-0026). Ledger logs a warning when X-Forwarded-For arrives from an untrusted loopback or private address."`
}

// Database configures the SQLite database.
type Database struct {
	Path        string        `cfg:"path" default:"data/ledger.db" desc:"Path of the SQLite database file. The directory is created if it is missing."`
	BusyTimeout time.Duration `cfg:"busy_timeout" default:"5s" desc:"How long a statement waits for a locked database before failing."`
}

// Log configures logging.
type Log struct {
	Level  string `cfg:"level" default:"info" enum:"debug,info,warn,error" desc:"Minimum log level."`
	Format string `cfg:"format" default:"json" enum:"text,json" desc:"Log output format: json for machines, text for people."`
}

// Reporting configures how dates are reported.
type Reporting struct {
	// TODO(audit-log task): the desc below promises an audit entry for a zone change;
	// until the audit log exists the change is only logged with a warning.
	Timezone string `cfg:"timezone" desc:"IANA time zone used for due dates and monthly views, for example Europe/Vilnius. Empty means the zone stored in the database; on first start against a new database the host's IANA zone is captured once and stored, and startup is refused if the host has no IANA name. A value that differs from the stored zone is applied with a warning, because it reclassifies dates; its audit entry is recorded once the audit log exists. 'Local' is not accepted."`
	Currency string `cfg:"currency" desc:"ISO 4217 code of the reporting currency, for example EUR: the outlook adds a secondary total converted into it, next to the per-currency figures, counting only amounts with an acceptable exchange rate and listing the rest as excluded (decision D5). Empty means no converted total unless a request asks for one."`
}

// Events configures event publishing.
type Events struct {
	Pulse Pulse `cfg:"pulse"`
}

// Pulse configures publishing events to Pulse (NATS JetStream).
type Pulse struct {
	Enabled   bool   `cfg:"enabled" default:"false" desc:"Publish events to Pulse. Off by default. While off, nothing is recorded or published, and turning it on later does not replay changes made while it was off."`
	Source    string `cfg:"source" default:"ledger" desc:"Event source stamped on every published event (the envelope's source field): 1 to 128 printable characters."`
	URL       string `cfg:"url" desc:"Pulse server URL; must use the tls:// scheme, for example tls://pulse.example.com:4222. Required when events.pulse.enabled is true. Nothing connects while publishing is off."`
	CredsFile string `cfg:"creds_file" desc:"Path of the Pulse .creds file (user JWT and key seed). Required when events.pulse.enabled is true. Only the path is configuration: the file is read by the Pulse client, never copied, and its path and content are never logged."`
	CAFile    string `cfg:"ca_file" desc:"Optional PEM file with an extra CA to trust for the Pulse server, added to the system roots. Certificate verification is never disabled."`
	Name      string `cfg:"name" default:"ledger" desc:"Connection name shown on the Pulse server."`
}

// Rates configures the exchange-rate provider (FX2, ADR-0046). Decision D11
// selects Frankfurter; rates are used only to add a converted secondary total
// next to per-currency figures, never to replace them.
type Rates struct {
	Enabled         bool          `cfg:"enabled" default:"false" desc:"Fetch exchange rates from Frankfurter (https://frankfurter.dev, free, no key) and cache them locally. Off by default: the outlook then shows only amounts already in the reporting currency and lists the rest as excluded (no_provider), though rates cached while it was on are still used and shown with their age. Turning it on sends currency codes (never amounts or account data) to the Frankfurter service."`
	BaseURL         string        `cfg:"base_url" default:"https://api.frankfurter.dev" desc:"Frankfurter base URL. https only (plain http only for a loopback test server)."`
	Source          string        `cfg:"source" default:"ECB" desc:"Frankfurter provider key whose rates are used, for example ECB (European Central Bank reference rates, published on working days; a weekend or holiday uses the previous working day), or blended for Frankfurter's own blend of its providers. Rates of different sources are cached apart and never mixed; changing this stops using the rates of the old source."`
	RefreshInterval time.Duration `cfg:"refresh_interval" default:"24h" desc:"Time between background refreshes of the rates from every currency in use to the reporting currency (decision D11: daily), and how long a cached rate is used without asking Frankfurter again. 1h to the stale_after value. Needs reporting.currency; without it nothing is refreshed in the background."`
	StaleAfter      time.Duration `cfg:"stale_after" default:"120h" desc:"How old a cached rate may get before it is treated as stale and left out of converted totals (decision D11: 5 days): measured from when it was last retrieved, and from its date to the date it is used for. 24h or more."`
	Timeout         time.Duration `cfg:"timeout" default:"15s" desc:"Time limit for one request to Frankfurter (1s to 2m). A request that exceeds it counts as an outage: cached rates stay in use."`
}

// Merchants configures merchant identification (E2, ADR-0054). Decision D15:
// the baseline is a local curated mapping plus the person's confirmations, fully
// offline; the remote source is off and none is approved.
type Merchants struct {
	Enrichment MerchantEnrichment `cfg:"enrichment"`
}

// MerchantEnrichment configures the lookups that identify a merchant from bank text.
type MerchantEnrichment struct {
	Remote MerchantRemote `cfg:"remote"`
}

// MerchantRemote configures the optional remote merchant source. The adapter is
// provider-neutral; it speaks the small JSON contract in ADR-0054.
type MerchantRemote struct {
	Enabled      bool          `cfg:"enabled" default:"false" desc:"Ask a remote merchant-identification service about transactions the local curated mapping and your confirmations cannot identify. Off by default and no service is approved (decision D15). Turning it on sends one cleaned merchant string per transaction: the counterparty name only, never the description, with every word that holds a digit removed and the words iban, bic, swift and account removed (text is split at white space and at , / . and ;), at most 100 characters. Nothing is sent unless the name starts or ends, as a whole word in any case, with one of the company forms uab, mb, sia, ltd, limited, llc, gmbh, inc, plc, corp, srl, sarl, kft, oü next to at least one other word. The short forms as, co, sa, ab, ag, sp, oy, nv and bv never count, a bare form or a form plus punctuation sends nothing, and so do names of people, transfers (transfer words such as sepa or transfer) and everything else. Merchants your own confirmations or rejections already cover are not asked about. If merchants.enrichment.remote.country is set, that country code is sent too. Never amounts, dates, IBANs, account holders or balances. Needs url, and you are responsible for the service's terms: see attribution, store_logo_ref and retention."`
	URL          string        `cfg:"url" desc:"Lookup endpoint of the remote service (POST, JSON; see ADR-0054 for the contract). https only (plain http only for a loopback test server), no credentials or query. Required when enabled."`
	APIKeyFile   string        `cfg:"api_key_file" desc:"Path of a file with the bearer key for the remote service (trailing newlines ignored). The key is read at startup, sent only to the configured URL and never logged. Optional."`
	Country      string        `cfg:"country" desc:"Optional ISO 3166-1 alpha-2 country code sent with each lookup as a hint, for example LT. Empty means no country is sent."`
	Timeout      time.Duration `cfg:"timeout" default:"10s" desc:"Time limit for one lookup (1s to 1m). A failure or timeout leaves the transaction without a remote answer and never delays or fails an import."`
	Attribution  string        `cfg:"attribution" desc:"Attribution text the service's licence requires, for example 'Logos by Example Service'. Shown with identities that came from it (GET /api/v1/merchants/sources). Up to 200 characters."`
	StoreLogoRef bool          `cfg:"store_logo_ref" default:"false" desc:"Keep the logo reference the service returns. Leave false unless its licence allows storing or linking its logos: Ledger then shows the name with a neutral icon. Logos are never downloaded or copied by Ledger."`
	Retention    time.Duration `cfg:"retention" default:"720h" desc:"How long answers from the remote service are kept (the longest a licence usually allows is 30 days, the default; 24h to 8760h, or 0 to keep them until replaced). Older answers are deleted, so the transaction is looked up again; your confirmations and corrections are never deleted. The limit still applies after the remote source is turned off: answers it cached are deleted when they pass it (0 keeps them until replaced, which never happens once it is off)."`
}

// Banking configures banking-data providers.
type Banking struct {
	// RedirectURL is where a bank sends the user back after consent.
	RedirectURL   string        `cfg:"redirect_url" desc:"Where a bank sends you back after you approve access: Ledger's external address followed by /connections/callback, for example https://ledger.example.com/connections/callback. The same URL must be registered with the banking provider. https only (plain http only on loopback). Empty means connecting a bank (and reconnecting) is switched off; refreshing and disconnecting existing connections still work."`
	ConsentTTL    time.Duration `cfg:"consent_ttl" default:"1h" desc:"How long a started bank consent may wait for the bank's redirect. After it a completion is refused (409 consent_not_found) and a connection that never connected becomes failed (consent_expired). Between 1 minute and 24 hours."`
	StaleAfter    time.Duration `cfg:"stale_after" default:"24h" desc:"How old the last successful refresh of a bank account may be before its data is shown as stale (and the background scheduler refreshes it). Between 1 minute and 30 days."`
	EnableBanking EnableBanking `cfg:"enable_banking"`
	Refresh       BankRefresh   `cfg:"refresh"`
}

// BankRefresh configures the background refresh scheduler (ADR-0028). The
// defaults are proposals, not operator decisions.
type BankRefresh struct {
	Enabled         bool          `cfg:"enabled" default:"true" desc:"Refresh active bank connections in the background. When false, connections are only refreshed on request. Retries of one read and the provider's daily fetch budget are not configurable."`
	Interval        time.Duration `cfg:"interval" default:"6h" desc:"Time between successful background refreshes of a connection. Banks commonly allow about four background fetches a day; the scheduler also keeps to the provider's daily budget whatever this is set to."`
	RequestCooldown time.Duration `cfg:"request_cooldown" default:"5m" desc:"How long after a refresh of a bank connection finished (whoever started it, whatever the outcome) another refresh on request for that connection is refused with HTTP 429 and Retry-After. It applies to the API, the web UI, the CLI and MCP; the background refresh is not held back. 0s switches it off; at most 24h (decision D-B6c)."`
	MaxBackoff      time.Duration `cfg:"max_backoff" default:"6h" desc:"Longest wait after failed refreshes. The wait starts at 15 minutes and doubles per consecutive failure. A rate limit waits at least 6 hours, or the provider's Retry-After (at most 24 hours)."`
}

// EnableBanking configures Enable Banking applications (ADR-0023). There is
// one slot per mode (decision D10), and each bank connection records which
// application and mode it was made with.
type EnableBanking struct {
	BaseURL   string           `cfg:"base_url" default:"https://api.enablebanking.com" desc:"Enable Banking API base URL. Sandbox and production applications use the same host; the environment belongs to the application."`
	App       EnableBankingApp `cfg:"app"`
	Developer EnableBankingApp `cfg:"developer"`
}

// EnableBankingApp configures one Enable Banking application. The slot's
// key (app or developer) is its mode.
type EnableBankingApp struct {
	Enabled        bool   `cfg:"enabled" default:"false" desc:"Configure the Enable Banking application for this mode. app mode means an unrestricted production application (signed contract with Enable Banking). developer mode means a restricted production application that reaches only the accounts linked to it in the Enable Banking Control Panel. Neither mode is the sandbox."`
	Name           string `cfg:"name" desc:"Ledger's name for this application, recorded on every connection made with it: 1 to 64 lower-case letters, digits, '-' or '_'. Empty means enablebanking-app or enablebanking-developer. Changing it orphans existing connections."`
	AppID          string `cfg:"app_id" desc:"Application id from the Enable Banking Control Panel (a UUID, sent as the JWT kid). Required when the application is enabled. Never logged."`
	Environment    string `cfg:"environment" default:"production" enum:"production,sandbox" desc:"Environment the application was registered in at Enable Banking. sandbox is for testing with simulated banks; it is not a mode."`
	PrivateKey     string `cfg:"private_key" secret:"true" desc:"PEM-encoded RSA private key of the application (in .env, one double-quoted line with \\n for line breaks). Secret: environment only, never in the YAML file and never a flag. Set this or private_key_file."`
	PrivateKeyFile string `cfg:"private_key_file" desc:"Path of the PEM-encoded RSA private key file of the application. Alternative to the private_key variable. Only the path is configuration; the key is never logged."`
}

// Slots returns the Enable Banking application slots with their mode names.
func (e EnableBanking) Slots() []struct {
	Mode string
	App  EnableBankingApp
} {
	return []struct {
		Mode string
		App  EnableBankingApp
	}{{"app", e.App}, {"developer", e.Developer}}
}

// KEK configures the key-encryption key that protects stored credentials. It
// is required: set exactly one of kek.value (environment only) and kek.file.
type KEK struct {
	Value string `cfg:"value" secret:"true" desc:"Key-encryption key for stored credentials (TOTP secrets are encrypted with a key derived from it). Use a long random value, for example 32 random bytes in base64. Changing it makes enrolled authenticators unusable: users then sign in with a recovery code, or the host resets their two-factor authentication. Required unless LEDGER_KEK_FILE is set. Secret: environment only, never in the YAML file and never a flag."`
	File  string `cfg:"file" desc:"Path of a file that contains the key-encryption key. Alternative to LEDGER_KEK_VALUE; trailing newlines are ignored."`
}

// Decisions configures the model-assisted decision layer (L1, ADR-0057,
// decision D13). Every task is off by default and nothing is sent anywhere
// while it is off.
type Decisions struct {
	Providers DecisionProviders `cfg:"providers"`
	Tasks     DecisionTasks     `cfg:"tasks"`
}

// DecisionProviders holds each decision provider's credentials.
type DecisionProviders struct {
	OpenRouter DecisionOpenRouter `cfg:"openrouter"`
	OpenAI     DecisionProvider   `cfg:"openai"`
	Anthropic  DecisionProvider   `cfg:"anthropic"`
	TypeSafe   DecisionProvider   `cfg:"typesafe"`
}

// DecisionProvider is a provider's API key.
type DecisionProvider struct {
	APIKey     string `cfg:"api_key" secret:"true" desc:"API key of this decision provider. Secret: environment only, never in the YAML file and never a flag. Set this or api_key_file. Required when an enabled decision task (or its fallback) uses the provider; never logged or sent anywhere but to the provider."`
	APIKeyFile string `cfg:"api_key_file" desc:"Path of a file that contains the API key of this decision provider. Alternative to the api_key variable; trailing newlines are ignored. Only the path is configuration; the key is never logged."`
}

// DecisionOpenRouter is OpenRouter's key and the upstream providers it may
// forward to.
type DecisionOpenRouter struct {
	APIKey           string   `cfg:"api_key" secret:"true" desc:"OpenRouter API key. Secret: environment only, never in the YAML file and never a flag. Set this or api_key_file. Required when an enabled decision task (or its fallback) uses OpenRouter."`
	APIKeyFile       string   `cfg:"api_key_file" desc:"Path of a file that contains the OpenRouter API key. Alternative to the api_key variable; trailing newlines are ignored."`
	AllowedUpstreams []string `cfg:"allowed_upstreams" desc:"Upstream providers OpenRouter may forward Ledger's requests to (a list in YAML, comma separated in the variable), for example anthropic. Empty (the default) means downstream sharing is disabled and no task may use OpenRouter (decision D13). Consent for another provider never covers an upstream reached through OpenRouter."`
}

// DecisionTasks holds one slot per decision task.
type DecisionTasks struct {
	Categorisation  DecisionTask `cfg:"categorisation"`
	LabelSuggestion DecisionTask `cfg:"label_suggestion"`
	Alerts          DecisionTask `cfg:"alerts"`
	PaymentMatching DecisionTask `cfg:"payment_matching"`
}

// DecisionTask is one task's provider, model and limits (decision D13).
type DecisionTask struct {
	Enabled         bool          `cfg:"enabled" default:"false" desc:"Ask a decision provider for suggestions for this task. Off by default. Even when on, nothing is sent until consent for this provider and this purpose is recorded; consent for one provider or purpose never covers another. Suggestions are kept apart from bank facts and are only suggestions: a failure never changes anything."`
	Provider        string        `cfg:"provider" desc:"Decision provider for this task: openrouter, openai, anthropic or typesafe. Required when the task is enabled. typesafe serves decide tasks only (categorisation, label_suggestion, payment_matching), not alerts."`
	Model           string        `cfg:"model" desc:"Model identifier at the provider. Required when the task is enabled. The model the provider reports is recorded with every suggestion."`
	Capability      string        `cfg:"capability" desc:"Capability the task needs: decide (a choice among candidates: categorisation, label_suggestion, payment_matching) or generate (free text: alerts). Empty means the task's own; any other value than the task's is refused."`
	Timeout         time.Duration `cfg:"timeout" default:"30s" desc:"Time limit for one run of the task on one provider, retries included (1s to 2m). A run that exceeds it ends without a suggestion and changes nothing."`
	MonthlyRequests int           `cfg:"monthly_requests" default:"500" desc:"Most requests the task may send per calendar month (UTC), every attempt counted, retries and fallback providers included (1 to 1000000). Once reached, the task makes no suggestions until the next month. The default of 500 is a proposal awaiting the operator."`
	Retries         int           `cfg:"retries" default:"2" desc:"Retries of a request the provider refused before generating anything (rate limit, overload: 0 to 2, decision D13), honouring Retry-After. A response that was generated, even a broken one, is never retried."`
	AutoApply       bool          `cfg:"auto_apply" default:"false" desc:"Mark suggestions for automatic application instead of review. Allowed for categorisation only (decision D13); an applied suggestion is always reversible and never replaces a person's choice."`
	Fields          []string      `cfg:"fields" desc:"Fields this task may send to its provider (a list in YAML, comma separated in the variable), from description, amount, currency, booking_date, merchant_name and candidate_names. Empty means the provider's default: all of them for openrouter, openai and anthropic (generate tasks have no candidates); description, merchant_name and candidate_names for typesafe. iban, account_number, counterparty_name and balance are never sent and are refused here. A decide task needs candidate_names. Applies to the main provider; fallback providers use their defaults."`
	Fallback        []string      `cfg:"fallback" desc:"Explicit fallback providers, as provider:model entries tried in order only when the one before had an outage (a list in YAML, comma separated in the variable). Empty (the default) means no fallback: there is never an automatic one. Each needs its own consent and key."`
}
