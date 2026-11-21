-- Soft-delete manual payments (C3c, decision D26-3a, ADR-0031 point 3). Deleting
-- a manual payment keeps its row: deleted_at and deleted_by mark it, and it
-- stops counting as payment evidence and drops out of lists by default (code
-- filters on deleted_at IS NULL). deleted_by is the actor that deleted it
-- ("user:<id>", "token:<id>" or "system"). The row still refers to its
-- occurrence with ON DELETE RESTRICT, so the occurrence keeps its payment
-- history and cannot be removed with it, but it does not make the occurrence
-- paid: terms edits still apply. Existing rows are not deleted.
ALTER TABLE manual_payments ADD COLUMN deleted_at TEXT CHECK (deleted_at IS NULL OR (length(deleted_at) = 30 AND deleted_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'));
ALTER TABLE manual_payments ADD COLUMN deleted_by TEXT CHECK (deleted_by IS NULL OR length(deleted_by) BETWEEN 1 AND 100);

CREATE TRIGGER manual_payments_deleted_pair
BEFORE UPDATE ON manual_payments
WHEN (NEW.deleted_at IS NULL) <> (NEW.deleted_by IS NULL) OR (OLD.deleted_at IS NOT NULL AND NEW.deleted_at IS NOT OLD.deleted_at)
BEGIN
    SELECT RAISE(ABORT, 'a manual payment is deleted with both deleted_at and deleted_by, and stays deleted');
END;

-- A manual payment is never removed outright: deleting is marking.
CREATE TRIGGER manual_payments_kept
BEFORE DELETE ON manual_payments
BEGIN
    SELECT RAISE(ABORT, 'a manual payment is never removed, only marked deleted');
END;

-- Cancelled occurrences and archived commitments (C3f, decision D31,
-- ADR-0027, ADR-0031). When removing an occurrence is intended (a start or end
-- date correction, an edit that no longer schedules the month, a commitment
-- delete) but a soft-deleted manual payment still refers to it, the occurrence
-- is not deleted: cancelled_at and cancelled_reason mark it. It keeps its
-- payment history, but leaves the monthly totals, the overdue counts and the
-- outlook, and takes no new payment or match. cancelled_reason is one of
-- out_of_range, terms_change, commitment_deleted. A later edit that schedules
-- the month again revives the row (both columns are cleared).
ALTER TABLE commitment_occurrences ADD COLUMN cancelled_at TEXT CHECK (cancelled_at IS NULL OR (length(cancelled_at) = 30 AND cancelled_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'));
ALTER TABLE commitment_occurrences ADD COLUMN cancelled_reason TEXT CHECK (cancelled_reason IS NULL OR cancelled_reason IN ('out_of_range', 'terms_change', 'commitment_deleted'));

CREATE TRIGGER commitment_occurrences_cancelled_pair
BEFORE UPDATE OF cancelled_at, cancelled_reason ON commitment_occurrences
WHEN (NEW.cancelled_at IS NULL) <> (NEW.cancelled_reason IS NULL)
BEGIN
    SELECT RAISE(ABORT, 'an occurrence is cancelled with both cancelled_at and cancelled_reason');
END;

CREATE TRIGGER manual_payments_not_on_cancelled
BEFORE INSERT ON manual_payments
WHEN (SELECT cancelled_at FROM commitment_occurrences WHERE id = NEW.occurrence_id) IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'a cancelled occurrence takes no new payment');
END;

CREATE TRIGGER payment_matches_not_on_cancelled
BEFORE INSERT ON payment_matches
WHEN (SELECT cancelled_at FROM commitment_occurrences WHERE id = NEW.occurrence_id) IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'a cancelled occurrence takes no new payment match');
END;

-- A commitment is deleted outright when no occurrence refers to it any more.
-- When cancelled occurrences keep payment history, the commitment row is kept
-- and marked deleted_at (archived): every read, list, edit and delete of
-- commitments treats it as gone, and no occurrences are generated for it.
ALTER TABLE commitments ADD COLUMN deleted_at TEXT CHECK (deleted_at IS NULL OR (length(deleted_at) = 30 AND deleted_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'));
