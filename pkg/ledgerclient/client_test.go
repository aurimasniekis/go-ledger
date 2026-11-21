package ledgerclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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

func TestPaymentMatchRequests(t *testing.T) {
	var got []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		raw, _ := json.Marshal(b)
		got = append(got, r.Method+" "+r.URL.Path+" "+r.URL.RawQuery+" if-match="+r.Header.Get("If-Match")+" "+string(raw))
		if r.Method == "GET" && r.URL.Path == "/api/v1/payment-matches" {
			writeJSON(w, 200, Page[PaymentMatch]{})
			return
		}
		writeJSON(w, 200, PaymentMatch{ID: "m1", Version: 2})
	})
	ctx := context.Background()
	why := "returned"
	if _, err := c.ListPaymentMatches(ctx, PaymentMatchFilter{States: []string{MatchSuggested}, Origins: []string{OriginModel, OriginAutoRule}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreatePaymentMatch(ctx, NewPaymentMatch{TransactionID: "t1", OccurrenceID: "o1", Amount: Money{Amount: "8.00", Currency: "EUR"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ConfirmPaymentMatch(ctx, "m1", 1, MatchDecision{AllowOverpayment: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CorrectPaymentMatch(ctx, "m1", 2, MatchCorrection{Amount: Money{Amount: "9.00", Currency: "EUR"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ReversePaymentMatch(ctx, "m1", 3, &why); err != nil {
		t.Fatal(err)
	}
	if _, err := c.UnmatchPaymentMatch(ctx, "m1", 4, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RejectPaymentMatch(ctx, "m1", 5, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RunPaymentMatching(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET /api/v1/payment-matches origin=model_suggestion&origin=auto_rule&state=suggested if-match= null",
		`POST /api/v1/payment-matches  if-match= {"amount":{"amount":"8.00","currency":"EUR"},"occurrence_id":"o1","transaction_id":"t1"}`,
		`POST /api/v1/payment-matches/m1/confirm  if-match="1" {"allow_overpayment":true}`,
		`POST /api/v1/payment-matches/m1/correct  if-match="2" {"amount":{"amount":"9.00","currency":"EUR"}}`,
		`POST /api/v1/payment-matches/m1/reverse  if-match="3" {"reason":"returned"}`,
		`POST /api/v1/payment-matches/m1/unmatch  if-match="4" {}`,
		`POST /api/v1/payment-matches/m1/reject  if-match="5" {}`,
		`POST /api/v1/payment-matching/runs  if-match= {}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("requests:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
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

// A write whose answer is larger than the client keeps has still happened: Raw reports the real status and the
// created resource, flagged as truncated, and not an error a caller might retry.
func TestRawReportsATooLargeAnswerAsTruncatedNotAsAFailure(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "/api/v1/commitments/01JD3Y4C8V9M2Q7W5R6T0XZBNT")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(strings.Repeat("x", maxResponseBytes+10)))
	})
	r, err := c.Raw(context.Background(), http.MethodPost, "/commitments", nil, []byte("{}"), "")
	if err != nil {
		t.Fatalf("a too-large answer must not be an error: %v", err)
	}
	if r.Status != http.StatusCreated || !r.Truncated || len(r.Body) != 0 || r.Location != "/api/v1/commitments/01JD3Y4C8V9M2Q7W5R6T0XZBNT" {
		t.Errorf("got status %d truncated=%v body=%d location=%q", r.Status, r.Truncated, len(r.Body), r.Location)
	}
}

// TestBankConnectionRequestShape checks the paths, query, bodies and If-Match
// of the bank connection methods; the real handlers are exercised by the CLI
// and MCP tests in cmd/ledger.
func TestBankConnectionRequestShape(t *testing.T) {
	const id = "01JD3Y4C8V9M2Q7W5R6T0XZBNA"
	conn := BankConnection{ID: id, Status: "active", Version: 4, Accounts: []BankAccount{}}
	var seen []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("If-Match")+" "+strings.TrimSpace(string(body)))
		switch {
		case r.URL.Path == "/api/v1/bank-applications":
			writeJSON(w, 200, map[string]any{"items": []BankApplication{{Name: "app", Mode: "app"}}})
		case strings.HasSuffix(r.URL.Path, "/institutions"):
			writeJSON(w, 200, map[string]any{"items": []BankInstitution{{ID: "b", Country: "FI"}}})
		case r.URL.Path == "/api/v1/bank-connections" && r.Method == "GET":
			writeJSON(w, 200, Page[BankConnection]{Items: []BankConnection{conn}})
		case strings.HasSuffix(r.URL.Path, "/reconnect"), r.Method == "POST" && r.URL.Path == "/api/v1/bank-connections":
			writeJSON(w, 200, BankConsentStart{Connection: conn, AuthorizationURL: "https://bank.example/consent"})
		default:
			writeJSON(w, 200, conn)
		}
	})
	ctx := context.Background()
	if apps, err := c.ListBankApplications(ctx); err != nil || len(apps) != 1 {
		t.Fatalf("applications: %v %v", apps, err)
	}
	if in, err := c.ListBankInstitutions(ctx, "my app", "FI"); err != nil || len(in) != 1 {
		t.Fatalf("institutions: %v %v", in, err)
	}
	if p, err := c.ListBankConnections(ctx, ListOptions{Limit: 5}); err != nil || len(p.Items) != 1 {
		t.Fatalf("list: %v %v", p, err)
	}
	if s, err := c.StartBankConnection(ctx, NewBankConnection{Application: "app", InstitutionID: "b", Country: "FI"}); err != nil || s.AuthorizationURL == "" {
		t.Fatalf("start: %v %v", s, err)
	}
	if _, err := c.GetBankConnection(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ReconnectBankConnection(ctx, id, 30); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RefreshBankConnection(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DisconnectBankConnection(ctx, id, 4); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET /api/v1/bank-applications  ",
		"GET /api/v1/bank-applications/my%20app/institutions?country=FI  ",
		"GET /api/v1/bank-connections?limit=5  ",
		"POST /api/v1/bank-connections  " + `{"application":"app","institution_id":"b","country":"FI"}`,
		"GET /api/v1/bank-connections/" + id + "  ",
		"POST /api/v1/bank-connections/" + id + "/reconnect  " + `{"valid_for_days":30}`,
		"POST /api/v1/bank-connections/" + id + "/refresh  {}",
		"POST /api/v1/bank-connections/" + id + "/disconnect \"4\" {}",
	}
	if strings.Join(seen, "\n") != strings.Join(want, "\n") {
		t.Errorf("requests:\n%s\nwant:\n%s", strings.Join(seen, "\n"), strings.Join(want, "\n"))
	}
}

func TestRefreshWaitsLongerThanTheOrdinaryTimeout(t *testing.T) {
	slow := func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		writeJSON(w, 200, map[string]any{"id": "bc_1", "refresh_running": true})
	}
	srv := httptest.NewServer(http.HandlerFunc(slow))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, secretToken, WithTimeout(100*time.Millisecond), WithRefreshTimeout(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.RefreshBankConnection(context.Background(), "bc_1")
	if err != nil || !b.RefreshRunning {
		t.Fatalf("refresh = %+v, %v; want the slow answer", b, err)
	}
	// The same wait through the raw call (the stdio MCP path).
	if _, err := c.Raw(context.Background(), "POST", "/bank-connections/bc_1/refresh", nil, []byte("{}"), ""); err != nil {
		t.Errorf("raw refresh: %v", err)
	}
	// Any other call keeps the ordinary timeout.
	if _, err := c.Raw(context.Background(), "GET", "/bank-connections/bc_1", nil, nil, ""); err == nil {
		t.Error("an ordinary slow call must time out")
	}
	// The refresh timeout never lowers the ordinary one.
	c2, _ := New(srv.URL, secretToken, WithTimeout(5*time.Second), WithRefreshTimeout(time.Millisecond))
	if c2.slow.Timeout != 5*time.Second {
		t.Errorf("slow timeout = %v", c2.slow.Timeout)
	}
}

func TestRatesRequestShapeAndExactValues(t *testing.T) {
	var seen []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path+"?"+r.URL.Query().Encode())
		switch {
		case r.Method == http.MethodPost:
			writeJSON(w, 200, map[string]any{"trigger": "manual", "started_at": "2026-11-21T10:00:00Z", "finished_at": "2026-11-21T10:00:01Z",
				"outcome": "failed", "currencies_refreshed": 0, "error": map[string]string{"kind": "unavailable", "code": "timeout"}})
		case strings.HasSuffix(r.URL.Path, "/status"):
			writeJSON(w, 200, map[string]any{"enabled": true, "provider": "frankfurter", "source": "ECB", "reporting_currency": "EUR", "refresh_available": true,
				"refresh_interval_seconds": 86400, "refresh_after_seconds": 86400, "stale_after_days": 5, "last_run": nil, "last_success_at": nil, "as_of": "2026-11-21T10:00:00Z"})
		default:
			writeJSON(w, 200, map[string]any{"items": []map[string]any{{"provider": "frankfurter", "source": "ECB", "base": "USD", "quote": "EUR", "value": "0.9200",
				"date": "2026-11-13", "retrieved_at": "2026-11-16T09:00:00Z", "first_retrieved_at": "2026-11-16T09:00:00Z", "confirmed_through": "2026-11-16",
				"stale": true, "stale_reason": "not_retrieved_recently", "previous": nil}}, "next_cursor": nil})
		}
	})
	ctx := context.Background()
	p, err := c.ListRates(ctx, RateFilter{ListOptions: ListOptions{Limit: 5}, Provider: "frankfurter", Source: "ECB", Base: "USD", Quote: "EUR", From: "2026-11-01", Until: "2026-11-30"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 1 || p.Items[0].Value != "0.9200" || !p.Items[0].Stale || p.Items[0].StaleReason == nil || *p.Items[0].StaleReason != "not_retrieved_recently" {
		t.Fatalf("items %+v", p.Items)
	}
	st, err := c.GetRatesStatus(ctx)
	if err != nil || !st.Enabled || st.StaleAfterDays != 5 || st.ReportingCurrency == nil || *st.ReportingCurrency != "EUR" {
		t.Fatalf("status %+v err %v", st, err)
	}
	run, err := c.RefreshRates(ctx)
	if err != nil || run.Outcome != "failed" || run.Error == nil || run.Error.Code != "timeout" {
		t.Fatalf("run %+v err %v", run, err)
	}
	want := []string{
		"GET /api/v1/rates?base=USD&date%5Bgte%5D=2026-11-01&date%5Blte%5D=2026-11-30&limit=5&provider=frankfurter&quote=EUR&source=ECB",
		"GET /api/v1/rates/status?",
		"POST /api/v1/rates/refreshes?",
	}
	if strings.Join(seen, "\n") != strings.Join(want, "\n") {
		t.Errorf("requests:\n%s\nwant:\n%s", strings.Join(seen, "\n"), strings.Join(want, "\n"))
	}
}
