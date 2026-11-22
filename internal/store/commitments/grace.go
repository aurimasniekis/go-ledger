package commitments

import (
	"context"

	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/payments"
)

// Grace returns the [payments.GraceFunc] backed by the commitments' recorded
// leeway (task DF2, ADR-0052). It reads the commitment of each occurrence it is
// asked about, so an edit shows at once. When the commitment cannot be read
// (deleted, or a database error) it reports nothing recorded: that can only
// make an overdue occurrence look like it has no known grace, never hide the
// obligation or invent a date.
func (s *Store) Grace() payments.GraceFunc {
	return func(o occurrences.Occurrence) payments.Grace {
		c, err := s.Get(context.Background(), o.CommitmentID)
		if err != nil || c.Leeway == nil {
			return payments.Grace{}
		}
		g := payments.Grace{Recorded: true, Kind: c.Leeway.Kind}
		if until, ok := c.Leeway.GraceUntil(o.DueDate); ok {
			g.Until = &until
		}
		return g
	}
}
