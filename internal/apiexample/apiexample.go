// Package apiexample is an example resource that shows the Management API
// conventions of ADR-0011 working end to end: ULID identifiers, a list with
// cursor pagination and filters, optimistic concurrency with ETag, If-Match,
// 409 and 428, and its OpenAPI description checked by the contract test.
//
// It is an example only. ledger serve does not register it, its table is not
// in the embedded migrations (tests append it), and its routes are not part
// of the published API. Real resources copy its shape in their own packages,
// with the HTTP, domain and store parts split as ADR-0001 requires; this
// package keeps them together because it exists only to be read and tested.
package apiexample

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/httpapi"
	"github.com/aurimasniekis/go-ledger/internal/store"
)

// OpenAPIPaths documents the example routes. Its paths and schemas are merged
// into the published document by the example's contract test only.
//
//go:embed openapi.yaml
var OpenAPIPaths []byte

// Example is the example resource.
type Example struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Note is optional: a merge patch clears it with null.
	Note      *string   `json:"note"`
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"created_at"`
}

var kinds = map[string]bool{"alpha": true, "beta": true}

const table = "api_examples"

// Migrations returns the embedded migrations followed by the example's table.
func Migrations() ([]store.Migration, error) {
	ms, err := store.EmbeddedMigrations()
	if err != nil {
		return nil, err
	}
	m := store.Migration{
		Version: len(ms) + 1,
		Name:    "api_examples",
		SQL: `CREATE TABLE api_examples (
    id         TEXT PRIMARY KEY CHECK (` + store.IDCheck("id") + `),
    name       TEXT NOT NULL CHECK (name <> ''),
    kind       TEXT NOT NULL CHECK (kind IN ('alpha', 'beta')),
    note       TEXT CHECK (note IS NULL OR length(note) BETWEEN 1 AND 200),
    version    INTEGER NOT NULL DEFAULT 1 CHECK (` + store.VersionCheck + `),
    created_at TEXT NOT NULL CHECK (` + store.TimestampCheck("created_at") + `)
) STRICT;`,
	}
	sum := sha256.Sum256([]byte(m.SQL))
	m.Checksum = hex.EncodeToString(sum[:])
	return append(ms, m), nil
}

// Store persists examples.
type Store struct {
	DB  *store.DB
	Now func() time.Time
}

const columns = "id, name, kind, note, version, created_at"

func scan(row interface{ Scan(...any) error }) (Example, error) {
	var e Example
	var at store.Timestamp
	if err := row.Scan(&e.ID, &e.Name, &e.Kind, &e.Note, &e.Version, &at); err != nil {
		return Example{}, err
	}
	e.CreatedAt = at.Time
	return e, nil
}

// Create inserts a new example at version 1.
func (s *Store) Create(ctx context.Context, name, kind string) (Example, error) {
	e := Example{ID: resource.NewID(), Name: name, Kind: kind, Version: 1, CreatedAt: s.Now().UTC()}
	err := s.DB.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO "+table+" (id, name, kind, created_at) VALUES (?, ?, ?, ?)",
			e.ID, e.Name, e.Kind, store.Timestamp{Time: e.CreatedAt})
		return err
	})
	return e, err
}

// Get returns one example or resource.ErrNotFound.
func (s *Store) Get(ctx context.Context, id string) (Example, error) {
	var e Example
	err := s.DB.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		e, err = scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM "+table+" WHERE id = ?", id))
		if errors.Is(err, sql.ErrNoRows) {
			return resource.ErrNotFound
		}
		return err
	})
	return e, err
}

// List returns up to q.Limit+1 examples after q.After in ID order, filtered
// by kind (any of) and name prefix.
func (s *Store) List(ctx context.Context, q httpapi.ListQuery) ([]Example, error) {
	where := []string{"id > ?"}
	args := []any{q.After}
	if ks := q.Filter("kind", httpapi.OpEq); len(ks) > 0 {
		where = append(where, "kind IN (?"+strings.Repeat(", ?", len(ks)-1)+")")
		for _, k := range ks {
			args = append(args, k)
		}
	}
	if p := q.Filter("name", httpapi.OpPrefix); len(p) == 1 {
		// substr, not LIKE: no wildcard escaping, and the match is exact.
		where = append(where, "substr(name, 1, length(?)) = ?")
		args = append(args, p[0], p[0])
	}
	args = append(args, q.Limit+1)
	var out []Example
	err := s.DB.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT "+columns+" FROM "+table+" WHERE "+strings.Join(where, " AND ")+" ORDER BY id LIMIT ?", args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			e, err := scan(rows)
			if err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

// Patch is a validated partial update (JSON Merge Patch semantics): a nil
// Name or Kind leaves the column unchanged; SetNote with a nil Note clears the
// note, SetNote with a value replaces it, and no SetNote leaves it unchanged.
type Patch struct {
	Name, Kind *string
	SetNote    bool
	Note       *string
}

// Update applies p when the example still has version expected, and returns
// it as stored.
func (s *Store) Update(ctx context.Context, id string, expected int64, p Patch) (Example, error) {
	var e Example
	err := s.DB.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		set := "name = coalesce(?, name), kind = coalesce(?, kind), note = CASE WHEN ? THEN ? ELSE note END"
		if _, err := store.UpdateVersioned(ctx, tx, table, set, id, expected, p.Name, p.Kind, p.SetNote, p.Note); err != nil {
			return err
		}
		var err error
		e, err = scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM "+table+" WHERE id = ?", id))
		return err
	})
	return e, err
}

// Delete removes the example when it still has version expected.
func (s *Store) Delete(ctx context.Context, id string, expected int64) error {
	return s.DB.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return store.DeleteVersioned(ctx, tx, table, id, expected)
	})
}

// Path is the example collection's path.
const Path = httpapi.APIPrefix + "/examples"

var listSpec = httpapi.ListSpec{Filters: map[string]httpapi.FilterSpec{
	"kind": {Ops: []httpapi.FilterOp{httpapi.OpEq}, Check: func(v string) error {
		if !kinds[v] {
			return errors.New("kind must be alpha or beta")
		}
		return nil
	}},
	"name": {Ops: []httpapi.FilterOp{httpapi.OpPrefix}},
}}

// Register adds the example routes to srv.
func Register(srv *httpapi.Server, st *Store) {
	srv.Handle("GET "+Path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, err := httpapi.ParseList(r, listSpec)
		if err != nil {
			httpapi.WriteError(w, r, err)
			return
		}
		items, err := st.List(r.Context(), q)
		if err != nil {
			httpapi.WriteError(w, r, err)
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, httpapi.NewPage(items, q, func(e Example) string { return e.ID }))
	}))
	srv.Handle("POST "+Path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name *string `json:"name"`
			Kind *string `json:"kind"`
		}
		if err := httpapi.DecodeJSON(r, &in); err != nil {
			httpapi.WriteError(w, r, err)
			return
		}
		if in.Name == nil {
			httpapi.WriteError(w, r, httpapi.Validation("name", "name is required"))
			return
		}
		if in.Kind == nil {
			httpapi.WriteError(w, r, httpapi.Validation("kind", "kind is required"))
			return
		}
		if err := validate(in.Name, in.Kind); err != nil {
			httpapi.WriteError(w, r, err)
			return
		}
		e, err := st.Create(r.Context(), *in.Name, *in.Kind)
		if err != nil {
			httpapi.WriteError(w, r, err)
			return
		}
		w.Header().Set("Location", Path+"/"+e.ID)
		httpapi.SetVersion(w, e.Version)
		httpapi.WriteJSON(w, http.StatusCreated, e)
	}))
	srv.Handle("GET "+Path+"/{id}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e, err := st.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			httpapi.WriteError(w, r, err)
			return
		}
		httpapi.SetVersion(w, e.Version)
		httpapi.WriteJSON(w, http.StatusOK, e)
	}))
	// PATCH is a JSON Merge Patch (RFC 7396, ADR-0015): an absent member is
	// unchanged, null clears an optional member (note), and null on a required
	// member is a 400. The optional "version" member is the precondition, not
	// data, and is not stored.
	srv.Handle("PATCH "+Path+"/{id}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		patch, err := httpapi.DecodeMergePatch(r, "name", "kind", "note", "version")
		if err != nil {
			httpapi.WriteError(w, r, err)
			return
		}
		var bodyVersion *int64
		if patch.Has("version") {
			var v int64
			if patch.IsNull("version") || patch.Decode("version", &v) != nil {
				httpapi.WriteError(w, r, httpapi.Validation("version", "version must be a positive integer"))
				return
			}
			bodyVersion = &v
		}
		expected, err := httpapi.ExpectedVersion(r, bodyVersion)
		if err != nil {
			httpapi.WriteError(w, r, err)
			return
		}
		in, err := parsePatch(patch)
		if err != nil {
			httpapi.WriteError(w, r, err)
			return
		}
		id := r.PathValue("id")
		e, err := st.Update(r.Context(), id, expected, in)
		if err != nil {
			writeWriteError(w, r, st, id, err)
			return
		}
		httpapi.SetVersion(w, e.Version)
		httpapi.WriteJSON(w, http.StatusOK, e)
	}))
	srv.Handle("DELETE "+Path+"/{id}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// DELETE takes the version from If-Match only (ADR-0015): it has no body.
		expected, err := httpapi.ExpectedVersion(r, nil)
		if err != nil {
			httpapi.WriteError(w, r, err)
			return
		}
		id := r.PathValue("id")
		if err := st.Delete(r.Context(), id, expected); err != nil {
			writeWriteError(w, r, st, id, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
}

// writeWriteError answers a failed write. A version conflict carries the
// current object so the client can reconcile; a caller that may write may
// also read it here.
func writeWriteError(w http.ResponseWriter, r *http.Request, st *Store, id string, err error) {
	var vc *resource.VersionConflictError
	if errors.As(err, &vc) {
		if cur, gerr := st.Get(r.Context(), id); gerr == nil {
			httpapi.WriteError(w, r, httpapi.VersionConflict(cur.Version, cur))
			return
		}
	}
	httpapi.WriteError(w, r, err)
}

func parsePatch(p httpapi.MergePatch) (Patch, error) {
	var out Patch
	for _, f := range []struct {
		field string
		dst   **string
	}{{"name", &out.Name}, {"kind", &out.Kind}} {
		if !p.Has(f.field) {
			continue
		}
		if p.IsNull(f.field) {
			return Patch{}, httpapi.Validation(f.field, f.field+" is required and cannot be cleared")
		}
		var v string
		if err := p.Decode(f.field, &v); err != nil {
			return Patch{}, err
		}
		*f.dst = &v
	}
	if p.Has("note") {
		out.SetNote = true
		if !p.IsNull("note") {
			var v string
			if err := p.Decode("note", &v); err != nil {
				return Patch{}, err
			}
			if n := utf8.RuneCountInString(v); n < 1 || n > 200 || !utf8.ValidString(v) {
				return Patch{}, httpapi.Validation("note", fmt.Sprintf("note must be 1 to 200 characters, or null to clear it, got %d", n))
			}
			out.Note = &v
		}
	}
	if err := validate(out.Name, out.Kind); err != nil {
		return Patch{}, err
	}
	return out, nil
}

func validate(name, kind *string) error {
	if name != nil {
		if n := utf8.RuneCountInString(*name); n < 1 || n > 100 || !utf8.ValidString(*name) {
			return httpapi.Validation("name", fmt.Sprintf("name must be 1 to 100 characters, got %d", n))
		}
	}
	if kind != nil && !kinds[*kind] {
		return httpapi.Validation("kind", "kind must be alpha or beta")
	}
	return nil
}
