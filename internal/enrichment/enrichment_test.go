package enrichment_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/merchants"
	"github.com/aurimasniekis/go-ledger/internal/enrichment"
)

func TestKeyDropsNumbersAndNoise(t *testing.T) {
	for in, want := range map[string]string{
		"LIDL 1523 VILNIUS":                  "lidl vilnius",
		"Lidl 0042 Vilnius":                  "lidl vilnius",
		"Card payment POS 12.11 NETFLIX.COM": "netflix com",
		"1234 5678":                          "",
		"":                                   "",
		"Café Müller 17":                     "café müller",
		strings.Repeat("abcdefgh ", 20):      strings.TrimSpace(strings.Repeat("abcdefgh ", 9)),
	} {
		if got := enrichment.Key(in); got != want {
			t.Errorf("Key(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCuratedIdentifiesAmbiguatesAndKnowsNothing(t *testing.T) {
	c := enrichment.NewCurated(enrichment.DefaultCurated())
	ctx := context.Background()
	a, err := c.Lookup(ctx, enrichment.Subject{Counterparty: "NETFLIX"})
	if err != nil || a.Status != merchants.StatusIdentified || a.Name != "Netflix" || a.Confidence != enrichment.ConfidenceCuratedExact || a.Source != "curated" {
		t.Fatalf("exact: %+v %v", a, err)
	}
	a, _ = c.Lookup(ctx, enrichment.Subject{Description: "CARD PAYMENT SPOTIFY P1A2B3 STOCKHOLM"})
	if a.Status != merchants.StatusIdentified || a.Name != "Spotify" || a.Confidence != enrichment.ConfidenceCuratedContains {
		t.Fatalf("contained: %+v", a)
	}
	// Whole words only: "amazonia" is not Amazon.
	if a, _ = c.Lookup(ctx, enrichment.Subject{Description: "AMAZONIA TOURS"}); a.Status != merchants.StatusNone {
		t.Fatalf("word boundary: %+v", a)
	}
	// Two merchants in one text: never a guess.
	a, _ = c.Lookup(ctx, enrichment.Subject{Description: "UBER NETFLIX GIFT"})
	if a.Status != merchants.StatusAmbiguous || len(a.Candidates) != 2 || len(a.Validate()) != 0 {
		t.Fatalf("ambiguous: %+v", a)
	}
	for _, in := range []enrichment.Subject{{Description: "SALARY OCTOBER"}, {}} {
		if a, _ = c.Lookup(ctx, in); a.Status != merchants.StatusNone || len(a.Validate()) != 0 {
			t.Fatalf("none: %+v", a)
		}
	}
}

func TestLearnedMappingIsASuggestionNeverShown(t *testing.T) {
	l := enrichment.NewLearned()
	l.Load([]enrichment.Confirmation{
		{Subject: enrichment.Subject{Description: "CORNER KIOSK 11 VILNIUS"}, Name: "Corner Kiosk"},
		{Subject: enrichment.Subject{Description: "SHARED NAME 1"}, Name: "Shop A"},
		{Subject: enrichment.Subject{Description: "SHARED NAME 2"}, Name: "Shop B"},
	})
	a, _ := l.Lookup(context.Background(), enrichment.Subject{Description: "CORNER KIOSK 99 VILNIUS"})
	if a.Status != merchants.StatusIdentified || a.Name != "Corner Kiosk" || a.Source != "local_mapping" || a.Confidence != enrichment.ConfidenceSuggestion {
		t.Fatalf("suggestion: %+v", a)
	}
	if enrichment.ConfidenceSuggestion >= merchants.DefaultThreshold {
		t.Fatal("a reused mapping must stay below the reliability threshold")
	}
	m := merchants.Resolve(&merchants.Enrichment{Status: a.Status, Name: a.Name, Source: a.Source, Confidence: a.Confidence}, nil, merchants.DefaultThreshold)
	if m.State != merchants.StateUncertain || !m.NeedsReview || !m.Fallback() {
		t.Fatalf("a suggestion must not be shown unconfirmed: %+v", m)
	}
	a, _ = l.Lookup(context.Background(), enrichment.Subject{Description: "SHARED NAME 7"})
	if a.Status != merchants.StatusAmbiguous || len(a.Candidates) != 2 {
		t.Fatalf("conflicting confirmations: %+v", a)
	}
	if a, _ = l.Lookup(context.Background(), enrichment.Subject{Description: "SOMETHING ELSE"}); a.Status != merchants.StatusNone {
		t.Fatalf("no mapping: %+v", a)
	}
}

type remoteServer struct {
	*httptest.Server
	calls atomic.Int32
	body  atomic.Value
	auth  atomic.Value
}

func newRemoteServer(t *testing.T, h func(w http.ResponseWriter)) *remoteServer {
	t.Helper()
	rs := &remoteServer{}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.calls.Add(1)
		b, _ := io.ReadAll(r.Body)
		rs.body.Store(string(b))
		rs.auth.Store(r.Header.Get("Authorization"))
		h(w)
	}))
	t.Cleanup(rs.Close)
	return rs
}

func reply(s string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) { _, _ = io.WriteString(w, s) }
}

func TestRemoteSendsOnlyTheAllowlistedFields(t *testing.T) {
	rs := newRemoteServer(t, reply(`{"status":"identified","name":"  Example   Shop ","logo_url":"https://img.example.com/x.png","confidence_permille":900}`))
	r, err := enrichment.NewRemote(enrichment.RemoteOptions{URL: rs.URL, APIKey: "k-secret", Country: "LT", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	sub := enrichment.Subject{Counterparty: "Example Shop\n UAB", Description: "IBAN LT121000011101001000 amount 12.50 ref 99"}
	a, err := r.Lookup(context.Background(), sub)
	if err != nil || a.Status != merchants.StatusIdentified || a.Name != "Example Shop" || a.Confidence != 900 || a.Source != "remote" {
		t.Fatalf("answer: %+v %v", a, err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(rs.body.Load().(string)), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["merchant"] != "Example Shop UAB" || got["country"] != "LT" {
		t.Fatalf("request body %v", got)
	}
	if strings.Contains(rs.body.Load().(string), "LT1210") || rs.auth.Load() != "Bearer k-secret" {
		t.Fatalf("leak or auth: %v %v", rs.body.Load(), rs.auth.Load())
	}
	// No country configured: none is sent. With no counterparty the
	// description is the merchant string.
	r2, _ := enrichment.NewRemote(enrichment.RemoteOptions{URL: rs.URL})
	if q := r2.Query(enrichment.Subject{Description: "Only a description"}); q.Merchant != "Only a description" || q.Country != "" {
		t.Fatalf("query %+v", q)
	}
}

func TestRemoteLogoReferenceFollowsTheLicence(t *testing.T) {
	rs := newRemoteServer(t, reply(`{"status":"identified","name":"Shop","logo_url":"https://img.example.com/x.png","confidence_permille":900}`))
	for _, keep := range []bool{false, true} {
		r, _ := enrichment.NewRemote(enrichment.RemoteOptions{URL: rs.URL, Licence: enrichment.Licence{StoreLogoRef: keep}})
		a, err := r.Lookup(context.Background(), enrichment.Subject{Counterparty: "Shop"})
		if err != nil || (a.LogoRef != "") != keep {
			t.Fatalf("keep=%v: %+v %v", keep, a, err)
		}
	}
	for _, bad := range []string{"http://img.example.com/x.png", "javascript:alert(1)", "https://u:p@img.example.com/x"} {
		rs2 := newRemoteServer(t, reply(`{"status":"identified","name":"Shop","logo_url":`+jsonString(bad)+`,"confidence_permille":900}`))
		r, _ := enrichment.NewRemote(enrichment.RemoteOptions{URL: rs2.URL, Licence: enrichment.Licence{StoreLogoRef: true}})
		if a, _ := r.Lookup(context.Background(), enrichment.Subject{Counterparty: "Shop"}); a.LogoRef != "" {
			t.Errorf("kept unsafe logo %q", bad)
		}
	}
}

func jsonString(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestRemoteFailuresAreErrorsWithoutBankText(t *testing.T) {
	for name, h := range map[string]func(http.ResponseWriter){
		"server error":   func(w http.ResponseWriter) { http.Error(w, "SECRET BODY", 500) },
		"not json":       reply("<html>SECRET BODY"),
		"bad status":     reply(`{"status":"maybe"}`),
		"no confidence":  reply(`{"status":"identified","name":"X"}`),
		"invalid answer": reply(`{"status":"identified","name":"X","confidence_permille":5000}`),
		"too large":      func(w http.ResponseWriter) { _, _ = io.WriteString(w, strings.Repeat("a", 70<<10)) },
		"redirect": func(w http.ResponseWriter) {
			w.Header().Set("Location", "https://elsewhere.example/")
			w.WriteHeader(302)
		},
	} {
		rs := newRemoteServer(t, h)
		r, _ := enrichment.NewRemote(enrichment.RemoteOptions{URL: rs.URL, APIKey: "k"})
		_, err := r.Lookup(context.Background(), enrichment.Subject{Counterparty: "PRIVATE SHOP NAME"})
		if err == nil || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "PRIVATE") || strings.Contains(err.Error(), rs.URL) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Unreachable and timed out.
	r, _ := enrichment.NewRemote(enrichment.RemoteOptions{URL: "http://127.0.0.1:1/x"})
	if _, err := r.Lookup(context.Background(), enrichment.Subject{Counterparty: "X"}); err == nil {
		t.Error("unreachable must fail")
	}
	slow := newRemoteServer(t, func(w http.ResponseWriter) { time.Sleep(300 * time.Millisecond) })
	r, _ = enrichment.NewRemote(enrichment.RemoteOptions{URL: slow.URL, Timeout: 50 * time.Millisecond})
	if _, err := r.Lookup(context.Background(), enrichment.Subject{Counterparty: "X"}); err == nil {
		t.Error("timeout must fail")
	}
	if _, err := enrichment.NewRemote(enrichment.RemoteOptions{URL: "https://u:p@example.com/"}); err == nil {
		t.Error("credentials in the URL must be refused")
	}
}

type fakeStore struct {
	pending   []enrichment.Candidate
	confirmed []enrichment.Confirmation
	recorded  map[banking.TransactionID]merchants.Answer
	recordErr error
	deleted   string
}

func (f *fakeStore) Pending(context.Context, int, time.Time) ([]enrichment.Candidate, error) {
	return f.pending, nil
}
func (f *fakeStore) RecordAnswer(_ context.Context, id banking.TransactionID, a merchants.Answer) (merchants.Merchant, error) {
	if f.recordErr != nil {
		return merchants.Merchant{}, f.recordErr
	}
	if f.recorded == nil {
		f.recorded = map[banking.TransactionID]merchants.Answer{}
	}
	f.recorded[id] = a
	return merchants.Merchant{}, nil
}
func (f *fakeStore) Confirmations(context.Context) ([]enrichment.Confirmation, error) {
	return f.confirmed, nil
}
func (f *fakeStore) DeleteAnswersBefore(_ context.Context, source string, _ time.Time) (int, error) {
	f.deleted = source
	return 3, nil
}

func newService(st enrichment.Store, r *enrichment.Remote) *enrichment.Service {
	return &enrichment.Service{Store: st, Curated: enrichment.NewCurated(enrichment.DefaultCurated()), Learned: enrichment.NewLearned(), Remote: r}
}

func TestServiceIsOfflineByDefaultAndLocalFirst(t *testing.T) {
	st := &fakeStore{pending: []enrichment.Candidate{
		{ID: "a", Subject: enrichment.Subject{Counterparty: "NETFLIX"}},
		{ID: "b", Subject: enrichment.Subject{Description: "SALARY"}},
		{ID: "c", Subject: enrichment.Subject{Description: "CORNER KIOSK 3"}},
	}, confirmed: []enrichment.Confirmation{{Subject: enrichment.Subject{Description: "CORNER KIOSK 8"}, Name: "Corner Kiosk"}}}
	rep, err := newService(st, nil).Run(context.Background())
	if err != nil || rep.Looked != 3 || rep.Identified != 2 || rep.None != 1 || rep.Failed != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	if a := st.recorded["a"]; a.Name != "Netflix" || a.Source != "curated" {
		t.Fatalf("a: %+v", a)
	}
	if a := st.recorded["c"]; a.Name != "Corner Kiosk" || a.Source != "local_mapping" || a.Confidence >= merchants.DefaultThreshold {
		t.Fatalf("c: %+v", a)
	}
	if a := st.recorded["b"]; a.Status != merchants.StatusNone {
		t.Fatalf("b: %+v", a)
	}
}

func TestRemoteIsOnlyAskedAboutWhatLocalSourcesCannotIdentify(t *testing.T) {
	rs := newRemoteServer(t, reply(`{"status":"identified","name":"Far Away Shop","confidence_permille":900}`))
	r, _ := enrichment.NewRemote(enrichment.RemoteOptions{URL: rs.URL, Licence: enrichment.Licence{Retention: 720 * time.Hour}})
	st := &fakeStore{pending: []enrichment.Candidate{
		{ID: "a", Subject: enrichment.Subject{Counterparty: "NETFLIX"}},
		{ID: "b", Subject: enrichment.Subject{Counterparty: "UNKNOWN SHOP"}},
	}}
	rep, err := newService(st, r).Run(context.Background())
	if err != nil || rep.Identified != 2 || rep.Expired != 3 || rs.calls.Load() != 1 || st.deleted != "remote" {
		t.Fatalf("%+v %v calls=%d deleted=%q", rep, err, rs.calls.Load(), st.deleted)
	}
	if st.recorded["b"].Source != "remote" || st.recorded["a"].Source != "curated" {
		t.Fatalf("%+v", st.recorded)
	}
}

func TestRemoteFailureDoesNotBlockAndIsRecordedAsUnavailable(t *testing.T) {
	rs := newRemoteServer(t, func(w http.ResponseWriter) { http.Error(w, "down", http.StatusServiceUnavailable) })
	r, _ := enrichment.NewRemote(enrichment.RemoteOptions{URL: rs.URL})
	st := &fakeStore{pending: []enrichment.Candidate{
		{ID: "a", Subject: enrichment.Subject{Counterparty: "NETFLIX"}},
		{ID: "b", Subject: enrichment.Subject{Counterparty: "UNKNOWN SHOP"}},
		{ID: "c", Subject: enrichment.Subject{Counterparty: "OTHER SHOP"}},
	}}
	rep, err := newService(st, r).Run(context.Background())
	if err != nil || rep.Looked != 3 || rep.Identified != 1 || rep.Failed != 2 {
		t.Fatalf("%+v %v", rep, err)
	}
	if st.recorded["b"].Status != merchants.StatusUnavailable || st.recorded["a"].Name != "Netflix" {
		t.Fatalf("%+v", st.recorded)
	}
}

func TestServiceStoreFailuresAreCountedNotFatal(t *testing.T) {
	st := &fakeStore{pending: []enrichment.Candidate{{ID: "a", Subject: enrichment.Subject{Counterparty: "NETFLIX"}}}, recordErr: errors.New("disk full")}
	rep, err := newService(st, nil).Run(context.Background())
	if err != nil || rep.Failed != 1 || rep.Looked != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
}

func TestCancelledPassStops(t *testing.T) {
	st := &fakeStore{pending: []enrichment.Candidate{{ID: "a", Subject: enrichment.Subject{Counterparty: "NETFLIX"}}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if rep, _ := newService(st, nil).Run(ctx); rep.Looked != 0 || len(st.recorded) != 0 {
		t.Fatalf("%+v", rep)
	}
}
