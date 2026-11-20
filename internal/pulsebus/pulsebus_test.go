package pulsebus

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aurimasniekis/go-pulse/pkg/pulse/envelope"

	"github.com/aurimasniekis/go-ledger/internal/outbox"
)

func TestDialerWithMissingCredsFailsWithoutLeakingPath(t *testing.T) {
	cfg := Config{URL: "tls://127.0.0.1:1", CredsFile: "/secret/place/pulse.creds", CAFile: "/secret/place/ca.pem", Source: "ledger"}
	_, err := Dialer(cfg)(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "/secret/place") {
		t.Fatalf("path leaked: %v", err)
	}
}

func TestDialerHonoursCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Dialer(Config{})(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestClassify(t *testing.T) {
	if err := classify(envelope.ErrSubject); !errors.Is(err, outbox.ErrPermanent) {
		t.Fatalf("subject error: %v", err)
	}
	if err := classify(context.DeadlineExceeded); !errors.Is(err, outbox.ErrTimeout) {
		t.Fatalf("deadline: %v", err)
	}
	other := errors.New("other")
	if !errors.Is(classify(other), other) || errors.Is(classify(other), outbox.ErrTimeout) {
		t.Fatal("other errors pass through")
	}
}

func TestScrubKeepsIdentityAndHidesPaths(t *testing.T) {
	cfg := Config{CredsFile: "/a/b.creds", CAFile: "/a/ca.pem"}
	base := errors.New("open /a/b.creds: no such file; read /a/ca.pem")
	err := scrub(base, cfg)
	if strings.Contains(err.Error(), "/a/") || !errors.Is(err, base) {
		t.Fatalf("%v", err)
	}
	if scrub(base, Config{}).Error() != base.Error() {
		t.Fatal("unchanged errors are returned as is")
	}
}
