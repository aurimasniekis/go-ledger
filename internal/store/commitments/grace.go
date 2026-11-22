package commitments

import (
	"context"
	"log/slog"

	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/payments"
)

// Grace returns the [payments.GraceFunc] backed by the commitments' recorded
// leeway (task DF2, ADR-0052). It reads the commitment of each occurrence it is
// asked about, so an edit shows at once, and the read uses the caller's ctx.
// When the commitment cannot be read (deleted, or a database error) it reports
// nothing recorded: that can only make an overdue occurrence look like it has
// no known grace, never hide the obligation or invent a date. The error is
// logged with the identifiers only, never the leeway text.
func (s *Store) Grace() payments.GraceFunc {
	return func(ctx context.Context, o occurrences.Occurrence) payments.Grace {
		c, err := s.Get(ctx, o.CommitmentID)
		if err != nil {
			slog.ErrorContext(ctx, "leeway not read: the commitment of an overdue occurrence could not be loaded; it is reported as not recorded",
				"occurrence_id", o.ID, "commitment_id", o.CommitmentID, "error", err)
			return payments.Grace{}
		}
		if c.Leeway == nil {
			return payments.Grace{}
		}
		g := payments.Grace{Recorded: true, Kind: c.Leeway.Kind}
		if until, ok := c.Leeway.GraceUntil(o.DueDate); ok {
			g.Until = &until
		}
		return g
	}
}
