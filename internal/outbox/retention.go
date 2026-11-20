package outbox

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/logging"
	"github.com/aurimasniekis/go-ledger/internal/store"
)

// RetentionJobName is the "job" log field and metric label of the retention job.
const RetentionJobName = "outbox-retention"

// expirePending marks pending rows created at or before now-maxAge as failed.
func expirePending(ctx context.Context, db *store.DB, now time.Time, maxAge time.Duration, reason string) (n int64, err error) {
	err = db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, `UPDATE outbox SET state = 'failed', failed_at = ?,
			last_error = CASE WHEN last_error = '' THEN ? ELSE last_error END
			WHERE state = 'pending' AND created_at <= ?`,
			store.Timestamp{Time: now}, reason, store.Timestamp{Time: now.Add(-maxAge)})
		if err != nil {
			return err
		}
		n, err = r.RowsAffected()
		return err
	})
	return n, err
}

// RetentionConfig configures a [Retention] job. DB is required.
type RetentionConfig struct {
	DB  *store.DB
	Log *slog.Logger
	// Meter receives the job's measurements; nil discards them.
	Meter Meter
	// Publishing says whether a publisher runs. When false, rows left pending
	// from an earlier enabled period are failed at once ("publishing disabled"),
	// so enabling later never replays them (docs/events.md); when true, pending
	// rows are failed after MaxAge, which the publisher also does on every pass.
	Publishing bool
	MaxAge     time.Duration // retry window; default [MaxRetryAge]
	Every      time.Duration // pass interval; default 1h
	Now        func() time.Time
}

// Retention applies the outbox retention rules whether or not publishing is
// enabled: expiry of pending rows and purge of published (7 days) and failed
// (30 days) rows (decision D17).
type Retention struct {
	cfg   RetentionConfig
	log   *slog.Logger
	meter Meter
}

// NewRetention fills in defaults.
func NewRetention(cfg RetentionConfig) *Retention {
	if cfg.MaxAge <= 0 {
		cfg.MaxAge = MaxRetryAge
	}
	if cfg.Every <= 0 {
		cfg.Every = time.Hour
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	r := &Retention{cfg: cfg, log: cfg.Log, meter: cfg.Meter}
	if r.log == nil {
		r.log = slog.New(slog.DiscardHandler)
	}
	if r.meter == nil {
		r.meter = nopMeter{}
	}
	return r
}

// Run works until ctx is cancelled; the first pass runs at once.
func (r *Retention) Run(ctx context.Context) {
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		r.Pass(ctx)
		t.Reset(r.cfg.Every)
	}
}

// Pass runs one expiry and purge pass.
func (r *Retention) Pass(ctx context.Context) {
	start := time.Now()
	jl, _ := logging.Job(r.log, RetentionJobName)
	now := r.cfg.Now().UTC()
	ok := true
	maxAge, reason := r.cfg.MaxAge, "not delivered within the retry window"
	if !r.cfg.Publishing {
		maxAge, reason = 0, "publishing disabled"
	}
	n, err := expirePending(ctx, r.cfg.DB, now, maxAge, reason)
	for i := int64(0); i < n; i++ {
		r.meter.EventFailed()
	}
	switch {
	case err != nil:
		ok = false
		if ctx.Err() == nil {
			jl.Error("expiring old events failed", "error", sanitize(err))
		}
	case n > 0:
		jl.Warn("pending events marked failed", "count", n, "reason", reason)
	}
	if ctx.Err() != nil {
		return
	}
	var res PurgeResult
	err = r.cfg.DB.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) (err error) {
		res, err = Purge(ctx, tx, now)
		return err
	})
	if err != nil {
		ok = false
		if ctx.Err() == nil {
			jl.Error("purging old events failed", "error", sanitize(err))
		}
	} else if res.Published+res.Failed > 0 {
		jl.Info("old events purged", "published", res.Published, "failed", res.Failed)
	}
	r.meter.JobDone(RetentionJobName, ok, time.Since(start))
}
