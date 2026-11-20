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

// KEK configures the key-encryption key that protects stored credentials. It
// is required: set exactly one of kek.value (environment only) and kek.file.
type KEK struct {
	Value string `cfg:"value" secret:"true" desc:"Key-encryption key for stored credentials. Required unless LEDGER_KEK_FILE is set. Secret: environment only, never in the YAML file and never a flag."`
	File  string `cfg:"file" desc:"Path of a file that contains the key-encryption key. Alternative to LEDGER_KEK_VALUE; trailing newlines are ignored."`
}
