package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/reporting"
)

// syncBuffer is a bytes.Buffer safe for the server goroutine and the test.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func TestServeStartsLogsAndShutsDown(t *testing.T) {
	env := map[string]string{
		"LEDGER_KEK_VALUE":          cmdSentinel,
		"LEDGER_BOGUS_NAME":         "x",
		"LEDGER_DATABASE_PATH":      filepath.Join(t.TempDir(), "ledger.db"),
		"LEDGER_REPORTING_TIMEZONE": "UTC",
	}
	files := map[string]string{".env": "LEDGER_LOG_LEVEL=info\nLEDGER_OTHER_TYPO=" + cmdSentinel + "\n"}
	opts := testOpts(env, files)
	opts.Environ = func() []string { return []string{"LEDGER_BOGUS_NAME=x", "PATH=/bin"} }

	var errOut syncBuffer
	addrc := make(chan net.Addr, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		done <- runServe(ctx, []string{"--http.listen=127.0.0.1:0", "--http.management_listen=127.0.0.1:0"}, &errOut,
			serveEnv{Options: opts, Listening: func(a net.Addr) { addrc <- a },
				UI: fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>Ledger</title>")}}})
	}()
	var addr net.Addr
	select {
	case addr = <-addrc:
	case code := <-done:
		t.Fatalf("exited %d: %s", code, errOut.String())
	case <-time.After(5 * time.Second):
		t.Fatal("server did not start")
	}

	req, _ := http.NewRequest("GET", "http://"+addr.String()+"/api/missing", nil)
	req.Header.Set("X-Request-ID", "serve-test-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 404 || resp.Header.Get("X-Request-ID") != "serve-test-1" || !strings.Contains(string(body), `"request_id":"serve-test-1"`) {
		t.Fatalf("got %d %v %s", resp.StatusCode, resp.Header, body)
	}

	// The shell is served at / by the same process, next to the API.
	resp, err = http.Get("http://" + addr.String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "<title>Ledger</title>") {
		t.Fatalf("shell: got %d %s", resp.StatusCode, body)
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errOut.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("did not shut down")
	}

	logs := errOut.String()
	if strings.Contains(logs, cmdSentinel) {
		t.Fatalf("secret in logs: %s", logs)
	}
	var sawEnv, sawUnknown, sawRequest bool
	for _, l := range strings.Split(strings.TrimSpace(logs), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("default log format must be JSON, got %q", l)
		}
		switch {
		case m["msg"] == "env file loaded" && strings.Contains(l, "LEDGER_OTHER_TYPO"):
			sawEnv = true
		case m["msg"] == "unknown LEDGER_ variable is ignored; check the spelling" && m["variable"] == "LEDGER_BOGUS_NAME":
			sawUnknown = true
		case m["msg"] == "request" && m["request_id"] == "serve-test-1":
			sawRequest = true
		}
	}
	if !sawEnv || !sawUnknown || !sawRequest {
		t.Fatalf("env=%v unknown=%v request=%v in:\n%s", sawEnv, sawUnknown, sawRequest, logs)
	}
}

func TestServeRejectsBadConfigWithUnknownWarning(t *testing.T) {
	opts := testOpts(map[string]string{"LEDGER_KEK_VLAUE": cmdSentinel}, nil)
	opts.Environ = func() []string { return []string{"LEDGER_KEK_VLAUE=" + cmdSentinel} }
	var errOut bytes.Buffer
	code := runServe(context.Background(), nil, &errOut, serveEnv{Options: opts})
	if code != 1 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"warning: unknown variable LEDGER_KEK_VLAUE", "kek: required"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr lacks %q:\n%s", want, errOut.String())
		}
	}
	if strings.Contains(errOut.String(), cmdSentinel) {
		t.Fatal("secret leaked")
	}
}

func TestServeUsageErrors(t *testing.T) {
	var errOut bytes.Buffer
	if code := runServe(context.Background(), []string{"extra"}, &errOut, serveEnv{Options: testOpts(nil, nil)}); code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
	if code := runServe(context.Background(), []string{"--nope"}, &errOut, serveEnv{Options: testOpts(nil, nil)}); code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
}

// served is what a test needs to probe and stop a running "ledger serve".
type served struct {
	addr   net.Addr
	cancel context.CancelFunc
	done   chan int
	logs   *syncBuffer
}

// startServe runs "ledger serve" against a temp database and returns what a
// test needs to probe and stop it. dbPath is the database file.
func startServe(t *testing.T, dbPath string, extraEnv map[string]string, host func() (string, bool)) *served {
	t.Helper()
	env := map[string]string{"LEDGER_KEK_VALUE": cmdSentinel, "LEDGER_DATABASE_PATH": dbPath}
	for k, v := range extraEnv {
		env[k] = v
	}
	s := &served{done: make(chan int, 1), logs: &syncBuffer{}}
	addrc := make(chan net.Addr, 1)
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	t.Cleanup(cancel)
	go func() {
		s.done <- runServe(ctx, []string{"--http.listen=127.0.0.1:0", "--http.management_listen="}, s.logs, serveEnv{
			Options:   testOpts(env, nil),
			Listening: func(a net.Addr) { addrc <- a },
			Reporting: reporting.Options{HostZoneName: host},
		})
	}()
	select {
	case s.addr = <-addrc:
	case code := <-s.done:
		t.Fatalf("exited %d: %s", code, s.logs.String())
	case <-time.After(5 * time.Second):
		t.Fatal("server did not start")
	}
	return s
}

func (s *served) stop(t *testing.T) {
	t.Helper()
	s.cancel()
	select {
	case code := <-s.done:
		if code != 0 {
			t.Fatalf("exit %d: %s", code, s.logs.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("did not shut down")
	}
}

func hostIs(name string) func() (string, bool) { return func() (string, bool) { return name, true } }

func TestServeOpensStoreCapturesZoneAndClosesOnShutdown(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sub", "ledger.db")
	s := startServe(t, dbPath, nil, hostIs("Europe/Vilnius"))

	resp, err := http.Get("http://" + s.addr.String() + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"database":"ok"`) {
		t.Fatalf("readyz: %d %s", resp.StatusCode, body)
	}
	if fi, err := os.Stat(dbPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("database file: %v %v", fi, err)
	}
	s.stop(t)

	// Closed on shutdown: the last connection checkpointed and removed the WAL.
	if _, err := os.Stat(dbPath + "-wal"); err == nil {
		t.Error("the WAL file remains: the store was not closed")
	}
	logs := s.logs.String()
	for _, want := range []string{"database ready", "reporting time zone captured and stored", "Europe/Vilnius"} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs lack %q:\n%s", want, logs)
		}
	}

	// Second start: the stored zone wins over a different host zone, and an
	// explicit different value warns.
	s2 := startServe(t, dbPath, nil, hostIs("Asia/Tokyo"))
	s2.stop(t)
	if !strings.Contains(s2.logs.String(), `"source":"stored"`) || strings.Contains(s2.logs.String(), "Asia/Tokyo") {
		t.Errorf("stored zone not used:\n%s", s2.logs.String())
	}
	s3 := startServe(t, dbPath, map[string]string{"LEDGER_REPORTING_TIMEZONE": "UTC"}, hostIs("Asia/Tokyo"))
	s3.stop(t)
	if !strings.Contains(s3.logs.String(), "reporting_timezone_changed") || !strings.Contains(s3.logs.String(), `"level":"WARN"`) {
		t.Errorf("no change warning:\n%s", s3.logs.String())
	}
}

func TestServeRefusesStartupWithoutResolvableZone(t *testing.T) {
	var errOut syncBuffer
	code := runServe(context.Background(), []string{"--http.listen=127.0.0.1:0", "--http.management_listen="}, &errOut, serveEnv{
		Options:   testOpts(map[string]string{"LEDGER_KEK_VALUE": cmdSentinel, "LEDGER_DATABASE_PATH": filepath.Join(t.TempDir(), "l.db")}, nil),
		Listening: func(net.Addr) { t.Error("must not listen") },
		Reporting: reporting.Options{HostZoneName: func() (string, bool) { return "", false }},
	})
	if code != 1 || !strings.Contains(errOut.String(), "LEDGER_REPORTING_TIMEZONE") {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
}

func TestServeRefusesUnopenableDatabase(t *testing.T) {
	// The parent of the database path is a file, so it cannot be created.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var errOut syncBuffer
	code := runServe(context.Background(), []string{"--http.listen=127.0.0.1:0", "--http.management_listen="}, &errOut, serveEnv{
		Options:   testOpts(map[string]string{"LEDGER_KEK_VALUE": cmdSentinel, "LEDGER_DATABASE_PATH": filepath.Join(blocker, "l.db")}, nil),
		Listening: func(net.Addr) { t.Error("must not listen") },
	})
	if code != 1 || !strings.Contains(errOut.String(), "database") {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
}
