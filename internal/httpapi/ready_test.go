package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/config"
)

func TestReadyHandler(t *testing.T) {
	s := New(config.HTTP{MaxBodyBytes: 1024}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	down := errors.New("secret detail")
	cases := []struct {
		name   string
		checks []ReadyCheck
		code   int
		body   string
	}{
		{"ok", []ReadyCheck{{"database", func(context.Context) error { return nil }}}, 200, `{"status":"ok"}`},
		{"failing", []ReadyCheck{{"database", func(context.Context) error { return down }}}, 503, `{"status":"unavailable","failing":["database"]}`},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		s.ReadyHandler(c.checks...).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if rec.Code != c.code || rec.Body.String() != c.body {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), "secret") {
			t.Errorf("%s: error detail leaked", c.name)
		}
	}
}
