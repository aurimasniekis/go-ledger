package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func get(t *testing.T, h http.Handler) (int, report, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	var rep report
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	return rec.Code, rep, rec.Body.String()
}

func TestHealthzIsAlwaysOK(t *testing.T) {
	code, rep, _ := get(t, Healthz())
	if code != 200 || rep.Status != "ok" {
		t.Errorf("got %d %+v", code, rep)
	}
}

func TestReadinessIncludesDatabaseCheck(t *testing.T) {
	var dbErr error
	r := NewReadiness(nil)
	r.Add(Func("database", func(context.Context) error { return dbErr }))

	code, rep, _ := get(t, r.Handler())
	if code != 200 || rep.Status != "ready" || rep.Checks["database"] != "ok" {
		t.Errorf("healthy: %d %+v", code, rep)
	}

	dbErr = errors.New("open /secret/path/ledger.db: disk I/O error")
	code, rep, body := get(t, r.Handler())
	if code != 503 || rep.Status != "not_ready" || rep.Checks["database"] != "failed" {
		t.Errorf("down: %d %+v", code, rep)
	}
	if strings.Contains(body, "/secret/path") {
		t.Errorf("error text leaked into the response: %s", body)
	}
}

// Optional integrations are not readiness checks (D17): with only the
// database registered, the report names the database and nothing else, and a
// failing optional integration cannot change the answer.
func TestReadinessListsOnlyRegisteredRequiredChecks(t *testing.T) {
	r := NewReadiness(nil)
	r.Add(Func("database", func(context.Context) error { return nil }))
	_, rep, _ := get(t, r.Handler())
	if !reflect.DeepEqual(r.Names(), []string{"database"}) || len(rep.Checks) != 1 {
		t.Errorf("names %v, checks %v", r.Names(), rep.Checks)
	}
}

func TestReadinessWithNoChecksIsReady(t *testing.T) {
	code, rep, _ := get(t, NewReadiness(nil).Handler())
	if code != 200 || len(rep.Checks) != 0 {
		t.Errorf("got %d %+v", code, rep)
	}
}
