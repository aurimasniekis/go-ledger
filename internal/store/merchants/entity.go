package merchants

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/merchants"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/store"
)

var _ merchants.EntityStore = (*Store)(nil)

// Merchant records (task M1, ADR-0058): the merchants and merchant_aliases
// tables of migration 0030. Decisions and organisation assignments refer to a
// merchant by id; a name reaches a merchant through an alias key
// (merchants.AliasKey).

const entityColumns = "id, display_name, coalesce(merged_into_id, ''), archived_at, created_at, updated_at, version"

func scanEntity(row interface{ Scan(...any) error }) (merchants.Entity, error) {
	var e merchants.Entity
	var archived sql.Null[store.Timestamp]
	var created, updated store.Timestamp
	if err := row.Scan(&e.ID, &e.DisplayName, &e.MergedInto, &archived, &created, &updated, &e.Version); err != nil {
		return merchants.Entity{}, err
	}
	if archived.Valid {
		t := archived.V.Time
		e.ArchivedAt = &t
	}
	e.CreatedAt, e.UpdatedAt = created.Time, updated.Time
	return e, nil
}

func entityAliases(ctx context.Context, tx *sql.Tx, id string) ([]merchants.Alias, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id, name, alias_key, origin, created_at FROM merchant_aliases WHERE merchant_id = ? ORDER BY alias_key", id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []merchants.Alias{}
	for rows.Next() {
		var a merchants.Alias
		var at store.Timestamp
		if err := rows.Scan(&a.ID, &a.Name, &a.Key, &a.Origin, &at); err != nil {
			return nil, err
		}
		a.CreatedAt = at.Time
		out = append(out, a)
	}
	return out, rows.Err()
}

// getEntity returns a merchant with its aliases, or resource.ErrNotFound.
func getEntity(ctx context.Context, tx *sql.Tx, id string) (merchants.Entity, error) {
	if !resource.ValidID(id) {
		return merchants.Entity{}, resource.ErrNotFound
	}
	e, err := scanEntity(tx.QueryRowContext(ctx, "SELECT "+entityColumns+" FROM merchants WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return merchants.Entity{}, resource.ErrNotFound
	}
	if err != nil {
		return merchants.Entity{}, err
	}
	if e.Aliases, err = entityAliases(ctx, tx, id); err != nil {
		return merchants.Entity{}, err
	}
	return e, nil
}

// merchantByKey returns the merchant an alias key names, or "".
func merchantByKey(ctx context.Context, tx *sql.Tx, key string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, "SELECT merchant_id FROM merchant_aliases WHERE alias_key = ?", key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// merchantForName returns the merchant name resolves to through its alias
// key, or "" when no alias has it.
func merchantForName(ctx context.Context, tx *sql.Tx, name string) (string, error) {
	key, ok := merchants.AliasKey(name)
	if !ok {
		return "", nil
	}
	return merchantByKey(ctx, tx, key)
}

func (s *Store) entityEvent(ctx context.Context, tx *sql.Tx, a audit.Action, actor audit.Actor, id string, at time.Time, extra map[string]string) error {
	_, via := storedActor(actor)
	details := map[string]string{"via": string(via)}
	for k, v := range extra {
		details[k] = v
	}
	return s.audit.WriteAudit(ctx, tx, audit.Event{Action: a, Actor: actor, TargetType: merchants.EntityTargetType, TargetID: id, At: at, Details: details})
}

// addAlias attaches name's key to merchant id. It reports whether a row was
// added: false when the merchant already has the key. Another merchant's key
// is merchants.ErrAliasTaken. Identified answers whose name has the key and
// no merchant yet now resolve to this one.
func addAlias(ctx context.Context, tx *sql.Tx, id, name string, origin merchants.AliasOrigin, at time.Time) (bool, error) {
	key, ok := merchants.AliasKey(name)
	if !ok {
		return false, commitments.Validation(merchants.CheckName("name", name))
	}
	owner, err := merchantByKey(ctx, tx, key)
	if err != nil {
		return false, err
	}
	switch owner {
	case id:
		return false, nil
	case "":
	default:
		return false, merchants.ErrAliasTaken
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO merchant_aliases (id, merchant_id, alias_key, name, origin, created_at) VALUES (?, ?, ?, ?, ?, ?)",
		resource.NewIDAt(at), id, key, name, string(origin), store.Timestamp{Time: at}); err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, "UPDATE merchant_enrichments SET merchant_id = ? WHERE status = 'identified' AND merchant_id IS NULL AND ledger_merchant_key(name) = ?", id, key)
	return true, err
}

// createEntity makes a merchant named displayName with that name as its first
// alias (merchants.ErrAliasTaken when another merchant has it).
func (s *Store) createEntity(ctx context.Context, tx *sql.Tx, actor audit.Actor, displayName string, origin merchants.AliasOrigin, at time.Time) (string, error) {
	id := resource.NewIDAt(at)
	ts := store.Timestamp{Time: at}
	if _, err := tx.ExecContext(ctx, "INSERT INTO merchants (id, display_name, created_at, updated_at) VALUES (?, ?, ?, ?)", id, displayName, ts, ts); err != nil {
		return "", err
	}
	if _, err := addAlias(ctx, tx, id, displayName, origin, at); err != nil {
		return "", err
	}
	return id, s.entityEvent(ctx, tx, audit.MerchantCreated, actor, id, at, map[string]string{"origin": string(origin)})
}

func normalizeNames(field string, names ...string) ([]string, error) {
	var errs []commitments.FieldError
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = merchants.NormalizeDisplayName(n)
		errs = append(errs, merchants.CheckName(field, out[i])...)
	}
	return out, commitments.Validation(errs)
}

// ListEntities returns up to f.Limit+1 merchants after f.After in ID order,
// with their aliases: active ones, or all when f.Archived is set.
func (s *Store) ListEntities(ctx context.Context, f merchants.EntityFilter) ([]merchants.Entity, error) {
	q := "SELECT " + entityColumns + " FROM merchants WHERE id > ?"
	if !f.Archived {
		q += " AND archived_at IS NULL"
	}
	q += " ORDER BY id"
	if f.Limit > 0 {
		q += " LIMIT " + strconv.Itoa(f.Limit+1)
	}
	var out []merchants.Entity
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, q, f.After)
		if err != nil {
			return err
		}
		for rows.Next() {
			e, err := scanEntity(rows)
			if err != nil {
				_ = rows.Close()
				return err
			}
			out = append(out, e)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for i := range out {
			if out[i].Aliases, err = entityAliases(ctx, tx, out[i].ID); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// GetEntity returns one merchant with its aliases, or resource.ErrNotFound.
func (s *Store) GetEntity(ctx context.Context, id string) (merchants.Entity, error) {
	var e merchants.Entity
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) (err error) {
		e, err = getEntity(ctx, tx, id)
		return err
	})
	return e, err
}

// CreateEntity makes a merchant at version 1. The display name is its first
// alias, and aliases adds further names; any of them naming another merchant
// is merchants.ErrAliasTaken and nothing is created.
func (s *Store) CreateEntity(ctx context.Context, actor audit.Actor, displayName string, aliases []string) (merchants.Entity, error) {
	names, err := normalizeNames("display_name", displayName)
	if err != nil {
		return merchants.Entity{}, err
	}
	extra, err := normalizeNames("aliases", aliases...)
	if err != nil {
		return merchants.Entity{}, err
	}
	at := s.now().UTC()
	var out merchants.Entity
	err = s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		id, err := s.createEntity(ctx, tx, actor, names[0], merchants.AliasUser, at)
		if err != nil {
			return err
		}
		for _, a := range extra {
			if _, err := addAlias(ctx, tx, id, a, merchants.AliasUser, at); err != nil {
				return err
			}
		}
		out, err = getEntity(ctx, tx, id)
		return err
	})
	return out, err
}

// writable loads a merchant that may change: merged ones may not.
func writable(ctx context.Context, tx *sql.Tx, id string) (merchants.Entity, error) {
	e, err := getEntity(ctx, tx, id)
	if err != nil {
		return e, err
	}
	if e.MergedInto != "" {
		return e, merchants.ErrMerged
	}
	return e, nil
}

func checkVersion(e merchants.Entity, expected int64) error {
	if e.Version != expected {
		return &resource.VersionConflictError{Expected: expected, Current: e.Version}
	}
	return nil
}

// RenameEntity changes a merchant's display name. Decisions and assignments
// refer to the id, so they all stay. The new name becomes an alias when no
// merchant has it yet; when another merchant has it the rename is refused
// (merchants.ErrAliasTaken: merge the two instead). The same name is no
// change and keeps the version.
func (s *Store) RenameEntity(ctx context.Context, actor audit.Actor, id string, expected int64, displayName string) (merchants.Entity, error) {
	names, err := normalizeNames("display_name", displayName)
	if err != nil {
		return merchants.Entity{}, err
	}
	at := s.now().UTC()
	var out merchants.Entity
	err = s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		e, err := writable(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := checkVersion(e, expected); err != nil {
			return err
		}
		if e.DisplayName == names[0] {
			out = e
			return nil
		}
		if _, err := addAlias(ctx, tx, id, names[0], merchants.AliasUser, at); err != nil {
			return err
		}
		if _, err := store.UpdateVersioned(ctx, tx, "merchants", "display_name = ?, updated_at = ?", id, expected, names[0], store.Timestamp{Time: at}); err != nil {
			return err
		}
		if err := s.entityEvent(ctx, tx, audit.MerchantUpdated, actor, id, at, map[string]string{"fields": "display_name"}); err != nil {
			return err
		}
		out, err = getEntity(ctx, tx, id)
		return err
	})
	return out, err
}

// SetEntityArchived archives or unarchives a merchant. An archived merchant
// keeps its aliases, decisions and assignments; nothing new is attached to it
// by id. A merged merchant stays archived.
func (s *Store) SetEntityArchived(ctx context.Context, actor audit.Actor, id string, expected int64, archived bool) (merchants.Entity, error) {
	at := s.now().UTC()
	var out merchants.Entity
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		e, err := writable(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := checkVersion(e, expected); err != nil {
			return err
		}
		if e.Archived() == archived {
			out = e
			return nil
		}
		var ts any
		action := audit.MerchantUnarchived
		if archived {
			ts, action = store.Timestamp{Time: at}, audit.MerchantArchived
		}
		if _, err := store.UpdateVersioned(ctx, tx, "merchants", "archived_at = ?, updated_at = ?", id, expected, ts, store.Timestamp{Time: at}); err != nil {
			return err
		}
		if err := s.entityEvent(ctx, tx, action, actor, id, at, nil); err != nil {
			return err
		}
		out, err = getEntity(ctx, tx, id)
		return err
	})
	return out, err
}

func count(ctx context.Context, tx *sql.Tx, q string, args ...any) (int, error) {
	var n int
	err := tx.QueryRowContext(ctx, q, args...).Scan(&n)
	return n, err
}

// DeleteEntity deletes a merchant that no decision, organisation assignment
// or merged merchant refers to (merchants.ErrInUse otherwise), with its
// aliases. Answers that resolved to it lose the reference (they are derived
// data).
func (s *Store) DeleteEntity(ctx context.Context, actor audit.Actor, id string, expected int64) error {
	at := s.now().UTC()
	return s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		e, err := getEntity(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := checkVersion(e, expected); err != nil {
			return err
		}
		n, err := count(ctx, tx, "SELECT (SELECT count(*) FROM merchant_reviews WHERE merchant_id = ?1) + (SELECT count(*) FROM category_assignments WHERE merchant_id = ?1)"+
			" + (SELECT count(*) FROM label_assignments WHERE merchant_id = ?1) + (SELECT count(*) FROM merchants WHERE merged_into_id = ?1)", id)
		if err != nil {
			return err
		}
		if n > 0 {
			return merchants.ErrInUse
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM merchant_aliases WHERE merchant_id = ?", id); err != nil {
			return err
		}
		if err := store.DeleteVersioned(ctx, tx, "merchants", id, expected); err != nil {
			return err
		}
		return s.entityEvent(ctx, tx, audit.MerchantDeleted, actor, id, at, nil)
	})
}

// bump raises a merchant's version for a change to its aliases.
func bump(ctx context.Context, tx *sql.Tx, id string, at time.Time) error {
	_, err := tx.ExecContext(ctx, "UPDATE merchants SET version = version + 1, updated_at = ? WHERE id = ?", store.Timestamp{Time: at}, id)
	return err
}

// AddAlias adds a spelling to a merchant: transactions whose merchant name has
// the same key resolve to it, and so keep its assignments. A key the merchant
// already has is no change; another merchant's is merchants.ErrAliasTaken.
func (s *Store) AddAlias(ctx context.Context, actor audit.Actor, id string, name string) (merchants.Entity, error) {
	names, err := normalizeNames("name", name)
	if err != nil {
		return merchants.Entity{}, err
	}
	at := s.now().UTC()
	var out merchants.Entity
	err = s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := writable(ctx, tx, id); err != nil {
			return err
		}
		added, err := addAlias(ctx, tx, id, names[0], merchants.AliasUser, at)
		if err != nil {
			return err
		}
		if added {
			if err := bump(ctx, tx, id, at); err != nil {
				return err
			}
			if err := s.entityEvent(ctx, tx, audit.MerchantAliasAdded, actor, id, at, nil); err != nil {
				return err
			}
		}
		out, err = getEntity(ctx, tx, id)
		return err
	})
	return out, err
}

// RemoveAlias removes one alias of a merchant (merchants.ErrNoAlias when it
// has no such alias). Decisions and assignments stay with the merchant;
// identified answers that resolved to it through that key lose the
// reference.
func (s *Store) RemoveAlias(ctx context.Context, actor audit.Actor, id string, aliasID string) (merchants.Entity, error) {
	at := s.now().UTC()
	var out merchants.Entity
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := writable(ctx, tx, id); err != nil {
			return err
		}
		var key string
		err := tx.QueryRowContext(ctx, "DELETE FROM merchant_aliases WHERE id = ? AND merchant_id = ? RETURNING alias_key", aliasID, id).Scan(&key)
		if errors.Is(err, sql.ErrNoRows) {
			return merchants.ErrNoAlias
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE merchant_enrichments SET merchant_id = NULL WHERE merchant_id = ? AND ledger_merchant_key(name) = ?", id, key); err != nil {
			return err
		}
		if err := bump(ctx, tx, id, at); err != nil {
			return err
		}
		if err := s.entityEvent(ctx, tx, audit.MerchantAliasRemoved, actor, id, at, nil); err != nil {
			return err
		}
		out, err = getEntity(ctx, tx, id)
		return err
	})
	return out, err
}

// Merge moves everything of merchant from into merchant into, in one
// transaction: its aliases, the decisions and answers that refer to it, and
// its organisation assignments. Where both have an assignment for the same
// slot (the category, or the same label), into's stands unless from's is the
// person's own and into's is automatic (a rule's or a model's): a person's
// word outranks automation, as everywhere in ADR-0053. Assignments not moved
// are counted in Kept. from is left archived with merged_into_id = into (so
// its id still resolves), and merchants merged into from earlier now point at
// into. into must be active and at version expected; from may be archived but
// not merged. One audit event (merchant.merged, target into) records the
// counts.
func (s *Store) Merge(ctx context.Context, actor audit.Actor, into string, expected int64, from string) (merchants.MergeResult, error) {
	if into == from {
		return merchants.MergeResult{}, merchants.ErrMergeSelf
	}
	at := s.now().UTC()
	ts := store.Timestamp{Time: at}
	var res merchants.MergeResult
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		target, err := writable(ctx, tx, into)
		if err != nil {
			return err
		}
		if err := checkVersion(target, expected); err != nil {
			return err
		}
		if target.Archived() {
			return merchants.ErrArchived
		}
		source, err := getEntity(ctx, tx, from)
		if errors.Is(err, resource.ErrNotFound) {
			return commitments.Validation([]commitments.FieldError{{Field: "merchant_id", Message: "merchant_id names no merchant"}})
		}
		if err != nil {
			return err
		}
		if source.MergedInto != "" {
			return merchants.ErrMerged
		}
		exec := func(q string, args ...any) (int, error) {
			r, err := tx.ExecContext(ctx, q, args...)
			if err != nil {
				return 0, err
			}
			n, err := r.RowsAffected()
			return int(n), err
		}
		if res.Aliases, err = exec("UPDATE merchant_aliases SET merchant_id = ? WHERE merchant_id = ?", into, from); err != nil {
			return err
		}
		if res.Decisions, err = exec("UPDATE merchant_reviews SET merchant_id = ? WHERE merchant_id = ?", into, from); err != nil {
			return err
		}
		if res.Enrichments, err = exec("UPDATE merchant_enrichments SET merchant_id = ? WHERE merchant_id = ?", into, from); err != nil {
			return err
		}
		// The category slot: the person's own outranks an automatic one.
		if _, err = exec("DELETE FROM category_assignments WHERE object_type = 'merchant' AND merchant_id = ? AND origin <> 'user'"+
			" AND EXISTS (SELECT 1 FROM category_assignments f WHERE f.object_type = 'merchant' AND f.merchant_id = ? AND f.origin = 'user')", into, from); err != nil {
			return err
		}
		kept, err := exec("DELETE FROM category_assignments WHERE object_type = 'merchant' AND merchant_id = ?"+
			" AND EXISTS (SELECT 1 FROM category_assignments i WHERE i.object_type = 'merchant' AND i.merchant_id = ?)", from, into)
		if err != nil {
			return err
		}
		res.Kept += kept
		if res.Categories, err = exec("UPDATE category_assignments SET object_id = ?1, merchant_id = ?1 WHERE object_type = 'merchant' AND merchant_id = ?2", into, from); err != nil {
			return err
		}
		// Labels, per label, the same way.
		if _, err = exec("DELETE FROM label_assignments WHERE object_type = 'merchant' AND merchant_id = ? AND origin <> 'user'"+
			" AND EXISTS (SELECT 1 FROM label_assignments f WHERE f.object_type = 'merchant' AND f.merchant_id = ? AND f.label_id = label_assignments.label_id AND f.origin = 'user')", into, from); err != nil {
			return err
		}
		if kept, err = exec("DELETE FROM label_assignments WHERE object_type = 'merchant' AND merchant_id = ?"+
			" AND EXISTS (SELECT 1 FROM label_assignments i WHERE i.object_type = 'merchant' AND i.merchant_id = ? AND i.label_id = label_assignments.label_id)", from, into); err != nil {
			return err
		}
		res.Kept += kept
		if res.Labels, err = exec("UPDATE label_assignments SET object_id = ?1, merchant_id = ?1 WHERE object_type = 'merchant' AND merchant_id = ?2", into, from); err != nil {
			return err
		}
		if _, err = exec("UPDATE merchants SET merged_into_id = ?, updated_at = ?, version = version + 1 WHERE merged_into_id = ?", into, ts, from); err != nil {
			return err
		}
		if _, err = exec("UPDATE merchants SET merged_into_id = ?, archived_at = coalesce(archived_at, ?), updated_at = ?, version = version + 1 WHERE id = ?", into, ts, ts, from); err != nil {
			return err
		}
		if _, err := store.UpdateVersioned(ctx, tx, "merchants", "updated_at = ?", into, expected, ts); err != nil {
			return err
		}
		details := map[string]string{"merged_from": from}
		for k, v := range map[string]int{"aliases": res.Aliases, "decisions": res.Decisions, "categories": res.Categories, "labels": res.Labels, "kept": res.Kept} {
			details[k] = strconv.Itoa(v)
		}
		if err := s.entityEvent(ctx, tx, audit.MerchantMerged, actor, into, at, details); err != nil {
			return err
		}
		res.Into, err = getEntity(ctx, tx, into)
		return err
	})
	return res, err
}
