package enablebanking_test

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/providers/enablebanking"
)

// fakeEB is an offline stand-in for the Enable Banking API, driven by the
// synthetic fixtures in testdata. It checks the application JWT on every
// request, keeps sessions with per-session account uids, strips unlinked
// accounts for a developer-mode (restricted) application, pages transactions
// with continuation keys bound to the query and serves injected failures.
type fakeEB struct {
	t        *testing.T
	srv      *httptest.Server
	pub      *rsa.PublicKey
	kid      string
	env      string // SANDBOX or PRODUCTION
	mode     banking.Mode
	now      func() time.Time
	pageSize int

	mu       sync.Mutex
	linked   map[string]bool // account key -> linked to the application
	sessions map[string]*fakeSession
	n        int
	faults   []fault
	reqs     []string
	authBody map[string]any
}

type fakeSession struct {
	status     string
	validUntil time.Time
	uids       map[string]string // uid -> account key
}

type fault struct {
	match  string // substring of "METHOD /path"
	status int
	file   string // fixture with the body, or
	raw    string // a raw body
	header http.Header
}

const (
	testAppID      = "0f6c3c1e-1b2a-4c3d-9e8f-123456789abc"
	syntheticCode  = "synthetic-authorization-code"
	accountEUR     = "eur"
	accountGBP     = "gbp"
	hashEUR        = "synthetic-hash-eur"
	hashGBP        = "synthetic-hash-gbp"
	echoMarker     = "SECRET-ECHO"
	syntheticIBAN1 = "XX00SYNTHETIC0000000001"
)

var t0 = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

var testKey = sync.OnceValue(func() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return k
})

func testKeyPEM() []byte {
	b, err := x509.MarshalPKCS8PrivateKey(testKey())
	if err != nil {
		panic(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: b})
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newFake(t *testing.T, mode banking.Mode, env banking.Environment, c *clock) *fakeEB {
	t.Helper()
	f := &fakeEB{
		t: t, pub: &testKey().PublicKey, kid: testAppID, mode: mode, now: c.Now, pageSize: 2,
		env:      map[banking.Environment]string{banking.EnvironmentProduction: "PRODUCTION", banking.EnvironmentSandbox: "SANDBOX"}[env],
		linked:   map[string]bool{accountEUR: true, accountGBP: false},
		sessions: map[string]*fakeSession{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// provider returns an adapter pointed at the fake.
func (f *fakeEB) provider(c *clock, mod ...func(*enablebanking.Config)) *enablebanking.Provider {
	f.t.Helper()
	key, err := enablebanking.ParsePrivateKey(testKeyPEM())
	if err != nil {
		f.t.Fatal(err)
	}
	env := banking.EnvironmentProduction
	if f.env == "SANDBOX" {
		env = banking.EnvironmentSandbox
	}
	cfg := enablebanking.Config{
		Application: "eb-" + string(f.mode), AppID: testAppID, Mode: f.mode, Environment: env,
		PrivateKey: key, BaseURL: f.srv.URL, HTTPClient: f.srv.Client(), Now: c.Now,
	}
	for _, m := range mod {
		m(&cfg)
	}
	p, err := enablebanking.New(cfg)
	if err != nil {
		f.t.Fatal(err)
	}
	return p
}

func (f *fakeEB) fail(fs ...fault) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.faults = append(f.faults, fs...)
}

func (f *fakeEB) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reqs...)
}

func (f *fakeEB) count(prefix string) int {
	n := 0
	for _, r := range f.requests() {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

func (f *fakeEB) setSession(status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.sessions {
		s.status = status
	}
}

func fixture(t *testing.T, name string, repl ...string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return []byte(strings.NewReplacer(repl...).Replace(string(b)))
}

func (f *fakeEB) write(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (f *fakeEB) writeJSON(w http.ResponseWriter, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		f.t.Error(err)
	}
	f.write(w, http.StatusOK, b)
}

func (f *fakeEB) errorBody(status int, code string) []byte {
	return []byte(fmt.Sprintf(`{"message":"%s %s","code":%d,"error":"%s"}`, code, echoMarker, status, code))
}

// checkJWT verifies the application JWT as Enable Banking documents it.
func (f *fakeEB) checkJWT(r *http.Request) error {
	auth := r.Header.Get("Authorization")
	tok, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok {
		return fmt.Errorf("no bearer token")
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return fmt.Errorf("token has %d parts", len(parts))
	}
	dec := base64.RawURLEncoding
	var h struct{ Typ, Alg, Kid string }
	var c struct {
		Iss, Aud string
		Iat, Exp int64
	}
	hb, err1 := dec.DecodeString(parts[0])
	cb, err2 := dec.DecodeString(parts[1])
	sig, err3 := dec.DecodeString(parts[2])
	if err1 != nil || err2 != nil || err3 != nil || json.Unmarshal(hb, &h) != nil || json.Unmarshal(cb, &c) != nil {
		return fmt.Errorf("token not decodable")
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(f.pub, crypto.SHA256, sum[:], sig); err != nil {
		return fmt.Errorf("bad signature")
	}
	now := f.now().Unix()
	switch {
	case h.Typ != "JWT" || h.Alg != "RS256" || h.Kid != f.kid:
		return fmt.Errorf("bad header %+v", h)
	case c.Iss != "enablebanking.com" || c.Aud != "api.enablebanking.com":
		return fmt.Errorf("bad iss/aud")
	case c.Exp-c.Iat > 86400 || c.Exp <= now || c.Iat > now:
		return fmt.Errorf("bad lifetime iat=%d exp=%d now=%d", c.Iat, c.Exp, now)
	}
	return nil
}

func (f *fakeEB) serve(w http.ResponseWriter, r *http.Request) {
	if err := f.checkJWT(r); err != nil {
		f.t.Errorf("fake EB: %s %s: %v", r.Method, r.URL.Path, err)
		f.write(w, http.StatusUnauthorized, f.errorBody(401, "UNAUTHORIZED_ACCESS"))
		return
	}
	line := r.Method + " " + r.URL.Path
	f.mu.Lock()
	f.reqs = append(f.reqs, line+"?"+r.URL.RawQuery)
	for i, ft := range f.faults {
		if strings.Contains(line, ft.match) {
			f.faults = append(f.faults[:i], f.faults[i+1:]...)
			f.mu.Unlock()
			for k, v := range ft.header {
				w.Header()[k] = v
			}
			body := []byte(ft.raw)
			if ft.file != "" {
				body = fixture(f.t, ft.file)
			}
			f.write(w, ft.status, body)
			return
		}
	}
	f.mu.Unlock()

	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && path == "/application":
		f.write(w, 200, fixture(f.t, "application.json", "{{kid}}", f.kid, "{{environment}}", f.env))
	case r.Method == http.MethodGet && path == "/aspsps":
		if r.URL.Query().Get("country") == "" {
			f.write(w, 422, f.errorBody(422, "WRONG_REQUEST_PARAMETERS"))
			return
		}
		f.write(w, 200, fixture(f.t, "aspsps.json"))
	case r.Method == http.MethodPost && path == "/auth":
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["state"] == nil || body["redirect_url"] == nil || body["aspsp"] == nil || body["access"] == nil {
			f.write(w, 422, f.errorBody(422, "WRONG_REQUEST_PARAMETERS"))
			return
		}
		f.mu.Lock()
		f.authBody = body
		f.mu.Unlock()
		f.write(w, 200, fixture(f.t, "auth.json"))
	case r.Method == http.MethodPost && path == "/sessions":
		f.authorize(w, r)
	case strings.HasPrefix(path, "/sessions/"):
		f.session(w, r, strings.TrimPrefix(path, "/sessions/"))
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/accounts/"):
		f.account(w, r, strings.TrimPrefix(path, "/accounts/"))
	default:
		f.write(w, 404, f.errorBody(404, "WRONG_REQUEST_PARAMETERS"))
	}
}

func (f *fakeEB) visible() []string {
	var out []string
	for _, k := range []string{accountEUR, accountGBP} {
		if f.mode == banking.ModeDeveloper && !f.linked[k] {
			continue
		}
		out = append(out, k)
	}
	return out
}

func (f *fakeEB) details(key, uid string) map[string]any {
	var v map[string]any
	if err := json.Unmarshal(fixture(f.t, "details_"+key+".json", "{{uid}}", uid), &v); err != nil {
		f.t.Fatal(err)
	}
	return v
}

func (f *fakeEB) authorize(w http.ResponseWriter, r *http.Request) {
	var body struct{ Code string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Code != syntheticCode {
		f.write(w, 422, fixture(f.t, "error_wrong_code.json"))
		return
	}
	f.mu.Lock()
	f.n++
	id := fmt.Sprintf("00000000-0000-4000-9000-%012d", f.n)
	s := &fakeSession{status: "AUTHORIZED", validUntil: f.now().Add(90 * 24 * time.Hour), uids: map[string]string{}}
	f.sessions[id] = s
	var accounts []any
	for _, k := range f.visible() {
		uid := fmt.Sprintf("uid-%d-%s", f.n, k)
		s.uids[uid] = k
		accounts = append(accounts, f.details(k, uid))
	}
	f.mu.Unlock()
	if accounts == nil {
		accounts = []any{}
	}
	f.writeJSON(w, map[string]any{
		"session_id": id, "accounts": accounts,
		"aspsp": map[string]any{"name": "Example Bank", "country": "FI"}, "psu_type": "personal",
		"access": map[string]any{"valid_until": s.validUntil.Format(time.RFC3339Nano)},
	})
}

// status returns the effective session status (expiry by the clock).
func (f *fakeEB) status(s *fakeSession) string {
	if s.status == "AUTHORIZED" && !f.now().Before(s.validUntil) {
		return "EXPIRED"
	}
	return s.status
}

func (f *fakeEB) session(w http.ResponseWriter, r *http.Request, id string) {
	f.mu.Lock()
	s, ok := f.sessions[id]
	f.mu.Unlock()
	if !ok {
		f.write(w, 404, f.errorBody(404, "SESSION_DOES_NOT_EXIST"))
		return
	}
	switch r.Method {
	case http.MethodGet:
		f.mu.Lock()
		var uids []string
		var data []any
		for _, k := range f.visible() {
			for uid, key := range s.uids {
				if key == k {
					uids = append(uids, uid)
					h := map[string]string{accountEUR: hashEUR, accountGBP: hashGBP}[k]
					data = append(data, map[string]any{"uid": uid, "identification_hash": h, "identification_hashes": []string{h}})
				}
			}
		}
		st := f.status(s)
		f.mu.Unlock()
		if uids == nil {
			uids, data = []string{}, []any{}
		}
		f.writeJSON(w, map[string]any{
			"status": st, "accounts": uids, "accounts_data": data,
			"aspsp": map[string]any{"name": "Example Bank", "country": "FI"}, "psu_type": "personal",
			"psu_id_hash": "synthetic-psu-hash", "created": t0.Format(time.RFC3339),
			"access": map[string]any{"valid_until": s.validUntil.Format(time.RFC3339)},
		})
	case http.MethodDelete:
		f.mu.Lock()
		closed := s.status == "CLOSED"
		s.status = "CLOSED"
		f.mu.Unlock()
		if closed {
			f.write(w, 400, fixture(f.t, "error_closed_session.json"))
			return
		}
		f.write(w, 200, []byte(`{"message":"OK"}`))
	default:
		f.write(w, 405, f.errorBody(405, "WRONG_REQUEST_PARAMETERS"))
	}
}

func (f *fakeEB) account(w http.ResponseWriter, r *http.Request, rest string) {
	uid, what, _ := strings.Cut(rest, "/")
	f.mu.Lock()
	var sess *fakeSession
	var key string
	for _, s := range f.sessions {
		if k, ok := s.uids[uid]; ok {
			sess, key = s, k
		}
	}
	st := ""
	if sess != nil {
		st = f.status(sess)
	}
	f.mu.Unlock()
	switch {
	case sess == nil:
		f.write(w, 404, f.errorBody(404, "ACCOUNT_DOES_NOT_EXIST"))
		return
	case st == "EXPIRED":
		f.write(w, 401, fixture(f.t, "error_expired_session.json"))
		return
	case st == "REVOKED":
		f.write(w, 401, fixture(f.t, "error_revoked_session.json"))
		return
	case st != "AUTHORIZED":
		f.write(w, 400, fixture(f.t, "error_closed_session.json"))
		return
	}
	switch what {
	case "details":
		f.writeJSON(w, f.details(key, uid))
	case "balances":
		f.write(w, 200, fixture(f.t, "balances_"+key+".json"))
	case "transactions":
		f.transactions(w, r, key)
	default:
		f.write(w, 404, f.errorBody(404, "WRONG_REQUEST_PARAMETERS"))
	}
}

func txDate(t map[string]any) string {
	for _, k := range []string{"booking_date", "value_date", "transaction_date"} {
		if s, ok := t[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func (f *fakeEB) transactions(w http.ResponseWriter, r *http.Request, key string) {
	q := r.URL.Query()
	from, to := q.Get("date_from"), q.Get("date_to")
	if to != "" && from == "" {
		f.write(w, 422, f.errorBody(422, "DATE_TO_WITHOUT_DATE_FROM"))
		return
	}
	if from != "" {
		if _, err := calendar.ParseDate(from); err != nil {
			f.write(w, 422, f.errorBody(422, "WRONG_REQUEST_PARAMETERS"))
			return
		}
	}
	var all struct {
		Transactions []map[string]any `json:"transactions"`
	}
	if err := json.Unmarshal(fixture(f.t, "transactions_"+key+".json"), &all); err != nil {
		f.t.Fatal(err)
	}
	var sel []map[string]any
	for _, t := range all.Transactions {
		d := txDate(t)
		if q.Get("strategy") != "longest" && (from != "" && d < from || to != "" && d > to) {
			continue
		}
		sel = append(sel, t)
	}
	// The key binds the query; the first page is empty, as some banks do.
	bind := fmt.Sprintf("%x", sha256.Sum256([]byte(from+"|"+to+"|"+q.Get("strategy"))))[:8]
	offset := -1
	if ck := q.Get("continuation_key"); ck != "" {
		parts := strings.Split(ck, ":")
		n, err := strconv.Atoi(parts[len(parts)-1])
		if len(parts) != 3 || parts[1] != bind || err != nil {
			f.write(w, 400, f.errorBody(400, "WRONG_CONTINUATION_KEY"))
			return
		}
		offset = n
	}
	page := []map[string]any{}
	next := 0
	if offset >= 0 {
		end := min(offset+f.pageSize, len(sel))
		page = sel[offset:end]
		next = end
	}
	resp := map[string]any{"transactions": page, "continuation_key": nil}
	if offset < 0 || next < len(sel) {
		resp["continuation_key"] = "ck:" + bind + ":" + strconv.Itoa(next)
	}
	f.writeJSON(w, resp)
}
