package banking

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"
)

// KeyKind says how a transaction's idempotency key was built.
type KeyKind string

const (
	// KeyEntryReference: from the provider's entry reference.
	KeyEntryReference KeyKind = "entry_reference"
	// KeyFallback: from the transaction's content, because the provider gave
	// no entry reference.
	KeyFallback KeyKind = "fallback"
)

// KeyedTransaction is a transaction with the idempotency key it is stored
// under. Within an account, one key is one stored transaction: importing the
// same transaction again updates that row instead of adding another.
type KeyedTransaction struct {
	Transaction
	Key     string
	KeyKind KeyKind
}

// KeyTransactions computes idempotency keys for the transactions of one
// account, fetched for one date window (all pages of it together).
//
// The key is a hash, so it holds no description or counterparty text:
//
//   - With an entry reference: "ref:" + hash(entry reference, amount,
//     currency) + "#n". The amount is part of the key because Enable Banking
//     documents that some banks repeat entry references; two transactions
//     that share a reference but differ in amount stay apart. Dates and
//     status are not part of it, so a pending transaction that keeps its
//     reference becomes booked in place.
//   - Without one: "fb:" + hash(date, amount, currency, description,
//     counterparty) + "#n". Status is not part of it, so a pending item
//     whose content does not change becomes booked in place; one whose date,
//     amount or text changes on booking becomes a new row, and the pending
//     row is later marked no longer reported (B2, decision D8).
//
// n numbers transactions with the same hash in the order given, from 1. It
// separates genuinely identical transactions (two equal coffees on one day)
// and is stable as long as every fetch covers whole days and returns
// identical items in the same relative order, so callers must key a whole
// window at once, not page by page. A provider that drops one of two
// identical pending items can shift n; that limitation is recorded in
// ADR-0013.
//
// It returns an error, naming the position only, for a transaction that
// fails [Transaction.Validate].
func KeyTransactions(txs []Transaction) ([]KeyedTransaction, error) {
	out := make([]KeyedTransaction, len(txs))
	seen := map[string]int{}
	for i, t := range txs {
		if err := t.Validate(); err != nil {
			return nil, fmt.Errorf("transaction %d: %w", i, err)
		}
		var base string
		kind := KeyFallback
		if t.EntryReference != "" {
			kind = KeyEntryReference
			base = "ref:" + hashFields(t.EntryReference, t.Amount.Amount().String(), t.Amount.Currency().Code())
		} else {
			base = "fb:" + hashFields(t.Date().String(), t.Amount.Amount().String(), t.Amount.Currency().Code(), t.Description, t.Counterparty)
		}
		seen[base]++
		out[i] = KeyedTransaction{Transaction: t, Key: base + "#" + strconv.Itoa(seen[base]), KeyKind: kind}
	}
	return out, nil
}

// hashFields hashes length-prefixed fields, so ("ab","c") and ("a","bc")
// differ. 128 bits of SHA-256 are plenty for one account's transactions.
func hashFields(fields ...string) string {
	h := sha256.New()
	var n [8]byte
	for _, f := range fields {
		binary.BigEndian.PutUint64(n[:], uint64(len(f)))
		h.Write(n[:])
		h.Write([]byte(f))
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}
