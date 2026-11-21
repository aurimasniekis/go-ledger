package enablebanking

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"sync"
	"time"
)

// JWT claims Enable Banking requires (research R1, section 3).
const (
	jwtIssuer   = "enablebanking.com"
	jwtAudience = "api.enablebanking.com"
)

// signer makes and caches the application JWT. A token is reused until five
// minutes (or a tenth of its lifetime, if shorter) before it expires.
type signer struct {
	kid string
	key *rsa.PrivateKey
	ttl time.Duration
	now func() time.Time

	mu      sync.Mutex
	token   string
	renewAt time.Time
}

type jwtHeader struct {
	Typ string `json:"typ"`
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

type jwtClaims struct {
	Iss string `json:"iss"`
	Aud string `json:"aud"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
}

func (s *signer) bearer() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.token != "" && now.Before(s.renewAt) {
		return s.token, nil
	}
	tok, err := signJWT(s.kid, s.key, now, s.ttl)
	if err != nil {
		return "", err
	}
	margin := min(5*time.Minute, s.ttl/10)
	s.token, s.renewAt = tok, now.Add(s.ttl-margin)
	return tok, nil
}

func signJWT(kid string, key *rsa.PrivateKey, now time.Time, ttl time.Duration) (string, error) {
	h, err := json.Marshal(jwtHeader{Typ: "JWT", Alg: "RS256", Kid: kid})
	if err != nil {
		return "", err
	}
	c, err := json.Marshal(jwtClaims{Iss: jwtIssuer, Aud: jwtAudience, Iat: now.Unix(), Exp: now.Add(ttl).Unix()})
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	signing := enc.EncodeToString(h) + "." + enc.EncodeToString(c)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", errSigning
	}
	return signing + "." + enc.EncodeToString(sig), nil
}
