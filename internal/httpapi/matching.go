package httpapi

import (
	"context"
	"net/http"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/automatch"
)

// PaymentMatchingRunsPath starts an automatic payment-matching run (task X2,
// ADR-0039).
const PaymentMatchingRunsPath = APIPrefix + "/payment-matching/runs"

// MatchingRunner runs the automatic payment matching for an actor.
// *automatch.Engine implements it.
type MatchingRunner interface {
	Request(ctx context.Context, actor audit.Actor) (automatch.Result, error)
}

// registerMatching registers POST /payment-matching/runs. It runs the same
// engine as the run after every bank import; matches it writes are the
// system's (held to the automatic rules), and the run itself is audited as
// payment_matching.run for the caller. Like the matches it writes, it needs
// commitments:write.
func registerMatching(s *Server, run MatchingRunner) {
	s.HandleAPI("POST "+PaymentMatchingRunsPath, Requires(grantCommitmentsWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No options yet: the body is empty or an empty object.
		doc, err := decodeObject(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		for k := range doc {
			WriteError(w, r, Validation(k, "unknown field "+k))
			return
		}
		res, err := run.Request(r.Context(), actor(r))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, res)
	}))
}
