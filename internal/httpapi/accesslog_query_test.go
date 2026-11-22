package httpapi

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A bank's return carries a single-use consent code in the query: the access log records the path only.
func TestAccessLogNeverRecordsTheQuery(t *testing.T) {
	var buf bytes.Buffer
	h := accessLogMiddleware(slog.New(slog.NewJSONHandler(&buf, nil)))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/connections/callback?state=S3CRET&code=C0DE", nil))
	out := buf.String()
	if !strings.Contains(out, "/connections/callback") {
		t.Fatalf("path missing from log: %s", out)
	}
	for _, secret := range []string{"S3CRET", "C0DE", "state="} {
		if strings.Contains(out, secret) {
			t.Errorf("log contains %q: %s", secret, out)
		}
	}
}
