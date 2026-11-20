package store

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
)

func TestLogAuditWritesOneStructuredLine(t *testing.T) {
	var buf bytes.Buffer
	w := LogAudit(slog.New(slog.NewJSONHandler(&buf, nil)))
	e := audit.Event{
		Action:     audit.UserRoleChanged,
		Actor:      audit.Actor{Kind: audit.ActorToken, UserID: "U", TokenID: "T"},
		TargetType: "user", TargetID: "X",
		At:      time.Date(2026, 1, 2, 3, 4, 5, 6, time.FixedZone("x", 3600)),
		Details: map[string]string{"to": "administrator", "from": "read-only"},
	}
	if err := w.WriteAudit(context.Background(), nil, e); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("%v: %s", err, buf.String())
	}
	want := map[string]any{"msg": "audit", "action": "user.role_changed", "actor_kind": "token", "actor_user_id": "U",
		"actor_token_id": "T", "target_type": "user", "target_id": "X", "at": "2026-01-02T02:04:05.000000006Z",
		"detail_from": "read-only", "detail_to": "administrator"}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v", k, m[k], v)
		}
	}
}
