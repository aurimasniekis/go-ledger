package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
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
	// A file whose mode cannot be fixed is removed rather than left behind.
	if err := chmodFile(path, 0o600); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("store: chmod %s: %w", path, err)
	}
	return nil
}

// chmodFile is os.Chmod; tests replace it to simulate a failure.
var chmodFile = os.Chmod

// statWAL is os.Stat for a "<backup>-wal" file; tests replace it to simulate
// a failure.
var statWAL = os.Stat

// renameRestored puts the restored copy in place; tests replace it to simulate
// a failure of the final rename.
var renameRestored = os.Rename

// restoreSyncDir flushes the database directory after the final rename; tests
// replace it to simulate a failure.
var restoreSyncDir = syncDir

// syncFile flushes a file's contents to stable storage.
func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("store: sync %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("store: sync %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("store: sync %s: %w", path, err)
	}
	return nil
}

// vacuumInto writes a consistent, compacted copy of the database behind conn
// to dest. dest is created first with O_EXCL and mode 0600 (SQLite accepts an
// existing empty file), flushed to stable storage, and removed again if the
// copy, the chmod or the flush fails.
func vacuumInto(ctx context.Context, conn *sql.Conn, dest string) error {
	if err := createExclusive(dest); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "VACUUM INTO ?", dest); err != nil {
		_ = os.Remove(dest)
		return fmt.Errorf("store: copy to %s: %w", dest, err)
	}
	if err := chmodFile(dest, 0o600); err != nil {
		_ = os.Remove(dest)
		return fmt.Errorf("store: chmod %s: %w", dest, err)
	}
	if err := syncFile(dest); err != nil {
		_ = os.Remove(dest)
		return err
	}
	return nil
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
	info, err := verifyBackup(ctx, dest, nil, filepath.Dir(dbPath))
	if err != nil {
		_ = os.Remove(dest)
		return BackupInfo{}, fmt.Errorf("store: backup failed verification: %w", err)
	}
	if err := syncDir(filepath.Dir(dest)); err != nil {
		_ = os.Remove(dest)
		return BackupInfo{}, err
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
	return verifyBackup(ctx, path, nil, "")
}

// verifyBackup is VerifyBackup with the migration set to check against (nil
// means the embedded one) and the directory for a private copy of a backup
// with a WAL ("" means the system temporary directory).
func verifyBackup(ctx context.Context, path string, known []Migration, tmpDir string) (BackupInfo, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return BackupInfo{}, fmt.Errorf("store: %w", err)
	}
	if known == nil {
		if known, err = EmbeddedMigrations(); err != nil {
			return BackupInfo{}, err
		}
	}
	db, cleanup, err := openBackupSource(path, tmpDir)
	if err != nil {
		return BackupInfo{}, fmt.Errorf("%w: %w", ErrBackupInvalid, err)
	}
	defer cleanup()
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

// openBackupSource opens a backup file for reading, one connection.
//
// Normally it opens the file read-only and immutable: mode=ro never creates
// the file or touches it, and immutable=1 stops SQLite writing a -shm/-wal or
// hot-journal recovery next to a copy. Immutable ignores a -wal file, so when
// "<path>-wal" exists and is not empty (a backup that was a live database,
// such as a .pre-restore-* file) the file and its WAL are copied to a private
// temporary directory inside tmpDir ("" means the system temporary directory)
// and opened normally there: SQLite replays the WAL, and the original files
// are never modified. The returned function closes the database and removes
// the temporary directory. Only a missing or empty WAL selects the immutable
// open; any other error reading the WAL is returned, because falling back
// would silently drop its data.
func openBackupSource(path, tmpDir string) (*sql.DB, func(), error) {
	open := func(dsn string, cleanup func()) (*sql.DB, func(), error) {
		db, err := sql.Open(driverName, dsn)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		db.SetMaxOpenConns(1)
		return db, func() { _ = db.Close(); cleanup() }, nil
	}
	fi, err := statWAL(path + "-wal")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("store: %w", err)
	}
	if err != nil || fi.Size() == 0 {
		return open("file:"+escapeURIPath(path)+"?mode=ro&immutable=1", func() {})
	}
	tmp, err := os.MkdirTemp(tmpDir, "ledger-backup-")
	if err != nil {
		return nil, nil, fmt.Errorf("store: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	cp := filepath.Join(tmp, "backup.db")
	if err := copyPrivate(path, cp); err != nil {
		cleanup()
		return nil, nil, err
	}
	if err := copyPrivate(path+"-wal", cp+"-wal"); err != nil {
		cleanup()
		return nil, nil, err
	}
	return open("file:"+escapeURIPath(cp), cleanup)
}

// copyPrivate copies src to a new file dst with mode 0600.
func copyPrivate(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("store: copy %s: %w", src, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("store: copy %s: %w", src, err)
	}
	return nil
}

func escapeURIPath(p string) string {
	r := strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23")
	return r.Replace(p)
}

// RestoreResult reports what Restore did.
type RestoreResult struct {
	// Backup describes the restored backup.
	Backup BackupInfo
	// MovedAside is where the previous database now lives, with its -wal,
	// -shm and -journal files kept next to it as aside-wal, aside-shm and
	// aside-journal. It is empty when there was no database to move aside.
	MovedAside string
	// StaleMoved lists the stale -wal, -shm and -journal files that had no
	// database next to them and were moved aside to the restore's aside name.
	// It is empty when a database was moved aside or there were no such files.
	StaleMoved []string
	// Placed is true once the restored database is in place at the database
	// path. It can be true together with an error (the directory sync failed).
	Placed bool
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
	// The directory is created first: a private copy of the backup, when it
	// has a WAL, is made next to the database.
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return RestoreResult{}, fmt.Errorf("store: %w", err)
	}
	info, err := verifyBackup(ctx, backupPath, known, dir)
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
	// A -wal, -shm or -journal without a database is just as dangerous: SQLite
	// would replay a WAL, or roll a hot journal back, into whatever is renamed
	// into place.
	leftovers := false
	for _, s := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(dbPath + s); err == nil {
			leftovers = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return RestoreResult{}, fmt.Errorf("store: %w", err)
		}
	}
	if exists && !replace {
		return RestoreResult{}, fmt.Errorf("%w: %s (restore with the replace option to move it aside)", ErrRestoreTargetExists, dbPath)
	}
	// Copy via VACUUM INTO into a private staging file in the same directory
	// (same file system, so the final rename is atomic).
	staged := fmt.Sprintf("%s.restore-%s.tmp", dbPath, now.UTC().Format(backupTimeFormat))
	src, closeSrc, err := openBackupSource(backupPath, dir)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("store: open backup: %w", err)
	}
	defer closeSrc()
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
	if _, err := verifyBackup(ctx, staged, known, dir); err != nil {
		return RestoreResult{}, fmt.Errorf("store: restored copy failed verification: %w", err)
	}

	res := RestoreResult{Backup: info}
	aside := fmt.Sprintf("%s.pre-restore-%s", dbPath, now.UTC().Format(backupTimeFormat))
	var undo func() error
	if exists || leftovers {
		moved, u, err := moveAside(dbPath, aside)
		if err != nil {
			return RestoreResult{}, err
		}
		undo = u
		if exists {
			res.MovedAside = aside
		} else {
			res.StaleMoved = moved
		}
	}
	if err := renameRestored(staged, dbPath); err != nil {
		note := ""
		if undo != nil {
			note = rollBack(undo, exists, aside)
		}
		return RestoreResult{}, fmt.Errorf("store: put the restored database in place: %w%s", err, note)
	}
	cleanup = false
	res.Placed = true
	if err := restoreSyncDir(dir); err != nil {
		return res, fmt.Errorf("store: restore completed, but the sync of directory %s failed (the restored database may not survive a crash until the directory is synced): %w", dir, err)
	}
	return res, nil
}

// rollBack puts back the files moved aside by a restore whose final rename
// failed, and describes the outcome for the error message.
func rollBack(undo func() error, hadDatabase bool, aside string) string {
	if err := undo(); err != nil {
		return fmt.Sprintf("; putting the previous files back failed (%v); check the files named %s*", err, aside)
	}
	if hadDatabase {
		return "; the previous database was put back"
	}
	return "; the stale WAL, SHM and journal files were put back"
}

func sameFileIfBoth(a, b string) (bool, error) {
	if _, err := os.Stat(b); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return sameFile(a, b)
}

// moveAside renames the database and its -wal, -shm and -journal files to
// aside, aside-wal, aside-shm and aside-journal, each only if it exists (a
// stale sidecar file may exist without the database). A stale -wal or -shm
// left behind would be replayed into the restored database, and a stale
// -journal could be rolled back into it, so they must move with it. If a move
// fails, the files already moved are put back and the error names aside. On
// success it returns the new paths and a function that puts everything back.
func moveAside(dbPath, aside string) ([]string, func() error, error) {
	var moved []string // suffixes already moved, in order
	var dests []string
	undo := func() error {
		var errs []error
		for i := len(moved) - 1; i >= 0; i-- {
			if err := os.Rename(aside+moved[i], dbPath+moved[i]); err != nil {
				errs = append(errs, err)
			}
		}
		moved = nil
		return errors.Join(errs...)
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if err := os.Rename(dbPath+suffix, aside+suffix); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			_ = undo()
			return nil, nil, fmt.Errorf("store: move the existing database aside to %s: %w (files already moved were put back where possible; check %s)", aside, err, aside)
		}
		moved = append(moved, suffix)
		dests = append(dests, aside+suffix)
	}
	return dests, undo, nil
}

// syncDir flushes a directory entry change. Errors are returned except on
// Windows, where directories cannot be synced.
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("store: sync directory %s: %w", dir, err)
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return fmt.Errorf("store: sync directory %s: %w", dir, err)
	}
	return d.Close()
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
// other local users: the database, its -wal/-shm/-journal files and the
// backups and restore files next to it should not be accessible by group or
// others, and the directory should not be writable by them. It returns nothing on Windows, where mode bits do
// not describe access. Files that do not exist are skipped.
func ModeWarnings(dbPath string) []string {
	if runtime.GOOS == "windows" {
		return nil
	}
	var out []string
	files := []string{dbPath, dbPath + "-wal", dbPath + "-shm", dbPath + "-journal", dbPath + ".lock"}
	if b, err := PreMigrationBackups(dbPath); err == nil {
		files = append(files, b...)
	}
	// Backups made with "ledger backup", moved-aside databases (each with its
	// -wal, -shm and -journal files) and restore staging files left by a crash.
	for _, pat := range []string{".backup-*.db*", ".pre-restore-*", ".restore-*.tmp"} {
		if m, err := filepath.Glob(escapeGlob(dbPath) + pat); err == nil {
			files = append(files, m...)
		}
	}
	seen := map[string]bool{}
	for _, f := range files {
		if seen[f] {
			continue
		}
		seen[f] = true
		if fi, err := os.Stat(f); err == nil && fi.Mode().Perm()&0o077 != 0 {
			out = append(out, fmt.Sprintf("%s has mode %04o; financial data should be owner-only (chmod 600)", f, fi.Mode().Perm()))
		}
	}
	if fi, err := os.Stat(filepath.Dir(dbPath)); err == nil && fi.Mode().Perm()&0o022 != 0 {
		out = append(out, fmt.Sprintf("%s has mode %04o; other users could replace or delete the database (chmod 700)", filepath.Dir(dbPath), fi.Mode().Perm()))
	}
	return out
}
