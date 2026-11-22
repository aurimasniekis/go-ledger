// Package organisation persists categories, labels and buckets and their
// assignments (task O1, ADR-0053) in the tables of migration 0028_organisation.
//
// It implements [organisation.Store]. Every write runs in one transaction with
// its audit event. Nothing here reads or writes a bank transaction's, an
// account's or a commitment's own columns: assignments are separate rows keyed
// by the object, so a bank refresh or a re-import never touches them.
package organisation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/organisation"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/store"
)

var _ organisation.Store = (*Store)(nil)

// Store keeps organisation data on a [store.DB].
type Store struct {
	db    *store.DB
	audit store.AuditWriter
	now   func() time.Time
	newID func() string
}

// New returns a Store. aw records audit events and is required; now and newID
// default to time.Now and resource.NewID.
func New(db *store.DB, aw store.AuditWriter, now func() time.Time, newID func() string) *Store {
	if aw == nil {
		panic("organisation.New: an audit writer is required")
	}
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = resource.NewID
	}
	return &Store{db: db, audit: aw, now: now, newID: newID}
}

func table(k organisation.Kind) string {
	switch k {
	case organisation.KindCategory:
		return "categories"
	case organisation.KindLabel:
		return "labels"
	case organisation.KindBucket:
		return "buckets"
	}
	return ""
}

func fieldErr(field, msg string) error {
	return commitments.Validation([]commitments.FieldError{{Field: field, Message: msg}})
}

func checkKind(k organisation.Kind) error {
	if !k.Valid() {
		return fmt.Errorf("organisation: unknown kind %q", string(k))
	}
	return nil
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// whoOf returns the columns that record who acted.
func whoOf(actor audit.Actor) (kind string, user any, via string) {
	switch actor.Kind {
	case audit.ActorUser, audit.ActorToken:
		if actor.UserID != "" {
			user = actor.UserID
		}
		return string(actor.Kind), user, string(actor.Channel())
	}
	return string(audit.ActorSystem), nil, string(audit.ViaSystem)
}

func actorFrom(kind string, user sql.Null[string], via string) audit.Actor {
	return audit.Actor{Kind: audit.ActorKind(kind), UserID: user.V, Via: audit.Via(via)}
}

func viaOf(actor audit.Actor) string {
	_, _, v := whoOf(actor)
	return v
}

// --- definitions ---------------------------------------------------------

func defSelect(k organisation.Kind) string {
	parent := "NULL"
	if k == organisation.KindCategory {
		parent = "parent_id"
	}
	return "SELECT id, name, " + parent + ", archived_at, version, created_at, updated_at FROM " + table(k)
}

func scanDef(k organisation.Kind, row interface{ Scan(...any) error }) (organisation.Definition, error) {
	d := organisation.Definition{Kind: k}
	var parent sql.Null[string]
	var archived sql.Null[store.Timestamp]
	var created, updated store.Timestamp
	if err := row.Scan(&d.ID, &d.Name, &parent, &archived, &d.Version, &created, &updated); err != nil {
		return organisation.Definition{}, err
	}
	if parent.Valid {
		d.ParentID = &parent.V
	}
	if archived.Valid {
		t := archived.V.Time
		d.ArchivedAt = &t
	}
	d.CreatedAt, d.UpdatedAt = created.Time, updated.Time
	return d, nil
}

func getDef(ctx context.Context, tx *sql.Tx, k organisation.Kind, id string) (organisation.Definition, error) {
	d, err := scanDef(k, tx.QueryRowContext(ctx, defSelect(k)+" WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return organisation.Definition{}, resource.ErrNotFound
	}
	return d, err
}

func count(ctx context.Context, tx *sql.Tx, query string, args ...any) (int, error) {
	var n int
	err := tx.QueryRowContext(ctx, query, args...).Scan(&n)
	return n, err
}

// checkParent validates parentID as the parent of the category selfID ("" for
// a category being created).
func checkParent(ctx context.Context, tx *sql.Tx, selfID, parentID string) error {
	if parentID == selfID {
		return fieldErr("parent_id", "a category cannot be its own parent")
	}
	p, err := getDef(ctx, tx, organisation.KindCategory, parentID)
	if errors.Is(err, resource.ErrNotFound) {
		return fieldErr("parent_id", "parent_id names no category")
	}
	if err != nil {
		return err
	}
	if p.ParentID != nil {
		return fieldErr("parent_id", "categories have two levels: a child category cannot be a parent")
	}
	if p.Archived() {
		return organisation.ErrArchived
	}
	if selfID != "" {
		n, err := count(ctx, tx, "SELECT count(*) FROM categories WHERE parent_id = ?", selfID)
		if err != nil {
			return err
		}
		if n > 0 {
			return fieldErr("parent_id", "categories have two levels: a category with children cannot become a child")
		}
	}
	return nil
}

func (s *Store) definitionEvent(ctx context.Context, tx *sql.Tx, a audit.Action, actor audit.Actor, d organisation.Definition, at time.Time, extra map[string]string) error {
	details := map[string]string{"kind": string(d.Kind), "via": viaOf(actor)}
	for k, v := range extra {
		details[k] = v
	}
	return s.audit.WriteAudit(ctx, tx, audit.Event{Action: a, Actor: actor, TargetType: string(d.Kind), TargetID: d.ID, At: at, Details: details})
}

// CreateDefinition creates a category, label or bucket at version 1.
func (s *Store) CreateDefinition(ctx context.Context, actor audit.Actor, k organisation.Kind, f organisation.Fields) (organisation.Definition, error) {
	if err := checkKind(k); err != nil {
		return organisation.Definition{}, err
	}
	f = f.Normalized()
	if err := commitments.Validation(f.Validate(k)); err != nil {
		return organisation.Definition{}, err
	}
	now := s.now().UTC()
	d := organisation.Definition{ID: s.newID(), Kind: k, Name: f.Name, ParentID: f.ParentID, Version: 1, CreatedAt: now, UpdatedAt: now}
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		ts := store.Timestamp{Time: now}
		var err error
		if k == organisation.KindCategory {
			if f.ParentID != nil {
				if err := checkParent(ctx, tx, "", *f.ParentID); err != nil {
					return err
				}
			}
			_, err = tx.ExecContext(ctx, "INSERT INTO categories (id, name, name_key, parent_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
				d.ID, d.Name, organisation.NameKey(d.Name), f.ParentID, ts, ts)
		} else {
			_, err = tx.ExecContext(ctx, "INSERT INTO "+table(k)+" (id, name, name_key, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
				d.ID, d.Name, organisation.NameKey(d.Name), ts, ts)
		}
		if isUnique(err) {
			return organisation.ErrDuplicateName
		}
		if err != nil {
			return err
		}
		return s.definitionEvent(ctx, tx, audit.OrganisationDefinitionCreated, actor, d, now, nil)
	})
	if err != nil {
		return organisation.Definition{}, err
	}
	return d, nil
}

// GetDefinition returns one definition or resource.ErrNotFound.
func (s *Store) GetDefinition(ctx context.Context, k organisation.Kind, id string) (organisation.Definition, error) {
	if err := checkKind(k); err != nil {
		return organisation.Definition{}, err
	}
	var d organisation.Definition
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) (err error) {
		d, err = getDef(ctx, tx, k, id)
		return err
	})
	return d, err
}

func likeEscape(s string) string {
	return strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(s)
}

// ListDefinitions returns up to f.Limit+1 definitions after f.After, in ID
// order.
func (s *Store) ListDefinitions(ctx context.Context, k organisation.Kind, f organisation.ListFilter) ([]organisation.Definition, error) {
	if err := checkKind(k); err != nil {
		return nil, err
	}
	where := []string{"id > ?"}
	args := []any{f.After}
	if f.Archived != nil {
		if *f.Archived {
			where = append(where, "archived_at IS NOT NULL")
		} else {
			where = append(where, "archived_at IS NULL")
		}
	}
	if f.Parent != nil && k == organisation.KindCategory {
		if *f.Parent == "" {
			where = append(where, "parent_id IS NULL")
		} else {
			where = append(where, "parent_id = ?")
			args = append(args, *f.Parent)
		}
	}
	if f.NamePrefix != "" {
		where = append(where, "name_key LIKE ? ESCAPE '\\'")
		args = append(args, likeEscape(organisation.NameKey(f.NamePrefix))+"%")
	}
	q := defSelect(k) + " WHERE " + strings.Join(where, " AND ") + " ORDER BY id"
	if f.Limit > 0 {
		q += " LIMIT " + strconv.Itoa(f.Limit+1)
	}
	var out []organisation.Definition
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, q, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			d, err := scanDef(k, rows)
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

func sameParent(a, b *string) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

// UpdateDefinition renames a definition or moves a category. An archived
// definition may be renamed (that frees its name); a change that alters
// nothing is not a change and leaves the version alone.
func (s *Store) UpdateDefinition(ctx context.Context, actor audit.Actor, k organisation.Kind, id string, expected int64, f organisation.Fields) (organisation.Definition, error) {
	if err := checkKind(k); err != nil {
		return organisation.Definition{}, err
	}
	f = f.Normalized()
	if err := commitments.Validation(f.Validate(k)); err != nil {
		return organisation.Definition{}, err
	}
	now := s.now().UTC()
	var out organisation.Definition
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		before, err := getDef(ctx, tx, k, id)
		if err != nil {
			return err
		}
		if before.Version != expected {
			return &resource.VersionConflictError{Expected: expected, Current: before.Version}
		}
		var changed []string
		set := []string{"name = ?", "name_key = ?"}
		args := []any{f.Name, organisation.NameKey(f.Name)}
		if f.Name != before.Name {
			changed = append(changed, "name")
		}
		if k == organisation.KindCategory && !sameParent(f.ParentID, before.ParentID) {
			changed = append(changed, "parent_id")
			if f.ParentID != nil {
				if err := checkParent(ctx, tx, id, *f.ParentID); err != nil {
					return err
				}
			}
		}
		if len(changed) == 0 {
			out = before
			return nil
		}
		if k == organisation.KindCategory {
			set = append(set, "parent_id = ?")
			args = append(args, f.ParentID)
		}
		set = append(set, "updated_at = ?")
		args = append(args, store.Timestamp{Time: now})
		if _, err := store.UpdateVersioned(ctx, tx, table(k), strings.Join(set, ", "), id, expected, args...); err != nil {
			if isUnique(err) {
				return organisation.ErrDuplicateName
			}
			return err
		}
		if out, err = getDef(ctx, tx, k, id); err != nil {
			return err
		}
		return s.definitionEvent(ctx, tx, audit.OrganisationDefinitionUpdated, actor, out, now, map[string]string{"fields": strings.Join(changed, ",")})
	})
	return out, err
}

// SetArchived archives or unarchives a definition. Archiving keeps every
// assignment; it only stops new ones. A category is archived after its
// children and unarchived before them. Asking for the state it already has
// changes nothing.
func (s *Store) SetArchived(ctx context.Context, actor audit.Actor, k organisation.Kind, id string, expected int64, archived bool) (organisation.Definition, error) {
	if err := checkKind(k); err != nil {
		return organisation.Definition{}, err
	}
	now := s.now().UTC()
	var out organisation.Definition
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		before, err := getDef(ctx, tx, k, id)
		if err != nil {
			return err
		}
		if before.Version != expected {
			return &resource.VersionConflictError{Expected: expected, Current: before.Version}
		}
		if before.Archived() == archived {
			out = before
			return nil
		}
		var at any
		action := audit.OrganisationDefinitionUnarchived
		if archived {
			at, action = store.Timestamp{Time: now}, audit.OrganisationDefinitionArchived
			if k == organisation.KindCategory {
				n, err := count(ctx, tx, "SELECT count(*) FROM categories WHERE parent_id = ? AND archived_at IS NULL", id)
				if err != nil {
					return err
				}
				if n > 0 {
					return organisation.ErrHasActiveChildren
				}
			}
		} else if before.ParentID != nil {
			p, err := getDef(ctx, tx, k, *before.ParentID)
			if err != nil {
				return err
			}
			if p.Archived() {
				return organisation.ErrParentArchived
			}
		}
		if _, err := store.UpdateVersioned(ctx, tx, table(k), "archived_at = ?, updated_at = ?", id, expected, at, store.Timestamp{Time: now}); err != nil {
			return err
		}
		if out, err = getDef(ctx, tx, k, id); err != nil {
			return err
		}
		return s.definitionEvent(ctx, tx, action, actor, out, now, nil)
	})
	return out, err
}

// DeleteDefinition deletes a definition that nothing is assigned to. One that
// is assigned is deleted only when reassignTo names an active replacement of
// the same kind: its assignments then move to it as the person's (origin
// user), and the audit event records how many moved. A category with children
// is never deleted. Labels the person removed from objects (remembered so
// automatic runs do not add them back) go with the label.
func (s *Store) DeleteDefinition(ctx context.Context, actor audit.Actor, k organisation.Kind, id string, expected int64, reassignTo string) (organisation.DeleteResult, error) {
	if err := checkKind(k); err != nil {
		return organisation.DeleteResult{}, err
	}
	if reassignTo != "" && !resource.ValidID(reassignTo) {
		return organisation.DeleteResult{}, fieldErr("reassign_to", "reassign_to must be an identifier")
	}
	now := s.now().UTC()
	var res organisation.DeleteResult
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		before, err := getDef(ctx, tx, k, id)
		if err != nil {
			return err
		}
		if before.Version != expected {
			return &resource.VersionConflictError{Expected: expected, Current: before.Version}
		}
		if k == organisation.KindCategory {
			n, err := count(ctx, tx, "SELECT count(*) FROM categories WHERE parent_id = ?", id)
			if err != nil {
				return err
			}
			if n > 0 {
				return organisation.ErrHasChildren
			}
		}
		var usedQ string
		switch k {
		case organisation.KindCategory:
			usedQ = "SELECT count(*) FROM category_assignments WHERE category_id = ?"
		case organisation.KindLabel:
			usedQ = "SELECT count(*) FROM label_assignments WHERE label_id = ? AND removed = 0"
		default:
			usedQ = "SELECT count(*) FROM bucket_members WHERE bucket_id = ?"
		}
		used, err := count(ctx, tx, usedQ, id)
		if err != nil {
			return err
		}
		if used > 0 && reassignTo == "" {
			return &organisation.InUseError{Assignments: used}
		}
		if reassignTo != "" {
			if reassignTo == id {
				return fieldErr("reassign_to", "reassign_to must be another "+string(k))
			}
			target, err := getDef(ctx, tx, k, reassignTo)
			if errors.Is(err, resource.ErrNotFound) {
				return fieldErr("reassign_to", "reassign_to names no "+string(k))
			}
			if err != nil {
				return err
			}
			if target.Archived() {
				return organisation.ErrArchived
			}
			if res.Reassigned, err = s.reassign(ctx, tx, actor, k, id, reassignTo, now); err != nil {
				return err
			}
		}
		if k == organisation.KindLabel {
			if _, err := tx.ExecContext(ctx, "DELETE FROM label_assignments WHERE label_id = ?", id); err != nil {
				return err
			}
		}
		if err := store.DeleteVersioned(ctx, tx, table(k), id, expected); err != nil {
			return err
		}
		return s.definitionEvent(ctx, tx, audit.OrganisationDefinitionDeleted, actor, before, now, map[string]string{"reassigned": strconv.Itoa(res.Reassigned)})
	})
	return res, err
}

// reassignedBy is the SET clause that records who moved a row. Only the person's
// own rows become the mover's: the person's decision concerns the definition,
// not each object's classification, so a rule's or model's row keeps its
// origin, source, who set it and when, and only its definition changes.
const reassignedBy = "set_by_kind = CASE WHEN origin = 'user' THEN ? ELSE set_by_kind END, set_by_user = CASE WHEN origin = 'user' THEN ? ELSE set_by_user END, " +
	"set_by_via = CASE WHEN origin = 'user' THEN ? ELSE set_by_via END, set_at = CASE WHEN origin = 'user' THEN ? ELSE set_at END"

func (s *Store) reassign(ctx context.Context, tx *sql.Tx, actor audit.Actor, k organisation.Kind, from, to string, at time.Time) (int, error) {
	kind, user, via := whoOf(actor)
	ts := store.Timestamp{Time: at}
	var moved sql.Result
	var err error
	switch k {
	case organisation.KindCategory:
		moved, err = tx.ExecContext(ctx, "UPDATE category_assignments SET category_id = ?, "+reassignedBy+" WHERE category_id = ?",
			to, kind, user, via, ts, from)
	case organisation.KindLabel:
		// An object that already has the replacement (or whose person removed
		// it) keeps that; the old label's row just goes.
		if _, err = tx.ExecContext(ctx, "DELETE FROM label_assignments WHERE label_id = ? AND removed = 0 AND EXISTS ("+
			"SELECT 1 FROM label_assignments t WHERE t.object_type = label_assignments.object_type AND t.object_id = label_assignments.object_id AND t.label_id = ?)", from, to); err != nil {
			return 0, err
		}
		moved, err = tx.ExecContext(ctx, "UPDATE label_assignments SET label_id = ?, "+reassignedBy+" WHERE label_id = ? AND removed = 0",
			to, kind, user, via, ts, from)
	default:
		moved, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO bucket_members (bucket_id, object_type, object_id, commitment_id, account_id, added_by_kind, added_by_user, added_by_via, added_at) "+
			"SELECT ?, object_type, object_id, commitment_id, account_id, ?, ?, ?, ? FROM bucket_members WHERE bucket_id = ?", to, kind, user, via, ts, from)
		if err == nil {
			_, err = tx.ExecContext(ctx, "DELETE FROM bucket_members WHERE bucket_id = ?", from)
		}
	}
	if err != nil {
		return 0, err
	}
	n, err := moved.RowsAffected()
	return int(n), err
}

// --- assignments ---------------------------------------------------------

func objectExists(ctx context.Context, tx *sql.Tx, obj organisation.ObjectRef) error {
	var q string
	switch obj.Type {
	case organisation.ObjectTransaction:
		q = "SELECT 1 FROM bank_transactions WHERE id = ?"
	case organisation.ObjectCommitment:
		q = "SELECT 1 FROM commitments WHERE id = ? AND deleted_at IS NULL"
	case organisation.ObjectAccount:
		q = "SELECT 1 FROM bank_accounts WHERE id = ?"
	case organisation.ObjectMerchant:
		q = "SELECT 1 FROM merchants WHERE id = ?"
	default:
		return fieldErr("object_type", "unknown object type")
	}
	var one int
	err := tx.QueryRowContext(ctx, q, obj.ID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return resource.ErrNotFound
	}
	return err
}

// objectWritable is objectExists for a change: an archived or merged merchant
// gets nothing new (organisation.ErrObjectArchived); what it has is kept and
// readable.
func objectWritable(ctx context.Context, tx *sql.Tx, obj organisation.ObjectRef) error {
	if err := objectExists(ctx, tx, obj); err != nil {
		return err
	}
	if obj.Type != organisation.ObjectMerchant {
		return nil
	}
	n, err := count(ctx, tx, "SELECT count(*) FROM merchants WHERE id = ? AND archived_at IS NULL AND merged_into_id IS NULL", obj.ID)
	if err != nil {
		return err
	}
	if n == 0 {
		return organisation.ErrObjectArchived
	}
	return nil
}

func checkObject(obj organisation.ObjectRef) error {
	return commitments.Validation(obj.Validate())
}

// objectColumns returns the object columns of an assignment row and their
// values: the typed column that carries the foreign key holds the ID.
func objectColumns(obj organisation.ObjectRef) (cols string, vals []any) {
	var tx, cm, ac, mc any
	switch obj.Type {
	case organisation.ObjectTransaction:
		tx = obj.ID
	case organisation.ObjectCommitment:
		cm = obj.ID
	case organisation.ObjectAccount:
		ac = obj.ID
	case organisation.ObjectMerchant:
		mc = obj.ID
	}
	return "object_type, object_id, transaction_id, commitment_id, account_id, merchant_id", []any{string(obj.Type), obj.ID, tx, cm, ac, mc}
}

// lineage returns the object and, for a transaction, the pending transactions
// it superseded, nearest first (bounded): a booking can arrive under a new
// key, and what the person assigned to the pending row must not be lost.
func lineage(ctx context.Context, tx *sql.Tx, obj organisation.ObjectRef) ([]string, error) {
	ids := []string{obj.ID}
	if obj.Type != organisation.ObjectTransaction {
		return ids, nil
	}
	frontier := []string{obj.ID}
	for depth := 0; depth < 8 && len(frontier) > 0; depth++ {
		var next []string
		for _, cur := range frontier {
			rows, err := tx.QueryContext(ctx, "SELECT id FROM bank_transactions WHERE superseded_by = ? ORDER BY id", cur)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var p string
				if err := rows.Scan(&p); err != nil {
					_ = rows.Close()
					return nil, err
				}
				if !slices.Contains(ids, p) {
					ids = append(ids, p)
					next = append(next, p)
				}
			}
			if err := rows.Close(); err != nil {
				return nil, err
			}
		}
		frontier = next
	}
	return ids, nil
}

type catRow struct {
	categoryID sql.Null[string]
	origin     organisation.Origin
	source     string
	by         audit.Actor
	at         time.Time
	inherited  bool
}

func readCategoryRow(ctx context.Context, tx *sql.Tx, typ organisation.ObjectType, id string) (*catRow, error) {
	var r catRow
	var kind, via string
	var user sql.Null[string]
	var at store.Timestamp
	err := tx.QueryRowContext(ctx, "SELECT category_id, origin, source, set_by_kind, set_by_user, set_by_via, set_at FROM category_assignments WHERE object_type = ? AND object_id = ?", string(typ), id).
		Scan(&r.categoryID, &r.origin, &r.source, &kind, &user, &via, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.by, r.at = actorFrom(kind, user, via), at.Time
	return &r, nil
}

type labelRow struct {
	labelID   string
	removed   bool
	origin    organisation.Origin
	source    string
	by        audit.Actor
	at        time.Time
	inherited bool
}

func readLabelRows(ctx context.Context, tx *sql.Tx, typ organisation.ObjectType, id string) ([]labelRow, error) {
	rows, err := tx.QueryContext(ctx, "SELECT label_id, removed, origin, source, set_by_kind, set_by_user, set_by_via, set_at FROM label_assignments WHERE object_type = ? AND object_id = ? ORDER BY label_id", string(typ), id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []labelRow
	for rows.Next() {
		var r labelRow
		var kind, via string
		var user sql.Null[string]
		var at store.Timestamp
		if err := rows.Scan(&r.labelID, &r.removed, &r.origin, &r.source, &kind, &user, &via, &at); err != nil {
			return nil, err
		}
		r.by, r.at = actorFrom(kind, user, via), at.Time
		out = append(out, r)
	}
	return out, rows.Err()
}

// effective is what holds for an object: the nearest category row along the
// lineage, and each label's nearest row, except that a farther row the nearer
// one may not replace (organisation.CanReplace) wins.
type effective struct {
	category *catRow
	labels   map[string]labelRow
	order    []string // label IDs in the order first seen
}

func loadEffective(ctx context.Context, tx *sql.Tx, obj organisation.ObjectRef) (effective, error) {
	e := effective{labels: map[string]labelRow{}}
	ids, err := lineage(ctx, tx, obj)
	if err != nil {
		return e, err
	}
	for i, id := range ids {
		r, err := readCategoryRow(ctx, tx, obj.Type, id)
		if err != nil {
			return e, err
		}
		// The nearest row wins, unless it could not replace this farther one:
		// an inherited user row outranks the object's own rule or model row.
		if r != nil && (e.category == nil || !organisation.CanReplace(r.origin, e.category.origin)) {
			r.inherited = i > 0
			e.category = r
		}
		rows, err := readLabelRows(ctx, tx, obj.Type, id)
		if err != nil {
			return e, err
		}
		for _, r := range rows {
			cur, seen := e.labels[r.labelID]
			if seen && organisation.CanReplace(r.origin, cur.origin) {
				continue
			}
			r.inherited = i > 0
			e.labels[r.labelID] = r
			if !seen {
				e.order = append(e.order, r.labelID)
			}
		}
	}
	return e, nil
}

func readAssignments(ctx context.Context, tx *sql.Tx, obj organisation.ObjectRef) (organisation.Assignments, error) {
	out := organisation.Assignments{Object: obj}
	e, err := loadEffective(ctx, tx, obj)
	if err != nil {
		return out, err
	}
	if c := e.category; c != nil {
		ca := &organisation.CategoryAssignment{Origin: c.origin, Source: c.source, By: c.by, At: c.at, Inherited: c.inherited}
		if c.categoryID.Valid {
			d, err := getDef(ctx, tx, organisation.KindCategory, c.categoryID.V)
			if err != nil {
				return out, err
			}
			ca.Category = &d
		}
		out.Category = ca
	}
	for _, id := range e.order {
		r := e.labels[id]
		d, err := getDef(ctx, tx, organisation.KindLabel, id)
		if err != nil {
			return out, err
		}
		la := organisation.LabelAssignment{Label: d, Origin: r.origin, Source: r.source, By: r.by, At: r.at, Removed: r.removed, Inherited: r.inherited}
		if r.removed {
			out.Removed = append(out.Removed, la)
		} else {
			out.Labels = append(out.Labels, la)
		}
	}
	byName := func(a, b organisation.LabelAssignment) int {
		return strings.Compare(organisation.NameKey(a.Label.Name), organisation.NameKey(b.Label.Name))
	}
	slices.SortFunc(out.Labels, byName)
	slices.SortFunc(out.Removed, byName)
	if obj.Type.BucketMember() {
		rows, err := tx.QueryContext(ctx, "SELECT bucket_id FROM bucket_members WHERE object_type = ? AND object_id = ?", string(obj.Type), obj.ID)
		if err != nil {
			return out, err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return out, err
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return out, err
		}
		for _, id := range ids {
			d, err := getDef(ctx, tx, organisation.KindBucket, id)
			if err != nil {
				return out, err
			}
			out.Buckets = append(out.Buckets, d)
		}
		slices.SortFunc(out.Buckets, func(a, b organisation.Definition) int {
			return strings.Compare(organisation.NameKey(a.Name), organisation.NameKey(b.Name))
		})
	}
	return out, nil
}

// Get returns what is assigned to an object.
func (s *Store) Get(ctx context.Context, obj organisation.ObjectRef) (organisation.Assignments, error) {
	if err := checkObject(obj); err != nil {
		return organisation.Assignments{}, err
	}
	var out organisation.Assignments
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := objectExists(ctx, tx, obj); err != nil {
			return err
		}
		var err error
		out, err = readAssignments(ctx, tx, obj)
		return err
	})
	return out, err
}

// auditTarget names an object in the audit log by its id. A merchant is
// named by its record's id (ADR-0058), which is not derived from its name.
func auditTarget(obj organisation.ObjectRef) (typ, id string) {
	return string(obj.Type), obj.ID
}

func (s *Store) assignmentEvent(ctx context.Context, tx *sql.Tx, actor audit.Actor, obj organisation.ObjectRef, k organisation.Kind, change string, origin organisation.Origin, from string, at time.Time) error {
	typ, id := auditTarget(obj)
	return s.audit.WriteAudit(ctx, tx, audit.Event{Action: audit.OrganisationAssignmentChanged, Actor: actor, TargetType: typ, TargetID: id, At: at,
		Details: map[string]string{"kind": string(k), "change": change, "origin": string(origin), "from": from, "via": viaOf(actor)}})
}

func originName(r *catRow) string {
	if r == nil {
		return "none"
	}
	return string(r.origin)
}

// SetCategory records the person's category for an object (nil: the person
// chose none). The row is the object's own: it overrides an inherited or an
// automatic assignment, and automatic origins never replace it.
func (s *Store) SetCategory(ctx context.Context, actor audit.Actor, obj organisation.ObjectRef, categoryID *string) (organisation.Assignments, error) {
	if err := checkObject(obj); err != nil {
		return organisation.Assignments{}, err
	}
	if categoryID != nil && !resource.ValidID(*categoryID) {
		return organisation.Assignments{}, fieldErr("category_id", "category_id must be an identifier")
	}
	now := s.now().UTC()
	var out organisation.Assignments
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := objectWritable(ctx, tx, obj); err != nil {
			return err
		}
		if categoryID != nil {
			d, err := getDef(ctx, tx, organisation.KindCategory, *categoryID)
			if errors.Is(err, resource.ErrNotFound) {
				return fieldErr("category_id", "category_id names no category")
			}
			if err != nil {
				return err
			}
			if d.Archived() {
				return organisation.ErrArchived
			}
		}
		prev, err := readCategoryRow(ctx, tx, obj.Type, obj.ID)
		if err != nil {
			return err
		}
		if err := upsertCategory(ctx, tx, obj, categoryID, organisation.OriginUser, "", actor, now); err != nil {
			return err
		}
		change := "set"
		if categoryID == nil {
			change = "none"
		}
		if err := s.assignmentEvent(ctx, tx, actor, obj, organisation.KindCategory, change, organisation.OriginUser, originName(prev), now); err != nil {
			return err
		}
		out, err = readAssignments(ctx, tx, obj)
		return err
	})
	return out, err
}

func upsertCategory(ctx context.Context, tx *sql.Tx, obj organisation.ObjectRef, categoryID *string, origin organisation.Origin, source string, actor audit.Actor, at time.Time) error {
	cols, vals := objectColumns(obj)
	kind, user, via := whoOf(actor)
	vals = append(vals, categoryID, string(origin), source, kind, user, via, store.Timestamp{Time: at})
	_, err := tx.ExecContext(ctx, "INSERT INTO category_assignments ("+cols+", category_id, origin, source, set_by_kind, set_by_user, set_by_via, set_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"+
		" ON CONFLICT (object_type, object_id) DO UPDATE SET category_id = excluded.category_id, origin = excluded.origin, source = excluded.source,"+
		" set_by_kind = excluded.set_by_kind, set_by_user = excluded.set_by_user, set_by_via = excluded.set_by_via, set_at = excluded.set_at", vals...)
	return err
}

// ResetCategory forgets the person's choice for an object (a category, or
// none), so a rule or a model may assign again. It returns
// [organisation.ErrNoAssignment] when the object has no category assignment,
// and [organisation.ErrInheritedAssignment] when it only inherits one from the
// pending transaction it replaces.
func (s *Store) ResetCategory(ctx context.Context, actor audit.Actor, obj organisation.ObjectRef) (organisation.Assignments, error) {
	if err := checkObject(obj); err != nil {
		return organisation.Assignments{}, err
	}
	now := s.now().UTC()
	var out organisation.Assignments
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := objectExists(ctx, tx, obj); err != nil {
			return err
		}
		prev, err := readCategoryRow(ctx, tx, obj.Type, obj.ID)
		if err != nil {
			return err
		}
		if prev == nil {
			e, err := loadEffective(ctx, tx, obj)
			if err != nil {
				return err
			}
			if e.category != nil && e.category.inherited {
				return organisation.ErrInheritedAssignment
			}
			return organisation.ErrNoAssignment
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM category_assignments WHERE object_type = ? AND object_id = ?", string(obj.Type), obj.ID); err != nil {
			return err
		}
		if err := s.assignmentEvent(ctx, tx, actor, obj, organisation.KindCategory, "reset", organisation.OriginUser, originName(prev), now); err != nil {
			return err
		}
		out, err = readAssignments(ctx, tx, obj)
		return err
	})
	return out, err
}

func upsertLabel(ctx context.Context, tx *sql.Tx, obj organisation.ObjectRef, labelID string, removed bool, origin organisation.Origin, source string, actor audit.Actor, at time.Time) error {
	cols, vals := objectColumns(obj)
	kind, user, via := whoOf(actor)
	vals = append(vals, labelID, removed, string(origin), source, kind, user, via, store.Timestamp{Time: at})
	_, err := tx.ExecContext(ctx, "INSERT INTO label_assignments ("+cols+", label_id, removed, origin, source, set_by_kind, set_by_user, set_by_via, set_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"+
		" ON CONFLICT (object_type, object_id, label_id) DO UPDATE SET removed = excluded.removed, origin = excluded.origin, source = excluded.source,"+
		" set_by_kind = excluded.set_by_kind, set_by_user = excluded.set_by_user, set_by_via = excluded.set_by_via, set_at = excluded.set_at", vals...)
	return err
}

func labelFrom(r labelRow, ok bool) string {
	if !ok {
		return "none"
	}
	return string(r.origin)
}

// AddLabel puts a label on an object as the person's. It replaces an
// automatic assignment of the same label and a removal the person made
// earlier.
func (s *Store) AddLabel(ctx context.Context, actor audit.Actor, obj organisation.ObjectRef, labelID string) (organisation.Assignments, error) {
	if err := checkObject(obj); err != nil {
		return organisation.Assignments{}, err
	}
	if !resource.ValidID(labelID) {
		return organisation.Assignments{}, fieldErr("label_id", "label_id must be an identifier")
	}
	now := s.now().UTC()
	var out organisation.Assignments
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := objectWritable(ctx, tx, obj); err != nil {
			return err
		}
		d, err := getDef(ctx, tx, organisation.KindLabel, labelID)
		if err != nil {
			return err
		}
		if d.Archived() {
			return organisation.ErrArchived
		}
		e, err := loadEffective(ctx, tx, obj)
		if err != nil {
			return err
		}
		prev, ok := e.labels[labelID]
		if err := upsertLabel(ctx, tx, obj, labelID, false, organisation.OriginUser, "", actor, now); err != nil {
			return err
		}
		if err := s.assignmentEvent(ctx, tx, actor, obj, organisation.KindLabel, "added", organisation.OriginUser, labelFrom(prev, ok), now); err != nil {
			return err
		}
		out, err = readAssignments(ctx, tx, obj)
		return err
	})
	return out, err
}

// RemoveLabel takes a label off an object and records the removal as the
// person's, so a rule or a model cannot add it back. It returns
// [organisation.ErrNoAssignment] when the object does not have the label.
func (s *Store) RemoveLabel(ctx context.Context, actor audit.Actor, obj organisation.ObjectRef, labelID string) (organisation.Assignments, error) {
	if err := checkObject(obj); err != nil {
		return organisation.Assignments{}, err
	}
	if !resource.ValidID(labelID) {
		return organisation.Assignments{}, fieldErr("label_id", "label_id must be an identifier")
	}
	now := s.now().UTC()
	var out organisation.Assignments
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := objectExists(ctx, tx, obj); err != nil {
			return err
		}
		if _, err := getDef(ctx, tx, organisation.KindLabel, labelID); err != nil {
			return err
		}
		e, err := loadEffective(ctx, tx, obj)
		if err != nil {
			return err
		}
		prev, ok := e.labels[labelID]
		if !ok || prev.removed {
			return organisation.ErrNoAssignment
		}
		if err := upsertLabel(ctx, tx, obj, labelID, true, organisation.OriginUser, "", actor, now); err != nil {
			return err
		}
		if err := s.assignmentEvent(ctx, tx, actor, obj, organisation.KindLabel, "removed", organisation.OriginUser, labelFrom(prev, true), now); err != nil {
			return err
		}
		out, err = readAssignments(ctx, tx, obj)
		return err
	})
	return out, err
}

func bucketObject(obj organisation.ObjectRef) error {
	if err := checkObject(obj); err != nil {
		return err
	}
	if !obj.Type.BucketMember() {
		return organisation.ErrNotApplicable
	}
	return nil
}

// AddToBucket puts a commitment or account in an active bucket; being in it
// already is not an error.
func (s *Store) AddToBucket(ctx context.Context, actor audit.Actor, bucketID string, obj organisation.ObjectRef) error {
	if err := bucketObject(obj); err != nil {
		return err
	}
	now := s.now().UTC()
	return s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := objectExists(ctx, tx, obj); err != nil {
			return err
		}
		b, err := getDef(ctx, tx, organisation.KindBucket, bucketID)
		if err != nil {
			return err
		}
		if b.Archived() {
			return organisation.ErrArchived
		}
		var cm, ac any
		if obj.Type == organisation.ObjectCommitment {
			cm = obj.ID
		} else {
			ac = obj.ID
		}
		kind, user, via := whoOf(actor)
		res, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO bucket_members (bucket_id, object_type, object_id, commitment_id, account_id, added_by_kind, added_by_user, added_by_via, added_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
			bucketID, string(obj.Type), obj.ID, cm, ac, kind, user, via, store.Timestamp{Time: now})
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		return s.assignmentEvent(ctx, tx, actor, obj, organisation.KindBucket, "added", organisation.OriginUser, "none", now)
	})
}

// RemoveFromBucket takes an object out of a bucket (an archived one too). It
// returns [organisation.ErrNoAssignment] when it is not in it.
func (s *Store) RemoveFromBucket(ctx context.Context, actor audit.Actor, bucketID string, obj organisation.ObjectRef) error {
	if err := bucketObject(obj); err != nil {
		return err
	}
	now := s.now().UTC()
	return s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := getDef(ctx, tx, organisation.KindBucket, bucketID); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, "DELETE FROM bucket_members WHERE bucket_id = ? AND object_type = ? AND object_id = ?", bucketID, string(obj.Type), obj.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return organisation.ErrNoAssignment
		}
		return s.assignmentEvent(ctx, tx, actor, obj, organisation.KindBucket, "removed", organisation.OriginUser, string(organisation.OriginUser), now)
	})
}

// ListMembers returns up to f.Limit+1 members of a bucket after f.After
// ("type:id"), ordered by object type then ID.
func (s *Store) ListMembers(ctx context.Context, bucketID string, f organisation.MemberFilter) ([]organisation.Member, error) {
	afterType, afterID, _ := strings.Cut(f.After, ":")
	q := "SELECT object_type, object_id, added_by_kind, added_by_user, added_by_via, added_at FROM bucket_members WHERE bucket_id = ?" +
		" AND (object_type > ? OR (object_type = ? AND object_id > ?)) ORDER BY object_type, object_id"
	if f.Limit > 0 {
		q += " LIMIT " + strconv.Itoa(f.Limit+1)
	}
	var out []organisation.Member
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := getDef(ctx, tx, organisation.KindBucket, bucketID); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, q, bucketID, afterType, afterType, afterID)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var m organisation.Member
			var typ, kind, via string
			var user sql.Null[string]
			var at store.Timestamp
			if err := rows.Scan(&typ, &m.Object.ID, &kind, &user, &via, &at); err != nil {
				return err
			}
			m.Object.Type, m.By, m.At = organisation.ObjectType(typ), actorFrom(kind, user, via), at.Time
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, err
}

// MemberIDs returns the IDs of every member of a bucket of type t, sorted.
func (s *Store) MemberIDs(ctx context.Context, bucketID string, t organisation.ObjectType) ([]string, error) {
	var out []string
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := getDef(ctx, tx, organisation.KindBucket, bucketID); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT object_id FROM bucket_members WHERE bucket_id = ? AND object_type = ? ORDER BY object_id", bucketID, string(t))
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
		}
		return rows.Err()
	})
	return out, err
}

func placeholders(n int) string { return "?" + strings.Repeat(", ?", n-1) }

func anyArgs(ids []string) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

func queryIDs(ctx context.Context, tx *sql.Tx, q string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Select returns the IDs of the objects of type t whose effective category and
// labels satisfy f, sorted.
func (s *Store) Select(ctx context.Context, t organisation.ObjectType, f organisation.Filter) ([]string, error) {
	if !t.Valid() {
		return nil, fieldErr("object_type", "unknown object type")
	}
	var out []string
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for _, d := range []struct {
			k   organisation.Kind
			ids []string
		}{{organisation.KindCategory, f.CategoryIDs}, {organisation.KindLabel, f.LabelIDs}} {
			for _, id := range d.ids {
				if _, err := getDef(ctx, tx, d.k, id); errors.Is(err, resource.ErrNotFound) {
					return &organisation.UnknownDefinitionError{Kind: d.k, ID: id}
				} else if err != nil {
					return err
				}
			}
		}
		// A category matches its children too.
		cats := slices.Clone(f.CategoryIDs)
		if len(cats) > 0 {
			kids, err := queryIDs(ctx, tx, "SELECT id FROM categories WHERE parent_id IN ("+placeholders(len(cats))+")", anyArgs(cats)...)
			if err != nil {
				return err
			}
			cats = append(cats, kids...)
		}
		// Candidates: objects with a matching row of their own, and for a
		// transaction the ones that inherit from such a row.
		var cand []string
		if len(cats) > 0 {
			ids, err := queryIDs(ctx, tx, "SELECT object_id FROM category_assignments WHERE object_type = ? AND category_id IN ("+placeholders(len(cats))+")", append([]any{string(t)}, anyArgs(cats)...)...)
			if err != nil {
				return err
			}
			cand = append(cand, ids...)
		}
		if len(f.LabelIDs) > 0 {
			ids, err := queryIDs(ctx, tx, "SELECT object_id FROM label_assignments WHERE object_type = ? AND removed = 0 AND label_id IN ("+placeholders(len(f.LabelIDs))+")", append([]any{string(t)}, anyArgs(f.LabelIDs)...)...)
			if err != nil {
				return err
			}
			cand = append(cand, ids...)
		}
		slices.Sort(cand)
		cand = slices.Compact(cand)
		if t == organisation.ObjectTransaction {
			seen := map[string]bool{}
			for _, id := range cand {
				seen[id] = true
			}
			frontier := cand
			for depth := 0; depth < 8 && len(frontier) > 0; depth++ {
				var next []string
				for start := 0; start < len(frontier); start += 500 {
					chunk := frontier[start:min(start+500, len(frontier))]
					ids, err := queryIDs(ctx, tx, "SELECT superseded_by FROM bank_transactions WHERE superseded_by IS NOT NULL AND id IN ("+placeholders(len(chunk))+")", anyArgs(chunk)...)
					if err != nil {
						return err
					}
					for _, id := range ids {
						if !seen[id] {
							seen[id] = true
							next = append(next, id)
						}
					}
				}
				frontier = next
			}
			cand = cand[:0]
			for id := range seen {
				cand = append(cand, id)
			}
			slices.Sort(cand)
		}
		if len(cand) > organisation.MaxSelected {
			return organisation.ErrTooManyObjects
		}
		catSet := map[string]bool{}
		for _, id := range cats {
			catSet[id] = true
		}
		for _, id := range cand {
			e, err := loadEffective(ctx, tx, organisation.ObjectRef{Type: t, ID: id})
			if err != nil {
				return err
			}
			if len(cats) > 0 && (e.category == nil || !e.category.categoryID.Valid || !catSet[e.category.categoryID.V]) {
				continue
			}
			if len(f.LabelIDs) > 0 {
				ok := false
				for _, l := range f.LabelIDs {
					if r, has := e.labels[l]; has && !r.removed {
						ok = true
						break
					}
				}
				if !ok {
					continue
				}
			}
			out = append(out, id)
		}
		return nil
	})
	return out, err
}

func keptReason(existing organisation.Origin) organisation.KeptReason {
	if existing == organisation.OriginUser {
		return organisation.KeptUserDecision
	}
	return organisation.KeptHigherOrigin
}

// ApplyAutomatic applies what a rule or a model proposes for one object. It
// never replaces what organisation.CanReplace forbids: a category or label the
// person assigned, a category the person cleared, a label the person removed
// and anything a stronger automatic origin set are left as they are and
// reported in Kept. Archived definitions are not applied. It changes nothing
// about the object itself, and an object with no change gets no audit event.
func (s *Store) ApplyAutomatic(ctx context.Context, actor audit.Actor, obj organisation.ObjectRef, a organisation.Automatic) (organisation.AutomaticResult, error) {
	if err := checkObject(obj); err != nil {
		return organisation.AutomaticResult{}, err
	}
	if err := commitments.Validation(a.Validate()); err != nil {
		return organisation.AutomaticResult{}, err
	}
	now := s.now().UTC()
	var res organisation.AutomaticResult
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res = organisation.AutomaticResult{}
		if err := objectWritable(ctx, tx, obj); err != nil {
			return err
		}
		e, err := loadEffective(ctx, tx, obj)
		if err != nil {
			return err
		}
		if a.CategoryID != nil {
			id := *a.CategoryID
			d, err := getDef(ctx, tx, organisation.KindCategory, id)
			if errors.Is(err, resource.ErrNotFound) {
				return fieldErr("category_id", "category_id names no category")
			}
			if err != nil {
				return err
			}
			cur := e.category
			switch {
			case d.Archived():
				res.Kept = append(res.Kept, organisation.Kept{Kind: organisation.KindCategory, ID: id, Reason: organisation.KeptArchived})
			case cur != nil && !organisation.CanReplace(cur.origin, a.Origin):
				reason := keptReason(cur.origin)
				if cur.categoryID.Valid && cur.categoryID.V == id {
					reason = organisation.KeptPresent
				}
				res.Kept = append(res.Kept, organisation.Kept{Kind: organisation.KindCategory, ID: id, Reason: reason})
			case cur != nil && !cur.inherited && cur.categoryID.Valid && cur.categoryID.V == id && cur.origin == a.Origin && cur.source == a.Source:
				res.Kept = append(res.Kept, organisation.Kept{Kind: organisation.KindCategory, ID: id, Reason: organisation.KeptPresent})
			default:
				if err := upsertCategory(ctx, tx, obj, &id, a.Origin, a.Source, actor, now); err != nil {
					return err
				}
				res.CategoryApplied = true
			}
		}
		seen := map[string]bool{}
		for _, id := range a.LabelIDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			d, err := getDef(ctx, tx, organisation.KindLabel, id)
			if errors.Is(err, resource.ErrNotFound) {
				return fieldErr("label_ids", "label_ids names no label")
			}
			if err != nil {
				return err
			}
			cur, has := e.labels[id]
			switch {
			case d.Archived():
				res.Kept = append(res.Kept, organisation.Kept{Kind: organisation.KindLabel, ID: id, Reason: organisation.KeptArchived})
			case has && cur.removed:
				res.Kept = append(res.Kept, organisation.Kept{Kind: organisation.KindLabel, ID: id, Reason: organisation.KeptUserDecision})
			case has && !organisation.CanReplace(cur.origin, a.Origin):
				reason := keptReason(cur.origin)
				if cur.origin == organisation.OriginUser {
					reason = organisation.KeptPresent // the person's own label is there already
				}
				res.Kept = append(res.Kept, organisation.Kept{Kind: organisation.KindLabel, ID: id, Reason: reason})
			case has && !cur.inherited && cur.origin == a.Origin && cur.source == a.Source:
				res.Kept = append(res.Kept, organisation.Kept{Kind: organisation.KindLabel, ID: id, Reason: organisation.KeptPresent})
			default:
				if err := upsertLabel(ctx, tx, obj, id, false, a.Origin, a.Source, actor, now); err != nil {
					return err
				}
				res.LabelsApplied = append(res.LabelsApplied, id)
			}
		}
		if !res.Changed() {
			return nil
		}
		typ, tid := auditTarget(obj)
		return s.audit.WriteAudit(ctx, tx, audit.Event{Action: audit.OrganisationAutomaticApplied, Actor: actor, TargetType: typ, TargetID: tid, At: now,
			Details: map[string]string{"origin": string(a.Origin), "category": strconv.FormatBool(res.CategoryApplied), "labels": strconv.Itoa(len(res.LabelsApplied)), "via": viaOf(actor)}})
	})
	return res, err
}
