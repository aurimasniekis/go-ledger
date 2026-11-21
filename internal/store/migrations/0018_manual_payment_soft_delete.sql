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
