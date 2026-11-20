// Package reporting owns the reporting time zone (decision D5): the one IANA
// zone that decides which calendar date an instant belongs to. It is captured
// once, on the first start against a database, and persisted in the settings
// table; later tasks read it through [Service].
package reporting

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/store"
)

// SettingKey is the settings row that holds the zone name.
const SettingKey = "reporting.timezone"

// ErrNoZone means no zone was configured, none is stored and the host has no
// IANA zone name. Startup is refused; there is no silent UTC fallback.
var ErrNoZone = errors.New("reporting: no reporting time zone can be determined; set reporting.timezone (LEDGER_REPORTING_TIMEZONE) to an IANA name such as Europe/Vilnius or UTC")

// Source says where the zone in force came from at startup.
type Source string

const (
	// SourceStored: an empty config, so the stored zone is used.
	SourceStored Source = "stored"
	// SourceConfig: first start, captured from reporting.timezone.
	SourceConfig Source = "config"
	// SourceHost: first start, captured from the host's IANA zone.
	SourceHost Source = "host"
	// SourceConfigChanged: an explicit value that differs from the stored one
	// replaced it.
	SourceConfigChanged Source = "config-changed"
	// SourceConfigSame: an explicit value equal to the stored one.
	SourceConfigSame Source = "config-same"
)

// Service gives the reporting zone to the rest of the application. It is
// immutable after [Resolve]; changing the zone takes a restart.
type Service struct {
	zone   calendar.Zone
	source Source
}

// Zone returns the reporting zone.
func (s *Service) Zone() calendar.Zone { return s.zone }

// Name returns the IANA name of the reporting zone.
func (s *Service) Name() string { return s.zone.Name() }

// Source reports how the zone was determined at startup.
func (s *Service) Source() Source { return s.source }

// New wraps an already-known zone, for tests of code that needs a Service.
func New(zone calendar.Zone) *Service { return &Service{zone: zone, source: SourceConfig} }

// Options carries what tests replace.
type Options struct {
	// HostZoneName returns the host's IANA zone name. Nil means
	// [calendar.HostZoneName].
	HostZoneName func() (string, bool)
	// Now is the clock for the settings row. Nil means time.Now.
	Now func() time.Time
}

// Resolve determines the reporting zone and persists it.
//
//   - A stored zone is used when explicit is empty.
//   - On a database with no stored zone, explicit is captured, else the host's
//     IANA name; if neither exists it returns [ErrNoZone].
//   - An explicit value that differs from the stored one is applied (and
//     stored), with a warning, because it reclassifies dates.
//
// The read and the write happen in one write transaction, so two processes
// starting together agree on the captured value.
func Resolve(ctx context.Context, db *store.DB, explicit string, log *slog.Logger, opts Options) (*Service, error) {
	if opts.HostZoneName == nil {
		opts.HostZoneName = calendar.HostZoneName
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	var (
		zone   calendar.Zone
		source Source
		prev   string
	)
	err := db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var stored string
		err := tx.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", SettingKey).Scan(&stored)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			stored = ""
		case err != nil:
			return fmt.Errorf("reporting: read stored zone: %w", err)
		}
		// A stored value that no longer loads (hand-edited database, or a
		// build without the zone) must not be silently replaced.
		var storedZone calendar.Zone
		if stored != "" {
			if storedZone, err = calendar.LoadZone(stored); err != nil {
				if explicit == "" {
					return fmt.Errorf("reporting: stored zone %q cannot be loaded; set reporting.timezone to replace it: %w", stored, err)
				}
				storedZone = calendar.Zone{}
			}
		}

		var want string
		switch {
		case explicit != "":
			want = explicit
			switch stored {
			case "":
				source = SourceConfig
			case explicit:
				source = SourceConfigSame
			default:
				source = SourceConfigChanged
			}
		case stored != "":
			zone, source = storedZone, SourceStored
			return nil
		default:
			name, ok := opts.HostZoneName()
			if !ok {
				return ErrNoZone
			}
			want, source = name, SourceHost
		}
		z, err := calendar.LoadZone(want)
		if err != nil {
			return err
		}
		zone, prev = z, stored
		if source == SourceConfigSame {
			return nil
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			SettingKey, want, store.Timestamp{Time: opts.Now()})
		if err != nil {
			return fmt.Errorf("reporting: store zone: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	switch source {
	case SourceConfig, SourceHost:
		log.Info("reporting time zone captured and stored", "zone", zone.Name(), "source", string(source))
	case SourceConfigChanged:
		// TODO(audit-log task): record this change in the audit log once it
		// exists; until then the warning below is the only record.
		log.Warn("reporting time zone changed by configuration; due dates and monthly views are reclassified",
			"event", "reporting_timezone_changed", "from", prev, "to", zone.Name())
	default:
		log.Info("reporting time zone", "zone", zone.Name(), "source", string(source))
	}
	return &Service{zone: zone, source: source}, nil
}
