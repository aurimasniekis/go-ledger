package httpapi

import (
	"bufio"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/logging"
)

// RequestIDHeader carries the request ID in and out.
const RequestIDHeader = "X-Request-ID"

// validRequestID accepts a client-supplied ID only if it is short and made of
// harmless characters, so it is safe in logs and headers.
func validRequestID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

// statusWriter records the status and size of a response.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach Flush, Hijack and deadlines.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("hijacking is not supported")
	}
	return h.Hijack()
}

// requestIDMiddleware takes the ID from the request header (or makes a ULID),
// echoes it in the response and stores it in the context.
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if !validRequestID(id) {
			id = logging.NewULID()
		}
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(logging.WithRequestID(r.Context(), id)))
	})
}

// accessLogMiddleware logs one line per request, after it completes. It logs
// the method, path (never the query string), status, size and duration; never
// headers or bodies. The request-scoped logger is put in the context.
func accessLogMiddleware(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w}
			// The request logger carries request_id itself, so a handler that
			// logs without a context still gets it.
			reqLog := log.With("request_id", logging.RequestID(r.Context()))
			ctx := logging.WithLogger(r.Context(), reqLog)
			// Logged in a defer so a request aborted with http.ErrAbortHandler
			// (which is re-panicked) still gets its line.
			completed := false
			defer func() {
				aborted := !completed
				if sw.status == 0 && !aborted {
					sw.status = http.StatusOK
				}
				level := slog.LevelInfo
				if sw.status >= 500 || aborted {
					level = slog.LevelError
				}
				remote, _, err := net.SplitHostPort(r.RemoteAddr)
				if err != nil {
					remote = r.RemoteAddr
				}
				reqLog.Log(ctx, level, "request",
					"method", r.Method,
					"path", r.URL.Path,
					"status", sw.status,
					"bytes", sw.bytes,
					"duration_ms", float64(time.Since(start).Microseconds())/1000,
					"remote", remote,
					"aborted", aborted,
				)
			}()
			next.ServeHTTP(sw, r.WithContext(ctx))
			completed = true
		})
	}
}

// recoverMiddleware turns a panic into a 500 error envelope. The panic value
// and stack go to the log only, never to the response.
func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(rec)
			}
			logging.FromContext(r.Context()).ErrorContext(r.Context(), "panic in handler",
				"panic", rec, "stack", string(debug.Stack()))
			if sw, ok := w.(*statusWriter); ok && sw.status != 0 {
				return // the response has started; nothing more can be sent
			}
			WriteError(w, r, &Error{Status: http.StatusInternalServerError, Code: CodeInternal, Message: "internal error"})
		}()
		next.ServeHTTP(w, r)
	})
}

// bodyLimitMiddleware rejects bodies over max bytes: at once when Content-Length
// says so, otherwise when a handler reads past the limit.
func bodyLimitMiddleware(max int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if max > 0 {
				if r.ContentLength > max {
					WriteError(w, r, tooLarge(max))
					return
				}
				r.Body = http.MaxBytesReader(w, r.Body, max)
			}
			next.ServeHTTP(w, r)
		})
	}
}
