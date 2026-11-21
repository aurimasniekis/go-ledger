package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/auth"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
)

func deviceID(n int) string { return fmt.Sprintf("01JD3Y4C8V9M2Q7W5R6T0X%04d", n) }

func deviceHash(n int) string { return fmt.Sprintf("%064x", n) }

func TestDeviceCookies(t *testing.T) {
	ctx := context.Background()
	s, _, _, alice, bob := setup(t)
	t0 := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	dev := func(n int, user string, at time.Time) auth.DeviceCookie {
		return auth.DeviceCookie{ID: deviceID(n), UserID: user, TokenHash: deviceHash(n), CreatedAt: at, ExpiresAt: at.Add(24 * time.Hour)}
	}
	if _, err := s.DeviceCookieByTokenHash(ctx, deviceHash(1)); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("unknown cookie: %v", err)
	}
	if err := s.IssueDeviceCookie(ctx, dev(1, alice, t0), "", 3); err != nil {
		t.Fatal(err)
	}
	got, err := s.DeviceCookieByTokenHash(ctx, deviceHash(1))
	if err != nil || got != dev(1, alice, t0) {
		t.Fatalf("%+v %v", got, err)
	}
	// Replacing is scoped to the user: bob cannot delete alice's cookie.
	if err := s.IssueDeviceCookie(ctx, dev(2, bob, t0), deviceID(1), 3); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeviceCookieByTokenHash(ctx, deviceHash(1)); err != nil {
		t.Fatalf("another user's issue deleted the cookie: %v", err)
	}
	// Rotation deletes the replaced cookie.
	if err := s.IssueDeviceCookie(ctx, dev(3, alice, t0.Add(time.Minute)), deviceID(1), 3); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeviceCookieByTokenHash(ctx, deviceHash(1)); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("rotated cookie still there: %v", err)
	}
	// The cap keeps the newest; expired cookies go first.
	for n := 4; n <= 6; n++ {
		if err := s.IssueDeviceCookie(ctx, dev(n, alice, t0.Add(time.Duration(n)*time.Minute)), "", 3); err != nil {
			t.Fatal(err)
		}
	}
	for n, want := range map[int]bool{3: false, 4: true, 5: true, 6: true} {
		if _, err := s.DeviceCookieByTokenHash(ctx, deviceHash(n)); (err == nil) != want {
			t.Errorf("cookie %d present=%t, want %t", n, err == nil, want)
		}
	}
	if err := s.IssueDeviceCookie(ctx, dev(7, alice, t0.Add(48*time.Hour)), "", 3); err != nil {
		t.Fatal(err)
	}
	for n, want := range map[int]bool{4: false, 5: false, 6: false, 7: true} {
		if _, err := s.DeviceCookieByTokenHash(ctx, deviceHash(n)); (err == nil) != want {
			t.Errorf("after expiry: cookie %d present=%t, want %t", n, err == nil, want)
		}
	}
	// A password change deletes the user's cookies only.
	if _, err := s.SetPassword(ctx, alice, hash1, "", t0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeviceCookieByTokenHash(ctx, deviceHash(7)); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("alice's cookie survived the password change: %v", err)
	}
	if _, err := s.DeviceCookieByTokenHash(ctx, deviceHash(2)); err != nil {
		t.Errorf("bob's cookie: %v", err)
	}
}
