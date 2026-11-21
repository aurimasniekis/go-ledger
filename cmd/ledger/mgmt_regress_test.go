package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/aurimasniekis/go-ledger/internal/httpapi"
	"github.com/aurimasniekis/go-ledger/pkg/ledgerclient"
)

// These tests run the management commands against a fake server that answers
// every request with one fixed JSON document and records the bodies of writes.

// cliRun runs the CLI with the common flags pointed at the fake server h.
func cliRun(t *testing.T, h http.HandlerFunc, timeout string, args ...string) (int, string, string) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	tok := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tok, []byte("ldgr_test_value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("LEDGER_TOKEN", "")
	t.Setenv("LEDGER_URL", "")
	t.Setenv("LEDGER_CLIENT_CONFIG", "")
	var out, errb bytes.Buffer
	full := append(append([]string{}, args...), "--url", srv.URL, "--token-file", tok, "--timeout", timeout)
	rc := run(full, &out, &errb)
	return rc, out.String(), errb.String()
}

// bodyRecorder answers every request with reply and keeps the JSON bodies of
// POST and PATCH requests, in order.
type bodyRecorder struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func (r *bodyRecorder) handler(reply any) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost || req.Method == http.MethodPatch {
			var b map[string]any
			if err := json.NewDecoder(req.Body).Decode(&b); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			r.mu.Lock()
			r.bodies = append(r.bodies, b)
			r.mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reply)
	}
}

func (r *bodyRecorder) got() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bodies
}

func userReply() map[string]any {
	return map[string]any{"id": "u1", "username": "alice", "role": "administrator", "disabled": false, "version": 2}
}

func commitmentReply(name, notes string) map[string]any {
	return map[string]any{
		"id": "c1", "name": name, "kind": "server", "notes": notes,
		"amount":     map[string]any{"amount": "1.00", "currency": "EUR"},
		"schedule":   map[string]any{"frequency": "annual", "month": 3, "day": 1, "version": 1},
		"start_date": "2026-01-01", "version": 2,
	}
}

// F1: --disabled is a string flag that takes true or false, as in users list.
func TestUsersUpdateDisabledTakesTrueOrFalse(t *testing.T) {
	for _, val := range []string{"true", "false"} {
		rec := &bodyRecorder{}
		rc, _, errb := cliRun(t, rec.handler(userReply()), "10s", "users", "update", "u1", "--version", "1", "--disabled", val)
		if rc != 0 {
			t.Fatalf("--disabled %s: exit %d: %s", val, rc, errb)
		}
		got := rec.got()
		if len(got) != 1 || got[0]["disabled"] != (val == "true") {
			t.Fatalf("--disabled %s sent %v", val, got)
		}
	}
	rec := &bodyRecorder{}
	rc, _, errb := cliRun(t, rec.handler(userReply()), "10s", "users", "update", "u1", "--version", "1", "--disabled", "yes")
	if rc != 2 || !strings.Contains(errb, "--disabled must be true or false") || len(rec.got()) != 0 {
		t.Fatalf("--disabled yes: exit %d, stderr %q, sent %v", rc, errb, rec.got())
	}
}

// F2: two optional values that are equal must both be sent.
func TestCommitmentsCreateKeepsEqualOptionalValues(t *testing.T) {
	base := []string{"commitments", "create", "--name", "VPS", "--kind", "server", "--amount", "12.50", "--currency", "EUR",
		"--frequency", "monthly", "--day", "15", "--start-date", "2026-01-01"}
	for _, tc := range []struct {
		name   string
		extra  []string
		checks map[string]string
	}{
		{"priority and notes", []string{"--priority", "essential", "--notes", "essential"},
			map[string]string{"priority": "essential", "notes": "essential"}},
		{"end date and notes", []string{"--end-date", "2030-12-31", "--notes", "2030-12-31"},
			map[string]string{"end_date": "2030-12-31", "notes": "2030-12-31"}},
	} {
		rec := &bodyRecorder{}
		rc, _, errb := cliRun(t, rec.handler(commitmentReply("VPS", "")), "10s", append(append([]string{}, base...), tc.extra...)...)
		if rc != 0 {
			t.Fatalf("%s: exit %d: %s", tc.name, rc, errb)
		}
		got := rec.got()
		if len(got) != 1 {
			t.Fatalf("%s: sent %d bodies", tc.name, len(got))
		}
		for k, want := range tc.checks {
			if got[0][k] != want {
				t.Errorf("%s: %s = %v, want %q (body %v)", tc.name, k, got[0][k], want, got[0])
			}
		}
	}
}

// F7: switching to monthly without --interval defaults the interval to 1, as create does.
func TestCommitmentsUpdateMonthlyDefaultsIntervalToOne(t *testing.T) {
	rec := &bodyRecorder{}
	rc, _, errb := cliRun(t, rec.handler(commitmentReply("VPS", "")), "10s",
		"commitments", "update", "c1", "--version", "1", "--frequency", "monthly", "--day", "5")
	if rc != 0 {
		t.Fatalf("exit %d: %s", rc, errb)
	}
	got := rec.got()
	if len(got) != 1 {
		t.Fatalf("sent %d bodies", len(got))
	}
	sc, _ := got[0]["schedule"].(map[string]any)
	if sc["interval"] != float64(1) {
		t.Errorf("interval = %v, want 1 (schedule %v)", sc["interval"], sc)
	}
	if v, ok := sc["month"]; !ok || v != nil {
		t.Errorf("month = %v (present %v), want an explicit null (schedule %v)", v, ok, sc)
	}
}

// F3: an implicit version is reported; an explicit one is not.
func TestImplicitVersionIsReported(t *testing.T) {
	rec := &bodyRecorder{}
	rc, _, errb := cliRun(t, rec.handler(userReply()), "10s", "users", "update", "u1", "--username", "bob")
	if rc != 0 || !strings.Contains(errb, "using version 2") {
		t.Fatalf("implicit version: exit %d, stderr %q", rc, errb)
	}
	rc, _, errb = cliRun(t, rec.handler(userReply()), "10s", "users", "update", "u1", "--version", "2", "--username", "bob")
	if rc != 0 || strings.Contains(errb, "using version") {
		t.Fatalf("explicit version: exit %d, stderr %q", rc, errb)
	}
}

// F5: server text cannot inject newlines or terminal escapes into text output.
func TestTextOutputEscapesControlCharacters(t *testing.T) {
	notes := "todo\n\x1b[31mred\x1b[0m\tend"
	rc, out, errb := cliRun(t, (&bodyRecorder{}).handler(commitmentReply("VPS", notes)), "10s", "commitments", "get", "c1")
	if rc != 0 {
		t.Fatalf("get: exit %d: %s", rc, errb)
	}
	if strings.ContainsAny(out, "\x1b\r") {
		t.Fatalf("control characters reached the output: %q", out)
	}
	if !strings.Contains(out, `todo\x0a\x1b[31mred\x1b[0m\x09end`+"\n") {
		t.Fatalf("notes not escaped: %q", out)
	}

	page := map[string]any{"items": []any{commitmentReply("a\n\x1b[2Jb", "")}, "next_cursor": nil}
	rc, out, errb = cliRun(t, (&bodyRecorder{}).handler(page), "10s", "commitments", "list")
	if rc != 0 || strings.Contains(out, "\x1b") || !strings.Contains(out, `a\x0a\x1b[2Jb`) {
		t.Fatalf("list: exit %d, output %q, stderr %q", rc, out, errb)
	}
}

// F4: --timeout bounds each request, never the whole call: no overall deadline.
func TestCallHasNoOverallDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"items":[],"next_cursor":null}`)
	}))
	t.Cleanup(srv.Close)
	tok := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tok, []byte("ldgr_test_value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &mgmt{stdout: io.Discard, stderr: io.Discard, url: srv.URL, tokenFile: tok, timeout: time.Second,
		env: mgmtEnv{Getenv: func(string) string { return "" }}}
	rc := m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		if _, ok := ctx.Deadline(); ok {
			return errors.New("the call has an overall deadline")
		}
		_, err := c.ListCommitments(ctx, ledgerclient.CommitmentFilter{})
		return err
	})
	if rc != 0 {
		t.Fatalf("exit %d", rc)
	}
}

// The per-request timeout still ends a request that is too slow.
func TestPerRequestTimeoutStillApplies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(5 * time.Second):
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	tok := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tok, []byte("ldgr_test_value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &mgmt{stdout: io.Discard, stderr: io.Discard, url: srv.URL, tokenFile: tok, timeout: 100 * time.Millisecond,
		env: mgmtEnv{Getenv: func(string) string { return "" }}}
	start := time.Now()
	rc := m.call(func(ctx context.Context, c *ledgerclient.Client) error {
		_, err := c.GetUser(ctx, "u1")
		return err
	})
	if rc != 1 || time.Since(start) > 3*time.Second {
		t.Fatalf("exit %d after %v; want 1 and a prompt failure", rc, time.Since(start))
	}
}

// The PATCH members the CLI sends must be exactly the schema's members (the
// version travels in If-Match, so it is the one member the CLI never sends).
func TestPatchMembersMatchWhatTheCLISends(t *testing.T) {
	var doc map[string]any
	if err := yaml.Unmarshal(httpapi.OpenAPIYAML(), &doc); err != nil {
		t.Fatal(err)
	}
	schemaMembers := func(path string) []string {
		var cur any = doc
		for _, k := range []string{"paths", path, "patch", "requestBody", "content", "application/merge-patch+json", "schema", "properties"} {
			m, ok := cur.(map[string]any)
			if !ok {
				t.Fatalf("openapi.yaml has no %s", path)
			}
			cur = m[k]
		}
		props, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("openapi.yaml has no PATCH properties for %s", path)
		}
		var out []string
		for k := range props {
			if k != "version" {
				out = append(out, k)
			}
		}
		slices.Sort(out)
		return out
	}
	sent := func(reply map[string]any, runs ...[]string) []string {
		seen := map[string]bool{}
		for _, args := range runs {
			rec := &bodyRecorder{}
			if rc, _, errb := cliRun(t, rec.handler(reply), "10s", args...); rc != 0 {
				t.Fatalf("%v: exit %d: %s", args, rc, errb)
			}
			for _, b := range rec.got() {
				for k := range b {
					seen[k] = true
				}
			}
		}
		var out []string
		for k := range seen {
			out = append(out, k)
		}
		slices.Sort(out)
		return out
	}
	users := sent(userReply(),
		[]string{"users", "update", "u1", "--version", "1", "--username", "u", "--display-name", "d", "--role", "read-only", "--disabled", "true"},
		[]string{"users", "update", "u1", "--version", "1", "--clear-display-name", "--disabled", "false"},
	)
	if want := schemaMembers("/users/{id}"); !slices.Equal(users, want) {
		t.Errorf("users PATCH: the CLI sends %v, openapi.yaml allows %v", users, want)
	}
	commitments := sent(commitmentReply("n", ""),
		[]string{"commitments", "update", "c1", "--version", "1", "--effective-from", "2026-01-01", "--name", "n", "--kind", "server",
			"--amount", "1.00", "--currency", "EUR", "--frequency", "monthly", "--interval", "1", "--day", "3", "--start-date", "2026-01-01",
			"--end-date", "2030-01-01", "--priority", "essential", "--notes", "n", "--match-tolerance", "0.50"},
		[]string{"commitments", "update", "c1", "--version", "1", "--clear-end-date", "--clear-priority", "--clear-notes", "--clear-match-tolerance"},
	)
	if want := schemaMembers("/commitments/{id}"); !slices.Equal(commitments, want) {
		t.Errorf("commitments PATCH: the CLI sends %v, openapi.yaml allows %v", commitments, want)
	}
}
