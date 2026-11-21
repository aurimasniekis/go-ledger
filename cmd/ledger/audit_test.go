package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/users"
	"github.com/aurimasniekis/go-ledger/internal/httpapi"
	"github.com/aurimasniekis/go-ledger/internal/store"
	userstore "github.com/aurimasniekis/go-ledger/internal/store/users"
)

type auditPage struct {
	Items []struct {
		ID         string            `json:"id"`
		Action     string            `json:"action"`
		ActorKind  string            `json:"actor_kind"`
		ActorUser  *string           `json:"actor_user_id"`
		ActorToken *string           `json:"actor_token_id"`
		TargetType *string           `json:"target_type"`
		TargetID   *string           `json:"target_id"`
		Details    map[string]string `json:"details"`
	} `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

func (f *apiFixture) auditList(t *testing.T, query, as string, status int) auditPage {
	t.Helper()
	rec := f.do("GET", "/api/v1/audit"+query, "", map[string]string{"X-Test-User": f.ids[as]})
	if rec.Code != status {
		t.Fatalf("GET /audit%s as %s: %d %s", query, as, rec.Code, rec.Body.String())
	}
	var p auditPage
	if status == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func TestAuditAPIIsAdministratorOnly(t *testing.T) {
	f := newAPIFixture(t)
	if rec := f.do("GET", "/api/v1/audit", "", nil); rec.Code != 401 {
		t.Errorf("no credentials: %d", rec.Code)
	}
	f.auditList(t, "", "bob", 403) // read-only holds no audit grant
	// A token holds only what it was given: audit:read must be granted explicitly.
	rec := f.do("GET", "/api/v1/audit", "", map[string]string{"X-Test-User": f.ids["alice"], "X-Test-Token-Grants": "users:read"})
	if rec.Code != 403 {
		t.Errorf("token without audit:read: %d", rec.Code)
	}
	rec = f.do("GET", "/api/v1/audit", "", map[string]string{"X-Test-User": f.ids["alice"], "X-Test-Token-Grants": "audit:read"})
	if rec.Code != 200 {
		t.Errorf("token with audit:read: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("POST", "/api/v1/audit", `{}`, map[string]string{"X-Test-User": f.ids["alice"]}); rec.Code != 405 {
		t.Errorf("POST /audit: %d, the log is read-only through the API", rec.Code)
	}
}

func TestAuditAPIListsChangesWithFiltersAndPages(t *testing.T) {
	f := newAPIFixture(t)
	all := f.auditList(t, "?limit=200", "alice", 200)
	// The fixture created four users and disabled one: five entries, oldest first.
	var actions []string
	for _, e := range all.Items {
		actions = append(actions, e.Action)
		if e.ActorKind != "system" || e.ActorUser != nil || e.ActorToken != nil {
			t.Errorf("fixture entries are made by the system: %+v", e)
		}
	}
	if got := strings.Join(actions, ","); got != "user.created,user.created,user.created,user.created,user.disabled" {
		t.Fatalf("actions %s", got)
	}
	if all.Items[0].Details["username"] != "alice" || all.Items[0].Details["role"] != "administrator" {
		t.Errorf("details %v", all.Items[0].Details)
	}

	// A change made through the API is recorded with the actor, in its own transaction.
	carol := f.ids["carol"]
	if rec := f.do("PATCH", "/api/v1/users/"+carol, `{"role":"administrator"}`, map[string]string{"X-Test-User": f.ids["alice"], "If-Match": `"1"`}); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	got := f.auditList(t, "?action=user.role_changed&actor_user_id="+f.ids["alice"]+"&target_id="+carol, "alice", 200)
	if len(got.Items) != 1 || got.Items[0].ActorKind != "user" || got.Items[0].Details["from"] != "read-only" || got.Items[0].Details["to"] != "administrator" {
		t.Fatalf("role change entry: %+v", got.Items)
	}
	if n := len(f.auditList(t, "?action[prefix]=user.&actor_kind=system", "alice", 200).Items); n != 5 {
		t.Errorf("system entries: %d, want 5", n)
	}
	if n := len(f.auditList(t, "?action=user.disabled&action=user.role_changed", "alice", 200).Items); n != 2 {
		t.Errorf("any-of actions: %d, want 2", n)
	}
	if n := len(f.auditList(t, "?at[gte]="+time.Now().Add(time.Hour).Format(time.RFC3339), "alice", 200).Items); n != 0 {
		t.Errorf("future range: %d", n)
	}
	if n := len(f.auditList(t, "?at[lt]="+time.Now().Add(time.Hour).Format(time.RFC3339), "alice", 200).Items); n != 6 {
		t.Errorf("past range: %d, want 6", n)
	}

	// Pagination walks every entry once.
	seen := map[string]bool{}
	q := "?limit=2"
	for pages := 0; ; pages++ {
		p := f.auditList(t, q, "alice", 200)
		for _, e := range p.Items {
			if seen[e.ID] {
				t.Errorf("entry %s seen twice", e.ID)
			}
			seen[e.ID] = true
		}
		if p.NextCursor == nil {
			if pages != 2 {
				t.Errorf("%d pages, want 3", pages+1)
			}
			break
		}
		q = "?limit=2&cursor=" + *p.NextCursor
	}
	if len(seen) != 6 {
		t.Errorf("walked %d entries, want 6", len(seen))
	}

	for _, bad := range []string{"?actor_kind=robot", "?actor_user_id=x", "?at[gte]=yesterday", "?nonsense=1", "?action[gt]=a", "?limit=0", "?cursor=zzz"} {
		f.auditList(t, bad, "alice", 400)
	}
}

func TestAuditedTokenWritesRecordTokenIdentity(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "ledger.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	al := store.NewAuditLog(db)
	us := userstore.New(db, al, nil, nil)
	admin, err := us.Create(ctx, audit.System, users.New{Username: "alice", Role: authz.RoleAdministrator})
	if err != nil {
		t.Fatal(err)
	}
	const tokenID = "01JD3Y4C8V9M2Q7W5R6T0XZBNT"
	authn := httpapi.AuthenticatorFunc(func(r *http.Request) (authz.Principal, error) {
		switch r.Header.Get("X-Test") {
		case "token":
			return authz.TokenPrincipal(tokenID, admin.ID, admin.Role, authz.AllGrants()), nil
		case "user":
			return authz.UserPrincipal(admin.ID, admin.Role), nil
		}
		return authz.Principal{}, httpapi.ErrNoCredentials
	})
	var failing recorderFunc
	rec := &failing
	srv := httpapi.New(config.HTTP{MaxBodyBytes: 1 << 16, ShutdownTimeout: time.Second}, slog.New(slog.DiscardHandler),
		httpapi.WithAuthenticators(authn), httpapi.WithAuditRecorder(rec, nil))
	registerAPI(srv, db, al)
	*rec = al.Record
	do := func(method, path, body, who string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-Test", who)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		return w
	}
	tokenWrites := func() []audit.Record {
		got, err := al.List(ctx, audit.Filter{Actions: []audit.Action{audit.TokenWriteUsed}, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	if w := do("GET", "/api/v1/users", "", "token"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := do("POST", "/api/v1/users", `{"username":"bob","role":"read-only"}`, "user"); w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	if n := len(tokenWrites()); n != 0 {
		t.Fatalf("reads and user writes made %d token.write_used entries", n)
	}
	w := do("POST", "/api/v1/users", `{"username":"carol","role":"read-only","password":"sentinel-secret"}`, "token")
	if w.Code != 400 { // unknown member: refused by the handler, after the token was recorded
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if w := do("POST", "/api/v1/users", `{"username":"carol","role":"read-only"}`, "token"); w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	got := tokenWrites()
	if len(got) != 2 {
		t.Fatalf("%d token.write_used entries, want 2 (every state-changing use, even a refused one)", len(got))
	}
	e := got[1]
	if e.Actor != (audit.Actor{Kind: audit.ActorToken, UserID: admin.ID, TokenID: tokenID}) || e.Details["method"] != "POST" || e.Details["route"] != "/api/v1/users" {
		t.Errorf("entry %+v", e)
	}
	// The change itself is attributed to the token as well.
	created, _ := al.List(ctx, audit.Filter{Actions: []audit.Action{audit.UserCreated}, ActorTokenID: tokenID})
	if len(created) != 1 || created[0].Details["username"] != "carol" {
		t.Errorf("user.created by the token: %+v", created)
	}
	dump, _ := al.List(ctx, audit.Filter{Limit: 200})
	for _, r := range dump {
		for _, v := range r.Details {
			if strings.Contains(v, "sentinel-secret") {
				t.Errorf("a request body value reached the audit log: %+v", r)
			}
		}
	}

	// When the entry cannot be written the write does not happen.
	*rec = func(context.Context, audit.Event) error { return errors.New("disk full") }
	if w := do("POST", "/api/v1/users", `{"username":"dave","role":"read-only"}`, "token"); w.Code != 500 {
		t.Errorf("unaudited token write: %d, want 500", w.Code)
	}
	list, _ := us.List(ctx, users.ListFilter{UsernamePrefix: "dave", Limit: 5})
	if len(list) != 0 {
		t.Error("the write went ahead although its audit entry failed")
	}
}

type recorderFunc func(context.Context, audit.Event) error

func (f recorderFunc) Record(ctx context.Context, e audit.Event) error { return f(ctx, e) }
