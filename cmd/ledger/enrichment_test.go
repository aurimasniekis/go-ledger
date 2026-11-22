package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/enrichment"
)

func TestMerchantSourcesAPI(t *testing.T) {
	f := newAPIFixture(t)
	admin := bankCall{t: t, f: f, hdr: f.signIn(t, "alice")}
	reader := bankCall{t: t, f: f, hdr: f.signIn(t, "bob")}
	code, body, _ := admin.do("GET", "/api/v1/merchants/sources", "")
	if code != 200 {
		t.Fatalf("%d %v", code, body)
	}
	if code, _, _ = reader.do("GET", "/api/v1/merchants/sources", ""); code != 403 {
		t.Fatalf("a reader must not see the sources: %d", code)
	}
}

func TestBuildEnrichmentIsOfflineByDefault(t *testing.T) {
	svc, infos, key, err := buildEnrichment(config.MerchantRemote{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil || svc.Remote != nil || key != "" || len(infos) != 3 {
		t.Fatalf("%+v %v", infos, err)
	}
	for _, i := range infos {
		if i.Name == "remote" && (i.Enabled || len(i.SendsOffMachine) != 0) {
			t.Fatalf("remote must be off: %+v", i)
		}
		if i.Kind == "local" && !i.Enabled {
			t.Fatalf("local sources are always on: %+v", i)
		}
	}
	on, infos, _, err := buildEnrichment(config.MerchantRemote{Enabled: true, URL: "https://lookup.example.com/v1", Country: "LT", Attribution: "Data by Example", Retention: 48 * time.Hour, Timeout: time.Second}, nil, nil)
	if err != nil || on.Remote == nil {
		t.Fatalf("%v", err)
	}
	r := infos[2]
	if !r.Enabled || len(r.SendsOffMachine) != 2 || r.Attribution == nil || *r.Attribution != "Data by Example" || r.RetentionHours == nil || *r.RetentionHours != 48 || r.StoresLogoRef {
		t.Fatalf("%+v", r)
	}
	if _, _, _, err := buildEnrichment(config.MerchantRemote{Enabled: true, URL: "https://x.example.com", APIKeyFile: "/nonexistent/key"}, nil, nil); err == nil {
		t.Fatal("an unreadable key file must be refused at startup")
	}
}

type failingStore struct{ enrichment.Store }

func (failingStore) Confirmations(context.Context) ([]enrichment.Confirmation, error) {
	return nil, errors.New("database is locked")
}
func (failingStore) Pending(context.Context, int, time.Time, bool) ([]enrichment.Candidate, error) {
	panic("boom")
}

// A failing or even panicking enrichment pass must not escape into the import
// that triggered it.
func TestEnrichAfterRefreshNeverFailsTheImport(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	enrichAfterRefresh(context.Background(), nil, log)
	enrichAfterRefresh(context.Background(), &enrichment.Service{Store: failingStore{}, Learned: enrichment.NewLearned()}, log)
	enrichAfterRefresh(context.Background(), &enrichment.Service{Store: failingStore{}}, log)
}

type slowStore struct {
	enrichment.Store
	release chan struct{}
	started chan struct{}
}

func (s slowStore) Confirmations(context.Context) ([]enrichment.Confirmation, error) { return nil, nil }
func (s slowStore) Pending(ctx context.Context, _ int, _ time.Time, _ bool) ([]enrichment.Candidate, error) {
	s.started <- struct{}{}
	select {
	case <-s.release:
	case <-ctx.Done():
	}
	return nil, nil
}

// The pass after a refresh runs in the background: the refresh is not held up,
// a second request while one runs is skipped, and the pass can be waited for.
func TestEnrichInBackgroundNeverBlocksTheRefresh(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := slowStore{release: make(chan struct{}), started: make(chan struct{}, 2)}
	svc := &enrichment.Service{Store: st}
	var running atomic.Bool
	var wg sync.WaitGroup
	done := make(chan struct{})
	go func() {
		enrichInBackground(context.Background(), svc, log, &running, &wg)
		enrichInBackground(context.Background(), svc, log, &running, &wg) // overlapping: skipped
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("enrichInBackground blocked on a slow pass")
	}
	<-st.started
	select {
	case <-st.started:
		t.Fatal("a second pass started while one was running")
	case <-time.After(50 * time.Millisecond):
	}
	close(st.release)
	wg.Wait()
	if running.Load() {
		t.Fatal("running flag not cleared")
	}
	enrichInBackground(context.Background(), nil, log, &running, &wg)
}
