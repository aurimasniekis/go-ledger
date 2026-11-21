package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

// Sealer encrypts small secrets for storage. aad binds a ciphertext to its
// context (the user it belongs to), so a sealed value copied to another row
// does not open. Open returns [ErrUnsealable] for anything it cannot decrypt.
type Sealer interface {
	Seal(plaintext, aad []byte) (string, error)
	Open(sealed string, aad []byte) ([]byte, error)
}

// ErrUnsealable is returned for a sealed value that cannot be decrypted: the
// key-encryption key changed, or the value is corrupt.
var ErrUnsealable = errors.New("auth: a stored secret cannot be decrypted (was the key-encryption key changed?)")

// sealPrefix versions the sealed format: "v1." then base64url(nonce ||
// AES-256-GCM ciphertext and tag).
const sealPrefix = "v1."

type gcmSealer struct{ aead cipher.AEAD }

// NewKEKSealer returns a [Sealer] with an AES-256-GCM key derived from the
// configured key-encryption key with HKDF-SHA256, using purpose as the HKDF
// info, so each kind of secret has its own key. HKDF does not stretch a weak
// key: the KEK must be long and random (ADR-0030).
func NewKEKSealer(kek, purpose string) (Sealer, error) {
	if kek == "" {
		return nil, errors.New("auth: a key-encryption key is required")
	}
	key, err := hkdf.Key(sha256.New, []byte(kek), nil, purpose, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcmSealer{aead: aead}, nil
}

func (g gcmSealer) Seal(plaintext, aad []byte) (string, error) {
	nonce := make([]byte, g.aead.NonceSize())
	_, _ = rand.Read(nonce) // never fails (Go 1.24+)
	out := g.aead.Seal(nonce, nonce, plaintext, aad)
	return sealPrefix + base64.RawURLEncoding.EncodeToString(out), nil
}

func (g gcmSealer) Open(sealed string, aad []byte) ([]byte, error) {
	body, ok := strings.CutPrefix(sealed, sealPrefix)
	if !ok {
		return nil, ErrUnsealable
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil || len(raw) < g.aead.NonceSize()+g.aead.Overhead() {
		return nil, ErrUnsealable
	}
	n := g.aead.NonceSize()
	pt, err := g.aead.Open(nil, raw[:n], raw[n:], aad)
	if err != nil {
		return nil, ErrUnsealable
	}
	return pt, nil
}
