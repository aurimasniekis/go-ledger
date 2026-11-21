package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var ctxb = context.Background()

// seeded opens a database with the embedded migrations and one setting.
func seeded(t *testing.T, path, value string) *DB {
	t.Helper()
	db := openTest(t, path, Options{})
	err := db.WriteTx(ctxb, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO settings(key, value, updated_at) VALUES ('k', ?, '2026-11-20T00:00:00.000000000Z')", value)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func settingValue(t *testing.T, path string) string {
	t.Helper()
	var v string
	if err := rawDB(t, path).QueryRowContext(ctxb, "SELECT value FROM settings WHERE key='k'").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestBackupOfLiveDatabase(t *testing.T) {
	path := tempPath(t)
	db := seeded(t, path, "before")
	dest := filepath.Join(t.TempDir(), "b.db")
	info, err := Backup(ctxb, path, dest, 0)
	if err != nil {
		t.Fatal(err)
	}
	if info.SchemaVersion != db.SchemaVersion() || info.Bytes == 0 || info.Path != dest {
		t.Fatalf("info = %+v", info)
	}
	if fi, err := os.Stat(dest); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("stat = %v, %v; want 0600", fi, err)
	}
	// The database keeps working and changes after the backup are not in it.
	if err := db.WriteTx(ctxb, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE settings SET value='after' WHERE key='k'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := settingValue(t, dest); got != "before" {
		t.Fatalf("backup value = %q, want before", got)
	}
	if got := settingValue(t, path); got != "after" {
		t.Fatalf("live value = %q, want after", got)
	}
	// No side files next to the backup.
	if m, _ := filepath.Glob(dest + "-*"); len(m) != 0 {
		t.Fatalf("side files: %v", m)
	}
}

func TestBackupModeIsOwnerOnlyUnderPermissiveUmask(t *testing.T) {
	path := tempPath(t)
	seeded(t, path, "v")
	dest := filepath.Join(t.TempDir(), "b.db")
	if _, err := Backup(ctxb, path, dest, 0); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dest); fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("mode %v exposes the backup", fi.Mode())
	}
}

func TestBackupRefusals(t *testing.T) {
	path := tempPath(t)
	seeded(t, path, "v")
	dir := t.TempDir()

	t.Run("never overwrites", func(t *testing.T) {
		dest := filepath.Join(dir, "exists.db")
		if err := os.WriteFile(dest, []byte("precious"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Backup(ctxb, path, dest, 0); !errors.Is(err, ErrBackupExists) {
			t.Fatalf("err = %v, want ErrBackupExists", err)
		}
		if b, _ := os.ReadFile(dest); string(b) != "precious" {
			t.Fatal("existing file was modified")
		}
	})
	t.Run("not onto the database itself", func(t *testing.T) {
		if _, err := Backup(ctxb, path, path, 0); !errors.Is(err, ErrBackupExists) {
			t.Fatalf("err = %v, want ErrBackupExists", err)
		}
	})
	t.Run("missing database is not created", func(t *testing.T) {
		missing := filepath.Join(dir, "none.db")
		_, err := Backup(ctxb, missing, filepath.Join(dir, "o.db"), 0)
		if !errors.Is(err, ErrNoDatabase) {
			t.Fatalf("err = %v, want ErrNoDatabase", err)
		}
		if _, err := os.Stat(missing); err == nil {
			t.Fatal("the database was created")
		}
		if _, err := os.Stat(filepath.Join(dir, "o.db")); err == nil {
			t.Fatal("a backup file was left behind")
		}
	})
	t.Run("failed copy leaves no file", func(t *testing.T) {
		notDB := filepath.Join(dir, "garbage.db")
		if err := os.WriteFile(notDB, []byte(strings.Repeat("not sqlite ", 50)), 0o600); err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(dir, "g.out")
		if _, err := Backup(ctxb, notDB, dest, 0); err == nil {
			t.Fatal("backing up a non-database succeeded")
		}
		if _, err := os.Stat(dest); err == nil {
			t.Fatal("a failed backup left a file")
		}
	})
}

func TestVerifyBackupRejects(t *testing.T) {
	dir := t.TempDir()
	t.Run("garbage", func(t *testing.T) {
		p := filepath.Join(dir, "g")
		_ = os.WriteFile(p, []byte(strings.Repeat("x", 4096)), 0o600)
		if _, err := VerifyBackup(ctxb, p); !errors.Is(err, ErrBackupInvalid) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("missing file", func(t *testing.T) {
		if _, err := VerifyBackup(ctxb, filepath.Join(dir, "none")); err == nil {
			t.Fatal("want error")
		}
		if _, err := os.Stat(filepath.Join(dir, "none")); err == nil {
			t.Fatal("verify created the file")
		}
	})
	t.Run("sqlite without a migration ledger", func(t *testing.T) {
		p := filepath.Join(dir, "plain.db")
		mustExec(t, rawDB(t, p), "CREATE TABLE t(x)")
		if _, err := VerifyBackup(ctxb, p); !errors.Is(err, ErrBackupInvalid) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("newer than this binary", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "n.db")
		db := openTest(t, p, Options{})
		_ = db.Close()
		raw := rawDB(t, p)
		mustExec(t, raw, "INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (99, 'future', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', '2026-11-20T00:00:00.000000000Z')")
		_, _ = raw.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
		_ = raw.Close()
		if _, err := VerifyBackup(ctxb, p); !errors.Is(err, ErrDatabaseNewer) {
			t.Fatalf("err = %v, want ErrDatabaseNewer", err)
		}
	})
	t.Run("changed migration", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "c.db")
		db := openTest(t, p, Options{})
		_ = db.Close()
		raw := rawDB(t, p)
		mustExec(t, raw, "UPDATE schema_migrations SET checksum='bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' WHERE version=2")
		_ = raw.Close()
		if _, err := VerifyBackup(ctxb, p); !errors.Is(err, ErrChecksumMismatch) {
			t.Fatalf("err = %v, want ErrChecksumMismatch", err)
		}
	})
}

func TestRestoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.db")
	db := seeded(t, path, "good")
	backup := filepath.Join(t.TempDir(), "b.db")
	if _, err := Backup(ctxb, path, backup, 0); err != nil {
		t.Fatal(err)
	}
	// Damage the data after the backup, leaving WAL frames behind.
	if err := db.WriteTx(ctxb, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE settings SET value='bad' WHERE key='k'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	if _, err := Restore(ctxb, backup, path, false, now); !errors.Is(err, ErrRestoreTargetExists) {
		t.Fatalf("restore over existing without replace: %v", err)
	}
	res, err := Restore(ctxb, backup, path, true, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := settingValue(t, path); got != "good" {
		t.Fatalf("restored value = %q, want good", got)
	}
	if res.MovedAside == "" || !strings.Contains(res.MovedAside, ".pre-restore-") {
		t.Fatalf("MovedAside = %q", res.MovedAside)
	}
	if got := settingValue(t, res.MovedAside); got != "bad" {
		t.Fatalf("previous database value = %q, want bad (kept intact)", got)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("restored mode = %v, %v", fi, err)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(m) != 0 {
		t.Fatalf("staging files left: %v", m)
	}
	// The restored database opens normally, at the same version, no migration.
	again := openTest(t, path, Options{})
	if again.SchemaVersion() != res.Backup.SchemaVersion {
		t.Fatalf("version %d, backup %d", again.SchemaVersion(), res.Backup.SchemaVersion)
	}
	if b := backups(t, path); len(b) != 0 {
		t.Fatalf("restore triggered a migration backup: %v", b)
	}
}

func TestRestoreIntoNewLocationAndRefusals(t *testing.T) {
	path := tempPath(t)
	seeded(t, path, "v")
	backup := filepath.Join(t.TempDir(), "b.db")
	if _, err := Backup(ctxb, path, backup, 0); err != nil {
		t.Fatal(err)
	}
	t.Run("no existing database", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "sub", "ledger.db")
		res, err := Restore(ctxb, backup, target, false, time.Now())
		if err != nil || res.MovedAside != "" {
			t.Fatalf("res = %+v, err = %v", res, err)
		}
		if settingValue(t, target) != "v" {
			t.Fatal("data missing")
		}
	})
	t.Run("invalid backup leaves the database untouched", func(t *testing.T) {
		target := tempPath(t)
		seeded(t, target, "keep")
		bad := filepath.Join(t.TempDir(), "bad.db")
		_ = os.WriteFile(bad, []byte(strings.Repeat("z", 8192)), 0o600)
		if _, err := Restore(ctxb, bad, target, true, time.Now()); !errors.Is(err, ErrBackupInvalid) {
			t.Fatalf("err = %v", err)
		}
		if settingValue(t, target) != "keep" {
			t.Fatal("database was modified")
		}
		if m, _ := filepath.Glob(target + ".pre-restore-*"); len(m) != 0 {
			t.Fatalf("moved aside despite failure: %v", m)
		}
	})
	t.Run("not from the database itself", func(t *testing.T) {
		if _, err := Restore(ctxb, path, path, true, time.Now()); err == nil {
			t.Fatal("restoring a file onto itself succeeded")
		}
	})
	t.Run("backup file is untouched", func(t *testing.T) {
		before, _ := os.ReadFile(backup)
		target := filepath.Join(t.TempDir(), "x.db")
		if _, err := Restore(ctxb, backup, target, false, time.Now()); err != nil {
			t.Fatal(err)
		}
		after, _ := os.ReadFile(backup)
		if string(before) != string(after) {
			t.Fatal("restore modified the backup")
		}
	})
}

// TestFailedUpgradeRecovery is the documented upgrade-recovery procedure end
// to end: an upgrade fails, the database is left at the old version with a
// pre-migration backup beside it, and restoring that backup gives the old
// data back.
func TestFailedUpgradeRecovery(t *testing.T) {
	path := tempPath(t)
	v2 := exampleSet(t, 2)
	db := openTest(t, path, Options{Migrations: v2})
	mustExec(t, db.writer, "INSERT INTO accounts(id, name, created_at) VALUES (1, 'a', '2026-11-20T00:00:00.000000000Z')")
	_ = db.Close()

	broken := append(append([]Migration{}, v2...), Migration{Version: 3, Name: "broken", SQL: "CREATE TABLE ok(x); THIS IS NOT SQL;", Checksum: "x"})
	if _, err := Open(ctxb, path, Options{Migrations: broken}); err == nil {
		t.Fatal("broken upgrade succeeded")
	}
	b := backups(t, path)
	if len(b) != 1 {
		t.Fatalf("backups = %v, want one", b)
	}
	// The failed migration rolled back: still version 2, still usable.
	if n := queryInt(t, rawDB(t, path), "SELECT count(*) FROM schema_migrations"); n != 2 {
		t.Fatalf("version after failed upgrade = %d, want 2", n)
	}

	// Operator decides to go back to the backup (for example after a bad
	// but successful upgrade): restore it, replacing the database.
	res, err := restore(ctxb, b[0], path, true, time.Now(), v2)
	if err != nil {
		t.Fatal(err)
	}
	if res.Backup.SchemaVersion != 2 {
		t.Fatalf("restored version %d, want 2", res.Backup.SchemaVersion)
	}
	if n := queryInt(t, rawDB(t, path), "SELECT count(*) FROM accounts"); n != 1 {
		t.Fatalf("restored rows = %d, want 1", n)
	}
	// The fixed binary then upgrades the restored database.
	fixed := exampleSet(t, 3)
	up := openTest(t, path, Options{Migrations: fixed})
	if up.SchemaVersion() != 3 {
		t.Fatalf("version %d, want 3", up.SchemaVersion())
	}
}

func TestInstanceLock(t *testing.T) {
	path := tempPath(t)
	l1, err := LockInstance(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path + ".lock"); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("lock file = %v, %v", fi, err)
	}
	if _, err := LockInstance(path); err != nil && !errors.Is(err, ErrInstanceLocked) {
		t.Fatalf("second lock err = %v", err)
	} else if err == nil && lockSupported {
		t.Fatal("second lock succeeded while the first was held")
	}
	if err := l1.Close(); err != nil {
		t.Fatal(err)
	}
	l2, err := LockInstance(path)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	_ = l2.Close()
	if err := (*InstanceLock)(nil).Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPruneMigrationBackups(t *testing.T) {
	path := tempPath(t)
	names := []string{".pre-v0002-20260101T000000.000000000Z.bak", ".pre-v0003-20260102T000000.000000000Z.bak", ".pre-v0004-20260103T000000.000000000Z.bak"}
	for _, n := range names {
		_ = os.WriteFile(path+n, []byte("x"), 0o600)
	}
	other := []string{path, path + ".backup-20260101T000000Z.db", path + ".pre-restore-20260101T000000.000000000Z"}
	for _, n := range other {
		_ = os.WriteFile(n, []byte("x"), 0o600)
	}
	if _, err := PruneMigrationBackups(path, -1); err == nil {
		t.Fatal("negative keep accepted")
	}
	removed, err := PruneMigrationBackups(path, 1)
	if err != nil || len(removed) != 2 {
		t.Fatalf("removed = %v, err = %v", removed, err)
	}
	left, _ := PreMigrationBackups(path)
	if len(left) != 1 || !strings.HasSuffix(left[0], names[2]) {
		t.Fatalf("left = %v, want only the newest", left)
	}
	for _, n := range other {
		if _, err := os.Stat(n); err != nil {
			t.Fatalf("%s was removed: %v", n, err)
		}
	}
}

func TestModeWarnings(t *testing.T) {
	if lockSupported == false && os.PathSeparator == '\\' {
		t.Skip("mode bits are not meaningful here")
	}
	dir := filepath.Join(t.TempDir(), "d")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ledger.db")
	if w := ModeWarnings(path); len(w) != 0 {
		t.Fatalf("warnings for missing files: %v", w)
	}
	seeded(t, path, "v")
	if w := ModeWarnings(path); len(w) != 0 {
		t.Fatalf("warnings for a database Open created: %v", w)
	}
	_ = os.Chmod(path, 0o644)
	_ = os.Chmod(dir, 0o777)
	w := ModeWarnings(path)
	if len(w) != 2 {
		t.Fatalf("warnings = %v, want the file and the directory", w)
	}
}

// liveWAL returns a database at path that still has committed data only in
// its -wal file (the connection stays open): the db and wal bytes.
func liveWAL(t *testing.T, path, value string) (dbBytes, walBytes []byte) {
	t.Helper()
	seeded(t, path, value)
	db, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wal, err := os.ReadFile(path + "-wal")
	if err != nil || len(wal) == 0 {
		t.Fatalf("expected WAL content: %v, %d bytes", err, len(wal))
	}
	return db, wal
}

func TestRestoreIgnoresStaleWALWithoutDatabase(t *testing.T) {
	src := filepath.Join(t.TempDir(), "good.db")
	seeded(t, src, "from-backup")
	backup := filepath.Join(t.TempDir(), "b.db")
	if _, err := Backup(ctxb, src, backup, 0); err != nil {
		t.Fatal(err)
	}
	_, otherWAL := liveWAL(t, filepath.Join(t.TempDir(), "other.db"), "b-wal")

	path := tempPath(t) // no database file, only a stale WAL and SHM
	if err := os.WriteFile(path+"-wal", otherWAL, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+"-shm", []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	res, err := Restore(ctxb, backup, path, false, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := settingValue(t, path); got != "from-backup" {
		t.Fatalf("restored value = %q, want from-backup (stale WAL replayed)", got)
	}
	if !res.Placed {
		t.Fatal("restored database not reported as placed")
	}
	if res.MovedAside != "" {
		t.Fatalf("MovedAside = %q, want empty: there was no database to move aside", res.MovedAside)
	}
	if len(res.StaleMoved) != 2 || !strings.HasSuffix(res.StaleMoved[0], "-wal") || !strings.HasSuffix(res.StaleMoved[1], "-shm") {
		t.Fatalf("StaleMoved = %v, want the -wal and -shm files", res.StaleMoved)
	}
	if b, err := os.ReadFile(res.StaleMoved[0]); err != nil || len(b) != len(otherWAL) {
		t.Fatalf("stale WAL not kept aside: %v", err)
	}
	if _, err := os.Stat(path + "-wal"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale WAL still next to the restored database")
	}
}

func TestRestoreBackupWithOwnWAL(t *testing.T) {
	// A moved-aside database whose newest data is only in its WAL must
	// restore with that data.
	aside := filepath.Join(t.TempDir(), "ledger.db.pre-restore-20261120T120000.000000000Z")
	dbBytes, wal := liveWAL(t, aside, "new-in-wal")
	copyDir := t.TempDir()
	backup := filepath.Join(copyDir, filepath.Base(aside))
	if err := os.WriteFile(backup, dbBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup+"-wal", wal, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBackup(ctxb, backup); err != nil {
		t.Fatal(err)
	}
	path := tempPath(t)
	if _, err := Restore(ctxb, backup, path, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := settingValue(t, path); got != "new-in-wal" {
		t.Fatalf("restored value = %q, want new-in-wal", got)
	}
	// The backup files themselves are untouched, and no -shm appeared.
	if b, _ := os.ReadFile(backup + "-wal"); len(b) != len(wal) {
		t.Fatal("backup WAL was modified")
	}
	if _, err := os.Stat(backup + "-shm"); err == nil {
		t.Fatal("restore created a -shm next to the backup")
	}
}

func TestVacuumIntoRemovesFileWhenChmodFails(t *testing.T) {
	path := tempPath(t)
	seeded(t, path, "v")
	dest := filepath.Join(t.TempDir(), "b.db")
	chmodFile = func(string, os.FileMode) error { return errors.New("boom") }
	t.Cleanup(func() { chmodFile = os.Chmod })
	if _, err := Backup(ctxb, path, dest, 0); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Backup error = %v, want the chmod error", err)
	}
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup file left behind: %v", err)
	}
}

func TestMoveAsideRollsBack(t *testing.T) {
	path := tempPath(t)
	db := seeded(t, path, "keep")
	_ = db.Close()
	aside := path + ".aside"
	if err := os.WriteFile(path+"-wal", []byte("w"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A non-empty directory in the way makes the -wal rename fail after the
	// database itself was moved.
	if err := os.MkdirAll(filepath.Join(aside+"-wal", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, _, err := moveAside(path, aside)
	if err == nil || !strings.Contains(err.Error(), aside) {
		t.Fatalf("error = %v, want one naming %s", err, aside)
	}
	if got := settingValue(t, path); got != "keep" {
		t.Fatalf("database not put back: %q", got)
	}
}

func TestModeWarningsCoverBackupsAndMovedAside(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ledger.db")
	for _, n := range []string{".backup-x.db", ".pre-restore-x", ".pre-restore-x-wal", ".backup-x.db-wal", ".restore-x.tmp"} {
		if err := os.WriteFile(path+n, []byte("d"), 0o644); err != nil {
			t.Fatal(err)
		}
		_ = os.Chmod(path+n, 0o644)
	}
	if w := ModeWarnings(path); len(w) != 5 {
		t.Fatalf("warnings = %v, want 5", w)
	}
}

// writeBackupWithWAL writes a backup file and its WAL next to each other and
// returns the backup path.
func writeBackupWithWAL(t *testing.T, value string) string {
	t.Helper()
	dbBytes, wal := liveWAL(t, filepath.Join(t.TempDir(), "live.db"), value)
	backup := filepath.Join(t.TempDir(), "b.db")
	if err := os.WriteFile(backup, dbBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup+"-wal", wal, 0o600); err != nil {
		t.Fatal(err)
	}
	return backup
}

// noRestoreLeftovers fails when a failed restore left its aside or staging
// files next to path.
func noRestoreLeftovers(t *testing.T, path string) {
	t.Helper()
	for _, pat := range []string{".pre-restore-*", ".restore-*.tmp"} {
		if m, _ := filepath.Glob(escapeGlob(path) + pat); len(m) != 0 {
			t.Fatalf("leftover restore files %v", m)
		}
	}
}

func TestBackupSourceReturnsWALStatErrors(t *testing.T) {
	src := filepath.Join(t.TempDir(), "good.db")
	seeded(t, src, "v")
	backup := filepath.Join(t.TempDir(), "b.db")
	if _, err := Backup(ctxb, src, backup, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { statWAL = os.Stat })

	// Only a missing WAL selects the immutable open.
	statWAL = func(p string) (os.FileInfo, error) {
		return nil, &os.PathError{Op: "stat", Path: p, Err: os.ErrNotExist}
	}
	if _, err := VerifyBackup(ctxb, backup); err != nil {
		t.Fatalf("missing WAL: %v", err)
	}
	// Any other stat error must surface instead of silently dropping the WAL.
	statWAL = func(string) (os.FileInfo, error) { return nil, errors.New("wal stat boom") }
	if _, err := VerifyBackup(ctxb, backup); err == nil || !strings.Contains(err.Error(), "wal stat boom") {
		t.Fatalf("VerifyBackup error = %v, want the WAL stat error", err)
	}
}

func TestBackupSourceCopiesIntoGivenDirectory(t *testing.T) {
	backup := writeBackupWithWAL(t, "w")
	dir := t.TempDir()
	if _, _, err := openBackupSource(backup, filepath.Join(dir, "missing")); err == nil {
		t.Fatal("the private copy was made outside the given directory")
	}
	_, cleanup, err := openBackupSource(backup, dir)
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "ledger-backup-*")); len(m) != 1 {
		t.Fatalf("private copy directories = %v, want one next to the database", m)
	}
	cleanup()
	if m, _ := filepath.Glob(filepath.Join(dir, "ledger-backup-*")); len(m) != 0 {
		t.Fatalf("private copy not removed: %v", m)
	}
}

func TestCreateExclusiveRemovesFileWhenChmodFails(t *testing.T) {
	p := filepath.Join(t.TempDir(), "new.db")
	chmodFile = func(string, os.FileMode) error { return errors.New("boom") }
	t.Cleanup(func() { chmodFile = os.Chmod })
	if err := createExclusive(p); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("createExclusive error = %v, want the chmod error", err)
	}
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file left behind: %v", err)
	}
}

func TestRestoreMovesStaleJournalAside(t *testing.T) {
	src := filepath.Join(t.TempDir(), "good.db")
	seeded(t, src, "from-backup")
	backup := filepath.Join(t.TempDir(), "b.db")
	if _, err := Backup(ctxb, src, backup, 0); err != nil {
		t.Fatal(err)
	}
	path := tempPath(t) // no database, only a hot journal
	if err := os.WriteFile(path+"-journal", []byte("hot"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Restore(ctxb, backup, path, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := settingValue(t, path); got != "from-backup" {
		t.Fatalf("restored value = %q, want from-backup", got)
	}
	if _, err := os.Stat(path + "-journal"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale journal still next to the restored database")
	}
	found := false
	for _, p := range res.StaleMoved {
		if strings.HasSuffix(p, "-journal") {
			b, err := os.ReadFile(p)
			found = err == nil && string(b) == "hot"
		}
	}
	if !found {
		t.Fatalf("StaleMoved = %v, want the journal kept aside", res.StaleMoved)
	}
}

func TestRestoreReportsSyncFailureAfterPlacing(t *testing.T) {
	src := filepath.Join(t.TempDir(), "good.db")
	seeded(t, src, "from-backup")
	backup := filepath.Join(t.TempDir(), "b.db")
	if _, err := Backup(ctxb, src, backup, 0); err != nil {
		t.Fatal(err)
	}
	path := tempPath(t)
	db := seeded(t, path, "old")
	_ = db.Close()
	restoreSyncDir = func(string) error { return errors.New("sync boom") }
	t.Cleanup(func() { restoreSyncDir = syncDir })

	res, err := Restore(ctxb, backup, path, true, time.Now())
	if err == nil || !strings.Contains(err.Error(), "restore completed") || !strings.Contains(err.Error(), "sync boom") {
		t.Fatalf("error = %v, want a report that the restore completed but the sync failed", err)
	}
	if !res.Placed || res.MovedAside == "" {
		t.Fatalf("result = %+v, want the restore placed and the aside path reported", res)
	}
	if got := settingValue(t, path); got != "from-backup" {
		t.Fatalf("restored value = %q, want from-backup", got)
	}
	if got := settingValue(t, res.MovedAside); got != "old" {
		t.Fatalf("moved-aside value = %q, want old", got)
	}
}

func TestRestoreRollsBackWhenFinalRenameFails(t *testing.T) {
	src := filepath.Join(t.TempDir(), "good.db")
	seeded(t, src, "from-backup")
	backup := filepath.Join(t.TempDir(), "b.db")
	if _, err := Backup(ctxb, src, backup, 0); err != nil {
		t.Fatal(err)
	}
	renameRestored = func(string, string) error { return errors.New("rename boom") }
	t.Cleanup(func() { renameRestored = os.Rename })
	check := func(t *testing.T, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "rename boom") || !strings.Contains(err.Error(), "put back") {
			t.Fatalf("error = %v, want the rename error and a report that the previous files were put back", err)
		}
	}

	t.Run("previous database", func(t *testing.T) {
		path := tempPath(t)
		db := seeded(t, path, "keep")
		_ = db.Close()
		res, err := Restore(ctxb, backup, path, true, time.Now())
		check(t, err)
		if res.Placed {
			t.Fatal("Placed set although the rename failed")
		}
		if got := settingValue(t, path); got != "keep" {
			t.Fatalf("database not put back: %q", got)
		}
		noRestoreLeftovers(t, path)
	})

	t.Run("stale WAL without database", func(t *testing.T) {
		path := tempPath(t)
		if err := os.WriteFile(path+"-wal", []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Restore(ctxb, backup, path, false, time.Now())
		check(t, err)
		if b, err := os.ReadFile(path + "-wal"); err != nil || string(b) != "stale" {
			t.Fatalf("stale WAL not put back: %q, %v", b, err)
		}
		noRestoreLeftovers(t, path)
	})
}
