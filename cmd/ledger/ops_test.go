package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// startServe runs "ledger serve" with args and returns the main and management
// addresses (management is nil when there is no such listener).
func startServe(t *testing.T, args ...string) (main, mgmt net.Addr, errOut *syncBuffer) {
	t.Helper()
	opts := testOpts(map[string]string{"LEDGER_KEK_VALUE": cmdSentinel}, nil)
	errOut = &syncBuffer{}
	mainc, mgmtc := make(chan net.Addr, 1), make(chan net.Addr, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		done <- runServe(ctx, args, errOut, serveEnv{Options: opts,
			Listening:           func(a net.Addr) { mainc <- a },
			ManagementListening: func(a net.Addr) { mgmtc <- a }})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("server did not stop")
		}
	})
	select {
	case main = <-mainc:
	case code := <-done:
		t.Fatalf("exited %d: %s", code, errOut.String())
	case <-time.After(5 * time.Second):
		t.Fatal("server did not start")
	}
	select {
	case mgmt = <-mgmtc:
	case <-time.After(200 * time.Millisecond):
	}
	return main, mgmt, errOut
}

func fetch(t *testing.T, addr net.Addr, path string) (int, string) {
	t.Helper()
	resp, err := http.Get("http://" + addr.String() + path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestManagementListenerServesOpsEndpoints(t *testing.T) {
	main, mgmt, _ := startServe(t, "--http.listen=127.0.0.1:0", "--http.management_listen=127.0.0.1:0")
	if mgmt == nil {
		t.Fatal("no management listener")
	}
	fetch(t, main, "/api/some/user/path-12345") // a 404 on the main listener
	if code, body := fetch(t, mgmt, "/metrics"); code != 200 ||
		!strings.Contains(body, "ledger_build_info{") ||
		!strings.Contains(body, `ledger_http_requests_total{route="unmatched",method="GET",status="404"} 1`) ||
		strings.Contains(body, "path-12345") {
		t.Errorf("metrics: %d\n%s", code, body)
	}
	if code, _ := fetch(t, mgmt, "/healthz"); code != 200 {
		t.Errorf("healthz %d", code)
	}
	if code, _ := fetch(t, mgmt, "/readyz"); code != 200 {
		t.Errorf("readyz %d", code)
	}
	// The main listener does not expose them: the single-page shell answers
	// instead of the operational payloads.
	for _, p := range []string{"/metrics", "/healthz", "/readyz"} {
		if _, body := fetch(t, main, p); strings.Contains(body, "ledger_build_info") || strings.Contains(body, `"status"`) {
			t.Errorf("main listener served %s: %s", p, body)
		}
	}
}

func TestEmptyManagementListenUsesMainListener(t *testing.T) {
	main, mgmt, errOut := startServe(t, "--http.listen=127.0.0.1:0", "--http.management_listen=")
	if mgmt != nil {
		t.Fatal("unexpected management listener")
	}
	for _, p := range []string{"/metrics", "/healthz", "/readyz"} {
		if code, _ := fetch(t, main, p); code != 200 {
			t.Errorf("%s: %d", p, code)
		}
	}
	if !strings.Contains(errOut.String(), "main listener, unauthenticated") {
		t.Errorf("no exposure warning in log:\n%s", errOut.String())
	}
}

func TestNonLoopbackManagementListenerWarns(t *testing.T) {
	_, mgmt, errOut := startServe(t, "--http.listen=127.0.0.1:0", "--http.management_listen=0.0.0.0:0")
	if mgmt == nil {
		t.Fatal("no management listener")
	}
	if !strings.Contains(errOut.String(), "keep it off public interfaces") {
		t.Errorf("no warning in log:\n%s", errOut.String())
	}
}
