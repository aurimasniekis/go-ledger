package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/enrichment"
	"github.com/aurimasniekis/go-ledger/internal/httpapi"
	merchantstore "github.com/aurimasniekis/go-ledger/internal/store/merchants"
)

// enrichmentBudget bounds one enrichment pass after a bank refresh, so a slow
// remote source can never hold up the refresh that triggered it (task E2).
const enrichmentBudget = time.Minute

// buildEnrichment builds the merchant enrichment service (ADR-0054) and the
// description of its sources for the API. The local sources are always on; the
// remote source only when the operator enabled it (decision D15: off by default,
// none approved). It returns the API key it read so the caller can register it
// with the redactor.
func buildEnrichment(cfg config.MerchantRemote, st *merchantstore.Store, log *slog.Logger) (*enrichment.Service, []httpapi.MerchantSourceInfo, string, error) {
	svc := &enrichment.Service{Store: st, Curated: enrichment.NewCurated(enrichment.DefaultCurated()), Learned: enrichment.NewLearned(), Log: log}
	infos := []httpapi.MerchantSourceInfo{
		{Name: enrichment.SourceCurated, Kind: "local", Enabled: true, Description: "A small built-in list of well-known merchants matched against the counterparty or description, offline."},
		{Name: enrichment.SourceLearned, Kind: "local", Enabled: true, Description: "Your confirmed and corrected merchant names, reused as suggestions for similar transactions; never confirmed automatically."},
	}
	remote := httpapi.MerchantSourceInfo{Name: enrichment.SourceRemote, Kind: "remote", Enabled: cfg.Enabled, Description: "An optional remote merchant-identification service. Off unless the operator enables it."}
	if !cfg.Enabled {
		return svc, append(infos, remote), "", nil
	}
	var key string
	if cfg.APIKeyFile != "" {
		b, err := os.ReadFile(cfg.APIKeyFile)
		if err != nil {
			return nil, nil, "", errors.New("merchant enrichment: the API key file could not be read")
		}
		key = strings.TrimRight(string(b), "\r\n")
	}
	lic := enrichment.Licence{Attribution: cfg.Attribution, StoreLogoRef: cfg.StoreLogoRef, Retention: cfg.Retention}
	r, err := enrichment.NewRemote(enrichment.RemoteOptions{URL: cfg.URL, APIKey: key, Country: cfg.Country, Timeout: cfg.Timeout, Licence: lic})
	if err != nil {
		return nil, nil, "", err
	}
	svc.Remote = r
	remote.SendsOffMachine = []string{"merchant string (counterparty name, else the start of the description)"}
	if cfg.Country != "" {
		remote.SendsOffMachine = append(remote.SendsOffMachine, "country code")
	}
	remote.StoresLogoRef = cfg.StoreLogoRef
	if cfg.Attribution != "" {
		a := cfg.Attribution
		remote.Attribution = &a
	}
	if cfg.Retention > 0 {
		h := int(cfg.Retention / time.Hour)
		remote.RetentionHours = &h
	}
	return svc, append(infos, remote), key, nil
}

// enrichAfterRefresh runs one bounded enrichment pass. It never returns an
// error and never panics out: a failing source is logged and the import it
// follows is unaffected.
func enrichAfterRefresh(ctx context.Context, svc *enrichment.Service, log *slog.Logger) {
	if svc == nil {
		return
	}
	defer func() {
		if p := recover(); p != nil {
			log.Warn("merchant enrichment panicked; ignored")
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, enrichmentBudget)
	defer cancel()
	rep, err := svc.Run(ctx)
	if err != nil {
		if ctx.Err() == nil {
			log.Warn("merchant enrichment after a bank refresh failed", "error", err.Error())
		}
		return
	}
	if rep.Looked > 0 || rep.Expired > 0 {
		log.Info("merchant enrichment", "looked_up", rep.Looked, "identified", rep.Identified, "ambiguous", rep.Ambiguous, "none", rep.None, "failed", rep.Failed, "expired", rep.Expired)
	}
}
