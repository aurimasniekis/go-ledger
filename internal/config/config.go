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
	Auth      Auth      `cfg:"auth"`
}

// Auth configures password sign-in and sessions (ADR-0026). The defaults are
// proposals, not operator decisions yet.
type Auth struct {
	Password AuthPassword `cfg:"password"`
	Session  AuthSession  `cfg:"session"`
	SignIn   AuthSignIn   `cfg:"sign_in"`
}

// AuthPassword configures the password policy.
type AuthPassword struct {
	MinLength int `cfg:"min_length" default:"12" desc:"Minimum password length in bytes (1 to 1024). Length is the only rule: there are no composition rules."`
}

// AuthSession configures sign-in sessions.
type AuthSession struct {
	IdleTimeout     time.Duration `cfg:"idle_timeout" default:"30m" desc:"A session not used for this long ends. Must not exceed the absolute timeout."`
	AbsoluteTimeout time.Duration `cfg:"absolute_timeout" default:"12h" desc:"A session ends this long after sign-in, however much it is used."`
	ReauthWindow    time.Duration `cfg:"reauth_window" default:"10m" desc:"How long a password check counts as recent for sensitive actions (credential changes, token creation, user management). After it, the user confirms their password again."`
}

// AuthSignIn configures sign-in rate limits. They are kept in memory and
// reset when the server restarts.
type AuthSignIn struct {
	AccountMaxFailures int           `cfg:"account_max_failures" default:"10" desc:"Failed sign-ins (and re-authentications) allowed per username within auth.sign_in.window before further attempts for it are refused with 429."`
	AddressMaxFailures int           `cfg:"address_max_failures" default:"30" desc:"Failed sign-ins allowed per client address (an IPv6 address counts as its /64) within auth.sign_in.window before further attempts from it are refused with 429."`
	Window             time.Duration `cfg:"window" default:"15m" desc:"Sliding window over which failed sign-ins are counted."`
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
	TrustedProxies    []string      `cfg:"trusted_proxies" desc:"IP addresses or CIDR ranges of reverse proxies whose forwarding headers are trusted (a list in YAML, comma separated in the variable). Empty trusts none."`
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

// Banking configures banking-data providers.
type Banking struct {
	EnableBanking EnableBanking `cfg:"enable_banking"`
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
	Value string `cfg:"value" secret:"true" desc:"Key-encryption key for stored credentials. Required unless LEDGER_KEK_FILE is set. Secret: environment only, never in the YAML file and never a flag."`
	File  string `cfg:"file" desc:"Path of a file that contains the key-encryption key. Alternative to LEDGER_KEK_VALUE; trailing newlines are ignored."`
}
