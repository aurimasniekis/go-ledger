package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Errors returned by backup and restore. Match with errors.Is.
var (
	// ErrBackupExists means the destination already exists. Backups are never
	// overwritten.
	ErrBackupExists = errors.New("store: backup destination already exists")
	// ErrNoDatabase means the database file to back up does not exist.
	ErrNoDatabase = errors.New("store: database file does not exist")
	// ErrBackupInvalid means a file is not a usable Ledger backup: not a
	// SQLite database, failing integrity or foreign key checks, or without a
	// valid migration ledger.
	ErrBackupInvalid = errors.New("store: not a valid Ledger backup")
	// ErrRestoreTargetExists means restore would replace an existing
	// database and was not told to.
	ErrRestoreTargetExists = errors.New("store: database already exists")
)

// BackupInfo describes a backup file.
type BackupInfo struct {
	Path          string
	SchemaVersion int
	Bytes         int64
}

const backupTimeFormat = "20060102T150405.000000000Z"

// createExclusive creates path with O_EXCL and mode 0600, so it never
// overwrites a file and is owner-only whatever the umask, before any data is
// written to it.
func createExclusive(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: %s", ErrBackupExists, path)
		}
		return fmt.Errorf("store: create %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("store: create %s: %w", path, err)
	}
	// O_CREATE applies the umask only downwards; this makes the mode certain.
	return os.Chmod(path, 0o600)
}

// vacuumInto writes a consistent, compacted copy of the database behind conn
// to dest. dest is created first with O_EXCL and mode 0600 (SQLite accepts an
// existing empty file), and removed again if the copy fails.
func vacuumInto(ctx context.Context, conn *sql.Conn, dest string) error {
	if err := createExclusive(dest); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "VACUUM INTO ?", dest); err != nil {
		_ = os.Remove(dest)
		return fmt.Errorf("store: copy to %s: %w", dest, err)
	}
	return os.Chmod(dest, 0o600)
}

// Backup writes a consistent copy of the live database at dbPath to dest with
// VACUUM INTO (decision D17), then verifies the copy. It is safe to run while
// "ledger serve" uses the database: it reads one snapshot and does not block
// the writer. It does not migrate and never creates dbPath. dest must not
// exist; it is created with O_EXCL and mode 0600 and removed again if the
// copy or its verification fails.
func Backup(ctx context.Context, dbPath, dest string, busy time.Duration) (BackupInfo, error) {
	if err := checkPath(dbPath); err != nil {
		return BackupInfo{}, err
	}
	if _, err := os.Stat(dbPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return BackupInfo{}, fmt.Errorf("%w: %s", ErrNoDatabase, dbPath)
		}
		return BackupInfo{}, fmt.Errorf("store: %w", err)
	}
	if busy <= 0 {
		busy = DefaultBusyTimeout
	}
	same, err := sameFile(dbPath, dest)
	if err != nil {
		return BackupInfo{}, err
	}
	if same {
		return BackupInfo{}, fmt.Errorf("%w: %s is the database itself", ErrBackupExists, dest)
	}
	// Not query_only: SQLite refuses VACUUM INTO on a query-only connection.
	// Nothing in this connection writes to dbPath.
	src, err := sql.Open(driverName, dbPath+"?_busy_timeout="+strconv.FormatInt(busy.Milliseconds(), 10))
	if err != nil {
		return BackupInfo{}, fmt.Errorf("store: open %s: %w", dbPath, err)
	}
	defer func() { _ = src.Close() }()
	src.SetMaxOpenConns(1)
	conn, err := connWithBusyRetry(ctx, src, busy)
	if err != nil {
		return BackupInfo{}, fmt.Errorf("store: open %s: %w", dbPath, err)
	}
	defer func() { _ = conn.Close() }()
	if err := vacuumInto(ctx, conn, dest); err != nil {
		return BackupInfo{}, err
	}
	info, err := VerifyBackup(ctx, dest)
	if err != nil {
		_ = os.Remove(dest)
		return BackupInfo{}, fmt.Errorf("store: backup failed verification: %w", err)
	}
	return info, nil
}

func sameFile(a, b string) (bool, error) {
	fa, err := os.Stat(a)
	if err != nil {
		return false, fmt.Errorf("store: %w", err)
	}
	fb, err := os.Stat(b)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("store: %w", err)
	}
	return os.SameFile(fa, fb), nil
}

// VerifyBackup checks that path is a usable Ledger database copy without
// modifying it: SQLite integrity_check and foreign_key_check pass and the
// schema_migrations ledger is a contiguous prefix of this binary's migrations
// with unchanged checksums. A backup from a newer binary is rejected
// (ErrDatabaseNewer); one from an older binary is valid and is migrated when
// the application next opens it.
func VerifyBackup(ctx context.Context, path string) (BackupInfo, error) {
	return verifyBackup(ctx, path, nil)
}

func verifyBackup(ctx context.Context, path string, known []Migration) (BackupInfo, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return BackupInfo{}, fmt.Errorf("store: %w", err)
	}
	if known == nil {
		if known, err = EmbeddedMigrations(); err != nil {
			return BackupInfo{}, err
		}
	}
	// mode=ro never creates the file or touches it; immutable=1 additionally
	// stops SQLite writing a -shm/-wal or hot-journal recovery next to a copy.
	db, err := sql.Open(driverName, "file:"+escapeURIPath(path)+"?mode=ro&immutable=1")
	if err != nil {
		return BackupInfo{}, fmt.Errorf("%w: %w", ErrBackupInvalid, err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	var res string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&res); err != nil {
		return BackupInfo{}, fmt.Errorf("%w: %w", ErrBackupInvalid, err)
	}
	if res != "ok" {
		return BackupInfo{}, fmt.Errorf("%w: integrity_check: %s", ErrBackupInvalid, res)
	}
	if err := foreignKeyCheck(ctx, db); err != nil {
		return BackupInfo{}, fmt.Errorf("%w: %w", ErrBackupInvalid, err)
	}
	applied, err := readApplied(ctx, db)
	if err != nil {
		return BackupInfo{}, fmt.Errorf("%w: %w", ErrBackupInvalid, err)
	}
	if len(applied) == 0 {
		return BackupInfo{}, fmt.Errorf("%w: no applied migrations", ErrBackupInvalid)
	}
	if err := checkApplied(applied, known); err != nil {
		return BackupInfo{}, err
	}
	return BackupInfo{Path: path, SchemaVersion: applied[len(applied)-1].Version, Bytes: fi.Size()}, nil
}

func escapeURIPath(p string) string {
	r := strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23")
	return r.Replace(p)
}

// RestoreResult reports what Restore did.
type RestoreResult struct {
	// Backup describes the restored backup.
	Backup BackupInfo
	// MovedAside is where the previous database (without its -wal and -shm
	// files, which are kept next to it) now lives; empty when there was none.
	MovedAside string
}

// Restore replaces the database at dbPath with the backup at backupPath.
//
// The caller must hold the instance lock ([LockInstance]) so no Ledger
// process uses the database. Restore verifies the backup first, copies it to a
// new private file next to dbPath, checks the copy, and only then moves any
// existing database (and its -wal/-shm files) aside to
// "<db>.pre-restore-<UTC>" and renames the copy into place; a failure before
// that point leaves the existing database untouched. The previous database is
// kept, never deleted. An existing database is replaced only when replace is
// true (ErrRestoreTargetExists otherwise). The restored database keeps its
// own schema version: an older one is migrated, with a pre-migration backup,
// the next time the application starts.
func Restore(ctx context.Context, backupPath, dbPath string, replace bool, now time.Time) (RestoreResult, error) {
	return restore(ctx, backupPath, dbPath, replace, now, nil)
}

// restore is Restore with the migration set to verify against (nil means the
// embedded one); tests pass their own.
func restore(ctx context.Context, backupPath, dbPath string, replace bool, now time.Time, known []Migration) (RestoreResult, error) {
	if err := checkPath(dbPath); err != nil {
		return RestoreResult{}, err
	}
	info, err := verifyBackup(ctx, backupPath, known)
	if err != nil {
		return RestoreResult{}, err
	}
	if same, err := sameFileIfBoth(backupPath, dbPath); err != nil {
		return RestoreResult{}, err
	} else if same {
		return RestoreResult{}, fmt.Errorf("store: the backup %s is the database itself", backupPath)
	}
	_, statErr := os.Stat(dbPath)
	exists := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return RestoreResult{}, fmt.Errorf("store: %w", statErr)
	}
	if exists && !replace {
		return RestoreResult{}, fmt.Errorf("%w: %s (restore with the replace option to move it aside)", ErrRestoreTargetExists, dbPath)
	}
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return RestoreResult{}, fmt.Errorf("store: %w", err)
	}

	// Copy via VACUUM INTO into a private staging file in the same directory
	// (same file system, so the final rename is atomic).
	staged := fmt.Sprintf("%s.restore-%s.tmp", dbPath, now.UTC().Format(backupTimeFormat))
	src, err := sql.Open(driverName, "file:"+escapeURIPath(backupPath)+"?mode=ro&immutable=1")
	if err != nil {
		return RestoreResult{}, fmt.Errorf("store: open backup: %w", err)
	}
	defer func() { _ = src.Close() }()
	src.SetMaxOpenConns(1)
	conn, err := src.Conn(ctx)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("store: open backup: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if err := vacuumInto(ctx, conn, staged); err != nil {
		return RestoreResult{}, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(staged)
		}
	}()
	if _, err := verifyBackup(ctx, staged, known); err != nil {
		return RestoreResult{}, fmt.Errorf("store: restored copy failed verification: %w", err)
	}

	res := RestoreResult{Backup: info}
	if exists {
		aside := fmt.Sprintf("%s.pre-restore-%s", dbPath, now.UTC().Format(backupTimeFormat))
		if err := moveAside(dbPath, aside); err != nil {
			return RestoreResult{}, err
		}
		res.MovedAside = aside
	}
	if err := os.Rename(staged, dbPath); err != nil {
		return RestoreResult{}, fmt.Errorf("store: put the restored database in place: %w (the previous database is at %s)", err, res.MovedAside)
	}
	cleanup = false
	syncDir(dir)
	return res, nil
}

func sameFileIfBoth(a, b string) (bool, error) {
	if _, err := os.Stat(b); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return sameFile(a, b)
}

// moveAside renames the database and its WAL and shared-memory files to
// aside, aside-wal and aside-shm. A stale -wal or -shm left behind would be
// replayed into the restored database, so they must move with it.
func moveAside(dbPath, aside string) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Rename(dbPath+suffix, aside+suffix); err != nil && (suffix == "" || !errors.Is(err, os.ErrNotExist)) {
			return fmt.Errorf("store: move the existing database aside: %w", err)
		}
	}
	return nil
}

// syncDir flushes a directory entry change; best effort (not available on
// every platform).
func syncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// PreMigrationBackups lists the automatic "<db>.pre-vNNNN-<UTC>.bak" files
// next to dbPath, oldest first. The name embeds the time, so name order is
// age order within a version, and the version order matches too.
func PreMigrationBackups(dbPath string) ([]string, error) {
	m, err := filepath.Glob(escapeGlob(dbPath) + ".pre-v[0-9][0-9][0-9][0-9]-*.bak")
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	sort.Slice(m, func(i, j int) bool { return stamp(m[i]) < stamp(m[j]) })
	return m, nil
}

// stamp is the "<UTC time>.bak" part of a pre-migration backup name.
func stamp(p string) string {
	i := strings.LastIndex(p, ".pre-v")
	if i < 0 || len(p) < i+len(".pre-v0000-") {
		return p
	}
	return p[i+len(".pre-v0000-"):]
}

func escapeGlob(p string) string {
	return strings.NewReplacer("*", "[*]", "?", "[?]", "[", "[[]").Replace(p)
}

// PruneMigrationBackups deletes all but the newest keep automatic
// pre-migration backups of dbPath and returns the deleted paths. It never
// touches backups made with [Backup], restore leftovers or the database.
func PruneMigrationBackups(dbPath string, keep int) ([]string, error) {
	if keep < 0 {
		return nil, errors.New("store: keep must not be negative")
	}
	all, err := PreMigrationBackups(dbPath)
	if err != nil {
		return nil, err
	}
	if len(all) <= keep {
		return nil, nil
	}
	var removed []string
	for _, p := range all[:len(all)-keep] {
		if err := os.Remove(p); err != nil {
			return removed, fmt.Errorf("store: prune: %w", err)
		}
		removed = append(removed, p)
	}
	return removed, nil
}

// ModeWarnings reports file system permissions that expose the database to
// other local users: the database, its -wal/-shm files and the backups next
// to it should not be accessible by group or others, and the directory should
// not be writable by them. It returns nothing on Windows, where mode bits do
// not describe access. Files that do not exist are skipped.
func ModeWarnings(dbPath string) []string {
	if runtime.GOOS == "windows" {
		return nil
	}
	var out []string
	files := []string{dbPath, dbPath + "-wal", dbPath + "-shm", dbPath + ".lock"}
	if b, err := PreMigrationBackups(dbPath); err == nil {
		files = append(files, b...)
	}
	for _, f := range files {
		if fi, err := os.Stat(f); err == nil && fi.Mode().Perm()&0o077 != 0 {
			out = append(out, fmt.Sprintf("%s has mode %04o; financial data should be owner-only (chmod 600)", f, fi.Mode().Perm()))
		}
	}
	if fi, err := os.Stat(filepath.Dir(dbPath)); err == nil && fi.Mode().Perm()&0o022 != 0 {
		out = append(out, fmt.Sprintf("%s has mode %04o; other users could replace or delete the database (chmod 700)", filepath.Dir(dbPath), fi.Mode().Perm()))
	}
	return out
}
