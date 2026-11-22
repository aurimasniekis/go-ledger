package ledgerclient

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSettingsMethods(t *testing.T) {
	var got []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.Path+" if-match="+r.Header.Get("If-Match")+" ct="+r.Header.Get("Content-Type")+" body="+string(b))
		switch {
		case r.URL.Path == "/api/v1/settings":
			writeJSON(w, 200, map[string]any{"items": []map[string]any{{"group": "logging", "grant": "settings", "version": 1, "updated_at": nil, "pinned": false, "apply_state": map[string]any{"state": "applied"}}}, "next_cursor": nil})
		case r.URL.Path == "/api/v1/settings/deployment":
			writeJSON(w, 200, map[string]any{"items": []map[string]any{{"key": "http.listen", "source": "default", "value": "127.0.0.1:8080"}, {"key": "kek.value", "source": "environment", "set": true}}, "next_cursor": nil})
		case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/mcp"):
			writeJSON(w, 409, map[string]any{"code": "setting_pinned", "message": "settings: pinned by the configuration: mcp.enabled (set by environment)", "field": "mcp.enabled", "request_id": "r"})
		default:
			writeJSON(w, 200, map[string]any{"level": "info", "version": 2, "updated_at": "2026-11-22T10:00:00Z", "sources": map[string]string{"level": "api"}, "pinned": []string{}, "apply_state": map[string]any{"state": "applied"}})
		}
	})
	ctx := context.Background()
	list, err := c.ListSettings(ctx)
	if err != nil || len(list) != 1 || list[0].Group != "logging" || list[0].ApplyState.State != "applied" {
		t.Fatalf("ListSettings = %+v, %v", list, err)
	}
	dep, err := c.GetDeploymentSettings(ctx)
	if err != nil || len(dep) != 2 || string(dep[0].Value) != `"127.0.0.1:8080"` || dep[1].Set == nil || !*dep[1].Set || dep[1].Value != nil {
		t.Fatalf("GetDeploymentSettings = %+v, %v", dep, err)
	}
	g, err := c.GetSettingsGroup(ctx, "logging")
	if err != nil || g.Version != 2 || string(g.Values["level"]) != `"info"` || g.Sources["level"] != "api" || len(g.Values) != 1 {
		t.Fatalf("GetSettingsGroup = %+v, %v", g, err)
	}
	if _, err := c.UpdateSettingsGroup(ctx, "logging", 1, MergePatch{"level": "warn"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ResetSettingsGroup(ctx, "logging", 2); err != nil {
		t.Fatal(err)
	}
	_, err = c.UpdateSettingsGroup(ctx, "mcp", 1, MergePatch{"enabled": false})
	if !IsSettingPinned(err) {
		t.Fatalf("pinned write: %v", err)
	}
	want := []string{
		"GET /api/v1/settings if-match= ct= body=",
		"GET /api/v1/settings/deployment if-match= ct= body=",
		"GET /api/v1/settings/logging if-match= ct= body=",
		`PATCH /api/v1/settings/logging if-match="1" ct=application/merge-patch+json body={"level":"warn"}`,
		`DELETE /api/v1/settings/logging if-match="2" ct= body=`,
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("call %d = %q, want %q", i, got[i], w)
		}
	}
}
