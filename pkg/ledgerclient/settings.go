package ledgerclient

import (
	"context"
	"encoding/json"
	"time"
)

// Runtime settings (ADR-0066). The effective value of a setting comes, highest
// first, from a flag, the environment, the .env file, the YAML file, the value
// stored through these methods, then the built-in default. A member a
// configuration layer sets is pinned: writing it is a 409 setting_pinned (see
// [IsSettingPinned]). Secrets are never returned.

// SettingsApplyState says whether a group's values are in force: "applied",
// "failed" (a component could not rebuild after the last write; the setting
// stays committed) or "restart_required" (stored and valid, but applied only at
// the next start). Code is a stable error code, never a value.
type SettingsApplyState struct {
	State string     `json:"state"`
	Code  string     `json:"code,omitempty"`
	At    *time.Time `json:"at,omitempty"`
}

// SettingsGroupSummary is one entry of [Client.ListSettings].
type SettingsGroupSummary struct {
	Group      string             `json:"group"`
	Grant      string             `json:"grant"`
	Version    int64              `json:"version"`
	UpdatedAt  *time.Time         `json:"updated_at"`
	Pinned     bool               `json:"pinned"`
	ApplyState SettingsApplyState `json:"apply_state"`
}

// SettingsGroup is one group's representation. Values holds the effective
// value of every member by its name (dotted names such as "refresh.interval"
// are flat), as the JSON the server sent, and the secret companions
// ("client_secret_set" and so on); the other fields are the read-only members.
type SettingsGroup struct {
	Version    int64
	UpdatedAt  *time.Time
	Sources    map[string]string
	Pinned     []string
	ApplyState SettingsApplyState
	Values     map[string]json.RawMessage
}

// UnmarshalJSON splits the read-only members from the settings.
func (g *SettingsGroup) UnmarshalJSON(b []byte) error {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return err
	}
	var meta struct {
		Version    int64              `json:"version"`
		UpdatedAt  *time.Time         `json:"updated_at"`
		Sources    map[string]string  `json:"sources"`
		Pinned     []string           `json:"pinned"`
		ApplyState SettingsApplyState `json:"apply_state"`
	}
	if err := json.Unmarshal(b, &meta); err != nil {
		return err
	}
	for _, k := range []string{"version", "updated_at", "sources", "pinned", "apply_state"} {
		delete(all, k)
	}
	*g = SettingsGroup{Version: meta.Version, UpdatedAt: meta.UpdatedAt, Sources: meta.Sources, Pinned: meta.Pinned, ApplyState: meta.ApplyState, Values: all}
	return nil
}

// DeploymentSetting is one configuration-only key (read-only). Value is absent
// for secrets, secret-file paths and application identifiers, whose Set says
// whether they have a value.
type DeploymentSetting struct {
	Key    string          `json:"key"`
	Source string          `json:"source"`
	Value  json.RawMessage `json:"value,omitempty"`
	Set    *bool           `json:"set,omitempty"`
}

// ListSettings returns the singleton settings groups (needs settings:read; the
// integrations groups also need integrations:read). The list is always one page.
func (c *Client) ListSettings(ctx context.Context) ([]SettingsGroupSummary, error) {
	var p Page[SettingsGroupSummary]
	if err := c.do(ctx, request{op: opListSettings}, &p); err != nil {
		return nil, err
	}
	return p.Items, nil
}

// GetDeploymentSettings returns the deployment settings with the layer each
// comes from (needs settings:read). They are never writable through the API.
func (c *Client) GetDeploymentSettings(ctx context.Context) ([]DeploymentSetting, error) {
	var p Page[DeploymentSetting]
	if err := c.do(ctx, request{op: opGetDeploymentSetting}, &p); err != nil {
		return nil, err
	}
	return p.Items, nil
}

// GetSettingsGroup returns a group's effective values with their sources, its
// version and its apply state (needs settings:read).
func (c *Client) GetSettingsGroup(ctx context.Context, group string) (*SettingsGroup, error) {
	var out SettingsGroup
	if err := c.do(ctx, request{op: opGetSettingsGroup, params: map[string]string{"group": group}}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateSettingsGroup applies a merge patch to a group whose current version is
// version (sent as If-Match; a group never written has version 1). A value
// sets the member, nil removes the stored value. Needs settings:write.
func (c *Client) UpdateSettingsGroup(ctx context.Context, group string, version int64, patch MergePatch) (*SettingsGroup, error) {
	var out SettingsGroup
	if err := c.do(ctx, request{op: opUpdateSettingsGroup, params: map[string]string{"group": group}, version: version, body: patch}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ResetSettingsGroup removes every stored member of a group that no
// configuration layer pins, so each falls back to its default (needs
// settings:write).
func (c *Client) ResetSettingsGroup(ctx context.Context, group string, version int64) (*SettingsGroup, error) {
	var out SettingsGroup
	if err := c.do(ctx, request{op: opResetSettingsGroup, params: map[string]string{"group": group}, version: version}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
