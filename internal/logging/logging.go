// Package logging builds Ledger's slog logger and carries request and job
// identity through contexts.
//
// Convention (decision D17): every request log line carries request_id; every
// background log line carries job_id (a ULID, new for each run) and job (the
// job's name). Neither field is ever derived from user data.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/aurimasniekis/go-ledger/internal/redact"
)

// New returns a logger that writes to w in the given format ("json" or "text")
// at the given level ("debug", "info", "warn" or "error"). Output passes through
// r (nil means redact.Default()), and lines logged with a context that carries
// a request ID get a request_id attribute.
func New(w io.Writer, level, format string, r *redact.Redactor) (*slog.Logger, error) {
	l, _, err := NewLeveled(w, level, format, r)
	return l, err
}

// NewLeveled is New, and also returns the level variable the logger reads, so a
// caller can change the level after startup (for example once the stored
// settings are known).
func NewLeveled(w io.Writer, level, format string, r *redact.Redactor) (*slog.Logger, *slog.LevelVar, error) {
	lv, err := ParseLevel(level)
	if err != nil {
		return nil, nil, err
	}
	levelVar := new(slog.LevelVar)
	levelVar.Set(lv)
	if r == nil {
		r = redact.Default()
	}
	opts := &slog.HandlerOptions{Level: levelVar, ReplaceAttr: r.ReplaceAttr}
	var h slog.Handler
	switch strings.ToLower(format) {
	case "json", "":
		h = slog.NewJSONHandler(w, opts)
	case "text":
		h = slog.NewTextHandler(w, opts)
	default:
		return nil, nil, fmt.Errorf("unknown log format %q", format)
	}
	return slog.New(contextHandler{Handler: h}), levelVar, nil
}

// ParseLevel maps a configured level name to its slog level.
func ParseLevel(level string) (slog.Level, error) {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "info", "":
		lv = slog.LevelInfo
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		return 0, fmt.Errorf("unknown log level %q", level)
	}
	return lv, nil
}

// contextHandler adds the request ID found in the log call's context.
// A logger that already carries request_id (see httpapi's access log) is not
// given a second one.
type contextHandler struct {
	slog.Handler
	hasID bool
}

func (h contextHandler) Handle(ctx context.Context, rec slog.Record) error {
	if id := RequestID(ctx); id != "" && !h.hasID {
		rec.AddAttrs(slog.String("request_id", id))
	}
	return h.Handler.Handle(ctx, rec)
}

func (h contextHandler) WithAttrs(a []slog.Attr) slog.Handler {
	has := h.hasID
	for _, at := range a {
		if at.Key == "request_id" {
			has = true
		}
	}
	return contextHandler{h.Handler.WithAttrs(a), has}
}

func (h contextHandler) WithGroup(n string) slog.Handler {
	// An ID already attached by With sits above the group, so hasID stays.
	// Without one, the ID added in Handle lands inside the group (known).
	return contextHandler{h.Handler.WithGroup(n), h.hasID}
}

type ctxKey int

const (
	requestIDKey ctxKey = iota
	loggerKey
)

// WithRequestID returns ctx carrying the request ID.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestID returns the request ID in ctx, or "".
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// WithLogger returns ctx carrying a request-scoped logger.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, l)
}

// FromContext returns the logger in ctx, or the default logger.
func FromContext(ctx context.Context) *slog.Logger {
	if l, _ := ctx.Value(loggerKey).(*slog.Logger); l != nil {
		return l
	}
	return slog.Default()
}

// Job returns a logger for one run of the named background job, and the run's
// job_id. Every line it writes carries job_id and job.
func Job(l *slog.Logger, name string) (*slog.Logger, string) {
	id := NewULID()
	return l.With("job_id", id, "job", name), id
}
