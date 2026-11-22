package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/store"
)

func TestBuildDecisions(t *testing.T) {
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "ledger.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	aw := store.NewAuditLog(db)
	var off config.Decisions
	off.Tasks.Categorisation.Timeout, off.Tasks.Categorisation.MonthlyRequests, off.Tasks.Categorisation.Retries = 30*time.Second, 500, 2
	if svc, err := buildDecisions(off, db, aw, time.Now); err != nil || svc != nil {
		t.Fatalf("all off: %v %v", svc, err)
	}
	on := off
	on.Tasks.Categorisation.Enabled = true
	on.Tasks.Categorisation.Provider = "anthropic"
	on.Tasks.Categorisation.Model = "m"
	on.Providers.Anthropic.APIKey = "SENTINEL-KEY"
	_, err = buildDecisions(on, db, aw, time.Now)
	if err == nil || !strings.Contains(err.Error(), "not available") || !strings.Contains(err.Error(), "no decision-provider adapters") {
		t.Fatalf("enabled without an adapter: %v", err)
	}
	if strings.Contains(err.Error(), "SENTINEL-KEY") {
		t.Fatal("the key is in the error")
	}
}
