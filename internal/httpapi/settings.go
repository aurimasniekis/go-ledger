package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	reg "github.com/aurimasniekis/go-ledger/internal/domain/settings"
	"github.com/aurimasniekis/go-ledger/internal/settings"
)

// Runtime settings routes (task S1, ADR-0066).
const (
	SettingsPath           = APIPrefix + "/settings"
	SettingsDeploymentPath = APIPrefix + "/settings/deployment"
	SettingsGroupPath      = APIPrefix + "/settings/{group}"
)

var (
	grantSettingsRead  = authz.G(authz.ResourceSettings, authz.ActionRead)
	grantSettingsWrite = authz.G(authz.ResourceSettings, authz.ActionWrite)
)

// Error codes of the settings routes (ADR-0066).
const (
	// CodeSettingPinned: 409, a write names a key that a configuration layer
	// (flag, environment, .env file, YAML or a secret file) sets. The answer
	// names the keys and the layers, never a value.
	CodeSettingPinned Code = "setting_pinned"
	// CodeSecretUnreadable: 409, an integration secret stored in the database
	// does not open with the current key-encryption key, so an operation that
	// needs it cannot run (ADR-0066 point 12; raised by the sealed-secret task S2).
	CodeSecretUnreadable Code = "secret_unreadable"
	// CodeReclassificationRequired: 409, a change of the reporting time zone
	// would reclassify due dates and monthly views and the request did not
	// carry reclassify: true (ADR-0066 point 6a; raised by the reporting-group
	// task S3).
	CodeReclassificationRequired Code = "reclassification_required"
)

// SettingsService is what the settings routes need from the runtime settings
// service. *settings.Service implements it. A nil service answers every route
// with 503.
type SettingsService interface {
	Snapshot() *settings.Snapshot
	View(group string, snap *settings.Snapshot) (settings.View, error)
	Update(ctx context.Context, actor audit.Actor, group string, expected int64, patch map[string]json.RawMessage) (*settings.Snapshot, error)
	Reset(ctx context.Context, actor audit.Actor, group string, expected int64) (*settings.Snapshot, error)
}

type applyStateJSON struct {
	State string     `json:"state"`
	Code  string     `json:"code,omitempty"`
	At    *time.Time `json:"at,omitempty"`
}

func toApplyStateJSON(a settings.ApplyState) applyStateJSON {
	return applyStateJSON{State: a.State, Code: a.Code, At: timePtr(a.At)}
}

type settingsListItemJSON struct {
	Group      string         `json:"group"`
	Grant      string         `json:"grant"`
	Version    int64          `json:"version"`
	UpdatedAt  *time.Time     `json:"updated_at"`
	Pinned     bool           `json:"pinned"`
	ApplyState applyStateJSON `json:"apply_state"`
}

// readOnlySettingMembers are the members every group representation carries
// besides its settings. A patch naming one is a 400.
var readOnlySettingMembers = []string{"version", "updated_at", "sources", "pinned", "apply_state"}

// singletonGroup returns the singleton group the path names. Keyed objects
// (one decision task, one provider) have routes of their own (ADR-0066 point 8).
func singletonGroup(name string) (reg.Group, bool) {
	g, ok := reg.Lookup(name)
	if !ok || g.Kind != reg.Singleton {
		return reg.Group{}, false
	}
	return g, true
}

// secretCompanions are a secret member's read-only companions (ADR-0066 point
// 9): whether it is set, when (always absent until sealed secrets exist, task
// S2) and from which configuration layer. The value itself is never read into
// a response.
func secretCompanions(name string) [3]string {
	return [3]string{name + "_set", name + "_set_at", name + "_source"}
}

// groupJSON builds a group's representation: the effective values as
// top-level members, then the read-only members.
func groupJSON(snap *settings.Snapshot, g reg.Group, v settings.View) map[string]any {
	out := make(map[string]any, len(v.Values)+len(readOnlySettingMembers)+3)
	for k, val := range v.Values {
		out[k] = val
	}
	cfg := snap.Config()
	for _, m := range g.Members {
		if !m.Secret {
			continue
		}
		c := secretCompanions(m.Name)
		set := false
		if f, ok := config.FieldByPath(m.Key); ok {
			// Only whether a value exists is read: it never leaves this function.
			s, _ := f.Value(&cfg).(string)
			set = s != ""
		}
		out[c[0]] = set
		out[c[1]] = nil
		if set {
			out[c[2]] = config.Layer(snap.Source(m.Key))
		}
	}
	pinned := v.Pinned
	if pinned == nil {
		pinned = []string{}
	}
	out["version"] = v.Version
	out["updated_at"] = timePtr(v.UpdatedAt)
	out["sources"] = v.Sources
	out["pinned"] = pinned
	out["apply_state"] = toApplyStateJSON(v.Apply)
	return out
}

// settingsError maps the settings service's errors to the API's.
func settingsError(err error) error {
	var ve *reg.ValidationError
	var pe *reg.PinnedError
	switch {
	case errors.Is(err, reg.ErrUnknownGroup):
		return NotFound("no such settings group")
	case errors.As(err, &ve):
		fields := make([]FieldDetail, len(ve.Fields))
		for i, f := range ve.Fields {
			fields[i] = FieldDetail{Field: f.Field, Message: f.Message}
		}
		if len(fields) == 0 {
			return Validation("", "the settings are not valid")
		}
		return ValidationFields(fields)
	case errors.As(err, &pe):
		e := &Error{Status: http.StatusConflict, Code: CodeSettingPinned, Message: pe.Error()}
		for _, k := range pe.Keys {
			e.Errors = append(e.Errors, FieldDetail{Field: k.Key, Message: "pinned by the " + k.Layer + " configuration layer"})
		}
		if len(e.Errors) > 0 {
			e.Field = e.Errors[0].Field
		}
		if len(e.Errors) < 2 {
			e.Errors = nil
		}
		return e
	}
	return err
}

type deploymentItemJSON struct {
	Key    string `json:"key"`
	Source string `json:"source"`
	// Value is present for ordinary keys. Secrets, file paths and application
	// identifiers are never shown: Set says whether they have a value.
	Value any   `json:"value,omitempty"`
	Set   *bool `json:"set,omitempty"`
}

// withheld reports whether a deployment key's value stays out of the API:
// secrets, host file paths and the Enable Banking application identifiers.
func withheld(f config.Field) bool {
	return f.Secret || strings.HasSuffix(f.Path, "_file") || strings.HasSuffix(f.Path, ".app_id")
}

func deploymentItems(snap *settings.Snapshot) ([]deploymentItemJSON, error) {
	cfg := snap.Config()
	var out []deploymentItemJSON
	for _, f := range config.Fields() {
		if !reg.DeploymentKey(f.Path) {
			continue
		}
		it := deploymentItemJSON{Key: f.Path, Source: config.Layer(snap.Source(f.Path))}
		if withheld(f) {
			set := false
			switch v := f.Value(&cfg).(type) {
			case string:
				set = v != ""
			}
			it.Set = &set
		} else {
			raw, err := config.StoredValue(f, &cfg)
			if err != nil {
				return nil, err
			}
			var v any
			if err := json.Unmarshal(raw, &v); err != nil {
				return nil, err
			}
			it.Value = v
		}
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func registerSettings(s *Server, svc SettingsService) {
	unavailable := func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, Unavailable("runtime settings are not available", nil))
	}
	// held reports whether the caller holds the group's own grant: the
	// integrations groups need integrations:read (or write) besides settings.
	held := func(r *http.Request, g reg.Group, write bool) bool {
		res := authz.Resource(g.Grant)
		act := authz.ActionRead
		if write {
			act = authz.ActionWrite
		}
		return principal(r).Allows(authz.G(res, act))
	}
	denied := func(g reg.Group, write bool) error {
		a := "read"
		if write {
			a = "write"
		}
		return Forbidden("this settings group needs the " + g.Grant + ":" + a + " grant")
	}

	s.HandleAPI("GET "+SettingsPath, Requires(grantSettingsRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			unavailable(w, r)
			return
		}
		snap := svc.Snapshot()
		items := []settingsListItemJSON{}
		for _, g := range reg.Groups() {
			if g.Kind != reg.Singleton || !held(r, g, false) {
				continue
			}
			v, err := svc.View(g.Name, snap)
			if err != nil {
				WriteError(w, r, Internal(err))
				return
			}
			items = append(items, settingsListItemJSON{Group: g.Name, Grant: g.Grant, Version: v.Version, UpdatedAt: timePtr(v.UpdatedAt),
				Pinned: len(v.Pinned) > 0, ApplyState: toApplyStateJSON(v.Apply)})
		}
		WriteJSON(w, http.StatusOK, Page[settingsListItemJSON]{Items: items})
	}))

	// The deployment settings stay in the configuration system (ADR-0066 point
	// 2): this is their read-only view.
	s.HandleAPI("GET "+SettingsDeploymentPath, Requires(grantSettingsRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			unavailable(w, r)
			return
		}
		items, err := deploymentItems(svc.Snapshot())
		if err != nil {
			WriteError(w, r, Internal(err))
			return
		}
		WriteJSON(w, http.StatusOK, Page[deploymentItemJSON]{Items: items})
	}))

	s.HandleAPI("GET "+SettingsGroupPath, Requires(grantSettingsRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g, ok := singletonGroup(r.PathValue("group"))
		if !ok {
			WriteError(w, r, NotFound("no such settings group"))
			return
		}
		if !held(r, g, false) {
			WriteError(w, r, denied(g, false))
			return
		}
		if svc == nil {
			unavailable(w, r)
			return
		}
		snap := svc.Snapshot()
		v, err := svc.View(g.Name, snap)
		if err != nil {
			WriteError(w, r, Internal(err))
			return
		}
		SetVersion(w, v.Version)
		WriteJSON(w, http.StatusOK, groupJSON(snap, g, v))
	}))

	// current is the group's representation for a version conflict.
	current := func(g reg.Group) any {
		snap := svc.Snapshot()
		v, err := svc.View(g.Name, snap)
		if err != nil {
			return nil
		}
		return groupJSON(snap, g, v)
	}
	fail := func(w http.ResponseWriter, r *http.Request, g reg.Group, err error) {
		var vc *resource.VersionConflictError
		if errors.As(err, &vc) {
			WriteError(w, r, VersionConflict(vc.Current, current(g)))
			return
		}
		WriteError(w, r, settingsError(err))
	}

	// PATCH is a JSON Merge Patch (ADR-0015). Members are named as in the
	// representation; null removes the stored value. A group always exists: the
	// first write names version 1 (ADR-0066 point 9).
	s.HandleAPI("PATCH "+SettingsGroupPath, Requires(grantSettingsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g, ok := singletonGroup(r.PathValue("group"))
		if !ok {
			WriteError(w, r, NotFound("no such settings group"))
			return
		}
		if !held(r, g, true) {
			WriteError(w, r, denied(g, true))
			return
		}
		known := append([]string{}, readOnlySettingMembers...)
		for _, m := range g.Members {
			known = append(known, m.Name)
			if m.Secret {
				c := secretCompanions(m.Name)
				known = append(known, c[:]...)
			}
		}
		patch, err := DecodeMergePatch(r, known...)
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
		delete(patch, "version")
		for _, k := range sortedKeys(patch) {
			if _, member := g.Member(k); !member {
				WriteError(w, r, Validation(k, k+" is read-only"))
				return
			}
		}
		if svc == nil {
			unavailable(w, r)
			return
		}
		snap, err := svc.Update(r.Context(), actor(r), g.Name, expected, patch)
		if err != nil {
			fail(w, r, g, err)
			return
		}
		v, err := svc.View(g.Name, snap)
		if err != nil {
			WriteError(w, r, Internal(err))
			return
		}
		SetVersion(w, v.Version)
		WriteJSON(w, http.StatusOK, groupJSON(snap, g, v))
	}))

	// DELETE resets every unpinned member to its default (the destroy of a
	// Terraform settings resource); the group itself stays.
	s.HandleAPI("DELETE "+SettingsGroupPath, Requires(grantSettingsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g, ok := singletonGroup(r.PathValue("group"))
		if !ok {
			WriteError(w, r, NotFound("no such settings group"))
			return
		}
		if !held(r, g, true) {
			WriteError(w, r, denied(g, true))
			return
		}
		expected, err := ExpectedVersion(r, nil)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		if svc == nil {
			unavailable(w, r)
			return
		}
		snap, err := svc.Reset(r.Context(), actor(r), g.Name, expected)
		if err != nil {
			fail(w, r, g, err)
			return
		}
		v, err := svc.View(g.Name, snap)
		if err != nil {
			WriteError(w, r, Internal(err))
			return
		}
		SetVersion(w, v.Version)
		WriteJSON(w, http.StatusOK, groupJSON(snap, g, v))
	}))
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
