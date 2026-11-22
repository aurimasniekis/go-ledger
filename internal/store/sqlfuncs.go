package store

import (
	"database/sql/driver"
	"errors"
	"fmt"

	"modernc.org/sqlite"

	"github.com/aurimasniekis/go-ledger/internal/domain/merchants"
)

// SQL functions the migrations may call. They are registered with the driver
// for every connection the process opens. A function a migration calls must
// stay registered, with the same results, for as long as that migration ships.
func init() {
	// ledger_merchant_key(name) is merchants.AliasKey: the key a merchant
	// alias is matched by (NFC, white space collapsed, lower case), or '' for
	// a name that is not valid display text. Migration 0030 builds alias keys
	// with it, so they equal the keys the store computes in Go.
	sqlite.MustRegisterDeterministicScalarFunction("ledger_merchant_key", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		var s string
		switch v := args[0].(type) {
		case string:
			s = v
		case []byte:
			s = string(v)
		case nil:
			return "", nil
		default:
			return nil, fmt.Errorf("ledger_merchant_key: want text, got %T", v)
		}
		k, ok := merchants.AliasKey(s)
		if !ok {
			return "", nil
		}
		return k, nil
	})
	// ledger_refuse(message) fails the statement with message: a data
	// migration that would lose data refuses with a report instead (SQLite's
	// RAISE only works inside triggers).
	sqlite.MustRegisterScalarFunction("ledger_refuse", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		msg, _ := args[0].(string)
		if msg == "" {
			msg = "refused"
		}
		return nil, errors.New(msg)
	})
}
