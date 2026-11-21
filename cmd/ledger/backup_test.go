package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/reporting"
	"github.com/aurimasniekis/go-ledger/internal/store"
)

func opsFor(dbPath string) opsEnv {
	return opsEnv{
		Options: testOpts(map[string]string{"LEDGER_KEK_VALUE": cmdSentinel, "LEDGER_DATABASE_PATH": dbPath}, nil),
		Now:     func() time.Time { return time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC) },
	}
}

func newDB(t *testing.T, path string) {
	t.Helper()
	db, err := store.Open(context.Background(), path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBackupAndRestoreCommands(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "ledger.db")
	newDB(t, dbPath)
	env := opsFor(dbPath)

	var out, errOut bytes.Buffer
	if code := runBackup(nil, &out, &errOut, env); code != 0 {
		t.Fatalf("backup exit %d: %s", code, errOut.String())
	}
	want := dbPath + ".backup-20261120T120000Z.db"
	if !strings.Contains(out.String(), want) || !strings.Contains(out.String(), "verified") {
		t.Fatalf("stdout = %q", out.String())
	}
	if strings.Contains(out.String()+errOut.String(), cmdSentinel) {
		t.Fatal("secret in output")
	}
	if fi, err := os.Stat(want); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("backup = %v, %v", fi, err)
	}

	// A second backup at the same second refuses to overwrite.
	out.Reset()
	errOut.Reset()
	if code := runBackup(nil, &out, &errOut, env); code != 1 || !strings.Contains(errOut.String(), "already exists") {
		t.Fatalf("second backup exit %d: %s", code, errOut.String())
	}

	// Restore refuses to replace without --force.
	out.Reset()
	errOut.Reset()
	if code := runRestore([]string{want}, &out, &errOut, env); code != 1 || !strings.Contains(errOut.String(), "--force") {
		t.Fatalf("restore exit %d: %s", code, errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if code := runRestore([]string{"--force", want}, &out, &errOut, env); code != 0 {
		t.Fatalf("restore --force exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "previous database kept at") {
		t.Fatalf("stdout = %q", out.String())
	}
	if m, _ := filepath.Glob(dbPath + ".pre-restore-*"); len(m) == 0 {
		t.Fatal("no moved-aside database")
	}
}

func TestBackupCommandErrors(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ledger.db")
	env := opsFor(dbPath)
	var out, errOut bytes.Buffer
	if code := runBackup(nil, &out, &errOut, env); code != 1 || !strings.Contains(errOut.String(), "does not exist") {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if _, err := os.Stat(dbPath); err == nil {
		t.Fatal("backup created the database")
	}
	errOut.Reset()
	if code := runBackup([]string{"extra"}, &out, &errOut, env); code != 2 {
		t.Fatalf("exit %d", code)
	}
	errOut.Reset()
	if code := runRestore(nil, &out, &errOut, env); code != 2 || !strings.Contains(errOut.String(), "usage") {
		t.Fatalf("restore exit %d: %s", code, errOut.String())
	}
	errOut.Reset()
	if code := runRestore([]string{filepath.Join(t.TempDir(), "nope.db")}, &out, &errOut, env); code != 1 {
		t.Fatalf("restore of a missing file exit %d: %s", code, errOut.String())
	}
}

func TestBackupPruneCommand(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ledger.db")
	env := opsFor(dbPath)
	for _, n := range []string{".pre-v0001-20260101T000000.000000000Z.bak", ".pre-v0002-20260102T000000.000000000Z.bak"} {
		_ = os.WriteFile(dbPath+n, []byte("x"), 0o600)
	}
	var out, errOut bytes.Buffer
	if code := runBackup([]string{"prune"}, &out, &errOut, env); code != 2 {
		t.Fatalf("prune without --keep exit %d", code)
	}
	errOut.Reset()
	if code := runBackup([]string{"prune", "--keep", "1"}, &out, &errOut, env); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if m, _ := store.PreMigrationBackups(dbPath); len(m) != 1 {
		t.Fatalf("left %v", m)
	}
}

func TestRestoreRefusedWhileServing(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ledger.db")
	newDB(t, dbPath)
	backup := dbPath + ".b"
	if _, err := store.Backup(context.Background(), dbPath, backup, 0); err != nil {
		t.Fatal(err)
	}
	lock, err := store.LockInstance(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	var out, errOut bytes.Buffer
	if code := runRestore([]string{"--force", backup}, &out, &errOut, opsFor(dbPath)); code != 1 || !strings.Contains(errOut.String(), "another Ledger process") {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
}

func TestServeRefusesSecondInstance(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ledger.db")
	env := serveEnv{Options: testOpts(map[string]string{"LEDGER_KEK_VALUE": cmdSentinel, "LEDGER_DATABASE_PATH": dbPath}, nil),
		Reporting: reporting.Options{HostZoneName: hostIs("UTC")}}
	up := make(chan net.Addr, 1)
	env.Listening = func(a net.Addr) { up <- a }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	first := &syncBuffer{}
	go func() {
		done <- runServe(ctx, []string{"--http.listen=127.0.0.1:0", "--http.management_listen="}, first, env)
	}()
	select {
	case <-up:
	case code := <-done:
		t.Fatalf("first instance exited %d: %s", code, first.String())
	case <-time.After(5 * time.Second):
		t.Fatal("first instance did not start")
	}
	second := &syncBuffer{}
	env2 := env
	env2.Listening = nil
	if code := runServe(context.Background(), []string{"--http.listen=127.0.0.1:0", "--http.management_listen="}, second, env2); code != 1 || !strings.Contains(second.String(), "another Ledger process") {
		t.Fatalf("second instance exit %d: %s", code, second.String())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("first instance did not stop")
	}
	// After a clean stop the lock is free again.
	l, err := store.LockInstance(dbPath)
	if err != nil {
		t.Fatalf("lock after stop: %v", err)
	}
	_ = l.Close()
}

func TestRestoreReportsStaleWALWithoutDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "ledger.db")
	newDB(t, dbPath)
	env := opsFor(dbPath)
	var out, errOut bytes.Buffer
	if code := runBackup(nil, &out, &errOut, env); code != 0 {
		t.Fatalf("backup exit %d: %s", code, errOut.String())
	}
	backup := dbPath + ".backup-20261120T120000Z.db"
	if err := os.Remove(dbPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dbPath+"-wal", []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	if code := runRestore([]string{backup}, &out, &errOut, env); code != 0 {
		t.Fatalf("restore exit %d: %s", code, errOut.String())
	}
	if strings.Contains(out.String(), "previous database kept at") {
		t.Fatalf("stdout claims a previous database was kept: %q", out.String())
	}
	if !strings.Contains(out.String(), "stale WAL/SHM/journal moved to "+dbPath+".pre-restore-") {
		t.Fatalf("stdout = %q, want the stale files reported", out.String())
	}
}
