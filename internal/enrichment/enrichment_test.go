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
	if q := r2.Query(enrichment.Subject{Description: "Only a description UAB"}); q.Merchant != "Only a description UAB" || q.Country != "" {
		t.Fatalf("query %+v", q)
	}
}

func TestRemoteLogoReferenceFollowsTheLicence(t *testing.T) {
	rs := newRemoteServer(t, reply(`{"status":"identified","name":"Shop","logo_url":"https://img.example.com/x.png","confidence_permille":900}`))
	for _, keep := range []bool{false, true} {
		r, _ := enrichment.NewRemote(enrichment.RemoteOptions{URL: rs.URL, Licence: enrichment.Licence{StoreLogoRef: keep}})
		a, err := r.Lookup(context.Background(), enrichment.Subject{Counterparty: "Example Shop UAB"})
		if err != nil || (a.LogoRef != "") != keep {
			t.Fatalf("keep=%v: %+v %v", keep, a, err)
		}
	}
	for _, bad := range []string{"http://img.example.com/x.png", "javascript:alert(1)", "https://u:p@img.example.com/x"} {
		rs2 := newRemoteServer(t, reply(`{"status":"identified","name":"Shop","logo_url":`+jsonString(bad)+`,"confidence_permille":900}`))
		r, _ := enrichment.NewRemote(enrichment.RemoteOptions{URL: rs2.URL, Licence: enrichment.Licence{StoreLogoRef: true}})
		if a, _ := r.Lookup(context.Background(), enrichment.Subject{Counterparty: "Example Shop UAB"}); a.LogoRef != "" {
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
		_, err := r.Lookup(context.Background(), enrichment.Subject{Counterparty: "PRIVATE SHOP NAME UAB"})
		if err == nil || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "PRIVATE") || strings.Contains(err.Error(), rs.URL) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Unreachable and timed out.
	r, _ := enrichment.NewRemote(enrichment.RemoteOptions{URL: "http://127.0.0.1:1/x"})
	if _, err := r.Lookup(context.Background(), enrichment.Subject{Counterparty: "X Shop UAB"}); err == nil {
		t.Error("unreachable must fail")
	}
	slow := newRemoteServer(t, func(w http.ResponseWriter) { time.Sleep(300 * time.Millisecond) })
	r, _ = enrichment.NewRemote(enrichment.RemoteOptions{URL: slow.URL, Timeout: 50 * time.Millisecond})
	if _, err := r.Lookup(context.Background(), enrichment.Subject{Counterparty: "X Shop UAB"}); err == nil {
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
	// recheckNone records the flag of each Pending call.
	recheckNone []bool
}

func (f *fakeStore) Pending(_ context.Context, _ int, _ time.Time, recheckNone bool) ([]enrichment.Candidate, error) {
	f.recheckNone = append(f.recheckNone, recheckNone)
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
		{ID: "b", Subject: enrichment.Subject{Counterparty: "UNKNOWN SHOP UAB"}},
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
		{ID: "b", Subject: enrichment.Subject{Counterparty: "UNKNOWN SHOP UAB"}},
		{ID: "c", Subject: enrichment.Subject{Counterparty: "OTHER SHOP UAB"}},
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

// Finding 1: the person's correction or rejection beats a curated identity for
// the same key.
func TestPersonsDecisionBeatsCuratedIdentity(t *testing.T) {
	ctx := context.Background()
	cases := map[string]struct {
		confirmed []enrichment.Confirmation
		check     func(t *testing.T, a merchants.Answer)
	}{
		"corrected": {
			confirmed: []enrichment.Confirmation{{Subject: enrichment.Subject{Description: "BOLT HARDWARE STORE 12"}, Name: "Bolt Hardware Store"}},
			check: func(t *testing.T, a merchants.Answer) {
				if a.Status != merchants.StatusIdentified || a.Name != "Bolt Hardware Store" || a.Source != "local_mapping" || a.Confidence >= merchants.DefaultThreshold {
					t.Fatalf("%+v", a)
				}
			},
		},
		"two names": {
			confirmed: []enrichment.Confirmation{
				{Subject: enrichment.Subject{Description: "BOLT HARDWARE STORE 12"}, Name: "Bolt Hardware Store"},
				{Subject: enrichment.Subject{Description: "BOLT HARDWARE STORE 7"}, Name: "Bolt Hardware"},
			},
			check: func(t *testing.T, a merchants.Answer) {
				if a.Status != merchants.StatusAmbiguous || len(a.Candidates) != 2 {
					t.Fatalf("%+v", a)
				}
			},
		},
		"rejected": {
			confirmed: []enrichment.Confirmation{{Subject: enrichment.Subject{Description: "BOLT HARDWARE STORE 12"}, Rejected: true}},
			check: func(t *testing.T, a merchants.Answer) {
				if a.Status != merchants.StatusNone {
					t.Fatalf("%+v", a)
				}
			},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			st := &fakeStore{confirmed: c.confirmed, pending: []enrichment.Candidate{{ID: "x", Subject: enrichment.Subject{Description: "BOLT HARDWARE STORE 99"}}}}
			if _, err := newService(st, nil).Run(ctx); err != nil {
				t.Fatal(err)
			}
			c.check(t, st.recorded["x"])
		})
	}
	// The same merchant named by the person and by the curated list keeps the
	// curated (more reliable) answer.
	st := &fakeStore{confirmed: []enrichment.Confirmation{{Subject: enrichment.Subject{Description: "NETFLIX AMS 1"}, Name: "Netflix"}}, pending: []enrichment.Candidate{{ID: "x", Subject: enrichment.Subject{Description: "NETFLIX AMS 5"}}}}
	if _, err := newService(st, nil).Run(ctx); err != nil || st.recorded["x"].Source != "curated" {
		t.Fatalf("%+v %v", st.recorded["x"], err)
	}
}

// A person's decision for a key also keeps the remote source from being asked.
func TestRemoteIsNotAskedAboutAKeyThePersonDecided(t *testing.T) {
	rs := newRemoteServer(t, reply(`{"status":"identified","name":"Elsewhere","confidence_permille":900}`))
	r, _ := enrichment.NewRemote(enrichment.RemoteOptions{URL: rs.URL})
	st := &fakeStore{confirmed: []enrichment.Confirmation{{Subject: enrichment.Subject{Description: "CORNER KIOSK 8"}, Rejected: true}},
		pending: []enrichment.Candidate{{ID: "x", Subject: enrichment.Subject{Description: "CORNER KIOSK 3"}}}}
	if _, err := newService(st, r).Run(context.Background()); err != nil || rs.calls.Load() != 0 || st.recorded["x"].Status != merchants.StatusNone {
		t.Fatalf("%+v %v calls=%d", st.recorded["x"], err, rs.calls.Load())
	}
}

// Finding 2: none answers are only rechecked when something could change them.
func TestNoneRechecksNeedARemoteSourceOrChangedConfirmations(t *testing.T) {
	ctx := context.Background()
	st := &fakeStore{}
	svc := newService(st, nil)
	for i := 0; i < 3; i++ {
		if _, err := svc.Run(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := st.recheckNone; len(got) != 3 || !got[0] || got[1] || got[2] {
		t.Fatalf("recheck flags without a remote source: %v (first pass true, then false)", got)
	}
	st.confirmed = []enrichment.Confirmation{{Subject: enrichment.Subject{Description: "CORNER KIOSK 8"}, Name: "Corner Kiosk"}}
	if _, err := svc.Run(ctx); err != nil || !st.recheckNone[3] {
		t.Fatalf("a changed confirmation must recheck: %v %v", st.recheckNone, err)
	}
	rs := newRemoteServer(t, reply(`{"status":"none"}`))
	r, _ := enrichment.NewRemote(enrichment.RemoteOptions{URL: rs.URL})
	st2 := &fakeStore{}
	svc2 := newService(st2, r)
	_, _ = svc2.Run(ctx)
	_, _ = svc2.Run(ctx)
	if !st2.recheckNone[0] || !st2.recheckNone[1] {
		t.Fatalf("with a remote source none answers are rechecked: %v", st2.recheckNone)
	}
}

// Finding 3: what may leave the machine.
func TestMerchantStringSendsNoIdentifiersAmountsDatesOrTransfers(t *testing.T) {
	cases := []struct {
		name string
		in   enrichment.Subject
		want string
	}{
		{"merchant counterparty", enrichment.Subject{Counterparty: "Example Shop UAB"}, "Example Shop UAB"},
		{"company form with digits dropped", enrichment.Subject{Counterparty: "Example Shop 1523 UAB"}, "Example Shop UAB"},
		{"description with form", enrichment.Subject{Description: "CARD PURCHASE Corner Kiosk Ltd LT121000011101001000 12.50 EUR 2026-10-01 ref 99A"}, ""},
		{"form inside a description", enrichment.Subject{Description: "Payment to Jonas Jonaitis as agreed"}, ""},
		{"form inside a longer description", enrichment.Subject{Description: "Invoice paid to Corner Kiosk Ltd for lunch"}, ""},
		{"lowercase ambiguous form", enrichment.Subject{Counterparty: "Jonas Jonaitis as"}, ""},
		{"lowercase co", enrichment.Subject{Counterparty: "Jonas Jonaitis co"}, ""},
		{"form first", enrichment.Subject{Counterparty: "UAB Example Shop"}, "UAB Example Shop"},
		{"capital AS last", enrichment.Subject{Counterparty: "Example AS"}, "Example AS"},
		{"lowercase unambiguous form", enrichment.Subject{Counterparty: "Example Shop uab"}, "Example Shop uab"},
		{"split iban", enrichment.Subject{Description: "Kiosk GmbH IBAN LT12 1000 0111 0100 1000 account 4730"}, "Kiosk GmbH"},
		{"multi-word curated pattern", enrichment.Subject{Description: "APPLE.COM/BILL 12345"}, "APPLE COM BILL"},
		{"single curated word", enrichment.Subject{Counterparty: "LIDL 1523 VILNIUS"}, ""},
		{"business word only", enrichment.Subject{Description: "Corner Kiosk Bakery"}, ""},
		{"form alone", enrichment.Subject{Counterparty: "UAB"}, ""},
		{"only numbers", enrichment.Subject{Description: "12345678 2026-10-01"}, ""},
		{"sepa transfer", enrichment.Subject{Counterparty: "Example Shop UAB", Description: "SEPA credit transfer"}, ""},
		{"transfer word", enrichment.Subject{Description: "Transfer to savings UAB"}, ""},
		{"person counterparty", enrichment.Subject{Counterparty: "Jonas Jonaitis", Description: "rent"}, ""},
		{"person with initial", enrichment.Subject{Counterparty: "Anna K. Smith"}, ""},
		{"surname first", enrichment.Subject{Counterparty: "SMITH, JOHN"}, ""},
		{"person and number", enrichment.Subject{Counterparty: "John Smith 12345"}, ""},
		{"person and iban", enrichment.Subject{Counterparty: "John Smith LT121000011101001000"}, ""},
		{"person and reason", enrichment.Subject{Counterparty: "John Smith / rent"}, ""},
		{"single name", enrichment.Subject{Counterparty: "JONAS"}, ""},
		{"initial dot surname", enrichment.Subject{Counterparty: "J.SMITH"}, ""},
		{"particles", enrichment.Subject{Counterparty: "Maria del Carmen Lopez"}, ""},
		{"curated word as surname", enrichment.Subject{Counterparty: "Mark Bolt"}, ""},
		{"empty", enrichment.Subject{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.in.MerchantString(); got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
	// Nothing is sent for an empty string: no request leaves.
	rs := newRemoteServer(t, reply(`{"status":"none"}`))
	r, _ := enrichment.NewRemote(enrichment.RemoteOptions{URL: rs.URL})
	a, err := r.Lookup(context.Background(), enrichment.Subject{Counterparty: "Jonas Jonaitis"})
	if err != nil || a.Status != merchants.StatusNone || rs.calls.Load() != 0 {
		t.Fatalf("%+v %v calls=%d", a, err, rs.calls.Load())
	}
}

// Finding 4: remote answers are purged by the configured retention even when
// the remote source is off.
func TestRemoteRetentionAppliesWithoutARemoteSource(t *testing.T) {
	st := &fakeStore{}
	svc := newService(st, nil)
	if _, err := svc.Run(context.Background()); err != nil || st.deleted != "" {
		t.Fatalf("no retention, no purge: %q %v", st.deleted, err)
	}
	svc.RemoteRetention = 48 * time.Hour
	rep, err := svc.Run(context.Background())
	if err != nil || st.deleted != "remote" || rep.Expired != 3 {
		t.Fatalf("%+v %v deleted=%q", rep, err, st.deleted)
	}
}

// Finding 5: a generic word inside longer text is never reliable on its own.
func TestCuratedGenericWordsAreNotReliableInsideLongerText(t *testing.T) {
	c := enrichment.NewCurated(enrichment.DefaultCurated())
	for _, d := range []string{"BOLT HARDWARE STORE 12", "APPLE ORCHARD CAFE", "BOOKING OFFICE LUNCH", "UBER EATS PIZZA", "GOOGLE STREET PARKING"} {
		a, err := c.Lookup(context.Background(), enrichment.Subject{Description: d})
		if err != nil || a.Status != merchants.StatusIdentified || a.Confidence >= merchants.DefaultThreshold {
			t.Fatalf("%s: %+v %v", d, a, err)
		}
	}
	// Exact, multi-word and distinctive matches stay reliable.
	for _, d := range []string{"BOLT", "Apple.com/bill", "ITUNES", "BOOKING.COM 123", "SPOTIFY STOCKHOLM"} {
		a, _ := c.Lookup(context.Background(), enrichment.Subject{Description: d})
		if a.Status != merchants.StatusIdentified || a.Confidence < merchants.DefaultThreshold {
			t.Fatalf("%s: %+v", d, a)
		}
	}
}
