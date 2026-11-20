package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestJSONDefaultWithRequestID(t *testing.T) {
	var buf bytes.Buffer
	l, err := New(&buf, "info", "json", nil)
	if err != nil {
		t.Fatal(err)
	}
	l.InfoContext(WithRequestID(context.Background(), "rid-1"), "hello")
	l.Debug("hidden")
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("not one JSON line: %v: %s", err, buf.String())
	}
	if m["request_id"] != "rid-1" || m["msg"] != "hello" {
		t.Fatalf("bad line: %v", m)
	}
}

func TestTextFormatAndErrors(t *testing.T) {
	var buf bytes.Buffer
	l, err := New(&buf, "debug", "text", nil)
	if err != nil {
		t.Fatal(err)
	}
	l.Debug("hello")
	if !strings.Contains(buf.String(), "level=DEBUG msg=hello") {
		t.Fatalf("not text: %s", buf.String())
	}
	if _, err := New(&buf, "loud", "json", nil); err == nil {
		t.Fatal("bad level accepted")
	}
	if _, err := New(&buf, "info", "xml", nil); err == nil {
		t.Fatal("bad format accepted")
	}
}

func TestJobCarriesJobIDAndName(t *testing.T) {
	var buf bytes.Buffer
	l, _ := New(&buf, "info", "json", nil)
	jl, id := Job(l, "sync")
	jl.Info("tick")
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["job_id"] != id || m["job"] != "sync" || len(id) != 26 {
		t.Fatalf("bad job line: %v", m)
	}
}

func TestULID(t *testing.T) {
	a := ulidAt(time.UnixMilli(1_700_000_000_000))
	b := ulidAt(time.UnixMilli(1_700_000_001_000))
	if len(a) != 26 || len(b) != 26 || a >= b {
		t.Fatalf("not sortable: %s %s", a, b)
	}
	if a[0] > '7' {
		t.Fatalf("first character overflows 128 bits: %s", a)
	}
	if strings.Trim(a, crockford) != "" {
		t.Fatalf("bad alphabet: %s", a)
	}
	if first, second := NewULID(), NewULID(); first == second {
		t.Fatal("collision")
	}
}

func TestWithGroupKeepsSingleRequestID(t *testing.T) {
	var buf bytes.Buffer
	base, _ := New(&buf, "info", "json", nil)
	ctx := WithRequestID(context.Background(), "RID")
	base.With("request_id", "RID").WithGroup("g").InfoContext(ctx, "m", "a", 1)
	if n := strings.Count(buf.String(), "request_id"); n != 1 {
		t.Fatalf("want one request_id, got %d: %s", n, buf.String())
	}
}
