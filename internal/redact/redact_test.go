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

func TestSecretNamedGroupIsMasked(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{ReplaceAttr: New().ReplaceAttr}))
	l.Info("m", slog.Group("api_token", "value", "abc123"), slog.Group("outer", slog.Group("session", "id", "def456")))
	if strings.Contains(buf.String(), "abc123") || strings.Contains(buf.String(), "def456") {
		t.Fatalf("leaked: %s", buf.String())
	}
}

func TestTextMasksURLParameters(t *testing.T) {
	in := "GET https://idp.example.com/cb?code=zzz&access_token=qqq&state=ok&api_key=kkk failed"
	out := New().Text(in)
	for _, s := range []string{"zzz", "qqq", "kkk"} {
		if strings.Contains(out, s) {
			t.Fatalf("leaked %s: %s", s, out)
		}
	}
	if !strings.Contains(out, "state=ok") {
		t.Fatalf("over-redacted: %s", out)
	}
}

func TestTextKeepsOrdinaryKeyValueText(t *testing.T) {
	for _, in := range []string{
		"currency_code=EUR", "status_code=500, retrying", "exit code=1",
		"config key=http.listen is invalid", "idempotency_key=abc123", "barcode=1", "monkey=banana",
	} {
		if out := New().Text(in); out != in {
			t.Errorf("over-redacted %q -> %q", in, out)
		}
	}
}

func TestTextMasksExactParameters(t *testing.T) {
	for _, in := range []string{
		"/cb?code=zzz", "/x?a=1&key=zzz", "password=zzz", "client_secret=zzz", "refresh_token=zzz", "id_token=zzz", "apikey=zzz", "token=zzz",
	} {
		if out := New().Text(in); strings.Contains(out, "zzz") {
			t.Errorf("leaked in %q -> %q", in, out)
		}
	}
	if out := New().Text("status_code=500, retrying"); out != "status_code=500, retrying" {
		t.Errorf("got %q", out)
	}
}
