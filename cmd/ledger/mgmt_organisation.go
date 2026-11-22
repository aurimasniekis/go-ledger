package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/aurimasniekis/go-ledger/pkg/ledgerclient"
)

// The organisation commands manage the person's own categories, labels and
// buckets and what is assigned to transactions, commitments, accounts and
// merchants (task O4, ADR-0053). Reading needs organisation:read and every
// change organisation:write; a merchant also needs the merchants grant. They
// go through the Management API like every other management command.
//
// The words are the person's own organisation of their data: none of it
// changes a bank fact, and a bucket never holds or allocates money.

func init() {
	for _, k := range []struct{ cmd, kind, noun string }{
		{"categories", ledgerclient.KindCategory, "category"},
		{"labels", ledgerclient.KindLabel, "label"},
		{"buckets", ledgerclient.KindBucket, "bucket"},
	} {
		vs := map[string]verb{
			"list": {"list [--archived true|false] [--name-prefix P]" + map[bool]string{true: " [--parent-id none|ID]", false: ""}[k.kind == ledgerclient.KindCategory] + " [--limit N] [--cursor C] [--all]", definitionsList(k.cmd, k.kind)},
			"get":  {"get <id>", definitionsGet(k.cmd, k.kind)},
			"create": {"create --name N" + map[bool]string{true: " [--parent-id ID]", false: ""}[k.kind == ledgerclient.KindCategory] +
				"   (needs organisation:write)", definitionsCreate(k.cmd, k.kind)},
			"rename":    {"rename <id> --name N [--version N]", definitionsRename(k.cmd, k.kind)},
			"archive":   {"archive <id> [--version N]   (keeps every assignment and stops new use)", definitionsArchive(k.cmd, k.kind, true)},
			"unarchive": {"unarchive <id> [--version N]", definitionsArchive(k.cmd, k.kind, false)},
			"delete":    {"delete <id> [--version N] [--reassign-to ID]   (deleting what is still assigned needs --reassign-to an active " + k.noun + ")", definitionsDelete(k.cmd, k.kind)},
		}
		switch k.kind {
		case ledgerclient.KindCategory:
			vs["move"] = verb{"move <id> --parent ID | --top-level [--version N]   (categories are two levels deep)", categoriesMove}
		case ledgerclient.KindBucket:
			vs["members"] = verb{"members <id> [--limit N] [--cursor C] [--all]   (the commitments and accounts in the bucket)", bucketMembers}
			vs["add-member"] = verb{"add-member <id> --type commitment|account --object ID   (idempotent)", bucketMemberChange(true)}
			vs["remove-member"] = verb{"remove-member <id> --type commitment|account --object ID", bucketMemberChange(false)}
		}
		mgmtVerbs[k.cmd] = vs
	}
	mgmtVerbs["organisation"] = map[string]verb{
		"get":            {"get <type> <id>   (type: transaction, commitment, account or merchant)", orgGet},
		"set-category":   {"set-category <type> <id> --category ID | --none   (your own choice; a rule or model never replaces it)", orgSetCategory},
		"reset-category": {"reset-category <type> <id>   (hands the category back to automatic assignment)", orgResetCategory},
		"add-label":      {"add-label <type> <id> --label ID", orgLabel(true)},
		"remove-label":   {"remove-label <type> <id> --label ID   (a rule or model will not add it back)", orgLabel(false)},
	}
}

func definitionText(w io.Writer, d *ledgerclient.Definition) {
	kv(w, "id", d.ID, "kind", d.Kind, "name", d.Name, "parent", deref(d.ParentID), "archived", fmt.Sprint(d.Archived),
		"version", fmt.Sprint(d.Version), "created", d.CreatedAt, "updated", d.UpdatedAt)
}

func definitionsList(cmd, kind string) func(*mgmt, context.Context, []string) int {
	return func(m *mgmt, _ context.Context, args []string) int {
		fs := m.flags(cmd + " list")
		var f ledgerclient.DefinitionFilter
		fs.StringVar(&f.Archived, "archived", "", "")
		fs.StringVar(&f.NamePrefix, "name-prefix", "", "")
		if kind == ledgerclient.KindCategory {
			fs.StringVar(&f.ParentID, "parent-id", "", "")
		}
		l := addListFlags(fs)
		if _, rc := m.parse(fs, args, false); rc != 0 {
			return max(rc, 0)
		}
		if f.Archived != "" && f.Archived != "true" && f.Archived != "false" {
			say(m.stderr, cmd+" list: --archived must be true or false")
			return 2
		}
		return runList(m, fs, nil, l, func(ctx context.Context, cl *ledgerclient.Client, o ledgerclient.ListOptions) (ledgerclient.Page[ledgerclient.Definition], error) {
			f.ListOptions = o
			return cl.ListDefinitions(ctx, kind, f)
		}, []string{"ID", "NAME", "PARENT", "ARCHIVED", "VERSION"}, func(d ledgerclient.Definition) []any {
			return []any{d.ID, d.Name, deref(d.ParentID), d.Archived, d.Version}
		})
	}
}

func definitionsGet(cmd, kind string) func(*mgmt, context.Context, []string) int {
	return func(m *mgmt, _ context.Context, args []string) int {
		fs := m.flags(cmd + " get")
		id, rc := m.parse(fs, args, true)
		if rc != 0 {
			return max(rc, 0)
		}
		return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
			d, err := c.GetDefinition(ctx, kind, id)
			if err != nil {
				return err
			}
			return m.emit(d, func(w io.Writer) { definitionText(w, d) })
		})
	}
}

func definitionsCreate(cmd, kind string) func(*mgmt, context.Context, []string) int {
	return func(m *mgmt, _ context.Context, args []string) int {
		fs := m.flags(cmd + " create")
		var in ledgerclient.NewDefinition
		var parent string
		fs.StringVar(&in.Name, "name", "", "")
		if kind == ledgerclient.KindCategory {
			fs.StringVar(&parent, "parent-id", "", "")
		}
		if _, rc := m.parse(fs, args, false); rc != 0 {
			return max(rc, 0)
		}
		if strings.TrimSpace(in.Name) == "" {
			say(m.stderr, cmd+" create: --name is required")
			return 2
		}
		if parent != "" {
			in.ParentID = &parent
		}
		return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
			d, err := c.CreateDefinition(ctx, kind, in)
			if err != nil {
				return err
			}
			return m.emit(d, func(w io.Writer) { definitionText(w, d) })
		})
	}
}

// updateDefinition applies patch to the definition at its version (--version,
// else the current one, reported on standard error).
func (m *mgmt) updateDefinition(kind, id string, version int64, patch ledgerclient.MergePatch) int {
	return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		v, err := m.resolveVersion(version, func() (int64, error) { return definitionVersion(ctx, c, kind, id) })
		if err != nil {
			return err
		}
		d, err := c.UpdateDefinition(ctx, kind, id, v, patch)
		if err != nil {
			return err
		}
		return m.emit(d, func(w io.Writer) { definitionText(w, d) })
	})
}

func definitionVersion(ctx context.Context, c *ledgerclient.Client, kind, id string) (int64, error) {
	d, err := c.GetDefinition(ctx, kind, id)
	if err != nil {
		return 0, err
	}
	return d.Version, nil
}

func definitionsRename(cmd, kind string) func(*mgmt, context.Context, []string) int {
	return func(m *mgmt, _ context.Context, args []string) int {
		fs := m.flags(cmd + " rename")
		var name string
		var version int64
		fs.StringVar(&name, "name", "", "")
		fs.Int64Var(&version, "version", 0, "")
		id, rc := m.parse(fs, args, true)
		if rc != 0 {
			return max(rc, 0)
		}
		if strings.TrimSpace(name) == "" {
			say(m.stderr, cmd+" rename: --name is required")
			return 2
		}
		return m.updateDefinition(kind, id, version, ledgerclient.MergePatch{"name": name})
	}
}

func categoriesMove(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("categories move")
	var parent string
	var top bool
	var version int64
	fs.StringVar(&parent, "parent", "", "")
	fs.BoolVar(&top, "top-level", false, "")
	fs.Int64Var(&version, "version", 0, "")
	id, rc := m.parse(fs, args, true)
	if rc != 0 {
		return max(rc, 0)
	}
	if (parent == "") == !top {
		say(m.stderr, "categories move: give exactly one of --parent ID and --top-level")
		return 2
	}
	var p any
	if parent != "" {
		p = parent
	}
	return m.updateDefinition(ledgerclient.KindCategory, id, version, ledgerclient.MergePatch{"parent_id": p})
}

func definitionsArchive(cmd, kind string, archive bool) func(*mgmt, context.Context, []string) int {
	verbName := map[bool]string{true: "archive", false: "unarchive"}[archive]
	return func(m *mgmt, _ context.Context, args []string) int {
		fs := m.flags(cmd + " " + verbName)
		var version int64
		fs.Int64Var(&version, "version", 0, "")
		id, rc := m.parse(fs, args, true)
		if rc != 0 {
			return max(rc, 0)
		}
		return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
			v, err := m.resolveVersion(version, func() (int64, error) { return definitionVersion(ctx, c, kind, id) })
			if err != nil {
				return err
			}
			var d *ledgerclient.Definition
			if archive {
				d, err = c.ArchiveDefinition(ctx, kind, id, v)
			} else {
				d, err = c.UnarchiveDefinition(ctx, kind, id, v)
			}
			if err != nil {
				return err
			}
			return m.emit(d, func(w io.Writer) { definitionText(w, d) })
		})
	}
}

func definitionsDelete(cmd, kind string) func(*mgmt, context.Context, []string) int {
	return func(m *mgmt, _ context.Context, args []string) int {
		fs := m.flags(cmd + " delete")
		var version int64
		var reassign string
		fs.Int64Var(&version, "version", 0, "")
		fs.StringVar(&reassign, "reassign-to", "", "")
		id, rc := m.parse(fs, args, true)
		if rc != 0 {
			return max(rc, 0)
		}
		return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
			v, err := m.resolveVersion(version, func() (int64, error) { return definitionVersion(ctx, c, kind, id) })
			if err != nil {
				return err
			}
			out, err := c.DeleteDefinition(ctx, kind, id, v, reassign)
			if err != nil {
				return err
			}
			return m.emit(map[string]any{"deleted": id, "reassigned": out.Reassigned}, func(w io.Writer) {
				_, _ = fmt.Fprintf(w, "deleted %s (%d assignment(s) moved)\n", printable(id), out.Reassigned)
			})
		})
	}
}

func bucketMembers(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("buckets members")
	l := addListFlags(fs)
	id, rc := m.parse(fs, args, true)
	if rc != 0 {
		return max(rc, 0)
	}
	return runList(m, fs, nil, l, func(ctx context.Context, cl *ledgerclient.Client, o ledgerclient.ListOptions) (ledgerclient.Page[ledgerclient.BucketMember], error) {
		return cl.ListBucketMembers(ctx, id, o)
	}, []string{"TYPE", "OBJECT", "ADDED BY", "ADDED AT"}, func(b ledgerclient.BucketMember) []any {
		return []any{b.ObjectType, b.ObjectID, actorText(b.AddedBy), b.AddedAt}
	})
}

func bucketMemberChange(add bool) func(*mgmt, context.Context, []string) int {
	name := map[bool]string{true: "add-member", false: "remove-member"}[add]
	return func(m *mgmt, _ context.Context, args []string) int {
		fs := m.flags("buckets " + name)
		var typ, obj string
		fs.StringVar(&typ, "type", "", "")
		fs.StringVar(&obj, "object", "", "")
		id, rc := m.parse(fs, args, true)
		if rc != 0 {
			return max(rc, 0)
		}
		if typ != "commitment" && typ != "account" {
			say(m.stderr, "buckets "+name+": --type must be commitment or account")
			return 2
		}
		if obj == "" {
			say(m.stderr, "buckets "+name+": --object is required")
			return 2
		}
		return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
			var err error
			if add {
				err = c.AddBucketMember(ctx, id, typ, obj)
			} else {
				err = c.RemoveBucketMember(ctx, id, typ, obj)
			}
			if err != nil {
				return err
			}
			key := map[bool]string{true: "added", false: "removed"}[add]
			return m.emit(map[string]any{key: obj, "bucket": id, "object_type": typ}, func(w io.Writer) {
				_, _ = fmt.Fprintf(w, "%s %s %s (bucket %s)\n", key, typ, printable(obj), printable(id))
			})
		})
	}
}

// ---- assignments ----

func actorText(a ledgerclient.OrganisationActor) string {
	s := a.Kind
	if a.UserID != nil {
		s += " " + *a.UserID
	}
	if a.Via != nil {
		s += " via " + *a.Via
	}
	return s
}

// parseObject reads "<type> <id>" as the first two arguments, then flags.
func (m *mgmt) parseObject(fs *flag.FlagSet, args []string) (typ, id string, rc int) {
	if len(args) < 2 || strings.HasPrefix(args[0], "-") || strings.HasPrefix(args[1], "-") {
		say(m.stderr, fs.Name()+": an object type (transaction, commitment, account or merchant) and an object ID are required as the first two arguments")
		return "", "", 2
	}
	typ, id = args[0], args[1]
	switch typ {
	case "transaction", "commitment", "account", "merchant":
	default:
		say(m.stderr, fmt.Sprintf("%s: the object type must be transaction, commitment, account or merchant, not %q", fs.Name(), printable(typ)))
		return "", "", 2
	}
	_, rc = m.parse(fs, args[2:], false)
	return typ, id, max(rc, 0)
}

func assignmentsText(w io.Writer, a *ledgerclient.ObjectAssignments) {
	kv(w, "object", a.ObjectType+" "+a.ObjectID)
	if a.Category == nil {
		kv(w, "category", "(none assigned)")
	} else {
		name := "(none chosen)"
		if a.Category.Category != nil {
			name = a.Category.Category.Name + " [" + a.Category.Category.ID + "]"
		}
		kv(w, "category", name, "  origin", originText(a.Category.Origin, a.Category.Source, a.Category.Inherited), "  set", actorText(a.Category.By)+" at "+a.Category.At)
	}
	for _, l := range a.Labels {
		kv(w, "label", l.Label.Name+" ["+l.Label.ID+"]", "  origin", originText(l.Origin, l.Source, l.Inherited))
	}
	for _, l := range a.RemovedLabels {
		kv(w, "removed label", l.Label.Name+" ["+l.Label.ID+"]", "  origin", originText(l.Origin, l.Source, l.Inherited))
	}
	for _, b := range a.Buckets {
		kv(w, "bucket", b.Name+" ["+b.ID+"]")
	}
}

func originText(origin string, source *string, inherited bool) string {
	s := origin
	if source != nil && *source != "" {
		s += " (" + *source + ")"
	}
	if inherited {
		s += ", inherited"
	}
	return s
}

func (m *mgmt) assignments(f func(context.Context, *ledgerclient.Client) (*ledgerclient.ObjectAssignments, error)) int {
	return m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		a, err := f(ctx, c)
		if err != nil {
			return err
		}
		return m.emit(a, func(w io.Writer) { assignmentsText(w, a) })
	})
}

func orgGet(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("organisation get")
	typ, id, rc := m.parseObject(fs, args)
	if rc != 0 {
		return rc
	}
	return m.assignments(func(ctx context.Context, c *ledgerclient.Client) (*ledgerclient.ObjectAssignments, error) {
		return c.GetObjectOrganisation(ctx, typ, id)
	})
}

func orgSetCategory(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("organisation set-category")
	var cat string
	var none bool
	fs.StringVar(&cat, "category", "", "")
	fs.BoolVar(&none, "none", false, "")
	typ, id, rc := m.parseObject(fs, args)
	if rc != 0 {
		return rc
	}
	if (cat == "") == !none {
		say(m.stderr, "organisation set-category: give exactly one of --category ID and --none")
		return 2
	}
	var cp *string
	if cat != "" {
		cp = &cat
	}
	return m.assignments(func(ctx context.Context, c *ledgerclient.Client) (*ledgerclient.ObjectAssignments, error) {
		return c.SetObjectCategory(ctx, typ, id, cp)
	})
}

func orgResetCategory(m *mgmt, _ context.Context, args []string) int {
	fs := m.flags("organisation reset-category")
	typ, id, rc := m.parseObject(fs, args)
	if rc != 0 {
		return rc
	}
	return m.assignments(func(ctx context.Context, c *ledgerclient.Client) (*ledgerclient.ObjectAssignments, error) {
		return c.ResetObjectCategory(ctx, typ, id)
	})
}

func orgLabel(add bool) func(*mgmt, context.Context, []string) int {
	name := map[bool]string{true: "add-label", false: "remove-label"}[add]
	return func(m *mgmt, _ context.Context, args []string) int {
		fs := m.flags("organisation " + name)
		var label string
		fs.StringVar(&label, "label", "", "")
		typ, id, rc := m.parseObject(fs, args)
		if rc != 0 {
			return rc
		}
		if label == "" {
			say(m.stderr, "organisation "+name+": --label is required")
			return 2
		}
		return m.assignments(func(ctx context.Context, c *ledgerclient.Client) (*ledgerclient.ObjectAssignments, error) {
			if add {
				return c.AddObjectLabel(ctx, typ, id, label)
			}
			return c.RemoveObjectLabel(ctx, typ, id, label)
		})
	}
}
