package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/settings"
	"github.com/aurimasniekis/go-ledger/internal/store"
	settingsstore "github.com/aurimasniekis/go-ledger/internal/store/settings"
)

// settingsSecret stands for every secret value these tests configure. It must
// never appear in a response, a log line or an audit entry.
const settingsSecret = "SENTINEL-settings-api-secret-0123456789"

// newSettingsRig builds the settings service as ledger serve does: the
// configuration (env on top of defaults) with the stored layer from db, and
// the logging group bound to the returned level.
func newSettingsRig(t *testing.T, db *store.DB, aw store.AuditWriter, env map[string]string) (*settings.Service, *slog.LevelVar) {
	t.Helper()
	e := map[string]string{"LEDGER_KEK_VALUE": settingsSecret + "-kek"}
	for k, v := range env {
		e[k] = v
	}
	res, err := config.Load(config.Options{
		LookupEnv: func(k string) (string, bool) { v, ok := e[k]; return v, ok },
		ReadFile: func(name string) ([]byte, error) {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
		},
		Environ: func() []string { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := settings.Open(context.Background(), res, settingsstore.New(db, aw, nil, nil), settings.Options{})
	if err != nil {
		t.Fatal(err)
	}
	lv := new(slog.LevelVar)
	t.Cleanup(bindLogging(svc, lv))
	return svc, lv
}

func (f *apiFixture) admin() map[string]string {
	return map[string]string{"X-Test-User": f.ids["alice"]}
}

// settingsCall sends a settings request as hdr; version "" sends no If-Match.
func (f *apiFixture) settingsCall(method, path, body, version string, hdr map[string]string) (int, map[string]any, string) {
	h := map[string]string{}
	for k, v := range hdr {
		h[k] = v
	}
	if version != "" {
		h["If-Match"] = `"` + version + `"`
	}
	rec := f.do(method, path, body, h)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Body.String()
}

func calendarUTC() calendar.Zone { return calendar.MustLoadZone("UTC") }

func jsonString(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestSettingsGroupsAreReadWithSources(t *testing.T) {
	f := newAPIFixtureEnv(t, calendarUTC(), map[string]string{"LEDGER_MCP_BURST": "7"})
	code, list, raw := f.settingsCall("GET", "/api/v1/settings", "", "", f.admin())
	if code != 200 {
		t.Fatalf("list: %d %s", code, raw)
	}
	byGroup := map[string]map[string]any{}
	for _, it := range list["items"].([]any) {
		m := it.(map[string]any)
		byGroup[m["group"].(string)] = m
	}
	for _, g := range []string{"reporting", "banking", "rates", "merchant_enrichment", "auth", "oidc", "pulse", "mcp", "logging", "alerts"} {
		if byGroup[g] == nil {
			t.Errorf("group %s is not listed", g)
		}
	}
	if len(byGroup) != 10 || byGroup["decision_tasks.alerts"] != nil {
		t.Errorf("only singleton groups are listed: %v", byGroup)
	}
	if byGroup["mcp"]["pinned"] != true || byGroup["logging"]["pinned"] != false || byGroup["logging"]["version"] != float64(1) || byGroup["logging"]["updated_at"] != nil {
		t.Errorf("summaries: mcp=%v logging=%v", byGroup["mcp"], byGroup["logging"])
	}

	rec := f.do("GET", "/api/v1/settings/mcp", "", f.admin())
	if rec.Code != 200 || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("get mcp: %d %s", rec.Code, rec.Body.String())
	}
	var mcp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &mcp)
	src := mcp["sources"].(map[string]any)
	if mcp["burst"] != float64(7) || src["burst"] != "environment" || src["enabled"] != "default" || jsonString(mcp["pinned"]) != `["burst"]` {
		t.Errorf("mcp group: %s", rec.Body.String())
	}
	if as := mcp["apply_state"].(map[string]any); as["state"] != "applied" {
		t.Errorf("apply_state: %v", as)
	}
	for _, name := range []string{"decision_tasks.alerts", "decision_providers.openai", "nope", "a.b.c"} {
		if code, _, _ := f.settingsCall("GET", "/api/v1/settings/"+name, "", "", f.admin()); code != 404 {
			t.Errorf("GET /settings/%s: %d, want 404", name, code)
		}
	}
}

func TestSettingsGrantsAreEnforced(t *testing.T) {
	f := newAPIFixture(t)
	tok := func(grants string) map[string]string {
		return map[string]string{"X-Test-User": f.ids["alice"], "X-Test-Token-Grants": grants}
	}
	patch := `{"level":"warn"}`
	// A read-only user and a token with another grant never reach the routes.
	bob := map[string]string{"X-Test-User": f.ids["bob"]}
	for _, c := range []struct {
		name string
		hdr  map[string]string
		code int
	}{{"read-only user", bob, 403}, {"rates token", tok("rates:read"), 403}, {"integrations token", tok("integrations:write"), 403}} {
		if code, _, _ := f.settingsCall("GET", "/api/v1/settings/logging", "", "", c.hdr); code != c.code {
			t.Errorf("%s GET: %d, want %d", c.name, code, c.code)
		}
		if code, _, _ := f.settingsCall("PATCH", "/api/v1/settings/logging", patch, "1", c.hdr); code != c.code {
			t.Errorf("%s PATCH: %d, want %d", c.name, code, c.code)
		}
	}
	// settings:read reads, never writes; the integrations groups also need integrations:read.
	ro := tok("settings:read")
	if code, _, raw := f.settingsCall("GET", "/api/v1/settings/logging", "", "", ro); code != 200 {
		t.Errorf("settings:read GET logging: %d %s", code, raw)
	}
	if code, _, _ := f.settingsCall("PATCH", "/api/v1/settings/logging", patch, "1", ro); code != 403 {
		t.Errorf("settings:read PATCH: %d, want 403", code)
	}
	if code, _, _ := f.settingsCall("DELETE", "/api/v1/settings/logging", "", "1", ro); code != 403 {
		t.Errorf("settings:read DELETE: %d, want 403", code)
	}
	for _, g := range []string{"pulse", "merchant_enrichment"} {
		if code, body, _ := f.settingsCall("GET", "/api/v1/settings/"+g, "", "", ro); code != 403 || body["code"] != "forbidden" || !strings.Contains(body["message"].(string), "integrations:read") {
			t.Errorf("settings:read GET %s: %d %v, want 403 naming integrations:read", g, code, body)
		}
		if code, _, _ := f.settingsCall("GET", "/api/v1/settings/"+g, "", "", tok("settings:read,integrations:read")); code != 200 {
			t.Errorf("both grants GET %s: %d", g, code)
		}
		if code, _, _ := f.settingsCall("PATCH", "/api/v1/settings/"+g, `{"enabled":false}`, "1", tok("settings:write")); code != 403 {
			t.Errorf("settings:write PATCH %s: %d, want 403 without integrations:write", g, code)
		}
	}
	_, list, _ := f.settingsCall("GET", "/api/v1/settings", "", "", ro)
	for _, it := range list["items"].([]any) {
		if g := it.(map[string]any)["group"]; g == "pulse" || g == "merchant_enrichment" {
			t.Errorf("the list shows %v to a caller without integrations:read", g)
		}
	}
	// settings:write alone changes a settings group (a token's write implies read).
	if code, _, raw := f.settingsCall("PATCH", "/api/v1/settings/logging", patch, "1", tok("settings:write")); code != 200 {
		t.Errorf("settings:write PATCH: %d %s", code, raw)
	}
}

// The logging group is live end to end: a write applies to the running
// logger, is audited with field names only, and a reset restores the default.
func TestSettingsLoggingIsLive(t *testing.T) {
	f := newAPIFixture(t)
	if f.logLevel.Level() != slog.LevelInfo {
		t.Fatalf("starts at %v", f.logLevel.Level())
	}
	code, got, raw := f.settingsCall("PATCH", "/api/v1/settings/logging", `{"level":"warn"}`, "1", f.admin())
	if code != 200 || got["level"] != "warn" || got["version"] != float64(2) || got["sources"].(map[string]any)["level"] != "api" ||
		got["apply_state"].(map[string]any)["state"] != "applied" || got["updated_at"] == nil {
		t.Fatalf("patch: %d %s", code, raw)
	}
	if f.logLevel.Level() != slog.LevelWarn {
		t.Fatalf("the running logger is at %v, want warn", f.logLevel.Level())
	}
	// Audited in the same transaction: names of fields, never values.
	var line map[string]any
	for _, l := range auditLines(t, f.log.String()) {
		if l["action"] == "settings.updated" {
			line = l
		}
	}
	if line == nil || line["actor_user_id"] != f.ids["alice"] || line["target_type"] != "setting_group" || line["target_id"] != "logging" ||
		line["detail_fields"] != "level" || line["detail_group"] != "logging" || strings.Contains(fmt.Sprint(line["detail_fields"], line["detail_group"]), "warn") {
		t.Fatalf("audit line: %v", line)
	}
	// A stale version is a 409 with the current representation.
	code, got, raw = f.settingsCall("PATCH", "/api/v1/settings/logging", `{"level":"error"}`, "1", f.admin())
	rc, _ := got["reconciliation"].(map[string]any)
	if code != 409 || got["code"] != "version_conflict" || rc["current_version"] != float64(2) || rc["current"].(map[string]any)["level"] != "warn" {
		t.Fatalf("stale: %d %s", code, raw)
	}
	if f.logLevel.Level() != slog.LevelWarn {
		t.Errorf("a refused write changed the level to %v", f.logLevel.Level())
	}
	// No version is 428; an empty patch keeps the version and audits nothing new.
	if code, _, _ = f.settingsCall("PATCH", "/api/v1/settings/logging", `{"level":"error"}`, "", f.admin()); code != 428 {
		t.Errorf("no If-Match: %d, want 428", code)
	}
	n := len(auditLines(t, f.log.String()))
	if code, got, _ = f.settingsCall("PATCH", "/api/v1/settings/logging", `{}`, "2", f.admin()); code != 200 || got["version"] != float64(2) || len(auditLines(t, f.log.String())) != n {
		t.Errorf("empty patch: %d %v", code, got)
	}
	// The version may be given in the body.
	if code, got, _ = f.settingsCall("PATCH", "/api/v1/settings/logging", `{"level":"debug","version":2}`, "", f.admin()); code != 200 || got["version"] != float64(3) || f.logLevel.Level() != slog.LevelDebug {
		t.Errorf("version in body: %d %v level=%v", code, got, f.logLevel.Level())
	}
	// null removes the stored value: the default (info) applies again.
	if code, got, _ = f.settingsCall("PATCH", "/api/v1/settings/logging", `{"level":null}`, "3", f.admin()); code != 200 || got["level"] != "info" || got["sources"].(map[string]any)["level"] != "default" || f.logLevel.Level() != slog.LevelInfo {
		t.Errorf("null: %d %v", code, got)
	}
	// DELETE resets to the defaults, audited as settings.reset, and keeps the group.
	f.settingsCall("PATCH", "/api/v1/settings/logging", `{"level":"error"}`, "4", f.admin())
	if code, _, _ = f.settingsCall("DELETE", "/api/v1/settings/logging", "", "", f.admin()); code != 428 {
		t.Errorf("DELETE without If-Match: %d, want 428", code)
	}
	code, got, raw = f.settingsCall("DELETE", "/api/v1/settings/logging", "", "5", f.admin())
	if code != 200 || got["level"] != "info" || got["version"] != float64(6) || f.logLevel.Level() != slog.LevelInfo {
		t.Fatalf("reset: %d %s", code, raw)
	}
	var reset bool
	for _, l := range auditLines(t, f.log.String()) {
		if l["action"] == "settings.reset" {
			reset = true
			if l["detail_fields"] != "level" || l["detail_group"] != "logging" {
				t.Errorf("reset audit: %v", l)
			}
		}
	}
	if !reset {
		t.Error("the reset was not audited")
	}
	// A reset of a group with no row changes nothing and keeps version 1.
	if code, got, _ = f.settingsCall("DELETE", "/api/v1/settings/mcp", "", "1", f.admin()); code != 200 || got["version"] != float64(1) {
		t.Errorf("reset without a row: %d %v", code, got)
	}
}

func TestSettingsWriteRefusals(t *testing.T) {
	f := newAPIFixtureEnv(t, calendarUTC(), map[string]string{"LEDGER_LOG_LEVEL": "debug", "LEDGER_AUTH_OIDC_CLIENT_SECRET": settingsSecret})
	patch := func(group, body string) (int, map[string]any, string) {
		return f.settingsCall("PATCH", "/api/v1/settings/"+group, body, "1", f.admin())
	}
	field := func(m map[string]any, name string) string {
		if m["field"] == name {
			return m["message"].(string)
		}
		es, _ := m["errors"].([]any)
		for _, e := range es {
			if d := e.(map[string]any); d["field"] == name {
				return d["message"].(string)
			}
		}
		return ""
	}
	// Sensitive members, secrets and the reporting zone wait for their gates (S2, S3).
	for _, c := range []struct{ group, body, key string }{
		{"banking", `{"redirect_url":"https://ledger.example.com/connections/callback"}`, "banking.redirect_url"},
		{"oidc", `{"client_secret":"` + settingsSecret + `"}`, "auth.oidc.client_secret"},
		{"auth", `{"require_2fa":true}`, "auth.require_2fa"},
		{"alerts", `{"channels":["pulse"]}`, "alerts.channels"},
		{"reporting", `{"timezone":"UTC"}`, "reporting.timezone"},
		{"pulse", `{"enabled":true}`, "events.pulse.enabled"},
	} {
		code, body, raw := patch(c.group, c.body)
		if code != 400 || body["code"] != "validation_failed" || !strings.Contains(field(body, c.key), "not writable through the API yet") {
			t.Errorf("%s %s: %d %s", c.group, c.body, code, raw)
		}
		if strings.Contains(raw, settingsSecret) {
			t.Errorf("the refusal echoes a secret: %s", raw)
		}
	}
	// A refused DELETE of stored sensitive state is not testable without a
	// writable one; unknown, read-only and invalid members are plain 400s.
	for _, c := range []struct{ group, body, key, want string }{
		{"mcp", `{"nope":1}`, "nope", "unknown field"},
		{"mcp", `{"sources":{}}`, "sources", "read-only"},
		{"mcp", `{"apply_state":null}`, "apply_state", "read-only"},
		{"oidc", `{"client_secret_set":true}`, "client_secret_set", "read-only"},
		{"banking", `{"stale_after":"1s"}`, "banking.stale_after", ""},
		{"mcp", `{"burst":"many"}`, "mcp.burst", ""},
	} {
		code, body, raw := patch(c.group, c.body)
		if code != 400 || body["code"] != "validation_failed" || !strings.Contains(field(body, c.key)+fmt0(body, c.key), c.want) || (c.want == "" && field(body, c.key) == "") {
			t.Errorf("%s %s: %d %s", c.group, c.body, code, raw)
		}
	}
	// Whatever was refused changed nothing.
	_, mcp, _ := f.settingsCall("GET", "/api/v1/settings/mcp", "", "", f.admin())
	if mcp["version"] != float64(1) {
		t.Errorf("a refused write changed mcp: %v", mcp)
	}

	// A key a configuration layer sets is pinned: 409 naming the key and the
	// layer, never its value; the unpinned members of the group stay writable.
	code, body, raw := patch("logging", `{"level":"warn"}`)
	if code != 409 || body["code"] != "setting_pinned" || body["field"] != "log.level" ||
		!strings.Contains(body["message"].(string), "log.level (set by environment)") || strings.Contains(raw, "debug") {
		t.Fatalf("pinned: %d %s", code, raw)
	}
	_, lg, _ := f.settingsCall("GET", "/api/v1/settings/logging", "", "", f.admin())
	if lg["level"] != "debug" || lg["sources"].(map[string]any)["level"] != "environment" || jsonString(lg["pinned"]) != `["level"]` || lg["version"] != float64(1) {
		t.Errorf("pinned logging: %v", lg)
	}
	// Removing a pinned key's stored value is a write to it too.
	if code, body, _ = patch("logging", `{"level":null}`); code != 409 || body["code"] != "setting_pinned" {
		t.Errorf("null on a pinned key: %d %v", code, body)
	}
	// A reset skips pinned members and a group with nothing to change stays at version 1.
	if code, body, _ = f.settingsCall("DELETE", "/api/v1/settings/logging", "", "1", f.admin()); code != 200 || body["version"] != float64(1) || body["level"] != "debug" {
		t.Errorf("reset of a pinned group: %d %v", code, body)
	}

	// Content type and version syntax.
	rec := f.do("PATCH", "/api/v1/settings/mcp", `{"burst":3}`, map[string]string{"X-Test-User": f.ids["alice"], "If-Match": `"1"`, "Content-Type": "application/json"})
	if rec.Code != 415 {
		t.Errorf("PATCH as application/json: %d, want 415", rec.Code)
	}
}

func fmt0(m map[string]any, name string) string {
	es, _ := m["errors"].([]any)
	for _, e := range es {
		if d := e.(map[string]any); d["field"] == name {
			return d["message"].(string)
		}
	}
	return ""
}

// Groups without a live consumer yet say so (ADR-0066 point 17).
func TestSettingsGroupWithoutConsumerRequiresRestart(t *testing.T) {
	f := newAPIFixture(t)
	code, got, raw := f.settingsCall("PATCH", "/api/v1/settings/mcp", `{"requests_per_minute":30}`, "1", f.admin())
	as, _ := got["apply_state"].(map[string]any)
	if code != 200 || got["requests_per_minute"] != float64(30) || as["state"] != "restart_required" {
		t.Fatalf("%d %s", code, raw)
	}
	// The stored layer is validated whole: a value the configuration refuses is a 400 naming the key.
	code, got, _ = f.settingsCall("PATCH", "/api/v1/settings/rates", `{"timeout":"0s"}`, "1", f.admin())
	if code != 400 || got["code"] != "validation_failed" {
		t.Errorf("invalid timeout: %d %v", code, got)
	}
}

// No secret value reaches a response, a log line, an audit entry or an error:
// the configured secrets show only as set, with their source.
func TestSettingsNeverExposeSecrets(t *testing.T) {
	f := newAPIFixtureEnv(t, calendarUTC(), map[string]string{
		"LEDGER_AUTH_OIDC_CLIENT_SECRET": settingsSecret,
	})
	var all strings.Builder
	get := func(path string) map[string]any {
		code, body, raw := f.settingsCall("GET", path, "", "", f.admin())
		all.WriteString(raw)
		if code != 200 {
			t.Fatalf("%s: %d %s", path, code, raw)
		}
		return body
	}
	list := get("/api/v1/settings")
	for _, it := range list["items"].([]any) {
		get("/api/v1/settings/" + it.(map[string]any)["group"].(string))
	}
	oidc := get("/api/v1/settings/oidc")
	if oidc["client_secret_set"] != true || oidc["client_secret_source"] != "environment" || oidc["client_secret_set_at"] != nil {
		t.Errorf("oidc secret companions: %v", oidc)
	}
	if _, ok := oidc["client_secret"]; ok {
		t.Error("the secret is a member of the representation")
	}
	dep := get("/api/v1/settings/deployment")
	var kek, listen map[string]any
	for _, it := range dep["items"].([]any) {
		switch m := it.(map[string]any); m["key"] {
		case "kek.value":
			kek = m
		case "http.listen":
			listen = m
		}
	}
	if kek == nil || kek["set"] != true || kek["value"] != nil || kek["source"] != "environment" || listen == nil || listen["value"] == nil || listen["source"] != "default" {
		t.Errorf("deployment: kek=%v listen=%v", kek, listen)
	}
	// Writes that fail and succeed, and their audit trail.
	for _, c := range []struct{ group, body string }{
		{"oidc", `{"client_secret":"` + settingsSecret + `","issuer":"` + settingsSecret + `"}`},
		{"oidc", `{"client_secret":null}`},
		{"mcp", `{"burst":2}`},
	} {
		_, _, raw := f.settingsCall("PATCH", "/api/v1/settings/"+c.group, c.body, "1", f.admin())
		all.WriteString(raw)
	}
	all.WriteString(f.log.String())
	recs, err := store.NewAuditLog(f.db).List(context.Background(), audit.Filter{Limit: 500})
	if err != nil || len(recs) == 0 {
		t.Fatalf("audit list: %d %v", len(recs), err)
	}
	b, _ := json.Marshal(recs)
	all.Write(b)
	if strings.Contains(all.String(), settingsSecret) {
		i := strings.Index(all.String(), settingsSecret)
		t.Fatalf("a secret value leaked: ...%s...", all.String()[max(0, i-80):min(all.Len(), i+80)])
	}
}
