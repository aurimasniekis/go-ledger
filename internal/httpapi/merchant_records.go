package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/merchants"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
)

// Error codes of the merchant record routes (task M1, ADR-0057). A stale
// version is the usual version_conflict.
const (
	// CodeMerchantAliasTaken (409): the alias (or the new display name) already
	// names another merchant; merge the two, or remove the alias there.
	CodeMerchantAliasTaken Code = "merchant_alias_taken"
	// CodeMerchantArchived (409): the merchant is archived or merged, and
	// nothing new is attached to it.
	CodeMerchantArchived Code = "merchant_archived"
	// CodeMerchantInUse (409): decisions or assignments refer to the merchant,
	// so it cannot be deleted; archive or merge it.
	CodeMerchantInUse Code = "merchant_in_use"
)

type merchantAliasJSON struct {
	ID        string                `json:"id"`
	Name      string                `json:"name"`
	Key       string                `json:"key"`
	Origin    merchants.AliasOrigin `json:"origin"`
	CreatedAt time.Time             `json:"created_at"`
}

// merchantRecordJSON is a merchant record: a stable id, the name to show and
// the alias names transactions are matched by.
type merchantRecordJSON struct {
	ID          string              `json:"id"`
	DisplayName string              `json:"display_name"`
	Aliases     []merchantAliasJSON `json:"aliases"`
	Archived    bool                `json:"archived"`
	ArchivedAt  *time.Time          `json:"archived_at"`
	// MergedInto is the merchant this one was merged into, or null.
	MergedInto *string   `json:"merged_into"`
	Version    int64     `json:"version"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func toMerchantRecordJSON(e merchants.Entity) merchantRecordJSON {
	out := merchantRecordJSON{ID: e.ID, DisplayName: e.DisplayName, Aliases: []merchantAliasJSON{}, Archived: e.Archived(), ArchivedAt: e.ArchivedAt,
		MergedInto: strPtr(e.MergedInto), Version: e.Version, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt}
	for _, a := range e.Aliases {
		out.Aliases = append(out.Aliases, merchantAliasJSON{ID: a.ID, Name: a.Name, Key: a.Key, Origin: a.Origin, CreatedAt: a.CreatedAt})
	}
	return out
}

func merchantRecordError(err error) error {
	switch {
	case errors.Is(err, merchants.ErrAliasTaken):
		return &Error{Status: http.StatusConflict, Code: CodeMerchantAliasTaken, Message: err.Error(), Cause: err}
	case errors.Is(err, merchants.ErrArchived), errors.Is(err, merchants.ErrMerged):
		return &Error{Status: http.StatusConflict, Code: CodeMerchantArchived, Message: err.Error(), Cause: err}
	case errors.Is(err, merchants.ErrInUse):
		return &Error{Status: http.StatusConflict, Code: CodeMerchantInUse, Message: err.Error(), Cause: err}
	case errors.Is(err, merchants.ErrMergeSelf):
		return Validation("merchant_id", err.Error())
	case errors.Is(err, merchants.ErrNoAlias):
		return NotFound(err.Error())
	}
	return merchantError(err)
}

var merchantsListSpec = ListSpec{Filters: map[string]FilterSpec{
	"archived": {Ops: []FilterOp{OpEq}, Check: func(v string) error {
		if v != "true" && v != "false" {
			return errors.New("archived must be true or false")
		}
		return nil
	}},
}}

// decodeStrictObject decodes a JSON object whose members are all named in
// allowed, reporting unknown members as field errors.
func decodeStrictObject(r *http.Request, allowed ...string) (map[string]json.RawMessage, []commitments.FieldError, error) {
	doc, err := decodeObject(r)
	if err != nil {
		return nil, nil, err
	}
	var errs []commitments.FieldError
	var unknown []string
	for k := range doc {
		if !slices.Contains(allowed, k) {
			unknown = append(unknown, k)
		}
	}
	slices.Sort(unknown)
	for _, k := range unknown {
		errs = append(errs, commitments.FieldError{Field: k, Message: "unknown field " + k})
	}
	return doc, errs, nil
}

// stringMember reads a required string member.
func stringMember(doc map[string]json.RawMessage, key string) (string, []commitments.FieldError) {
	raw, ok := doc[key]
	if !ok || isJSONNull(raw) {
		return "", []commitments.FieldError{{Field: key, Message: key + " is required"}}
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", []commitments.FieldError{{Field: key, Message: key + " must be a string"}}
	}
	return s, nil
}

// registerMerchantRecords registers the merchant record routes (task M1,
// ADR-0057, decision D-MER 1a): list, create, read, rename (PATCH), archive,
// unarchive, delete, aliases and merge. Merchant names and aliases derive
// from bank text, so the grants are the administrator-only merchants grants,
// like the rest of the merchant routes (ADR-0051).
func registerMerchantRecords(s *Server, st merchants.EntityStore) {
	item := MerchantsPath + "/{id}"
	failed := func(w http.ResponseWriter, r *http.Request, id string, err error) {
		var vc *resource.VersionConflictError
		if errors.As(err, &vc) {
			if cur, gerr := st.GetEntity(r.Context(), id); gerr == nil {
				WriteError(w, r, VersionConflict(cur.Version, toMerchantRecordJSON(cur)))
				return
			}
		}
		WriteError(w, r, merchantRecordError(err))
	}
	ok := func(w http.ResponseWriter, status int, e merchants.Entity) {
		SetVersion(w, e.Version)
		WriteJSON(w, status, toMerchantRecordJSON(e))
	}

	s.HandleAPI("GET "+MerchantsPath, Requires(grantMerchantsRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, err := ParseList(r, merchantsListSpec)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		f := merchants.EntityFilter{After: q.After, Limit: q.Limit}
		if v := q.Filter("archived", OpEq); len(v) == 1 {
			f.Archived = v[0] == "true"
		} else if len(v) > 1 {
			WriteError(w, r, Validation("archived", "archived must be given once"))
			return
		}
		items, err := st.ListEntities(r.Context(), f)
		if err != nil {
			WriteError(w, r, merchantRecordError(err))
			return
		}
		out := make([]merchantRecordJSON, len(items))
		for i, e := range items {
			out[i] = toMerchantRecordJSON(e)
		}
		WriteJSON(w, http.StatusOK, NewPage(out, q, func(m merchantRecordJSON) string { return m.ID }))
	}))

	s.HandleAPI("POST "+MerchantsPath, Requires(grantMerchantsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		doc, errs, err := decodeStrictObject(r, "display_name", "aliases")
		if err != nil {
			WriteError(w, r, err)
			return
		}
		name, ferrs := stringMember(doc, "display_name")
		errs = append(errs, ferrs...)
		var aliases []string
		if raw, ok := doc["aliases"]; ok && !isJSONNull(raw) {
			if json.Unmarshal(raw, &aliases) != nil {
				errs = append(errs, commitments.FieldError{Field: "aliases", Message: "aliases must be an array of strings"})
			}
		}
		if len(errs) > 0 {
			WriteError(w, r, fieldErrors(errs))
			return
		}
		e, err := st.CreateEntity(r.Context(), matchActor(r), name, aliases)
		if err != nil {
			WriteError(w, r, merchantRecordError(err))
			return
		}
		w.Header().Set("Location", MerchantsPath+"/"+e.ID)
		ok(w, http.StatusCreated, e)
	}))

	s.HandleAPI("GET "+item, Requires(grantMerchantsRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e, err := st.GetEntity(r.Context(), r.PathValue("id"))
		if err != nil {
			WriteError(w, r, merchantRecordError(err))
			return
		}
		ok(w, http.StatusOK, e)
	}))

	// PATCH is a JSON Merge Patch of display_name, the only writable member.
	s.HandleAPI("PATCH "+item, Requires(grantMerchantsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		patch, err := DecodeMergePatch(r, "version", "display_name")
		if err != nil {
			WriteError(w, r, err)
			return
		}
		var bodyVersion *int64
		if patch.Has("version") {
			var v int64
			if patch.IsNull("version") || patch.Decode("version", &v) != nil {
				WriteError(w, r, Validation("version", "version must be a positive integer"))
				return
			}
			bodyVersion = &v
		}
		expected, err := ExpectedVersion(r, bodyVersion)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		id := r.PathValue("id")
		cur, err := st.GetEntity(r.Context(), id)
		if err != nil {
			WriteError(w, r, merchantRecordError(err))
			return
		}
		if cur.Version != expected {
			WriteError(w, r, VersionConflict(cur.Version, toMerchantRecordJSON(cur)))
			return
		}
		name := cur.DisplayName
		if patch.Has("display_name") {
			var errs []commitments.FieldError
			if name, errs = stringMember(map[string]json.RawMessage(patch), "display_name"); len(errs) > 0 {
				WriteError(w, r, fieldErrors(errs))
				return
			}
		}
		e, err := st.RenameEntity(r.Context(), matchActor(r), id, expected, name)
		if err != nil {
			failed(w, r, id, err)
			return
		}
		ok(w, http.StatusOK, e)
	}))

	archive := func(archived bool) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			expected, err := ExpectedVersion(r, nil)
			if err != nil {
				WriteError(w, r, err)
				return
			}
			id := r.PathValue("id")
			e, err := st.SetEntityArchived(r.Context(), matchActor(r), id, expected, archived)
			if err != nil {
				failed(w, r, id, err)
				return
			}
			ok(w, http.StatusOK, e)
		})
	}
	s.HandleAPI("POST "+item+"/archive", Requires(grantMerchantsWrite), archive(true))
	s.HandleAPI("POST "+item+"/unarchive", Requires(grantMerchantsWrite), archive(false))

	s.HandleAPI("DELETE "+item, Requires(grantMerchantsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected, err := ExpectedVersion(r, nil)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		id := r.PathValue("id")
		if err := st.DeleteEntity(r.Context(), matchActor(r), id, expected); err != nil {
			failed(w, r, id, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	s.HandleAPI("POST "+item+"/aliases", Requires(grantMerchantsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		doc, errs, err := decodeStrictObject(r, "name")
		if err != nil {
			WriteError(w, r, err)
			return
		}
		name, ferrs := stringMember(doc, "name")
		if errs = append(errs, ferrs...); len(errs) > 0 {
			WriteError(w, r, fieldErrors(errs))
			return
		}
		e, err := st.AddAlias(r.Context(), matchActor(r), r.PathValue("id"), name)
		if err != nil {
			WriteError(w, r, merchantRecordError(err))
			return
		}
		ok(w, http.StatusOK, e)
	}))

	s.HandleAPI("DELETE "+item+"/aliases/{alias_id}", Requires(grantMerchantsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e, err := st.RemoveAlias(r.Context(), matchActor(r), r.PathValue("id"), r.PathValue("alias_id"))
		if err != nil {
			WriteError(w, r, merchantRecordError(err))
			return
		}
		ok(w, http.StatusOK, e)
	}))

	// POST .../merge merges the merchant named in the body into this one
	// (If-Match: this merchant's version).
	s.HandleAPI("POST "+item+"/merge", Requires(grantMerchantsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected, err := ExpectedVersion(r, nil)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		doc, errs, err := decodeStrictObject(r, "merchant_id")
		if err != nil {
			WriteError(w, r, err)
			return
		}
		from, ferrs := stringMember(doc, "merchant_id")
		if errs = append(errs, ferrs...); len(errs) == 0 && !resource.ValidID(from) {
			errs = append(errs, commitments.FieldError{Field: "merchant_id", Message: "merchant_id must be an identifier"})
		}
		if len(errs) > 0 {
			WriteError(w, r, fieldErrors(errs))
			return
		}
		id := r.PathValue("id")
		res, err := st.Merge(r.Context(), matchActor(r), id, expected, from)
		if err != nil {
			failed(w, r, id, err)
			return
		}
		SetVersion(w, res.Into.Version)
		WriteJSON(w, http.StatusOK, map[string]any{
			"merchant": toMerchantRecordJSON(res.Into),
			"moved": map[string]int{"aliases": res.Aliases, "decisions": res.Decisions, "enrichments": res.Enrichments,
				"categories": res.Categories, "labels": res.Labels},
			"kept": res.Kept,
		})
	}))
}
