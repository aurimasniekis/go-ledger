package oidc

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// Defaults (ADR-0033 point 13, accepted by the operator 2026-11-21).
const (
	// TransactionTTL is how long an authorization request may take, from
	// leaving Ledger to the callback.
	TransactionTTL = 10 * time.Minute
	// MaxTransactions bounds the pending requests kept in memory; when full,
	// the one expiring first is dropped (that user starts again).
	MaxTransactions = 1000
	// MaxStateLength bounds a presented state value.
	MaxStateLength = 128
)

// Modes of a transaction.
const (
	ModeSignIn = "sign_in"
	ModeLink   = "link"
	ModeReauth = "reauth"
)

// Transaction is one pending authorization request: what the callback needs
// to finish it. It lives only in memory (a restart discards it) and is used
// at most once.
type Transaction struct {
	Mode string
	// UserID and SessionID are the signed-in user and session that started a
	// link or re-authentication.
	UserID    string
	SessionID string
	// ReturnTo is a same-origin path to send the browser to afterwards.
	ReturnTo  string
	Nonce     string
	Verifier  string
	CreatedAt time.Time
}

// Transactions holds pending authorization requests keyed by the hash of
// their state. Safe for concurrent use.
type Transactions struct {
	mu  sync.Mutex
	m   map[string]Transaction
	now func() time.Time
}

// NewTransactions returns an empty store; now nil means time.Now.
func NewTransactions(now func() time.Time) *Transactions {
	if now == nil {
		now = time.Now
	}
	return &Transactions{m: map[string]Transaction{}, now: now}
}

func random() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // never fails (Go 1.24+)
	return base64.RawURLEncoding.EncodeToString(b)
}

func stateKey(state string) string {
	sum := sha256.Sum256([]byte(state))
	return hex.EncodeToString(sum[:])
}

// Begin stores t with a fresh nonce and PKCE verifier and returns it with
// its new state (a 256-bit random value).
func (s *Transactions) Begin(t Transaction) (string, Transaction) {
	t.Nonce, t.Verifier, t.CreatedAt = random(), oauth2.GenerateVerifier(), s.now().UTC()
	state := random()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.m) >= MaxTransactions {
		for k, x := range s.m {
			if s.expired(x) {
				delete(s.m, k)
			}
		}
		for len(s.m) >= MaxTransactions {
			var victim string
			var at time.Time
			for k, x := range s.m {
				if victim == "" || x.CreatedAt.Before(at) {
					victim, at = k, x.CreatedAt
				}
			}
			delete(s.m, victim)
		}
	}
	s.m[stateKey(state)] = t
	return state, t
}

func (s *Transactions) expired(t Transaction) bool {
	return !s.now().Before(t.CreatedAt.Add(TransactionTTL))
}

// Take removes and returns the transaction of state when it is pending and
// cookie (the state the browser was given) matches it. A state is accepted
// once: a replayed callback finds nothing.
func (s *Transactions) Take(state, cookie string) (Transaction, bool) {
	if state == "" || len(state) > MaxStateLength || subtle.ConstantTimeCompare([]byte(state), []byte(cookie)) != 1 {
		return Transaction{}, false
	}
	k := stateKey(state)
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.m[k]
	if !ok {
		return Transaction{}, false
	}
	delete(s.m, k)
	if s.expired(t) {
		return Transaction{}, false
	}
	return t, true
}

// Len is the number of pending transactions (tests).
func (s *Transactions) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.m)
}
