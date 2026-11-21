package fx_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/fx"
	"github.com/aurimasniekis/go-ledger/internal/domain/fx/fxtest"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
)

func memCache(*testing.T) fx.Cache { return fxtest.NewCache() }

func TestMemoryCacheContract(t *testing.T) { fxtest.CacheContract(t, memCache) }

func TestServiceWithMemoryCache(t *testing.T) { fxtest.ServiceSuite(t, memCache) }

func TestNewServiceValidates(t *testing.T) {
	c := fxtest.NewCache()
	if _, err := fx.NewService(nil, nil, fx.Options{ProviderName: "x"}); err == nil {
		t.Error("nil cache accepted")
	}
	if _, err := fx.NewService(c, nil, fx.Options{}); err == nil {
		t.Error("no provider name accepted")
	}
	for _, f := range []fx.Freshness{
		{RefreshAfter: 0, StaleAfter: 48 * time.Hour},
		{RefreshAfter: time.Hour, StaleAfter: time.Hour},
		{RefreshAfter: 72 * time.Hour, StaleAfter: 48 * time.Hour},
	} {
		if _, err := fx.NewService(c, nil, fx.Options{ProviderName: "x", Freshness: f}); err == nil {
			t.Errorf("freshness %+v accepted", f)
		}
	}
	if err := fx.DefaultFreshness.Validate(); err != nil {
		t.Error(err)
	}
}

func TestProviderErrorHidesCause(t *testing.T) {
	cause := errors.New("GET https://api.example/latest?key=secret-value: 503")
	e := &fx.ProviderError{Kind: fx.KindUnavailable, Provider: "frankfurter", Op: "latest", Code: "http_503", Err: cause}
	if strings.Contains(e.Error(), "secret") || strings.Contains(e.Error(), "https") {
		t.Errorf("message leaks the cause: %s", e)
	}
	if !errors.Is(e, fx.ErrUnavailable) || errors.Is(e, fx.ErrRateLimited) || !errors.Is(e, cause) {
		t.Error("errors.Is does not follow the kind and cause")
	}
}

func TestUnclassifiedProviderErrorServesCache(t *testing.T) {
	ctx := context.Background()
	c := fxtest.NewCache()
	p := fxtest.NewProvider("frankfurter", "ECB")
	clock := fxtest.NewClock(fxtest.T0)
	svc, err := fx.NewService(c, p, fx.Options{Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	p.SetDown(errors.New("dial tcp: connection refused"))
	got, err := svc.Convert(ctx, money.MustParse("1", "EUR"), money.MustParseCurrency("USD"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != fx.StatusMissing || !errors.Is(got.RefreshError, fx.ErrUnavailable) {
		t.Errorf("unclassified failure: %+v", got)
	}
}

func TestCanceledContextIsAnError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc, err := fx.NewService(fxtest.NewCache(), fxtest.NewProvider("frankfurter", "ECB"), fx.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Convert(ctx, money.MustParse("1", "EUR"), money.MustParseCurrency("USD")); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}
