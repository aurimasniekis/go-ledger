package ledgerclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const secretToken = "ldgr_test_secret_value"

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, secretToken)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func TestNewValidation(t *testing.T) {
	for _, tc := range []struct {
		url, token string
		opts       []Option
		ok         bool
	}{
		{"http://127.0.0.1:8080", "t", nil, true},
		{"http://localhost:8080/api/v1/", "t", nil, true},
		{"https://ledger.example.com", "t", nil, true},
		{"http://ledger.example.com", "t", nil, false}, // token over plain http
		{"http://ledger.example.com", "t", []Option{WithInsecureHTTP()}, true},
		{"https://ledger.example.com/other", "t", nil, false},
		{"https://u:p@ledger.example.com", "t", nil, false},
		{"ftp://x", "t", nil, false},
		{"https://ledger.example.com", "", nil, false},
		{"https://ledger.example.com", "a b", nil, false},
	} {
		_, err := New(tc.url, tc.token, tc.opts...)
		if (err == nil) != tc.ok {
			t.Errorf("New(%q): err=%v, want ok=%v", tc.url, err, tc.ok)
		}
		if err != nil && strings.Contains(err.Error(), "ledger.example.com") && strings.Contains(err.Error(), "u:p") {
			t.Errorf("error leaks URL credentials: %v", err)
		}
	}
}

func TestRequestShape(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secretToken {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		switch r.Method {
		case "PATCH":
			if r.URL.Path != "/api/v1/commitments/01JD3Y4C8V9M2Q7W5R6T0XZBNA" || r.Header.Get("If-Match") != `"7"` ||
				r.Header.Get("Content-Type") != "application/merge-patch+json" {
				t.Errorf("patch: %s %v", r.URL.Path, r.Header)
			}
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			if v, ok := b["notes"]; !ok || v != nil {
				t.Errorf("explicit null lost: %v", b)
			}
			writeJSON(w, 200, Commitment{ID: "x", Version: 8})
		case "DELETE":
			if r.Header.Get("If-Match") != `"3"` || r.Header.Get("Content-Type") != "" {
				t.Errorf("delete headers: %v", r.Header)
			}
			w.WriteHeader(204)
		default:
			q := r.URL.Query()
			if got := q["kind"]; len(got) != 2 || got[0] != "server" || got[1] != "domain" || q.Get("limit") != "5" || q.Get("cursor") != "abc" {
				t.Errorf("query: %v", q)
			}
			writeJSON(w, 200, Page[Commitment]{})
		}
	})
	ctx := context.Background()
	if _, err := c.UpdateCommitment(ctx, "01JD3Y4C8V9M2Q7W5R6T0XZBNA", 7, MergePatch{"notes": nil}); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteCommitment(ctx, "01JD3Y4C8V9M2Q7W5R6T0XZBNA", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListCommitments(ctx, CommitmentFilter{ListOptions: ListOptions{Limit: 5, Cursor: "abc"}, Kinds: []string{"server", "domain"}}); err != nil {
		t.Fatal(err)
	}
}

func TestPathIDIsEscaped(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/api/v1/users/a%2Fb%3Fc" {
			t.Errorf("path %q", r.URL.EscapedPath())
		}
		writeJSON(w, 404, map[string]any{"code": "not_found", "message": "no such resource", "request_id": "r1"})
	})
	if _, err := c.GetUser(context.Background(), "a/b?c"); !IsNotFound(err) {
		t.Fatalf("err = %v", err)
	}
}

func TestErrorEnvelope(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/commitments":
			writeJSON(w, 400, map[string]any{"code": "validation_failed", "message": "name is required", "field": "name", "request_id": "r1",
				"errors": []map[string]string{{"field": "name", "message": "name is required"}, {"field": "schedule.day", "message": "must be 1-31"}}})
		case "/api/v1/users/01JD3Y4C8V9M2Q7W5R6T0XZBNA":
			w.Header().Set("ETag", `"4"`)
			writeJSON(w, 409, map[string]any{"code": "version_conflict", "message": "stale", "request_id": "r2",
				"reconciliation": map[string]any{"current_version": 4, "current": map[string]any{"id": "01JD3Y4C8V9M2Q7W5R6T0XZBNA", "username": "alice", "version": 4}}})
		case "/api/v1/me":
			writeJSON(w, 401, map[string]any{"code": "unauthenticated", "message": "invalid credentials", "request_id": "r3"})
		case "/api/v1/grants":
			writeJSON(w, 403, map[string]any{"code": "forbidden", "message": "missing grant", "request_id": "r4"})
		case "/api/v1/audit":
			writeJSON(w, 409, map[string]any{"code": "pending_terms_change", "message": "later edit pending", "field": "effective_from", "request_id": "r5"})
		default:
			http.Error(w, "<html>bad gateway</html>", http.StatusBadGateway)
		}
	})
	ctx := context.Background()
	_, err := c.CreateCommitment(ctx, NewCommitment{})
	var ae *APIError
	if !errors.As(err, &ae) || !IsValidation(err) || ae.Status != 400 || ae.RequestID != "r1" || len(ae.FieldErrors()) != 2 || ae.FieldErrors()[1].Field != "schedule.day" {
		t.Fatalf("validation: %#v", err)
	}
	if !strings.Contains(err.Error(), "schedule.day") {
		t.Errorf("message lacks fields: %v", err)
	}

	_, err = c.UpdateUser(ctx, "01JD3Y4C8V9M2Q7W5R6T0XZBNA", 2, MergePatch{"role": "read-only"})
	if !IsVersionConflict(err) || IsConflict(err) {
		t.Fatalf("version conflict: %v", err)
	}
	ae = asAPIError(err)
	var cur User
	if ae.Reconciliation.CurrentVersion != 4 || ae.Reconciliation.DecodeCurrent(&cur) != nil || cur.Username != "alice" {
		t.Fatalf("reconciliation: %+v", ae.Reconciliation)
	}

	if _, err = c.Me(ctx); !IsUnauthenticated(err) {
		t.Fatalf("401: %v", err)
	}
	if _, err = c.Grants(ctx); !IsForbidden(err) {
		t.Fatalf("403: %v", err)
	}
	if _, err = c.ListAudit(ctx, AuditFilter{}); !IsConflict(err) || asAPIError(err).Code != CodePendingTerms || asAPIError(err).Field != "effective_from" {
		t.Fatalf("other conflict: %v", err)
	}
	// Not the envelope (a proxy): the status survives, the HTML does not.
	_, err = c.ListUsers(ctx, UserFilter{})
	ae = asAPIError(err)
	if ae == nil || ae.Status != 502 || ae.Code != "" || strings.Contains(err.Error(), "html") {
		t.Fatalf("non-envelope: %v", err)
	}
}

func TestPagingWalkAndCollect(t *testing.T) {
	var cursors []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		cur := r.URL.Query().Get("cursor")
		cursors = append(cursors, cur)
		if r.URL.Query().Get("role") != "read-only" {
			t.Errorf("filter dropped on page %q", cur)
		}
		switch cur {
		case "":
			writeJSON(w, 200, map[string]any{"items": []User{{ID: "1"}, {ID: "2"}}, "next_cursor": "c2"})
		case "c2":
			writeJSON(w, 200, map[string]any{"items": []User{{ID: "3"}}, "next_cursor": nil})
		}
	})
	got, err := Collect(c.AllUsers(context.Background(), UserFilter{Roles: []string{"read-only"}, ListOptions: ListOptions{Cursor: "ignored"}}))
	if err != nil || len(got) != 3 || got[2].ID != "3" || strings.Join(cursors, ",") != ",c2" {
		t.Fatalf("got %v err %v cursors %v", got, err, cursors)
	}
	// Early break stops fetching.
	cursors = nil
	for _, err := range c.AllUsers(context.Background(), UserFilter{Roles: []string{"read-only"}}) {
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	if len(cursors) != 1 {
		t.Errorf("fetched %d pages after break", len(cursors))
	}
}

func TestPagingStopsOnError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") == "" {
			writeJSON(w, 200, map[string]any{"items": []User{{ID: "1"}}, "next_cursor": "bad"})
			return
		}
		writeJSON(w, 400, map[string]any{"code": "validation_failed", "message": "bad cursor", "field": "cursor", "request_id": "r"})
	})
	got, err := Collect(c.AllUsers(context.Background(), UserFilter{}))
	if got != nil || !IsValidation(err) {
		t.Fatalf("got %v err %v", got, err)
	}
}

func TestNoRedirectAndNoTokenInErrors(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("redirect followed with %q", r.Header.Get("Authorization"))
	}))
	defer other.Close()
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	})
	_, err := c.Me(context.Background())
	if ae := asAPIError(err); ae == nil || ae.Status != 302 {
		t.Fatalf("err = %v", err)
	}
	// A refused connection names the endpoint, never the token.
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	c2, _ := New(url, secretToken)
	_, err = c2.Me(context.Background())
	if err == nil || strings.Contains(err.Error(), secretToken) {
		t.Fatalf("err = %v", err)
	}
}

// WithTimeout applies to a caller's HTTP client that has no timeout of its
// own, and a timeout the caller set is kept.
func TestWithTimeoutAppliesToCallerClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(5 * time.Second):
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, secretToken, WithHTTPClient(&http.Client{}), WithTimeout(100*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := c.GetUser(context.Background(), "x"); err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("the timeout did not apply: err=%v after %v", err, time.Since(start))
	}
	kept, err := New(srv.URL, secretToken, WithHTTPClient(&http.Client{Timeout: time.Hour}), WithTimeout(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if kept.hc.Timeout != time.Hour {
		t.Fatalf("the caller's timeout was replaced: %v", kept.hc.Timeout)
	}
}
