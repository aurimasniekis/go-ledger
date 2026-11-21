package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
)

const (
	sealedA = "v1.c2VhbGVkLXNlY3JldC1hLXN5bnRoZXRpYy1ub3QtcmVhbC0w"
	sealedB = "v1.c2VhbGVkLXNlY3JldC1iLXN5bnRoZXRpYy1ub3QtcmVhbC0x"
)

func TestTwoFactorLifecycle(t *testing.T) {
	ctx := context.Background()
	s, _, al, alice, bob := setup(t)
	at := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	if st, err := s.TwoFactor(ctx, alice); err != nil || st.Enabled() || st.PendingSecret != "" || st.RecoveryCodes != 0 {
		t.Fatalf("empty: %+v %v", st, err)
	}
	// Two sessions: confirming keeps the current one only.
	for i, id := range []string{"01JZ0000000000000000000201", "01JZ0000000000000000000202"} {
		if err := s.CreateSession(ctx, session(id, alice, hexOf(byte('a'+i)), at)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetPendingTOTP(ctx, alice, sealedA, at); err != nil {
		t.Fatal(err)
	}
	st, _ := s.TwoFactor(ctx, alice)
	if st.Enabled() || st.PendingSecret != sealedA || !st.PendingAt.Equal(at) {
		t.Fatalf("pending: %+v", st)
	}
	// Recovery codes cannot be issued before TOTP is enabled.
	if err := s.ReplaceRecoveryCodes(ctx, alice, []string{hexOf('1')}, at); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("codes before enabling: %v", err)
	}
	// A confirmation of a secret that is no longer pending changes nothing.
	if _, err := s.ConfirmTOTP(ctx, alice, sealedB, 5, nil, "", at, nil); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("stale confirm: %v", err)
	}
	ev := func(ended int) []audit.Event {
		return []audit.Event{{Action: audit.TwoFactorEnabled, Actor: audit.Actor{Kind: audit.ActorUser, UserID: alice}, TargetType: "user", TargetID: alice, At: at,
			Details: map[string]string{"method": "totp", "replaced": "false", "sessions_ended": "1"}}}
	}
	ended, err := s.ConfirmTOTP(ctx, alice, sealedA, 100, []string{hexOf('1'), hexOf('2')}, "01JZ0000000000000000000201", at, ev)
	if err != nil || ended != 1 {
		t.Fatalf("confirm: %d %v", ended, err)
	}
	st, _ = s.TwoFactor(ctx, alice)
	if !st.Enabled() || st.Secret != sealedA || st.PendingSecret != "" || st.LastStep != 100 || st.RecoveryCodes != 2 || !st.EnabledAt.Equal(at) {
		t.Fatalf("enabled: %+v", st)
	}
	if list, _ := s.ListSessions(ctx, alice); len(list) != 1 || list[0].ID != "01JZ0000000000000000000201" {
		t.Fatalf("sessions after confirm: %+v", list)
	}
	// Steps are used once, in order; another secret's step is refused.
	for _, c := range []struct {
		sealed string
		step   int64
		ok     bool
	}{{sealedA, 100, false}, {sealedA, 99, false}, {sealedA, 101, true}, {sealedA, 101, false}, {sealedB, 200, false}} {
		if ok, err := s.UseTOTPStep(ctx, alice, c.sealed, c.step); err != nil || ok != c.ok {
			t.Errorf("use step %d: %v %v, want %v", c.step, ok, err, c.ok)
		}
	}
	// Recovery codes are consumed; another user's hash does not match.
	if n, ok, err := s.UseRecoveryCode(ctx, bob, hexOf('1')); err != nil || ok || n != 0 {
		t.Errorf("bob: %d %v %v", n, ok, err)
	}
	if n, ok, err := s.UseRecoveryCode(ctx, alice, hexOf('1')); err != nil || !ok || n != 1 {
		t.Errorf("use: %d %v %v", n, ok, err)
	}
	if _, ok, _ := s.UseRecoveryCode(ctx, alice, hexOf('1')); ok {
		t.Error("a used recovery code worked again")
	}
	if err := s.ReplaceRecoveryCodes(ctx, alice, []string{hexOf('3')}, at, audit.Event{Action: audit.RecoveryCodesReset,
		Actor: audit.Actor{Kind: audit.ActorUser, UserID: alice}, TargetType: "user", TargetID: alice, At: at}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.UseRecoveryCode(ctx, alice, hexOf('2')); ok {
		t.Error("a replaced recovery code still works")
	}
	// A re-enrolment keeps the active secret until it is confirmed.
	if err := s.SetPendingTOTP(ctx, alice, sealedB, at); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.TwoFactor(ctx, alice); st.Secret != sealedA || st.PendingSecret != sealedB {
		t.Fatalf("re-enrolment: %+v", st)
	}
	dis := audit.Event{Action: audit.TwoFactorDisabled, Actor: audit.System, TargetType: "user", TargetID: alice, At: at, Details: map[string]string{"method": "totp", "by": "host"}}
	evs := func(int) []audit.Event { return []audit.Event{dis} }
	if was, _, err := s.DisableTwoFactor(ctx, alice, "", evs); err != nil || !was {
		t.Fatalf("disable: %v %v", was, err)
	}
	if st, _ := s.TwoFactor(ctx, alice); st.Enabled() || st.PendingSecret != "" || st.RecoveryCodes != 0 {
		t.Fatalf("after disable: %+v", st)
	}
	if was, _, err := s.DisableTwoFactor(ctx, alice, "", evs); err != nil || was {
		t.Fatalf("disable again: %v %v", was, err)
	}
	got, _ := al.List(ctx, audit.Filter{Limit: 100})
	var actions []string
	for _, r := range got {
		if strings.HasPrefix(string(r.Action), "two_factor.") {
			actions = append(actions, string(r.Action))
		}
	}
	// Oldest first; the second disable wrote nothing.
	if strings.Join(actions, " ") != "two_factor.enabled two_factor.recovery_codes_regenerated two_factor.disabled" {
		t.Errorf("audit %v", actions)
	}
}
