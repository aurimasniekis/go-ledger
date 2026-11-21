package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/store"
)

// opsEnv carries what tests replace for the backup and restore commands.
type opsEnv struct {
	Options config.Options
	Now     func() time.Time
	// Stdin is where "ledger auth set-password" reads the password.
	Stdin io.Reader
}

func (e opsEnv) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// loadDBConfig parses the shared configuration flags and loads the
// configuration. It returns exit code 0 and a nil config only for -h.
func loadDBConfig(fs *flag.FlagSet, fl *config.Flags, args []string, stderr io.Writer, env opsEnv) (cfg *config.Config, code int) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, 0
		}
		return nil, 2
	}
	opts := fl.Options()
	opts.LookupEnv, opts.ReadFile, opts.Environ = env.Options.LookupEnv, env.Options.ReadFile, env.Options.Environ
	res, err := config.Load(opts)
	if err != nil {
		var ce *config.Error
		if errors.As(err, &ce) {
			warnUnknown(stderr, ce.UnknownEnv)
		}
		say(stderr, "ledger: "+err.Error())
		return nil, 1
	}
	return &res.Config, -1
}

func ensureDir(dbPath string) error {
	return os.MkdirAll(filepath.Dir(dbPath), 0o700)
}

// runBackup implements "ledger backup [flags]" and "ledger backup prune".
func runBackup(args []string, stdout, stderr io.Writer, env opsEnv) int {
	if len(args) > 0 && args[0] == "prune" {
		return runBackupPrune(args[1:], stdout, stderr, env)
	}
	fs := flag.NewFlagSet("ledger backup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fl := config.RegisterFlags(fs)
	output := fs.String("output", "", "backup file to create (must not exist); default \"<database>.backup-<UTC time>.db\"")
	cfg, code := loadDBConfig(fs, fl, args, stderr, env)
	if cfg == nil {
		return code
	}
	if fs.NArg() > 0 {
		say(stderr, fmt.Sprintf("ledger backup: unexpected argument %q", fs.Arg(0)))
		return 2
	}
	dest := *output
	if dest == "" {
		dest = fmt.Sprintf("%s.backup-%s.db", cfg.Database.Path, env.now().UTC().Format("20060102T150405Z"))
	}
	info, err := store.Backup(context.Background(), cfg.Database.Path, dest, cfg.Database.BusyTimeout)
	if err != nil {
		say(stderr, "ledger backup: "+err.Error())
		return 1
	}
	say(stdout, fmt.Sprintf("backup written: %s (schema version %04d, %d bytes, mode 0600, verified)", info.Path, info.SchemaVersion, info.Bytes))
	return 0
}

func runBackupPrune(args []string, stdout, stderr io.Writer, env opsEnv) int {
	fs := flag.NewFlagSet("ledger backup prune", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fl := config.RegisterFlags(fs)
	keep := fs.Int("keep", -1, "number of newest automatic pre-migration backups to keep (required)")
	cfg, code := loadDBConfig(fs, fl, args, stderr, env)
	if cfg == nil {
		return code
	}
	if *keep < 0 || fs.NArg() > 0 {
		say(stderr, "usage: ledger backup prune --keep N   (deletes older <database>.pre-vNNNN-<time>.bak files only)")
		return 2
	}
	removed, err := store.PruneMigrationBackups(cfg.Database.Path, *keep)
	for _, p := range removed {
		say(stdout, "deleted "+p)
	}
	if err != nil {
		say(stderr, "ledger backup prune: "+err.Error())
		return 1
	}
	say(stdout, fmt.Sprintf("%d pre-migration backup(s) deleted, newest %d kept", len(removed), *keep))
	return 0
}

// runRestore implements "ledger restore [flags] <backup-file>".
func runRestore(args []string, stdout, stderr io.Writer, env opsEnv) int {
	fs := flag.NewFlagSet("ledger restore", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fl := config.RegisterFlags(fs)
	force := fs.Bool("force", false, "replace an existing database: it is moved aside to <database>.pre-restore-<time>, never deleted")
	cfg, code := loadDBConfig(fs, fl, args, stderr, env)
	if cfg == nil {
		return code
	}
	if fs.NArg() != 1 {
		say(stderr, "usage: ledger restore [flags] <backup-file>   (flags go before the file; stop ledger serve first)")
		return 2
	}
	backup, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		say(stderr, "ledger restore: "+err.Error())
		return 1
	}
	if err := ensureDir(cfg.Database.Path); err != nil {
		say(stderr, "ledger restore: "+err.Error())
		return 1
	}
	lock, err := store.LockInstance(cfg.Database.Path)
	if err != nil {
		say(stderr, "ledger restore: "+err.Error()+"; stop it before restoring")
		return 1
	}
	defer func() { _ = lock.Close() }()
	res, err := store.Restore(context.Background(), backup, cfg.Database.Path, *force, env.now())
	if err != nil && !res.Placed {
		say(stderr, "ledger restore: "+err.Error())
		if errors.Is(err, store.ErrRestoreTargetExists) {
			say(stderr, "ledger restore: re-run with --force to move the current database aside and restore")
		}
		return 1
	}
	// The restored database is in place. A later error (the directory sync)
	// still gets the report of what was moved aside, then exits non-zero.
	say(stdout, fmt.Sprintf("restored %s (schema version %04d) to %s", res.Backup.Path, res.Backup.SchemaVersion, cfg.Database.Path))
	if res.MovedAside != "" {
		say(stdout, "previous database kept at "+res.MovedAside)
	}
	if len(res.StaleMoved) > 0 {
		say(stdout, "stale WAL/SHM/journal moved to "+strings.Join(res.StaleMoved, ", ")+" (there was no database)")
	}
	if err != nil {
		say(stderr, "ledger restore: "+err.Error())
		return 1
	}
	say(stdout, "start ledger serve to apply any newer migrations (an automatic pre-migration backup is written first)")
	return 0
}
