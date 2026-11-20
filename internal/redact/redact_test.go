package redact

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

const sentinel = "SENTINEL-redact-secret-0123456789"

func TestSensitiveKey(t *testing.T) {
	for _, k := range []string{"password", "Authorization", "X-Api-Key", "kek_value", "session_id", "refresh_token", "Cookie", "totp"} {
		if !SensitiveKey(k) {
			t.Errorf("%q should be sensitive", k)
		}
	}
	for _, k := range []string{"request_id", "method", "path", "status", "job_id"} {
		if SensitiveKey(k) {
			t.Errorf("%q should not be sensitive", k)
		}
	}
}

func TestTextMasksRegisteredSecretsAndBearer(t *testing.T) {
	r := New(sentinel, "")
	got := r.Text("value " + sentinel + " and Bearer abcdef123456 end")
	if strings.Contains(got, sentinel) || strings.Contains(got, "abcdef123456") {
		t.Fatalf("leaked: %s", got)
	}
	if !strings.Contains(got, Mask) {
		t.Fatalf("no mask: %s", got)
	}
	if r.Text("plain") != "plain" {
		t.Fatal("plain text changed")
	}
	var nilR *Redactor
	if nilR.Text("x") != "x" {
		t.Fatal("nil redactor must pass text through")
	}
}

func TestReplaceAttrInSlog(t *testing.T) {
	var buf bytes.Buffer
	r := New(sentinel)
	l := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{ReplaceAttr: r.ReplaceAttr}))
	l.Info("msg "+sentinel, "password", sentinel, "note", "x "+sentinel, "err", errors.New("boom "+sentinel),
		slog.Group("g", slog.String("api_token", "zzz"), slog.Any("v", sentinel)))
	if strings.Contains(buf.String(), sentinel) || strings.Contains(buf.String(), "zzz") {
		t.Fatalf("leaked: %s", buf.String())
	}
}
