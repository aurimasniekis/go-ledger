// Package pulsebus adapts the Pulse SDK to [outbox.Bus]. It is the only place
// that imports the SDK, so provider details stay out of the publisher.
package pulsebus

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aurimasniekis/go-pulse/pkg/pulse"
	"github.com/aurimasniekis/go-pulse/pkg/pulse/envelope"
	"github.com/nats-io/nats.go"

	"github.com/aurimasniekis/go-ledger/internal/outbox"
)

// Config is what Dial needs. CredsFile is a path; its content is read by the SDK
// and never by Ledger, and the path never appears in an error or log line.
type Config struct {
	URL       string
	CredsFile string
	CAFile    string // optional extra CA, appended to the system pool
	Name      string // connection name shown on the server
	Source    string // default event source
}

// Dialer returns an [outbox.Dialer] for cfg. Nothing connects until it is called.
// The SDK dials without a context, so the call can outlast ctx by the SDK's own
// short connect timeout; the publisher dials from its own goroutine.
func Dialer(cfg Config) outbox.Dialer {
	return func(ctx context.Context) (outbox.Bus, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		opts := []pulse.Option{pulse.WithCredsFile(cfg.CredsFile), pulse.WithSource(cfg.Source)}
		if cfg.CAFile != "" {
			opts = append(opts, pulse.WithCAFile(cfg.CAFile))
		}
		if cfg.Name != "" {
			opts = append(opts, pulse.WithName(cfg.Name))
		}
		// No WithNATSOptions: TLS verification is never weakened.
		c, err := pulse.Connect(cfg.URL, opts...)
		if err != nil {
			return nil, scrub(err, cfg)
		}
		return &bus{c: c, cfg: cfg}, nil
	}
}

type bus struct {
	c   *pulse.Client
	cfg Config
}

func (b *bus) Close() { b.c.Close() }

func (b *bus) Publish(ctx context.Context, m outbox.Message) (outbox.Ack, error) {
	ack, err := b.c.Publish(ctx, m.Subject, m.Type, m.Payload,
		pulse.WithID(m.ID), pulse.WithTime(m.Time), pulse.WithEventSource(m.Source))
	if err != nil {
		return outbox.Ack{}, scrub(classify(err), b.cfg)
	}
	return outbox.Ack{Duplicate: ack.Duplicate}, nil
}

func classify(err error) error {
	switch {
	case errors.Is(err, envelope.ErrSubject):
		return fmt.Errorf("%w: %w", outbox.ErrPermanent, err)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, nats.ErrTimeout):
		return fmt.Errorf("%w: %w", outbox.ErrTimeout, err)
	}
	return err
}

// scrub replaces the creds and CA paths in an error text, keeping the error's
// identity for errors.Is.
func scrub(err error, cfg Config) error {
	s := err.Error()
	out := s
	for path, name := range map[string]string{cfg.CredsFile: "[creds_file]", cfg.CAFile: "[ca_file]"} {
		if path != "" {
			out = strings.ReplaceAll(out, path, name)
		}
	}
	if out == s {
		return err
	}
	return &scrubbed{msg: out, err: err}
}

type scrubbed struct {
	msg string
	err error
}

func (e *scrubbed) Error() string { return e.msg }
func (e *scrubbed) Unwrap() error { return e.err }
