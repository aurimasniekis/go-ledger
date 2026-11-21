package enablebanking

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
)

// ProviderName is the adapter's provider name in capabilities and bank facts.
const ProviderName = "enablebanking"

// DefaultBaseURL is the Enable Banking API host. The documentation uses the
// same host for sandbox and production applications; the environment comes
// from the application (research R1, section 3).
const DefaultBaseURL = "https://api.enablebanking.com"

// MaxTokenTTL is the longest JWT lifetime Enable Banking accepts.
const MaxTokenTTL = 24 * time.Hour

// DefaultTokenTTL is how long a signed JWT is used before a new one is made.
const DefaultTokenTTL = time.Hour

// DefaultRequestTimeout bounds one HTTP call when no client is given.
const DefaultRequestTimeout = 60 * time.Second

// defaultMaxResponseBytes bounds a response body.
const defaultMaxResponseBytes = 16 << 20

// RateLimitBackoff is the delay Enable Banking recommends after a bank's
// rate limit hit a background fetch (research R1, section 7). It is used as
// RetryAfter when the response names none.
const RateLimitBackoff = 6 * time.Hour

// Config configures one Enable Banking application.
type Config struct {
	// Application is the operator's name for this application, unique among
	// the configured Enable Banking applications; it is recorded on every
	// connection.
	Application string
	// AppID is the application id from the Enable Banking Control Panel,
	// sent as the JWT kid. Never logged.
	AppID string
	// Mode is the access mode (D10).
	Mode banking.Mode
	// Environment is the environment the application was registered in.
	Environment banking.Environment
	// PrivateKey signs the JWTs. Never logged.
	PrivateKey *rsa.PrivateKey

	// BaseURL defaults to [DefaultBaseURL]; tests point it at a test server.
	BaseURL string
	// HTTPClient defaults to a client with [DefaultRequestTimeout].
	HTTPClient *http.Client
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// TokenTTL defaults to [DefaultTokenTTL]; at most [MaxTokenTTL].
	TokenTTL time.Duration
	// MaxResponseBytes bounds a response body; 0 means 16 MiB.
	MaxResponseBytes int64
}

var (
	appIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	namePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
)

// ValidAppID reports whether s has the form of an Enable Banking
// application id (a UUID).
func ValidAppID(s string) bool { return appIDPattern.MatchString(s) }

func (c Config) validate() error {
	switch {
	case !namePattern.MatchString(c.Application):
		return errors.New("enablebanking: application name must be 1 to 64 lower-case letters, digits, '-' or '_'")
	case !ValidAppID(c.AppID):
		return errors.New("enablebanking: application id must be a UUID")
	case !c.Mode.Valid():
		return errors.New("enablebanking: mode must be app or developer")
	case !c.Environment.Valid():
		return errors.New("enablebanking: environment must be production or sandbox")
	case c.PrivateKey == nil:
		return errors.New("enablebanking: private key is required")
	case c.TokenTTL < 0 || c.TokenTTL > MaxTokenTTL:
		return fmt.Errorf("enablebanking: token lifetime must be at most %s", MaxTokenTTL)
	}
	return nil
}

// ParsePrivateKey reads an RSA private key in PEM form (PKCS#8 "PRIVATE KEY"
// or PKCS#1 "RSA PRIVATE KEY"). Errors never contain key material.
func ParsePrivateKey(data []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("enablebanking: private key is not PEM encoded")
	}
	var key any
	var err error
	switch block.Type {
	case "PRIVATE KEY":
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	default:
		return nil, errors.New("enablebanking: private key PEM block must be PRIVATE KEY or RSA PRIVATE KEY")
	}
	if err != nil {
		return nil, errors.New("enablebanking: private key cannot be parsed")
	}
	rk, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("enablebanking: private key must be an RSA key (Enable Banking accepts RS256 only)")
	}
	if rk.N.BitLen() < 2048 {
		return nil, errors.New("enablebanking: RSA private key must have at least 2048 bits")
	}
	return rk, nil
}
